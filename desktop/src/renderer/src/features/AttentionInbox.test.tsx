import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AttentionItem } from '../../../shared/ipc';
import {
  AttentionDetail,
  AttentionInbox,
  emptyAttentionDrafts,
  type AttentionAction,
  type AttentionDrafts,
} from './AttentionInbox';
import { installAgenticoMock } from '../test/agenticoMock';

afterEach(cleanup);

const permissionItem: AttentionItem = {
  kind: 'permission',
  id: 'perm-1',
  featureId: 'feature-1',
  sessionId: 'session-1',
  phase: 'Implement',
  toolName: 'Bash',
  input: { command: 'printf attention' },
  waitingSince: '2026-07-15T10:00:00.000Z',
};

const reviewItem: AttentionItem = {
  kind: 'review',
  id: 'review:feature-1:4:plan:PhasePlanNeedsReview',
  featureId: 'feature-1',
  waitingSince: '2026-07-15T10:00:00.000Z',
  reviewKind: 'Phase plan',
  phase: 'plan',
};

const gateItem: Extract<AttentionItem, { kind: 'gate' }> = {
  kind: 'gate',
  id: 'verification-gate-1',
  featureId: 'abcd1234ef567890',
  waitingSince: '2026-07-29T00:00:00Z',
  repoName: 'repo-a',
  summary: 'Verification could not finish.',
  questions: [{ index: 1, prompt: 'How should Agentico continue?', answer: '' }],
  verification: {
    blockers: [
      {
        itemId: 'deploy',
        name: 'Deployment smoke test',
        repoName: 'repo-a',
        command: 'make deploy-smoke',
        reason: 'missing declared capability "Okta session"',
        capabilities: ['Okta session'],
        remediation: 'Make Okta session available, then retry verification.',
      },
    ],
    allowedActions: ['WAIVE', 'RETRY_AFTER_AUTH'],
  },
};

const questionsItem: Extract<AttentionItem, { kind: 'questions' }> = {
  kind: 'questions',
  id: 'questions-1',
  featureId: 'feature-1',
  sessionId: 'session-1',
  phase: 'Inquire',
  waitingSince: '2026-07-15T10:00:00.000Z',
  questions: [
    {
      key: 'Which overall direction should this project take?',
      header: 'Project direction',
      multiSelect: false,
      options: [
        {
          label: 'Harden the review pipeline (Recommended)',
          description: 'Invest in the existing multi-axis review workflow.',
          confidence: 0.86,
        },
        {
          label: 'Build user-facing features',
          description: 'Shift toward capabilities for end users.',
          confidence: 0.62,
        },
        { label: 'Improve documentation', confidence: 0.45 },
      ],
    },
  ],
};

const helpQuestionItem: Extract<AttentionItem, { kind: 'help' }> = {
  kind: 'help',
  id: 'feature-1:kb1234567890abcdef',
  featureId: 'feature-1',
  sessionId: 'kb1234567890abcdef',
  phase: 'Knowledge Base',
  waitingSince: '2026-07-15T10:00:00.000Z',
  prompt: 'Where should the config live?',
  waitingKind: 'question',
};

const helpWaitingItem: Extract<AttentionItem, { kind: 'help' }> = {
  ...helpQuestionItem,
  id: 'feature-1:kb1234567890abcdef:waiting',
  prompt: 'Agent has a question',
  waitingKind: 'input',
  runningTasks: ['Indexing repository layout', 'Summarizing packages'],
};

function Harness({ items, onJump }: { items: AttentionItem[]; onJump: ReturnType<typeof vi.fn> }) {
  const [drafts, setDrafts] = useState<AttentionDrafts>(emptyAttentionDrafts);
  return (
    <AttentionInbox
      items={items}
      refresh={async () => items}
      featureLabel={() => 'Search revamp'}
      drafts={drafts}
      setDrafts={setDrafts}
      onJump={onJump}
    />
  );
}

function GateDetailHarness({
  item = gateItem,
}: {
  item?: Extract<AttentionItem, { kind: 'gate' }>;
}): React.ReactElement {
  const [drafts, setDrafts] = useState<AttentionDrafts>(emptyAttentionDrafts);
  const run = (action: AttentionAction): void => {
    void action();
  };
  return (
    <AttentionDetail
      item={item}
      busy={false}
      submit={run}
      saveDraft={async (action) => {
        await action();
      }}
      drafts={drafts}
      setDrafts={setDrafts}
    />
  );
}

