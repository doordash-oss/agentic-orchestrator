import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { defaultSettings, type Settings } from '../../../shared/ipc';
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
    shell: { activeFeatureId: featureId, sidebarCollapsed: false },
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

  it('restores the previously active feature from shell.activeFeatureId and persists a new selection', async () => {
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
        shell: { activeFeatureId: null, sidebarCollapsed: false },
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
    await user.click(screen.getByRole('button', { name: 'Next: What' }));
    await user.type(screen.getByLabelText('Name'), 'Search revamp');
    await user.click(screen.getByRole('button', { name: 'Next: Pipeline' }));
    await user.click(screen.getByRole('button', { name: 'Next: Review' }));
    await user.click(screen.getByRole('button', { name: 'Create and start' }));

    expect(
      await screen.findByRole('region', { name: 'Feature Search revamp' }),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(mock.api.updateSettings).toHaveBeenCalledWith({
        shell: { activeFeatureId: FEATURE_ID, sidebarCollapsed: false },
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

  it('asks before discarding entered creation details when leaving via Overview', async () => {
    installAgenticoMock();
    render(<WorkspaceShell />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole('button', { name: 'New feature' }));
    await user.click(await screen.findByRole('checkbox', { name: /repo-a/ }));
    await user.click(screen.getByRole('button', { name: 'Next: What' }));
    await user.type(screen.getByLabelText('Name'), 'Unsaved feature');
    await user.click(screen.getByRole('button', { name: 'Back to Overview' }));

    expect(await screen.findByRole('dialog', { name: 'Discard feature draft' })).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Keep editing' }));
    expect(screen.getByDisplayValue('Unsaved feature')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Back to Overview' }));
    await user.click(screen.getByRole('button', { name: 'Discard draft' }));
    const newFeature = await screen.findByRole('button', { name: 'New feature' });
    await waitFor(() => expect(newFeature).toHaveFocus());
  });

  it('guards sidebar row navigation before discarding a dirty creation draft', async () => {
    installAgenticoMock({
      settings: settingsWithActive(null),
      features: [summaryOf(featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' }))],
      feature: featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' }),
    });
    render(<WorkspaceShell />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole('button', { name: 'New feature' }));
    await user.click(await screen.findByRole('checkbox', { name: /repo-a/ }));
    await user.click(screen.getByRole('button', { name: 'Next: What' }));
    await user.type(screen.getByLabelText('Name'), 'Unsaved feature');
    await user.click(await screen.findByRole('option', { name: /Search revamp/ }));

    expect(await screen.findByRole('dialog', { name: 'Discard feature draft' })).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Discard draft' }));
    expect(await screen.findByRole('region', { name: 'Feature Search revamp' })).toBeVisible();
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

  it('deselects every sidebar row while Settings is open, and restores the prior feature on a neutral close', async () => {
    const mock = installAgenticoMock({
      settings: settingsWithActive(FEATURE_ID),
      features: [summaryOf(featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' }))],
      feature: featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' }),
    });
    const { rerender } = render(<WorkspaceShell />);
    const user = userEvent.setup();

    const featureRow = await screen.findByRole('option', { name: /Search revamp/ });
    expect(featureRow).toHaveAttribute('aria-selected', 'true');

    // Settings is reached the same way ⌘, does it: a routeRequest targeting
    // 'settings', dispatched by App.tsx from the native menu accelerator.
    rerender(<WorkspaceShell routeRequest={{ id: 1, event: { target: 'settings' } }} />);
    expect(await screen.findByRole('heading', { name: 'Settings' })).toBeInTheDocument();
    // Nothing in the sidebar reads as selected while Settings is open.
    for (const option of screen.getAllByRole('option')) {
      expect(option).toHaveAttribute('aria-selected', 'false');
    }
    // The underlying persisted selection was never touched.
    expect(mock.api.updateSettings).not.toHaveBeenCalled();

    // A neutral close (the panel's own Back control) restores the feature
    // that was open before Settings — not Overview.
    await user.click(screen.getByRole('button', { name: 'Back' }));
    expect(screen.queryByRole('heading', { name: 'Settings' })).not.toBeInTheDocument();
    expect(
      await screen.findByRole('region', { name: 'Feature Search revamp' }),
    ).toBeInTheDocument();
    expect(screen.getByRole('option', { name: /Search revamp/ })).toHaveAttribute(
      'aria-selected',
      'true',
    );
  });

  it('returns to Overview when ⌘1 (routed as target "home") fires while Settings is open over a selected feature', async () => {
    const mock = installAgenticoMock({
      settings: settingsWithActive(FEATURE_ID),
      features: [summaryOf(featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' }))],
      feature: featureSnapshot({ id: FEATURE_ID, name: 'Search revamp' }),
    });
    const { rerender } = render(<WorkspaceShell />);

    await screen.findByRole('region', { name: 'Feature Search revamp' });
    rerender(<WorkspaceShell routeRequest={{ id: 1, event: { target: 'settings' } }} />);
    await screen.findByRole('heading', { name: 'Settings' });

    // ⌘1 is dispatched by App.tsx as a routeRequest targeting 'home'.
    rerender(<WorkspaceShell routeRequest={{ id: 2, event: { target: 'home' } }} />);

    expect(screen.queryByRole('heading', { name: 'Settings' })).not.toBeInTheDocument();
    expect(await screen.findByRole('option', { name: 'Overview' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    await waitFor(() =>
      expect(mock.api.updateSettings).toHaveBeenCalledWith({
        shell: { activeFeatureId: null, sidebarCollapsed: false },
      }),
    );
  });

  it('selecting a different sidebar row while Settings is open leaves Settings and opens that row', async () => {
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
    const { rerender } = render(<WorkspaceShell />);
    const user = userEvent.setup();

    await screen.findByRole('region', { name: 'Feature Search revamp' });
    rerender(<WorkspaceShell routeRequest={{ id: 1, event: { target: 'settings' } }} />);
    await screen.findByRole('heading', { name: 'Settings' });

    await user.click(await screen.findByRole('option', { name: /Second feature/ }));
    expect(screen.queryByRole('heading', { name: 'Settings' })).not.toBeInTheDocument();
    expect(
      await screen.findByRole('region', { name: 'Feature Second feature' }),
    ).toBeInTheDocument();
    expect(screen.getByRole('option', { name: /Second feature/ })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    await waitFor(() =>
      expect(mock.api.updateSettings).toHaveBeenCalledWith({
        shell: { activeFeatureId: SECOND_FEATURE_ID, sidebarCollapsed: false },
      }),
    );
  });

  it('opening Settings from Overview and closing it via Back returns to Overview', async () => {
    installAgenticoMock({ settings: settingsWithActive(null), features: [] });
    const { rerender } = render(<WorkspaceShell />);
    const user = userEvent.setup();

    await screen.findByRole('option', { name: 'Overview' });
    rerender(<WorkspaceShell routeRequest={{ id: 1, event: { target: 'settings' } }} />);
    await screen.findByRole('heading', { name: 'Settings' });
    for (const option of screen.getAllByRole('option')) {
      expect(option).toHaveAttribute('aria-selected', 'false');
    }

    await user.click(screen.getByRole('button', { name: 'Back' }));
    expect(screen.queryByRole('heading', { name: 'Settings' })).not.toBeInTheDocument();
    expect(screen.getByRole('option', { name: 'Overview' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
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

describe('WorkspaceShell toolbar', () => {
  it('toggles and persists shell.sidebarCollapsed from the sidebar-toggle button', async () => {
    const mock = installAgenticoMock({ settings: settingsWithActive(null), features: [] });
    render(<WorkspaceShell />);

    const toggle = await screen.findByRole('button', { name: 'Hide sidebar' });
    expect(screen.getByRole('navigation', { name: 'Feature sidebar' })).toHaveAttribute(
      'data-collapsed',
      'false',
    );

    await userEvent.click(toggle);
    await waitFor(() =>
      expect(mock.api.updateSettings).toHaveBeenCalledWith({
        shell: { activeFeatureId: null, sidebarCollapsed: true },
      }),
    );
    expect(await screen.findByRole('button', { name: 'Show sidebar' })).toBeInTheDocument();
    expect(screen.getByRole('navigation', { name: 'Feature sidebar' })).toHaveAttribute(
      'data-collapsed',
      'true',
    );
  });

  it('routes the sidebar footer AMA hint through onOpenAma', async () => {
    installAgenticoMock({ settings: settingsWithActive(null), features: [] });
    const onOpenAma = vi.fn();
    render(<WorkspaceShell onOpenAma={onOpenAma} />);

    await userEvent.click(await screen.findByRole('button', { name: 'Ask ⌘⇧M' }));
    expect(onOpenAma).toHaveBeenCalledTimes(1);
  });

  it('shows "Overview" with no sub-line and hides the attention bell on the Overview surface', async () => {
    installAgenticoMock({ settings: settingsWithActive(null), features: [] });
    render(<WorkspaceShell />);

    expect(
      await screen.findByText('Overview', { selector: '.toolbar__title-name' }),
    ).toBeInTheDocument();
    expect(document.querySelector('.toolbar__title-subline')).toBeNull();
    expect(screen.queryByLabelText(/Attention inbox, \d+ pending/)).not.toBeVisible();
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
    expect(screen.getByRole('menu')).toBeInTheDocument();
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
        shell: { activeFeatureId: null, sidebarCollapsed: true },
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
      settings: { ...defaultSettings(), shell: { activeFeatureId: null, sidebarCollapsed: true } },
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
