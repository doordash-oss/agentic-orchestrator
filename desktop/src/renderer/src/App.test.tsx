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

import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it } from 'vitest';
import {
  ConnectionStateSchema,
  defaultSettings,
  type AttentionItem,
  type ConnectionState,
} from '../../shared/ipc';
import App from './App';
import { defaultUpdateState, installAgenticoMock, readySnapshot } from './test/agenticoMock';
import { dispatchMediaChange, matchMediaState } from './test/setup';

/** Builds a state through the schema, so tests can only emit valid variants. */
function connection(overrides: Record<string, unknown>): ConnectionState {
  return ConnectionStateSchema.parse({
    status: 'discovering',
    stage: 'discover',
    detail: 'Looking for a running Agentico runtime.',
    ownership: 'none',
    // Ready states carry main-owned locality; default it so tests that do not
    // pin locality still emit a schema-valid ready state.
    ...(overrides.status === 'ready' ? { kind: 'local' } : {}),
    ...overrides,
  });
}

beforeEach(() => {
  matchMediaState.darkScheme = true;
  matchMediaState.reducedMotion = false;
  delete document.documentElement.dataset['theme'];
});

describe('App theming', () => {
  it('applies the resolved theme from the main process on startup', async () => {
    installAgenticoMock({ theme: { preference: 'system', resolved: 'dark' } });
    render(<App />);
    await waitFor(() => expect(document.documentElement.dataset['theme']).toBe('dark'));
  });

  it('applies the main process theme broadcast published by the Settings window', async () => {
    // The switcher itself lives in the Settings window's Appearance pane; the
    // main window learns about the change only through this broadcast.
    const mock = installAgenticoMock({ theme: { preference: 'system', resolved: 'dark' } });
    render(<App />);
    await waitFor(() => expect(document.documentElement.dataset['theme']).toBe('dark'));

    act(() => mock.emitAppEvent({ type: 'theme', preference: 'light', resolved: 'light' }));

    await waitFor(() => expect(document.documentElement.dataset['theme']).toBe('light'));
  });

  it('follows OS appearance changes while the preference is system', async () => {
    installAgenticoMock({ theme: { preference: 'system', resolved: 'dark' } });
    render(<App />);
    await waitFor(() => expect(document.documentElement.dataset['theme']).toBe('dark'));

    dispatchMediaChange('(prefers-color-scheme: dark)', false);
    await waitFor(() => expect(document.documentElement.dataset['theme']).toBe('light'));
  });

  it('ignores OS appearance changes when an explicit theme is chosen', async () => {
    installAgenticoMock({ theme: { preference: 'dark', resolved: 'dark' } });
    render(<App />);
    await waitFor(() => expect(document.documentElement.dataset['theme']).toBe('dark'));

    dispatchMediaChange('(prefers-color-scheme: dark)', false);
    await new Promise((resolve) => setTimeout(resolve, 10));
    expect(document.documentElement.dataset['theme']).toBe('dark');
  });
});

