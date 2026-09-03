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
import { afterEach, describe, expect, it, vi } from 'vitest';
import { defaultSettings } from '../../../shared/ipc';
import { FEATURE_COMMAND_IDS, commandById } from '../../../shared/commands';
import { registerFeatureCommandExecutor } from '../features/featureCommands';
import { featureSnapshot, installAgenticoMock } from '../test/agenticoMock';
import { CommandPalette } from './CommandPalette';

let unregister: (() => void) | null = null;

afterEach(() => {
  unregister?.();
  unregister = null;
  cleanup();
});

const ACTIVE_FEATURE_ID = 'active1234abcd5678';

/** Stands in for the mounted cockpit: the funnel's registered executor. */
function registerCockpit(featureId: string, actions: Parameters<typeof featureSnapshot>[0] = {}) {
  const run = vi.fn();
  unregister = registerFeatureCommandExecutor({
    featureId,
    actions: () => featureSnapshot({ id: featureId, ...actions }).actions,
    run,
    toggleInspector: vi.fn(),
  });
  return run;
}

const MIXED_ACTIONS = [
  { id: 'start', enabled: true, disabledReasons: [] },
  {
    id: 'pause-stop',
    enabled: false,
    disabledReasons: [{ code: 'not_running', message: 'nothing is running' }],
  },
  { id: 'delete', enabled: true, disabledReasons: [] },
];

function renderPalette(options: { selectedRowId?: string | null } = {}) {
  const rowId = options.selectedRowId;
  return render(
    <>
      {rowId === undefined || rowId === null ? null : (
        <div id={`sidebar-row-${rowId}`} role="option" aria-selected="true">
          Active row
        </div>
      )}
      <CommandPalette
        ready
        routeRequest={{ id: 1, event: { target: 'palette' } }}
        onRoute={() => undefined}
      />
    </>,
  );
}

