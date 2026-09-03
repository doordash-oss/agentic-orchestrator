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
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { defaultSettings, type ModelCatalogue, type Settings } from '../../shared/ipc';
import SettingsWindow from './SettingsWindow';
import { SETTINGS_PANE_CATALOGUE } from './features/settingsPanes';
import { defaultUpdateState, installAgenticoMock, readySnapshot } from './test/agenticoMock';

afterEach(cleanup);

beforeEach(() => {
  document.title = '';
});

/** The panes, in the order the source list documents. */
const PANE_ORDER = [
  'Workspace roots',
  'Servers',
  'Providers',
  'Appearance',
  'Updates',
  'Notifications',
  'Diagnostics',
  'Advanced',
  'Workspace defaults',
];

/** Each pane's own aria-labelled region inside the content column. */
const PANE_REGION_LABEL: Record<string, string> = {
  'workspace-roots': 'Workspace roots',
  servers: 'Servers',
  providers: 'Provider readiness',
  appearance: 'Appearance',
  updates: 'Updates',
  notifications: 'Notifications',
  diagnostics: 'Diagnostics',
  advanced: 'Advanced runtime path',
  'workspace-defaults': 'Workspace defaults',
};

const EMPTY_CATALOGUE: ModelCatalogue = {
  providerOrder: [],
  providerModels: {},
  phaseDefaults: {},
  phaseProviderModels: {},
};

function settingsWithPane(pane: Settings['settingsWindow']['pane']): Settings {
  const base = defaultSettings();
  return { ...base, settingsWindow: { ...base.settingsWindow, pane } };
}

/**
 * The Settings window is its own renderer root, so its mock is the settings
 * window's own: the readiness snapshot and update state every pane reads.
 */
function installSettingsWindowMock(overrides: { settings?: Settings } = {}) {
  const mock = installAgenticoMock({
    windowPurpose: 'settings',
    readiness: readySnapshot(),
    updates: defaultUpdateState(),
    ...(overrides.settings === undefined ? {} : { settings: overrides.settings }),
  });
  // Reached only by the Workspace defaults pane; the shared mock rejects both.
  mock.api.getWorkspaceDefaults.mockResolvedValue({
    models: {},
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
  });
  mock.api.getModelCatalogue.mockResolvedValue(EMPTY_CATALOGUE);
  return mock;
}

/** Asserts exactly one pane's region is mounted. */
function expectOnlyPaneRendered(paneId: string): void {
  for (const [id, label] of Object.entries(PANE_REGION_LABEL)) {
    const region = screen.queryByRole('region', { name: label });
    if (id === paneId) {
      expect(region).toBeInTheDocument();
    } else {
      expect(region).not.toBeInTheDocument();
    }
  }
}

