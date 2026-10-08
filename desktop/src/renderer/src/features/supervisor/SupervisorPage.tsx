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
 * build a provisional assistant row keyed by generation and stream message
 * id until the matching record commits, `state` replaces the read model,
 * `request` adds a pending card ahead of its state change, and `reset`
 * re-snapshots, keeping records streamed during the reload above the
 * fetched page's head. Deltas and requests from a retired generation, and
 * deltas for a stream message that already committed, are dropped.
 *
 * Older history loads a page at a time as the person scrolls near the top,
 * with the previously first visible row held in place. Each page request is
 * stamped with the reset epoch and server epoch it started in; a page that
 * lands after a reset or a server switch is discarded. Pending
 * permissions and questions sit at the bottom of the transcript in the
 * shared attention card and question turn, and are answered through the
 * existing attention submit path.
 *
 * A routed compose request ("Explain in chat", ⌘⇧M) focuses the composer
 * and may draft text into it unsent, carrying an error reference that rides
 * hidden with the next send.
 *
 * The draft (text, reference, attachments) and the local message queue live
 * in the renderer-wide drafts store, keyed by server, so they survive
 * navigation and server switches. A message submitted during a turn queues;
 * the queue sends one message per turn when a turn completes naturally and
 * holds after an interrupted, failed or restart-cut one. Escape that reaches
 * the page unhandled stops a working turn. "New conversation" resets the
 * server's conversation, confirming first only when work would be lost.
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
  ConnectionState,
  ErrorReference,
  SupervisorPendingRequest,
  SupervisorRecord,
  SupervisorSettingsRequest,
  SupervisorSettings,
  SupervisorState,
} from '../../../../shared/ipc';
import { ErrorSurface } from '../../components/ErrorSurface';
import { AgenticoMonogram, StopIcon } from '../../components/icons';
import { retryAction, useConnectionState } from '../../hooks';
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
import type { ConversationAttachment } from '../transcript/conversation';
import { greetingFor, inspirationFor } from './supervisorWelcome';
import {
  isBlockingStagedItem,
  STAGED_ITEMS_BLOCK_SUBMIT,
  submittableReferences,
} from '../stagedItems';
import { SupervisorModelChip } from './SupervisorModelChip';
import { SupervisorContextRing } from './SupervisorContextRing';
import { SupervisorSwitchDialog, type SupervisorSwitchChoice } from './SupervisorSwitchDialog';
import { SupervisorQueueStrip } from './SupervisorQueueStrip';
import { SupervisorResetDialog } from './SupervisorResetDialog';
import {
  draftItemsEmpty,
  useSupervisorDraftEntry,
  useSupervisorDrafts,
  type SupervisorDraftItems,
  type SupervisorQueuedMessage,
} from './supervisorDrafts';
import {
  findCatalogueModel,
  harnessLabel,
  isPausedByRestart,
  isTurnActive,
  isWaitingOnRequest,
  mergeRecords,
  mergeSnapshotRecords,
  settingsChosen,
  streamKey,
  SUPERVISOR_COPY,
  supervisorConversationBuilder,
  supervisorConversationTail,
  supervisorStatusLine,
  type ProvisionalReply,
} from './supervisorModel';
import { SUPERVISOR_MESSAGE_MAX_CHARS } from '../../../../shared/ipc';

const NO_REPOSITORIES: readonly never[] = [];
const NO_FILES: readonly never[] = [];
const ignoreUpdate = (): void => {};
/** The drafts-store key when the shell supplies no server key (standalone renders). */
const DEFAULT_SERVER_KEY = 'default';
/** The server's machine code for a send refused because a turn is running. */
const TURN_ACTIVE = 'turn_active';

/**
 * A routed ask to focus the composer, optionally drafting `draft` into it
 * with the error reference the drafted message carries. Handled once per id.
 */
export interface SupervisorComposeRequest {
  id: number;
  draft?: string;
  errorReference?: ErrorReference;
}

/** One routed "New conversation" intent; the page applies its confirmation rule. */
export interface SupervisorNewConversationRequest {
  id: number;
}

export interface SupervisorPageProps {
  /** The connection key the page's draft and queue are stored under. */
  serverKey?: string;
  attentionDrafts: AttentionDrafts;
  setAttentionDrafts: Dispatch<SetStateAction<AttentionDrafts>>;
  /** Refreshes the shell-wide attention snapshot after an answer. */
  refreshAttention(): Promise<AttentionItem[]>;
  /** Owned by the shell: the newest routed compose request, until handled. */
  composeRequest?: SupervisorComposeRequest | null;
  onComposeRequestHandled?(): void;
  /**
   * Owned by the shell: the newest "New conversation" intent (toolbar button,
   * palette, Navigate menu), until handled. Each request has a fresh id.
   */
  newConversationRequest?: SupervisorNewConversationRequest | null;
  onNewConversationRequestHandled?(): void;
}

/** One message on its way to the server: the composer's or a queued item's. */
interface OutgoingMessage {
  text: string;
  items: SupervisorDraftItems;
  errorReference: ErrorReference | null;
}