describe('CommandPalette feature group', () => {
  it('lists all fifteen verbs with catalogue labels, mixed enablement, and reasons', async () => {
    const mock = installAgenticoMock({
      settings: {
        ...defaultSettings(),
        shell: {
          featureByServer: { 'default-runtime': ACTIVE_FEATURE_ID },
          sidebarCollapsed: false,
        },
      },
    });
    mock.api.getFeature.mockImplementation((featureId: string) =>
      Promise.resolve(featureSnapshot({ id: featureId, actions: MIXED_ACTIONS })),
    );
    renderPalette({ selectedRowId: ACTIVE_FEATURE_ID });

    const group = await screen.findByRole('region', { name: 'Feature' });
    await waitFor(() =>
      expect(within(group).getByRole('option', { name: /^Start/ })).toBeEnabled(),
    );
    expect(within(group).getAllByRole('option')).toHaveLength(FEATURE_COMMAND_IDS.length);
    for (const id of FEATURE_COMMAND_IDS) {
      expect(within(group).getByText(commandById(id).label)).toBeInTheDocument();
    }
    expect(within(group).getByRole('option', { name: /^Delete/ })).toBeEnabled();
    // Configuration is always available once a feature is selected.
    expect(within(group).getByRole('option', { name: /^Configuration/ })).toBeEnabled();
    // A disabled verb stays visible and carries its server reason.
    const stop = within(group).getByRole('option', { name: /^Stop/ });
    expect(stop).toBeDisabled();
    expect(stop).toHaveTextContent('nothing is running');
    expect(within(group).getByRole('option', { name: /^Merge/ })).toBeDisabled();
  });

  it('disables the whole group with the no-active-feature reason on Overview', async () => {
    installAgenticoMock({
      settings: { ...defaultSettings(), shell: { featureByServer: {}, sidebarCollapsed: false } },
    });
    renderPalette();

    const group = await screen.findByRole('region', { name: 'Feature' });
    await waitFor(() => expect(within(group).getAllByRole('option')).toHaveLength(15));
    for (const option of within(group).getAllByRole('option')) {
      expect(option).toBeDisabled();
      expect(option).toHaveTextContent('No feature is selected.');
    }
  });

  it('dispatches through the funnel against the selected row, not the stale persisted id', async () => {
    const mock = installAgenticoMock({
      settings: {
        ...defaultSettings(),
        shell: {
          featureByServer: { 'default-runtime': 'stale1234abcd5678' },
          sidebarCollapsed: false,
        },
      },
    });
    mock.api.getFeature.mockImplementation((featureId: string) =>
      Promise.resolve(featureSnapshot({ id: featureId, actions: MIXED_ACTIONS })),
    );
    const run = registerCockpit(ACTIVE_FEATURE_ID, { actions: MIXED_ACTIONS });
    renderPalette({ selectedRowId: ACTIVE_FEATURE_ID });

    const palette = await screen.findByRole('dialog', { name: 'Command palette' });
    const group = await screen.findByRole('region', { name: 'Feature' });
    await waitFor(() =>
      expect(within(group).getByRole('option', { name: /^Start/ })).toBeEnabled(),
    );
    await userEvent.click(within(group).getByRole('option', { name: /^Start/ }));

    await waitFor(() => expect(run).toHaveBeenCalledWith('feature.start'));
    // The funnel owns dispatch now, so the palette never calls the raw action.
    expect(mock.api.dispatchFeatureAction).not.toHaveBeenCalled();
    expect(palette).not.toBeInTheDocument();
  });

  it('is usable on the frame it opens, without waiting on its own snapshot fetch', async () => {
    // The mounted cockpit already holds the live catalogue, so a slow (here:
    // never-resolving) settings/snapshot round trip must not leave the group
    // disabled and swallow the first keystroke.
    const mock = installAgenticoMock({
      settings: {
        ...defaultSettings(),
        shell: {
          featureByServer: { 'default-runtime': ACTIVE_FEATURE_ID },
          sidebarCollapsed: false,
        },
      },
    });
    mock.api.getSettings.mockReturnValue(new Promise(() => {}));
    mock.api.getFeature.mockReturnValue(new Promise(() => {}));
    const run = registerCockpit(ACTIVE_FEATURE_ID, { actions: MIXED_ACTIONS });
    renderPalette({ selectedRowId: ACTIVE_FEATURE_ID });

    const group = await screen.findByRole('region', { name: 'Feature' });
    expect(within(group).getByRole('option', { name: /^Start/ })).toBeEnabled();
    expect(within(group).getByRole('option', { name: /^Stop/ })).toBeDisabled();

    await userEvent.type(screen.getByLabelText('Search features and commands'), 'start feature');
    await userEvent.keyboard('{Enter}');
    await waitFor(() => expect(run).toHaveBeenCalledWith('feature.start'));
  });

  it('is a no-op when the mounted cockpit is showing a different feature', async () => {
    const mock = installAgenticoMock({
      settings: {
        ...defaultSettings(),
        shell: {
          featureByServer: { 'default-runtime': ACTIVE_FEATURE_ID },
          sidebarCollapsed: false,
        },
      },
    });
    mock.api.getFeature.mockImplementation((featureId: string) =>
      Promise.resolve(featureSnapshot({ id: featureId, actions: MIXED_ACTIONS })),
    );
    const run = registerCockpit('1234abcd5678ef90', { actions: MIXED_ACTIONS });
    renderPalette({ selectedRowId: ACTIVE_FEATURE_ID });

    const group = await screen.findByRole('region', { name: 'Feature' });
    await waitFor(() =>
      expect(within(group).getByRole('option', { name: /^Start/ })).toBeEnabled(),
    );
    await userEvent.click(within(group).getByRole('option', { name: /^Start/ }));

    expect(run).not.toHaveBeenCalled();
    expect(mock.api.dispatchFeatureAction).not.toHaveBeenCalled();
  });
});

