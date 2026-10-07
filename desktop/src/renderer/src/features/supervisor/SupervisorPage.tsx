/*
Copyright 2026 DoorDash, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

/**
 * The Supervisor page: one durable conversation with the server's
 * supervisor, read through the shared conversation transcript (ascending,
 * pinned to the bottom) above the shared description composer.
 *
 * The page snapshots the read model and the newest transcript page, then
 * follows the supervisor stream: committed `record`s merge by seq, `delta`s
 * build a provisional assistant row keyed by stream message id until the
 * matching record commits, `state` replaces the read model, `request` adds a
 * pending card ahead of its state change, and `reset` re-snapshots. Pending
 * permissions and questions sit at the bottom of the transcript in the
 * shared attention card and question turn, and are answered through the
 * existing attention submit path.
 *
 * A routed compose request ("Explain in chat", ⌘⇧M) focuses the composer
 * and may draft text into it unsent, carrying an error reference that rides
 * hidden with the next send.
 */
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type Dispatch,
  type SetStateAction,
} from 'react';
import type {
  AttentionItem,
  CanonicalError,
  ErrorReference,
  SupervisorPendingRequest,
  SupervisorRecord,
  SupervisorSettingsRequest,
  SupervisorSettings,
  SupervisorState,
} from '../../../../shared/ipc';
import { ErrorSurface } from '../../components/ErrorSurface';
import { StopIcon } from '../../components/icons';
import { retryAction } from '../../hooks';
import { parseIpcError } from '../../wizard/ipcError';
import {
  AttentionDetail,
  attentionError,
  runAttentionSubmit,
  type AttentionAction,
  type AttentionDrafts,
  type AttentionSubmitOptions,
} from '../AttentionInbox';
import { useModelCatalogue } from '../ConfigEditor';
import {
  DescriptionComposer,
  runComposerSlashCommand,
  type ComposerSlashCommand,
  type DescriptionComposerHandle,
} from '../DescriptionComposer';
import {
  QuestionComposer,
  QuestionConversationTurn,
  questionAnswersRequest,
} from '../QuestionTurn';
import { ConversationTranscript } from '../transcript/ConversationTranscript';
import { SupervisorModelChip } from './SupervisorModelChip';
import {
  buildSupervisorConversation,
  findCatalogueModel,
  isPausedByRestart,
  isTurnActive,
  mergeRecords,
  settingsChosen,
  SUPERVISOR_COPY,
  supervisorStatusLine,
  type ProvisionalReply,
} from './supervisorModel';
import { SUPERVISOR_MESSAGE_MAX_CHARS } from '../../../../shared/ipc';

const NO_REPOSITORIES: readonly never[] = [];
const NO_FILES: readonly never[] = [];
const ignoreUpdate = (): void => {};

/**
 * A routed ask to focus the composer, optionally drafting `draft` into it
 * with the error reference the drafted message carries. Handled once per id.
 */
export interface SupervisorComposeRequest {
  id: number;
  draft?: string;
  errorReference?: ErrorReference;
}

export interface SupervisorPageProps {
  attentionDrafts: AttentionDrafts;
  setAttentionDrafts: Dispatch<SetStateAction<AttentionDrafts>>;
  /** Refreshes the shell-wide attention snapshot after an answer. */
  refreshAttention(): Promise<AttentionItem[]>;
  /** Owned by the shell: the newest routed compose request, until handled. */
  composeRequest?: SupervisorComposeRequest | null;
  onComposeRequestHandled?(): void;
}

/** Sets an empty composer to the draft; otherwise appends it after a blank line. */
function appendDraft(current: string, draft: string): string {
  return current.trim() === '' ? draft : `${current.trimEnd()}\n\n${draft}`;
}

function userRecordText(record: SupervisorRecord): string | undefined {
  if (record.kind !== 'user') return undefined;
  return record.messages
    .map((message) => message.text ?? '')
    .join('')
    .trim();
}