function QuestionDetailHarness({
  item = questionsItem,
}: {
  item?: Extract<AttentionItem, { kind: 'questions' }>;
}): React.ReactElement {
  const [drafts, setDrafts] = useState<AttentionDrafts>(emptyAttentionDrafts);
  return (
    <AttentionDetail
      item={item}
      busy={false}
      submit={(action) => void action()}
      drafts={drafts}
      setDrafts={setDrafts}
    />
  );
}

describe('AttentionInbox navigation', () => {
  it('deep-links a scoped agent request and closes the inbox', async () => {
    const onJump = vi.fn();
    render(<Harness items={[permissionItem]} onJump={onJump} />);
    const user = userEvent.setup();

    await user.click(screen.getByRole('button', { name: /Attention inbox, 1 pending/ }));
    const inbox = screen.getByRole('complementary', { name: 'Attention inbox' });
    await user.click(within(inbox).getByRole('button', { name: /Permission/ }));

    expect(onJump).toHaveBeenCalledWith('feature-1', 'perm-1');
    expect(
      screen.queryByRole('complementary', { name: 'Attention inbox' }),
    ).not.toBeInTheDocument();
  });

  it('routes reviews to the feature without requesting the conversation overlay', async () => {
    const onJump = vi.fn();
    render(<Harness items={[reviewItem]} onJump={onJump} />);
    const user = userEvent.setup();

    await user.click(screen.getByRole('button', { name: /Attention inbox, 1 pending/ }));
    await user.click(screen.getByRole('button', { name: /Review/ }));

    expect(onJump).toHaveBeenCalledWith('feature-1', undefined);
    expect(
      screen.queryByRole('complementary', { name: 'Attention inbox' }),
    ).not.toBeInTheDocument();
  });

  it('does not reopen a closed inbox for an already-handled route request', async () => {
    const onJump = vi.fn();
    const props = {
      items: [reviewItem],
      refresh: async () => [reviewItem],
      featureLabel: () => 'Search revamp',
      drafts: emptyAttentionDrafts(),
      setDrafts: vi.fn(),
      onJump,
      openRequest: { id: 7 },
    };
    const view = render(<AttentionInbox {...props} />);
    const user = userEvent.setup();

    await user.click(screen.getByRole('button', { name: 'Close inbox' }));
    view.rerender(<AttentionInbox {...props} openRequest={{ id: 7 }} />);

    expect(
      screen.queryByRole('complementary', { name: 'Attention inbox' }),
    ).not.toBeInTheDocument();
  });

  it('keeps arrow-key navigation between inbox rows', async () => {
    const onJump = vi.fn();
    const second = { ...permissionItem, id: 'perm-2' };
    render(<Harness items={[permissionItem, second]} onJump={onJump} />);
    const user = userEvent.setup();

    await user.click(screen.getByRole('button', { name: /Attention inbox, 2 pending/ }));
    const rows = screen.getAllByRole('button', { name: /Permission/ });
    rows[0]!.focus();
    await user.keyboard('{ArrowDown}');
    expect(rows[1]).toHaveFocus();
    await user.keyboard('{ArrowUp}');
    expect(rows[0]).toHaveFocus();
  });
});

