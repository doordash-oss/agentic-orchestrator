import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type Dispatch,
  type KeyboardEvent as ReactKeyboardEvent,
  type SetStateAction,
} from 'react';
import {
  ATTENTION_ALREADY_RESOLVED_NOTICE,
  ATTENTION_SUBMITTED_NOTICE,
  type AttentionActionResult,
  type AttentionItem,
} from '../../../shared/ipc';
import { useAttentionDraftSaves } from './useAttentionDraftSaves';

interface QuestionAnswerDraft {
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
  onJump(featureId: string): void;
  openRequest?: { id: number; attentionId?: string } | null;
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

/** Global, snapshot-only blocking-input inbox. Draft text stays in renderer
 * session state until submitted; server-owned gate drafts are persisted on blur. */
export function AttentionInbox({
  items,
  refresh,
  featureLabel,
  drafts,
  setDrafts,
  onJump,
  openRequest = null,
}: AttentionInboxProps) {
  const [open, setOpen] = useState(false);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [notice, setNotice] = useState('');
  const [pendingFocus, setPendingFocus] = useState<string | null>(null);
  const bell = useRef<HTMLButtonElement>(null);
  const itemButtons = useRef(new Map<string, HTMLButtonElement>());

  useEffect(() => {
    if (openRequest === null) return;
    setOpen(true);
    if (openRequest.attentionId !== undefined) {
      setExpanded(openRequest.attentionId);
      setPendingFocus(openRequest.attentionId);
    }
  }, [openRequest]);

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

  useEffect(() => {
    const key = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.shiftKey && event.key.toLowerCase() === 'a') {
        event.preventDefault();
        setOpen(true);
      }
      if (event.key === 'Escape' && open) {
        if (document.querySelector('[data-attention-confirm="true"]') !== null) return;
        setOpen(false);
        bell.current?.focus();
      }
    };
    window.addEventListener('keydown', key);
    return () => window.removeEventListener('keydown', key);
  }, [open]);

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

  const actionableCount = items.filter((i) => i.kind !== 'recovery').length;

  return (
    <>
      <button
        ref={bell}
        type="button"
        className="attention-bell"
        data-empty={actionableCount === 0}
        aria-label={`Attention inbox, ${actionableCount} pending`}
        aria-expanded={open}
        aria-controls="attention-inbox"
        onClick={() => setOpen(true)}
      >
        <span className="attention-bell__glyph" aria-hidden="true">
          !
        </span>
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
      {open ? (
        <aside
          id="attention-inbox"
          className="attention-inbox"
          aria-label="Attention inbox"
          tabIndex={-1}
        >
          <header className="attention-inbox__header">
            <div>
              <p className="attention-inbox__eyebrow">Blocking input</p>
              <h2>Attention inbox</h2>
            </div>
            <button
              type="button"
              className="attention-button"
              onClick={() => {
                setOpen(false);
                bell.current?.focus();
              }}
            >
              Close inbox
            </button>
          </header>
          {notice !== '' ? (
            <p className="attention-status" role="status" aria-live="polite">
              {notice}
            </p>
          ) : null}
          {items.length === 0 ? (
            <p className="attention-inbox__empty" role="status">
              No blocking input is waiting.
            </p>
          ) : (
            <ul className="attention-inbox__list">
              {items.map((item) => (
                <li key={`${item.kind}:${item.id}`} className="attention-inbox__row">
                  <button
                    ref={(node) => {
                      if (node === null) itemButtons.current.delete(item.id);
                      else itemButtons.current.set(item.id, node);
                    }}
                    type="button"
                    className="attention-inbox__item"
                    aria-expanded={expanded === item.id}
                    onClick={() => setExpanded(expanded === item.id ? null : item.id)}
                    onKeyDown={(event) => handleItemKeyDown(event, item.id, focusRelativeItem)}
                  >
                    <span className="attention-inbox__item-main">
                      <span className="attention-inbox__kind">{attentionKindLabel(item)}</span>
                      <span className="attention-inbox__feature">
                        {item.kind === 'recovery'
                          ? 'Recovery workspace'
                          : featureLabel(item.featureId)}
                      </span>
                    </span>
                    <span className="attention-inbox__waiting">
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
        </aside>
      ) : null}
    </>
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
  const [confirmingAbort, setConfirmingAbort] = useState(false);
  const abortButtonRef = useRef<HTMLButtonElement>(null);
  const detailKey = `${item.kind}:${item.id}`;
  const helpText = drafts.help[detailKey] ?? '';
  const questionDraft = drafts.questions[detailKey] ?? {};

  const closeAbortConfirm = useCallback(() => {
    setConfirmingAbort(false);
    abortButtonRef.current?.focus();
  }, []);

  useEffect(() => {
    if (!confirmingAbort) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        closeAbortConfirm();
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [closeAbortConfirm, confirmingAbort]);

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

  if (item.kind === 'questions')
    return (
      <div className="attention-detail">
        <AttentionContextMeta item={item} />
        {item.questions.map((question) => {
          const draft = questionDraft[question.key] ?? { selected: [], freeText: '' };
          return (
            <fieldset key={question.key} className="attention-question">
              <legend>
                <span className="attention-question__header">{question.header}</span>
                <span className="attention-question__prompt">{question.key}</span>
              </legend>
              {question.options.map((option) => (
                <label key={option.label} className="attention-option">
                  <input
                    type={question.multiSelect ? 'checkbox' : 'radio'}
                    name={`${detailKey}:${question.key}`}
                    value={option.label}
                    checked={draft.selected.includes(option.label)}
                    onChange={(event) =>
                      setQuestionDraft(setDrafts, detailKey, question.key, {
                        selected: question.multiSelect
                          ? event.currentTarget.checked
                            ? [...new Set([...draft.selected, option.label])]
                            : draft.selected.filter((value) => value !== option.label)
                          : [option.label],
                      })
                    }
                  />
                  <span className="attention-option__copy">
                    <span className="attention-option__label">{option.label}</span>
                    {option.description === undefined ? null : (
                      <span className="attention-option__description">{option.description}</span>
                    )}
                  </span>
                  {option.confidence === undefined ? null : (
                    <span className="attention-option__confidence">
                      {formatConfidence(option.confidence)}
                    </span>
                  )}
                </label>
              ))}
              <label className="attention-free-text">
                <span>Free text</span>
                <input
                  aria-label={`${question.header} free text`}
                  value={draft.freeText}
                  onChange={(event) =>
                    setQuestionDraft(setDrafts, detailKey, question.key, {
                      freeText: event.target.value,
                    })
                  }
                />
                <span className="attention-free-text__hint">
                  Free text replaces selected options.
                </span>
              </label>
            </fieldset>
          );
        })}
        <div className="attention-detail__actions">
          <button
            className="attention-button attention-button--primary"
            disabled={
              busy || item.questions.some((q) => questionAnswer(questionDraft[q.key]) === '')
            }
            onClick={() =>
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
              )
            }
          >
            Submit answers
          </button>
          <AttentionJumpAction item={item} onJump={onJump} />
        </div>
      </div>
    );

  if (item.kind === 'help')
    return (
      <div className="attention-detail">
        <AttentionContextMeta item={item} />
        <p className="attention-detail__summary">{item.prompt}</p>
        <textarea
          aria-label="Help reply"
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
                  featureId: item.featureId,
                  ...(item.sessionId === undefined ? {} : { sessionId: item.sessionId }),
                  message: helpText,
                }),
              )
            }
          >
            Send reply
          </button>
          <AttentionJumpAction item={item} onJump={onJump} />
        </div>
      </div>
    );

  if (item.kind === 'gate') {
    const gateDraft = gateDraftFor(item, drafts.gates[detailKey]);
    const complete = item.questions.every((q) => (gateDraft[q.index] ?? '').trim() !== '');
    return (
      <div className="attention-detail">
        <AttentionContextMeta item={item} />
        <p className="attention-detail__summary">{item.summary ?? 'Input required'}</p>
        {item.questions.map((question) => (
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
        ))}
        {!complete ? (
          <p className="attention-detail__hint" id={`${detailKey}-resume-hint`}>
            Answer every question before resuming.
          </p>
        ) : null}
        <div className="attention-detail__actions">
          <button
            className="attention-button attention-button--primary"
            disabled={busy || !complete}
            aria-describedby={!complete ? `${detailKey}-resume-hint` : undefined}
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
                  ...(item.cycleType === undefined ? {} : { cycleType: item.cycleType }),
                  decision: 'resume',
                });
              })
            }
          >
            Resume
          </button>
          <button
            ref={abortButtonRef}
            className="attention-button attention-button--danger"
            disabled={busy}
            onClick={() => setConfirmingAbort(true)}
          >
            Abort gate
          </button>
          <AttentionJumpAction item={item} onJump={onJump} />
        </div>
        {confirmingAbort ? (
          <div className="attention-confirm__backdrop" data-attention-confirm="true">
            <div
              role="dialog"
              aria-modal="true"
              aria-labelledby={`${detailKey}-abort-title`}
              className="attention-confirm"
            >
              <p className="attention-confirm__eyebrow">Abort gate</p>
              <h3 id={`${detailKey}-abort-title`}>Confirm abort</h3>
              <p>
                {item.repoName === undefined
                  ? 'Abort will terminally fail this feature.'
                  : 'Abort will fail only this repository cycle; sibling cycles continue.'}
              </p>
              <div className="attention-confirm__actions">
                <button
                  className="attention-button"
                  type="button"
                  onClick={closeAbortConfirm}
                  autoFocus
                >
                  Keep working
                </button>
                <button
                  className="attention-button attention-button--danger"
                  type="button"
                  disabled={busy}
                  onClick={() =>
                    submit(() =>
                      window.agentico.resolveGate({
                        featureId: item.featureId,
                        ...(item.repoName === undefined ? {} : { repoName: item.repoName }),
                        ...(item.cycleType === undefined ? {} : { cycleType: item.cycleType }),
                        decision: 'abort',
                      }),
                    )
                  }
                >
                  Confirm abort
                </button>
              </div>
            </div>
          </div>
        ) : null}
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