/** An outgoing message's attachments as optimistic chips. */
function outgoingChips(items: SupervisorDraftItems): ConversationAttachment[] {
  return [
    ...items.images.map((path) => ({ kind: 'image' as const, name: basename(path) })),
    ...items.imageUploads.map((item) => ({ kind: 'image' as const, name: item.name })),
    ...items.attachments.map((path) => ({ kind: 'file' as const, name: basename(path) })),
    ...items.attachmentUploads.map((item) => ({ kind: 'file' as const, name: item.name })),
  ];
}

function basename(path: string): string {
  return path.split(/[\\/]/).at(-1) ?? path;
}

/** Where loading the page before the oldest loaded record stands. */
type EarlierPageStatus = 'idle' | 'loading' | 'failed';

/** The epochs a page request started in; a mismatch on arrival discards it. */
interface PageStamp {
  resetEpoch: number;
  serverEpoch: number;
}

/** The stream ids committed in one generation; replaced when the generation changes. */
interface CommittedStreams {
  generation: number;
  ids: Set<string>;
}

/** The server's machine code for a transcript cursor beyond the head. */
const CURSOR_OUT_OF_RANGE = 'cursor_out_of_range';

function userRecordText(record: SupervisorRecord): string | undefined {
  if (record.kind !== 'user') return undefined;
  return record.messages
    .map((message) => message.text ?? '')
    .join('')
    .trim();
}

