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
import type { SlackSettingsSnapshot } from '../../../shared/ipc';
import { SlackWarningPopover } from './SlackWarningPopover';

afterEach(cleanup);

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
    await user.click(document.body);
    expect(screen.queryByRole('region', { name: 'Slack needs attention' })).not.toBeInTheDocument();

    view.rerender(<Harness dismissed />);
    expect(
      screen.queryByRole('button', { name: 'Show Slack credential warning' }),
    ).not.toBeInTheDocument();
  });
});
