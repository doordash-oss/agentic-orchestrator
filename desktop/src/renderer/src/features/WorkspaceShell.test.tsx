import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useCallback, useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { defaultSettings, type AttentionItem, type Settings } from '../../../shared/ipc';
import { featureSnapshot, installAgenticoMock } from '../test/agenticoMock';
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

afterEach(cleanup);

const FEATURE_ID = 'abcd1234ef567890';

const permissionAttention: AttentionItem = {
  kind: 'permission',
  id: 'perm-1',
  featureId: FEATURE_ID,
  sessionId: 'session-1',
  phase: 'Implement',
  toolName: 'Bash',
  summary: 'printf attention',
  input: { command: 'printf attention' },
  waitingSince: '2026-07-15T10:00:00.000Z',
};

function settingsWithTab(): Settings {
  return {
    ...defaultSettings(),
    tabs: {
      open: [{ featureId: FEATURE_ID, titleHint: 'Search revamp' }],
      activeFeatureId: FEATURE_ID,
    },
  };
}

describe('WorkspaceShell tabs', () => {
  it('keeps Home focused on the authoritative feature list and enters creation deliberately', async () => {
    installAgenticoMock({
      features: [
        {
          id: FEATURE_ID,
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
    });
    render(<WorkspaceShell />);

    expect(await screen.findByRole('tab', { name: 'Home' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    const listRegion = await screen.findByRole('region', { name: 'Existing features' });
    expect(within(listRegion).getByText('Search revamp')).toBeInTheDocument();
    expect(screen.queryByRole('form', { name: /create a feature/i })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'New feature' }));
    expect(await screen.findByRole('form', { name: /create a feature/i })).toBeInTheDocument();
  });

  it('hydrates and renders intervention-first rows with operational detail and safe failure text', async () => {
    const mock = installAgenticoMock({
      features: [
        {
          id: FEATURE_ID,
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
    });
    mock.api.getFeature.mockResolvedValue(
      featureSnapshot({
        status: 'Failed',
        currentPhase: 'Implement',
        failure: { type: 'agent_exit', message: 'Provider exited before completion.' },
        actions: [],
      }),
    );
    render(<WorkspaceShell />);

    const listRegion = await screen.findByRole('region', { name: 'Existing features' });
    const row = await within(listRegion).findByRole('listitem');
    expect(row).toHaveTextContent('Search revamp');
    expect(row).toHaveTextContent('repo-a');
    expect(row).toHaveTextContent('Failed');
    expect(row).toHaveTextContent('Implement');
    // A failed run is a parked intervention state: amber treatment, named badge.
    expect(row).toHaveAttribute('data-state', 'attention');
    expect(row).toHaveTextContent('Provider exited before completion.');
    expect(mock.api.getFeature).toHaveBeenCalledWith(FEATURE_ID);
  });

  it('groups Home features by lifecycle state', async () => {
    const inProgress = featureSnapshot({
      id: 'feature-in-progress',
      name: 'Electron app',
      status: 'CodeReady',
      currentPhase: 'Final Review',
      createdAt: '2026-07-16T10:00:00Z',
      setup: { status: 'done', attempt: 1, tasks: [] },
    });
    const published = featureSnapshot({
      id: 'feature-published',
      name: 'Review automation',
      status: 'Published',
      currentPhase: 'Publish',
      createdAt: '2026-07-15T10:00:00Z',
      setup: { status: 'done', attempt: 1, tasks: [] },
    });
    const done = featureSnapshot({
      id: 'feature-done',
      name: 'MCP server',
      status: 'Done',
      currentPhase: 'Publish',
      createdAt: '2026-07-14T10:00:00Z',
      setup: { status: 'done', attempt: 1, tasks: [] },
    });
    const snapshots = [inProgress, published, done];
    const mock = installAgenticoMock({
      features: snapshots.map((feature) => ({
        id: feature.id,
        name: feature.name,
        status: feature.status,
        currentPhase: feature.currentPhase,
        repos: feature.repos,
        createdAt: feature.createdAt,
        activeRun: feature.activeRun,
        runCount: 1,
        warnings: [],
      })),
    });
    mock.api.getFeature.mockImplementation((featureId: string) => {
      const snapshot = snapshots.find((feature) => feature.id === featureId);
      return snapshot === undefined
        ? Promise.reject(new Error('not_found: feature not found'))
        : Promise.resolve(snapshot);
    });

    render(<WorkspaceShell />);

    const inProgressGroup = await screen.findByRole('region', { name: 'In progress' });
    const publishedGroup = screen.getByRole('region', { name: 'Published' });
    const doneGroup = screen.getByRole('region', { name: 'Done' });

    expect(within(inProgressGroup).getByText('Electron app')).toBeInTheDocument();
    expect(within(inProgressGroup).queryByText('Review automation')).not.toBeInTheDocument();
    expect(within(publishedGroup).getByText('Review automation')).toBeInTheDocument();
    expect(within(doneGroup).getByText('MCP server')).toBeInTheDocument();
  });

  it('opens a persistent tab after creation and stores only identity/presentation', async () => {
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

    const tab = await screen.findByRole('tab', { name: 'Search revamp' });
    expect(tab).toHaveAttribute('aria-selected', 'true');
    expect(mock.api.updateSettings).toHaveBeenCalledWith({
      tabs: {
        open: [{ featureId: FEATURE_ID, titleHint: 'Search revamp' }],
        activeFeatureId: FEATURE_ID,
      },
    });
    // The cockpit itself always loads from the server.
    await waitFor(() => expect(mock.api.getFeature).toHaveBeenCalledWith(FEATURE_ID));
  });

  it('asks before discarding entered creation details', async () => {
    installAgenticoMock();
    render(<WorkspaceShell />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole('tab', { name: 'Home' }));
    await user.click(await screen.findByRole('button', { name: 'New feature' }));
    await user.click(await screen.findByRole('checkbox', { name: /repo-a/ }));
    await user.click(screen.getByRole('button', { name: 'Next: What' }));
    await user.type(screen.getByLabelText('Name'), 'Unsaved feature');
    await user.click(screen.getByRole('button', { name: 'Back to Home' }));

    expect(await screen.findByRole('dialog', { name: 'Discard feature draft' })).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Keep editing' }));
    expect(screen.getByDisplayValue('Unsaved feature')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Back to Home' }));
    await user.click(screen.getByRole('button', { name: 'Discard draft' }));
    const newFeature = await screen.findByRole('button', { name: 'New feature' });
    await waitFor(() => expect(newFeature).toHaveFocus());
  });

  it('guards tab navigation before discarding a dirty creation draft', async () => {
    installAgenticoMock({ settings: settingsWithTab() });
    render(<WorkspaceShell />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole('tab', { name: 'Home' }));
    await user.click(await screen.findByRole('button', { name: 'New feature' }));
    await user.click(await screen.findByRole('checkbox', { name: /repo-a/ }));
    await user.click(screen.getByRole('button', { name: 'Next: What' }));
    await user.type(screen.getByLabelText('Name'), 'Unsaved feature');
    await user.click(screen.getByRole('tab', { name: 'Search revamp' }));

    expect(await screen.findByRole('dialog', { name: 'Discard feature draft' })).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Discard draft' }));
    expect(await screen.findByRole('heading', { name: 'Search revamp' })).toBeVisible();
  });

  it('leaves an untouched creation form silently when the server defaults to the current branch', async () => {
    const mock = installAgenticoMock();
    mock.api.getCreationDefaults.mockResolvedValue({
      repositories: [],
      defaults: { pipeline: 'medium', inquireness: 'medium', models: [], useCurrentBranch: true },
    });
    render(<WorkspaceShell />);
    const user = userEvent.setup();

    await user.click(await screen.findByRole('button', { name: 'New feature' }));
    await screen.findByRole('form', { name: /create a feature/i });
    await user.click(screen.getByRole('button', { name: 'Back to Home' }));

    expect(screen.queryByRole('dialog', { name: 'Discard feature draft' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'New feature' })).toBeVisible();
  });

  it('restores tabs from local settings on restart and refetches from the server', async () => {
    const mock = installAgenticoMock({ settings: settingsWithTab() });
    render(<WorkspaceShell />);

    expect(await screen.findByRole('tab', { name: 'Search revamp' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    await waitFor(() => expect(mock.api.getFeature).toHaveBeenCalledWith(FEATURE_ID));
    expect(await screen.findByRole('heading', { name: 'Search revamp' })).toBeInTheDocument();
  });

  it('shows a no-longer-exists state for a restored tab whose feature vanished', async () => {
    const mock = installAgenticoMock({ settings: settingsWithTab() });
    mock.api.getFeature.mockRejectedValue(new Error('not_found: feature not found'));
    render(<WorkspaceShell />);
    const user = userEvent.setup();

    expect(
      await screen.findByText('This feature no longer exists on the server.'),
    ).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Close tab' }));
    await waitFor(() =>
      expect(screen.queryByRole('tab', { name: 'Search revamp' })).not.toBeInTheDocument(),
    );
    expect(mock.api.updateSettings).toHaveBeenLastCalledWith({
      tabs: { open: [], activeFeatureId: null },
    });
    // Back on Home without crashing.
    expect(screen.getByRole('tab', { name: 'Home' })).toHaveAttribute('aria-selected', 'true');
  });

  it('opens an existing server feature from the Home list', async () => {
    const mock = installAgenticoMock({
      features: [
        {
          id: FEATURE_ID,
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
    });
    render(<WorkspaceShell />);
    const user = userEvent.setup();
    const listRegion = await screen.findByRole('region', { name: 'Existing features' });
    await user.click(within(listRegion).getByRole('button', { name: 'Open' }));

    expect(await screen.findByRole('tab', { name: 'Search revamp' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    await waitFor(() => expect(mock.api.getFeature).toHaveBeenCalledWith(FEATURE_ID));
  });

  it('removes a deleted feature from Home after the cockpit delete succeeds', async () => {
    const mock = installAgenticoMock({
      features: [
        {
          id: FEATURE_ID,
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
      feature: featureSnapshot({
        id: FEATURE_ID,
        name: 'Search revamp',
        status: 'Created',
        actions: [{ id: 'delete', enabled: true, disabledReasons: [] }],
      }),
    });
    render(<WorkspaceShell />);
    const user = userEvent.setup();

    const listRegion = await screen.findByRole('region', { name: 'Existing features' });
    await user.click(within(listRegion).getByRole('button', { name: 'Open' }));
    expect(await screen.findByRole('tab', { name: 'Search revamp' })).toHaveAttribute(
      'aria-selected',
      'true',
    );

    mock.api.listFeatures.mockResolvedValueOnce([]);
    await user.click(await screen.findByLabelText('More actions'));
    await user.click(screen.getByRole('menuitem', { name: 'Delete feature' }));
    const dialog = await screen.findByRole('dialog', { name: /Delete Search revamp/ });
    await user.click(within(dialog).getByRole('button', { name: 'Delete feature' }));

    await waitFor(() =>
      expect(mock.api.dispatchFeatureAction).toHaveBeenCalledWith({
        featureId: FEATURE_ID,
        action: 'delete',
        body: {},
      }),
    );
    expect(await screen.findByRole('tab', { name: 'Home' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    await waitFor(() => expect(mock.api.listFeatures.mock.calls.length).toBeGreaterThan(1));
    expect(
      within(await screen.findByRole('region', { name: 'Existing features' })).queryByText(
        'Search revamp',
      ),
    ).toBeNull();
  });

  it('refetches the feature list on feature invalidations and resync', async () => {
    const mock = installAgenticoMock();
    render(<WorkspaceShell />);
    await screen.findByRole('region', { name: 'Existing features' });
    const base = mock.api.listFeatures.mock.calls.length;

    mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: FEATURE_ID });
    await waitFor(() => expect(mock.api.listFeatures.mock.calls.length).toBe(base + 1));

    mock.emitAppEvent({ type: 'invalidated', kind: 'resync' });
    await waitFor(() => expect(mock.api.listFeatures.mock.calls.length).toBe(base + 2));

    mock.emitAppEvent({ type: 'invalidated', kind: 'session.updated' });
    expect(mock.api.listFeatures.mock.calls.length).toBe(base + 2);
  });

  it('refetches the feature list when returning Home without an invalidation', async () => {
    const published = featureSnapshot({
      id: FEATURE_ID,
      name: 'Search revamp',
      status: 'Published',
      currentPhase: 'Publish',
      setup: { status: 'done', attempt: 1, tasks: [] },
    });
    const done = {
      ...published,
      status: 'Done' as const,
    };
    let current = published;
    const mock = installAgenticoMock({
      settings: settingsWithTab(),
      features: [
        {
          id: FEATURE_ID,
          name: 'Search revamp',
          status: 'Published',
          currentPhase: 'Publish',
          repos: ['repo-a'],
          createdAt: '2026-07-14T10:00:00Z',
          activeRun: 1,
          runCount: 1,
          warnings: [],
        },
      ],
    });
    mock.api.getFeature.mockImplementation(() => Promise.resolve(current));
    render(<WorkspaceShell />);

    await screen.findByRole('heading', { name: 'Search revamp' });
    await waitFor(() => expect(mock.api.listFeatures).toHaveBeenCalledTimes(1));
    current = done;

    await userEvent.click(screen.getByRole('tab', { name: 'Home' }));

    const doneGroup = await screen.findByRole('region', { name: 'Done' });
    expect(within(doneGroup).getByText('Search revamp')).toBeInTheDocument();
    expect(mock.api.listFeatures).toHaveBeenCalledTimes(2);
  });

  it('supports keyboard navigation across the tab strip', async () => {
    installAgenticoMock({ settings: settingsWithTab() });
    render(<WorkspaceShell />);
    const user = userEvent.setup();

    const home = await screen.findByRole('tab', { name: 'Home' });
    const settingsTab = screen.getByRole('tab', { name: 'Settings' });
    const featureTab = screen.getByRole('tab', { name: 'Search revamp' });
    featureTab.focus();
    await user.keyboard('{ArrowLeft}');
    expect(settingsTab).toHaveFocus();
    await user.keyboard('{ArrowLeft}');
    expect(home).toHaveFocus();
    await user.keyboard('{ArrowRight}');
    expect(settingsTab).toHaveFocus();
    await user.keyboard('{ArrowRight}');
    expect(featureTab).toHaveFocus();
  });

  it('opens a feature from the typed global attention jump request', async () => {
    const onAttentionJumpHandled = vi.fn();
    installAgenticoMock();
    render(
      <WorkspaceShell
        attentionJump={{ requestId: 1, featureId: FEATURE_ID }}
        onAttentionJumpHandled={onAttentionJumpHandled}
      />,
    );

    expect(await screen.findByRole('tab', { name: 'Search revamp' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    expect(onAttentionJumpHandled).toHaveBeenCalledTimes(1);
  });

  it('returns an open review feature from Live activity to its Document surface', async () => {
    const mock = installAgenticoMock({
      settings: settingsWithTab(),
      feature: featureSnapshot({
        status: 'ResearchNeedsReview',
        currentPhase: 'Research',
        setup: { status: 'done', attempt: 1, tasks: [] },
        actions: [],
      }),
    });
    vi.mocked(mock.api.openReview).mockResolvedValue({
      featureId: FEATURE_ID,
      reviewId: 'phase-research',
      reviewMode: 'phase_plan',
      targetPhase: 'Research',
      runNumber: 1,
      artifactId: 'research.md',
      text: '# Research',
      draftRevision: 'r1',
      sourceRevision: 's1',
      canIterate: false,
    });
    vi.mocked(mock.api.validateReview).mockResolvedValue({
      applicable: true,
      valid: true,
      revision: 'r1',
      findings: [],
    });

    function ReviewJumpHarness() {
      const [attentionJump, setAttentionJump] = useState<{
        requestId: number;
        featureId: string;
      } | null>(null);
      return (
        <>
          <button
            type="button"
            onClick={() => setAttentionJump({ requestId: 1, featureId: FEATURE_ID })}
          >
            Jump to review
          </button>
          <WorkspaceShell attentionJump={attentionJump} />
        </>
      );
    }

    render(<ReviewJumpHarness />);
    const user = userEvent.setup();
    const stageTabs = await screen.findByRole('tablist', { name: 'Stage view' });
    const documentTab = within(stageTabs).getByRole('tab', { name: 'Document' });
    const liveTab = within(stageTabs).getByRole('tab', { name: /Live activity/ });

    await user.click(liveTab);
    expect(liveTab).toHaveAttribute('aria-selected', 'true');

    await user.click(screen.getByRole('button', { name: 'Jump to review' }));
    await waitFor(() => expect(documentTab).toHaveAttribute('aria-selected', 'true'));
  });

  it('shows matching attention badges on open tabs and dashboard rows', async () => {
    installAgenticoMock({
      settings: {
        ...defaultSettings(),
        tabs: {
          open: [{ featureId: FEATURE_ID, titleHint: 'Search revamp' }],
          activeFeatureId: null,
        },
      },
      features: [
        {
          id: FEATURE_ID,
          name: 'Search revamp',
          status: 'Implementing',
          currentPhase: 'Implement',
          repos: ['repo-a'],
          createdAt: '2026-07-14T10:00:00Z',
          activeRun: 1,
          runCount: 1,
          warnings: [],
        },
      ],
      feature: featureSnapshot({
        status: 'Implementing',
        currentPhase: 'Implement',
        setup: { status: 'done', attempt: 1, tasks: [] },
        actions: [],
      }),
      attention: { items: [permissionAttention] },
    });
    render(<WorkspaceShell attentionItems={[permissionAttention]} />);

    await screen.findByRole('region', { name: 'Existing features' });

    expect(
      screen.getAllByRole('status', { name: 'Blocking input for Search revamp: 1 pending' }),
    ).toHaveLength(2);
  });

  it('opens routed attention in the expanded conversation and refreshes after resolution', async () => {
    const mock = installAgenticoMock({
      settings: settingsWithTab(),
      feature: featureSnapshot({
        status: 'Implementing',
        currentPhase: 'Implement',
        setup: { status: 'done', attempt: 1, tasks: [] },
        actions: [],
      }),
    });
    let currentAttention: AttentionItem[] = [permissionAttention];
    mock.api.getAttention.mockImplementation(() => Promise.resolve({ items: currentAttention }));
    mock.api.answerPermission.mockImplementation(() => {
      currentAttention = [];
      return Promise.resolve({ result: 'submitted' });
    });
    function RoutedWorkspace() {
      const [attentionItems, setAttentionItems] = useState<AttentionItem[]>([permissionAttention]);
      const refreshAttention = useCallback(async () => {
        const snapshot = await window.agentico.getAttention();
        setAttentionItems(snapshot.items);
        return snapshot.items;
      }, []);
      return (
        <WorkspaceShell
          attentionItems={attentionItems}
          refreshAttention={refreshAttention}
          attentionJump={{ requestId: 7, featureId: FEATURE_ID, attentionId: 'perm-1' }}
        />
      );
    }
    render(<RoutedWorkspace />);
    const user = userEvent.setup();

    const preview = await screen.findByRole('dialog', { name: 'Live agent preview' });
    expect(within(preview).getByRole('region', { name: 'Live agent transcript' })).toBeVisible();
    const request = within(preview).getByRole('region', { name: 'Agent request' });
    await user.click(within(request).getByRole('button', { name: 'Deny' }));

    expect(mock.api.answerPermission).toHaveBeenCalledWith({
      requestId: 'perm-1',
      sessionId: 'session-1',
      decision: 'deny',
    });
    expect(
      within(preview).queryByRole('region', { name: 'Agent request' }),
    ).not.toBeInTheDocument();
  });
});
