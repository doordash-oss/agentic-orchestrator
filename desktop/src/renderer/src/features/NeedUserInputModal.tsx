import { useCallback, useMemo, useRef, useState, type Dispatch, type SetStateAction } from 'react';
import type { AttentionItem, VerificationGateAction } from '../../../shared/ipc';
import { useModalDismiss } from '../components/useModalDismiss';
import type { AttentionDrafts } from './AttentionInbox';
import {
  hasStructuredVerificationDecision,
  NeedUserInputVerificationDecision,
} from './NeedUserInputVerificationDecision';
import { parseIpcError } from '../wizard/ipcError';

export type AttentionGate = Extract<AttentionItem, { kind: 'gate' }>;

export interface NeedUserInputModalProps {
  item: AttentionGate;
  busy: boolean;
  drafts: AttentionDrafts;
  setDrafts: Dispatch<SetStateAction<AttentionDrafts>>;
  onAnswerLater(): void;
  onResolved(): Promise<void>;
}

export function NeedUserInputModal({
  item,
  busy,
  drafts,
  setDrafts,
  onAnswerLater,
  onResolved,
}: NeedUserInputModalProps): React.ReactElement {
  const dialogRef = useRef<HTMLDivElement>(null);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const detailKey = `gate:${item.id}`;
  const draft = useMemo(
    () =>
      drafts.gates[detailKey] ??
      Object.fromEntries(item.questions.map((question) => [question.index, question.answer])),
    [detailKey, drafts.gates, item.questions],
  );
  const itemRef = useRef(item);
  itemRef.current = item;
  const draftRef = useRef(draft);
  draftRef.current = draft;
  const saveQueueRef = useRef<Promise<void>>(Promise.resolve());
  const answerLaterRef = useRef(onAnswerLater);
  answerLaterRef.current = onAnswerLater;
  const structuredVerification = hasStructuredVerificationDecision(item);
  const verificationQuestion = structuredVerification ? item.questions[0] : undefined;
  const selectedVerificationAction =
    verificationQuestion === undefined
      ? ''
      : ((draft[verificationQuestion.index] ?? '') as VerificationGateAction | '');
  const complete = structuredVerification
    ? selectedVerificationAction === 'RETRY_AFTER_AUTH' || selectedVerificationAction === 'WAIVE'
    : item.questions.every((question) => (draft[question.index] ?? '').trim() !== '');

  const saveDraft = useCallback(
    (currentItem = itemRef.current, currentDraft = draftRef.current): Promise<void> => {
      const run = saveQueueRef.current
        .catch(() => undefined)
        .then(async () => {
          if (currentItem.questions.length === 0) return;
          await window.agentico.saveGateDraft({
            featureId: currentItem.featureId,
            ...(currentItem.repoName === undefined ? {} : { repoName: currentItem.repoName }),
            answers: Object.fromEntries(
              currentItem.questions.map((question) => [
                String(question.index),
                currentDraft[question.index] ?? '',
              ]),
            ),
          });
        });
      saveQueueRef.current = run;
      return run;
    },
    [],
  );

  const answerLater = useCallback(() => {
    void saveDraft().catch(() => undefined);
    answerLaterRef.current();
  }, [saveDraft]);

  useModalDismiss(dialogRef, answerLater);

  const resume = async (): Promise<void> => {
    if (!complete || busy || submitting) return;
    setSubmitting(true);
    setError(null);
    try {
      await saveDraft();
      await window.agentico.resolveGate({
        featureId: item.featureId,
        ...(item.repoName === undefined ? {} : { repoName: item.repoName }),
      });
      await onResolved();
    } catch (cause) {
      setError(parseIpcError(cause).message);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="cockpit__modal-backdrop need-input-modal__backdrop" onMouseDown={answerLater}>
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={`${detailKey}-title`}
        className="cockpit__modal need-input-modal"
        tabIndex={-1}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="need-input-modal__header">
          <p className="post-workspace__eyebrow">Agent paused · Input required</p>
          <h2 id={`${detailKey}-title`}>
            {structuredVerification ? 'Verification needs your input' : 'Agent needs your input'}
          </h2>
          <p>
            {structuredVerification
              ? `${item.verification!.blockers.length} required check(s) could not run.`
              : (item.summary ??
                'Answer the questions below, then resume the agent from the same checkpoint.')}
          </p>
        </header>

        <div className="need-input-modal__questions">
          {structuredVerification && verificationQuestion !== undefined ? (
            <NeedUserInputVerificationDecision
              item={item}
              selectedAction={selectedVerificationAction}
              idPrefix={detailKey}
              onSelect={(action) => {
                const nextDraft = {
                  ...draft,
                  [verificationQuestion.index]: action,
                };
                draftRef.current = nextDraft;
                setDrafts((current) => ({
                  ...current,
                  gates: {
                    ...current.gates,
                    [detailKey]: nextDraft,
                  },
                }));
                void saveDraft(item, nextDraft).catch(() => undefined);
              }}
            />
          ) : (
            item.questions.map((question) => (
              <label key={question.index} className="need-input-modal__question">
                <span>
                  <small>Question {question.index}</small>
                  <strong>{question.prompt}</strong>
                </span>
                <textarea
                  autoFocus={question.index === item.questions[0]?.index}
                  value={draft[question.index] ?? ''}
                  onChange={(event) => {
                    const value = event.target.value;
                    setDrafts((current) => ({
                      ...current,
                      gates: {
                        ...current.gates,
                        [detailKey]: {
                          ...draft,
                          [question.index]: value,
                        },
                      },
                    }));
                  }}
                  onBlur={() => void saveDraft().catch(() => undefined)}
                />
              </label>
            ))
          )}
        </div>

        {!structuredVerification && !complete ? (
          <p id={`${detailKey}-hint`} className="need-input-modal__hint">
            Answer every question before resuming.
          </p>
        ) : null}
        {error === null ? null : (
          <p role="alert" className="form-field__error">
            {error}
          </p>
        )}

        <footer className="need-input-modal__footer">
          <button type="button" className="need-input-modal__later" onClick={answerLater}>
            Answer later
          </button>
          <button
            type="button"
            className="need-input-modal__primary"
            data-tone={
              structuredVerification && selectedVerificationAction === 'WAIVE'
                ? 'warning'
                : undefined
            }
            disabled={!complete || busy || submitting}
            aria-describedby={
              !structuredVerification && !complete ? `${detailKey}-hint` : undefined
            }
            onClick={() => void resume()}
          >
            {submitting || busy
              ? 'Resuming…'
              : structuredVerification
                ? selectedVerificationAction === 'WAIVE'
                  ? 'Waive and resume'
                  : 'Retry verification'
                : 'Resume agent'}
          </button>
        </footer>
      </div>
    </div>
  );
}
