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

import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import type {
  ConnectionState,
  ModelCatalogue,
  ProviderModelRefreshResult,
  WorkspaceDefaults,
} from '../../../shared/ipc';
import { defaultUpdateState, installAgenticoMock, readySnapshot } from '../test/agenticoMock';
import { SettingsPanel } from './SettingsPanel';

afterEach(cleanup);

const WORKSPACE_DEFAULTS: WorkspaceDefaults = {
  models: { implementation: 'claude:old' },
  effort: {},
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
  automaticReviewEnabled: false,
};

const OLD_CATALOGUE: ModelCatalogue = {
  providerOrder: ['claude', 'codex'],
  providerModels: {
    claude: [{ id: 'old', displayName: 'Old Claude' }],
    codex: [{ id: 'gpt', displayName: 'GPT' }],
  },
  phaseDefaults: { implementation: 'claude:old' },
  phaseProviderModels: {
    implementation: { claude: ['old'], codex: ['gpt'] },
  },
};

describe('SettingsPanel provider refresh', () => {
  it('always shows row actions and applies the selected provider refresh to model pickers', async () => {
    const initialReadiness = readySnapshot({
      providers: [
        { name: 'claude', installed: true, version: '2.1.220', ready: true },
        { name: 'codex', installed: true, version: '0.145.0', ready: true },
      ],
    });
    const refreshed: ProviderModelRefreshResult = {
      readiness: {
        ...initialReadiness,
        models: { available: true, models: ['new', 'gpt'] },
      },
      catalogue: {
        ...OLD_CATALOGUE,
        providerModels: {
          ...OLD_CATALOGUE.providerModels,
          claude: [{ id: 'new', displayName: 'New Claude' }],
        },
        phaseDefaults: { implementation: 'claude:new' },
        phaseProviderModels: {
          implementation: { claude: ['new'], codex: ['gpt'] },
        },
      },
    };
    let resolveRefresh: ((value: ProviderModelRefreshResult) => void) | undefined;
    const refreshPromise = new Promise<ProviderModelRefreshResult>((resolve) => {
      resolveRefresh = resolve;
    });
    const mock = installAgenticoMock({ readiness: initialReadiness });
    mock.api.getWorkspaceDefaults.mockResolvedValue(WORKSPACE_DEFAULTS);
    mock.api.getModelCatalogue.mockResolvedValue(OLD_CATALOGUE);
    mock.api.refreshProviderModels.mockReturnValue(refreshPromise);

    const { rerender } = render(<SettingsPanel pane="providers" />);

    const claudeRow = (await screen.findByText('claude')).closest('li');
    const codexRow = (await screen.findByText('codex')).closest('li');
    expect(claudeRow).toBeDefined();
    expect(codexRow).toBeDefined();
    expect(within(claudeRow!).getByRole('button', { name: 'Recheck' })).toBeVisible();
    expect(within(codexRow!).getByRole('button', { name: 'Recheck' })).toBeVisible();

    await userEvent.click(within(claudeRow!).getByRole('button', { name: 'Recheck' }));

    expect(mock.api.refreshProviderModels).toHaveBeenCalledWith('claude');
    expect(within(claudeRow!).getByRole('button', { name: 'Rechecking…' })).toBeDisabled();
    expect(within(codexRow!).getByRole('button', { name: 'Recheck' })).toBeEnabled();

    await act(async () => {
      resolveRefresh!(refreshed);
      await refreshPromise;
    });

    // The refreshed catalogue lives on the panel, not the section, so the
    // model pickers show it as soon as the Workspace defaults pane is shown.
    rerender(<SettingsPanel pane="workspace-defaults" />);
    const picker = await screen.findByLabelText('Implementation model');
    await waitFor(() =>
      expect(within(picker).getByRole('option', { name: /New Claude/ })).toHaveValue('claude:new'),
    );
  });

  it('shows a safe error and preserves the row after refresh fails', async () => {
    const readiness = readySnapshot();
    const mock = installAgenticoMock({ readiness });
    mock.api.getWorkspaceDefaults.mockResolvedValue(WORKSPACE_DEFAULTS);
    mock.api.getModelCatalogue.mockResolvedValue(OLD_CATALOGUE);
    mock.api.refreshProviderModels.mockRejectedValue(
      new Error('E_PROVIDER_MODEL_REFRESH: Model refresh failed. Retry the provider.'),
    );

    render(<SettingsPanel pane="providers" />);
    const recheck = await screen.findByRole('button', { name: 'Recheck' });
    await userEvent.click(recheck);

    expect(await screen.findByRole('alert')).toHaveTextContent('Model refresh failed');
    expect(screen.getByRole('button', { name: 'Recheck' })).toBeEnabled();
    expect(screen.getByText('claude')).toBeVisible();
  });
});

