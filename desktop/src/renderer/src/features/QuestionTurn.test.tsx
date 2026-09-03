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

import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { emptyAttentionDrafts, type AttentionDrafts } from './AttentionInbox';
import {
  QuestionComposer,
  QuestionConversationTurn,
  questionAnswersRequest,
  type QuestionsAttentionItem,
} from './QuestionTurn';

afterEach(cleanup);

const item: QuestionsAttentionItem = {
  kind: 'questions',
  id: 'questions-1',
  featureId: 'feature-1',
  sessionId: 'session-1',
  phase: 'Inquire',
  waitingSince: '2026-08-01T10:00:00.000Z',
  questions: [
    {
      key: 'Which overall direction should this project take?',
      header: 'Project direction',
      multiSelect: false,
      options: [
        {
          label: 'Harden the review pipeline (Recommended)',
          description: 'Invest in the existing review workflow.',
          confidence: 0.86,
        },
        { label: 'Build user-facing features', confidence: 0.62 },
      ],
    },
  ],
};

function Harness({ onSubmit = vi.fn() }: { onSubmit?: () => void }) {
  const [drafts, setDrafts] = useState<AttentionDrafts>(emptyAttentionDrafts);
  return (
    <>
      <QuestionConversationTurn
        item={item}
        busy={false}
        drafts={drafts}
        setDrafts={setDrafts}
        onSubmit={onSubmit}
      />
      <QuestionComposer
        item={item}
        busy={false}
        drafts={drafts}
        setDrafts={setDrafts}
        onSubmit={onSubmit}
      />
      <DraftProbe drafts={drafts} />
    </>
  );
}

let latestDrafts: AttentionDrafts = emptyAttentionDrafts();
function DraftProbe({ drafts }: { drafts: AttentionDrafts }) {
  latestDrafts = drafts;
  return null;
}

describe('QuestionConversationTurn', () => {
  it('renders the question as a turn with keycap options and previews the reply', async () => {
    const user = userEvent.setup();
    render(<Harness />);

    const turn = screen.getByRole('group', { name: 'Agent question' });
    expect(
      within(turn).getByText('Which overall direction should this project take?'),
    ).toBeVisible();
    expect(within(turn).getByText('Recommended')).toBeVisible();
    // The topic label is the question header alone; the mono note beside it
    // carries the phase and what the agent is waiting for.
    expect(within(turn).getByText('Project direction')).toBeVisible();
    expect(within(turn).getByText('Inquire · waiting on you')).toBeVisible();
    expect(screen.queryByText(/needs your call/)).not.toBeInTheDocument();
    expect(screen.queryByText(/not sent yet/)).not.toBeInTheDocument();

    await user.click(within(turn).getByRole('radio', { name: /Harden the review pipeline/ }));
    expect(screen.getByText('Your reply — not sent yet')).toBeVisible();
    expect(screen.getByText('1 · Harden the review pipeline')).toBeVisible();
  });

  it('selects with number keys and submits with Enter once complete', async () => {
    const onSubmit = vi.fn();
    const user = userEvent.setup();
    render(<Harness onSubmit={onSubmit} />);

    const first = screen.getByRole('radio', { name: /Harden the review pipeline/ });
    first.focus();
    await user.keyboard('{Enter}');
    expect(onSubmit).not.toHaveBeenCalled();
    await user.keyboard('2');
    expect(screen.getByRole('radio', { name: /Build user-facing features/ })).toBeChecked();
    await user.keyboard('{Enter}');
    expect(onSubmit).toHaveBeenCalledTimes(1);
    expect(questionAnswersRequest(item, latestDrafts)).toEqual({
      requestId: 'questions-1',
      sessionId: 'session-1',
      answers: {
        'Which overall direction should this project take?': 'Build user-facing features',
      },
    });
  });
});

describe('QuestionComposer', () => {
  it('free text answers the question, replacing any selection', async () => {
    const onSubmit = vi.fn();
    const user = userEvent.setup();
    render(<Harness onSubmit={onSubmit} />);

    const send = screen.getByRole('button', { name: 'Send' });
    expect(send).toBeDisabled();

    const input = screen.getByLabelText('Project direction free text');
    await user.type(input, 'Focus on speed');
    expect(send).toBeEnabled();
    await user.keyboard('{Enter}');
    expect(onSubmit).toHaveBeenCalledTimes(1);
    expect(questionAnswersRequest(item, latestDrafts)).toEqual({
      requestId: 'questions-1',
      sessionId: 'session-1',
      answers: { 'Which overall direction should this project take?': 'Focus on speed' },
    });

    await user.click(send);
    expect(onSubmit).toHaveBeenCalledTimes(2);
  });
});