const FEATURE_ROWS = [
  {
    id: 'checkout1234abcd56',
    name: 'Checkout redesign',
    status: 'Running',
    currentPhase: 'implement',
    repos: ['pedregal'],
    createdAt: '2026-08-01T00:00:00Z',
    activeRun: 1,
    runCount: 1,
    warnings: [],
    errors: [],
  },
  {
    id: 'search1234abcd5678',
    name: 'Search revamp',
    status: 'Created',
    currentPhase: 'plan',
    repos: ['pedregal'],
    createdAt: '2026-08-02T00:00:00Z',
    activeRun: 0,
    runCount: 0,
    warnings: [],
    errors: [],
  },
];

describe('CommandPalette feature search', () => {
  function installFeatures() {
    return installAgenticoMock({
      settings: { ...defaultSettings(), shell: { featureByServer: {}, sidebarCollapsed: false } },
      features: FEATURE_ROWS,
    });
  }

  it('lists features matching the query by name, with their status and phase', async () => {
    installFeatures();
    renderPalette();

    await screen.findByRole('dialog', { name: 'Command palette' });
    // Features are a search result: an empty query is the command catalogue.
    expect(screen.queryByRole('region', { name: 'Features' })).toBeNull();

    await userEvent.type(screen.getByLabelText('Search features and commands'), 'revamp');
    const group = await screen.findByRole('region', { name: 'Features' });
    const rows = within(group).getAllByRole('option');
    expect(rows).toHaveLength(1);
    expect(rows[0]).toHaveTextContent('Search revamp');
    expect(rows[0]).toHaveTextContent('Created · Plan');
  });

  it('selects the matched feature and closes', async () => {
    installFeatures();
    const onRoute = vi.fn();
    render(
      <CommandPalette
        ready
        routeRequest={{ id: 1, event: { target: 'palette' } }}
        onRoute={onRoute}
      />,
    );

    const palette = await screen.findByRole('dialog', { name: 'Command palette' });
    await userEvent.type(screen.getByLabelText('Search features and commands'), 'checkout');
    await userEvent.click(await screen.findByRole('option', { name: /Checkout redesign/ }));

    expect(onRoute).toHaveBeenCalledWith({
      target: 'select-feature',
      featureId: 'checkout1234abcd56',
    });
    expect(palette).not.toBeInTheDocument();
  });

  it('puts a prefix match under the cursor so Enter opens it', async () => {
    installFeatures();
    const onRoute = vi.fn();
    render(
      <CommandPalette
        ready
        routeRequest={{ id: 1, event: { target: 'palette' } }}
        onRoute={onRoute}
      />,
    );

    await screen.findByRole('dialog', { name: 'Command palette' });
    await userEvent.type(screen.getByLabelText('Search features and commands'), 'search');
    await waitFor(() =>
      expect(screen.getByRole('option', { selected: true })).toHaveTextContent('Search revamp'),
    );
    await userEvent.keyboard('{Enter}');

    expect(onRoute).toHaveBeenCalledWith({
      target: 'select-feature',
      featureId: 'search1234abcd5678',
    });
  });

  it('reports no matches when neither a feature nor a command matches', async () => {
    installFeatures();
    renderPalette();

    await screen.findByRole('dialog', { name: 'Command palette' });
    await userEvent.type(screen.getByLabelText('Search features and commands'), 'zzzznope');
    expect(await screen.findByText('No features or commands match.')).toBeInTheDocument();
  });
});

