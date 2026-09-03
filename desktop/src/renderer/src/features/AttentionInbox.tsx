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

import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type Dispatch,
  type KeyboardEvent as ReactKeyboardEvent,
  type SetStateAction,
} from 'react';
import {
  actionableAttentionCount,
  ATTENTION_ALREADY_RESOLVED_NOTICE,
  ATTENTION_SUBMITTED_NOTICE,
  attentionOwnerFeatureId,
  CHAT_SESSION_ID,
  isSyntheticHelpItem,
  type AttentionActionResult,
  type AttentionItem,
  type AutoApproveScope,
  type VerificationGateAction,
} from '../../../shared/ipc';
import { BellIcon, ChevronDownIcon } from '../components/icons';
import { ToolbarPopover, ToolbarPopoverAnchor } from '../components/ToolbarPopover';
import { useDetailsDismiss } from '../components/useDetailsDismiss';
import {
  hasStructuredVerificationDecision,
  NeedUserInputVerificationDecision,
} from './NeedUserInputVerificationDecision';
import { formatWaitingDuration } from './phaseRail';
import { useAttentionDraftSaves } from './useAttentionDraftSaves';

export interface QuestionAnswerDraft {
  selected: string[];
  freeText: string;
}

export interface AttentionDrafts {
  questions: Record<string, Record<string, QuestionAnswerDraft>>;
  help: Record<string, string>;
  gates: Record<string, Record<number, string>>;
}

export function emptyAttentionDrafts(): AttentionDrafts {
  return { questions: {}, help: {}, gates: {} };
}

export interface AttentionInboxProps {
  items: AttentionItem[];
  refresh(): Promise<AttentionItem[]>;
  featureLabel(featureId: string | undefined): string;
  drafts: AttentionDrafts;
  setDrafts: Dispatch<SetStateAction<AttentionDrafts>>;
  onJump(featureId: string, attentionId?: string): void;
  openRequest?: { id: number; attentionId?: string } | null;
  /**
   * Lifts the popover's open state to the toolbar, which enforces the
   * one-popover-at-a-time rule against the update popover. Omit both to let
   * the inbox own its own state (harness scenes and focused tests).
   */
  open?: boolean;
  onOpenChange?(open: boolean): void;
}

export interface AttentionSubmitOptions {
  collapseOnSuccess?: boolean;
  successNotice?: string;
}

export type AttentionAction = () => Promise<AttentionActionResult>;

export async function runAttentionSubmit(
  action: AttentionAction,
  refresh: () => Promise<AttentionItem[]>,
  options: AttentionSubmitOptions = {},
): Promise<{ latest: AttentionItem[]; notice: string; result: AttentionActionResult }> {
  const result = await action();
  const latest = await refresh();
  return {
    latest,
    notice: attentionActionNotice(result, options),
    result,
  };
}

export function attentionActionNotice(
  result: AttentionActionResult,
  options: AttentionSubmitOptions = {},
): string {
  return (
    result.notice ??
    options.successNotice ??
    (result.alreadyResolved === true
      ? ATTENTION_ALREADY_RESOLVED_NOTICE
      : ATTENTION_SUBMITTED_NOTICE)
  );
}

export function attentionErrorMessage(error: unknown): string {
  return error instanceof Error ? error.message : 'Could not submit this response.';
}

/**
 * Global, snapshot-only blocking-input inbox, presented as a transient popover
 * anchored under the toolbar bell. Draft text stays in renderer session state
 * until submitted; server-owned gate drafts are persisted on blur.
 */
