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

import { useCallback, useMemo, useRef, useState, type Dispatch, type SetStateAction } from 'react';
import type { AttentionItem, VerificationGateAction } from '../../../shared/ipc';
import { useModalDismiss } from '../components/useModalDismiss';
import type { AttentionDrafts } from './AttentionInbox';
import {
  hasStructuredVerificationDecision,
  NeedUserInputVerificationDecision,
} from './NeedUserInputVerificationDecision';
import { parseIpcError } from '../wizard/ipcError';
import { bucketElapsedSince } from './phaseRail';

export type AttentionGate = Extract<AttentionItem, { kind: 'gate' }>;

/**
 * Spelled-out counts read as a sentence rather than a form field, which is
 * what the title is. The gate caps its questions well inside this range;
 * numerals are the honest fallback past it rather than a wrong word.
 */
const SPELLED_COUNTS = [
  'zero',
  'one',
  'two',
  'three',
  'four',
  'five',
  'six',
  'seven',
  'eight',
  'nine',
  'ten',
  'eleven',
  'twelve',
];

/** "Answer two questions to resume" — the ask and the outcome in one line. */
export function gateSheetTitle(questionCount: number): string {
  if (questionCount === 0) return 'Resume the paused agent';
  const count = SPELLED_COUNTS[questionCount] ?? String(questionCount);
  return `Answer ${count} question${questionCount === 1 ? '' : 's'} to resume`;
}

/**
 * The lede's first sentence, built only from facts actually present: the
 * phase comes from the cockpit's snapshot, the iteration and stop time from
 * the gate item. Any of them can be missing, so the sentence drops whole
 * rather than naming a placeholder.
 */
export function gateStoppedSentence(inputs: {
  phase?: string;
  iteration?: number;
  waitingSince?: string;
}): string | undefined {
  const since = formatStoppedAgo(inputs.waitingSince);
  if (since === undefined) return undefined;
  const subject = gateStoppedSubject(inputs.phase, inputs.iteration);
  if (subject === undefined) return undefined;
  return `${subject} stopped ${since}.`;
}

function gateStoppedSubject(phase: string | undefined, iteration: number | undefined) {
  if (phase !== undefined && iteration !== undefined) return `${phase} #${iteration}`;
  if (phase !== undefined) return phase;
  if (iteration !== undefined) return `Iteration ${iteration}`;
  return undefined;
}

/** The rail's buckets in prose, so `4h` there and `4 hours ago` here agree. */
function formatStoppedAgo(waitingSince: string | undefined): string | undefined {
  const bucket = bucketElapsedSince(waitingSince);
  if (bucket === null) return undefined;
  switch (bucket.unit) {
    case 'sub-minute':
      return 'moments ago';
    case 'minutes':
      return plural(bucket.value, 'minute');
    case 'hours':
      return plural(bucket.value, 'hour');
    case 'days':
      return plural(bucket.value, 'day');
  }
}

function plural(value: number, unit: string): string {
  return `${value} ${unit}${value === 1 ? '' : 's'} ago`;
}

export interface NeedUserInputModalProps {
  item: AttentionGate;
  busy: boolean;
  drafts: AttentionDrafts;
  setDrafts: Dispatch<SetStateAction<AttentionDrafts>>;
  /** The run's current phase, for the lede. Absent when it isn't known. */
  phase?: string;
  onAnswerLater(): void;
  onResolved(): Promise<void>;
}

/**
 * The cycle gate as a window-modal sheet: the scrim covers the whole window
 * and the sheet descends from its top edge, so the hold reads as something
 * the window is waiting on rather than an app-blocking dialog. Answering
 * later is always available and always saves, which is what the footer's
 * `Saved as you type` note is there to make believable.
 */
export function NeedUserInputModal({
  item,
  busy,
  drafts,
  setDrafts,
  phase,
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
  // A snapshot can carry an empty phase before the run names one; that is a
  // missing fact, not a phase called "".
  const gatePhase = phase === undefined || phase.trim() === '' ? undefined : phase;
  const stopped = gateStoppedSentence({
    ...(gatePhase === undefined ? {} : { phase: gatePhase }),
    ...(item.iteration === undefined ? {} : { iteration: item.iteration }),
    waitingSince: item.waitingSince,
  });

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
    <div className="sheet-scrim" onMouseDown={answerLater}>
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={`${detailKey}-title`}
        className="sheet need-input-sheet"
        tabIndex={-1}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="sheet__body">
          <header className="need-input-sheet__header">
            <p className="need-input-sheet__eyebrow">
              <span aria-hidden="true">{'⚠'}</span> Agent paused · Input required
            </p>
            <h2 id={`${detailKey}-title`} className="need-input-sheet__title">
              {structuredVerification
                ? 'Verification needs your input'
                : gateSheetTitle(item.questions.length)}
            </h2>
            {structuredVerification ? (
              <p className="need-input-sheet__lede">
                {item.verification!.blockers.length} required check(s) could not run.
              </p>
            ) : (
              <p className="need-input-sheet__lede">
                {stopped === undefined ? null : `${stopped} `}
                It resumes from the same checkpoint — nothing is re-run.
              </p>
            )}
            {!structuredVerification && item.summary !== undefined ? (
              <p className="need-input-sheet__summary">{item.summary}</p>
            ) : null}
          </header>

          <div className="need-input-sheet__questions">
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
                <label key={question.index} className="need-input-sheet__question">
                  <span>
                    <small>Question {question.index}</small>
                    <strong>{question.prompt}</strong>
                  </span>
                  <textarea
                    autoFocus={question.index === item.questions[0]?.index}
                    placeholder="Type your answer"
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
            <p id={`${detailKey}-hint`} className="need-input-sheet__hint">
              Answer every question before resuming.
            </p>
          ) : null}
          {error === null ? null : (
            <p role="alert" className="form-field__error">
              {error}
            </p>
          )}
        </div>

        <footer className="sheet__footer">
          <button type="button" className="sheet__footer-secondary" onClick={answerLater}>
            Answer later
          </button>
          <span className="sheet__footer-note">Saved as you type</span>
          <button
            type="button"
            className="sheet__footer-primary"
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
