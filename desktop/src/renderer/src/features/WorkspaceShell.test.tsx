import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useCallback, useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { defaultSettings, type AttentionItem, type Settings } from '../../../shared/ipc';
import { featureSnapshot, installAgenticoMock } from '../test/agenticoMock';
import { WorkspaceShell } from './WorkspaceShell';

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

function WorkspaceWithAttention({ initialItems }: { initialItems: AttentionItem[] }) {
  const [attentionItems, setAttentionItems] = useState(initialItems);
  const refreshAttention = useCallback(async () => {
    const snapshot = await window.agentico.getAttention();
    setAttentionItems(snapshot.items);
    return snapshot.items;
  }, []);
  return <WorkspaceShell attentionItems={attentionItems} refreshAttention={refreshAttention} />;
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
    expect(row).toHaveTextContent('Intervention');
    expect(row).toHaveTextContent('Provider exited before completion.');
    expect(mock.api.getFeature).toHaveBeenCalledWith(FEATURE_ID);
  });

  it('opens a persistent tab after creation and stores only identity/presentation', async () => {
    const mock = installAgenticoMock();
    render(<WorkspaceShell />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'New feature' }));
    await screen.findByRole('form', { name: /create a feature/i });

    await user.type(screen.getByLabelText('Name'), 'Search revamp');
    await user.click(screen.getByRole('button', { name: 'Next: Where' }));
    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
    await user.click(screen.getByRole('button', { name: 'Next: Pipeline' }));
    await user.click(screen.getByRole('button', { name: 'Next: Review' }));
    await user.click(screen.getByRole('button', { name: 'Create feature' }));

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
      <WorkspaceShell attentionJump={FEATURE_ID} onAttentionJumpHandled={onAttentionJumpHandled} />,
    );

    expect(await screen.findByRole('tab', { name: 'Search revamp' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    expect(onAttentionJumpHandled).toHaveBeenCalledTimes(1);
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

  it('refreshes shared attention after inline cockpit resolution', async () => {
    const mock = installAgenticoMock({ settings: settingsWithTab() });
    let currentAttention: AttentionItem[] = [permissionAttention];
    mock.api.getAttention.mockImplementation(() => Promise.resolve({ items: currentAttention }));
    mock.api.answerPermission.mockImplementation(() => {
      currentAttention = [];
      return Promise.resolve({ result: 'submitted' });
    });
    render(<WorkspaceWithAttention initialItems={[permissionAttention]} />);
    const user = userEvent.setup();

    const inlineAttention = await screen.findByRole('region', { name: 'Feature attention' });
    await user.click(within(inlineAttention).getByRole('button', { name: 'Deny' }));

    expect(mock.api.answerPermission).toHaveBeenCalledWith({
      requestId: 'perm-1',
      sessionId: 'session-1',
      decision: 'deny',
    });
    expect(screen.queryByRole('region', { name: 'Feature attention' })).not.toBeInTheDocument();
  });
});