export function AttentionInbox({
  items: allItems,
  refresh,
  featureLabel,
  drafts,
  setDrafts,
  onJump,
  openRequest = null,
  open: controlledOpen,
  onOpenChange,
}: AttentionInboxProps) {
  // Synthetic help items (a session idling between turns — phase coordination
  // or the chat resting after a reply) are never inbox rows. Memoized: effects
  // key on the list's identity, so a fresh array every render would loop them.
  const items = useMemo(() => allItems.filter((item) => !isSyntheticHelpItem(item)), [allItems]);
  const [uncontrolledOpen, setUncontrolledOpen] = useState(false);
  const open = controlledOpen ?? uncontrolledOpen;
  const setOpen = useCallback(
    (next: boolean) => {
      setUncontrolledOpen(next);
      onOpenChange?.(next);
    },
    [onOpenChange],
  );
  const [expanded, setExpanded] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [notice, setNotice] = useState('');
  const [pendingFocus, setPendingFocus] = useState<string | null>(null);
  const bell = useRef<HTMLButtonElement>(null);
  const itemButtons = useRef(new Map<string, HTMLButtonElement>());
  const openedRequestId = useRef<number | null>(null);

  useEffect(() => {
    if (openRequest === null) return;
    if (openedRequestId.current !== openRequest.id) {
      openedRequestId.current = openRequest.id;
      setOpen(true);
    }
    if (openRequest.attentionId !== undefined) {
      const requested = items.find((item) => item.id === openRequest.attentionId);
      setExpanded(
        requested !== undefined &&
          requested.kind !== 'recovery' &&
          requested.featureId === undefined
          ? openRequest.attentionId
          : null,
      );
      setPendingFocus(openRequest.attentionId);
    }
  }, [items, openRequest, setOpen]);

  useEffect(() => {
    if (!open) return;
    if (pendingFocus === null) return;
    if (hasActiveModalDialog()) {
      setPendingFocus(null);
      return;
    }
    itemButtons.current.get(pendingFocus)?.focus();
    setPendingFocus(null);
  }, [items, open, pendingFocus]);

  useEffect(() => {
    if (!open) return;
    if (expanded === null || busy !== null) return;
    if (items.some((item) => item.id === expanded)) return;
    const next = items[0];
    setNotice(ATTENTION_ALREADY_RESOLVED_NOTICE);
    setExpanded(next?.id ?? null);
    setPendingFocus(hasActiveModalDialog() ? null : (next?.id ?? null));
  }, [busy, expanded, items, open]);

  // ⌘⇧A is the bell click's keyboard twin: it toggles, and closing returns
  // focus to the bell. Escape and the outside pointer live in ToolbarPopover.
  useEffect(() => {
    const key = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.shiftKey && event.key.toLowerCase() === 'a') {
        event.preventDefault();
        setOpen(!open);
        if (open) bell.current?.focus();
      }
    };
    window.addEventListener('keydown', key);
    return () => window.removeEventListener('keydown', key);
  }, [open, setOpen]);

  const submit = async (
    id: string,
    action: AttentionAction,
    options: AttentionSubmitOptions = {},
  ) => {
    if (busy !== null) return;
    setBusy(id);
    setNotice('');
    try {
      const { latest, notice: nextNotice } = await runAttentionSubmit(action, refresh, options);
      setNotice(nextNotice);
      const stillPending = latest.some((item) => item.id === id);
      if (options.collapseOnSuccess !== false && !stillPending) {
        const previousIndex = items.findIndex((item) => item.id === id);
        const remaining = latest.filter((item) => item.id !== id);
        const next = remaining[Math.min(Math.max(previousIndex, 0), remaining.length - 1)];
        setExpanded(next?.id ?? null);
        setPendingFocus(next?.id ?? null);
      }
    } catch (error) {
      setNotice(attentionErrorMessage(error));
    } finally {
      setBusy(null);
    }
  };

  const saveDraft = useAttentionDraftSaves({
    notify: (result, options) => setNotice(attentionActionNotice(result, options)),
    notifyError: (error) => setNotice(attentionErrorMessage(error)),
    onAlreadyResolved: async () => {
      await refresh();
    },
  });

  const focusRelativeItem = (currentId: string, direction: 1 | -1) => {
    const index = items.findIndex((item) => item.id === currentId);
    if (index < 0 || items.length === 0) return;
    const next = items[(index + direction + items.length) % items.length];
    if (next === undefined) return;
    itemButtons.current.get(next.id)?.focus();
  };

  const actionableCount = actionableAttentionCount(items);

  return (
    <ToolbarPopoverAnchor>
      <button
        ref={bell}
        type="button"
        className="attention-bell"
        data-empty={actionableCount === 0}
        aria-label={`Attention inbox, ${actionableCount} pending`}
        aria-expanded={open}
        aria-controls="attention-popover"
        onClick={() => {
          setOpen(!open);
          if (open) bell.current?.focus();
        }}
      >
        <BellIcon />
        {/* Quiet at zero: no badge at all, rather than a badge reading "0". */}
        {actionableCount > 0 ? (
          <span className="attention-bell__count" aria-hidden="true">
            {actionableCount}
          </span>
        ) : null}
      </button>
      {notice !== '' && !open ? (
        <p className="sr-only" role="status">
          {notice}
        </p>
      ) : null}
      <ToolbarPopover
        open={open}
        as="aside"
        id="attention-popover"
        className="attention-popover"
        label="Attention inbox"
        anchorRef={bell}
        onDismiss={() => setOpen(false)}
      >
        <header className="attention-popover__header">
          <h2>Attention inbox</h2>
          <span className="attention-popover__count">{actionableCount} waiting</span>
        </header>
        {notice !== '' ? (
          <p className="attention-status" role="status" aria-live="polite">
            {notice}
          </p>
        ) : null}
        {items.length === 0 ? (
          <p className="attention-popover__empty" role="status">
            No blocking input is waiting.
          </p>
        ) : (
          <ul className="attention-popover__list">
            {items.map((item) => (
              <li key={`${item.kind}:${item.id}`} className="attention-popover__row">
                <button
                  ref={(node) => {
                    if (node === null) itemButtons.current.delete(item.id);
                    else itemButtons.current.set(item.id, node);
                  }}
                  type="button"
                  className="attention-popover__item"
                  aria-expanded={
                    item.kind !== 'recovery' && item.featureId === undefined
                      ? expanded === item.id
                      : undefined
                  }
                  onClick={() => {
                    if (item.kind === 'recovery') {
                      setOpen(false);
                      onJump('__recovery__');
                      return;
                    }
                    const ownerFeatureId = attentionOwnerFeatureId(item);
                    if (ownerFeatureId !== undefined) {
                      setOpen(false);
                      onJump(ownerFeatureId, item.kind === 'review' ? undefined : item.id);
                      return;
                    }
                    setExpanded(expanded === item.id ? null : item.id);
                  }}
                  onKeyDown={(event) => handleItemKeyDown(event, item.id, focusRelativeItem)}
                >
                  <span className="attention-popover__item-main">
                    <span className="attention-popover__kind">{attentionKindLabel(item)}</span>
                    <span className="attention-popover__feature">
                      {item.kind === 'recovery'
                        ? 'Recovery workspace'
                        : featureLabel(attentionOwnerFeatureId(item))}
                    </span>
                  </span>
                  <span className="attention-popover__waiting">
                    {formatWaitingSince(item.waitingSince)}
                  </span>
                </button>
                {expanded === item.id ? (
                  <AttentionDetail
                    item={item}
                    busy={busy === item.id}
                    submit={(action, options) => submit(item.id, action, options)}
                    saveDraft={(action, options) => saveDraft(item.id, action, options)}
                    onJump={(featureId) => {
                      setExpanded(null);
                      setOpen(false);
                      onJump?.(featureId);
                    }}
                    drafts={drafts}
                    setDrafts={setDrafts}
                  />
                ) : null}
              </li>
            ))}
          </ul>
        )}
      </ToolbarPopover>
    </ToolbarPopoverAnchor>
  );
}