describe('AttentionInbox gate detail', () => {
  it('persists retry immediately and resumes through the exact cycle route', async () => {
    const mock = installAgenticoMock();
    mock.api.saveGateDraft.mockResolvedValue({ result: 'saved' });
    mock.api.resolveGate.mockResolvedValue({ result: 'resumed' });
    const user = userEvent.setup();
    render(<GateDetailHarness />);

    expect(screen.getByRole('heading', { name: 'Deployment smoke test' })).toBeVisible();
    expect(screen.getByText('missing declared capability "Okta session"')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Retry verification' })).toBeDisabled();

    await user.click(screen.getByRole('radio', { name: /retry verification/ }));
    await waitFor(() =>
      expect(mock.api.saveGateDraft).toHaveBeenCalledWith({
        featureId: gateItem.featureId,
        repoName: 'repo-a',
        answers: { '1': 'RETRY_AFTER_AUTH' },
      }),
    );

    await user.click(screen.getByRole('button', { name: 'Retry verification' }));
    await waitFor(() =>
      expect(mock.api.resolveGate).toHaveBeenCalledWith({
        featureId: gateItem.featureId,
        repoName: 'repo-a',
      }),
    );
  });

  it('uses the warning waiver label without exposing gate-specific abort controls', async () => {
    const mock = installAgenticoMock();
    mock.api.saveGateDraft.mockResolvedValue({ result: 'saved' });
    const user = userEvent.setup();
    render(<GateDetailHarness />);

    await user.click(screen.getByRole('radio', { name: /Waive blocked checks/ }));
    const waiveAndResume = screen.getByRole('button', { name: 'Waive and resume' });
    expect(waiveAndResume).toBeEnabled();
    expect(waiveAndResume).toHaveAttribute('data-tone', 'warning');
    await waitFor(() =>
      expect(mock.api.saveGateDraft).toHaveBeenCalledWith({
        featureId: gateItem.featureId,
        repoName: 'repo-a',
        answers: { '1': 'WAIVE' },
      }),
    );

    expect(screen.queryByRole('button', { name: 'Abort gate' })).not.toBeInTheDocument();
    expect(screen.queryByRole('dialog', { name: 'Confirm abort' })).not.toBeInTheDocument();
  });

  it('keeps legacy gate textareas and the resume action', () => {
    installAgenticoMock();
    const legacyGate = {
      ...gateItem,
      id: 'legacy-gate-1',
      questions: [{ index: 1, prompt: 'Deployment window?', answer: '' }],
      verification: undefined,
    };

    render(<GateDetailHarness item={legacyGate} />);

    expect(screen.getByLabelText(/Deployment window/)).toBeVisible();
    expect(screen.getByRole('button', { name: 'Resume' })).toBeVisible();
  });
});

describe('AttentionInbox help detail', () => {
  function HelpDetailHarness({ item }: { item: Extract<AttentionItem, { kind: 'help' }> }) {
    const [drafts, setDrafts] = useState<AttentionDrafts>(emptyAttentionDrafts);
    return (
      <AttentionDetail
        item={item}
        busy={false}
        submit={(action) => void action()}
        drafts={drafts}
        setDrafts={setDrafts}
      />
    );
  }

  it('renders a waiting session as harness state, never as a question', async () => {
    const mock = installAgenticoMock();
    const user = userEvent.setup();
    render(<HelpDetailHarness item={helpWaitingItem} />);

    expect(screen.getByText('Agent is waiting')).toBeVisible();
    expect(
      screen.getByText('The agent finished its turn; the runtime is coordinating next steps.'),
    ).toBeVisible();
    expect(screen.queryByText('Agent has a question')).not.toBeInTheDocument();

    const tasks = screen.getByRole('list', { name: 'Running background tasks' });
    expect(within(tasks).getByText('Indexing repository layout')).toBeVisible();
    expect(within(tasks).getByText('Summarizing packages')).toBeVisible();

    const meta = screen.getByLabelText('Attention context');
    expect(meta).toHaveTextContent('session kb123456…');
    expect(meta).toHaveTextContent('Knowledge Base');
    expect(meta).toHaveTextContent(/waiting/);

    await user.type(screen.getByLabelText('Message to the agent'), 'carry on');
    await user.click(screen.getByRole('button', { name: 'Send message' }));
    expect(mock.api.sendHelp).toHaveBeenCalledWith({
      featureId: 'feature-1',
      sessionId: 'kb1234567890abcdef',
      message: 'carry on',
    });
  });

  it('keeps the question framing when the agent asked something real', () => {
    installAgenticoMock();
    render(<HelpDetailHarness item={helpQuestionItem} />);

    expect(screen.getByText('Where should the config live?')).toBeVisible();
    expect(screen.getByLabelText('Help reply')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Send reply' })).toBeVisible();
    expect(screen.queryByText('Agent is waiting')).not.toBeInTheDocument();
    const meta = screen.getByLabelText('Attention context');
    expect(meta).toHaveTextContent('session kb123456…');
    expect(meta).toHaveTextContent('Knowledge Base');
  });

  it('labels waiting sessions distinctly in the inbox rows', async () => {
    const onJump = vi.fn();
    render(<Harness items={[helpWaitingItem, helpQuestionItem]} onJump={onJump} />);
    const user = userEvent.setup();

    await user.click(screen.getByRole('button', { name: /Attention inbox, 2 pending/ }));
    expect(screen.getByRole('button', { name: /Agent waiting/ })).toBeVisible();
    expect(screen.getByRole('button', { name: /Help request/ })).toBeVisible();
  });
});

describe('AttentionInbox question detail', () => {
  it('renders single-select options and free text as numbered cards', () => {
    installAgenticoMock();
    render(<QuestionDetailHarness />);

    expect(
      screen.getByRole('heading', { name: 'Which overall direction should this project take?' }),
    ).toBeVisible();
    const options = screen.getAllByRole('radio');
    expect(options).toHaveLength(3);
    const recommendedCard = screen.getByText('Recommended').closest('.attention-option');
    expect(recommendedCard).toHaveTextContent('Harden the review pipeline');
    expect(recommendedCard).not.toHaveTextContent('(Recommended)');
    expect(recommendedCard).toHaveAttribute('data-recommended');
    expect(screen.getAllByText('Recommended')).toHaveLength(1);
    expect(screen.queryByText('86%')).not.toBeInTheDocument();
    expect(screen.getByText('1', { selector: '.attention-option__number' })).toHaveAttribute(
      'aria-hidden',
      'true',
    );
    expect(screen.getByPlaceholderText('Type your own answer here')).toBeVisible();
    expect(screen.getByText('4', { selector: '.attention-option__number' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Submit' })).toBeDisabled();
  });

  it('preserves single-select and free-text answer payload behavior', async () => {
    const mock = installAgenticoMock();
    const user = userEvent.setup();
    render(<QuestionDetailHarness />);

    const selected = screen.getByRole('radio', { name: /Harden the review pipeline/ });
    await user.click(selected);
    expect(selected.closest('.attention-option')).toHaveAttribute('data-selected');
    await user.click(screen.getByRole('button', { name: 'Submit' }));
    expect(mock.api.answerQuestions).toHaveBeenLastCalledWith({
      requestId: questionsItem.id,
      sessionId: questionsItem.sessionId,
      answers: {
        'Which overall direction should this project take?':
          'Harden the review pipeline (Recommended)',
      },
    });

    const freeText = screen.getByPlaceholderText('Type your own answer here');
    await user.type(freeText, 'Focus on speed');
    expect(freeText.closest('.attention-option')).toHaveAttribute('data-selected');
    expect(selected.closest('.attention-option')).not.toHaveAttribute('data-selected');
    await user.click(screen.getByRole('button', { name: 'Submit' }));
    expect(mock.api.answerQuestions).toHaveBeenLastCalledWith({
      requestId: questionsItem.id,
      sessionId: questionsItem.sessionId,
      answers: { 'Which overall direction should this project take?': 'Focus on speed' },
    });

    await user.click(screen.getByRole('radio', { name: /Build user-facing features/ }));
    expect(freeText).toHaveValue('');
    await user.click(screen.getByRole('button', { name: 'Submit' }));
    expect(mock.api.answerQuestions).toHaveBeenLastCalledWith({
      requestId: questionsItem.id,
      sessionId: questionsItem.sessionId,
      answers: {
        'Which overall direction should this project take?': 'Build user-facing features',
      },
    });
  });

  it('selects options with number keys and submits with Enter', async () => {
    const mock = installAgenticoMock();
    const user = userEvent.setup();
    render(<QuestionDetailHarness />);

    const first = screen.getByRole('radio', { name: /Harden the review pipeline/ });
    first.focus();
    await user.keyboard('2');
    expect(screen.getByRole('radio', { name: /Build user-facing features/ })).toBeChecked();
    await user.keyboard('{Enter}');
    expect(mock.api.answerQuestions).toHaveBeenLastCalledWith({
      requestId: questionsItem.id,
      sessionId: questionsItem.sessionId,
      answers: {
        'Which overall direction should this project take?': 'Build user-facing features',
      },
    });

    const freeText = screen.getByPlaceholderText('Type your own answer here');
    freeText.focus();
    await user.keyboard('12 weeks');
    expect(freeText).toHaveValue('12 weeks');
  });

  it('keeps checkbox semantics and omits recommendation tags for multi-select', async () => {
    installAgenticoMock();
    const user = userEvent.setup();
    render(
      <QuestionDetailHarness
        item={{
          ...questionsItem,
          questions: [{ ...questionsItem.questions[0]!, multiSelect: true }],
        }}
      />,
    );

    const options = screen.getAllByRole('checkbox');
    await user.click(options[0]!);
    await user.click(options[1]!);
    expect(options[0]).toBeChecked();
    expect(options[1]).toBeChecked();
    expect(screen.queryByText(/Recommended/)).not.toBeInTheDocument();
  });
});