describe('App readiness gating', () => {
  it('does not call runtime-backed IPC until the connection is ready', async () => {
    const mock = installAgenticoMock({ readiness: readySnapshot() });
    render(<App />);

    await screen.findByLabelText(/agentico connection/i);
    expect(mock.api.getAttention).not.toHaveBeenCalled();
    expect(mock.api.scanRecovery).not.toHaveBeenCalled();
    expect(mock.api.listFeatures).not.toHaveBeenCalled();

    act(() => {
      mock.emitConnection(connection({ status: 'ready', stage: 'ready', ownership: 'app-owned' }));
    });

    await waitFor(() => expect(mock.api.getAttention).toHaveBeenCalledTimes(1));
    expect(mock.api.scanRecovery).toHaveBeenCalledTimes(1);
    // listFeatures is now fetched by the Overview surface inside the shell
    // (App no longer does its own feature-name lookup), which mounts once the
    // separate readiness fetch resolves — a second async hop after attention.
    await waitFor(() => expect(mock.api.listFeatures).toHaveBeenCalledTimes(1));
  });

  it('tells the native menu bar to go dark while the runtime is not ready', async () => {
    const mock = installAgenticoMock({ readiness: readySnapshot() });
    render(<App />);

    await screen.findByLabelText(/agentico connection/i);
    await waitFor(() => expect(mock.api.publishUiState).toHaveBeenCalled());
    const pushed = mock.api.publishUiState.mock.calls[0]![0] as {
      runtimeReady: boolean;
      activeFeatureId: string | null;
      featureCommands: Record<string, boolean>;
    };
    expect(pushed.runtimeReady).toBe(false);
    expect(pushed.activeFeatureId).toBeNull();
    expect(Object.keys(pushed.featureCommands)).toHaveLength(0);
  });

  it('uses the authoritative feature name in the global inbox', async () => {
    const featureId = 'abcd1234ef567890';
    const attention: AttentionItem = {
      kind: 'permission',
      id: 'permission-1',
      featureId,
      sessionId: 'session-1',
      phase: 'Implement',
      toolName: 'Bash',
      summary: 'Inspect the build output',
      input: { command: 'npm test' },
      waitingSince: '2026-07-15T10:00:00.000Z',
    };
    installAgenticoMock({
      connection: connection({ status: 'ready', stage: 'ready', ownership: 'external' }),
      readiness: readySnapshot(),
      settings: {
        ...defaultSettings(),
        // The bell is only shown in the toolbar once a feature is selected
        // (it is hidden entirely on Overview), so this test starts on
        // the feature it wants to jump from.
        shell: { featureByServer: { 'default-runtime': featureId }, sidebarCollapsed: false },
      },
      features: [
        {
          id: featureId,
          name: 'Search revamp',
          status: 'Created',
          currentPhase: 'Plan',
          repos: ['repo-a'],
          createdAt: '2026-07-14T10:00:00Z',
          activeRun: 1,
          runCount: 1,
          warnings: [],
        },
      ],
      attention: { items: [attention] },
    });
    render(<App />);

    await screen.findByRole('option', { name: 'Overview' });
    await userEvent.click(
      await screen.findByRole('button', { name: /Attention inbox, 1 pending/ }),
    );
    const inbox = screen.getByRole('complementary', { name: 'Attention inbox' });
    expect(within(inbox).getByText('Search revamp')).toBeVisible();
    await userEvent.click(within(inbox).getByRole('button', { name: /Permission.*Search revamp/ }));
    expect(
      screen.queryByRole('complementary', { name: 'Attention inbox' }),
    ).not.toBeInTheDocument();
  });

  it('does not replay a handled attention route after the popover is dismissed', async () => {
    const user = userEvent.setup();
    const mock = installAgenticoMock({
      connection: connection({ status: 'ready', stage: 'ready', ownership: 'external' }),
      readiness: readySnapshot(),
    });
    render(<App />);
    await screen.findByRole('option', { name: 'Overview' });

    act(() => mock.emitRouteRequest({ target: 'attention' }));
    await screen.findByRole('complementary', { name: 'Attention inbox' });
    await user.keyboard('{Escape}');
    expect(
      screen.queryByRole('complementary', { name: 'Attention inbox' }),
    ).not.toBeInTheDocument();

    // A background refresh must not replay the already-consumed route request.
    const updatesBeforeRefresh = mock.api.getUpdates.mock.calls.length;
    act(() => mock.emitAppEvent({ type: 'invalidated', kind: 'updates.changed' }));
    await waitFor(() =>
      expect(mock.api.getUpdates.mock.calls.length).toBeGreaterThan(updatesBeforeRefresh),
    );
    expect(
      screen.queryByRole('complementary', { name: 'Attention inbox' }),
    ).not.toBeInTheDocument();

    act(() => mock.emitRouteRequest({ target: 'attention' }));
    expect(await screen.findByRole('complementary', { name: 'Attention inbox' })).toBeVisible();
  });

  it('refreshes review attention after a lifecycle invalidation', async () => {
    const mock = installAgenticoMock({
      connection: connection({ status: 'ready', stage: 'ready', ownership: 'external' }),
      readiness: readySnapshot(),
    });
    render(<App />);
    await screen.findByRole('option', { name: 'Overview' });
    const before = mock.api.getAttention.mock.calls.length;

    act(() => {
      mock.emitAppEvent({ type: 'invalidated', kind: 'lifecycle.updated', featureId: 'feature-1' });
    });

    await waitFor(() => expect(mock.api.getAttention.mock.calls.length).toBeGreaterThan(before));
  });

  it('refreshes the passive update notice after an update invalidation', async () => {
    const mock = installAgenticoMock({
      connection: connection({ status: 'ready', stage: 'ready', ownership: 'external' }),
      readiness: readySnapshot(),
      updates: defaultUpdateState({ status: 'current' }),
    });
    render(<App />);
    await screen.findByRole('option', { name: 'Overview' });
    expect(screen.queryByLabelText('Update available')).not.toBeInTheDocument();

    mock.api.getUpdates.mockResolvedValueOnce(
      defaultUpdateState({
        status: 'ready',
        targetVersion: '0.2.0',
        packageFormat: 'macos',
        signatureStatus: 'verified',
        releaseNotesUrl: 'https://github.com/doordash-oss/agentic-orchestrator/releases/tag/v0.2.0',
        message: 'A verified update is downloaded and ready to install.',
      }),
    );
    const before = mock.api.getUpdates.mock.calls.length;

    act(() => {
      mock.emitAppEvent({ type: 'invalidated', kind: 'updates.changed' });
    });

    await waitFor(() => expect(mock.api.getUpdates.mock.calls.length).toBeGreaterThan(before));
    expect(await screen.findByLabelText('Update available')).toBeVisible();
  });

  it('shows the connection shell — never the wizard — before the gateway is ready', async () => {
    const mock = installAgenticoMock();
    render(<App />);
    // The global brand header is gone; only the connection shell's own
    // "Agentico" heading remains before the gateway is ready.
    await waitFor(() =>
      expect(screen.getAllByRole('heading', { name: /^agentico$/i })).toHaveLength(1),
    );
    expect(screen.queryByLabelText(/first-launch setup/i)).not.toBeInTheDocument();
    expect(mock.api.getReadiness).not.toHaveBeenCalled();
  });

  it('opens the mandatory wizard when the runtime is ready but setup is incomplete', async () => {
    const mock = installAgenticoMock({
      connection: connection({ status: 'ready', stage: 'ready', ownership: 'app-owned' }),
    });
    render(<App />);
    await waitFor(() =>
      expect(screen.getByRole('heading', { name: /set up agentico/i })).toBeInTheDocument(),
    );
    expect(mock.api.getReadiness).toHaveBeenCalled();
    // No path into feature creation exists while gates are unsatisfied.
    expect(screen.queryByRole('button', { name: /create|new feature/i })).not.toBeInTheDocument();
  });

  it('skips the wizard entirely for an already-ready runtime', async () => {
    installAgenticoMock({
      connection: connection({ status: 'ready', stage: 'ready', ownership: 'external' }),
      readiness: readySnapshot(),
    });
    render(<App />);
    expect(await screen.findByRole('option', { name: 'Overview' })).toBeInTheDocument();
    expect(screen.queryByLabelText(/first-launch setup/i)).not.toBeInTheDocument();
  });

  it('falls back to the connection shell on disconnect and re-derives on recovery', async () => {
    const mock = installAgenticoMock({
      connection: connection({ status: 'ready', stage: 'ready', ownership: 'app-owned' }),
    });
    render(<App />);
    await waitFor(() =>
      expect(screen.getByRole('heading', { name: /set up agentico/i })).toBeInTheDocument(),
    );
    const fetchesBeforeCrash = mock.api.getReadiness.mock.calls.length;

    act(() => {
      mock.emitConnection(
        connection({
          status: 'crashed',
          stage: 'connect',
          detail: 'The app-managed runtime exited unexpectedly.',
          error: { code: 'E_SERVER_CRASHED', message: 'exited', remediation: 'Retry.' },
        }),
      );
    });
    expect(screen.queryByLabelText(/first-launch setup/i)).not.toBeInTheDocument();
    expect(screen.getByLabelText(/agentico connection/i)).toBeInTheDocument();

    act(() => {
      mock.emitConnection(connection({ status: 'ready', stage: 'ready', ownership: 'app-owned' }));
    });
    // Recovery refetches the authoritative snapshot instead of trusting
    // anything remembered from before the crash.
    await waitFor(() =>
      expect(mock.api.getReadiness.mock.calls.length).toBeGreaterThan(fetchesBeforeCrash),
    );
    await waitFor(() =>
      expect(screen.getByRole('heading', { name: /set up agentico/i })).toBeInTheDocument(),
    );
  });
});

