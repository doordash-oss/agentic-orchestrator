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
import type { SlackSettingsSnapshot } from '../../../shared/ipc';
import { SlackWarningPopover } from './SlackWarningPopover';

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  delete document.documentElement.dataset['theme'];
});

const credentialError: SlackSettingsSnapshot = {
  supported: true,
  enabled: true,
  tokenSet: true,
  tokenHint: '1234',
  tokenType: 'bot',
  identity: {
    teamId: 'T123',
    teamName: 'Acme',
    userId: 'U123',
    displayName: 'Agentico',
    botId: 'B123',
  },
  grantedScopes: ['chat:write'],
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

function Harness({
  snapshot = credentialError,
  dismissed = false,
  onDismiss = vi.fn(),
  onOpenSettings = vi.fn(),
}: {
  snapshot?: SlackSettingsSnapshot | null;
  dismissed?: boolean;
  onDismiss?: () => void;
  onOpenSettings?: () => void;
}) {
  const [open, setOpen] = useState(false);
  return (
    <SlackWarningPopover
      snapshot={snapshot}
      dismissed={dismissed}
      open={open}
      onOpenChange={setOpen}
      onDismiss={onDismiss}
      onOpenSettings={onOpenSettings}
    />
  );
}

describe('SlackWarningPopover', () => {
  it.each([
    null,
    { ...credentialError, status: { ...credentialError.status, state: 'not_configured' as const } },
    { ...credentialError, status: { ...credentialError.status, state: 'connected' as const } },
    { ...credentialError, status: { ...credentialError.status, state: 'warning' as const } },
  ])('renders no trigger outside a credential-error episode', (snapshot) => {
    render(<Harness snapshot={snapshot} />);
    expect(
      screen.queryByRole('button', { name: 'Show Slack credential warning' }),
    ).not.toBeInTheDocument();
  });

  it('shows the canonical error and routes or dismisses through its actions', async () => {
    const onOpenSettings = vi.fn();
    const onDismiss = vi.fn();
    const user = userEvent.setup();
    render(<Harness onOpenSettings={onOpenSettings} onDismiss={onDismiss} />);

    const trigger = screen.getByRole('button', { name: 'Show Slack credential warning' });
    expect(trigger).toHaveAttribute('aria-expanded', 'false');
    await user.click(trigger);

    const popover = screen.getByRole('region', { name: 'Slack needs attention' });
    expect(
      within(popover).getByRole('heading', { name: 'Slack rejected the saved token' }),
    ).toBeVisible();
    expect(popover).toHaveTextContent('Slack rejected the saved token with invalid_auth.');
    await user.click(within(popover).getByRole('button', { name: 'Open Slack settings' }));
    expect(onOpenSettings).toHaveBeenCalledTimes(1);
    await user.click(trigger);
    await user.click(
      within(screen.getByRole('region', { name: 'Slack needs attention' })).getByRole('button', {
        name: 'Dismiss',
      }),
    );
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it('stays absent after dismissal and closes with focus return on Escape or outside pointer', async () => {
    const user = userEvent.setup();
    const view = render(<Harness />);
    const trigger = screen.getByRole('button', { name: 'Show Slack credential warning' });

    await user.click(trigger);
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('region', { name: 'Slack needs attention' })).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();

    await user.click(trigger);
    const settingsAction = screen.getByRole('button', { name: 'Open Slack settings' });
    settingsAction.focus();
    expect(settingsAction).toHaveFocus();
    await user.click(document.body);
    expect(screen.queryByRole('region', { name: 'Slack needs attention' })).not.toBeInTheDocument();
    await waitFor(() => expect(trigger).toHaveFocus());

    view.rerender(<Harness dismissed />);
    expect(
      screen.queryByRole('button', { name: 'Show Slack credential warning' }),
    ).not.toBeInTheDocument();
  });

  it.each(['light', 'dark'] as const)(
    'keeps its diagnosis and actions inside a 400px viewport in the %s theme',
    async (theme) => {
      let resizeViewport: (() => void) | undefined;
      class TestResizeObserver {
        constructor(callback: ResizeObserverCallback) {
          resizeViewport = () => callback([], this as unknown as ResizeObserver);
        }
        observe() {}
        unobserve() {}
        disconnect() {}
      }

      document.documentElement.dataset['theme'] = theme;
      vi.stubGlobal('ResizeObserver', TestResizeObserver);
      vi.stubGlobal('innerWidth', 480);
      vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (
        this: HTMLElement,
      ) {
        if (!this.classList.contains('slack-warning-popover')) {
          return new DOMRect(0, 0, 0, 0);
        }
        const shift =
          Number.parseFloat(
            this.style.getPropertyValue('--toolbar-popover-viewport-shift').replace('px', ''),
          ) || 0;
        return new DOMRect(window.innerWidth - 499 + shift, 44, 368, 180);
      });

      const user = userEvent.setup();
      render(<Harness />);
      await user.click(screen.getByRole('button', { name: 'Show Slack credential warning' }));
      vi.stubGlobal('innerWidth', 400);
      resizeViewport?.();
      await new Promise<void>((resolve) => window.requestAnimationFrame(() => resolve()));

      const popover = screen.getByRole('region', { name: 'Slack needs attention' });
      const bounds = popover.getBoundingClientRect();
      expect(bounds.left).toBeGreaterThanOrEqual(16);
      expect(bounds.right).toBeLessThanOrEqual(384);
      expect(
        within(popover).getByRole('heading', { name: 'Slack rejected the saved token' }),
      ).toBeVisible();
      expect(within(popover).getByRole('button', { name: 'Open Slack settings' })).toBeVisible();
      expect(within(popover).getByRole('button', { name: 'Dismiss' })).toBeVisible();
    },
  );
});