/**
 * Split button offered on a Bash prompt when auto mode is off but would have
 * handled the request. The main action allows the command and turns auto mode
 * on for this feature; the chevron menu offers the workspace-wide choice.
 * Running sessions pick the change up on their next command.
 */
function AutoModeSplitButton({
  item,
  busy,
  submit,
}: {
  item: Extract<AttentionItem, { kind: 'permission' }>;
  busy: boolean;
  submit(action: AttentionAction, options?: AttentionSubmitOptions): void;
}) {
  const menuRef = useRef<HTMLDetailsElement>(null);
  useDetailsDismiss(menuRef, '.attention-split__toggle');
  const offer = item.autoApprove;
  if (offer === undefined) return null;
  const answer = (scope: AutoApproveScope) => {
    if (menuRef.current !== null) menuRef.current.open = false;
    submit(
      () =>
        window.agentico.answerPermission({
          requestId: item.id,
          ...(item.sessionId === undefined ? {} : { sessionId: item.sessionId }),
          decision: 'allow_once',
          autoApproveScope: scope,
        }),
      {
        successNotice:
          scope === 'feature'
            ? 'Allowed. Auto mode is on for this feature.'
            : 'Allowed. Auto mode is on for all features.',
      },
    );
  };
  const why = offer.wouldFastPath
    ? 'Auto mode would have approved this command on its own: it matches the safe build and test fast path.'
    : 'Auto mode would have sent this command to a reviewer model, which approves it or hands it back to you.';
  const featureScoped = item.featureId !== undefined;
  return (
    <div className="attention-split" role="group" aria-label="Enable auto mode">
      <button
        className="attention-button attention-split__main"
        disabled={busy}
        title={`${why} Allows this command now and turns auto mode on${featureScoped ? ' for this feature' : ' for all features'}.`}
        onClick={() => answer(featureScoped ? 'feature' : 'workspace')}
      >
        {featureScoped ? 'Enable auto mode (this feature only)' : 'Enable auto mode (all features)'}
      </button>
      {featureScoped ? (
        <details ref={menuRef} className="attention-split__more">
          <summary
            className="attention-button attention-split__toggle"
            aria-label="More auto mode options"
            aria-disabled={busy}
          >
            <ChevronDownIcon className="attention-split__chevron" aria-hidden="true" />
          </summary>
          <div className="attention-split__menu" role="menu" aria-label="Auto mode scope">
            <button
              type="button"
              role="menuitem"
              className="attention-split__item"
              disabled={busy}
              title={`${why} Allows this command now and turns auto mode on for all features.`}
              onClick={() => answer('workspace')}
            >
              Enable auto mode (all features)
              <small>Safe commands run; a reviewer model screens the rest</small>
            </button>
          </div>
        </details>
      ) : null}
    </div>
  );
}

