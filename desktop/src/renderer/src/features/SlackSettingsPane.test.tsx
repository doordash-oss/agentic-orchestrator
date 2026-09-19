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

import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import type { SlackSettingsSnapshot } from '../../../shared/ipc';
import { installAgenticoMock, ipcError } from '../test/agenticoMock';
import { SlackSettingsPane } from './SlackSettingsPane';

const manifest = '{"display_information":{"name":"Agentico"}}';

function readyConnection(serverKey = 'a'.repeat(32)) {
  return {
    status: 'ready' as const,
    stage: 'ready' as const,
    detail: 'Connected.',
    ownership: 'external' as const,
    kind: 'local' as const,
    serverKey,
    serverName: 'Build server',
  };
}

function notConfigured(): SlackSettingsSnapshot {
  return {
    supported: true,
    enabled: false,
    tokenSet: false,
    tokenHint: '',
    tokenType: null,
    identity: null,
    grantedScopes: [],
    missingScopes: [],
    status: { state: 'not_configured', lastError: null, lastCheckedAt: null },
    manifest,
  };
}

function connected(overrides: Partial<Extract<SlackSettingsSnapshot, { supported: true }>> = {}) {
  return {
    supported: true as const,
    enabled: true,
    tokenSet: true,
    tokenHint: '1234',
    tokenType: 'bot' as const,
    identity: {
      teamId: 'T123',
      teamName: 'Acme',
      userId: 'U123',
      displayName: 'Agentico',
      botId: 'B123',
    },
    grantedScopes: ['chat:write', 'users:read'],
    missingScopes: [],
    status: {
      state: 'connected' as const,
      lastError: null,
      lastCheckedAt: '2026-09-19T10:00:00Z',
    },
    manifest,
    ...overrides,
  };
}

