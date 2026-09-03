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

import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  actionableAttentionCount,
  defaultSettings,
  type AttentionItem,
  type ConnectionState,
  type MainWindowUiState,
  type Settings,
  type UpdateState,
} from '../../../shared/ipc';
import { featureSnapshot, installAgenticoMock } from '../test/agenticoMock';
import { dispatchMediaChange, matchMediaState } from '../test/setup';
import { WorkspaceShell } from './WorkspaceShell';

vi.mock('monaco-editor', () => ({
  editor: {
    create: vi.fn(() => ({
      dispose: vi.fn(),
      onDidChangeModelContent: vi.fn(),
      getValue: vi.fn(() => ''),
      setValue: vi.fn(),
    })),
    createModel: vi.fn(() => ({ dispose: vi.fn() })),
    createDiffEditor: vi.fn(() => ({ dispose: vi.fn(), setModel: vi.fn() })),
    setTheme: vi.fn(),
  },
}));

afterEach(() => {
  cleanup();
  matchMediaState.narrowShell = false;
});

const FEATURE_ID = 'abcd1234ef567890';
const SECOND_FEATURE_ID = '1234abcd5678ef90';

function settingsWithActive(featureId: string | null = FEATURE_ID): Settings {
  return {
    ...defaultSettings(),
    shell: {
      featureByServer: featureId === null ? {} : { 'default-runtime': featureId },
      sidebarCollapsed: false,
    },
  };
}

function summaryOf(feature: ReturnType<typeof featureSnapshot>) {
  return {
    id: feature.id,
    name: feature.name,
    status: feature.status,
    currentPhase: feature.currentPhase,
    repos: feature.repos,
    createdAt: feature.createdAt,
    activeRun: feature.activeRun,
    runCount: 1,
    warnings: [],
  };
}