export function SupervisorPage({
  attentionDrafts,
  setAttentionDrafts,
  refreshAttention,
  composeRequest = null,
  onComposeRequestHandled,
}: SupervisorPageProps) {
  const catalogue = useModelCatalogue();
  const [state, setState] = useState<SupervisorState | null>(null);
  const [stateError, setStateError] = useState<CanonicalError | null>(null);
  const [records, setRecords] = useState<SupervisorRecord[]>([]);
  const [transcriptError, setTranscriptError] = useState<CanonicalError | null>(null);
  const [provisional, setProvisional] = useState<ReadonlyMap<string, ProvisionalReply>>(
    () => new Map(),
  );
  const [draft, setDraft] = useState('');
  // The hidden error reference attached to the pending draft: set by a
  // routed explain (a later one replaces it), cleared by a successful send
  // or by the person emptying the composer.
  const [errorReference, setErrorReference] = useState<ErrorReference | null>(null);
  const [focusToken, setFocusToken] = useState(0);
  const composerRef = useRef<DescriptionComposerHandle | null>(null);
  const handledComposeRequest = useRef<number | null>(null);
  const [optimistic, setOptimistic] = useState<string | null>(null);
  const [sending, setSending] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [inlineError, setInlineError] = useState<CanonicalError | null>(null);
  const [openSection, setOpenSection] = useState<{
    section: 'model' | 'effort';
    filter: string;
    token: number;
  } | null>(null);
  const [attentionBusy, setAttentionBusy] = useState<string | null>(null);
  // Requests answered from this page: hidden at once, before the server's
  // next state confirms they are gone.
  const [answered, setAnswered] = useState<ReadonlySet<string>>(() => new Set());
  const [pinToBottom, setPinToBottom] = useState(0);
  const loadRequest = useRef(0);
  const conversationIdRef = useRef<string | null>(null);
  conversationIdRef.current = state?.conversationId ?? null;
  // Counts the stream's state events. A fetched read model is applied only
  // if no stream state arrived while it was in flight: the stream's is newer,
  // and a stale "running" snapshot landing after the turn's idle event would
  // otherwise stick, since nothing follows an idle turn to correct it.
  const streamStateTick = useRef(0);

  const refreshState = useCallback(async () => {
    const tick = streamStateTick.current;
    try {
      const next = await window.agentico.getSupervisorState();
      if (tick !== streamStateTick.current) return;
      setState(next);
      setStateError(null);
    } catch {
      // The stream keeps the read model current; a missed refresh is not fatal.
    }
  }, []);

  /**
   * Snapshots the read model and the newest transcript page. `replace`
   * (after a stream reset) discards what the page held; the first load
   * merges, so records that streamed in while it was in flight survive.
   */
  const load = useCallback(async (replace: boolean) => {
    const request = ++loadRequest.current;
    const tick = streamStateTick.current;
    const [stateResult, pageResult] = await Promise.allSettled([
      window.agentico.getSupervisorState(),
      window.agentico.getSupervisorTranscript({}),
    ]);
    if (request !== loadRequest.current) return;
    if (stateResult.status === 'fulfilled') {
      if (tick === streamStateTick.current) setState(stateResult.value);
      setStateError(null);
    } else {
      setStateError(parseIpcError(stateResult.reason));
    }
    if (pageResult.status === 'fulfilled') {
      setRecords((current) => mergeRecords(replace ? [] : current, pageResult.value.items));
      setTranscriptError(null);
      if (replace) setProvisional(new Map());
    } else {
      setTranscriptError(parseIpcError(pageResult.reason));
    }
  }, []);

  useEffect(() => {
    const unsubscribe = window.agentico.onSupervisorEvent((event) => {
      if (event.type === 'reset') {
        void load(true);
        return;
      }
      if (event.type === 'stream-status') return;
      const conversationId = conversationIdRef.current;
      if (conversationId !== null && event.conversationId !== conversationId) return;
      if (event.type === 'record') {
        const { record } = event;
        setRecords((current) => mergeRecords(current, [record]));
        if (record.streamMessageId !== undefined) {
          const committed = record.streamMessageId;
          setProvisional((current) => {
            if (!current.has(committed)) return current;
            const next = new Map(current);
            next.delete(committed);
            return next;
          });
        }
        const text = userRecordText(record);
        if (text !== undefined) setOptimistic((current) => (current === text ? null : current));
      } else if (event.type === 'delta') {
        const { delta } = event;
        setProvisional((current) => {
          const known = current.get(delta.streamMessageId);
          const chunks = new Map(known?.chunks ?? []);
          chunks.set(delta.chunkIndex, delta.text);
          const next = new Map(current);
          next.set(delta.streamMessageId, {
            streamMessageId: delta.streamMessageId,
            generation: event.generation,
            chunks,
          });
          return next;
        });
      } else if (event.type === 'state') {
        streamStateTick.current += 1;
        setState(event.state);
        setStateError(null);
        // A turn that ended without committing its streamed text (an
        // interrupt) leaves nothing provisional behind.
        if (!isTurnActive(event.state.lifecycle)) setProvisional(new Map());
      } else {
        const { request } = event;
        setState((current) =>
          current === null || current.pendingRequests.some((item) => item.id === request.id)
            ? current
            : { ...current, pendingRequests: [...current.pendingRequests, request] },
        );
      }
    });
    void load(false);
    return () => {
      loadRequest.current += 1;
      unsubscribe();
    };
  }, [load]);

  // Each routed compose request is handled once by its id, so a re-render
  // carrying the same request never replays its draft.
  useEffect(() => {
    if (composeRequest === null || handledComposeRequest.current === composeRequest.id) return;
    handledComposeRequest.current = composeRequest.id;
    const { draft: routedDraft, errorReference: routedReference } = composeRequest;
    if (routedDraft !== undefined) setDraft((current) => appendDraft(current, routedDraft));
    if (routedReference !== undefined) setErrorReference(routedReference);
    setFocusToken((token) => token + 1);
    onComposeRequestHandled?.();
  }, [composeRequest, onComposeRequestHandled]);

  // Focus lands after the drafted text commits, so the caret sits at its end.
  useEffect(() => {
    if (focusToken === 0) return;
    composerRef.current?.focus();
  }, [focusToken]);

  const conversation = useMemo(
    () =>
      buildSupervisorConversation(records, {
        optimistic,
        provisional: [...provisional.values()],
      }),
    [optimistic, provisional, records],
  );

  const lifecycle = state?.lifecycle ?? 'stopped';
  const settings = state?.settings ?? { harness: '', model: '', effort: '' };
  const chosen = settingsChosen(settings);
  const pendingRequests: SupervisorPendingRequest[] = (state?.pendingRequests ?? []).filter(
    (item) => !answered.has(item.id),
  );
  const requestPending = pendingRequests.length > 0;
  const turnActive = isTurnActive(lifecycle);
  const canSend =
    state !== null && chosen && !requestPending && !turnActive && !sending && draft.trim() !== '';
  const placeholderOverride = requestPending
    ? SUPERVISOR_COPY.pendingPlaceholder
    : state !== null && !chosen
      ? SUPERVISOR_COPY.unsetPlaceholder
      : undefined;
  const waitingOnRequest = lifecycle === 'waiting_permission' || lifecycle === 'waiting_question';
  const transcriptWaiting =
    sending || (turnActive && !waitingOnRequest && !requestPending && provisional.size === 0);
  const statusInput = {
    lifecycle,
    startingStep: state?.startingStep,
    lastTurnOutcome: state?.lastTurnOutcome ?? 'none',
    interruptedBy: state?.interruptedBy ?? 'none',
    harness: settings.harness,
  } as const;
  const statusLine = supervisorStatusLine(statusInput, sending);
  const paused = !sending && isPausedByRestart(statusInput);
  // The launch failure lives in the read model, so its card survives a
  // reload and leaves with the `failed` lifecycle. The sender got the same
  // canonical error; one card says it once.
  const failure = lifecycle === 'failed' ? (state?.failure ?? null) : null;
  const shownInlineError =
    inlineError !== null && failure !== null && inlineError.code === failure.code
      ? null
      : inlineError;

  const send = async (): Promise<void> => {
    if (runComposerSlashCommand(draft, slashCommands)) {
      setDraft('');
      return;
    }
    const text = draft.trim();
    if (!canSend) return;
    const reference = errorReference;
    setOptimistic(text);
    setDraft('');
    setInlineError(null);
    setSending(true);
    setPinToBottom((token) => token + 1);
    try {
      const result = await window.agentico.sendSupervisorMessage({
        text,
        ...(reference === null ? {} : { errorReference: reference }),
      });
      setRecords((current) => mergeRecords(current, [result.record]));
      setOptimistic(null);
      // A newer explain routed in while the send was in flight keeps its own.
      setErrorReference((current) => (current === reference ? null : current));
    } catch (error) {
      setOptimistic(null);
      // Nothing was committed: the text goes back where it came from, unless
      // the person has already started a new draft.
      setDraft((current) => (current === '' ? text : current));
      setInlineError(parseIpcError(error));
    } finally {
      setSending(false);
      void refreshState();
    }
  };

  const stop = async (): Promise<void> => {
    if (stopping) return;
    setStopping(true);
    setInlineError(null);
    const tick = streamStateTick.current;
    try {
      const result = await window.agentico.interruptSupervisor();
      if (tick === streamStateTick.current) setState(result.state);
    } catch (error) {
      setInlineError(parseIpcError(error));
    } finally {
      setStopping(false);
    }
  };

  const commitSettings = async (target: SupervisorSettings): Promise<void> => {
    if (state === null) return;
    try {
      if (state.pendingChange !== undefined) {
        const cancelled = await window.agentico.cancelSupervisorPendingChange({
          requestId: state.pendingChange.requestId,
        });
        setState(cancelled);
      }
      const request: SupervisorSettingsRequest = {
        ...target,
        requestId: crypto.randomUUID(),
        expectedGeneration: state.generation,
      };
      const next = await window.agentico.updateSupervisorSettings(request);
      setState(next);
    } catch (error) {
      await refreshState();
      throw error;
    }
  };

  const slashCommands: readonly ComposerSlashCommand[] = [
    {
      name: '/model',
      description: 'Change the supervisor model',
      onExecute: (argument) => {
        const query = argument.trim().toLocaleLowerCase();
        const models = (catalogue?.phaseProviderModels.chat?.[settings.harness] ?? []).map(
          (id) => findCatalogueModel(catalogue, settings.harness, id) ?? { id },
        );
        const matches = models.filter((model) =>
          [model.id, model.displayName ?? '', ...(model.aliases ?? [])].some(
            (name) => name.toLocaleLowerCase() === query,
          ),
        );
        if (query !== '' && matches.length === 1) {
          const model = matches[0]!;
          const effort =
            settings.effort !== '' && !model.effortCapabilities?.includes(settings.effort as never)
              ? ''
              : settings.effort;
          void commitSettings({ ...settings, model: model.id, effort }).catch((error) =>
            setInlineError(parseIpcError(error)),
          );
        } else setOpenSection({ section: 'model', filter: argument.trim(), token: Date.now() });
      },
    },
    {
      name: '/effort',
      description: 'Change the supervisor effort',
      onExecute: (argument) => {
        const query = argument.trim().toLocaleLowerCase();
        const levels = [
          '',
          ...(findCatalogueModel(catalogue, settings.harness, settings.model)?.effortCapabilities ??
            []),
        ];
        const matched = levels.find((level) => (level === '' ? 'default' : level) === query);
        if (query !== '' && matched !== undefined) {
          void commitSettings({ ...settings, effort: matched }).catch((error) =>
            setInlineError(parseIpcError(error)),
          );
        } else setOpenSection({ section: 'effort', filter: argument.trim(), token: Date.now() });
      },
    },
  ];

  const submitAttention = async (
    item: SupervisorPendingRequest,
    action: AttentionAction,
    options: AttentionSubmitOptions = {},
  ): Promise<void> => {
    if (attentionBusy !== null) return;
    setAttentionBusy(item.id);
    setInlineError(null);
    try {
      await runAttentionSubmit(action, refreshAttention, { collapseOnSuccess: false, ...options });
      setAnswered((current) => new Set(current).add(item.id));
      void refreshState();
    } catch (error) {
      setInlineError(attentionError(error));
    } finally {
      setAttentionBusy(null);
    }
  };

  if (state === null && stateError !== null) {
    return (
      <section className="supervisor-page" aria-label={SUPERVISOR_COPY.title}>
        <div className="supervisor-page__failure">
          <ErrorSurface
            error={stateError}
            variant="compact"
            localAction={retryAction(() => void load(false))}
          />
        </div>
      </section>
    );
  }

  const trailing = requestPending ? (
    <div className="supervisor-page__requests">
      {pendingRequests.map((item) =>
        item.kind === 'permission' ? (
          <div key={`${item.kind}:${item.id}`} className="supervisor-request">
            <AttentionDetail
              item={item}
              busy={attentionBusy === item.id}
              drafts={attentionDrafts}
              setDrafts={setAttentionDrafts}
              submit={(action, options) => void submitAttention(item, action, options)}
            />
          </div>
        ) : (
          <div
            key={`${item.kind}:${item.id}`}
            className="supervisor-request supervisor-request--question"
          >
            <QuestionConversationTurn
              item={item}
              busy={attentionBusy === item.id}
              drafts={attentionDrafts}
              setDrafts={setAttentionDrafts}
              onSubmit={() =>
                void submitAttention(item, () =>
                  window.agentico.answerQuestions(questionAnswersRequest(item, attentionDrafts)),
                )
              }
            />
            <QuestionComposer
              item={item}
              busy={attentionBusy === item.id}
              drafts={attentionDrafts}
              setDrafts={setAttentionDrafts}
              onSubmit={() =>
                void submitAttention(item, () =>
                  window.agentico.answerQuestions(questionAnswersRequest(item, attentionDrafts)),
                )
              }
            />
          </div>
        ),
      )}
    </div>
  ) : undefined;

  return (
    <section className="supervisor-page" aria-label={SUPERVISOR_COPY.title}>
      <ConversationTranscript
        className="supervisor-page__transcript"
        ariaLabel="Supervisor conversation"
        assistantName={SUPERVISOR_COPY.title}
        items={conversation}
        waiting={transcriptWaiting}
        idleLabel="Working through your message"
        pinToBottomToken={pinToBottom}
        trailing={trailing}
        status={
          <>
            {state === null ? <p role="status">Loading the supervisor…</p> : null}
            {transcriptError !== null ? (
              <ErrorSurface
                error={transcriptError}
                variant="compact"
                localAction={retryAction(() => void load(false))}
              />
            ) : null}
          </>
        }
        emptyState={
          state === null ? null : (
            <div className="supervisor-page__empty">
              <strong>{SUPERVISOR_COPY.emptyHeading}</strong>
              <span>{SUPERVISOR_COPY.emptyBody}</span>
            </div>
          )
        }
      />
      <div className="supervisor-page__dock">
        {state?.pendingChange !== undefined ? (
          <div className="supervisor-pending-tray" role="status">
            <span>
              {state.pendingChange.kind === 'model'
                ? 'Model change pending'
                : 'Effort change pending'}{' '}
              — applies when{' '}
              {lifecycle === 'starting' ? 'the supervisor is ready' : 'this turn ends'}
            </span>
            <strong>
              {state.pendingChange.kind === 'model'
                ? state.pendingChange.target.model
                : state.pendingChange.target.effort || 'Default'}
            </strong>
            <button type="button" onClick={() => void stop()}>
              Stop turn &amp; apply
            </button>
            <button
              type="button"
              onClick={() =>
                void window.agentico
                  .cancelSupervisorPendingChange({ requestId: state.pendingChange!.requestId })
                  .then(setState)
                  .catch((error) => setInlineError(parseIpcError(error)))
              }
            >
              Cancel
            </button>
          </div>
        ) : null}
        {failure !== null ? (
          <ErrorSurface
            error={failure}
            variant="compact"
            localAction={
              draft.trim() === ''
                ? { label: SUPERVISOR_COPY.retry, disabledReason: SUPERVISOR_COPY.retryNeedsText }
                : { label: SUPERVISOR_COPY.retry, onAction: () => void send() }
            }
          />
        ) : null}
        {shownInlineError !== null ? (
          <ErrorSurface error={shownInlineError} variant="compact" />
        ) : null}
        <div className="supervisor-composer">
          <DescriptionComposer
            id="supervisor-composer"
            label={SUPERVISOR_COPY.composerLabel}
            hideLabel
            placeholder={SUPERVISOR_COPY.placeholder}
            placeholderOverride={placeholderOverride}
            value={draft}
            rows={2}
            maxLength={SUPERVISOR_MESSAGE_MAX_CHARS}
            composerRef={composerRef}
            allowUploads={false}
            searchRepositories={NO_REPOSITORIES}
            images={NO_FILES}
            attachments={NO_FILES}
            imageUploads={NO_FILES}
            attachmentUploads={NO_FILES}
            repositoryFiles={NO_FILES}
            onValueChange={(value) => {
              setDraft(value);
              if (value.trim() === '') setErrorReference(null);
            }}
            onImagesChange={ignoreUpdate}
            onAttachmentsChange={ignoreUpdate}
            onImageUploadsChange={ignoreUpdate}
            onAttachmentUploadsChange={ignoreUpdate}
            onRepositoryFilesChange={ignoreUpdate}
            onError={setInlineError}
            onSubmit={() => void send()}
            slashCommands={slashCommands}
            submitDisabled={!canSend}
            footer={
              <div className="supervisor-composer__footer">
                <SupervisorModelChip
                  settings={settings}
                  catalogue={catalogue}
                  onCommit={commitSettings}
                  openSection={openSection}
                />
                <p
                  className="supervisor-status"
                  role="status"
                  data-testid="supervisor-status"
                  data-lifecycle={lifecycle}
                  data-tone={paused ? 'paused' : undefined}
                >
                  <span className="supervisor-status__lamp" aria-hidden="true" />
                  {statusLine}
                </p>
                {turnActive ? (
                  <button
                    type="button"
                    className="supervisor-composer__stop"
                    disabled={stopping}
                    onClick={() => void stop()}
                  >
                    <StopIcon className="supervisor-composer__stop-icon" />
                    {SUPERVISOR_COPY.stop}
                  </button>
                ) : (
                  <button
                    type="button"
                    className="supervisor-composer__send"
                    disabled={!canSend}
                    onClick={() => void send()}
                  >
                    {SUPERVISOR_COPY.send}
                    <span aria-hidden="true"> ↵</span>
                  </button>
                )}
              </div>
            }
          />
        </div>
      </div>
    </section>
  );
}
