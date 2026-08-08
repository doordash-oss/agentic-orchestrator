import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AttentionItem, UpdateState } from '../../../shared/ipc';
import { emptyAttentionDrafts } from './AttentionInbox';
import { Toolbar, type ToolbarAttentionProps, type ToolbarUpdateProps } from './Toolbar';

afterEach(cleanup);

function baseProps() {
  return {
    sidebarCollapsed: false,
    onToggleSidebar: vi.fn(),
    title: 'Overview',
  };
}

const pendingItem: AttentionItem = {
  kind: 'permission',
  id: 'perm-1',
  featureId: 'feature-1',
  sessionId: 'session-1',
  phase: 'Implement',
  toolName: 'Bash',
  input: { command: 'printf attention' },
  waitingSince: '2026-07-15T10:00:00.000Z',
};

const readyUpdate: UpdateState = {
  status: 'ready',
  currentVersion: '0.1.0',
  targetVersion: '0.2.0',
  packageFormat: 'macos',
  signatureStatus: 'verified',
  message: 'A verified update is downloaded and ready to install.',
};

function attentionProps(): ToolbarAttentionProps {
  return {
    items: [pendingItem],
    refresh: async () => [pendingItem],
    featureLabel: () => 'Search revamp',
    drafts: emptyAttentionDrafts(),
    setDrafts: vi.fn(),
    onJump: vi.fn(),
    openRequest: null,
  };
}

function updateProps(): ToolbarUpdateProps {
  return {
    update: readyUpdate,
    dismissedVersion: null,
    scheduling: false,
    onDismiss: vi.fn(),
    onOpenSettings: vi.fn(),
    onInstallWhenIdle: async () => {},
  };
}

describe('Toolbar trailing notices', () => {
  it('keeps the bell mounted on every selection, with or without the cockpit slots', () => {
    const view = render(
      <Toolbar {...baseProps()} showTrailing={false} attention={attentionProps()} />,
    );
    expect(screen.getByRole('button', { name: 'Attention inbox, 1 pending' })).toBeVisible();

    view.rerender(
      <Toolbar {...baseProps()} title="Search revamp" showTrailing attention={attentionProps()} />,
    );
    expect(screen.getByRole('button', { name: 'Attention inbox, 1 pending' })).toBeVisible();

    view.rerender(
      <Toolbar
        {...baseProps()}
        title="Settings"
        showTrailing={false}
        attention={attentionProps()}
      />,
    );
    expect(screen.getByRole('button', { name: 'Attention inbox, 1 pending' })).toBeVisible();
  });

  it('opens at most one popover: each trigger closes the other', async () => {
    render(
      <Toolbar
        {...baseProps()}
        showTrailing={false}
        attention={attentionProps()}
        update={updateProps()}
      />,
    );
    const user = userEvent.setup();
    const bell = screen.getByRole('button', { name: 'Attention inbox, 1 pending' });
    const updateTrigger = screen.getByRole('button', { name: 'Show available update' });

    await user.click(bell);
    expect(screen.getByRole('complementary', { name: 'Attention inbox' })).toBeVisible();

    await user.click(updateTrigger);
    expect(screen.getByRole('region', { name: 'Available update' })).toBeVisible();
    expect(
      screen.queryByRole('complementary', { name: 'Attention inbox' }),
    ).not.toBeInTheDocument();

    await user.click(bell);
    expect(screen.getByRole('complementary', { name: 'Attention inbox' })).toBeVisible();
    expect(screen.queryByRole('region', { name: 'Available update' })).not.toBeInTheDocument();
  });

  it('omits the update trigger entirely when nothing is pending', () => {
    render(
      <Toolbar
        {...baseProps()}
        showTrailing={false}
        attention={attentionProps()}
        update={{ ...updateProps(), update: null }}
      />,
    );
    expect(screen.queryByRole('button', { name: 'Show available update' })).not.toBeInTheDocument();
  });
});

describe('Toolbar new-feature button', () => {
  it('renders as a real button when Overview is selected and showNewFeature is true', async () => {
    const onNewFeature = vi.fn();
    render(
      <Toolbar {...baseProps()} showTrailing={false} showNewFeature onNewFeature={onNewFeature} />,
    );

    const button = screen.getByRole('button', { name: 'New feature' });
    expect(button.tagName).toBe('BUTTON');
    expect(button).not.toHaveAttribute('tabindex', '-1');
    await userEvent.click(button);
    expect(onNewFeature).toHaveBeenCalledTimes(1);
  });

  it('is absent when a feature is selected', () => {
    render(
      <Toolbar
        {...baseProps()}
        title="Search revamp"
        showTrailing
        showNewFeature={false}
        onNewFeature={vi.fn()}
      />,
    );
    expect(screen.queryByRole('button', { name: 'New feature' })).not.toBeInTheDocument();
  });

  it('is absent for Settings', () => {
    render(
      <Toolbar
        {...baseProps()}
        title="Settings"
        showTrailing={false}
        showNewFeature={false}
        onNewFeature={vi.fn()}
      />,
    );
    expect(screen.queryByRole('button', { name: 'New feature' })).not.toBeInTheDocument();
  });
});