describe('WorkspaceShell sidebar', () => {
  it('keeps Overview selected on first render and enters creation deliberately', async () => {
    const feature = featureSnapshot({ id: FEATURE_ID, name: 'Search revamp', status: 'Created' });
    installAgenticoMock({ features: [summaryOf(feature)] });
    render(<WorkspaceShell />);

    expect(await screen.findByRole('option', { name: 'Overview' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    const listRegion = await screen.findByRole('region', { name: 'Existing features' });
    expect(within(listRegion).getByText('Search revamp')).toBeInTheDocument();
    expect(screen.queryByRole('form', { name: /create a feature/i })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'New feature' }));
    expect(await screen.findByRole('form', { name: /create a feature/i })).toBeInTheDocument();
  });

  it('groups features into lane sections with correct counts and hides empty lanes', async () => {
    const waiting = featureSnapshot({
      id: 'waiting1ef567890a',
      name: 'Needs a decision',
      status: 'Failed',
      actions: [],
    });
    const running = featureSnapshot({
      id: 'running1ef567890a',
      name: 'Mid-flight feature',
      status: 'Implementing',
      setup: { status: 'done', attempt: 1, tasks: [] },
      actions: [],
    });
    const published = featureSnapshot({
      id: 'publish1ef567890a',
      name: 'Shipped feature',
      status: 'Published',
      setup: { status: 'done', attempt: 1, tasks: [] },
      actions: [],
    });
    const snapshots = [waiting, running, published];
    const mock = installAgenticoMock({ features: snapshots.map(summaryOf) });
    mock.api.getFeature.mockImplementation((featureId: string) =>
      Promise.resolve(snapshots.find((snapshot) => snapshot.id === featureId) ?? snapshots[0]!),
    );
    render(<WorkspaceShell />);

    await screen.findByRole('option', { name: 'Overview' });
    // Populated lanes render with their count and members.
    const waitingGroup = await screen.findByRole('group', { name: 'Waiting on you' });
    expect(within(waitingGroup).getByText('Needs a decision')).toBeInTheDocument();
    const runningGroup = screen.getByRole('group', { name: 'Running' });
    expect(within(runningGroup).getByText('Mid-flight feature')).toBeInTheDocument();
    const publishedGroup = screen.getByRole('group', { name: 'Published' });
    expect(within(publishedGroup).getByText('Shipped feature')).toBeInTheDocument();
    // Lanes with no members never render a section.
    expect(screen.queryByRole('group', { name: 'Done' })).not.toBeInTheDocument();
    expect(screen.queryByRole('group', { name: 'At rest' })).not.toBeInTheDocument();
  });

  it.each([
    {
      label: 'bare phase when neither roadmap nor iteration data is present',
      currentRoadmapPhase: undefined,
      totalRoadmapPhases: undefined,
      currentIteration: undefined,
      expected: 'Implement',
    },
    {
      label: 'phase and iteration when there is no roadmap',
      currentRoadmapPhase: undefined,
      totalRoadmapPhases: undefined,
      currentIteration: 3,
      expected: 'Implement · iteration 3',
    },
    {
      label: 'phase and roadmap phase-of-total when there is no iteration',
      currentRoadmapPhase: 2,
      totalRoadmapPhases: 5,
      currentIteration: undefined,
      expected: 'Implement · phase 2/5',
    },
    {
      label: 'phase, roadmap phase-of-total, and iteration when all are present',
      currentRoadmapPhase: 2,
      totalRoadmapPhases: 5,
      currentIteration: 3,
      expected: 'Implement · phase 2/5 · iteration 3',
    },
  ])(
    'renders identical running sub-line copy in the sidebar and Overview: $label',
    async ({ currentRoadmapPhase, totalRoadmapPhases, currentIteration, expected }) => {
      const running = featureSnapshot({
        id: FEATURE_ID,
        name: 'Mid-flight feature',
        status: 'Implementing',
        currentPhase: 'Implement',
        currentRoadmapPhase,
        totalRoadmapPhases,
        currentIteration,
        setup: { status: 'done', attempt: 1, tasks: [] },
        actions: [],
      });
      const mock = installAgenticoMock({ features: [summaryOf(running)] });
      mock.api.getFeature.mockResolvedValue(running);
      render(<WorkspaceShell />);

      const sidebarRow = (
        await screen.findByRole('option', { name: /Mid-flight feature/ })
      ).closest('[role="option"]')!;
      expect(sidebarRow.querySelector('.sidebar__row-subline')?.textContent).toBe(expected);

      const lanes = await screen.findByRole('region', { name: 'Existing features' });
      const overviewRow = within(lanes).getByText('Mid-flight feature').closest('li')!;
      expect(overviewRow.querySelector('.overview-row__state')?.textContent).toBe(expected);
    },
  );

  it('shows Answer on a waiting-lane row and Open on every other lane row', async () => {
    const waiting = featureSnapshot({
      id: 'waiting1ef567890a',
      name: 'Needs a decision',
      status: 'Failed',
      actions: [],
    });
    const running = featureSnapshot({
      id: 'running1ef567890a',
      name: 'Mid-flight feature',
      status: 'Implementing',
      setup: { status: 'done', attempt: 1, tasks: [] },
      actions: [],
    });
    const snapshots = [waiting, running];
    const mock = installAgenticoMock({ features: snapshots.map(summaryOf) });
    mock.api.getFeature.mockImplementation((featureId: string) =>
      Promise.resolve(snapshots.find((snapshot) => snapshot.id === featureId) ?? snapshots[0]!),
    );
    render(<WorkspaceShell />);

    const lanes = await screen.findByRole('region', { name: 'Existing features' });
    const waitingRow = within(lanes).getByText('Needs a decision').closest('li')!;
    expect(within(waitingRow).getByRole('button', { name: 'Answer' })).toBeInTheDocument();

    const runningRow = within(lanes).getByText('Mid-flight feature').closest('li')!;
    expect(within(runningRow).getByRole('button', { name: 'Open' })).toBeInTheDocument();
  });

  it('opens the feature when clicking an Open row, and jumps via onAttentionJump when Answer has a pending item', async () => {
    const onAttentionJump = vi.fn();
    const waiting = featureSnapshot({
      id: FEATURE_ID,
      name: 'Needs a decision',
      status: 'Failed',
      actions: [],
    });
    const rested = featureSnapshot({
      id: SECOND_FEATURE_ID,
      name: 'Resting feature',
      status: 'CodeReady',
      setup: { status: 'done', attempt: 1, tasks: [] },
      actions: [],
    });
    const snapshots = [waiting, rested];
    const mock = installAgenticoMock({ features: snapshots.map(summaryOf) });
    mock.api.getFeature.mockImplementation((featureId: string) =>
      Promise.resolve(snapshots.find((snapshot) => snapshot.id === featureId) ?? snapshots[0]!),
    );
    const attentionItems = [
      {
        kind: 'help' as const,
        id: 'attn-1',
        featureId: FEATURE_ID,
        waitingSince: '2026-08-05T10:00:00Z',
        prompt: 'need input',
      },
    ];
    render(<WorkspaceShell attentionItems={attentionItems} onAttentionJump={onAttentionJump} />);

    const lanes = await screen.findByRole('region', { name: 'Existing features' });
    const restingRow = within(lanes).getByText('Resting feature').closest('li')!;
    await userEvent.click(within(restingRow).getByRole('button', { name: 'Open' }));
    expect(
      await screen.findByRole('region', { name: 'Feature Resting feature' }),
    ).toBeInTheDocument();

    await userEvent.click(await screen.findByRole('option', { name: 'Overview' }));
    const lanesAgain = await screen.findByRole('region', { name: 'Existing features' });
    const waitingRow = within(lanesAgain).getByText('Needs a decision').closest('li')!;
    await userEvent.click(within(waitingRow).getByRole('button', { name: 'Answer' }));
    expect(onAttentionJump).toHaveBeenCalledWith(FEATURE_ID, 'attn-1');
  });

  it('selects a feature by pointer click, mounting exactly one cockpit at a time', async () => {
    const feature = featureSnapshot({
      id: FEATURE_ID,
      name: 'Search revamp',
      status: 'Implementing',
      setup: { status: 'done', attempt: 1, tasks: [] },
      actions: [],
    });
    const mock = installAgenticoMock({ features: [summaryOf(feature)] });
    mock.api.getFeature.mockResolvedValue(feature);
    render(<WorkspaceShell />);
    const user = userEvent.setup();

    const row = await screen.findByRole('option', { name: /Search revamp/ });
    await user.click(row);

    expect(row).toHaveAttribute('aria-selected', 'true');
    expect(await screen.findByLabelText('Feature Search revamp')).toBeInTheDocument();
    expect(screen.queryByRole('option', { name: 'Overview' })).toHaveAttribute(
      'aria-selected',
      'false',
    );
    expect(screen.queryByRole('region', { name: 'Existing features' })).not.toBeInTheDocument();

    // Selecting Overview again unmounts the cockpit.
    await user.click(screen.getByRole('option', { name: 'Overview' }));
    expect(screen.queryByLabelText('Feature Search revamp')).not.toBeInTheDocument();
    expect(await screen.findByRole('region', { name: 'Existing features' })).toBeInTheDocument();
  });

  it('marks the pinned Overview row with the house glyph and leaves feature rows on status dots', async () => {
    const feature = featureSnapshot({
      id: FEATURE_ID,
      name: 'Search revamp',
      status: 'Implementing',
      setup: { status: 'done', attempt: 1, tasks: [] },
      actions: [],
    });
    const mock = installAgenticoMock({ features: [summaryOf(feature)] });
    mock.api.getFeature.mockResolvedValue(feature);
    render(<WorkspaceShell />);

    const overviewRow = await screen.findByRole('option', { name: 'Overview' });
    const overviewGlyph = overviewRow.querySelector('.sidebar__row-glyph');
    expect(overviewGlyph).not.toBeNull();
    expect(overviewGlyph).toHaveClass('sidebar__row-glyph--house');
    // Decorative: the glyph adds nothing to the row's accessible name.
    expect(overviewGlyph).toHaveAttribute('aria-hidden', 'true');
    expect(overviewGlyph!.querySelector('svg')).not.toBeNull();

    const featureRow = await screen.findByRole('option', { name: /Search revamp/ });
    const featureGlyph = featureRow.querySelector('.sidebar__row-glyph');
    expect(featureGlyph).not.toBeNull();
    expect(featureGlyph).not.toHaveClass('sidebar__row-glyph--house');
    expect(featureGlyph).toHaveAttribute('data-tone');

    // The glyph swap survives selection, where the row inverts its ink.
    await userEvent.click(overviewRow);
    expect(
      (await screen.findByRole('option', { name: 'Overview' })).querySelector(
        '.sidebar__row-glyph--house',
      ),
    ).not.toBeNull();
  });

  it('keeps exactly one row selected with a roving tabindex across Overview and lane rows', async () => {
    const feature = featureSnapshot({
      id: FEATURE_ID,
      name: 'Search revamp',
      status: 'Implementing',
      setup: { status: 'done', attempt: 1, tasks: [] },
      actions: [],
    });
    const mock = installAgenticoMock({ features: [summaryOf(feature)] });
    mock.api.getFeature.mockResolvedValue(feature);
    render(<WorkspaceShell />);

    const overviewRow = await screen.findByRole('option', { name: 'Overview' });
    const featureRow = await screen.findByRole('option', { name: /Search revamp/ });
    expect(overviewRow).toHaveAttribute('tabindex', '0');
    expect(featureRow).toHaveAttribute('tabindex', '-1');

    await userEvent.click(featureRow);
    expect(overviewRow).toHaveAttribute('tabindex', '-1');
    expect(featureRow).toHaveAttribute('tabindex', '0');

    const selected = screen
      .getAllByRole('option')
      .filter((row) => row.getAttribute('aria-selected') === 'true');
    expect(selected).toHaveLength(1);
  });

  it("restores the previously active feature from the shell's per-server map and persists a new selection", async () => {
    const mock = installAgenticoMock({
      settings: settingsWithActive(FEATURE_ID),
      features: [summaryOf(featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' }))],
      feature: featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' }),
    });
    render(<WorkspaceShell />);

    expect(
      await screen.findByRole('region', { name: 'Feature Search revamp' }),
    ).toBeInTheDocument();
    expect(screen.getByRole('option', { name: /Search revamp/ })).toHaveAttribute(
      'aria-selected',
      'true',
    );

    await userEvent.click(screen.getByRole('option', { name: 'Overview' }));
    await waitFor(() =>
      expect(mock.api.updateSettings).toHaveBeenCalledWith({
        shell: { setActiveFeature: { serverKey: 'default-runtime', featureId: null } },
      }),
    );
  });

  it('collapses the Done lane by default while other lanes start expanded', async () => {
    const done = featureSnapshot({
      id: FEATURE_ID,
      name: 'Finished feature',
      status: 'Done',
      setup: { status: 'done', attempt: 1, tasks: [] },
    });
    const waiting = featureSnapshot({
      id: SECOND_FEATURE_ID,
      name: 'Blocked feature',
      status: 'Failed',
      setup: { status: 'done', attempt: 1, tasks: [] },
      actions: [],
    });
    const snapshots = [done, waiting];
    const mock = installAgenticoMock({ features: snapshots.map(summaryOf) });
    mock.api.getFeature.mockImplementation((featureId: string) =>
      Promise.resolve(snapshots.find((snapshot) => snapshot.id === featureId) ?? snapshots[0]!),
    );
    render(<WorkspaceShell />);

    const doneGroup = await screen.findByRole('group', { name: 'Done' });
    const doneDetails = doneGroup.closest('details');
    expect(doneDetails).not.toBeNull();
    expect(doneDetails).not.toHaveAttribute('open');

    const waitingGroup = screen.getByRole('group', { name: 'Waiting on you' });
    const waitingDetails = waitingGroup.closest('details');
    expect(waitingDetails).toHaveAttribute('open');
  });

  it('removes a deleted feature from the sidebar and returns to Overview', async () => {
    const feature = featureSnapshot({
      id: FEATURE_ID,
      name: 'Search revamp',
      status: 'Created',
      actions: [
        {
          id: 'delete',
          enabled: true,
          disabledReasons: [],
          impactPreview: {
            kind: 'parent_cascade_delete',
            subject: { id: FEATURE_ID, name: 'Search revamp' },
            categories: [{ key: 'children', label: 'Children', items: [] }],
            retained: [],
          },
        },
      ],
    });
    const mock = installAgenticoMock({
      settings: settingsWithActive(FEATURE_ID),
      features: [summaryOf(feature)],
      feature,
    });
    mock.api.deleteFeatureCascade.mockResolvedValue({
      featureId: FEATURE_ID,
      operationId: 'delete-1',
      status: 'completed',
      diagnostics: [],
    });
    render(<WorkspaceShell />);
    const user = userEvent.setup();

    expect(
      await screen.findByRole('region', { name: 'Feature Search revamp' }),
    ).toBeInTheDocument();
    mock.api.listFeatures.mockResolvedValueOnce([]);
    await user.click(await screen.findByLabelText('More actions'));
    await user.click(screen.getByRole('menuitem', { name: 'Delete feature' }));
    const dialog = await screen.findByRole('dialog', { name: /Delete Search revamp/ });
    await user.click(within(dialog).getByRole('button', { name: 'Delete feature' }));

    await waitFor(() =>
      expect(mock.api.deleteFeatureCascade).toHaveBeenCalledWith({ featureId: FEATURE_ID }),
    );
    expect(await screen.findByRole('option', { name: 'Overview' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    expect(screen.queryByRole('option', { name: /Search revamp/ })).not.toBeInTheDocument();
  });

  it('opens a feature after creation from the Overview surface', async () => {
    const mock = installAgenticoMock();
    render(<WorkspaceShell />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'New feature' }));
    await screen.findByRole('form', { name: /create a feature/i });

    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    await user.type(screen.getByLabelText('Name'), 'Search revamp');
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));
    await user.click(screen.getByRole('button', { name: 'Create and start' }));

    expect(
      await screen.findByRole('region', { name: 'Feature Search revamp' }),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(mock.api.updateSettings).toHaveBeenCalledWith({
        shell: { setActiveFeature: { serverKey: 'default-runtime', featureId: FEATURE_ID } },
      }),
    );
    expect(mock.api.getFeature).toHaveBeenCalledWith(FEATURE_ID);
  });

  it('shows an in-flow create call-to-action on the empty Overview and opens creation from it', async () => {
    installAgenticoMock();
    render(<WorkspaceShell />);
    const user = userEvent.setup();

    await screen.findByText('Turn a goal into a supervised run.');
    await user.click(await screen.findByRole('button', { name: 'Create a feature' }));
    await screen.findByRole('form', { name: /create a feature/i });
  });

  it('cancels a dirty creation sheet only after confirmation, restoring focus to New feature', async () => {
    installAgenticoMock();
    render(<WorkspaceShell />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole('button', { name: 'New feature' }));
    await user.click(await screen.findByRole('checkbox', { name: /repo-a/ }));
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    await user.type(screen.getByLabelText('Name'), 'Unsaved feature');
    await user.click(screen.getByRole('button', { name: 'Cancel' }));

    expect(await screen.findByRole('dialog', { name: 'Discard feature draft' })).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Keep editing' }));
    expect(screen.getByDisplayValue('Unsaved feature')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    await user.click(screen.getByRole('button', { name: 'Discard draft' }));
    expect(screen.queryByRole('form', { name: /create a feature/i })).not.toBeInTheDocument();
    const newFeature = await screen.findByRole('button', { name: 'New feature' });
    await waitFor(() => expect(newFeature).toHaveFocus());
  });

  it('keeps the sheet open and the draft intact while navigation acts on the pane beneath', async () => {
    installAgenticoMock({
      settings: settingsWithActive(null),
      features: [summaryOf(featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' }))],
      feature: featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' }),
    });
    render(<WorkspaceShell />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole('button', { name: 'New feature' }));
    await user.click(await screen.findByRole('checkbox', { name: /repo-a/ }));
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    await user.type(screen.getByLabelText('Name'), 'Unsaved feature');

    // A sidebar row selects the feature beneath the scrim; no discard prompt,
    // no closed sheet, no lost draft.
    await user.click(await screen.findByRole('option', { name: /Search revamp/ }));
    expect(await screen.findByRole('region', { name: 'Feature Search revamp' })).toBeVisible();
    expect(screen.queryByRole('dialog', { name: 'Discard feature draft' })).not.toBeInTheDocument();
    expect(screen.getByRole('form', { name: /create a feature/i })).toBeInTheDocument();
    expect(screen.getByDisplayValue('Unsaved feature')).toBeInTheDocument();
  });

  it('routes menu navigation beneath an open creation sheet without touching the draft', async () => {
    installAgenticoMock();
    const { rerender } = render(<WorkspaceShell />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole('button', { name: 'New feature' }));
    await user.click(await screen.findByRole('checkbox', { name: /repo-a/ }));
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    await user.type(screen.getByLabelText('Name'), 'Unsaved feature');

    // A menu route (⌘⇧A here) is dispatched by App.tsx as a routeRequest; it
    // acts on the chrome beneath the scrim and never disturbs the draft.
    rerender(<WorkspaceShell routeRequest={{ id: 7, event: { target: 'attention' } }} />);

    expect(
      await screen.findByRole('complementary', { name: 'Attention inbox' }),
    ).toBeInTheDocument();
    expect(screen.getByRole('form', { name: /create a feature/i })).toBeInTheDocument();
    expect(screen.getByDisplayValue('Unsaved feature')).toBeInTheDocument();
  });

  it('renders the Overview recovery workspace and bulk preview panel alongside the queue', async () => {
    installAgenticoMock();
    render(<WorkspaceShell />);

    expect(await screen.findByRole('region', { name: 'Existing features' })).toBeInTheDocument();
    expect(screen.getByLabelText('Recovery workspace')).toBeInTheDocument();
    expect(screen.getByLabelText('Bulk resume and retry')).toBeInTheDocument();
  });

  it('opens a feature from the typed global attention jump request', async () => {
    const onAttentionJumpHandled = vi.fn();
    installAgenticoMock({
      settings: settingsWithActive(null),
      features: [summaryOf(featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' }))],
      feature: featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' }),
    });
    render(
      <WorkspaceShell
        attentionJump={{ requestId: 1, featureId: FEATURE_ID }}
        onAttentionJumpHandled={onAttentionJumpHandled}
      />,
    );

    expect(await screen.findByRole('option', { name: /Search revamp/ })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    expect(onAttentionJumpHandled).toHaveBeenCalledTimes(1);
  });

  it('returns to Overview when ⌘1 (routed as target "home") fires over a selected feature', async () => {
    const mock = installAgenticoMock({
      settings: settingsWithActive(FEATURE_ID),
      features: [summaryOf(featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' }))],
      feature: featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' }),
    });
    const { rerender } = render(<WorkspaceShell />);

    await screen.findByRole('region', { name: 'Feature Search revamp' });

    // ⌘1 is dispatched by App.tsx as a routeRequest targeting 'home'.
    rerender(<WorkspaceShell routeRequest={{ id: 2, event: { target: 'home' } }} />);

    expect(await screen.findByRole('option', { name: 'Overview' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    expect(screen.queryByRole('region', { name: 'Feature Search revamp' })).not.toBeInTheDocument();
    await waitFor(() =>
      expect(mock.api.updateSettings).toHaveBeenCalledWith({
        shell: { setActiveFeature: { serverKey: 'default-runtime', featureId: null } },
      }),
    );
  });

  it('selecting a different sidebar row opens that row and persists it', async () => {
    const secondFeature = featureSnapshot({ id: SECOND_FEATURE_ID, name: 'Second feature' });
    const mock = installAgenticoMock({
      settings: settingsWithActive(FEATURE_ID),
      features: [
        summaryOf(featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' })),
        summaryOf(secondFeature),
      ],
    });
    mock.api.getFeature.mockImplementation((featureId: string) =>
      Promise.resolve(
        featureId === SECOND_FEATURE_ID
          ? secondFeature
          : featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' }),
      ),
    );
    render(<WorkspaceShell />);
    const user = userEvent.setup();

    await screen.findByRole('region', { name: 'Feature Search revamp' });

    await user.click(await screen.findByRole('option', { name: /Second feature/ }));
    expect(
      await screen.findByRole('region', { name: 'Feature Second feature' }),
    ).toBeInTheDocument();
    expect(screen.getByRole('option', { name: /Second feature/ })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    await waitFor(() =>
      expect(mock.api.updateSettings).toHaveBeenCalledWith({
        shell: { setActiveFeature: { serverKey: 'default-runtime', featureId: SECOND_FEATURE_ID } },
      }),
    );
  });

  it('ignores a settings route request entirely: Settings is another window now', async () => {
    const mock = installAgenticoMock({
      settings: settingsWithActive(FEATURE_ID),
      features: [summaryOf(featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' }))],
      feature: featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' }),
    });
    const { rerender } = render(<WorkspaceShell />);

    await screen.findByRole('region', { name: 'Feature Search revamp' });
    const featureRow = screen.getByRole('option', { name: /Search revamp/ });
    expect(featureRow).toHaveAttribute('aria-selected', 'true');

    rerender(<WorkspaceShell routeRequest={{ id: 1, event: { target: 'settings' } }} />);

    // Nothing in the shell reacts: no settings surface, no selection change,
    // no persisted write. App.tsx raises the Settings window instead.
    expect(
      screen.queryByRole('region', { name: 'Settings and readiness' }),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Back' })).not.toBeInTheDocument();
    expect(
      await screen.findByRole('region', { name: 'Feature Search revamp' }),
    ).toBeInTheDocument();
    expect(screen.getByRole('option', { name: /Search revamp/ })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    expect(mock.api.updateSettings).not.toHaveBeenCalled();
  });

  it('unmounts and unsubscribes a cockpit when Overview is selected', async () => {
    const mock = installAgenticoMock({
      settings: settingsWithActive(FEATURE_ID),
      features: [],
      feature: featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' }),
    });
    render(<WorkspaceShell />);
    await screen.findByRole('region', { name: 'Feature Search revamp' });
    const listenersBeforeClose = mock.appEventListenerCount();
    await userEvent.click(screen.getByRole('option', { name: 'Overview' }));
    await waitFor(() =>
      expect(
        screen.queryByRole('region', { name: 'Feature Search revamp' }),
      ).not.toBeInTheDocument(),
    );
    expect(mock.appEventListenerCount()).toBeLessThan(listenersBeforeClose);
  });
});

describe('WorkspaceShell Overview loading', () => {
  it('renders every other row when one feature detail rejects', async () => {
    const failing = featureSnapshot({
      id: FEATURE_ID,
      name: 'Oversized feature',
      status: 'Failed',
      actions: [],
    });
    const healthy = featureSnapshot({
      id: SECOND_FEATURE_ID,
      name: 'Healthy feature',
      status: 'Implementing',
      setup: { status: 'done', attempt: 1, tasks: [] },
      actions: [],
    });
    const mock = installAgenticoMock({ features: [failing, healthy].map(summaryOf) });
    mock.api.getFeature.mockImplementation((featureId: string) =>
      featureId === FEATURE_ID
        ? Promise.reject(new Error('E_PAYLOAD_TOO_LARGE: payload rejected'))
        : Promise.resolve(healthy),
    );
    render(<WorkspaceShell />);

    const lanes = await screen.findByRole('region', { name: 'Existing features' });
    // The rest of Overview is intact: both rows, both lanes, no fatal surface.
    expect(within(lanes).getByText('Healthy feature')).toBeInTheDocument();
    expect(within(lanes).getByText('Oversized feature')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Try again' })).not.toBeInTheDocument();
    // Only the unusable row is flagged.
    const failingRow = within(lanes).getByText('Oversized feature').closest('li')!;
    await waitFor(() =>
      expect(within(failingRow).getByText('Details unavailable')).toBeInTheDocument(),
    );
    const healthyRow = within(lanes).getByText('Healthy feature').closest('li')!;
    expect(within(healthyRow).queryByText('Details unavailable')).not.toBeInTheDocument();
  });

  it('keeps the fatal error surface and its retry when the list fetch fails', async () => {
    const feature = featureSnapshot({
      id: FEATURE_ID,
      name: 'Search revamp',
      status: 'Implementing',
      setup: { status: 'done', attempt: 1, tasks: [] },
      actions: [],
    });
    const mock = installAgenticoMock({ features: [summaryOf(feature)] });
    mock.api.listFeatures.mockRejectedValueOnce(
      new Error('E_PAYLOAD_TOO_LARGE: payload rejected as too large'),
    );
    render(<WorkspaceShell />);

    expect(
      await screen.findByText('E_PAYLOAD_TOO_LARGE: payload rejected as too large'),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Try again' }));

    const lanes = await screen.findByRole('region', { name: 'Existing features' });
    expect(within(lanes).getByText('Search revamp')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Try again' })).not.toBeInTheDocument();
  });

  it('fetches detail only for the rows that need it, never one per feature', async () => {
    const running = featureSnapshot({
      id: FEATURE_ID,
      name: 'Mid-flight feature',
      status: 'Implementing',
      setup: { status: 'done', attempt: 1, tasks: [] },
      actions: [],
    });
    const resting = ['aaaa1111bbbb2222', 'cccc3333dddd4444', 'eeee5555ffff6666'].map((id, index) =>
      featureSnapshot({
        id,
        name: `Finished feature ${index}`,
        status: 'Done',
        setup: { status: 'done', attempt: 1, tasks: [] },
        actions: [],
      }),
    );
    const snapshots = [running, ...resting];
    const mock = installAgenticoMock({ features: snapshots.map(summaryOf) });
    mock.api.getFeature.mockImplementation((featureId: string) =>
      Promise.resolve(snapshots.find((snapshot) => snapshot.id === featureId) ?? running),
    );
    render(<WorkspaceShell />);

    const lanes = await screen.findByRole('region', { name: 'Existing features' });
    // Every row renders from its list summary.
    expect(within(lanes).getByText('Mid-flight feature')).toBeInTheDocument();
    expect(within(lanes).getByText('Finished feature 0')).toBeInTheDocument();
    await waitFor(() => expect(mock.api.getFeature).toHaveBeenCalledWith(FEATURE_ID));
    expect(mock.api.getFeature).toHaveBeenCalledTimes(1);
  });
});

describe('WorkspaceShell toolbar', () => {
  it('toggles and persists shell.sidebarCollapsed from the sidebar-toggle button', async () => {
    const mock = installAgenticoMock({ settings: settingsWithActive(null), features: [] });
    render(<WorkspaceShell />);

    const toggle = await screen.findByRole('button', { name: 'Hide sidebar' });
    // The window-chrome cluster lives in the sidebar header while the
    // sidebar is visible, per Claude Desktop — not in the content toolbar.
    expect(toggle.closest('.sidebar__header')).not.toBeNull();
    expect(screen.getByRole('navigation', { name: 'Feature sidebar' })).toHaveAttribute(
      'data-collapsed',
      'false',
    );

    await userEvent.click(toggle);
    await waitFor(() =>
      expect(mock.api.updateSettings).toHaveBeenCalledWith({
        shell: { sidebarCollapsed: true },
      }),
    );
    // Collapsing hides the sidebar header with the sidebar, so the same
    // cluster — toggle and magnifier both — moves to the toolbar's leading
    // zone and stays clickable at the top-left, and is never rendered twice.
    const reopened = await screen.findByRole('button', { name: 'Show sidebar' });
    expect(reopened.closest('.toolbar__leading')).not.toBeNull();
    expect(screen.getAllByRole('button', { name: /sidebar/ })).toHaveLength(1);
    const collapsedSearch = screen.getByRole('button', { name: 'Search features' });
    expect(collapsedSearch.closest('.toolbar__leading')).not.toBeNull();
    expect(screen.getByRole('navigation', { name: 'Feature sidebar' })).toHaveAttribute(
      'data-collapsed',
      'true',
    );
  });

  it('opens the command palette from the sidebar header magnifier, exactly as ⌘K does', async () => {
    installAgenticoMock({ settings: settingsWithActive(null), features: [] });
    const onOpenPalette = vi.fn();
    render(<WorkspaceShell onOpenPalette={onOpenPalette} />);

    const search = await screen.findByRole('button', { name: 'Search features' });
    expect(search.closest('.sidebar__header')).not.toBeNull();
    expect(search.closest('.sidebar__chrome-controls')).not.toBeNull();
    await userEvent.click(search);
    expect(onOpenPalette).toHaveBeenCalledTimes(1);
  });

  it('keeps runtime identity and the Ask action distinct in the footer', async () => {
    installAgenticoMock({
      settings: settingsWithActive(null),
      features: [],
      connection: {
        status: 'ready',
        stage: 'ready',
        detail: 'Connected to the app-owned runtime.',
        ownership: 'app-owned',
        kind: 'local',
      },
    });
    render(<WorkspaceShell />);

    expect(
      await screen.findByRole('button', { name: 'Runtime ready — switch server' }),
    ).toBeVisible();
    expect(screen.getByRole('button', { name: 'Ask ⌥Space' })).toBeVisible();
  });

  it('marks the Ask chip with an unread dot only while a reply is unseen', async () => {
    installAgenticoMock({
      settings: settingsWithActive(null),
      features: [],
      connection: {
        status: 'ready',
        stage: 'ready',
        detail: 'Connected to the app-owned runtime.',
        ownership: 'app-owned',
        kind: 'local',
      },
    });
    const { rerender } = render(<WorkspaceShell amaUnread />);

    expect(await screen.findByRole('img', { name: 'Unread AMA reply' })).toBeVisible();

    rerender(<WorkspaceShell amaUnread={false} />);
    expect(screen.queryByRole('img', { name: 'Unread AMA reply' })).not.toBeInTheDocument();
  });

  it('shows runtime problems as passive status instead of a server picker', async () => {
    installAgenticoMock({
      settings: settingsWithActive(null),
      features: [],
      connection: {
        status: 'error',
        stage: 'connect',
        detail: 'The runtime is unreachable.',
        ownership: 'none',
        error: { code: 'E_CONNECT', message: 'The runtime is unreachable.' },
      },
    });
    render(<WorkspaceShell />);

    expect(await screen.findByText('Runtime needs attention')).toBeVisible();
    expect(screen.queryByRole('button', { name: /switch server/ })).not.toBeInTheDocument();
  });

  it('shows the named server in the footer instead of the generic ready label', async () => {
    installAgenticoMock({
      settings: settingsWithActive(null),
      features: [],
      connection: {
        status: 'ready',
        stage: 'ready',
        detail: 'Connected to an externally managed Agentico runtime.',
        ownership: 'external',
        kind: 'remote',
        serverName: 'frothy-macchiato',
      },
    });
    render(<WorkspaceShell />);

    // The footer pill is now the server control: the name rides its button.
    expect(
      await screen.findByRole('button', { name: 'frothy-macchiato — switch server' }),
    ).toBeVisible();
    const footer = document.querySelector('.sidebar__footer')!;
    expect(footer).toHaveTextContent('frothy-macchiato');
    expect(screen.queryByText('Runtime ready')).toBeNull();
    // Two controls: the server switcher and the Ask affordance.
    expect(footer.getElementsByTagName('button')).toHaveLength(2);
  });

  it('falls back to "Runtime ready" for a ready but name-less server', async () => {
    installAgenticoMock({
      settings: settingsWithActive(null),
      features: [],
      connection: {
        status: 'ready',
        stage: 'ready',
        detail: 'Connected to an externally managed Agentico runtime.',
        ownership: 'external',
        kind: 'remote',
      },
    });
    render(<WorkspaceShell />);

    expect(await screen.findByText('Runtime ready')).toBeVisible();
  });

  it('never shows the server name while connecting or needing attention', async () => {
    installAgenticoMock({
      settings: settingsWithActive(null),
      features: [],
      connection: {
        status: 'discovering',
        stage: 'discover',
        detail: 'Looking for a running Agentico runtime.',
        ownership: 'none',
        serverName: 'frothy-macchiato',
      },
    });
    render(<WorkspaceShell />);

    expect(await screen.findByText('Connecting')).toBeVisible();
    expect(screen.queryByText('frothy-macchiato')).toBeNull();
  });

  it('routes the sidebar footer AMA hint through onOpenAma', async () => {
    installAgenticoMock({ settings: settingsWithActive(null), features: [] });
    const onOpenAma = vi.fn();
    render(<WorkspaceShell onOpenAma={onOpenAma} />);

    await userEvent.click(await screen.findByRole('button', { name: 'Ask ⌥Space' }));
    expect(onOpenAma).toHaveBeenCalledTimes(1);
  });

  it('shows "Overview" with no sub-line and keeps the attention bell on the Overview surface', async () => {
    installAgenticoMock({ settings: settingsWithActive(null), features: [] });
    render(<WorkspaceShell />);

    expect(
      await screen.findByText('Overview', { selector: '.toolbar__title-name' }),
    ).toBeInTheDocument();
    expect(document.querySelector('.toolbar__title-subline')).toBeNull();
    expect(screen.getByLabelText(/Attention inbox, \d+ pending/)).toBeVisible();
  });

  it('keeps the toolbar title on Overview when a settings route arrives', async () => {
    installAgenticoMock({ settings: settingsWithActive(null), features: [] });
    render(<WorkspaceShell routeRequest={{ id: 1, event: { target: 'settings' } }} />);

    // The toolbar has no "Settings" title any more: the shell never presents
    // Settings, so the Overview title and the bell both stay put.
    expect(
      await screen.findByText('Overview', { selector: '.toolbar__title-name' }),
    ).toBeInTheDocument();
    expect(screen.queryByText('Settings', { selector: '.toolbar__title-name' })).toBeNull();
    expect(screen.getByLabelText(/Attention inbox, \d+ pending/)).toBeVisible();
  });

  it('titles the toolbar with the feature name and a repo · branch sub-line, and shows the bell', async () => {
    installAgenticoMock({
      settings: settingsWithActive(FEATURE_ID),
      features: [summaryOf(featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' }))],
      feature: featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' }),
    });
    render(<WorkspaceShell />);

    expect(
      await screen.findByText('Search revamp', { selector: '.toolbar__title-name' }),
    ).toBeInTheDocument();
    expect(screen.getByText('repo-a · feature/search-revamp')).toBeInTheDocument();
    expect(screen.getByLabelText(/Attention inbox, \d+ pending/)).toBeVisible();
  });

  it('adds a +N suffix to the sub-line when a feature spans more than one repository', async () => {
    const feature = featureSnapshot({
      id: FEATURE_ID,
      name: 'Search revamp',
      repos: ['repo-a', 'repo-b', 'repo-c'],
    });
    installAgenticoMock({
      settings: settingsWithActive(FEATURE_ID),
      features: [summaryOf(feature)],
      feature,
    });
    render(<WorkspaceShell />);

    expect(await screen.findByText('repo-a · feature/search-revamp +2')).toBeInTheDocument();
  });

  it('still exposes the cockpit ⋯ overflow menu once it is relocated into the toolbar', async () => {
    installAgenticoMock({
      settings: settingsWithActive(FEATURE_ID),
      features: [summaryOf(featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' }))],
      feature: featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' }),
    });
    render(<WorkspaceShell />);

    await screen.findByText('Search revamp', { selector: '.toolbar__title-name' });
    const summary = screen.getByLabelText('More actions');
    // The menu portals into the toolbar's overflow slot, not the cockpit.
    expect(summary.closest('.toolbar__overflow-slot')).not.toBeNull();
    await userEvent.click(summary);
    expect(within(summary.closest('details')!).getByRole('menu')).toBeInTheDocument();
  });

  it('portals the cockpit status chip into the toolbar actions slot, not the cockpit content flow', async () => {
    installAgenticoMock({
      settings: settingsWithActive(FEATURE_ID),
      features: [summaryOf(featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' }))],
      feature: featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' }),
    });
    render(<WorkspaceShell />);

    await screen.findByText('Search revamp', { selector: '.toolbar__title-name' });
    const chip = screen.getByRole('status', { name: 'Current feature status' });
    // The action row portals into the toolbar's actions slot; it must never
    // land in the cockpit's own content flow.
    expect(chip.closest('.toolbar__actions-slot')).not.toBeNull();
  });

  it('wires the toolbar inspector toggle into the wide-layout split-view pane, hides it on Overview, and resets it across a feature switch', async () => {
    const secondFeature = featureSnapshot({ id: SECOND_FEATURE_ID, name: 'Second feature' });
    const mock = installAgenticoMock({
      settings: settingsWithActive(FEATURE_ID),
      features: [
        summaryOf(featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' })),
        summaryOf(secondFeature),
      ],
    });
    mock.api.getFeature.mockImplementation((featureId: string) =>
      Promise.resolve(
        featureId === SECOND_FEATURE_ID
          ? secondFeature
          : featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' }),
      ),
    );
    render(<WorkspaceShell />);
    const user = userEvent.setup();

    await screen.findByRole('region', { name: 'Feature Search revamp' });
    const toggle = screen.getByRole('button', { name: 'Toggle inspector' });
    // The toggle is chrome-owned: it portals into the toolbar's slot, not the
    // cockpit's own markup.
    expect(toggle.closest('.toolbar__inspector-slot')).not.toBeNull();
    expect(toggle).toHaveAttribute('aria-pressed', 'false');
    expect(screen.queryByRole('heading', { name: 'Search revamp' })).not.toBeInTheDocument();

    await user.click(toggle);
    expect(await screen.findByRole('heading', { name: 'Search revamp' })).toBeVisible();
    expect(toggle).toHaveAttribute('aria-pressed', 'true');

    // Switching the selected feature unmounts and remounts the cockpit
    // (`key={featureId}`), so the session-only inspector state resets to
    // closed with no dedicated reset logic.
    await user.click(await screen.findByRole('option', { name: /Second feature/ }));
    await screen.findByRole('region', { name: 'Feature Second feature' });
    expect(screen.queryByRole('heading', { name: 'Second feature' })).not.toBeInTheDocument();
    const toggleForSecond = screen.getByRole('button', { name: 'Toggle inspector' });
    expect(toggleForSecond).toHaveAttribute('aria-pressed', 'false');

    // Absent entirely on Overview, where no feature is selected.
    await user.click(screen.getByRole('option', { name: 'Overview' }));
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: 'Toggle inspector' })).not.toBeInTheDocument(),
    );
  });
});

/** One waiting-lane feature (a lane expanded by default) plus one done-lane
 * feature (collapsed by default) — enough to exercise both "visible rows
 * only" (Arrow/Home/End) and "every row regardless of disclosure" (⌘2-9). */
function waitingAndDoneFixture() {
  const waiting = featureSnapshot({
    id: FEATURE_ID,
    name: 'Needs a decision',
    status: 'Failed',
    actions: [],
  });
  const done = featureSnapshot({
    id: SECOND_FEATURE_ID,
    name: 'Finished feature',
    status: 'Done',
    setup: { status: 'done', attempt: 1, tasks: [] },
  });
  return [waiting, done] as const;
}

function installWaitingAndDoneMock() {
  const [waiting, done] = waitingAndDoneFixture();
  const mock = installAgenticoMock({ features: [waiting, done].map(summaryOf) });
  mock.api.getFeature.mockImplementation((featureId: string) =>
    Promise.resolve([waiting, done].find((feature) => feature.id === featureId) ?? waiting),
  );
  return mock;
}

describe('WorkspaceShell keyboard shortcuts', () => {
  it('moves focus and selection together through visible rows with Arrow/Home/End, skipping the collapsed Done row', async () => {
    installWaitingAndDoneMock();
    render(<WorkspaceShell />);

    const overviewRow = await screen.findByRole('option', { name: 'Overview' });
    const waitingRow = await screen.findByRole('option', { name: /Needs a decision/ });
    // The Done lane starts collapsed; its <details> stays in the DOM (⌘2-9
    // can still reach it below) but is not `open`.
    const doneGroup = screen.getByRole('group', { name: 'Done' });
    expect(doneGroup.closest('details')).not.toHaveAttribute('open');

    overviewRow.focus();
    await userEvent.keyboard('{ArrowDown}');
    expect(waitingRow).toHaveFocus();
    expect(waitingRow).toHaveAttribute('aria-selected', 'true');
    expect(overviewRow).toHaveAttribute('aria-selected', 'false');

    // Only two visible rows exist; ArrowDown from the last one wraps to Overview.
    await userEvent.keyboard('{ArrowDown}');
    expect(overviewRow).toHaveFocus();
    expect(overviewRow).toHaveAttribute('aria-selected', 'true');

    await userEvent.keyboard('{End}');
    expect(waitingRow).toHaveFocus();
    expect(waitingRow).toHaveAttribute('aria-selected', 'true');

    await userEvent.keyboard('{Home}');
    expect(overviewRow).toHaveFocus();
    expect(overviewRow).toHaveAttribute('aria-selected', 'true');
  });

  it('selects a feature by absolute sidebar position with ⌘2-9, including one inside a collapsed lane', async () => {
    installWaitingAndDoneMock();
    render(<WorkspaceShell />);
    await screen.findByRole('option', { name: 'Overview' });

    // ⌘2 → the 1st feature in absolute order (the waiting lane sorts first).
    fireEvent.keyDown(window, { key: '2', metaKey: true });
    expect(await screen.findByRole('option', { name: /Needs a decision/ })).toHaveAttribute(
      'aria-selected',
      'true',
    );

    // ⌘3 → the 2nd feature, which sits inside the still-collapsed Done lane.
    fireEvent.keyDown(window, { key: '3', metaKey: true });
    expect(await screen.findByRole('option', { name: /Finished feature/ })).toHaveAttribute(
      'aria-selected',
      'true',
    );
  });

  it('bails on ⌘2-9 and ⌘⌃S when a text input is focused, letting the keystroke through untouched', async () => {
    const mock = installWaitingAndDoneMock();
    render(<WorkspaceShell />);
    await screen.findByRole('option', { name: 'Overview' });

    const input = document.createElement('input');
    document.body.appendChild(input);
    input.focus();
    try {
      fireEvent.keyDown(input, { key: '2', metaKey: true });
      expect(screen.getByRole('option', { name: /Needs a decision/ })).toHaveAttribute(
        'aria-selected',
        'false',
      );

      fireEvent.keyDown(input, { key: 's', metaKey: true, ctrlKey: true });
      expect(mock.api.updateSettings).not.toHaveBeenCalled();
    } finally {
      input.remove();
    }
  });

  it('toggles and persists shell.sidebarCollapsed from ⌘⌃S through the same path as the toolbar button', async () => {
    const mock = installAgenticoMock({ settings: settingsWithActive(null), features: [] });
    render(<WorkspaceShell />);
    await screen.findByRole('option', { name: 'Overview' });
    expect(screen.getByRole('navigation', { name: 'Feature sidebar' })).toHaveAttribute(
      'data-collapsed',
      'false',
    );

    fireEvent.keyDown(window, { key: 's', metaKey: true, ctrlKey: true });
    await waitFor(() =>
      expect(mock.api.updateSettings).toHaveBeenCalledWith({
        shell: { sidebarCollapsed: true },
      }),
    );
    expect(screen.getByRole('navigation', { name: 'Feature sidebar' })).toHaveAttribute(
      'data-collapsed',
      'true',
    );
  });
});

describe('WorkspaceShell auto-collapse at narrow viewports', () => {
  it('auto-collapses visually below ~700px without ever calling updateSettings, and re-expands above it', async () => {
    const mock = installAgenticoMock({ settings: settingsWithActive(null), features: [] });
    render(<WorkspaceShell />);
    await screen.findByRole('option', { name: 'Overview' });
    expect(screen.getByRole('navigation', { name: 'Feature sidebar' })).toHaveAttribute(
      'data-collapsed',
      'false',
    );
    mock.api.updateSettings.mockClear();

    matchMediaState.narrowShell = true;
    dispatchMediaChange('(max-width: 700px)', true);
    await waitFor(() =>
      expect(screen.getByRole('navigation', { name: 'Feature sidebar' })).toHaveAttribute(
        'data-collapsed',
        'true',
      ),
    );
    expect(mock.api.updateSettings).not.toHaveBeenCalled();

    matchMediaState.narrowShell = false;
    dispatchMediaChange('(max-width: 700px)', false);
    await waitFor(() =>
      expect(screen.getByRole('navigation', { name: 'Feature sidebar' })).toHaveAttribute(
        'data-collapsed',
        'false',
      ),
    );
    expect(mock.api.updateSettings).not.toHaveBeenCalled();
  });

  it('stays collapsed at any width once the user has explicitly collapsed the sidebar', async () => {
    installAgenticoMock({
      settings: {
        ...defaultSettings(),
        shell: { featureByServer: {}, sidebarCollapsed: true },
      },
      features: [],
    });
    render(<WorkspaceShell />);
    await screen.findByRole('option', { name: 'Overview' });
    expect(screen.getByRole('navigation', { name: 'Feature sidebar' })).toHaveAttribute(
      'data-collapsed',
      'true',
    );

    matchMediaState.narrowShell = false;
    dispatchMediaChange('(max-width: 700px)', false);
    expect(screen.getByRole('navigation', { name: 'Feature sidebar' })).toHaveAttribute(
      'data-collapsed',
      'true',
    );
  });
});

describe('WorkspaceShell ambient notices', () => {
  const readyUpdate: UpdateState = {
    status: 'ready',
    currentVersion: '0.1.0',
    targetVersion: '0.2.0',
    packageFormat: 'macos',
    signatureStatus: 'verified',
    message: 'A verified update is downloaded and ready to install.',
  };

  /** The toolbar and the content pane, and provably nothing between them. */
  function contentColumnChildren(): string[] {
    const column = document.querySelector('.content-column')!;
    return Array.from(column.children).map((child) => child.className.split(' ')[0]!);
  }

  it('shows the footer dot and the toolbar trigger together, and drops both on dismissal', async () => {
    installAgenticoMock({ settings: settingsWithActive(null), features: [] });
    const view = render(<WorkspaceShell updateState={readyUpdate} />);
    const user = userEvent.setup();

    await screen.findByText('Overview', { selector: '.toolbar__title-name' });
    expect(screen.getByRole('img', { name: 'Update available' })).toBeVisible();
    const trigger = screen.getByRole('button', { name: 'Show available update' });

    await user.click(trigger);
    const popover = screen.getByRole('region', { name: 'Available update' });
    expect(popover).toHaveTextContent('Agentico 0.2.0 is available');

    // The footer dot is ambient, never a control.
    const footer = document.querySelector('.sidebar__footer')!;
    expect(within(footer as HTMLElement).getAllByRole('button')).toHaveLength(1);

    view.rerender(<WorkspaceShell updateState={readyUpdate} updateDismissedVersion="0.2.0" />);
    expect(screen.queryByRole('button', { name: 'Show available update' })).not.toBeInTheDocument();
    expect(screen.queryByRole('img', { name: 'Update available' })).not.toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'Available update' })).not.toBeInTheDocument();
  });

  it('never puts a notice in the flow: the layout is identical before, after, and while open', async () => {
    installAgenticoMock({ settings: settingsWithActive(null), features: [] });
    const view = render(<WorkspaceShell updateState={null} />);
    const user = userEvent.setup();

    await screen.findByText('Overview', { selector: '.toolbar__title-name' });
    const baseline = contentColumnChildren();
    expect(baseline).toEqual(['toolbar', 'content-pane']);

    view.rerender(<WorkspaceShell updateState={readyUpdate} />);
    expect(contentColumnChildren()).toEqual(baseline);

    await user.click(screen.getByRole('button', { name: 'Show available update' }));
    expect(screen.getByRole('region', { name: 'Available update' })).toBeVisible();
    expect(contentColumnChildren()).toEqual(baseline);

    await user.click(screen.getByRole('button', { name: /Attention inbox, \d+ pending/ }));
    expect(screen.getByRole('complementary', { name: 'Attention inbox' })).toBeVisible();
    expect(contentColumnChildren()).toEqual(baseline);
  });

  it('agrees across the bell badge, the sidebar rows and lane count, and the tray count', async () => {
    const questioned = featureSnapshot({
      id: FEATURE_ID,
      name: 'Needs answers',
      status: 'Failed',
      actions: [],
    });
    const gated = featureSnapshot({
      id: SECOND_FEATURE_ID,
      name: 'Needs a gate',
      status: 'Failed',
      actions: [],
    });
    const snapshots = [questioned, gated];
    const mock = installAgenticoMock({
      settings: settingsWithActive(null),
      features: snapshots.map(summaryOf),
    });
    mock.api.getFeature.mockImplementation((featureId: string) =>
      Promise.resolve(snapshots.find((snapshot) => snapshot.id === featureId) ?? snapshots[0]!),
    );
    const attentionItems: AttentionItem[] = [
      {
        kind: 'questions',
        id: 'q-1',
        featureId: FEATURE_ID,
        waitingSince: '2026-08-05T10:00:00Z',
        questions: [{ key: 'Which?', header: 'Direction', multiSelect: false, options: [] }],
      },
      {
        kind: 'questions',
        id: 'q-2',
        featureId: FEATURE_ID,
        waitingSince: '2026-08-05T10:01:00Z',
        questions: [{ key: 'Which again?', header: 'Direction', multiSelect: false, options: [] }],
      },
      {
        kind: 'gate',
        id: 'gate-1',
        featureId: SECOND_FEATURE_ID,
        waitingSince: '2026-08-05T10:02:00Z',
        questions: [{ index: 1, prompt: 'Window?', answer: '' }],
      },
      {
        kind: 'recovery',
        id: 'recovery-scan',
        waitingSince: '2026-08-05T10:03:00Z',
        liveCount: 1,
        deadCount: 0,
      },
    ];
    const view = render(<WorkspaceShell attentionItems={attentionItems} />);

    // The bell and the tray read the same rule: recovery is excluded.
    expect(await screen.findByRole('button', { name: 'Attention inbox, 3 pending' })).toBeVisible();
    expect(actionableAttentionCount(attentionItems)).toBe(3);

    const sidebar = screen.getByRole('navigation', { name: 'Feature sidebar' });
    const waitingLane = within(sidebar).getByRole('group', { name: 'Waiting on you' });
    expect(within(waitingLane).getAllByRole('option')).toHaveLength(2);
    expect(within(sidebar).getByText('Answer 2 questions')).toBeVisible();
    expect(within(sidebar).getByText('Resolve 1 gate')).toBeVisible();

    // Everything clears together once the items resolve.
    view.rerender(<WorkspaceShell attentionItems={[]} />);
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Attention inbox, 0 pending' })).toBeVisible(),
    );
    expect(actionableAttentionCount([])).toBe(0);
    expect(within(sidebar).queryByText('Answer 2 questions')).not.toBeInTheDocument();
  });
});

/**
 * The coarse renderer→main push behind the native menu bar, plus the routes
 * the new File and View items deliver.
 */
describe('WorkspaceShell native-menu UI state', () => {
  const READY: ConnectionState = {
    status: 'ready',
    stage: 'ready',
    detail: 'Runtime ready.',
    ownership: 'app-owned',
    kind: 'local',
  };

  function lastPush(mock: ReturnType<typeof installAgenticoMock>): MainWindowUiState {
    const calls = mock.api.publishUiState.mock.calls;
    return calls[calls.length - 1]![0] as MainWindowUiState;
  }

  it('pushes an everything-disabled feature map while Overview is selected', async () => {
    const mock = installAgenticoMock({
      settings: settingsWithActive(null),
      features: [],
      connection: READY,
    });
    render(<WorkspaceShell />);
    await screen.findByRole('option', { name: 'Overview' });

    await waitFor(() => expect(mock.api.publishUiState).toHaveBeenCalled());
    const pushed = lastPush(mock);
    expect(pushed.activeFeatureId).toBeNull();
    expect(pushed.runtimeReady).toBe(true);
    expect(pushed.inspectorAvailable).toBe(false);
    expect(Object.values(pushed.featureCommands).every((enabled) => !enabled)).toBe(true);
  });

  it('pushes the selected feature and its live enabled map, and nothing on an unchanged refresh', async () => {
    const feature = featureSnapshot({
      id: FEATURE_ID,
      name: 'Search revamp',
      actions: [
        { id: 'start', enabled: true, disabledReasons: [] },
        {
          id: 'pause-stop',
          enabled: false,
          disabledReasons: [{ code: 'not_running', message: 'nothing is running' }],
        },
      ],
    });
    const mock = installAgenticoMock({
      settings: settingsWithActive(FEATURE_ID),
      features: [summaryOf(feature)],
      feature,
      connection: READY,
    });
    render(<WorkspaceShell />);
    await screen.findByRole('region', { name: 'Feature Search revamp' });

    await waitFor(() => {
      const pushed = lastPush(mock);
      expect(pushed.activeFeatureId).toBe(FEATURE_ID);
      expect(pushed.featureCommands['feature.start']).toBe(true);
    });
    const pushed = lastPush(mock);
    expect(pushed.featureCommands['feature.pause-stop']).toBe(false);
    // Configuration needs no server action, so a selection alone enables it.
    expect(pushed.featureCommands['feature.configuration']).toBe(true);
    expect(pushed.inspectorAvailable).toBe(true);
    expect(pushed.inspectorOpen).toBe(false);

    // An invalidation that re-fetches the same snapshot must not push again.
    const before = mock.api.publishUiState.mock.calls.length;
    mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: FEATURE_ID });
    await waitFor(() => expect(mock.api.getFeature).toHaveBeenCalled());
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(mock.api.publishUiState.mock.calls.length).toBe(before);
  });

  it('pushes the collapsed sidebar state the menu label flips on', async () => {
    const mock = installAgenticoMock({
      settings: settingsWithActive(null),
      features: [],
      connection: READY,
    });
    render(<WorkspaceShell />);
    await screen.findByRole('option', { name: 'Overview' });
    await waitFor(() => expect(mock.api.publishUiState).toHaveBeenCalled());
    expect(lastPush(mock).sidebarCollapsed).toBe(false);

    fireEvent.keyDown(window, { key: 's', metaKey: true, ctrlKey: true });
    await waitFor(() => expect(lastPush(mock).sidebarCollapsed).toBe(true));
  });

  it('opens the creation sheet from ⌘N, the File route, and never twice from one press', async () => {
    installAgenticoMock({ settings: settingsWithActive(null), features: [], connection: READY });
    const { rerender } = render(<WorkspaceShell />);
    await screen.findByRole('option', { name: 'Overview' });

    fireEvent.keyDown(window, { key: 'n', metaKey: true });
    expect(await screen.findByRole('form', { name: /create a feature/i })).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    await waitFor(() =>
      expect(screen.queryByRole('form', { name: /create a feature/i })).not.toBeInTheDocument(),
    );

    // The File ▸ New Feature item arrives as a routed request.
    rerender(<WorkspaceShell routeRequest={{ id: 11, event: { target: 'new-feature' } }} />);
    expect(await screen.findByRole('form', { name: /create a feature/i })).toBeInTheDocument();
  });

  it('toggles the sidebar exactly once per View ▸ Show/Hide Sidebar route', async () => {
    const mock = installAgenticoMock({
      settings: settingsWithActive(null),
      features: [],
      connection: READY,
    });
    const { rerender } = render(<WorkspaceShell />);
    await screen.findByRole('option', { name: 'Overview' });

    rerender(<WorkspaceShell routeRequest={{ id: 12, event: { target: 'toggle-sidebar' } }} />);
    await waitFor(() =>
      expect(mock.api.updateSettings).toHaveBeenCalledWith({
        shell: { sidebarCollapsed: true },
      }),
    );
    expect(screen.getByRole('navigation', { name: 'Feature sidebar' })).toHaveAttribute(
      'data-collapsed',
      'true',
    );
    // A re-render with the same request id must not toggle a second time.
    rerender(<WorkspaceShell routeRequest={{ id: 12, event: { target: 'toggle-sidebar' } }} />);
    expect(screen.getByRole('navigation', { name: 'Feature sidebar' })).toHaveAttribute(
      'data-collapsed',
      'true',
    );
  });

  it('selects the feature a palette search routed to', async () => {
    const feature = featureSnapshot({ id: FEATURE_ID, name: 'Search revamp', status: 'Created' });
    const mock = installAgenticoMock({
      settings: settingsWithActive(null),
      features: [summaryOf(feature)],
      feature,
      connection: READY,
    });
    mock.api.listSessions.mockResolvedValue([]);
    const { rerender } = render(<WorkspaceShell />);
    await screen.findByRole('option', { name: 'Overview' });

    rerender(
      <WorkspaceShell
        routeRequest={{ id: 21, event: { target: 'select-feature', featureId: FEATURE_ID } }}
      />,
    );

    expect(
      await screen.findByRole('region', { name: 'Feature Search revamp' }),
    ).toBeInTheDocument();
    expect(mock.api.updateSettings).toHaveBeenCalledWith({
      shell: { setActiveFeature: { serverKey: 'default-runtime', featureId: FEATURE_ID } },
    });
  });

  it('runs a Feature-menu route through the funnel against the live selection', async () => {
    const feature = featureSnapshot({
      id: FEATURE_ID,
      name: 'Search revamp',
      status: 'Implementing',
      actions: [{ id: 'pause-stop', enabled: true, disabledReasons: [] }],
    });
    const mock = installAgenticoMock({
      settings: settingsWithActive(FEATURE_ID),
      features: [summaryOf(feature)],
      feature,
      connection: READY,
    });
    mock.api.listSessions.mockResolvedValue([]);
    const { rerender } = render(<WorkspaceShell />);
    await screen.findByRole('region', { name: 'Feature Search revamp' });

    rerender(
      <WorkspaceShell
        routeRequest={{
          id: 13,
          event: { target: 'feature-command', command: 'feature.pause-stop' },
        }}
      />,
    );

    // The cockpit's own confirmation, not a raw dispatch.
    expect(await screen.findByRole('dialog', { name: /stop/i })).toBeInTheDocument();
    expect(mock.api.dispatchFeatureAction).not.toHaveBeenCalled();
  });

  it('ignores a Feature-menu route for a verb the live catalogue disables', async () => {
    const feature = featureSnapshot({
      id: FEATURE_ID,
      name: 'Search revamp',
      actions: [
        {
          id: 'pause-stop',
          enabled: false,
          disabledReasons: [{ code: 'not_running', message: 'nothing is running' }],
        },
      ],
    });
    const mock = installAgenticoMock({
      settings: settingsWithActive(FEATURE_ID),
      features: [summaryOf(feature)],
      feature,
      connection: READY,
    });
    const { rerender } = render(<WorkspaceShell />);
    await screen.findByRole('region', { name: 'Feature Search revamp' });

    rerender(
      <WorkspaceShell
        routeRequest={{
          id: 14,
          event: { target: 'feature-command', command: 'feature.pause-stop' },
        }}
      />,
    );
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(mock.api.dispatchFeatureAction).not.toHaveBeenCalled();
  });
});

describe('WorkspaceShell per-server scoping', () => {
  function readyAt(serverKey: string): ConnectionState {
    return {
      status: 'ready',
      stage: 'ready',
      detail: 'Connected.',
      ownership: 'external',
      kind: 'remote',
      serverKey,
      serverName: serverKey,
    };
  }

  it("restores each server's active feature across a switch A→B→A", async () => {
    const feature = featureSnapshot({ id: FEATURE_ID, name: 'Search revamp', status: 'Created' });
    const mock = installAgenticoMock({
      features: [summaryOf(feature)],
      settings: {
        ...defaultSettings(),
        shell: {
          featureByServer: { 'key-alpha': FEATURE_ID },
          sidebarCollapsed: false,
        },
      },
      connection: readyAt('key-alpha'),
    });
    render(<WorkspaceShell />);

    // alpha's recorded selection is restored.
    expect(await screen.findByRole('option', { name: /Search revamp/ })).toHaveAttribute(
      'aria-selected',
      'true',
    );

    // server B has no recorded selection: the shell lands on Overview.
    act(() => mock.emitConnection(readyAt('key-beta')));
    await waitFor(() =>
      expect(screen.getByRole('option', { name: 'Overview' })).toHaveAttribute(
        'aria-selected',
        'true',
      ),
    );
    expect(screen.getByRole('option', { name: /Search revamp/ })).toHaveAttribute(
      'aria-selected',
      'false',
    );

    // Selecting on B persists under B's key, never A's.
    await userEvent.click(screen.getByRole('option', { name: /Search revamp/ }));
    await waitFor(() =>
      expect(mock.api.updateSettings).toHaveBeenCalledWith({
        shell: { setActiveFeature: { serverKey: 'key-beta', featureId: FEATURE_ID } },
      }),
    );

    // Back on A the recorded selection is restored exactly.
    act(() => mock.emitConnection(readyAt('key-alpha')));
    await waitFor(() =>
      expect(screen.getByRole('option', { name: /Search revamp/ })).toHaveAttribute(
        'aria-selected',
        'true',
      ),
    );
  });
});

/**
 * The "Switch Server…" route (menu item and palette command) must always
 * land on a visible switcher: below the ~700px auto-collapse breakpoint the
 * sidebar footer is display:none, so the control moves into the narrow dock.
 */
describe('WorkspaceShell routed server switcher', () => {
  const READY: ConnectionState = {
    status: 'ready',
    stage: 'ready',
    detail: 'Runtime ready.',
    ownership: 'app-owned',
    kind: 'local',
    serverName: 'alpha',
  };

  function installReadySwitcherMock() {
    const mock = installAgenticoMock({
      settings: settingsWithActive(null),
      features: [],
      connection: READY,
    });
    const snapshot = {
      rows: [
        {
          kind: 'local' as const,
          serverKey: 'a'.repeat(64),
          name: 'alpha',
          runtimeDir: '/rt/alpha',
          current: true,
          health: 'healthy' as const,
        },
        {
          kind: 'local' as const,
          serverKey: 'b'.repeat(64),
          name: 'beta',
          runtimeDir: '/rt/beta',
          current: false,
          health: 'healthy' as const,
        },
      ],
    };
    mock.api.listServers.mockResolvedValue(snapshot);
    mock.api.probeServers.mockResolvedValue(snapshot);
    return mock;
  }

  it('opens a usable Servers listbox from the route while the shell is narrow', async () => {
    matchMediaState.narrowShell = true;
    const mock = installReadySwitcherMock();
    const { rerender } = render(<WorkspaceShell />);
    await screen.findByRole('option', { name: 'Overview' });
    expect(screen.getByRole('navigation', { name: 'Feature sidebar' })).toHaveAttribute(
      'data-collapsed',
      'true',
    );

    rerender(<WorkspaceShell routeRequest={{ id: 31, event: { target: 'switch-server' } }} />);

    const listbox = await screen.findByRole('listbox', { name: 'Servers' });
    // Exactly one switcher surface exists, and it lives in the visible narrow
    // dock — not the collapsed sidebar footer.
    expect(screen.getAllByRole('listbox', { name: 'Servers' })).toHaveLength(1);
    const dock = document.querySelector('.server-switcher-dock')!;
    expect(dock).not.toBeNull();
    expect(dock.contains(listbox)).toBe(true);
    expect(document.querySelector('.sidebar__footer .sidebar__server-control')).toBeNull();
    // Focus lands on the control the route is meant to focus.
    expect(document.activeElement).toBe(dock.querySelector('.sidebar__server-control'));

    // A listed server row can be picked — the switcher is fully usable here.
    await userEvent.click(await screen.findByRole('option', { name: /beta at .+ — Available/ }));
    expect(mock.api.switchConnectionServer).toHaveBeenCalledWith({
      serverKey: 'b'.repeat(64),
    });
    await waitFor(() =>
      expect(screen.queryByRole('listbox', { name: 'Servers' })).not.toBeInTheDocument(),
    );
  });

  it('keeps the routed switcher in the sidebar footer at wide widths', async () => {
    installReadySwitcherMock();
    const { rerender } = render(<WorkspaceShell />);
    await screen.findByRole('option', { name: 'Overview' });
    expect(screen.getByRole('navigation', { name: 'Feature sidebar' })).toHaveAttribute(
      'data-collapsed',
      'false',
    );

    rerender(<WorkspaceShell routeRequest={{ id: 32, event: { target: 'switch-server' } }} />);

    const listbox = await screen.findByRole('listbox', { name: 'Servers' });
    expect(document.querySelector('.server-switcher-dock')).toBeNull();
    expect(document.querySelector('.sidebar__footer')!.contains(listbox)).toBe(true);
    expect(document.activeElement).toBe(
      screen.getByRole('button', { name: 'alpha — switch server' }),
    );
    // Escape closes it, same as the direct-click open.
    await userEvent.keyboard('{Escape}');
    await waitFor(() =>
      expect(screen.queryByRole('listbox', { name: 'Servers' })).not.toBeInTheDocument(),
    );
  });

  it('does not reopen the switcher when the breakpoint is crossed after a route', async () => {
    installReadySwitcherMock();
    const { rerender } = render(<WorkspaceShell />);
    await screen.findByRole('option', { name: 'Overview' });

    rerender(<WorkspaceShell routeRequest={{ id: 33, event: { target: 'switch-server' } }} />);
    await screen.findByRole('listbox', { name: 'Servers' });
    await userEvent.keyboard('{Escape}');
    await waitFor(() =>
      expect(screen.queryByRole('listbox', { name: 'Servers' })).not.toBeInTheDocument(),
    );

    // Crossing into the narrow layout remounts the control in the dock; the
    // consumed route must not replay there.
    matchMediaState.narrowShell = true;
    dispatchMediaChange('(max-width: 700px)', true);
    await waitFor(() =>
      expect(screen.getByRole('navigation', { name: 'Feature sidebar' })).toHaveAttribute(
        'data-collapsed',
        'true',
      ),
    );
    expect(document.querySelector('.server-switcher-dock')).not.toBeNull();
    expect(screen.queryByRole('listbox', { name: 'Servers' })).not.toBeInTheDocument();
  });
});
