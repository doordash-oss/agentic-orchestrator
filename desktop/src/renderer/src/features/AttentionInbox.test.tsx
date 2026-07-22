import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AttentionItem } from '../../../shared/ipc';
import { AttentionInbox, emptyAttentionDrafts, type AttentionDrafts } from './AttentionInbox';

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
