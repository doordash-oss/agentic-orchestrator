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

import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AttentionItem, UpdateState } from '../../../shared/ipc';
import { emptyAttentionDrafts } from './AttentionInbox';
import { Toolbar, type ToolbarAttentionProps, type ToolbarUpdateProps } from './Toolbar';

afterEach(cleanup);

function baseProps() {
  return {
    title: 'Supervisor',
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

describe('Toolbar leading slot', () => {
  it('owns no sidebar toggle of its own; it renders whatever leading content the shell hands it', () => {
    const view = render(<Toolbar {...baseProps()} showTrailing={false} />);
    // The toggle moved into the sidebar header (SidebarChromeControls); the
    // shell only hands it back here while the sidebar is collapsed.
    expect(screen.queryByRole('button', { name: 'Hide sidebar' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Search features' })).not.toBeInTheDocument();

    view.rerender(
      <Toolbar
        {...baseProps()}
        showTrailing={false}
        leading={<button type="button">Chrome cluster</button>}
      />,
    );
    expect(screen.getByRole('button', { name: 'Chrome cluster' })).toBeVisible();
  });
});

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

describe('Toolbar Recovery button', () => {
  it('sits immediately before the attention bell, closes any popover, and opens the sheet', async () => {
    const onOpen = vi.fn();
    render(
      <Toolbar
        {...baseProps()}
        showTrailing={false}
        attention={attentionProps()}
        recovery={{ attention: false, onOpen }}
      />,
    );
    const user = userEvent.setup();
    const recovery = screen.getByRole('button', { name: 'Recovery' });
    const bell = screen.getByRole('button', { name: 'Attention inbox, 1 pending' });
    expect(recovery).toHaveAttribute('aria-haspopup', 'dialog');
    expect(recovery).toHaveAttribute('data-attention', 'false');
    expect(recovery.compareDocumentPosition(bell) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(recovery.nextElementSibling?.contains(bell)).toBe(true);

    await user.click(bell);
    expect(screen.getByRole('complementary', { name: 'Attention inbox' })).toBeVisible();
    await user.click(recovery);
    expect(onOpen).toHaveBeenCalledTimes(1);
    expect(
      screen.queryByRole('complementary', { name: 'Attention inbox' }),
    ).not.toBeInTheDocument();
  });

  it('marks itself while the server reports orphaned sessions', () => {
    render(
      <Toolbar {...baseProps()} showTrailing recovery={{ attention: true, onOpen: vi.fn() }} />,
    );
    expect(screen.getByRole('button', { name: 'Recovery' })).toHaveAttribute(
      'data-attention',
      'true',
    );
  });
});

describe('Toolbar new-feature button', () => {
  it('renders as a real button on the Supervisor page', async () => {
    const onNewFeature = vi.fn();
    render(<Toolbar {...baseProps()} showTrailing={false} onNewFeature={onNewFeature} />);

    const button = screen.getByRole('button', { name: 'New feature' });
    expect(button.tagName).toBe('BUTTON');
    expect(button).not.toHaveAttribute('tabindex', '-1');
    await userEvent.click(button);
    expect(onNewFeature).toHaveBeenCalledTimes(1);
  });

  it('stays on a feature page, after the feature-only cockpit slots', async () => {
    const onNewFeature = vi.fn();
    const { container } = render(
      <Toolbar {...baseProps()} title="Search revamp" showTrailing onNewFeature={onNewFeature} />,
    );

    const button = screen.getByRole('button', { name: 'New feature' });
    const slot = container.querySelector('.toolbar__inspector-slot')!;
    expect(slot.compareDocumentPosition(button)).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
    await userEvent.click(button);
    expect(onNewFeature).toHaveBeenCalledTimes(1);
  });

  it('is absent when the shell offers no creation handler', () => {
    render(<Toolbar {...baseProps()} showTrailing={false} />);
    expect(screen.queryByRole('button', { name: 'New feature' })).not.toBeInTheDocument();
  });
});