export function AttentionDetail({
  item,
  busy,
  submit,
  saveDraft,
  onJump,
  drafts,
  setDrafts,
}: {
  item: AttentionItem;
  busy: boolean;
  submit(action: AttentionAction, options?: AttentionSubmitOptions): void;
  saveDraft?(action: AttentionAction, options?: AttentionSubmitOptions): Promise<void>;
  onJump?: (featureId: string) => void;
  drafts: AttentionDrafts;
  setDrafts: Dispatch<SetStateAction<AttentionDrafts>>;
}) {
  const detailKey = `${item.kind}:${item.id}`;
  const helpText = drafts.help[detailKey] ?? '';
  const questionDraft = drafts.questions[detailKey] ?? {};

  if (item.kind === 'permission')
    return (
      <div className="attention-detail">
        <AttentionContextMeta item={item} />
        <p className="attention-detail__summary">
          <strong>{item.toolName}</strong>
          {item.summary === undefined ? '' : ` - ${item.summary}`}
        </p>
        <pre aria-label="Structured tool input">{JSON.stringify(item.input ?? {}, null, 2)}</pre>
        {item.remember !== undefined ? (
          <p className="attention-detail__remember">
            Remember preview: <code>{item.remember.pattern}</code> in{' '}
            <code>{item.remember.scopeDisplay}</code>
          </p>
        ) : null}
        <div className="attention-detail__actions">
          <button
            className="attention-button attention-button--primary"
            disabled={busy}
            onClick={() =>
              submit(() =>
                window.agentico.answerPermission({
                  requestId: item.id,
                  ...(item.sessionId === undefined ? {} : { sessionId: item.sessionId }),
                  decision: 'allow_once',
                }),
              )
            }
          >
            Allow once
          </button>
          <button
            className="attention-button"
            disabled={busy}
            onClick={() =>
              submit(() =>
                window.agentico.answerPermission({
                  requestId: item.id,
                  ...(item.sessionId === undefined ? {} : { sessionId: item.sessionId }),
                  decision: 'deny',
                }),
              )
            }
          >
            Deny
          </button>
          {item.autoApprove !== undefined ? (
            <AutoModeSplitButton item={item} busy={busy} submit={submit} />
          ) : null}
          {item.remember !== undefined ? (
            <button
              className="attention-button"
              disabled={busy}
              onClick={() =>
                submit(() =>
                  window.agentico.answerPermission({
                    requestId: item.id,
                    ...(item.sessionId === undefined ? {} : { sessionId: item.sessionId }),
                    decision: 'allow_remember',
                    rememberPattern: item.remember!.pattern,
                    rememberScope: item.remember!.scope,
                  }),
                )
              }
            >
              Allow and remember {item.remember.pattern} ({item.remember.scopeDisplay})
            </button>
          ) : null}
          <AttentionJumpAction item={item} onJump={onJump} />
        </div>
      </div>
    );

  if (item.kind === 'questions') {
    const complete = item.questions.every(
      (question) => questionAnswer(questionDraft[question.key]) !== '',
    );
    const submitAnswers = () =>
      submit(() =>
        window.agentico.answerQuestions({
          requestId: item.id,
          ...(item.sessionId === undefined ? {} : { sessionId: item.sessionId }),
          answers: Object.fromEntries(
            item.questions.map((question) => [
              question.key,
              questionAnswer(questionDraft[question.key]),
            ]),
          ),
        }),
      );
    return (
      <div className="attention-detail attention-detail--questions">
        <AttentionContextMeta item={item} />
        {item.questions.map((question, questionIndex) => {
          const draft = questionDraft[question.key] ?? { selected: [], freeText: '' };
          let recommendedIndex = -1;
          let highestConfidence = Number.NEGATIVE_INFINITY;
          if (!question.multiSelect) {
            question.options.forEach((option, optionIndex) => {
              if (option.confidence !== undefined && option.confidence > highestConfidence) {
                highestConfidence = option.confidence;
                recommendedIndex = optionIndex;
              }
            });
          }
          const freeTextHintId = `${detailKey}:${questionIndex}:free-text-hint`;
          const chooseOption = (label: string, checked: boolean) =>
            setQuestionDraft(setDrafts, detailKey, question.key, {
              selected: question.multiSelect
                ? checked
                  ? [...new Set([...draft.selected, label])]
                  : draft.selected.filter((value) => value !== label)
                : [label],
              freeText: '',
            });
          const handleKeys = (event: ReactKeyboardEvent<HTMLFieldSetElement>) => {
            if (event.metaKey || event.ctrlKey || event.altKey) return;
            const target = event.target as HTMLElement;
            const typing = target instanceof HTMLInputElement && target.type === 'text';
            if (event.key === 'Enter') {
              if (!complete || busy) return;
              event.preventDefault();
              submitAnswers();
              return;
            }
            if (typing) return;
            const digit = Number.parseInt(event.key, 10);
            if (!Number.isInteger(digit) || digit < 1) return;
            if (digit <= question.options.length) {
              event.preventDefault();
              const option = question.options[digit - 1]!;
              chooseOption(option.label, !draft.selected.includes(option.label));
            } else if (digit === question.options.length + 1) {
              event.preventDefault();
              event.currentTarget
                .querySelector<HTMLInputElement>('.attention-free-text__input')
                ?.focus();
            }
          };
          return (
            <fieldset key={question.key} className="attention-question" onKeyDown={handleKeys}>
              <legend>
                {item.questions.length > 1 ? (
                  <span className="attention-question__progress">
                    Question {questionIndex + 1} of {item.questions.length}
                  </span>
                ) : null}
                <span className="attention-question__header">{question.header}</span>
                <span className="attention-question__prompt" role="heading" aria-level={3}>
                  {question.key}
                </span>
              </legend>
              {question.multiSelect ? (
                <p className="attention-question__cue">Choose every option that applies.</p>
              ) : null}
              {question.options.map((option, optionIndex) => {
                const recommended = optionIndex === recommendedIndex;
                const selected =
                  draft.freeText.trim() === '' && draft.selected.includes(option.label);
                return (
                  <label
                    key={option.label}
                    className="attention-option"
                    data-recommended={recommended ? true : undefined}
                    data-selected={selected ? true : undefined}
                  >
                    <input
                      className="sr-only"
                      type={question.multiSelect ? 'checkbox' : 'radio'}
                      name={`${detailKey}:${question.key}`}
                      value={option.label}
                      checked={draft.selected.includes(option.label)}
                      onChange={(event) => chooseOption(option.label, event.currentTarget.checked)}
                    />
                    <span className="attention-option__number" aria-hidden="true">
                      {optionIndex + 1}
                    </span>
                    <span className="attention-option__copy">
                      <span className="attention-option__heading">
                        <span className="attention-option__label">
                          {displayQuestionOptionLabel(option.label)}
                        </span>
                        {recommended ? (
                          <span className="attention-option__recommended">Recommended</span>
                        ) : null}
                      </span>
                      {option.description === undefined ? null : (
                        <span className="attention-option__description">{option.description}</span>
                      )}
                    </span>
                  </label>
                );
              })}
              <label
                className="attention-option attention-option--other"
                data-selected={draft.freeText.trim() === '' ? undefined : true}
              >
                <span className="attention-option__number" aria-hidden="true">
                  {question.options.length + 1}
                </span>
                <span className="attention-option__copy">
                  <span className="attention-option__label">Other</span>
                  <input
                    className="attention-free-text__input"
                    aria-label={`${question.header} free text`}
                    aria-describedby={freeTextHintId}
                    placeholder="Type your own answer here"
                    value={draft.freeText}
                    onChange={(event) =>
                      setQuestionDraft(setDrafts, detailKey, question.key, {
                        freeText: event.target.value,
                      })
                    }
                  />
                  <span id={freeTextHintId} className="attention-free-text__hint">
                    Typing here replaces your selection.
                  </span>
                </span>
              </label>
            </fieldset>
          );
        })}
        <div className="attention-detail__actions">
          <AttentionJumpAction item={item} onJump={onJump} />
          <button
            className="attention-button attention-button--primary"
            disabled={busy || !complete}
            onClick={submitAnswers}
          >
            Submit <span aria-hidden="true">↵</span>
          </button>
        </div>
      </div>
    );
  }

  if (item.kind === 'help') {
    // A harness wait is not a question: the turn ended and the runtime is
    // coordinating. The reply box stays — a message is a legitimate unblock.
    const waiting = item.waitingKind === 'input' || item.waitingKind === 'coordinating';
    return (
      <div className="attention-detail">
        <AttentionContextMeta item={item} />
        {waiting ? (
          <>
            <p className="attention-detail__summary">Agent is waiting</p>
            <p className="attention-detail__hint">
              The agent finished its turn; the runtime is coordinating next steps.
            </p>
            {item.runningTasks !== undefined && item.runningTasks.length > 0 ? (
              <ul className="attention-detail__tasks" aria-label="Running background tasks">
                {item.runningTasks.map((task) => (
                  <li key={task}>{task}</li>
                ))}
              </ul>
            ) : null}
          </>
        ) : (
          <p className="attention-detail__summary">{item.prompt}</p>
        )}
        <textarea
          aria-label={waiting ? 'Message to the agent' : 'Help reply'}
          value={helpText}
          onChange={(event) =>
            setDrafts((current) => ({
              ...current,
              help: { ...current.help, [detailKey]: event.target.value },
            }))
          }
        />
        <div className="attention-detail__actions">
          <button
            className="attention-button attention-button--primary"
            disabled={busy || helpText.trim() === ''}
            onClick={() =>
              submit(() =>
                window.agentico.sendHelp({
                  ...(item.featureId === undefined ? {} : { featureId: item.featureId }),
                  ...(item.sessionId === undefined ? {} : { sessionId: item.sessionId }),
                  message: helpText,
                }),
              )
            }
          >
            {waiting ? 'Send message' : 'Send reply'}
          </button>
          <AttentionJumpAction item={item} onJump={onJump} />
        </div>
      </div>
    );
  }

  if (item.kind === 'gate') {
    const gateDraft = gateDraftFor(item, drafts.gates[detailKey]);
    const structuredVerification = hasStructuredVerificationDecision(item);
    const verificationQuestion = structuredVerification ? item.questions[0] : undefined;
    const selectedVerificationAction =
      verificationQuestion === undefined
        ? ''
        : ((gateDraft[verificationQuestion.index] ?? '') as VerificationGateAction | '');
    const complete = structuredVerification
      ? selectedVerificationAction === 'RETRY_AFTER_AUTH' || selectedVerificationAction === 'WAIVE'
      : item.questions.every((q) => (gateDraft[q.index] ?? '').trim() !== '');
    return (
      <div className="attention-detail">
        <AttentionContextMeta item={item} />
        <p className="attention-detail__summary">{item.summary ?? 'Input required'}</p>
        {structuredVerification && verificationQuestion !== undefined ? (
          <NeedUserInputVerificationDecision
            item={item}
            selectedAction={selectedVerificationAction}
            idPrefix={detailKey}
            onSelect={(action) => {
              const nextDraft = {
                ...gateDraft,
                [verificationQuestion.index]: action,
              };
              setDrafts((current) => ({
                ...current,
                gates: {
                  ...current.gates,
                  [detailKey]: nextDraft,
                },
              }));
              void saveDraft?.(() => saveGateDraftForItem(item, nextDraft), {
                successNotice: 'Draft saved.',
              }).catch(() => undefined);
            }}
          />
        ) : (
          item.questions.map((question) => (
            <label key={question.index} className="attention-gate-question">
              <span>
                {question.index}. {question.prompt}
              </span>
              <textarea
                value={gateDraft[question.index] ?? ''}
                onChange={(event) => {
                  const value = event.target.value;
                  setDrafts((current) => ({
                    ...current,
                    gates: {
                      ...current.gates,
                      [detailKey]: {
                        ...gateDraftFor(item, current.gates[detailKey]),
                        [question.index]: value,
                      },
                    },
                  }));
                }}
                onBlur={() => {
                  void saveDraft?.(() => saveGateDraftForItem(item, gateDraft), {
                    successNotice: 'Draft saved.',
                  }).catch(() => undefined);
                }}
              />
            </label>
          ))
        )}
        {!structuredVerification && !complete ? (
          <p className="attention-detail__hint" id={`${detailKey}-resume-hint`}>
            Answer every question before resuming.
          </p>
        ) : null}
        <div className="attention-detail__actions">
          <button
            className="attention-button attention-button--primary"
            data-tone={
              structuredVerification && selectedVerificationAction === 'WAIVE'
                ? 'warning'
                : undefined
            }
            disabled={busy || !complete}
            aria-describedby={
              !structuredVerification && !complete ? `${detailKey}-resume-hint` : undefined
            }
            onClick={() =>
              submit(async () => {
                if (saveDraft !== undefined) {
                  await saveDraft(() => saveGateDraftForItem(item, gateDraft), {
                    successNotice: 'Draft saved.',
                  });
                }
                return window.agentico.resolveGate({
                  featureId: item.featureId,
                  ...(item.repoName === undefined ? {} : { repoName: item.repoName }),
                });
              })
            }
          >
            {structuredVerification
              ? selectedVerificationAction === 'WAIVE'
                ? 'Waive and resume'
                : 'Retry verification'
              : 'Resume'}
          </button>
          <AttentionJumpAction item={item} onJump={onJump} />
        </div>
      </div>
    );
  }

  if (item.kind === 'review')
    return (
      <div className="attention-detail">
        <AttentionContextMeta item={item} />
        <p className="attention-detail__summary">
          {item.reviewKind} review is waiting at {item.phase}.
        </p>
        <div className="attention-detail__actions">
          <button
            type="button"
            className="attention-button attention-button--primary"
            onClick={() => onJump?.(item.featureId)}
          >
            Open review
          </button>
        </div>
      </div>
    );

  if (item.kind === 'recovery')
    return (
      <div className="attention-detail">
        <p className="attention-detail__summary">
          {item.liveCount > 0
            ? `${item.liveCount} live orphan process${item.liveCount === 1 ? '' : 'es'} need recovery.`
            : `${item.deadCount} dead orphan session${item.deadCount === 1 ? '' : 's'} need cleanup.`}
        </p>
        <p className="attention-detail__hint">
          Recovery is contextual priority — unrelated features remain usable.
        </p>
        <div className="attention-detail__actions">
          <button
            type="button"
            className="attention-button attention-button--primary"
            onClick={() => onJump?.('__recovery__')}
          >
            Open recovery
          </button>
        </div>
      </div>
    );

  const exhaustive: never = item;
  throw new Error(`Unhandled attention item: ${JSON.stringify(exhaustive)}`);
}