function AttentionContextMeta({ item }: { item: AttentionItem }) {
  const entries: string[] = [];
  if ('phase' in item && item.phase !== undefined) entries.push(item.phase);
  if (item.kind === 'gate') {
    if (item.iteration !== undefined) entries.push(`iteration ${item.iteration}`);
    if (item.repoName !== undefined) entries.push(item.repoName);
    if (item.cycleType !== undefined) entries.push(item.cycleType);
  }
  if (entries.length === 0) return null;
  return (
    <div className="attention-detail__meta" aria-label="Attention context">
      {entries.map((entry, index) => (
        <span key={`${index}:${entry}`}>{entry}</span>
      ))}
    </div>
  );
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
    <button className="attention-button" onClick={() => onJump(featureId)}>
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
      return 'Help request';
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

function formatWaitingSince(value: string): string {
  const since = Date.parse(value);
  if (!Number.isFinite(since)) return 'waiting time unknown';
  const elapsedMs = Math.max(Date.now() - since, 0);
  const minutes = Math.floor(elapsedMs / 60_000);
  if (minutes < 1) return 'waiting <1m';
  if (minutes < 60) return `waiting ${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 48) return `waiting ${hours}h`;
  return `waiting ${Math.floor(hours / 24)}d`;
}

function formatConfidence(confidence: number): string {
  return `${Math.round(confidence * 100)}%`;
}

function setQuestionDraft(
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

function questionAnswer(draft: QuestionAnswerDraft | undefined): string {
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
    item.questions.map((question) => [question.prompt, draft[question.index] ?? '']),
  );
}

function saveGateDraftForItem(
  item: Extract<AttentionItem, { kind: 'gate' }>,
  draft: Record<number, string>,
): Promise<AttentionActionResult> {
  return window.agentico.saveGateDraft({
    featureId: item.featureId,
    ...(item.repoName === undefined ? {} : { repoName: item.repoName }),
    ...(item.cycleType === undefined ? {} : { cycleType: item.cycleType }),
    answers: gateAnswersForSubmit(item, draft),
  });
}