describe('SettingsWindow pane list', () => {
  it('lists the panes in order with single-select listbox semantics and a roving tabindex', async () => {
    installSettingsWindowMock();
    render(<SettingsWindow />);

    const list = await screen.findByRole('listbox', { name: 'Settings panes' });
    expect(screen.getByRole('navigation', { name: 'Settings panes' })).toContainElement(list);

    const rows = within(list).getAllByRole('option');
    expect(rows.map((row) => row.textContent)).toEqual(PANE_ORDER);
    expect(rows.map((row) => row.id)).toEqual(
      SETTINGS_PANE_CATALOGUE.map((pane) => `settings-pane-${pane.id}`),
    );

    // The first-ever open lands on Workspace roots, selected and the only
    // row in the tab order.
    expect(rows.filter((row) => row.getAttribute('aria-selected') === 'true')).toHaveLength(1);
    expect(rows[0]).toHaveAttribute('aria-selected', 'true');
    expect(rows[0]).toHaveAttribute('tabindex', '0');
    expect(rows[0]).toHaveAttribute('data-selected', 'true');
    for (const row of rows.slice(1)) {
      expect(row).toHaveAttribute('tabindex', '-1');
      expect(row).toHaveAttribute('data-selected', 'false');
    }
  });

  it('renders exactly one pane at a time', async () => {
    installSettingsWindowMock();
    render(<SettingsWindow />);

    await screen.findByRole('region', { name: 'Workspace roots' });
    expect(screen.getByRole('region', { name: 'Settings and readiness' })).toBeVisible();
    expectOnlyPaneRendered('workspace-roots');
  });

  it('switches the pane on click, persists it, and titles the window with the pane label', async () => {
    const user = userEvent.setup();
    const mock = installSettingsWindowMock();
    render(<SettingsWindow />);

    await screen.findByRole('region', { name: 'Workspace roots' });
    await waitFor(() => expect(document.title).toBe('Workspace roots'));

    await user.click(screen.getByRole('option', { name: 'Notifications' }));

    expect(await screen.findByRole('region', { name: 'Notifications' })).toBeVisible();
    expectOnlyPaneRendered('notifications');
    expect(screen.getByRole('option', { name: 'Notifications' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    expect(screen.getByRole('option', { name: 'Workspace roots' })).toHaveAttribute(
      'aria-selected',
      'false',
    );
    await waitFor(() => expect(document.title).toBe('Notifications'));
    await waitFor(() =>
      expect(mock.api.updateSettings).toHaveBeenCalledWith({
        settingsWindow: { pane: 'notifications' },
      }),
    );
  });

  it('keeps the persisted window bounds while writing a new pane', async () => {
    const user = userEvent.setup();
    const base = defaultSettings();
    const mock = installSettingsWindowMock({
      settings: {
        ...base,
        settingsWindow: {
          pane: 'workspace-roots',
          bounds: { x: 20, y: 30, width: 900, height: 640 },
        },
      },
    });
    render(<SettingsWindow />);

    await screen.findByRole('region', { name: 'Workspace roots' });
    await user.click(screen.getByRole('option', { name: 'Advanced' }));

    await waitFor(() =>
      expect(mock.api.updateSettings).toHaveBeenCalledWith({
        settingsWindow: {
          pane: 'advanced',
          bounds: { x: 20, y: 30, width: 900, height: 640 },
        },
      }),
    );
  });
});

describe('SettingsWindow keyboard navigation', () => {
  it('moves focus and selection together with ArrowDown, ArrowUp, Home, and End', async () => {
    installSettingsWindowMock();
    render(<SettingsWindow />);

    const first = await screen.findByRole('option', { name: 'Workspace roots' });
    const second = screen.getByRole('option', { name: 'Servers' });
    const last = screen.getByRole('option', { name: 'Workspace defaults' });

    first.focus();
    await userEvent.keyboard('{ArrowDown}');
    expect(second).toHaveFocus();
    expect(second).toHaveAttribute('aria-selected', 'true');
    expect(first).toHaveAttribute('aria-selected', 'false');
    expect(await screen.findByRole('region', { name: 'Servers' })).toBeVisible();

    await userEvent.keyboard('{ArrowUp}');
    expect(first).toHaveFocus();
    expect(first).toHaveAttribute('aria-selected', 'true');
    expect(await screen.findByRole('region', { name: 'Workspace roots' })).toBeVisible();

    await userEvent.keyboard('{End}');
    expect(last).toHaveFocus();
    expect(last).toHaveAttribute('aria-selected', 'true');
    expect(await screen.findByRole('region', { name: 'Workspace defaults' })).toBeVisible();

    await userEvent.keyboard('{Home}');
    expect(first).toHaveFocus();
    expect(first).toHaveAttribute('aria-selected', 'true');
    expect(await screen.findByRole('region', { name: 'Workspace roots' })).toBeVisible();
  });

  it('wraps from the first row to the last with ArrowUp', async () => {
    installSettingsWindowMock();
    render(<SettingsWindow />);

    const first = await screen.findByRole('option', { name: 'Workspace roots' });
    first.focus();
    await userEvent.keyboard('{ArrowUp}');

    const last = screen.getByRole('option', { name: 'Workspace defaults' });
    expect(last).toHaveFocus();
    expect(last).toHaveAttribute('aria-selected', 'true');
  });
});

describe('SettingsWindow pane restoration', () => {
  it('restores the last-viewed pane from settings', async () => {
    installSettingsWindowMock({ settings: settingsWithPane('diagnostics') });
    render(<SettingsWindow />);

    expect(await screen.findByRole('region', { name: 'Diagnostics' })).toBeVisible();
    expectOnlyPaneRendered('diagnostics');
    expect(screen.getByRole('option', { name: 'Diagnostics' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    await waitFor(() => expect(document.title).toBe('Diagnostics'));
  });

  it('lands on Workspace roots for a settings document carrying the default pane', async () => {
    installSettingsWindowMock({ settings: defaultSettings() });
    render(<SettingsWindow />);

    expect(await screen.findByRole('region', { name: 'Workspace roots' })).toBeVisible();
    expect(screen.getByRole('option', { name: 'Workspace roots' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
  });

  it('shows a restoring status until the persisted pane arrives, never a wrong pane first', async () => {
    const mock = installSettingsWindowMock();
    let release: ((settings: Settings) => void) | undefined;
    mock.api.getSettings.mockReturnValue(
      new Promise<Settings>((resolve) => {
        release = resolve;
      }),
    );
    render(<SettingsWindow />);

    expect(screen.getByRole('status')).toHaveTextContent('Restoring settings…');
    expect(screen.queryByRole('listbox', { name: 'Settings panes' })).not.toBeInTheDocument();
    for (const label of Object.values(PANE_REGION_LABEL)) {
      expect(screen.queryByRole('region', { name: label })).not.toBeInTheDocument();
    }

    await act(async () => {
      release!(settingsWithPane('appearance'));
      await Promise.resolve();
    });

    expect(await screen.findByRole('region', { name: 'Appearance' })).toBeVisible();
    expectOnlyPaneRendered('appearance');
  });

  it('falls back to Workspace roots when the settings read fails', async () => {
    const mock = installSettingsWindowMock();
    mock.api.getSettings.mockRejectedValue(new Error('E_SETTINGS: unreadable'));
    render(<SettingsWindow />);

    expect(await screen.findByRole('option', { name: 'Workspace roots' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
  });
});

describe('SettingsWindow deep links', () => {
  it.each([
    { section: 'updates' as const, row: 'Updates', region: 'Updates', pane: 'updates' },
    {
      section: 'diagnostics' as const,
      row: 'Diagnostics',
      region: 'Diagnostics',
      pane: 'diagnostics',
    },
    { section: 'servers' as const, row: 'Servers', region: 'Servers', pane: 'servers' },
  ])(
    'switches to the $row pane for a settings route carrying $section',
    async ({ section, row, region, pane }) => {
      const mock = installSettingsWindowMock();
      render(<SettingsWindow />);

      await screen.findByRole('region', { name: 'Workspace roots' });
      act(() => mock.emitRouteRequest({ target: 'settings', settingsSection: section }));

      expect(await screen.findByRole('region', { name: region })).toBeVisible();
      expectOnlyPaneRendered(pane);
      expect(screen.getByRole('option', { name: row })).toHaveAttribute('aria-selected', 'true');
      await waitFor(() => expect(document.title).toBe(row));
    },
  );

  it('focuses the Servers pane add field for a route carrying the add-server intent', async () => {
    const mock = installSettingsWindowMock();
    render(<SettingsWindow />);

    await screen.findByRole('region', { name: 'Workspace roots' });
    act(() =>
      mock.emitRouteRequest({
        target: 'settings',
        settingsSection: 'servers',
        settingsFocus: 'add-server',
      }),
    );

    await screen.findByRole('region', { name: 'Servers' });
    await waitFor(() =>
      expect(screen.getByRole('textbox', { name: /add a remote server/i })).toHaveFocus(),
    );
  });

  it('consumes a persisted cold-open focus intent once and clears it', async () => {
    const base = defaultSettings();
    const mock = installSettingsWindowMock({
      settings: {
        ...base,
        settingsWindow: { pane: 'servers', focus: 'add-server' },
      },
    });
    render(<SettingsWindow />);

    await screen.findByRole('region', { name: 'Servers' });
    await waitFor(() =>
      expect(screen.getByRole('textbox', { name: /add a remote server/i })).toHaveFocus(),
    );
    await waitFor(() =>
      expect(mock.api.updateSettings).toHaveBeenCalledWith({
        settingsWindow: { pane: 'servers', focus: undefined },
      }),
    );
  });

  it('ignores a settings route with no section, and any other route target', async () => {
    const mock = installSettingsWindowMock({ settings: settingsWithPane('notifications') });
    render(<SettingsWindow />);

    await screen.findByRole('region', { name: 'Notifications' });
    act(() => mock.emitRouteRequest({ target: 'settings' }));
    act(() => mock.emitRouteRequest({ target: 'home' }));

    expect(screen.getByRole('region', { name: 'Notifications' })).toBeVisible();
    expectOnlyPaneRendered('notifications');
  });
});

describe('SettingsWindow panes in their new home', () => {
  it('keeps the Appearance theme radiogroup present and functional', async () => {
    const user = userEvent.setup();
    const mock = installSettingsWindowMock({ settings: settingsWithPane('appearance') });
    render(<SettingsWindow />);

    const group = await screen.findByRole('radiogroup', { name: /theme/i });
    for (const name of [/^light$/i, /^dark$/i, /^system$/i]) {
      expect(within(group).getByRole('radio', { name })).toBeInTheDocument();
    }
    expect(within(group).getByRole('radio', { name: /^system$/i })).toBeChecked();

    await user.click(within(group).getByRole('radio', { name: /^light$/i }));

    expect(mock.api.setThemePreference).toHaveBeenCalledWith('light');
    await waitFor(() =>
      expect(within(group).getByRole('radio', { name: /^light$/i })).toBeChecked(),
    );
    await waitFor(() => expect(document.documentElement.dataset['theme']).toBe('light'));
  });

  it('renders the Diagnostics pane controls and retention summary', async () => {
    installSettingsWindowMock({ settings: settingsWithPane('diagnostics') });
    render(<SettingsWindow />);

    const diagnostics = await screen.findByRole('region', { name: 'Diagnostics' });
    expect(within(diagnostics).getByRole('button', { name: 'Reveal Folder' })).toBeEnabled();
    expect(within(diagnostics).getByRole('button', { name: 'Clear Diagnostics' })).toBeEnabled();
    // The snapshot arrives a hop after the pane mounts.
    await waitFor(() => expect(within(diagnostics).getByText('2 entries')).toBeVisible());
    expect(within(diagnostics).getByText('7 days')).toBeVisible();
    expect(within(diagnostics).getByText('2 KiB used')).toBeVisible();
    expect(within(diagnostics).getByText('Agentico desktop process started.')).toBeVisible();
  });
});
