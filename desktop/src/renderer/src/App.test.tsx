import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it } from 'vitest';
import {
  ConnectionStateSchema,
  defaultSettings,
  type AttentionItem,
  type ConnectionState,
  type WorkspaceDefaults,
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
    ...overrides,
  });
}

const WORKSPACE_DEFAULTS: WorkspaceDefaults = {
  models: {},
  inquireness: 'medium',
  checkpoints: {
    inquiryReview: false,
    researchReview: false,
    designReview: false,
    roadmapReview: true,
    phasePlanReview: true,
    manualPublish: true,
    draftPublish: false,
  },
  pipeline: 'large',
  muteFeatureInput: false,
};

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

  it('switches to the light theme when the user selects it', async () => {
    const mock = installAgenticoMock({ theme: { preference: 'system', resolved: 'dark' } });
    render(<App />);
    await waitFor(() => expect(document.documentElement.dataset['theme']).toBe('dark'));

    await userEvent.click(screen.getByRole('radio', { name: /light/i }));
    expect(mock.api.setThemePreference).toHaveBeenCalledWith('light');
    await waitFor(() => expect(document.documentElement.dataset['theme']).toBe('light'));
  });

  it('offers light, dark, and system choices in an accessible radiogroup', async () => {
    installAgenticoMock();
    render(<App />);
    const group = await screen.findByRole('radiogroup', { name: /theme/i });
    expect(group).toBeInTheDocument();
    for (const name of [/light/i, /dark/i, /system/i]) {
      expect(screen.getByRole('radio', { name })).toBeInTheDocument();
    }
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
    expect(mock.api.listFeatures).toHaveBeenCalledTimes(1);
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
      settings: { ...defaultSettings(), tabs: { open: [], activeFeatureId: null } },
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

    await screen.findByRole('tab', { name: 'Home' });
    await userEvent.click(
      await screen.findByRole('button', { name: /Attention inbox, 1 pending/ }),
    );
    const inbox = screen.getByRole('complementary', { name: 'Attention inbox' });
    await userEvent.click(within(inbox).getByRole('button', { name: /Permission.*Search revamp/ }));
    expect(within(inbox).getByText('Search revamp')).toBeVisible();
  });

  it('refreshes review attention after a lifecycle invalidation', async () => {
    const mock = installAgenticoMock({
      connection: connection({ status: 'ready', stage: 'ready', ownership: 'external' }),
      readiness: readySnapshot(),
    });
    render(<App />);
    await screen.findByRole('tab', { name: 'Home' });
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
    await screen.findByRole('tab', { name: 'Home' });
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

  it('renders DEB update guidance as a copyable package-manager command without install controls', async () => {
    const user = userEvent.setup();
    installAgenticoMock({
      connection: connection({ status: 'ready', stage: 'ready', ownership: 'external' }),
      readiness: readySnapshot(),
      updates: defaultUpdateState({
        status: 'available',
        targetVersion: '0.2.0',
        packageFormat: 'deb',
        signatureStatus: 'verified',
        releaseNotesUrl: 'https://github.com/doordash-oss/agentic-orchestrator/releases/tag/v0.2.0',
        message: 'A verified DEB update is available.',
        guidance: [
          'DEB installs are updated by the package manager, not by in-app replacement.',
          'Download the signed DEB and checksum from the GitHub release.',
          'Install with: sudo apt install ./agentico_0.2.0_amd64.deb',
        ],
      }),
    });
    render(<App />);

    await user.click(await screen.findByRole('tab', { name: 'Settings' }));
    const updates = screen.getByRole('region', { name: 'Updates' });
    expect(within(updates).getByText('sudo apt install ./agentico_0.2.0_amd64.deb')).toBeVisible();
    expect(screen.queryByRole('button', { name: 'Restart to Update' })).not.toBeInTheDocument();
    expect(
      screen.queryByRole('button', { name: 'Stop Work and Install Now' }),
    ).not.toBeInTheDocument();

    await user.click(
      within(updates).getByRole('button', { name: 'Copy the package-manager command' }),
    );
    await waitFor(() =>
      expect(within(updates).getByRole('status')).toHaveTextContent(
        'Copied the package-manager command.',
      ),
    );
    await expect(window.navigator.clipboard.readText()).resolves.toBe(
      'sudo apt install ./agentico_0.2.0_amd64.deb',
    );
  });

  it('preserves direct restart in Settings when a verified update is idle', async () => {
    const user = userEvent.setup();
    installAgenticoMock({
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

    await user.click(await screen.findByRole('tab', { name: 'Settings' }));
    const updates = screen.getByRole('region', { name: 'Updates' });
    expect(within(updates).getByRole('button', { name: 'Restart to Update' })).toBeVisible();
    expect(within(updates).queryByRole('button', { name: 'Install When Idle' })).toBeNull();
    expect(within(updates).queryByRole('button', { name: 'Stop Work and Install Now' })).toBeNull();
  });

  it('shows non-interrupting and explicit stop-work update controls only when work is active', async () => {
    const user = userEvent.setup();
    installAgenticoMock({
      connection: connection({ status: 'ready', stage: 'ready', ownership: 'external' }),
      readiness: readySnapshot(),
      updates: defaultUpdateState({
        status: 'ready',
        targetVersion: '0.2.0',
        packageFormat: 'macos',
        signatureStatus: 'verified',
        message: 'A verified update is downloaded and ready to install.',
        activeWorkSummary: '1 workflow and AMA session are active.',
      }),
    });
    render(<App />);

    await user.click(await screen.findByRole('tab', { name: 'Settings' }));
    const updates = screen.getByRole('region', { name: 'Updates' });
    expect(within(updates).getByText('1 workflow and AMA session are active.')).toBeVisible();
    expect(within(updates).getByRole('button', { name: 'Install When Idle' })).toBeVisible();
    expect(
      within(updates).getByRole('button', { name: 'Stop Work and Install Now' }),
    ).toBeVisible();
    expect(within(updates).queryByRole('button', { name: 'Restart to Update' })).toBeNull();
  });

  it('keeps Settings rendered when the workspace inquireness default changes', async () => {
    const user = userEvent.setup();
    const mock = installAgenticoMock({
      connection: connection({ status: 'ready', stage: 'ready', ownership: 'external' }),
      readiness: readySnapshot(),
    });
    mock.api.getWorkspaceDefaults.mockResolvedValue(WORKSPACE_DEFAULTS);
    mock.api.getModelCatalogue.mockResolvedValue({
      providerOrder: [],
      providerModels: {},
      phaseDefaults: {},
      phaseProviderModels: {},
    });
    render(<App />);

    await user.click(await screen.findByRole('tab', { name: 'Settings' }));
    await user.click(await screen.findByRole('radio', { name: /High/ }));

    expect(screen.getByRole('tabpanel', { name: 'Settings' })).toBeVisible();
    expect(screen.getByRole('region', { name: 'Workspace defaults' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Save changes' })).toBeEnabled();
  });

  it('keeps keyboard focus inside the stop-work install confirmation and restores the trigger', async () => {
    const user = userEvent.setup();
    installAgenticoMock({
      connection: connection({ status: 'ready', stage: 'ready', ownership: 'external' }),
      readiness: readySnapshot(),
      updates: defaultUpdateState({
        status: 'ready',
        targetVersion: '0.2.0',
        packageFormat: 'macos',
        signatureStatus: 'verified',
        message: 'A verified update is downloaded and ready to install.',
        activeWorkSummary: '1 workflow and AMA session are active.',
      }),
    });
    render(<App />);

    await user.click(await screen.findByRole('tab', { name: 'Settings' }));
    const trigger = screen.getByRole('button', { name: 'Stop Work and Install Now' });
    await user.click(trigger);
    const dialog = screen.getByRole('dialog', { name: 'Install update confirmation' });
    const cancel = within(dialog).getByRole('button', { name: 'Cancel' });
    const confirm = within(dialog).getByRole('button', { name: 'Stop Work and Install Now' });
    await waitFor(() => expect(cancel).toHaveFocus());

    await user.tab();
    expect(confirm).toHaveFocus();
    await user.tab();
    expect(cancel).toHaveFocus();
    await user.tab({ shift: true });
    expect(confirm).toHaveFocus();

    await user.keyboard('{Escape}');
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Install update confirmation' })).toBeNull(),
    );
    expect(trigger).toHaveFocus();
  });

  it('keeps keyboard focus inside Clear Diagnostics confirmation and restores the trigger', async () => {
    const user = userEvent.setup();
    installAgenticoMock({
      connection: connection({ status: 'ready', stage: 'ready', ownership: 'external' }),
      readiness: readySnapshot(),
    });
    render(<App />);

    await user.click(await screen.findByRole('tab', { name: 'Settings' }));
    const trigger = screen.getByRole('button', { name: 'Clear Diagnostics' });
    await user.click(trigger);
    const dialog = screen.getByRole('dialog', { name: 'Clear diagnostics confirmation' });
    expect(within(dialog).getByRole('heading', { name: 'Clear Diagnostics?' })).toBeVisible();
    const cancel = within(dialog).getByRole('button', { name: 'Cancel' });
    const confirm = within(dialog).getByRole('button', { name: 'Clear Diagnostics' });
    await waitFor(() => expect(cancel).toHaveFocus());

    await user.tab({ shift: true });
    expect(confirm).toHaveFocus();
    await user.keyboard('{Escape}');
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Clear diagnostics confirmation' })).toBeNull(),
    );
    expect(trigger).toHaveFocus();
  });

  it('shows the connection shell — never the wizard — before the gateway is ready', async () => {
    const mock = installAgenticoMock();
    render(<App />);
    await waitFor(() =>
      expect(screen.getAllByRole('heading', { name: /^agentico$/i })).toHaveLength(2),
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
    expect(await screen.findByRole('tab', { name: 'Home' })).toBeInTheDocument();
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