describe('CommandPalette keyboard selection', () => {
  it('keeps the highlighted entry in view as the arrow keys walk past the fold', async () => {
    // jsdom has no scrollIntoView, and no layout to scroll — the assertion is
    // that the palette asks for the highlighted row, which is the only
    // keyboard-focus signifier it has (DOM focus stays in the search input).
    const scrollIntoView = vi.fn();
    const original = (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView;
    (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView = scrollIntoView;
    try {
      const mock = installAgenticoMock({
        settings: {
          ...defaultSettings(),
          shell: {
            featureByServer: { 'default-runtime': ACTIVE_FEATURE_ID },
            sidebarCollapsed: false,
          },
        },
      });
      mock.api.getFeature.mockImplementation((featureId: string) =>
        Promise.resolve(featureSnapshot({ id: featureId, actions: MIXED_ACTIONS })),
      );
      renderPalette({ selectedRowId: ACTIVE_FEATURE_ID });

      const dialog = await screen.findByRole('dialog', { name: 'Command palette' });
      await waitFor(() =>
        expect(within(dialog).getByRole('option', { name: /^Start/ })).toBeEnabled(),
      );
      scrollIntoView.mockClear();

      const enabled = within(dialog)
        .getAllByRole('option')
        .filter((option) => !(option as HTMLButtonElement).disabled);
      expect(enabled.length).toBeGreaterThan(3);
      const input = screen.getByLabelText('Search features and commands');
      input.focus();
      for (let step = 0; step < enabled.length - 1; step += 1) {
        await userEvent.keyboard('{ArrowDown}');
      }

      // The walk ended on the last enabled entry, and every step asked for the
      // newly highlighted row.
      const selected = within(dialog).getByRole('option', { selected: true });
      expect(selected).toBe(enabled[enabled.length - 1]);
      expect(scrollIntoView).toHaveBeenCalledTimes(enabled.length - 1);
      expect(scrollIntoView).toHaveBeenLastCalledWith({ block: 'nearest' });
    } finally {
      (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView = original;
    }
  });
});

describe('CommandPalette global entries', () => {
  it('carries New Feature and the two View toggles, and never Close Window or Quit', async () => {
    installAgenticoMock({
      settings: { ...defaultSettings(), shell: { featureByServer: {}, sidebarCollapsed: false } },
    });
    renderPalette();

    const dialog = await screen.findByRole('dialog', { name: 'Command palette' });
    expect(within(dialog).getByRole('option', { name: /^New Feature/ })).toBeEnabled();
    expect(within(dialog).getByRole('option', { name: /^Show\/Hide Sidebar/ })).toBeEnabled();
    // Overview has no inspector to show or hide.
    expect(within(dialog).getByRole('option', { name: /^Show\/Hide Inspector/ })).toBeDisabled();
    expect(within(dialog).queryByText('Close Window')).toBeNull();
    expect(within(dialog).queryByText('Quit Agentico')).toBeNull();
    expect(within(dialog).queryByText('Show Agentico')).toBeNull();
  });

  it('routes New Feature and the sidebar toggle to their catalogue targets', async () => {
    installAgenticoMock({
      settings: { ...defaultSettings(), shell: { featureByServer: {}, sidebarCollapsed: false } },
    });
    const onRoute = vi.fn();
    render(
      <CommandPalette
        ready
        routeRequest={{ id: 1, event: { target: 'palette' } }}
        onRoute={onRoute}
      />,
    );

    const dialog = await screen.findByRole('dialog', { name: 'Command palette' });
    await userEvent.click(within(dialog).getByRole('option', { name: /^New Feature/ }));
    expect(onRoute).toHaveBeenCalledWith({ target: 'new-feature' });
  });

  it('enables Show/Hide Inspector once a feature is selected', async () => {
    const mock = installAgenticoMock({
      settings: {
        ...defaultSettings(),
        shell: {
          featureByServer: { 'default-runtime': ACTIVE_FEATURE_ID },
          sidebarCollapsed: false,
        },
      },
    });
    mock.api.getFeature.mockImplementation((featureId: string) =>
      Promise.resolve(featureSnapshot({ id: featureId, actions: MIXED_ACTIONS })),
    );
    renderPalette({ selectedRowId: ACTIVE_FEATURE_ID });

    const dialog = await screen.findByRole('dialog', { name: 'Command palette' });
    await waitFor(() =>
      expect(within(dialog).getByRole('option', { name: /^Show\/Hide Inspector/ })).toBeEnabled(),
    );
  });
});
