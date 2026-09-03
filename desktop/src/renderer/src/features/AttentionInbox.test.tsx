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

import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AttentionItem } from '../../../shared/ipc';
import {
  AttentionDetail,
  AttentionInbox,
  OwnerAwareAttention,
  emptyAttentionDrafts,
  type AttentionAction,
  type AttentionDrafts,
} from './AttentionInbox';
import { installAgenticoMock } from '../test/agenticoMock';
import { ErrorSurface } from '../components/ErrorSurface';

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

const errorItem: Extract<AttentionItem, { kind: 'error' }> = {
  kind: 'error',
  id: 'error:feature-1:run',
  featureId: 'feature-1',
  waitingSince: '2026-07-15T10:00:00.000Z',
  ref: { scope: 'run', code: 'iteration_budget_exhausted', featureId: 'feature-1' },
  class: 'blocking',
  code: 'iteration_budget_exhausted',
  title: 'Iteration budget exhausted',
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
  waitingKind: 'coordinating',
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

const recoveryItem: AttentionItem = {
  kind: 'recovery',
  id: 'recovery-scan',
  waitingSince: '2026-07-15T10:00:00.000Z',
  liveCount: 1,
  deadCount: 0,
};

function bell(): HTMLElement {
  return screen.getByRole('button', { name: /Attention inbox, \d+ pending/ });
}

function popover(): HTMLElement | null {
  return screen.queryByRole('complementary', { name: 'Attention inbox' });
}

function PermissionDetailHarness({
  item,
}: {
  item: Extract<AttentionItem, { kind: 'permission' }>;
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

describe('permission auto mode split button', () => {
  it('stays hidden when the server made no offer', () => {
    installAgenticoMock();
    render(<PermissionDetailHarness item={permissionItem} />);
    expect(screen.queryByRole('group', { name: 'Enable auto mode' })).toBeNull();
    expect(screen.getByRole('button', { name: 'Allow once' })).toBeVisible();
  });

  it('allows and turns auto mode on for this feature, with all features behind the chevron', async () => {
    const mock = installAgenticoMock();
    const user = userEvent.setup();
    render(
      <PermissionDetailHarness
        item={{ ...permissionItem, autoApprove: { wouldFastPath: true } }}
      />,
    );
    const split = screen.getByRole('group', { name: 'Enable auto mode' });
    const main = within(split).getByRole('button', {
      name: 'Enable auto mode (this feature only)',
    });
    expect(main).toHaveAttribute('title', expect.stringContaining('safe build and test fast path'));
    // The split button sits in the same row as the other decisions.
    expect(main.closest('.attention-detail__actions')).toBe(
      screen.getByRole('button', { name: 'Deny' }).closest('.attention-detail__actions'),
    );

    await user.click(main);
    expect(mock.api.answerPermission).toHaveBeenLastCalledWith({
      requestId: 'perm-1',
      sessionId: 'session-1',
      decision: 'allow_once',
      autoApproveScope: 'feature',
    });

    const menu = split.querySelector('details');
    expect(menu?.open).toBe(false);
    await user.click(within(split).getByLabelText('More auto mode options'));
    expect(menu?.open).toBe(true);
    const all = within(split).getByRole('menuitem', { name: /Enable auto mode \(all features\)/ });
    await user.click(all);
    expect(mock.api.answerPermission).toHaveBeenLastCalledWith({
      requestId: 'perm-1',
      sessionId: 'session-1',
      decision: 'allow_once',
      autoApproveScope: 'workspace',
    });
  });

  it('offers only the all-features choice for a request without a feature', () => {
    installAgenticoMock();
    const { featureId: _omit, ...ownerless } = permissionItem as Extract<
      AttentionItem,
      { kind: 'permission' }
    >;
    render(
      <PermissionDetailHarness item={{ ...ownerless, autoApprove: { wouldFastPath: false } }} />,
    );
    const split = screen.getByRole('group', { name: 'Enable auto mode' });
    const main = within(split).getByRole('button', { name: 'Enable auto mode (all features)' });
    expect(main).toHaveAttribute(
      'title',
      expect.stringContaining('sent this command to a reviewer model'),
    );
    expect(within(split).queryByLabelText('More auto mode options')).toBeNull();
  });
});

describe('AttentionInbox popover presentation', () => {
  it('toggles from the bell, dismisses on Escape and outside pointer, and never offers a close control', async () => {
    render(<Harness items={[permissionItem]} onJump={vi.fn()} />);
    const user = userEvent.setup();

    await user.click(bell());
    expect(popover()).toBeVisible();
    expect(bell()).toHaveAttribute('aria-expanded', 'true');
    expect(screen.queryByRole('button', { name: /close/i })).not.toBeInTheDocument();

    // A second click on the trigger closes it and returns focus to the bell.
    await user.click(bell());
    expect(popover()).not.toBeInTheDocument();
    expect(bell()).toHaveFocus();

    await user.click(bell());
    await user.keyboard('{Escape}');
    expect(popover()).not.toBeInTheDocument();
    expect(bell()).toHaveFocus();

    await user.click(bell());
    await user.click(document.body);
    expect(popover()).not.toBeInTheDocument();
  });

  it('toggles with the ⌘⇧A accelerator in both directions', async () => {
    render(<Harness items={[permissionItem]} onJump={vi.fn()} />);
    const user = userEvent.setup();

    await user.keyboard('{Meta>}{Shift>}a{/Shift}{/Meta}');
    expect(popover()).toBeVisible();

    await user.keyboard('{Meta>}{Shift>}a{/Shift}{/Meta}');
    expect(popover()).not.toBeInTheDocument();
    expect(bell()).toHaveFocus();
  });

  it('counts only actionable items in the badge and shows none at zero', async () => {
    const { rerender } = render(
      <AttentionInbox
        items={[permissionItem, reviewItem, recoveryItem]}
        refresh={async () => []}
        featureLabel={() => 'Search revamp'}
        drafts={emptyAttentionDrafts()}
        setDrafts={vi.fn()}
        onJump={vi.fn()}
      />,
    );

    // Recovery is contextual priority, never a pending answer.
    expect(bell()).toHaveAccessibleName('Attention inbox, 2 pending');
    expect(bell()).toHaveAttribute('data-empty', 'false');
    expect(bell().querySelector('.attention-bell__count')).toHaveTextContent('2');

    await userEvent.click(bell());
    expect(screen.getByText('2 waiting')).toBeVisible();

    rerender(
      <AttentionInbox
        items={[]}
        refresh={async () => []}
        featureLabel={() => 'Search revamp'}
        drafts={emptyAttentionDrafts()}
        setDrafts={vi.fn()}
        onJump={vi.fn()}
      />,
    );
    expect(bell()).toHaveAccessibleName('Attention inbox, 0 pending');
    expect(bell()).toHaveAttribute('data-empty', 'true');
    expect(bell().querySelector('.attention-bell__count')).toBeNull();
    expect(screen.getByRole('status')).toHaveTextContent('No blocking input is waiting.');
  });

  it('honours a controlled open state so the toolbar can close it for the update popover', async () => {
    const onOpenChange = vi.fn();
    const view = render(
      <AttentionInbox
        items={[permissionItem]}
        refresh={async () => [permissionItem]}
        featureLabel={() => 'Search revamp'}
        drafts={emptyAttentionDrafts()}
        setDrafts={vi.fn()}
        onJump={vi.fn()}
        open={true}
        onOpenChange={onOpenChange}
      />,
    );
    expect(popover()).toBeVisible();

    await userEvent.keyboard('{Escape}');
    expect(onOpenChange).toHaveBeenLastCalledWith(false);

    view.rerender(
      <AttentionInbox
        items={[permissionItem]}
        refresh={async () => [permissionItem]}
        featureLabel={() => 'Search revamp'}
        drafts={emptyAttentionDrafts()}
        setDrafts={vi.fn()}
        onJump={vi.fn()}
        open={false}
        onOpenChange={onOpenChange}
      />,
    );
    expect(popover()).not.toBeInTheDocument();
  });

  it('expands an ownerless item inline and jumps for a recovery item', async () => {
    const onJump = vi.fn();
    const ownerless: AttentionItem = { ...helpQuestionItem, featureId: undefined };
    render(<Harness items={[ownerless, recoveryItem]} onJump={onJump} />);
    const user = userEvent.setup();

    await user.click(bell());
    const row = screen.getByRole('button', { name: /Help request/ });
    await user.click(row);
    expect(row).toHaveAttribute('aria-expanded', 'true');
    expect(screen.getByLabelText('Help reply')).toBeVisible();

    await user.click(screen.getByRole('button', { name: /Recovery/ }));
    expect(onJump).toHaveBeenCalledWith('__recovery__');
    expect(popover()).not.toBeInTheDocument();
  });

  it('keeps a submission notice announced after the popover is dismissed', async () => {
    const mock = installAgenticoMock();
    mock.api.sendHelp.mockResolvedValue({ result: 'submitted' });
    const ownerless: AttentionItem = { ...helpQuestionItem, featureId: undefined };
    render(<Harness items={[ownerless]} onJump={vi.fn()} />);
    const user = userEvent.setup();

    await user.click(bell());
    await user.click(screen.getByRole('button', { name: /Help request/ }));
    await user.type(screen.getByLabelText('Help reply'), 'carry on');
    await user.click(screen.getByRole('button', { name: 'Send reply' }));
    await waitFor(() =>
      expect(screen.getByRole('status')).toHaveTextContent(/Waiting for the server snapshot/),
    );

    await user.keyboard('{Escape}');
    expect(popover()).not.toBeInTheDocument();
    const announcement = screen.getByRole('status');
    expect(announcement).toHaveClass('sr-only');
    expect(announcement).toHaveTextContent(/Waiting for the server snapshot/);
  });

  it('notices an already-resolved item and moves focus to its neighbour', async () => {
    const ownerless: AttentionItem = { ...helpQuestionItem, featureId: undefined };
    const neighbour: AttentionItem = {
      ...helpQuestionItem,
      id: 'feature-1:neighbour',
      featureId: undefined,
      prompt: 'Second question',
    };
    const view = render(
      <AttentionInbox
        items={[ownerless, neighbour]}
        refresh={async () => [neighbour]}
        featureLabel={() => 'Search revamp'}
        drafts={emptyAttentionDrafts()}
        setDrafts={vi.fn()}
        onJump={vi.fn()}
        openRequest={{ id: 3, attentionId: ownerless.id }}
      />,
    );

    view.rerender(
      <AttentionInbox
        items={[neighbour]}
        refresh={async () => [neighbour]}
        featureLabel={() => 'Search revamp'}
        drafts={emptyAttentionDrafts()}
        setDrafts={vi.fn()}
        onJump={vi.fn()}
        openRequest={{ id: 3, attentionId: ownerless.id }}
      />,
    );

    await waitFor(() =>
      expect(
        screen.getByText('This item was already resolved. The inbox has been refreshed.'),
      ).toBeVisible(),
    );
    const rows = screen.getAllByRole('button', { name: /Help request/ });
    expect(rows).toHaveLength(1);
    await waitFor(() => expect(rows[0]).toHaveFocus());
  });
});

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

  it('does not reopen a dismissed popover for an already-handled route request', async () => {
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

    await user.keyboard('{Escape}');
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

  it('persists a legacy gate answer on blur', async () => {
    const mock = installAgenticoMock();
    mock.api.saveGateDraft.mockResolvedValue({ result: 'saved' });
    const user = userEvent.setup();
    const legacyGate = {
      ...gateItem,
      id: 'legacy-gate-blur',
      questions: [{ index: 1, prompt: 'Deployment window?', answer: '' }],
      verification: undefined,
    };
    render(<GateDetailHarness item={legacyGate} />);

    await user.type(screen.getByLabelText(/Deployment window/), 'Tuesday');
    await user.tab();
    await waitFor(() =>
      expect(mock.api.saveGateDraft).toHaveBeenCalledWith({
        featureId: gateItem.featureId,
        repoName: 'repo-a',
        answers: { '1': 'Tuesday' },
      }),
    );
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

  it('keeps waiting sessions out of the inbox rows and the badge', async () => {
    const onJump = vi.fn();
    render(<Harness items={[helpWaitingItem, helpQuestionItem]} onJump={onJump} />);
    const user = userEvent.setup();

    await user.click(screen.getByRole('button', { name: /Attention inbox, 1 pending/ }));
    expect(screen.queryByRole('button', { name: /Agent waiting/ })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Help request/ })).toBeVisible();
  });

  it('shows the empty state when only a waiting session is open', async () => {
    render(<Harness items={[helpWaitingItem]} onJump={vi.fn()} />);
    const user = userEvent.setup();

    await user.click(screen.getByRole('button', { name: /Attention inbox, 0 pending/ }));
    expect(screen.getByText('No blocking input is waiting.')).toBeVisible();
  });

  // A chat resting after a reply is its normal state, not blocking input: the
  // AMA panel is its reply surface, so the inbox and the badge stay quiet.
  it('keeps a chat session waiting on the user out of the rows and the badge', async () => {
    render(<Harness items={[{ ...helpWaitingItem, waitingKind: 'input' }]} onJump={vi.fn()} />);
    const user = userEvent.setup();

    await user.click(screen.getByRole('button', { name: /Attention inbox, 0 pending/ }));
    expect(screen.queryByRole('button', { name: /Agent waiting/ })).not.toBeInTheDocument();
    expect(screen.getByText('No blocking input is waiting.')).toBeVisible();
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

describe('error items', () => {
  it('omits an error projection when its owning card is mounted in the same view', async () => {
    render(
      <>
        <ErrorSurface
          error={{
            code: errorItem.code,
            class: errorItem.class,
            title: errorItem.title,
            summary: 'The Implement phase exhausted its iteration budget.',
          }}
          explain={{ reference: errorItem.ref }}
        />
        <OwnerAwareAttention item={errorItem}>
          <section aria-label="Duplicate error projection">{errorItem.title}</section>
        </OwnerAwareAttention>
      </>,
    );

    await waitFor(() =>
      expect(screen.queryByRole('region', { name: 'Duplicate error projection' })).toBeNull(),
    );
    expect(screen.getAllByText('Iteration budget exhausted')).toHaveLength(1);
  });

  const blockingErrorItem: Extract<AttentionItem, { kind: 'error' }> = {
    kind: 'error',
    id: 'error:feature-1:run::iteration_budget_exhausted',
    featureId: 'feature-1',
    waitingSince: '2026-08-05T12:00:00.000Z',
    ref: { scope: 'run', code: 'iteration_budget_exhausted', featureId: 'feature-1' },
    class: 'blocking',
    code: 'iteration_budget_exhausted',
    title: 'Iteration budget exhausted',
  };
  const needsActionErrorItem: Extract<AttentionItem, { kind: 'error' }> = {
    ...blockingErrorItem,
    id: 'error:feature-1:repository:repo-a:publish_rebase_conflict',
    ref: {
      scope: 'repository',
      code: 'publish_rebase_conflict',
      featureId: 'feature-1',
      repository: 'repo-a',
    },
    class: 'needs_action',
    code: 'publish_rebase_conflict',
    title: 'Pull-rebase conflict',
  };

  it('renders rows named by the class label and counts them on the bell', async () => {
    render(<Harness items={[blockingErrorItem, needsActionErrorItem]} onJump={vi.fn()} />);
    const user = userEvent.setup();

    expect(bell()).toHaveAttribute('aria-label', 'Attention inbox, 2 pending');
    await user.click(bell());
    const failedRow = screen.getByRole('button', { name: /Failed/ });
    expect(failedRow).toHaveTextContent('Failed');
    expect(failedRow).toHaveTextContent('Search revamp');
    const needsActionRow = screen.getByRole('button', { name: /Needs your action/ });
    expect(needsActionRow).toHaveTextContent('Search revamp');
  });

  it('shows the catalog title in the detail with no remediation, and jumps with the item id', async () => {
    const onJump = vi.fn();
    render(<Harness items={[blockingErrorItem]} onJump={onJump} />);
    const user = userEvent.setup();

    await user.click(bell());
    const row = screen.getByRole('button', { name: /Failed/ });
    await user.click(row);
    // The row click closes the popover and jumps with owner and item id.
    expect(onJump).toHaveBeenCalledWith('feature-1', blockingErrorItem.id);
    expect(popover()).not.toBeInTheDocument();
  });

  it('jumps from the detail Open control with the item id', async () => {
    const onJump = vi.fn();
    const [drafts, setDrafts] = [
      { questions: {}, help: {}, gates: {} } as AttentionDrafts,
      undefined,
    ];
    void drafts;
    void setDrafts;
    render(
      <AttentionDetail
        item={needsActionErrorItem}
        busy={false}
        submit={() => undefined}
        drafts={emptyAttentionDrafts()}
        setDrafts={() => undefined}
        onJump={onJump}
      />,
    );

    expect(screen.getByText('Pull-rebase conflict')).toBeVisible();
    expect(screen.queryByText(/retry/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/Try/i)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Open' }));
    expect(onJump).toHaveBeenCalledWith('feature-1', needsActionErrorItem.id);
  });

  it('renders a rejected inbox action as a compact ErrorSurface with the catalog title', async () => {
    const mock = installAgenticoMock();
    // A canonical rejection as the preload rethrows it: the sentinel-prefixed
    // message carrying the serialized canonical object.
    mock.api.sendHelp.mockRejectedValue(
      new Error(
        'E_CANONICAL_ERROR {"code":"send_help_failed","class":"blocking","title":"Help could not be sent","summary":"The help message could not be delivered."}',
      ),
    );
    const ownerless: AttentionItem = { ...helpQuestionItem, featureId: undefined };
    render(<Harness items={[ownerless]} onJump={vi.fn()} />);
    const user = userEvent.setup();

    await user.click(bell());
    await user.click(screen.getByRole('button', { name: /Help request/ }));
    await user.type(screen.getByLabelText('Help reply'), 'carry on');
    await user.click(screen.getByRole('button', { name: 'Send reply' }));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveClass('error-surface', 'error-surface--compact');
    expect(within(alert).getByText('Help could not be sent')).toBeVisible();
    expect(within(alert).getByText('send_help_failed')).toHaveClass('error-surface__code');
    expect(within(alert).getByText('The help message could not be delivered.')).toBeVisible();
    // The sentinel wire text never renders.
    expect(alert).not.toHaveTextContent('E_CANONICAL_ERROR');
    expect(document.querySelector('.attention-status')).toBeNull();
  });
});
