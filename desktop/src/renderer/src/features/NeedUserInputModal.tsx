import {
  useCallback,
  useMemo,
  useRef,
  useState,
  type Dispatch,
  type SetStateAction,
} from 'react';
import type { AttentionItem } from '../../../shared/ipc';
import { useModalDismiss } from '../components/useModalDismiss';
import type { AttentionDrafts } from './AttentionInbox';
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
  const complete = item.questions.every((question) => (draft[question.index] ?? '').trim() !== '');

  const saveDraft = useCallback(async (): Promise<void> => {
    if (item.questions.length === 0) return;
    await window.agentico.saveGateDraft({
      featureId: item.featureId,
      ...(item.repoName === undefined ? {} : { repoName: item.repoName }),
      ...(item.cycleType === undefined ? {} : { cycleType: item.cycleType }),
      answers: Object.fromEntries(
        item.questions.map((question) => [question.prompt, draft[question.index] ?? '']),
      ),
    });
  }, [draft, item]);

  const answerLater = useCallback(() => {
    void saveDraft().catch(() => undefined);
    onAnswerLater();
  }, [onAnswerLater, saveDraft]);

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
        ...(item.cycleType === undefined ? {} : { cycleType: item.cycleType }),
        decision: 'resume',
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
          <h2 id={`${detailKey}-title`}>Agent needs your input</h2>
          <p>
            {item.summary ??
              'Answer the questions below, then resume the agent from the same checkpoint.'}
          </p>
        </header>

        <div className="need-input-modal__questions">
          {item.questions.map((question) => (
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
          ))}
        </div>

        {!complete ? (
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
            disabled={!complete || busy || submitting}
            aria-describedby={!complete ? `${detailKey}-hint` : undefined}
            onClick={() => void resume()}
          >
            {submitting || busy ? 'Resuming…' : 'Resume agent'}
          </button>
        </footer>
      </div>
    </div>
  );
}
