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
import type { AttentionItem, SlackSettingsSnapshot, UpdateState } from '../../../shared/ipc';
import { emptyAttentionDrafts } from './AttentionInbox';
import {
  Toolbar,
  type ToolbarAttentionProps,
  type ToolbarSlackWarningProps,
  type ToolbarUpdateProps,
} from './Toolbar';

afterEach(cleanup);

function baseProps() {
  return {
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

function slackWarningProps(): ToolbarSlackWarningProps {
  const snapshot: SlackSettingsSnapshot = {
    supported: true,
    enabled: true,
    tokenSet: true,
    tokenHint: '1234',
    tokenType: 'bot',
    identity: null,
    grantedScopes: [],
    missingScopes: [],
    defaultRecipients: [],
    categories: { progress: true, needsInput: true, problems: true },
    status: {
      state: 'credential_error',
      lastError: {
        code: 'slack_token_rejected',
        class: 'needs_action',
        title: 'Slack rejected the saved token',
        summary: 'Slack rejected the saved token with invalid_auth.',
      },
      lastCheckedAt: '2026-09-22T10:00:00Z',
    },
    manifest: '{}',
  };
  return {
    snapshot,
    dismissed: false,
    onDismiss: vi.fn(),
    onOpenSettings: vi.fn(),
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

  it('opens at most one popover among attention, update, and Slack warning', async () => {
    render(
      <Toolbar
        {...baseProps()}
        showTrailing={false}
        attention={attentionProps()}
        update={updateProps()}
        slackWarning={slackWarningProps()}
      />,
    );
    const user = userEvent.setup();
    const bell = screen.getByRole('button', { name: 'Attention inbox, 1 pending' });
    const updateTrigger = screen.getByRole('button', { name: 'Show available update' });
    const slackTrigger = screen.getByRole('button', { name: 'Show Slack credential warning' });

    await user.click(bell);
    expect(screen.getByRole('complementary', { name: 'Attention inbox' })).toBeVisible();

    await user.click(slackTrigger);
    expect(screen.getByRole('region', { name: 'Slack needs attention' })).toBeVisible();
    expect(
      screen.queryByRole('complementary', { name: 'Attention inbox' }),
    ).not.toBeInTheDocument();

    await user.click(updateTrigger);
    expect(screen.getByRole('region', { name: 'Available update' })).toBeVisible();
    expect(screen.queryByRole('region', { name: 'Slack needs attention' })).not.toBeInTheDocument();

    await user.click(bell);
    expect(screen.getByRole('complementary', { name: 'Attention inbox' })).toBeVisible();
    expect(screen.queryByRole('region', { name: 'Available update' })).not.toBeInTheDocument();

    await user.click(slackTrigger);
    expect(screen.getByRole('region', { name: 'Slack needs attention' })).toBeVisible();
    expect(
      screen.queryByRole('complementary', { name: 'Attention inbox' }),
    ).not.toBeInTheDocument();
  });

  it('does not reopen Slack automatically after recovery and a later credential failure', async () => {
    const warning = slackWarningProps();
    const view = render(<Toolbar {...baseProps()} showTrailing={false} slackWarning={warning} />);
    const user = userEvent.setup();

    await user.click(screen.getByRole('button', { name: 'Show Slack credential warning' }));
    expect(screen.getByRole('region', { name: 'Slack needs attention' })).toBeVisible();

    view.rerender(
      <Toolbar
        {...baseProps()}
        showTrailing={false}
        slackWarning={{
          ...warning,
          snapshot:
            warning.snapshot?.supported === true
              ? {
                  ...warning.snapshot,
                  status: {
                    state: 'connected',
                    lastError: null,
                    lastCheckedAt: '2026-09-22T10:05:00Z',
                  },
                }
              : warning.snapshot,
        }}
      />,
    );
    expect(
      screen.queryByRole('button', { name: 'Show Slack credential warning' }),
    ).not.toBeInTheDocument();

    view.rerender(<Toolbar {...baseProps()} showTrailing={false} slackWarning={warning} />);
    expect(screen.getByRole('button', { name: 'Show Slack credential warning' })).toHaveAttribute(
      'aria-expanded',
      'false',
    );
    expect(screen.queryByRole('region', { name: 'Slack needs attention' })).not.toBeInTheDocument();
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