/** Header for every blocking banner: what the agent needs, plus provenance
 * (session, phase, and since-when). */
function AttentionContextMeta({ item }: { item: AttentionItem }) {
  if (item.kind === 'recovery') return null;
  const entries: string[] = [];
  if ('sessionId' in item && item.sessionId !== undefined && item.sessionId !== CHAT_SESSION_ID) {
    entries.push(`session ${shortSessionId(item.sessionId)}`);
  }
  if ('phase' in item && item.phase !== undefined) entries.push(item.phase);
  if (item.kind === 'gate') {
    if (item.iteration !== undefined) entries.push(`iteration ${item.iteration}`);
    if (item.repoName !== undefined) entries.push(item.repoName);
  }
  entries.push(formatWaitingSince(item.waitingSince));
  return (
    <header className="attention-ask">
      <span className="attention-ask__eyebrow">{attentionAskLabel(item)}</span>
      <div className="attention-detail__meta" aria-label="Attention context">
        {entries.map((entry, index) => (
          <span key={`${index}:${entry}`}>{entry}</span>
        ))}
      </div>
    </header>
  );
}

function attentionAskLabel(item: Exclude<AttentionItem, { kind: 'recovery' }>): string {
  switch (item.kind) {
    case 'questions':
      return item.questions.length === 1 ? 'Agent question' : 'Agent questions';
    case 'permission':
      return 'Permission request';
    case 'help':
      return item.waitingKind === 'question' ? 'Help request' : 'Agent waiting';
    case 'gate':
      return 'Input needed';
    case 'review':
      return 'Review waiting';
    default: {
      const exhaustive: never = item;
      return exhaustive;
    }
  }
}

