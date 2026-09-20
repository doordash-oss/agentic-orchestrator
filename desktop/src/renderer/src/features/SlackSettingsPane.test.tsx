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

import { act, render, screen, waitFor } from '@testing-library/react';
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
    defaultRecipients: [],
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
    defaultRecipients: [],
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
        suggestedRecipient: null,
      },
    });
    render(<SlackSettingsPane />);

    expect(await screen.findByText('Token saved, but Slack could not be reached')).toBeVisible();
    expect(screen.getAllByText(/^Last checked /)).toHaveLength(1);
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

  it('preserves a pending stored-token check through same-credential invalidation', async () => {
    const user = userEvent.setup();
    let resolveCheck!: (
      value: Awaited<ReturnType<Window['agentico']['validateSlackSettings']>>,
    ) => void;
    const delayed = new Promise<Awaited<ReturnType<Window['agentico']['validateSlackSettings']>>>(
      (resolve) => {
        resolveCheck = resolve;
      },
    );
    const warning = connected({
      identity: null,
      grantedScopes: [],
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
    });
    mock.api.validateSlackSettings.mockReturnValue(delayed);
    render(<SlackSettingsPane />);

    await screen.findByText('Token saved, but Slack could not be reached');
    await user.click(screen.getByRole('button', { name: 'Check connection' }));
    mock.api.getSlackSettings.mockResolvedValue(connected());
    mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });
    await waitFor(() => expect(mock.api.getSlackSettings).toHaveBeenCalledTimes(2));

    resolveCheck({
      tokenType: 'bot',
      identity: connected().identity!,
      grantedScopes: ['chat:write'],
      missingScopes: [],
      suggestedRecipient: null,
    });

    expect(await screen.findByText(/1 scopes granted/)).toBeVisible();
    expect(screen.getByText('Connected to Acme as Agentico (bot)')).toBeVisible();
  });

  it('preserves a completed stored-token check through same-credential invalidation', async () => {
    const user = userEvent.setup();
    const mock = installAgenticoMock({
      connection: readyConnection(),
      slackSettings: connected(),
      slackValidation: {
        tokenType: 'bot',
        identity: connected().identity!,
        grantedScopes: ['chat:write'],
        missingScopes: [],
        suggestedRecipient: null,
      },
    });
    render(<SlackSettingsPane />);

    await screen.findByText(/Connected to Acme as Agentico/);
    await user.click(screen.getByRole('button', { name: 'Check connection' }));
    expect(await screen.findByText(/1 scopes granted/)).toBeVisible();
    await waitFor(() => expect(mock.api.getSlackSettings).toHaveBeenCalledTimes(2));

    mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });
    await waitFor(() => expect(mock.api.getSlackSettings).toHaveBeenCalledTimes(3));

    expect(screen.getByText(/1 scopes granted/)).toBeVisible();
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
      suggestedRecipient: null,
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
      suggestedRecipient: null,
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
        suggestedRecipient: null,
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

  it('resolves recipients on blur and Enter, rejects duplicates, and saves only resolved rows', async () => {
    const user = userEvent.setup();
    const mock = installAgenticoMock({
      connection: readyConnection(),
      slackSettings: connected(),
    });
    mock.api.resolveSlackRecipient
      .mockResolvedValueOnce({
        typedText: 'ada@example.com',
        kind: 'user',
        id: 'U23456789',
        displayName: 'Ada Lovelace',
      })
      .mockResolvedValueOnce({
        typedText: '@ada',
        kind: 'user',
        id: 'U23456789',
        displayName: 'Ada Lovelace',
      });
    render(<SlackSettingsPane />);

    await screen.findByText(/Connected to Acme as Agentico/);
    await user.click(screen.getByRole('button', { name: 'Add recipient' }));
    const first = screen.getByRole('textbox', { name: 'Recipient 1' });
    await user.type(first, 'ada@example.com');
    await user.tab();
    expect(await screen.findByText('Ada Lovelace')).toBeVisible();
    expect(mock.api.resolveSlackRecipient).toHaveBeenNthCalledWith(1, {
      input: 'ada@example.com',
    });

    await user.click(screen.getByRole('button', { name: 'Add recipient' }));
    const second = screen.getByRole('textbox', { name: 'Recipient 2' });
    await user.type(second, '@ada{Enter}');
    expect(await screen.findByText('Already in the list')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Save changes' })).toBeDisabled();
    expect(screen.getByText('Resolve or remove every recipient before saving.')).toBeVisible();

    await user.click(screen.getByRole('button', { name: 'Remove recipient 2' }));
    mock.api.updateSlackSettings.mockResolvedValue(
      connected({
        defaultRecipients: [
          {
            typedText: 'ada@example.com',
            kind: 'user',
            id: 'U23456789',
            displayName: 'Ada Lovelace',
          },
        ],
      }),
    );
    await user.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() =>
      expect(mock.api.updateSlackSettings).toHaveBeenCalledWith({
        enabled: true,
        defaultRecipients: [
          {
            typedText: 'ada@example.com',
            kind: 'user',
            id: 'U23456789',
            displayName: 'Ada Lovelace',
          },
        ],
      }),
    );
  });

  it('uses a draft token for resolution and disables rows without any token', async () => {
    const user = userEvent.setup();
    const mock = installAgenticoMock({
      connection: readyConnection(),
      slackSettings: notConfigured(),
    });
    mock.api.resolveSlackRecipient.mockResolvedValue({
      typedText: '#eng',
      kind: 'channel',
      id: 'C12345678',
      displayName: '#eng',
    });
    render(<SlackSettingsPane />);

    await screen.findByText('Not set up');
    await user.click(screen.getByRole('button', { name: 'Add recipient' }));
    expect(screen.getByRole('textbox', { name: 'Recipient 1' })).toBeDisabled();
    expect(screen.getByText('Add a Slack token before resolving recipients.')).toBeVisible();

    await user.type(screen.getByLabelText('Slack token'), 'xoxb-draft-token');
    const input = screen.getByRole('textbox', { name: 'Recipient 1' });
    expect(input).toBeEnabled();
    await user.type(input, '#eng{Enter}');
    await waitFor(() =>
      expect(mock.api.resolveSlackRecipient).toHaveBeenCalledWith({
        input: '#eng',
        token: 'xoxb-draft-token',
      }),
    );
  });

  it('marks the stored user-token owner as you and previews a validation suggestion', async () => {
    const user = userEvent.setup();
    const owner = {
      typedText: '@ada',
      kind: 'user' as const,
      id: 'U123',
      displayName: 'Ada',
    };
    const mock = installAgenticoMock({
      connection: readyConnection(),
      slackSettings: connected({
        tokenType: 'user',
        identity: { ...connected().identity!, displayName: 'Ada', botId: null },
        defaultRecipients: [owner],
      }),
      slackValidation: {
        tokenType: 'user',
        identity: { ...connected().identity!, displayName: 'Ada', botId: null },
        grantedScopes: ['chat:write'],
        missingScopes: [],
        suggestedRecipient: owner,
      },
    });
    render(<SlackSettingsPane />);

    expect(await screen.findByText('you')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Remove recipient 1' }));
    await user.click(screen.getByRole('button', { name: 'Check connection' }));
    expect(await screen.findByText('You will be notified by default.')).toBeVisible();
    expect(mock.api.validateSlackSettings).toHaveBeenCalledWith({});
  });

  it('saves recipient removals after the stored token has been cleared', async () => {
    const user = userEvent.setup();
    const recipients = [
      {
        typedText: '@ada',
        kind: 'user' as const,
        id: 'U12345678',
        displayName: 'Ada',
      },
      {
        typedText: '#eng',
        kind: 'channel' as const,
        id: 'C12345678',
        displayName: '#eng',
      },
    ];
    const withoutToken = connected({
      enabled: false,
      tokenSet: false,
      tokenHint: '',
      tokenType: null,
      identity: null,
      grantedScopes: [],
      defaultRecipients: recipients,
      status: { state: 'not_configured', lastError: null, lastCheckedAt: null },
    });
    const afterRemoval = {
      ...withoutToken,
      defaultRecipients: [recipients[1]],
    };
    const mock = installAgenticoMock({
      connection: readyConnection(),
      slackSettings: connected({ defaultRecipients: recipients }),
    });
    mock.api.updateSlackSettings
      .mockResolvedValueOnce(withoutToken)
      .mockResolvedValueOnce(afterRemoval);
    render(<SlackSettingsPane />);

    await screen.findByText(/Connected to Acme as Agentico/);
    await user.click(screen.getByRole('button', { name: 'Clear' }));
    await user.click(screen.getByRole('button', { name: 'Save changes' }));
    expect(await screen.findByText('Not set up')).toBeVisible();

    await user.click(screen.getByRole('button', { name: 'Remove recipient 1' }));
    expect(screen.getByRole('button', { name: 'Save changes' })).toBeEnabled();
    await user.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() =>
      expect(mock.api.updateSlackSettings).toHaveBeenLastCalledWith({
        enabled: false,
        defaultRecipients: [recipients[1]],
      }),
    );

    mock.api.getSlackSettings.mockResolvedValue(afterRemoval);
    mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });
    await waitFor(() => expect(mock.api.getSlackSettings).toHaveBeenCalledTimes(2));
    expect(screen.getByRole('textbox', { name: 'Recipient 1' })).toHaveValue('#eng');
    expect(screen.queryByRole('textbox', { name: 'Recipient 2' })).not.toBeInTheDocument();
  });

  it('keeps a pending recipient draft through invalidation and resets it explicitly', async () => {
    const user = userEvent.setup();
    let resolveRecipient!: (
      value: Awaited<ReturnType<Window['agentico']['resolveSlackRecipient']>>,
    ) => void;
    const delayed = new Promise<Awaited<ReturnType<Window['agentico']['resolveSlackRecipient']>>>(
      (resolve) => {
        resolveRecipient = resolve;
      },
    );
    const mock = installAgenticoMock({
      connection: readyConnection(),
      slackSettings: connected(),
    });
    mock.api.resolveSlackRecipient.mockReturnValue(delayed);
    render(<SlackSettingsPane />);

    await screen.findByText(/Connected to Acme as Agentico/);
    await user.click(screen.getByRole('button', { name: 'Add recipient' }));
    const input = screen.getByRole('textbox', { name: 'Recipient 1' });
    await user.type(input, '#eng{Enter}');
    expect(await screen.findByText('Resolving...')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Reset' })).toBeEnabled();

    mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });
    await Promise.resolve();
    expect(mock.api.getSlackSettings).toHaveBeenCalledTimes(1);
    expect(input).toHaveValue('#eng');

    await user.click(screen.getByRole('button', { name: 'Reset' }));
    expect(screen.queryByRole('textbox', { name: 'Recipient 1' })).not.toBeInTheDocument();

    resolveRecipient({
      typedText: '#eng',
      kind: 'channel',
      id: 'C12345678',
      displayName: '#eng',
    });
    await Promise.resolve();
    expect(screen.queryByText('#eng')).not.toBeInTheDocument();
  });

  it('keeps a failed recipient draft through invalidation and resets it explicitly', async () => {
    const user = userEvent.setup();
    const mock = installAgenticoMock({
      connection: readyConnection(),
      slackSettings: connected(),
    });
    mock.api.resolveSlackRecipient.mockRejectedValue(
      ipcError('slack_channel_not_found', 'Slack could not find that channel.'),
    );
    render(<SlackSettingsPane />);

    await screen.findByText(/Connected to Acme as Agentico/);
    await user.click(screen.getByRole('button', { name: 'Add recipient' }));
    const input = screen.getByRole('textbox', { name: 'Recipient 1' });
    await user.type(input, '#missing{Enter}');
    expect(await screen.findByText('Slack could not find that channel.')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Reset' })).toBeEnabled();

    mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });
    await Promise.resolve();
    expect(mock.api.getSlackSettings).toHaveBeenCalledTimes(1);
    expect(input).toHaveValue('#missing');
    expect(screen.getByText('Slack could not find that channel.')).toBeVisible();

    await user.click(screen.getByRole('button', { name: 'Reset' }));
    expect(screen.queryByRole('textbox', { name: 'Recipient 1' })).not.toBeInTheDocument();
    expect(screen.queryByText('Slack could not find that channel.')).not.toBeInTheDocument();
  });

  it('renders test-message delivery results and clears them when rows change', async () => {
    const user = userEvent.setup();
    const recipients = [
      {
        typedText: '@ada',
        kind: 'user' as const,
        id: 'U12345678',
        displayName: 'Ada',
      },
      {
        typedText: '#private-ops',
        kind: 'channel' as const,
        id: 'C12345678',
        displayName: '#private-ops',
      },
    ];
    const mock = installAgenticoMock({
      connection: readyConnection(),
      slackSettings: connected({ defaultRecipients: recipients }),
    });
    mock.api.sendSlackTestMessage.mockResolvedValue({
      results: [
        { recipient: recipients[0], delivered: true, error: null },
        {
          recipient: recipients[1],
          delivered: false,
          error: {
            code: 'slack_not_in_channel',
            class: 'warning',
            title: 'Agentico is not in this channel',
            summary: 'Agentico could not send to #private-ops.',
            remediation: { hint: 'Invite the Agentico app to #private-ops in Slack.' },
          },
        },
      ],
    });
    render(<SlackSettingsPane />);

    await screen.findByText(/Connected to Acme as Agentico/);
    const send = screen.getByRole('button', { name: 'Send test message' });
    expect(send).toBeEnabled();
    await user.click(send);
    expect(await screen.findByText('Sent')).toBeVisible();
    expect(screen.getByText('Agentico could not send to #private-ops.')).toBeVisible();
    expect(screen.getByText('Invite the Agentico app to #private-ops in Slack.')).toBeVisible();
    expect(mock.api.sendSlackTestMessage).toHaveBeenCalledWith({ recipients });

    await user.type(screen.getByRole('textbox', { name: 'Recipient 1' }), '-edited');
    expect(screen.queryByText('Sent')).not.toBeInTheDocument();
  });

  it('does not restore a delayed test error after a recipient edit', async () => {
    const user = userEvent.setup();
    let rejectSend!: (error: unknown) => void;
    const delayed = new Promise<never>((_resolve, reject) => {
      rejectSend = reject;
    });
    const recipient = {
      typedText: '@ada',
      kind: 'user' as const,
      id: 'U12345678',
      displayName: 'Ada',
    };
    const mock = installAgenticoMock({
      connection: readyConnection(),
      slackSettings: connected({ defaultRecipients: [recipient] }),
    });
    mock.api.sendSlackTestMessage.mockReturnValue(delayed);
    render(<SlackSettingsPane />);

    await screen.findByText(/Connected to Acme as Agentico/);
    await user.click(screen.getByRole('button', { name: 'Send test message' }));
    await user.type(screen.getByRole('textbox', { name: 'Recipient 1' }), '-edited');
    await act(async () => {
      rejectSend(ipcError('slack_unreachable', 'Slack was unreachable.'));
      await delayed.catch(() => undefined);
    });

    expect(screen.queryByText('Slack was unreachable.')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Send test message' })).toBeDisabled();
  });

  it('does not restore delayed test results after a save', async () => {
    const user = userEvent.setup();
    let resolveSend!: (
      value: Awaited<ReturnType<Window['agentico']['sendSlackTestMessage']>>,
    ) => void;
    const delayed = new Promise<Awaited<ReturnType<Window['agentico']['sendSlackTestMessage']>>>(
      (resolve) => {
        resolveSend = resolve;
      },
    );
    const recipient = {
      typedText: '@ada',
      kind: 'user' as const,
      id: 'U12345678',
      displayName: 'Ada',
    };
    const mock = installAgenticoMock({
      connection: readyConnection(),
      slackSettings: connected({ defaultRecipients: [recipient] }),
    });
    mock.api.sendSlackTestMessage.mockReturnValue(delayed);
    mock.api.updateSlackSettings.mockResolvedValue(
      connected({ enabled: false, defaultRecipients: [recipient] }),
    );
    render(<SlackSettingsPane />);

    await screen.findByText(/Connected to Acme as Agentico/);
    await user.click(screen.getByRole('button', { name: 'Send test message' }));
    await user.click(screen.getByRole('checkbox', { name: /Enabled/ }));
    await user.click(screen.getByRole('button', { name: 'Save changes' }));
    expect(await screen.findByText('Saved.')).toBeVisible();

    await act(async () => {
      resolveSend({
        results: [{ recipient, delivered: true, error: null }],
      });
      await delayed;
    });

    expect(screen.queryByText('Sent')).not.toBeInTheDocument();
  });

  it('does not restore delayed test results after reset', async () => {
    const user = userEvent.setup();
    let resolveSend!: (
      value: Awaited<ReturnType<Window['agentico']['sendSlackTestMessage']>>,
    ) => void;
    const delayed = new Promise<Awaited<ReturnType<Window['agentico']['sendSlackTestMessage']>>>(
      (resolve) => {
        resolveSend = resolve;
      },
    );
    const recipient = {
      typedText: '@ada',
      kind: 'user' as const,
      id: 'U12345678',
      displayName: 'Ada',
    };
    const mock = installAgenticoMock({
      connection: readyConnection(),
      slackSettings: connected({ defaultRecipients: [recipient] }),
    });
    mock.api.sendSlackTestMessage.mockReturnValue(delayed);
    render(<SlackSettingsPane />);

    await screen.findByText(/Connected to Acme as Agentico/);
    await user.click(screen.getByRole('button', { name: 'Send test message' }));
    await user.click(screen.getByRole('checkbox', { name: /Enabled/ }));
    await user.click(screen.getByRole('button', { name: 'Reset' }));

    await act(async () => {
      resolveSend({
        results: [{ recipient, delivered: true, error: null }],
      });
      await delayed;
    });

    expect(screen.queryByText('Sent')).not.toBeInTheDocument();
    expect(screen.getByRole('checkbox', { name: /Enabled/ })).toBeChecked();
  });

  it('keeps a newer test send pending when an obsolete send completes', async () => {
    const user = userEvent.setup();
    type TestResult = Awaited<ReturnType<Window['agentico']['sendSlackTestMessage']>>;
    let resolveFirst!: (value: TestResult) => void;
    let resolveSecond!: (value: TestResult) => void;
    const first = new Promise<TestResult>((resolve) => {
      resolveFirst = resolve;
    });
    const second = new Promise<TestResult>((resolve) => {
      resolveSecond = resolve;
    });
    const recipients = [
      {
        typedText: '@ada',
        kind: 'user' as const,
        id: 'U12345678',
        displayName: 'Ada',
      },
      {
        typedText: '#eng',
        kind: 'channel' as const,
        id: 'C12345678',
        displayName: '#eng',
      },
    ];
    const mock = installAgenticoMock({
      connection: readyConnection(),
      slackSettings: connected({ defaultRecipients: recipients }),
    });
    mock.api.sendSlackTestMessage.mockReturnValueOnce(first).mockReturnValueOnce(second);
    render(<SlackSettingsPane />);

    await screen.findByText(/Connected to Acme as Agentico/);
    await user.click(screen.getByRole('button', { name: 'Send test message' }));
    await user.click(screen.getByRole('button', { name: 'Remove recipient 2' }));
    await user.click(screen.getByRole('button', { name: 'Send test message' }));

    await act(async () => {
      resolveFirst({
        results: recipients.map((recipient) => ({ recipient, delivered: true, error: null })),
      });
      await first;
    });
    expect(screen.getByRole('button', { name: 'Sending...' })).toBeDisabled();
    expect(screen.queryByText('Sent')).not.toBeInTheDocument();

    await act(async () => {
      resolveSecond({
        results: [{ recipient: recipients[0]!, delivered: true, error: null }],
      });
      await second;
    });
    expect(await screen.findByText('Sent')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Send test message' })).toBeEnabled();
  });
});