describe('SlackSettingsPane', () => {
  it('renders the not-configured guide and enables a token save', async () => {
    const user = userEvent.setup();
    const mock = installAgenticoMock({
      connection: readyConnection(),
      slackSettings: notConfigured(),
    });
    render(<SlackSettingsPane />);

    expect(await screen.findByText('Not set up')).toBeVisible();
    expect(screen.getByText('Build server')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Set up the Slack app' })).toHaveAttribute(
      'aria-expanded',
      'true',
    );
    expect(screen.getByLabelText('Slack token')).toHaveAttribute('type', 'password');
    expect(screen.getByRole('checkbox', { name: /Enabled/ })).not.toBeChecked();
    expect(screen.getByRole('button', { name: 'Check connection' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Save changes' })).toBeDisabled();

    await user.type(screen.getByLabelText('Slack token'), 'xoxb-secret-1234');
    expect(screen.getByRole('checkbox', { name: /Enabled/ })).toBeChecked();
    expect(screen.getByRole('button', { name: 'Check connection' })).toBeEnabled();
    expect(screen.getByRole('button', { name: 'Save changes' })).toBeEnabled();

    mock.api.updateSlackSettings.mockResolvedValue(connected());
    await user.click(screen.getByRole('button', { name: 'Save changes' }));

    await waitFor(() =>
      expect(mock.api.updateSlackSettings).toHaveBeenCalledWith({
        enabled: true,
        token: 'xoxb-secret-1234',
      }),
    );
    expect(await screen.findByText(/Connected to Acme as Agentico/)).toBeVisible();
    expect(document.body.textContent).not.toContain('xoxb-secret-1234');
  });

  it('supports replace, cancel, clear, and enabled-only saves', async () => {
    const user = userEvent.setup();
    const mock = installAgenticoMock({
      connection: readyConnection(),
      slackSettings: connected({ enabled: false }),
    });
    render(<SlackSettingsPane />);

    expect(await screen.findByText('Disabled')).toBeVisible();
    expect(screen.getByText('••••••••1234')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Replace' }));
    expect(screen.getByLabelText('Slack token')).toHaveValue('');
    await user.click(screen.getByRole('button', { name: 'Cancel replacement' }));
    expect(screen.getByText('••••••••1234')).toBeVisible();

    await user.click(screen.getByRole('checkbox', { name: /Enabled/ }));
    mock.api.updateSlackSettings.mockResolvedValue(connected());
    await user.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() =>
      expect(mock.api.updateSlackSettings).toHaveBeenCalledWith({ enabled: true }),
    );

    await user.click(screen.getByRole('button', { name: 'Clear' }));
    expect(screen.getByText(/saved token will be removed/i)).toBeVisible();
    expect(screen.getByRole('checkbox', { name: /Enabled/ })).not.toBeChecked();
  });

  it('checks a stored token and renders warning and field errors inline', async () => {
    const user = userEvent.setup();
    const warning = connected({
      status: {
        state: 'warning',
        lastError: {
          code: 'slack_unreachable',
          class: 'warning',
          title: 'Slack could not be reached',
          summary: 'Slack was unreachable.',
        },
        lastCheckedAt: '2026-09-19T11:00:00Z',
      },
    });
    const mock = installAgenticoMock({
      connection: readyConnection(),
      slackSettings: warning,
      slackValidation: {
        tokenType: 'bot',
        identity: connected().identity!,
        grantedScopes: ['chat:write'],
        missingScopes: [],
      },
    });
    render(<SlackSettingsPane />);

    expect(await screen.findByText('Token saved, but Slack could not be reached')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Check connection' }));
    await waitFor(() => expect(mock.api.validateSlackSettings).toHaveBeenCalledWith({}));
    expect(await screen.findByText(/1 scopes granted/)).toBeVisible();

    await user.click(screen.getByRole('button', { name: 'Replace' }));
    await user.type(screen.getByLabelText('Slack token'), 'xoxb-bad-token');
    mock.api.updateSlackSettings.mockRejectedValue(
      ipcError('slack_invalid_token', 'Slack rejected the token.'),
    );
    await user.click(screen.getByRole('button', { name: 'Save changes' }));
    expect(await screen.findByText('Slack rejected the token.')).toBeVisible();
    const tokenInput = screen.getByLabelText('Slack token', { exact: true });
    expect(tokenInput).toHaveValue('xoxb-bad-token');
    expect(tokenInput).toHaveAttribute('aria-describedby', 'slack-token-error');
    expect(tokenInput).toHaveFocus();
  });

  it('recovers focus after a keyboard-submitted token rejection', async () => {
    const user = userEvent.setup();
    const mock = installAgenticoMock({
      connection: readyConnection(),
      slackSettings: notConfigured(),
    });
    mock.api.updateSlackSettings.mockRejectedValue(
      ipcError('slack_invalid_token', 'Slack rejected the token.'),
    );
    render(<SlackSettingsPane />);

    const tokenInput = await screen.findByLabelText('Slack token', { exact: true });
    await user.type(tokenInput, 'xoxb-bad-token');
    const saveButton = screen.getByRole('button', { name: 'Save changes' });
    saveButton.focus();
    await user.keyboard('{Enter}');

    expect(await screen.findByText('Slack rejected the token.')).toBeVisible();
    expect(screen.getByLabelText('Slack token', { exact: true })).toHaveFocus();
  });

  it('renders the unsupported marker without controls', async () => {
    installAgenticoMock({
      connection: readyConnection(),
      slackSettings: { supported: false },
    });
    render(<SlackSettingsPane />);

    expect(
      await screen.findByText('The connected server does not support Slack and needs an update.'),
    ).toBeVisible();
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
  });

  it('discards a delayed check result after switching servers', async () => {
    const user = userEvent.setup();
    let resolveCheck!: (
      value: Awaited<ReturnType<Window['agentico']['validateSlackSettings']>>,
    ) => void;
    const delayed = new Promise<Awaited<ReturnType<Window['agentico']['validateSlackSettings']>>>(
      (resolve) => {
        resolveCheck = resolve;
      },
    );
    const mock = installAgenticoMock({
      connection: readyConnection(),
      slackSettings: connected(),
    });
    mock.api.validateSlackSettings.mockReturnValue(delayed);
    render(<SlackSettingsPane />);

    await screen.findByText(/Connected to Acme as Agentico/);
    await user.click(screen.getByRole('button', { name: 'Check connection' }));
    mock.api.getSlackSettings.mockResolvedValue(notConfigured());
    mock.emitConnection(readyConnection('b'.repeat(32)));
    expect(await screen.findByText('Not set up')).toBeVisible();

    resolveCheck({
      tokenType: 'bot',
      identity: connected().identity!,
      grantedScopes: ['chat:write'],
      missingScopes: [],
    });
    await Promise.resolve();

    expect(screen.queryByText(/scopes granted/)).not.toBeInTheDocument();
    expect(screen.getByText('Not set up')).toBeVisible();
  });

  it('discards a delayed draft-token check after the token changes', async () => {
    const user = userEvent.setup();
    let resolveCheck!: (
      value: Awaited<ReturnType<Window['agentico']['validateSlackSettings']>>,
    ) => void;
    const delayed = new Promise<Awaited<ReturnType<Window['agentico']['validateSlackSettings']>>>(
      (resolve) => {
        resolveCheck = resolve;
      },
    );
    const mock = installAgenticoMock({
      connection: readyConnection(),
      slackSettings: notConfigured(),
    });
    mock.api.validateSlackSettings.mockReturnValue(delayed);
    render(<SlackSettingsPane />);

    const tokenInput = await screen.findByLabelText('Slack token', { exact: true });
    await user.type(tokenInput, 'xoxb-token-a');
    await user.click(screen.getByRole('button', { name: 'Check connection' }));
    expect(screen.getByRole('button', { name: 'Checking...' })).toBeDisabled();

    await user.clear(tokenInput);
    await user.type(tokenInput, 'xoxb-token-b');
    expect(screen.getByRole('button', { name: 'Check connection' })).toBeEnabled();

    resolveCheck({
      tokenType: 'bot',
      identity: connected().identity!,
      grantedScopes: ['chat:write'],
      missingScopes: [],
    });
    await Promise.resolve();

    expect(screen.queryByText(/scopes granted/)).not.toBeInTheDocument();
    expect(tokenInput).toHaveValue('xoxb-token-b');
  });

  it('clears visible check results on reset, replacement cancellation, and token removal', async () => {
    const user = userEvent.setup();
    installAgenticoMock({
      connection: readyConnection(),
      slackSettings: connected(),
      slackValidation: {
        tokenType: 'bot',
        identity: connected().identity!,
        grantedScopes: ['chat:write'],
        missingScopes: [],
      },
    });
    render(<SlackSettingsPane />);

    await screen.findByText(/Connected to Acme as Agentico/);
    await user.click(screen.getByRole('button', { name: 'Check connection' }));
    expect(await screen.findByText(/1 scopes granted/)).toBeVisible();

    await user.click(screen.getByRole('button', { name: 'Replace' }));
    await user.click(screen.getByRole('button', { name: 'Cancel replacement' }));
    expect(screen.queryByText(/scopes granted/)).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Check connection' }));
    expect(await screen.findByText(/1 scopes granted/)).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Clear' }));
    expect(screen.queryByText(/scopes granted/)).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Undo' }));
    await user.click(screen.getByRole('button', { name: 'Replace' }));
    const tokenInput = screen.getByLabelText('Slack token', { exact: true });
    await user.type(tokenInput, 'xoxb-token-b');
    await user.click(screen.getByRole('button', { name: 'Check connection' }));
    expect(await screen.findByText(/1 scopes granted/)).toBeVisible();
    await user.type(tokenInput, '-edited');
    expect(screen.queryByText(/scopes granted/)).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Check connection' }));
    expect(await screen.findByText(/1 scopes granted/)).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Reset' }));
    expect(screen.queryByText(/scopes granted/)).not.toBeInTheDocument();
    expect(screen.getByText('••••••••1234')).toBeVisible();
  });
});