function AttentionJumpAction({
  item,
  onJump,
}: {
  item: AttentionItem;
  onJump?: (featureId: string) => void;
}) {
  if (item.kind === 'recovery' || onJump === undefined) return null;
  if (item.featureId === undefined) return null;
  const featureId = item.featureId;
  return (
    <button className="attention-button attention-button--jump" onClick={() => onJump(featureId)}>
      Open feature
    </button>
  );
}

function handleItemKeyDown(
  event: ReactKeyboardEvent<HTMLButtonElement>,
  id: string,
  focusRelativeItem: (currentId: string, direction: 1 | -1) => void,
): void {
  if (event.key === 'ArrowDown') {
    event.preventDefault();
    focusRelativeItem(id, 1);
  } else if (event.key === 'ArrowUp') {
    event.preventDefault();
    focusRelativeItem(id, -1);
  }
}

function hasActiveModalDialog(): boolean {
  return document.querySelector('[role="dialog"][aria-modal="true"]') !== null;
}

function attentionKindLabel(item: AttentionItem): string {
  switch (item.kind) {
    case 'questions':
      return 'Questions';
    case 'gate':
      return 'Input gate';
    case 'help':
      return item.waitingKind === 'question' ? 'Help request' : 'Agent waiting';
    case 'permission':
      return 'Permission';
    case 'review':
      return 'Review';
    case 'recovery':
      return 'Recovery';
    default: {
      const exhaustive: never = item;
      return exhaustive;
    }
  }
}