describe('App settings-window routing', () => {
  /** Every settings entry point is a request to raise the separate window. */
  const readyMock = () =>
    installAgenticoMock({
      connection: connection({ status: 'ready', stage: 'ready', ownership: 'external' }),
      readiness: readySnapshot(),
    });

  it('asks the main process to open the Settings window instead of rendering a panel', async () => {
    const mock = readyMock();
    render(<App />);
    await screen.findByRole('option', { name: 'Overview' });

    act(() => mock.emitRouteRequest({ target: 'settings' }));

    await waitFor(() => expect(mock.api.openSettingsWindow).toHaveBeenCalledWith({}));
    // Nothing settings-shaped ever mounts in the main window.
    expect(
      screen.queryByRole('region', { name: 'Settings and readiness' }),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole('radiogroup', { name: /theme/i })).not.toBeInTheDocument();
    expect(screen.getByRole('option', { name: 'Overview' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
  });

  it('forwards a deep-linked settings section as the window request payload', async () => {
    const mock = readyMock();
    render(<App />);
    await screen.findByRole('option', { name: 'Overview' });

    act(() => mock.emitRouteRequest({ target: 'settings', settingsSection: 'updates' }));

    await waitFor(() =>
      expect(mock.api.openSettingsWindow).toHaveBeenCalledWith({ section: 'updates' }),
    );
    expect(screen.queryByRole('region', { name: 'Updates' })).not.toBeInTheDocument();
  });

  it('keeps the surface untouched when the window fails to open', async () => {
    const mock = readyMock();
    mock.api.openSettingsWindow.mockRejectedValueOnce(new Error('E_WINDOW: refused'));
    render(<App />);
    await screen.findByRole('option', { name: 'Overview' });

    act(() => mock.emitRouteRequest({ target: 'settings' }));

    await waitFor(() => expect(mock.api.openSettingsWindow).toHaveBeenCalledWith({}));
    expect(screen.getByRole('option', { name: 'Overview' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
  });

  it("reaches the window from the update popover's Updates action", async () => {
    const user = userEvent.setup();
    const mock = installAgenticoMock({
      connection: connection({ status: 'ready', stage: 'ready', ownership: 'external' }),
      readiness: readySnapshot(),
      updates: defaultUpdateState({
        status: 'ready',
        targetVersion: '0.2.0',
        packageFormat: 'macos',
        signatureStatus: 'verified',
        message: 'A verified update is downloaded and ready to install.',
      }),
    });
    render(<App />);
    await screen.findByRole('option', { name: 'Overview' });

    await user.click(await screen.findByRole('button', { name: 'Show available update' }));
    const popover = screen.getByRole('region', { name: 'Available update' });
    await user.click(within(popover).getByRole('button', { name: 'Updates' }));

    await waitFor(() =>
      expect(mock.api.openSettingsWindow).toHaveBeenCalledWith({ section: 'updates' }),
    );
  });

  it("reaches the window from the command palette's Settings entry", async () => {
    const user = userEvent.setup();
    const mock = readyMock();
    render(<App />);
    await screen.findByRole('option', { name: 'Overview' });

    act(() => mock.emitRouteRequest({ target: 'palette' }));
    const palette = await screen.findByRole('dialog', { name: 'Command palette' });
    await user.click(within(palette).getByRole('option', { name: /^Settings/ }));

    await waitFor(() => expect(mock.api.openSettingsWindow).toHaveBeenCalledWith({}));
    expect(screen.queryByRole('dialog', { name: 'Command palette' })).not.toBeInTheDocument();
  });
});

describe('App per-server attention drafts', () => {
  const helpItem: AttentionItem = {
    kind: 'help',
    id: 'feature-1:kb1234567890abcdef',
    featureId: 'abcd1234ef567890',
    sessionId: 'kb1234567890abcdef',
    phase: 'Knowledge Base',
    waitingSince: '2026-07-15T10:00:00.000Z',
    prompt: 'Where should the config live?',
    waitingKind: 'question',
  };

  function readyAt(serverKey: string, name: string): ConnectionState {
    return connection({
      status: 'ready',
      stage: 'ready',
      ownership: 'external',
      serverKey,
      serverName: name,
    });
  }

  it("restores each server's in-progress draft across A→B→A and never bleeds across", async () => {
    const user = userEvent.setup();
    const mock = installAgenticoMock({
      connection: readyAt('key-alpha', 'alpha'),
      readiness: readySnapshot(),
      attention: { items: [helpItem] },
    });
    render(<App />);
    await screen.findByRole('option', { name: 'Overview' });

    const draftOnCurrentServer = async (text: string) => {
      await user.click(await screen.findByRole('button', { name: /Attention inbox, 1 pending/ }));
      await user.click(screen.getByRole('button', { name: /Help request/ }));
      const reply = await screen.findByLabelText('Help reply');
      if (text !== '') await user.type(reply, text);
      return reply;
    };

    const alphaDraft = await draftOnCurrentServer('alpha says hi');
    expect(alphaDraft).toHaveValue('alpha says hi');

    // Ride the real connection-shell transition to server B.
    act(() => mock.emitConnection(connection({ status: 'attaching', stage: 'connect' })));
    act(() => mock.emitConnection(readyAt('key-beta', 'beta')));
    await screen.findByRole('option', { name: 'Overview' });

    const betaDraft = await draftOnCurrentServer('beta says hi');
    expect(betaDraft).toHaveValue('beta says hi');

    // Back to A: its draft is restored exactly; B's is nowhere visible.
    act(() => mock.emitConnection(connection({ status: 'attaching', stage: 'connect' })));
    act(() => mock.emitConnection(readyAt('key-alpha', 'alpha')));
    await screen.findByRole('option', { name: 'Overview' });
    expect(await draftOnCurrentServer('')).toHaveValue('alpha says hi');

    // And B still has its own.
    act(() => mock.emitConnection(connection({ status: 'attaching', stage: 'connect' })));
    act(() => mock.emitConnection(readyAt('key-beta', 'beta')));
    await screen.findByRole('option', { name: 'Overview' });
    expect(await draftOnCurrentServer('')).toHaveValue('beta says hi');
  });
});
