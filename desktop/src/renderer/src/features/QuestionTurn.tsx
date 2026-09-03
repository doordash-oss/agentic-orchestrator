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

import type { Dispatch, KeyboardEvent as ReactKeyboardEvent, SetStateAction } from 'react';
import type { AskUserAnswerRequest, AttentionItem } from '../../../shared/ipc';
import {
  displayQuestionOptionLabel,
  questionAnswer,
  setQuestionDraft,
  type AttentionDrafts,
} from './AttentionInbox';

export type QuestionsAttentionItem = Extract<AttentionItem, { kind: 'questions' }>;

export function questionsComplete(item: QuestionsAttentionItem, drafts: AttentionDrafts): boolean {
  const questionDraft = drafts.questions[`${item.kind}:${item.id}`] ?? {};
  return item.questions.every((question) => questionAnswer(questionDraft[question.key]) !== '');
}

export function questionAnswersRequest(
  item: QuestionsAttentionItem,
  drafts: AttentionDrafts,
): AskUserAnswerRequest {
  const questionDraft = drafts.questions[`${item.kind}:${item.id}`] ?? {};
  return {
    requestId: item.id,
    ...(item.sessionId === undefined ? {} : { sessionId: item.sessionId }),
    answers: Object.fromEntries(
      item.questions.map((question) => [question.key, questionAnswer(questionDraft[question.key])]),
    ),
  };
}

interface QuestionTurnProps {
  item: QuestionsAttentionItem;
  busy: boolean;
  drafts: AttentionDrafts;
  setDrafts: Dispatch<SetStateAction<AttentionDrafts>>;
  onSubmit(): void;
}

/**
 * The pending question rendered as the agent's next conversation turn: the
 * prompt and options live inside a message bubble in the transcript, and the
 * chosen answer previews as your drafted reply. Digits choose, Enter sends.
 */
export function QuestionConversationTurn({
  item,
  busy,
  drafts,
  setDrafts,
  onSubmit,
}: QuestionTurnProps) {
  const detailKey = `${item.kind}:${item.id}`;
  const questionDraft = drafts.questions[detailKey] ?? {};
  const complete = questionsComplete(item, drafts);
  const single = item.questions.length === 1;

  return (
    <>
      <div className="question-turn" role="group" aria-label="Agent question">
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
            if (target instanceof HTMLInputElement && target.type === 'text') return;
            if (event.key === 'Enter') {
              if (!complete || busy) return;
              event.preventDefault();
              onSubmit();
              return;
            }
            const digit = Number.parseInt(event.key, 10);
            if (!Number.isInteger(digit) || digit < 1 || digit > question.options.length) return;
            event.preventDefault();
            const option = question.options[digit - 1]!;
            chooseOption(option.label, !draft.selected.includes(option.label));
          };
          return (
            <fieldset key={question.key} className="attention-question" onKeyDown={handleKeys}>
              <legend>
                <span className="question-turn__head">
                  <span className="question-turn__topic">{question.header}</span>
                  {questionIndex === 0 ? (
                    <span className="question-turn__meta">
                      {item.phase !== undefined ? `${item.phase} · ` : ''}
                      {single ? 'waiting on you' : `question 1 of ${item.questions.length}`}
                    </span>
                  ) : null}
                </span>
                <span className="question-turn__prompt" role="heading" aria-level={3}>
                  {question.key}
                </span>
              </legend>
              {question.multiSelect ? (
                <p className="question-turn__cue">Choose every option that applies.</p>
              ) : null}
              {question.options.map((option, optionIndex) => {
                const selected =
                  draft.freeText.trim() === '' && draft.selected.includes(option.label);
                return (
                  <label
                    key={option.label}
                    className="attention-option"
                    data-recommended={optionIndex === recommendedIndex ? true : undefined}
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
                        {optionIndex === recommendedIndex ? (
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
              {single ? null : (
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
                      placeholder="Type your own answer here"
                      value={draft.freeText}
                      onChange={(event) =>
                        setQuestionDraft(setDrafts, detailKey, question.key, {
                          freeText: event.target.value,
                        })
                      }
                    />
                  </span>
                </label>
              )}
            </fieldset>
          );
        })}
      </div>
      {complete ? (
        <div className="question-turn__reply" aria-label="Your reply, not sent yet">
          <span className="question-turn__reply-who">Your reply — not sent yet</span>
          {item.questions.map((question) => {
            const draft = questionDraft[question.key] ?? { selected: [], freeText: '' };
            const text =
              draft.freeText.trim() !== ''
                ? draft.freeText.trim()
                : draft.selected
                    .map((label) => {
                      const index = question.options.findIndex((option) => option.label === label);
                      const display = displayQuestionOptionLabel(label);
                      return index >= 0 ? `${index + 1} · ${display}` : display;
                    })
                    .join(', ');
            return <strong key={question.key}>{text}</strong>;
          })}
        </div>
      ) : null}
    </>
  );
}

/**
 * The composer strip under the live activity: free text answers the single
 * pending question (replacing any selection), Send submits.
 */
export function QuestionComposer({ item, busy, drafts, setDrafts, onSubmit }: QuestionTurnProps) {
  const detailKey = `${item.kind}:${item.id}`;
  const questionDraft = drafts.questions[detailKey] ?? {};
  const complete = questionsComplete(item, drafts);
  const single = item.questions.length === 1 ? item.questions[0] : undefined;
  const draft =
    single === undefined
      ? undefined
      : (questionDraft[single.key] ?? { selected: [], freeText: '' });
  const hasSelection = draft !== undefined && draft.selected.length > 0;

  return (
    <div className="question-composer">
      <span className="question-composer__who">Your answer</span>
      {single !== undefined && draft !== undefined ? (
        <input
          className="question-composer__input"
          aria-label={`${single.header} free text`}
          placeholder={
            hasSelection
              ? 'Option selected — typing your own answer replaces it'
              : 'Choose an option above, or type your own answer'
          }
          value={draft.freeText}
          onChange={(event) =>
            setQuestionDraft(setDrafts, detailKey, single.key, { freeText: event.target.value })
          }
          onKeyDown={(event) => {
            if (event.key !== 'Enter' || !complete || busy) return;
            event.preventDefault();
            onSubmit();
          }}
        />
      ) : (
        <span className="question-composer__hint">Answer each question above, then send.</span>
      )}
      <button
        type="button"
        className="attention-button attention-button--primary"
        disabled={busy || !complete}
        onClick={onSubmit}
      >
        Send <span aria-hidden="true">↵</span>
      </button>
    </div>
  );
}
