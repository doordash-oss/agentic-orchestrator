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
  cycleType: 'review-comments',
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
        cycleType: 'review-comments',
        answers: { [gateItem.questions[0]!.prompt]: 'RETRY_AFTER_AUTH' },
      }),
    );

    await user.click(screen.getByRole('button', { name: 'Retry verification' }));
    await waitFor(() =>
      expect(mock.api.resolveGate).toHaveBeenCalledWith({
        featureId: gateItem.featureId,
        repoName: 'repo-a',
        cycleType: 'review-comments',
        decision: 'resume',
      }),
    );
  });

  it('uses the warning waiver label and preserves scoped abort routing', async () => {
    const mock = installAgenticoMock();
    mock.api.saveGateDraft.mockResolvedValue({ result: 'saved' });
    mock.api.resolveGate.mockResolvedValue({ result: 'aborted' });
    const user = userEvent.setup();
    render(<GateDetailHarness />);

    await user.click(screen.getByRole('radio', { name: /Waive blocked checks/ }));
    expect(screen.getByRole('button', { name: 'Waive and resume' })).toBeEnabled();
    await waitFor(() =>
      expect(mock.api.saveGateDraft).toHaveBeenCalledWith({
        featureId: gateItem.featureId,
        repoName: 'repo-a',
        cycleType: 'review-comments',
        answers: { [gateItem.questions[0]!.prompt]: 'WAIVE' },
      }),
    );

    await user.click(screen.getByRole('button', { name: 'Abort gate' }));
    await user.click(screen.getByRole('button', { name: 'Confirm abort' }));
    await waitFor(() =>
      expect(mock.api.resolveGate).toHaveBeenCalledWith({
        featureId: gateItem.featureId,
        repoName: 'repo-a',
        cycleType: 'review-comments',
        decision: 'abort',
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