// These update and diagnostics behaviours were asserted through App while
// Settings lived in the main window's content pane. Settings is its own window
// now, so they belong to the panel that actually owns the controls.
describe('SettingsPanel updates pane', () => {
  it('renders DEB update guidance as a copyable package-manager command without install controls', async () => {
    const user = userEvent.setup();
    installAgenticoMock({
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
    render(<SettingsPanel pane="updates" />);

    const updates = await screen.findByRole('region', { name: 'Updates' });
    // The section renders before its update state arrives, so the first
    // state-dependent assertion has to wait for it — a synchronous get here
    // races the fetch and only holds on a fast machine.
    expect(
      await within(updates).findByText('sudo apt install ./agentico_0.2.0_amd64.deb'),
    ).toBeVisible();
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

  it('preserves direct restart when a verified update is idle', async () => {
    installAgenticoMock({
      readiness: readySnapshot(),
      updates: defaultUpdateState({
        status: 'ready',
        targetVersion: '0.2.0',
        packageFormat: 'macos',
        signatureStatus: 'verified',
        message: 'A verified update is downloaded and ready to install.',
      }),
    });
    render(<SettingsPanel pane="updates" />);

    const updates = await screen.findByRole('region', { name: 'Updates' });
    expect(await within(updates).findByRole('button', { name: 'Restart to Update' })).toBeVisible();
    expect(within(updates).queryByRole('button', { name: 'Install When Idle' })).toBeNull();
    expect(within(updates).queryByRole('button', { name: 'Stop Work and Install Now' })).toBeNull();
  });

  it('shows non-interrupting and explicit stop-work update controls only when work is active', async () => {
    installAgenticoMock({
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
    render(<SettingsPanel pane="updates" />);

    const updates = await screen.findByRole('region', { name: 'Updates' });
    expect(
      await within(updates).findByText('1 workflow and AMA session are active.'),
    ).toBeVisible();
    expect(within(updates).getByRole('button', { name: 'Install When Idle' })).toBeVisible();
    expect(
      within(updates).getByRole('button', { name: 'Stop Work and Install Now' }),
    ).toBeVisible();
    expect(within(updates).queryByRole('button', { name: 'Restart to Update' })).toBeNull();
  });

  it('keeps keyboard focus inside the stop-work install confirmation and restores the trigger', async () => {
    const user = userEvent.setup();
    installAgenticoMock({
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
    render(<SettingsPanel pane="updates" />);

    const trigger = await screen.findByRole('button', { name: 'Stop Work and Install Now' });
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
});

describe('SettingsPanel diagnostics pane', () => {
  it('keeps keyboard focus inside Clear Diagnostics confirmation and restores the trigger', async () => {
    const user = userEvent.setup();
    installAgenticoMock({ readiness: readySnapshot() });
    render(<SettingsPanel pane="diagnostics" />);

    const trigger = await screen.findByRole('button', { name: 'Clear Diagnostics' });
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
});

describe('SettingsPanel workspace defaults pane', () => {
  it('stays rendered when the workspace inquireness default changes', async () => {
    const user = userEvent.setup();
    const mock = installAgenticoMock({ readiness: readySnapshot() });
    mock.api.getWorkspaceDefaults.mockResolvedValue(WORKSPACE_DEFAULTS);
    mock.api.getModelCatalogue.mockResolvedValue({
      providerOrder: [],
      providerModels: {},
      phaseDefaults: {},
      phaseProviderModels: {},
    });
    render(<SettingsPanel pane="workspace-defaults" />);

    await user.click(await screen.findByRole('radio', { name: /High/ }));

    expect(screen.getByRole('region', { name: 'Settings and readiness' })).toBeVisible();
    expect(screen.getByRole('region', { name: 'Workspace defaults' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Save changes' })).toBeEnabled();
  });
});

const READY_REMOTE: ConnectionState = {
  status: 'ready',
  stage: 'ready',
  detail: 'Runtime ready.',
  ownership: 'external',
  kind: 'remote',
};

describe('SettingsPanel workspace roots on a remote server', () => {
  it('replaces the native picker with typed paths the server validates inline', async () => {
    const user = userEvent.setup();
    const mock = installAgenticoMock({
      connection: READY_REMOTE,
      readiness: readySnapshot(),
    });
    mock.api.addWorkspaceRoot.mockRejectedValueOnce(
      new Error('invalid_workspace_root: /srv/work/gone does not exist on this server'),
    );
    render(<SettingsPanel pane="workspace-roots" />);

    // The native dialog is gone: only the server can see its own folders.
    const field = await screen.findByLabelText('Folder path on the server');
    expect(screen.queryByRole('button', { name: 'Add workspace root' })).toBeNull();

    // A non-absolute entry is refused before the server is ever asked.
    await user.type(field, 'srv/work');
    await user.click(screen.getByRole('button', { name: 'Add root' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/starting with \//);
    expect(mock.api.addWorkspaceRoot).not.toHaveBeenCalled();

    // The server's rejection renders inline and names the bad path.
    await user.clear(field);
    await user.type(field, '/srv/work/gone');
    await user.click(screen.getByRole('button', { name: 'Add root' }));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('/srv/work/gone does not exist on this server');
    expect(mock.api.addWorkspaceRoot).toHaveBeenCalledWith('/srv/work/gone');

    // A valid path saves through the same PATCH-backed action and clears.
    await user.clear(field);
    await user.type(field, '/srv/work');
    await user.click(screen.getByRole('button', { name: 'Add root' }));
    await waitFor(() => expect(mock.api.addWorkspaceRoot).toHaveBeenCalledWith('/srv/work'));
    await waitFor(() => expect(field).toHaveValue(''));
    expect(screen.queryByRole('alert')).toBeNull();
  });
});