function shortSessionId(id: string): string {
  return id.length > 9 ? `${id.slice(0, 8)}…` : id;
}

/** Delegates to the rail's shared duration formatter, keeping one source of truth. */
function formatWaitingSince(value: string): string {
  const duration = formatWaitingDuration(value);
  return duration === null ? 'waiting time unknown' : `waiting ${duration}`;
}

export function displayQuestionOptionLabel(label: string): string {
  return label.replace(/\s+\(recommended\)\s*$/i, '').trim();
}

export function setQuestionDraft(
  setDrafts: Dispatch<SetStateAction<AttentionDrafts>>,
  detailKey: string,
  questionKey: string,
  patch: Partial<QuestionAnswerDraft>,
): void {
  setDrafts((current) => {
    const itemDraft = current.questions[detailKey] ?? {};
    const existing = itemDraft[questionKey] ?? { selected: [], freeText: '' };
    return {
      ...current,
      questions: {
        ...current.questions,
        [detailKey]: {
          ...itemDraft,
          [questionKey]: { ...existing, ...patch },
        },
      },
    };
  });
}

export function questionAnswer(draft: QuestionAnswerDraft | undefined): string {
  if (draft === undefined) return '';
  return draft.freeText.trim() === '' ? draft.selected.join(', ') : draft.freeText;
}

function gateDraftFor(
  item: Extract<AttentionItem, { kind: 'gate' }>,
  draft: Record<number, string> | undefined,
): Record<number, string> {
  if (draft !== undefined) return draft;
  return Object.fromEntries(item.questions.map((question) => [question.index, question.answer]));
}

function gateAnswersForSubmit(
  item: Extract<AttentionItem, { kind: 'gate' }>,
  draft: Record<number, string>,
): Record<string, string> {
  return Object.fromEntries(
    item.questions.map((question) => [String(question.index), draft[question.index] ?? '']),
  );
}

function saveGateDraftForItem(
  item: Extract<AttentionItem, { kind: 'gate' }>,
  draft: Record<number, string>,
): Promise<AttentionActionResult> {
  return window.agentico.saveGateDraft({
    featureId: item.featureId,
    ...(item.repoName === undefined ? {} : { repoName: item.repoName }),
    answers: gateAnswersForSubmit(item, draft),
  });
}