export function SupervisorPage({
  serverKey = DEFAULT_SERVER_KEY,
  attentionDrafts,
  setAttentionDrafts,
  refreshAttention,
  composeRequest = null,
  onComposeRequestHandled,
  newConversationRequest = null,
  onNewConversationRequestHandled,
}: SupervisorPageProps) {
  const catalogue = useModelCatalogue();
  const [state, setState] = useState<SupervisorState | null>(null);
  const [stateError, setStateError] = useState<CanonicalError | null>(null);
  const [records, setRecords] = useState<SupervisorRecord[]>([]);
  const [transcriptError, setTranscriptError] = useState<CanonicalError | null>(null);
  const [provisional, setProvisional] = useState<ReadonlyMap<string, ProvisionalReply>>(
    () => new Map(),
  );
  // The draft text, the hidden error reference attached to it (set by a
  // routed explain, a later one replacing it; cleared by a successful send
  // or by the person emptying the composer), the attachments and the queue
  // all live in the per-server drafts store.
  const drafts = useSupervisorDrafts();
  const entry = useSupervisorDraftEntry(drafts, serverKey);
  const draft = entry.text;
  const errorReference = entry.errorReference;
  const draftItems = entry.items;
  const queue = entry.queue;
  const connection = useConnectionState();
  const connectionServerKey = connection.status === 'ready' ? (connection.serverKey ?? null) : null;
  const [focusToken, setFocusToken] = useState(0);
  const composerRef = useRef<DescriptionComposerHandle | null>(null);
  const handledComposeRequest = useRef<number | null>(null);
  const handledNewConversationRequest = useRef<number | null>(null);
  const [optimistic, setOptimistic] = useState<{
    text: string;
    attachments: ConversationAttachment[];
  } | null>(null);
  // Delivery failures of queued items, by id; Edit or Remove resolves one.
  const [queueFailures, setQueueFailures] = useState<ReadonlyMap<string, CanonicalError>>(
    () => new Map(),
  );
  // The stream state event a queued delivery last went out on: the next
  // automatic delivery waits for a newer idle-and-completed state.
  const [stateTick, setStateTick] = useState(0);
  const flushedAtTick = useRef(-1);
  const flushing = useRef(false);
  const [resetConfirm, setResetConfirm] = useState<{ draft?: string } | null>(null);
  const pageRef = useRef<HTMLElement | null>(null);
  const [sending, setSending] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [inlineError, setInlineError] = useState<CanonicalError | null>(null);
  const [openSection, setOpenSection] = useState<{
    section: 'model' | 'effort';
    filter: string;
    token: number;
  } | null>(null);
  const [switchChoice, setSwitchChoice] = useState<SupervisorSwitchChoice | null>(null);
  const [attentionBusy, setAttentionBusy] = useState<string | null>(null);
  // Requests answered from this page: hidden at once, before the server's
  // next state confirms they are gone.
  const [answered, setAnswered] = useState<ReadonlySet<string>>(() => new Set());
  const [pinToBottom, setPinToBottom] = useState(0);
  const [hasMoreBefore, setHasMoreBefore] = useState(false);
  const [earlierStatus, setEarlierStatus] = useState<EarlierPageStatus>('idle');
  const loadRequest = useRef(0);
  const conversationIdRef = useRef<string | null>(null);
  conversationIdRef.current = state?.conversationId ?? null;
  // The current read model's generation: deltas and requests below it belong
  // to a retired process. Also set by stream state events directly, so an
  // event in the same tick as the state change is already judged by it.
  const generationRef = useRef(0);
  generationRef.current = state?.generation ?? 0;
  // Bumped by every snapshot reload (stream reset or out-of-range cursor) and
  // by every server switch; an earlier-page request compares its stamp.
  const resetEpoch = useRef(0);
  const serverEpoch = useRef(0);
  const earlierRequest = useRef<PageStamp | null>(null);
  // Records the stream delivered while a snapshot reload was in flight, with
  // the conversation each belongs to: a reload that lands on a different
  // conversation (a reset) keeps only that conversation's records.
  const reloadStreamed = useRef<{ conversationId: string; record: SupervisorRecord }[] | null>(
    null,
  );
  const committedStreams = useRef<CommittedStreams>({ generation: -1, ids: new Set() });
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

  /** Remembers committed stream ids, starting a fresh set for a newer generation. */
  const rememberCommitted = useCallback((committed: readonly SupervisorRecord[]) => {
    for (const record of committed) {
      if (record.streamMessageId === undefined) continue;
      if (record.generation > committedStreams.current.generation) {
        committedStreams.current = { generation: record.generation, ids: new Set() };
      }
      if (record.generation === committedStreams.current.generation) {
        committedStreams.current.ids.add(record.streamMessageId);
      }
    }
  }, []);

  /**
   * Snapshots the read model and the newest transcript page. `replace`
   * (after a stream reset) discards what the page held except records the
   * stream delivered meanwhile above the fetched head, and abandons any
   * earlier page in flight; the first load merges, so records that streamed
   * in while it was in flight survive.
   */
  const load = useCallback(
    async (replace: boolean) => {
      const request = ++loadRequest.current;
      const tick = streamStateTick.current;
      const previousConversation = conversationIdRef.current;
      if (replace) {
        resetEpoch.current += 1;
        earlierRequest.current = null;
        reloadStreamed.current = [];
        setEarlierStatus('idle');
      }
      const [stateResult, pageResult] = await Promise.allSettled([
        window.agentico.getSupervisorState(),
        window.agentico.getSupervisorTranscript({}),
      ]);
      if (request !== loadRequest.current) return;
      const streamed = reloadStreamed.current ?? [];
      reloadStreamed.current = null;
      const fetchedConversation =
        stateResult.status === 'fulfilled'
          ? stateResult.value.conversationId
          : previousConversation;
      // Another conversation behind the reset (a New conversation here or
      // from another client): the old one's rows and pages go entirely.
      const swapped =
        previousConversation !== null &&
        fetchedConversation !== null &&
        fetchedConversation !== previousConversation;
      if (stateResult.status === 'fulfilled') {
        if (tick === streamStateTick.current || swapped) {
          conversationIdRef.current = stateResult.value.conversationId;
          setState(stateResult.value);
        }
        setStateError(null);
      } else {
        setStateError(parseIpcError(stateResult.reason));
      }
      if (swapped) setOptimistic(null);
      if (pageResult.status === 'fulfilled') {
        const fetched = pageResult.value;
        if (replace || swapped) {
          const kept = mergeSnapshotRecords(
            fetched.items,
            fetched.headSeq,
            streamed
              .filter((item) => item.conversationId === fetchedConversation)
              .map((item) => item.record),
          );
          committedStreams.current = { generation: -1, ids: new Set() };
          rememberCommitted(kept);
          setRecords(kept);
          setProvisional(new Map());
        } else {
          rememberCommitted(fetched.items);
          setRecords((current) => mergeRecords(current, fetched.items));
        }
        setHasMoreBefore(fetched.hasMoreBefore);
        setTranscriptError(null);
      } else {
        setTranscriptError(parseIpcError(pageResult.reason));
      }
    },
    [rememberCommitted],
  );

  const oldestSeq = records[0]?.seq;

  /**
   * Requests the page before the oldest loaded record. A page that lands
   * after a reset or a server switch is discarded; an out-of-range cursor
   * (the transcript moved under it) re-snapshots silently; any other failure
   * leaves the Retry row in place of the loading row.
   */
  const loadEarlier = useCallback(
    async (retry: boolean) => {
      if (earlierRequest.current !== null || oldestSeq === undefined || !hasMoreBefore) return;
      if (earlierStatus === 'failed' && !retry) return;
      const stamp: PageStamp = {
        resetEpoch: resetEpoch.current,
        serverEpoch: serverEpoch.current,
      };
      const current = (): boolean =>
        earlierRequest.current === stamp &&
        stamp.resetEpoch === resetEpoch.current &&
        stamp.serverEpoch === serverEpoch.current;
      earlierRequest.current = stamp;
      setEarlierStatus('loading');
      try {
        const earlier = await window.agentico.getSupervisorTranscript({ before: oldestSeq });
        if (!current()) return;
        earlierRequest.current = null;
        rememberCommitted(earlier.items);
        setRecords((loaded) => mergeRecords(loaded, earlier.items));
        setHasMoreBefore(earlier.hasMoreBefore);
        setEarlierStatus('idle');
      } catch (error) {
        if (!current()) return;
        earlierRequest.current = null;
        if (parseIpcError(error).code === CURSOR_OUT_OF_RANGE) {
          setEarlierStatus('idle');
          void load(true);
          return;
        }
        setEarlierStatus('failed');
      }
    },
    [earlierStatus, hasMoreBefore, load, oldestSeq, rememberCommitted],
  );

  // A server switch abandons any earlier page in flight; the stream's reset
  // that follows the switch re-snapshots the newest page.
  useEffect(() => {
    let serverKey: string | null | undefined;
    const observe = (connection: ConnectionState): void => {
      const next = connection.serverKey ?? null;
      if (next === null) return;
      if (serverKey !== undefined && serverKey !== null && next !== serverKey) {
        serverEpoch.current += 1;
        earlierRequest.current = null;
        committedStreams.current = { generation: -1, ids: new Set() };
        setEarlierStatus('idle');
      }
      serverKey = next;
    };
    const unsubscribe = window.agentico.onConnectionChanged(observe);
    void window.agentico
      .getConnectionStatus()
      .then((connection) => {
        if (serverKey === undefined) observe(connection);
      })
      .catch(() => {
        // The shell surfaces connection failures; paging simply keeps its epoch.
      });
    return unsubscribe;
  }, []);

  useEffect(() => {
    const unsubscribe = window.agentico.onSupervisorEvent((event) => {
      if (event.type === 'reset') {
        void load(true);
        return;
      }
      if (event.type === 'stream-status') return;
      if (event.type === 'record') {
        reloadStreamed.current?.push({
          conversationId: event.conversationId,
          record: event.record,
        });
      }
      const conversationId = conversationIdRef.current;
      if (conversationId !== null && event.conversationId !== conversationId) return;
      if (event.type === 'record') {
        const { record } = event;
        rememberCommitted([record]);
        setRecords((current) => mergeRecords(current, [record]));
        if (record.streamMessageId !== undefined) {
          const committed = streamKey(record.generation, record.streamMessageId);
          setProvisional((current) => {
            if (!current.has(committed)) return current;
            const next = new Map(current);
            next.delete(committed);
            return next;
          });
        }
        const text = userRecordText(record);
        if (text !== undefined) {
          setOptimistic((current) => (current?.text === text ? null : current));
        }
      } else if (event.type === 'delta') {
        const { delta } = event;
        // A retired generation's text, or a chunk of a message that already
        // committed (a replay or a late chunk), never becomes a row.
        if (event.generation < generationRef.current) return;
        if (
          event.generation === committedStreams.current.generation &&
          committedStreams.current.ids.has(delta.streamMessageId)
        ) {
          return;
        }
        const key = streamKey(event.generation, delta.streamMessageId);
        setProvisional((current) => {
          const known = current.get(key);
          const chunks = new Map(known?.chunks ?? []);
          chunks.set(delta.chunkIndex, delta.text);
          const next = new Map(current);
          next.set(key, {
            streamMessageId: delta.streamMessageId,
            generation: event.generation,
            chunks,
          });
          return next;
        });
      } else if (event.type === 'state') {
        streamStateTick.current += 1;
        setStateTick(streamStateTick.current);
        generationRef.current = event.state.generation;
        if (event.state.generation !== committedStreams.current.generation) {
          committedStreams.current = { generation: event.state.generation, ids: new Set() };
        }
        setState(event.state);
        setStateError(null);
        // A turn that ended without committing its streamed text (an
        // interrupt) leaves nothing provisional behind.
        if (!isTurnActive(event.state.lifecycle)) setProvisional(new Map());
      } else {
        const { request } = event;
        if (event.generation < generationRef.current) return;
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
  }, [load, rememberCommitted]);

  // Each routed compose request is handled once by its id, so a re-render
  // carrying the same request never replays its draft.
  useEffect(() => {
    if (composeRequest === null || handledComposeRequest.current === composeRequest.id) return;
    handledComposeRequest.current = composeRequest.id;
    drafts.appendDraft(serverKey, composeRequest.draft, composeRequest.errorReference);
    setFocusToken((token) => token + 1);
    onComposeRequestHandled?.();
  }, [composeRequest, drafts, onComposeRequestHandled, serverKey]);

  // The draft belongs to one conversation: when the server's current one
  // differs (a reset here or elsewhere), the text and attachments carry over
  // and the queue is discarded.
  const loadedConversation = state?.conversationId ?? null;
  useEffect(() => {
    if (loadedConversation === null) return;
    drafts.adoptConversation(serverKey, loadedConversation);
  }, [drafts, loadedConversation, serverKey]);

  // Focus lands after the drafted text commits, so the caret sits at its end.
  useEffect(() => {
    if (focusToken === 0) return;
    composerRef.current?.focus();
  }, [focusToken]);

  // The folded committed history rebuilds only when the records change; a
  // delta recomputes just the provisional tail.
  const committedConversation = useMemo(
    () => supervisorConversationBuilder.committed(records),
    [records],
  );
  // Welcome copy is fixed when the page mounts so the greeting never flips
  // while the empty conversation is on screen; it is recomputed on the next
  // mount.
  const [welcome] = useState(() => {
    const now = new Date();
    return { greeting: greetingFor(now), inspiration: inspirationFor(now) };
  });

  const conversation = useMemo(
    () =>
      supervisorConversationTail(committedConversation, {
        optimistic: optimistic?.text ?? null,
        optimisticAttachments: optimistic?.attachments ?? [],
        provisional: [...provisional.values()],
      }),
    [committedConversation, optimistic, provisional],
  );

  const lifecycle = state?.lifecycle ?? 'stopped';
  const settings = state?.settings ?? { harness: '', model: '', effort: '' };
  const chosen = settingsChosen(settings);
  const pendingRequests: SupervisorPendingRequest[] = (state?.pendingRequests ?? []).filter(
    (item) => !answered.has(item.id),
  );
  const requestPending = pendingRequests.length > 0;
  const turnActive = isTurnActive(lifecycle);
  // In-flight, failed or foreign-server uploads block Send until removed.
  const uploadsBlocking = [...draftItems.imageUploads, ...draftItems.attachmentUploads].some(
    (item) => isBlockingStagedItem(item, connectionServerKey),
  );
  const composerFilled = draft.trim() !== '' || !draftItemsEmpty(draftItems);
  // Submitting during a turn (or while a send is in flight) queues instead.
  const queues = turnActive || sending;
  const canSend = state !== null && chosen && !requestPending && !uploadsBlocking && composerFilled;
  const placeholderOverride = requestPending
    ? SUPERVISOR_COPY.pendingPlaceholder
    : state !== null && !chosen
      ? SUPERVISOR_COPY.unsetPlaceholder
      : undefined;
  const waitingOnRequest = isWaitingOnRequest(lifecycle);
  const transcriptWaiting =
    sending || (turnActive && !waitingOnRequest && !requestPending && provisional.size === 0);
  const statusInput = {
    lifecycle,
    startingStep: state?.startingStep,
    lastTurnOutcome: state?.lastTurnOutcome ?? 'none',
    interruptedBy: state?.interruptedBy ?? 'none',
    harness: settings.harness,
  } as const;
  const statusLine = supervisorStatusLine(statusInput, sending, requestPending);
  const paused = !sending && isPausedByRestart(statusInput);
  // The launch failure lives in the read model, so its card survives a
  // reload and leaves with the `failed` lifecycle. The sender got the same
  // canonical error; one card says it once.
  const failure = lifecycle === 'failed' ? (state?.failure ?? null) : null;
  const shownInlineError =
    inlineError !== null && failure !== null && inlineError.code === failure.code
      ? null
      : inlineError;

  /**
   * Sends one message through the ordinary path: local paths as paths and
   * staged items as references scoped to the connected server. Throws what
   * the send threw; a result from a conversation retired meanwhile is not
   * merged.
   */
  const deliver = async (message: OutgoingMessage): Promise<void> => {
    const { items } = message;
    const imageUploads = submittableReferences(items.imageUploads, 'image', connectionServerKey);
    const attachmentUploads = submittableReferences(
      items.attachmentUploads,
      'attachment',
      connectionServerKey,
    );
    const sentIn = conversationIdRef.current;
    setOptimistic({ text: message.text, attachments: outgoingChips(items) });
    setInlineError(null);
    setSending(true);
    setPinToBottom((token) => token + 1);
    try {
      const result = await window.agentico.sendSupervisorMessage({
        text: message.text,
        ...(message.errorReference === null ? {} : { errorReference: message.errorReference }),
        ...(items.images.length === 0 ? {} : { images: [...items.images] }),
        ...(items.attachments.length === 0 ? {} : { attachments: [...items.attachments] }),
        ...(imageUploads.length === 0 ? {} : { imageUploads }),
        ...(attachmentUploads.length === 0 ? {} : { attachmentUploads }),
      });
      if (sentIn === conversationIdRef.current) {
        rememberCommitted([result.record]);
        setRecords((current) => mergeRecords(current, [result.record]));
      }
      setOptimistic(null);
    } catch (error) {
      setOptimistic(null);
      throw error;
    } finally {
      setSending(false);
      void refreshState();
    }
  };

  const queueMessage = (message: OutgoingMessage): void => {
    drafts.enqueue(serverKey, { id: crypto.randomUUID(), ...message });
  };

  const send = async (): Promise<void> => {
    if (runComposerSlashCommand(draft, slashCommands)) {
      drafts.setText(serverKey, '');
      return;
    }
    if (!canSend) return;
    const message: OutgoingMessage = {
      text: draft.trim(),
      items: draftItems,
      errorReference,
    };
    drafts.clearComposer(serverKey);
    if (queues) {
      queueMessage(message);
      return;
    }
    // A manual send goes ahead of held items; the queue resumes after the
    // turn it starts completes, not on the idle state already showing.
    flushedAtTick.current = streamStateTick.current;
    try {
      await deliver(message);
    } catch (error) {
      const parsed = parseIpcError(error);
      if (parsed.code === TURN_ACTIVE) {
        // The view was stale: the server is mid-turn, so the message waits
        // at the head of the queue instead of failing.
        drafts.requeueFront(serverKey, { id: crypto.randomUUID(), ...message });
        return;
      }
      // Nothing was committed: the message goes back where it came from,
      // unless the person has already started a new draft.
      drafts.update(serverKey, (current) =>
        current.text === '' && draftItemsEmpty(current.items)
          ? {
              ...current,
              text: message.text,
              items: message.items,
              errorReference: current.errorReference ?? message.errorReference,
            }
          : current,
      );
      setInlineError(parsed);
    }
  };

  /** Sends one queued item, removing it on success and marking it failed otherwise. */
  const flushQueued = async (item: SupervisorQueuedMessage): Promise<void> => {
    if (flushing.current) return;
    flushing.current = true;
    flushedAtTick.current = streamStateTick.current;
    drafts.removeQueued(serverKey, item.id);
    try {
      await deliver({ text: item.text, items: item.items, errorReference: item.errorReference });
    } catch (error) {
      const parsed = parseIpcError(error);
      drafts.requeueFront(serverKey, { ...item, next: false });
      if (parsed.code !== TURN_ACTIVE) {
        setQueueFailures((current) => new Map(current).set(item.id, parsed));
      }
    } finally {
      flushing.current = false;
    }
  };

  const lastTurnOutcome = state?.lastTurnOutcome ?? 'none';
  const queueHead = queue[0];
  const markedNext = queue.find((item) => item.next === true);
  // The flush controller: a marked item goes at the first moment no turn is
  // working, whatever the last outcome; otherwise the head goes only after a
  // turn completed naturally, once per state event, and holds while failed.
  useEffect(() => {
    if (state === null || sending || flushing.current || requestPending || !chosen) return;
    if (markedNext !== undefined) {
      if (!turnActive) void flushQueued(markedNext);
      return;
    }
    if (queueHead === undefined || queueFailures.has(queueHead.id)) return;
    if (lifecycle !== 'idle' || lastTurnOutcome !== 'completed') return;
    if (flushedAtTick.current === stateTick) return;
    void flushQueued(queueHead);
    // flushQueued reads the latest render's values; the listed inputs are
    // the conditions that decide whether a delivery is due.
  }, [
    state,
    sending,
    requestPending,
    chosen,
    markedNext,
    turnActive,
    queueHead,
    queueFailures,
    lifecycle,
    lastTurnOutcome,
    stateTick,
  ]);

  const clearQueueFailure = (id: string): void => {
    setQueueFailures((current) => {
      if (!current.has(id)) return current;
      const next = new Map(current);
      next.delete(id);
      return next;
    });
  };

  const editQueued = (id: string): void => {
    clearQueueFailure(id);
    drafts.editQueued(serverKey, id);
    setFocusToken((token) => token + 1);
  };

  const removeQueued = (id: string): void => {
    clearQueueFailure(id);
    // Staged references the item held are left to the server's sweep.
    drafts.removeQueued(serverKey, id);
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

  // Escape stops a working turn, exactly as Stop does, when it reaches the
  // page unhandled: the slash and mention lists, the chip popover, the
  // palette and dialogs claim it first by preventing its default. Listening
  // on the window runs after those document- and element-level handlers.
  const stopRef = useRef(stop);
  stopRef.current = stop;
  const escapeStops = lifecycle === 'running' || lifecycle === 'starting';
  useEffect(() => {
    if (!escapeStops) return;
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key !== 'Escape' || event.defaultPrevented) return;
      const target = event.target;
      const onPage =
        target === document.body ||
        (target instanceof Node && pageRef.current?.contains(target) === true);
      if (!onPage) return;
      event.preventDefault();
      void stopRef.current();
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [escapeStops]);

  /**
   * Starts a new conversation: the queue is discarded, the draft text and
   * attachments carry over (or become `nextDraft` from `/new <text>`), and
   * the page swaps to the empty conversation the server returns.
   */
  const performReset = async (nextDraft?: string): Promise<void> => {
    try {
      const result = await window.agentico.resetSupervisor();
      drafts.update(serverKey, (current) => ({
        ...current,
        conversationId: result.state.conversationId,
        queue: [],
        ...(nextDraft === undefined ? {} : { text: nextDraft }),
      }));
      setQueueFailures(new Map());
      setResetConfirm(null);
      if (result.result === 'noop') {
        setState(result.state);
        return;
      }
      conversationIdRef.current = result.state.conversationId;
      generationRef.current = result.state.generation;
      committedStreams.current = { generation: -1, ids: new Set() };
      setState(result.state);
      setRecords([]);
      setProvisional(new Map());
      setOptimistic(null);
      setAnswered(new Set());
      setHasMoreBefore(false);
      setInlineError(null);
      void load(true);
    } catch (error) {
      setResetConfirm(null);
      setInlineError(parseIpcError(error));
    }
  };

  /** The one "New conversation" handler: confirms only when work would be lost. */
  const requestReset = (nextDraft?: string): void => {
    if (turnActive || queue.length > 0) {
      setResetConfirm(nextDraft === undefined ? {} : { draft: nextDraft });
      return;
    }
    void performReset(nextDraft);
  };

  // Each routed "New conversation" (toolbar, palette, Navigate menu) is
  // handled once by its id, after the read model has loaded.
  useEffect(() => {
    if (newConversationRequest === null || state === null) return;
    if (handledNewConversationRequest.current === newConversationRequest.id) return;
    handledNewConversationRequest.current = newConversationRequest.id;
    onNewConversationRequestHandled?.();
    requestReset();
    // requestReset reads the latest render's lifecycle and queue.
  }, [newConversationRequest, onNewConversationRequestHandled, state === null]);

  const sendQueuedNow = (id: string): void => {
    clearQueueFailure(id);
    drafts.markNext(serverKey, id);
    if (turnActive) void stop();
  };

  const commitSettings = async (
    target: Pick<SupervisorSettingsRequest, 'harness' | 'model' | 'effort'>,
  ): Promise<void> => {
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

  const chooseSwitch = (harness: string, model?: string): void => {
    if (settingsChosen(settings) && harness !== settings.harness && (state?.headSeq ?? 0) > 0) {
      setSwitchChoice({ harness, ...(model === undefined ? {} : { model }) });
      return;
    }
    void commitSettings({ harness, ...(model === undefined ? {} : { model }) }).catch((error) =>
      setInlineError(parseIpcError(error)),
    );
  };

  const retrySwitch = async (target: SupervisorSettings): Promise<void> => {
    try {
      await commitSettings(target);
      await send();
    } catch (error) {
      setInlineError(parseIpcError(error));
    }
  };

  const slashCommands: readonly ComposerSlashCommand[] = [
    {
      name: '/model',
      description: 'Change the supervisor model',
      onExecute: (argument) => {
        const query = argument.trim().toLocaleLowerCase();
        const matches = Object.entries(catalogue?.phaseProviderModels.chat ?? {})
          .flatMap(([harness, ids]) =>
            ids.map((id) => ({
              harness,
              model: findCatalogueModel(catalogue, harness, id) ?? { id },
            })),
          )
          .filter(({ model }) =>
            [model.id, model.displayName ?? '', ...(model.aliases ?? [])].some(
              (name) => name.toLocaleLowerCase() === query,
            ),
          );
        if (query !== '' && matches.length === 1) {
          const { harness, model } = matches[0]!;
          if (harness !== settings.harness) chooseSwitch(harness, model.id);
          else {
            const effort =
              settings.effort !== '' &&
              !model.effortCapabilities?.includes(settings.effort as never)
                ? ''
                : settings.effort;
            void commitSettings({ ...settings, model: model.id, effort }).catch((error) =>
              setInlineError(parseIpcError(error)),
            );
          }
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
    {
      name: '/new',
      description: 'Start a new conversation',
      onExecute: (argument) => {
        // The composer clears after a command runs; `<text>` becomes the new
        // conversation's draft once the reset lands, unsent.
        const nextDraft = argument.trim();
        requestReset(nextDraft === '' ? undefined : nextDraft);
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
      <section className="supervisor-page" aria-label={SUPERVISOR_COPY.title} ref={pageRef}>
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
    <section className="supervisor-page" aria-label={SUPERVISOR_COPY.title} ref={pageRef}>
      <ConversationTranscript
        className="supervisor-page__transcript"
        ariaLabel="Supervisor conversation"
        assistantName={SUPERVISOR_COPY.title}
        items={conversation}
        waiting={transcriptWaiting}
        idleLabel="Working through your message"
        pinToBottomToken={pinToBottom}
        trailing={trailing}
        anchorPrepend
        onNearTop={hasMoreBefore ? () => void loadEarlier(false) : undefined}
        top={
          earlierStatus === 'loading' ? (
            <p
              className="conversation__notice conversation__earlier"
              data-tone="neutral"
              data-state="loading"
              role="status"
            >
              <span className="conversation__notice-mark" aria-hidden="true">
                •
              </span>
              <span className="conversation__notice-text">{SUPERVISOR_COPY.loadingEarlier}</span>
            </p>
          ) : earlierStatus === 'failed' ? (
            <p
              className="conversation__notice conversation__earlier"
              data-tone="failed"
              data-state="failed"
              role="status"
            >
              <span className="conversation__notice-mark" aria-hidden="true">
                ✕
              </span>
              <span className="conversation__notice-text">{SUPERVISOR_COPY.earlierFailed}</span>
              <button
                type="button"
                className="conversation__notice-toggle"
                onClick={() => void loadEarlier(true)}
              >
                {SUPERVISOR_COPY.retry}
              </button>
            </p>
          ) : null
        }
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
            <div className="supervisor-page__welcome">
              <div className="supervisor-page__welcome-heading">
                <AgenticoMonogram className="supervisor-page__welcome-mark" />
                <h2 className="supervisor-page__welcome-greeting">{welcome.greeting}</h2>
              </div>
              <p className="supervisor-page__welcome-line">{welcome.inspiration}</p>
            </div>
          )
        }
      />
      <div className="supervisor-page__dock">
        {state?.pendingChange !== undefined ? (
          <div className="supervisor-pending-tray" role="status">
            <span>
              {state.pendingChange.kind === 'harness'
                ? `Switch to ${harnessLabel(state.pendingChange.target.harness)} pending`
                : state.pendingChange.kind === 'model'
                  ? 'Model change pending'
                  : 'Effort change pending'}{' '}
              — applies when{' '}
              {lifecycle === 'starting' ? 'the supervisor is ready' : 'this turn ends'}
            </span>
            <strong>
              {state.pendingChange.kind === 'harness'
                ? `${harnessLabel(state.pendingChange.target.harness)} · ${state.pendingChange.target.model}`
                : state.pendingChange.kind === 'model'
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
        <SupervisorQueueStrip
          queue={queue}
          failures={queueFailures}
          onEdit={editQueued}
          onSendNow={sendQueuedNow}
          onRemove={removeQueued}
        />
        {failure !== null ? (
          <ErrorSurface
            error={
              failure.attemptedSettings !== undefined &&
              failure.attemptedSettings.harness !== settings.harness
                ? {
                    ...failure,
                    summary: `Couldn't switch to ${harnessLabel(failure.attemptedSettings.harness)} — still using ${harnessLabel(settings.harness)}`,
                  }
                : failure
            }
            variant="compact"
            localAction={
              draft.trim() === ''
                ? { label: SUPERVISOR_COPY.retry, disabledReason: SUPERVISOR_COPY.retryNeedsText }
                : {
                    label: SUPERVISOR_COPY.retry,
                    onAction: () =>
                      void (failure.attemptedSettings !== undefined &&
                      failure.attemptedSettings.harness !== settings.harness
                        ? retrySwitch(failure.attemptedSettings)
                        : send()),
                  }
            }
          />
        ) : null}
        {state?.persistFailure !== undefined ? (
          // Independent of lifecycle: a later successful turn does not
          // restore the unsaved history, so only Dismiss or a new
          // conversation removes it.
          <ErrorSurface
            error={state.persistFailure}
            variant="compact"
            localAction={{
              label: SUPERVISOR_COPY.dismiss,
              onAction: () =>
                void window.agentico
                  .dismissSupervisorPersistFailure()
                  .then(setState)
                  .catch((error) => setInlineError(parseIpcError(error))),
            }}
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
            allowUploads
            attachmentTargetNoun="message"
            searchRepositories={NO_REPOSITORIES}
            images={draftItems.images}
            attachments={draftItems.attachments}
            imageUploads={draftItems.imageUploads}
            attachmentUploads={draftItems.attachmentUploads}
            repositoryFiles={NO_FILES}
            onValueChange={(value) => {
              drafts.setText(serverKey, value);
              if (value.trim() === '') drafts.setErrorReference(serverKey, null);
            }}
            onImagesChange={(update) => drafts.updateItems(serverKey, 'images', update)}
            onAttachmentsChange={(update) => drafts.updateItems(serverKey, 'attachments', update)}
            onImageUploadsChange={(update) => drafts.updateItems(serverKey, 'imageUploads', update)}
            onAttachmentUploadsChange={(update) =>
              drafts.updateItems(serverKey, 'attachmentUploads', update)
            }
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
                  onSwitch={chooseSwitch}
                  openSection={openSection}
                />
                {state !== null ? <SupervisorContextRing usage={state.contextUsage} /> : null}
                <p
                  className="supervisor-status"
                  role="status"
                  data-testid="supervisor-status"
                  data-lifecycle={lifecycle}
                  data-tone={paused ? 'paused' : undefined}
                >
                  <span className="supervisor-status__lamp" aria-hidden="true" />
                  <span className="supervisor-status__text">{statusLine}</span>
                </p>
                {uploadsBlocking ? (
                  <p className="supervisor-composer__blocked" role="status">
                    {STAGED_ITEMS_BLOCK_SUBMIT}
                  </p>
                ) : null}
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
      {resetConfirm !== null ? (
        <SupervisorResetDialog
          turnActive={turnActive}
          queued={queue.length}
          onCancel={() => setResetConfirm(null)}
          onConfirm={() => performReset(resetConfirm.draft)}
        />
      ) : null}
      {switchChoice !== null ? (
        <SupervisorSwitchDialog
          key={`${switchChoice.harness}:${switchChoice.model ?? ''}`}
          choice={switchChoice}
          catalogue={catalogue}
          lifecycle={lifecycle}
          onCancel={() => setSwitchChoice(null)}
          onSwitch={async (request) => {
            await commitSettings(request);
            setSwitchChoice(null);
          }}
        />
      ) : null}
    </section>
  );
}
