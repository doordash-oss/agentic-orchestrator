import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AttentionItem, FeatureSnapshot } from '../../../shared/ipc';
import { featureSnapshot, featureConfigSnapshot, installAgenticoMock } from '../test/agenticoMock';
import { dispatchMediaChange, matchMediaState } from '../test/setup';
import { emptyAttentionDrafts } from './AttentionInbox';
import { FeatureCockpit } from './FeatureCockpit';

// The review surface (Document stage) instantiates Monaco, which needs no real
// editor in jsdom; the stub keeps the stage-tab test light.
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

const gateAttention: AttentionItem = {
  kind: 'gate',
  id: `${FEATURE_ID}::`,
  featureId: FEATURE_ID,
  waitingSince: '2026-07-15T10:00:00.000Z',
  summary: 'Choose the deployment window before implementation continues.',
  iteration: 6,
  questions: [
    {
      index: 1,
      prompt: 'Which deployment window should implementation use?',
      answer: '',
    },
  ],
};

const helpAttention: AttentionItem = {
  kind: 'help',
  id: `${FEATURE_ID}:help-session`,
  featureId: FEATURE_ID,
  sessionId: 'help-session',
  waitingSince: '2026-07-15T10:00:00.000Z',
  prompt: 'Feature waiting for implementation guidance.',
};

function renderCockpit(mock = installAgenticoMock()) {
  const onClose = vi.fn();
  const onLoadedName = vi.fn();
  render(
    <FeatureCockpit
      featureId={FEATURE_ID}
      titleHint="Search revamp"
      onClose={onClose}
      onLoadedName={onLoadedName}
      attentionItems={[]}
      refreshAttention={() => Promise.resolve([])}
      attentionDrafts={emptyAttentionDrafts()}
      setAttentionDrafts={vi.fn()}
    />,
  );
  return { mock, onClose, onLoadedName };
}

describe('FeatureCockpit snapshot rendering', () => {
  it('always reloads the feature from the server and reports its name', async () => {
    const { mock, onLoadedName } = renderCockpit();
    await screen.findByRole('heading', { name: 'Search revamp' });
    expect(mock.api.getFeature).toHaveBeenCalledWith(FEATURE_ID);
    expect(onLoadedName).toHaveBeenCalledWith('Search revamp');
  });

  it('omits durable setup from the inspector', async () => {
    renderCockpit();
    await screen.findByRole('heading', { name: 'Search revamp' });
    expect(screen.queryByRole('region', { name: 'Durable setup' })).not.toBeInTheDocument();
  });

  it('renders only status and branch in the mono header facts', async () => {
    renderCockpit();
    await screen.findByRole('heading', { name: 'Search revamp' });
    const header = screen.getByText('Status').closest('dl');
    expect(header).not.toBeNull();
    expect(within(header!).getByLabelText('SettingUpWorktrees')).toBeInTheDocument();
    expect(within(header!).getByText('feature/search-revamp')).toBeInTheDocument();
    expect(within(header!).queryByText('Automatic review')).not.toBeInTheDocument();
    expect(within(header!).queryByText('Repository')).not.toBeInTheDocument();
  });

  it('shows the feature pipeline ladder with Setup active during setup', async () => {
    renderCockpit();
    await screen.findByRole('heading', { name: 'Search revamp' });
    const ladder = screen.getByRole('group', { name: 'Feature pipeline' });
    const active = within(ladder)
      .getAllByRole('listitem')
      .find((item) => item.getAttribute('aria-current') === 'step');
    expect(active).toHaveTextContent('Setup');
    expect(document.querySelector('.phase-spine')).not.toBeInTheDocument();
  });

  it('keeps rewind and run history in the overflow menu', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        actions: [{ id: 'rewind', enabled: true, disabledReasons: [] }],
      }),
    });
    render(
      <FeatureCockpit
        featureId={FEATURE_ID}
        titleHint="Search revamp"
        onClose={vi.fn()}
        onLoadedName={vi.fn()}
        attentionItems={[]}
        refreshAttention={() => Promise.resolve([])}
        attentionDrafts={emptyAttentionDrafts()}
        setAttentionDrafts={vi.fn()}
        onSelectRun={vi.fn()}
      />,
    );

    const actions = await screen.findByRole('group', { name: 'Feature actions' });
    await userEvent.click(within(actions).getByLabelText('More actions'));
    expect(within(actions).getByRole('menuitem', { name: 'Rewind feature' })).toBeVisible();
    expect(within(actions).getByRole('menuitem', { name: 'View run history' })).toBeVisible();
    await userEvent.click(within(actions).getByRole('menuitem', { name: 'Rewind feature' }));
    expect(await screen.findByRole('dialog', { name: /Rewind/ })).toBeVisible();
    expect(mock.api.getFeature).toHaveBeenCalledWith(FEATURE_ID);
  });

  it('closes the overflow menu on an outside pointer so it never lingers over drawers', async () => {
    installAgenticoMock({
      feature: featureSnapshot({
        actions: [{ id: 'rewind', enabled: true, disabledReasons: [] }],
      }),
    });
    render(
      <FeatureCockpit
        featureId={FEATURE_ID}
        titleHint="Search revamp"
        onClose={vi.fn()}
        onLoadedName={vi.fn()}
        attentionItems={[]}
        refreshAttention={() => Promise.resolve([])}
        attentionDrafts={emptyAttentionDrafts()}
        setAttentionDrafts={vi.fn()}
        onSelectRun={vi.fn()}
      />,
    );

    const actions = await screen.findByRole('group', { name: 'Feature actions' });
    const details = within(actions).getByLabelText('More actions').closest('details')!;
    await userEvent.click(within(actions).getByLabelText('More actions'));
    expect(details.open).toBe(true);

    await userEvent.click(document.body);
    expect(details.open).toBe(false);
  });

  it('opens the publish modal from a server-advertised completion verb', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'CodeReady',
        actions: [{ id: 'publish', enabled: true, disabledReasons: [] }],
      }),
    });
    mock.api.preflightCompletion.mockResolvedValue({
      featureId: FEATURE_ID,
      sourceRevision: 'rev-complete',
      canMarkDone: true,
      repos: [{ repo: 'repo-a', publishable: true, touched: true, status: 'eligible' }],
    });
    renderCockpit(mock);
    const user = userEvent.setup();

    await user.click(await screen.findByRole('button', { name: 'Publish' }));

    expect(
      await screen.findByRole('dialog', { name: 'Publish reviewed changes' }),
    ).toBeInTheDocument();
    expect(mock.api.preflightCompletion).toHaveBeenCalledWith({ featureId: FEATURE_ID });
  });

  it('hides completion affordances while completion actions are present but disabled', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Running',
        actions: [
          { id: 'publish', enabled: false, disabledReasons: [{ code: 'run_active', message: '' }] },
          { id: 'merge', enabled: false, disabledReasons: [{ code: 'run_active', message: '' }] },
          {
            id: 'mark-done',
            enabled: false,
            disabledReasons: [{ code: 'run_active', message: '' }],
          },
          { id: 'cleanup', enabled: false, disabledReasons: [{ code: 'run_active', message: '' }] },
        ],
      }),
    });
    renderCockpit(mock);

    await screen.findByRole('heading', { name: 'Search revamp' });

    expect(screen.queryByRole('tab', { name: 'Changes' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Publish' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Merge' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Clean up' })).not.toBeInTheDocument();
    expect(mock.api.preflightCompletion).not.toHaveBeenCalled();
  });

  it('opens configuration from the overflow menu', async () => {
    renderCockpit();
    const user = userEvent.setup();
    await screen.findByRole('heading', { name: 'Search revamp' });
    await user.click(screen.getByLabelText('More actions'));
    await user.click(screen.getByRole('menuitem', { name: 'Edit configuration…' }));
    expect(
      await screen.findByRole('dialog', { name: 'Feature configuration' }),
    ).toBeInTheDocument();
  });

  it('offers Document and Live activity stage tabs during a pending review', async () => {
    const mock = installAgenticoMock({
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
    renderCockpit(mock);
    const user = userEvent.setup();

    const tablist = await screen.findByRole('tablist', { name: 'Stage view' });
    const documentTab = within(tablist).getByRole('tab', { name: 'Document' });
    const liveTab = within(tablist).getByRole('tab', { name: /Live activity/ });
    expect(within(tablist).queryByRole('tab', { name: 'Aftercare' })).not.toBeInTheDocument();
    expect(documentTab).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('region', { name: 'Review editor' })).toBeInTheDocument();

    await user.click(liveTab);
    expect(liveTab).toHaveAttribute('aria-selected', 'true');
    expect(
      await screen.findByRole('region', { name: 'Current run inspection' }),
    ).toBeInTheDocument();
  });

  it.each([
    ['CodeReady', 'Implementation complete.'],
    ['Published', 'Published. Choose what comes next.'],
    ['Done', 'Work complete.'],
  ])('defaults %s features to Aftercare while retaining Run record', async (status, heading) => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status,
        activeRun: 8,
        actions: [{ id: 'rebase', enabled: true, disabledReasons: [] }],
      }),
    });
    mock.api.getRun.mockResolvedValue({
      runNumber: 8,
      artifactCount: 5,
      timing: { totalSeconds: 14700, byPhase: {} },
      cost: { totalUsd: 95.18, byPhase: {} },
    });
    renderCockpit(mock);

    expect(await screen.findByRole('region', { name: 'Feature aftercare' })).toBeVisible();
    expect(screen.queryByRole('tablist', { name: 'Stage view' })).not.toBeInTheDocument();
    expect(screen.queryByLabelText('Feature pipeline')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Run record' })).toBeVisible();
    expect(screen.getByRole('heading', { name: heading })).toBeVisible();
    await waitFor(() =>
      expect(mock.api.getRun).toHaveBeenCalledWith({ featureId: FEATURE_ID, runNumber: 8 }),
    );
  });

  it('keeps feature-scoped attention actionable from published aftercare', async () => {
    installAgenticoMock({
      feature: featureSnapshot({ status: 'Published', actions: [] }),
    });
    render(
      <FeatureCockpit
        featureId={FEATURE_ID}
        titleHint="Search revamp"
        onClose={vi.fn()}
        onLoadedName={vi.fn()}
        attentionItems={[helpAttention]}
        refreshAttention={() => Promise.resolve([helpAttention])}
        attentionDrafts={emptyAttentionDrafts()}
        setAttentionDrafts={vi.fn()}
      />,
    );

    const aftercare = await screen.findByRole('region', { name: 'Feature aftercare' });
    expect(aftercare).toBeVisible();
    const request = screen.getByRole('region', { name: 'Agent request' });
    expect(within(request).getByText(helpAttention.prompt)).toBeVisible();
    expect(within(request).getByLabelText('Help reply')).toBeVisible();
  });

  it('launches the rebase child directly from the Aftercare runway with no modal', async () => {
    const childId = 'rebase1234ef567890';
    const baseParent = featureSnapshot({
      id: FEATURE_ID,
      status: 'Published',
      actions: [{ id: 'rebase', enabled: true, disabledReasons: [] }],
    });
    const parentWithChild = featureSnapshot({
      id: FEATURE_ID,
      status: 'Published',
      actions: [],
      activeChild: {
        id: childId,
        name: 'Rebase pass',
        kind: 'rebase',
        displayToken: `rebase:${childId}`,
        displayState: 'Active — Created',
        pipeline: 'medium',
        status: 'Created',
        relationshipState: 'active',
        startedAt: '2026-07-30T10:00:00Z',
        cost: { totalUsd: 0, byPhase: {} },
        integrationState: 'pending',
        attention: [],
        cleanupWarnings: [],
      },
    });
    const child = featureSnapshot({
      id: childId,
      name: 'Rebase pass',
      status: 'Created',
      setupComplete: true,
      setup: { status: 'done', attempt: 1, tasks: [] },
      actions: [{ id: 'start', enabled: true, disabledReasons: [] }],
    });
    let currentParent = baseParent;
    const mock = installAgenticoMock({ feature: baseParent });
    mock.api.getFeature.mockImplementation((id: string) =>
      Promise.resolve(id === childId ? child : currentParent),
    );
    mock.api.launchRebaseChild.mockImplementation(async () => {
      currentParent = parentWithChild;
      return { childId, parentId: FEATURE_ID, result: 'created' };
    });
    renderCockpit(mock);
    const user = userEvent.setup();

    const aftercare = await screen.findByRole('region', { name: 'Feature aftercare' });
    // The card reads the new "Start rebase pass" label and reworded description.
    const card = within(aftercare).getByRole('button', { name: /Start rebase pass/ });
    expect(card).toBeVisible();
    expect(card).toHaveTextContent(/merges each behind repository/);
    await user.click(card);

    // Exactly one zero-input launch call; no dialog is mounted at any point.
    await waitFor(() => expect(mock.api.launchRebaseChild).toHaveBeenCalledOnce());
    expect(mock.api.launchRebaseChild).toHaveBeenCalledWith({ featureId: FEATURE_ID });
    expect(screen.queryByRole('dialog', { name: 'Rebase' })).not.toBeInTheDocument();
    expect(screen.queryByRole('dialog', { name: 'Rebase preflight' })).not.toBeInTheDocument();

    // The returned child arms auto-start; the cockpit flips into the pass workspace.
    await waitFor(() =>
      expect(mock.api.dispatchFeatureAction).toHaveBeenCalledWith({
        featureId: childId,
        action: 'start',
      }),
    );
    expect(await screen.findByRole('region', { name: 'Rebase pass' })).toBeVisible();
  });

  it('renders the inline already-up-to-date notice on the aftercare surface and keeps the card available', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        id: FEATURE_ID,
        status: 'Published',
        actions: [{ id: 'rebase', enabled: true, disabledReasons: [] }],
      }),
    });
    mock.api.launchRebaseChild.mockRejectedValue(
      new Error(
        'rebase_already_up_to_date: Every repository is already up to date with its target branch. Nothing to merge.',
      ),
    );
    renderCockpit(mock);
    const user = userEvent.setup();

    const aftercare = await screen.findByRole('region', { name: 'Feature aftercare' });
    await user.click(within(aftercare).getByRole('button', { name: /Start rebase pass/ }));

    // The typed failure renders inline near the aftercare cards with code + message.
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('rebase_already_up_to_date');
    expect(alert).toHaveTextContent(/already up to date with its target branch/);
    // No pass workspace is mounted; the cockpit stays in aftercare.
    expect(screen.queryByRole('region', { name: 'Rebase pass' })).not.toBeInTheDocument();
    // The card remains available for another attempt.
    expect(within(aftercare).getByRole('button', { name: /Start rebase pass/ })).toBeEnabled();
  });

  it('renders an inline typed failure for a target-resolution error and clears it on the next attempt', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        id: FEATURE_ID,
        status: 'Published',
        actions: [{ id: 'rebase', enabled: true, disabledReasons: [] }],
      }),
    });
    mock.api.launchRebaseChild
      .mockRejectedValueOnce(
        new Error(
          'rebase_target_resolution_failed: Could not resolve a target branch for repo-a. Check the repository default branch.',
        ),
      )
      .mockResolvedValueOnce({
        childId: 'rebase1234ef567890',
        parentId: FEATURE_ID,
        result: 'created',
      });
    renderCockpit(mock);
    const user = userEvent.setup();

    const aftercare = await screen.findByRole('region', { name: 'Feature aftercare' });
    await user.click(within(aftercare).getByRole('button', { name: /Start rebase pass/ }));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('rebase_target_resolution_failed');
    expect(alert).toHaveTextContent(/Could not resolve a target branch/);

    // A new launch attempt clears the previous error.
    await user.click(within(aftercare).getByRole('button', { name: /Start rebase pass/ }));
    await waitFor(() => expect(mock.api.launchRebaseChild).toHaveBeenCalledTimes(2));
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('gives a running maintenance cycle exclusive ownership of the stage', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Published',
        cycle: {
          type: 'rebase',
          status: 'running',
          count: 2,
          iteration: 1,
          phase: 'final_review',
        },
        actions: [{ id: 'pause-stop', enabled: true, disabledReasons: [] }],
      }),
    });
    renderCockpit(mock);

    expect(await screen.findByRole('region', { name: 'Rebase cycle' })).toBeVisible();
    expect(screen.getByRole('heading', { name: 'Live agent activity' })).toBeVisible();
    expect(screen.queryByRole('region', { name: 'Feature aftercare' })).not.toBeInTheDocument();
    expect(screen.queryByRole('tablist', { name: 'Stage view' })).not.toBeInTheDocument();
    expect(screen.queryByLabelText('Feature pipeline')).not.toBeInTheDocument();
  });

  it('keeps feature configuration available while a maintenance cycle owns the stage', async () => {
    const user = userEvent.setup();
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Published',
        cycle: {
          type: 'rebase',
          status: 'running',
          count: 4,
          iteration: 1,
          phase: 'resolve_conflicts',
        },
        actions: [{ id: 'pause-stop', enabled: true, disabledReasons: [] }],
      }),
    });
    renderCockpit(mock);

    const cycle = await screen.findByRole('region', { name: 'Rebase cycle' });
    await user.click(within(cycle).getByRole('button', { name: 'Edit configuration…' }));

    expect(await screen.findByRole('dialog', { name: 'Feature configuration' })).toBeVisible();
  });

  it('offers Publish from CodeReady without repeating the feature title in Aftercare', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        name: 'Search revamp',
        status: 'CodeReady',
        actions: [{ id: 'publish', enabled: true, disabledReasons: [] }],
      }),
    });
    mock.api.preflightCompletion.mockResolvedValue({
      featureId: FEATURE_ID,
      sourceRevision: 'publish-ready',
      canPublish: true,
      repos: [{ repo: 'repo-a', publishable: true, touched: true, status: 'eligible' }],
    });
    renderCockpit(mock);

    const aftercare = await screen.findByRole('region', { name: 'Feature aftercare' });
    expect(within(aftercare).getByRole('button', { name: /Publish this feature/ })).toBeVisible();
    expect(within(aftercare).queryByText('Search revamp')).not.toBeInTheDocument();
  });

  it('lists each enabled maintenance cycle separately on the runway', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Published',
        actions: [
          { id: 'rebase', enabled: true, disabledReasons: [] },
          { id: 'refactor', enabled: true, disabledReasons: [] },
        ],
      }),
    });
    renderCockpit(mock);
    const aftercare = await screen.findByRole('region', { name: 'Feature aftercare' });
    expect(
      within(aftercare).getByRole('button', { name: /Bring branches up to date/ }),
    ).toBeVisible();
    expect(within(aftercare).getByText('Start rebase pass')).toBeVisible();
    expect(within(aftercare).getByRole('button', { name: /Start a refactor pass/ })).toBeVisible();
  });

  it('hands the stage to the refactor pass while a child is active', async () => {
    const childId = 'child1234ef567890';
    const parent = featureSnapshot({
      id: FEATURE_ID,
      status: 'Published',
      actions: [],
      activeChild: {
        id: childId,
        name: 'Slop removal pass',
        kind: 'refactor',
        displayToken: `refactor:${childId}`,
        displayState: 'Active — Created',
        pipeline: 'large',
        status: 'Created',
        relationshipState: 'active',
        startedAt: '2026-07-30T10:00:00Z',
        cost: { totalUsd: 0, byPhase: {} },
        integrationState: 'pending',
        attention: [],
        cleanupWarnings: [],
      },
    });
    const child = featureSnapshot({
      id: childId,
      name: 'Slop removal pass',
      status: 'Created',
      setupComplete: true,
      setup: { status: 'done', attempt: 1, tasks: [] },
      actions: [
        { id: 'start', enabled: true, disabledReasons: [] },
        {
          id: 'discard',
          enabled: true,
          disabledReasons: [],
          impactPreview: {
            kind: 'child_discard',
            subject: { id: childId, name: 'Slop removal pass' },
            categories: [{ key: 'sessions', label: 'Sessions stopped', items: [] }],
            retained: ['Review configuration retained'],
          },
        },
      ],
    });
    const mock = installAgenticoMock({ feature: parent });
    mock.api.getFeature.mockImplementation((id: string) =>
      Promise.resolve(id === childId ? child : parent),
    );
    mock.api.discardRefactorChild.mockResolvedValue({
      result: 'refactor child discarded',
      status: 'completed',
    });
    renderCockpit(mock);
    const user = userEvent.setup();

    expect(await screen.findByRole('region', { name: 'Refactor pass' })).toBeVisible();
    // The contradictory "choose what comes next" hero never renders next to a live pass.
    expect(screen.queryByRole('region', { name: 'Feature aftercare' })).not.toBeInTheDocument();
    expect(screen.queryByText(/Choose what comes next/)).not.toBeInTheDocument();
    const status = screen.getByRole('status', { name: 'Current feature status' });
    expect(status).toHaveTextContent('Refactoring');

    // The pass verbs live in the action bar like any feature tab.
    const bar = screen.getByRole('group', { name: 'Feature actions' });
    await user.click(await within(bar).findByRole('button', { name: 'Start pass' }));
    expect(mock.api.dispatchFeatureAction).toHaveBeenCalledWith({
      featureId: childId,
      action: 'start',
    });

    await user.click(within(bar).getByRole('button', { name: 'Discard pass…' }));
    const dialog = await screen.findByRole('dialog', { name: /Discard Slop removal pass/ });
    await user.click(within(dialog).getByRole('button', { name: 'Discard pass' }));
    expect(mock.api.discardRefactorChild).toHaveBeenCalledWith({ childId });

    // The one config entry announces the pairing the server applies.
    await user.click(within(bar).getByLabelText('More actions'));
    await user.click(screen.getByRole('menuitem', { name: 'Edit configuration…' }));
    const config = await screen.findByRole('dialog', { name: 'Feature configuration' });
    expect(within(config).getByRole('heading', { name: 'Paired configuration' })).toBeVisible();
    expect(within(config).getByText(/Pipeline is preserved per record/)).toBeVisible();
  });

  it('opens the completed transcript from Run record', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Published',
        activeRun: 8,
        actions: [{ id: 'rebase', enabled: true, disabledReasons: [] }],
      }),
    });
    mock.api.getRun.mockResolvedValue({
      runNumber: 8,
      artifactCount: 1,
    });
    renderCockpit(mock);
    const user = userEvent.setup();

    await user.click(await screen.findByRole('button', { name: 'Run record' }));

    expect(
      await screen.findByRole('region', { name: 'Current run inspection' }),
    ).toBeInTheDocument();
    expect(screen.getByRole('dialog', { name: 'Run record' })).toHaveClass(
      'cockpit__modal--workspace',
    );
  });

  it('opens the feature pull request through the guarded desktop bridge', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Published',
        repoStatus: [
          {
            name: 'agentic-orchestrator',
            publishable: true,
            prUrl: 'https://github.com/doordash-oss/agentic-orchestrator/pull/107',
          },
        ],
      }),
    });
    mock.api.openExternal.mockResolvedValue({ ok: true });
    renderCockpit(mock);
    const user = userEvent.setup();

    await user.click(await screen.findByRole('button', { name: 'Open pull request' }));

    expect(mock.api.openExternal).toHaveBeenCalledWith({
      url: 'https://github.com/doordash-oss/agentic-orchestrator/pull/107',
    });
  });

  it('opens the regular inspector pull request through the guarded desktop bridge', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Interrupted',
        repoStatus: [
          {
            name: 'agentic-orchestrator',
            publishable: true,
            prUrl: 'https://github.com/doordash-oss/agentic-orchestrator/pull/109',
          },
        ],
      }),
    });
    mock.api.openExternal.mockResolvedValue({ ok: true });
    renderCockpit(mock);
    const user = userEvent.setup();

    await user.click(await screen.findByRole('button', { name: 'Open pull request' }));

    expect(mock.api.openExternal).toHaveBeenCalledWith({
      url: 'https://github.com/doordash-oss/agentic-orchestrator/pull/109',
    });
  });

  it('keeps a stopped rebase in its focused workspace and resumes the cycle', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Interrupted',
        currentPhase: 'Publish',
        cycle: {
          type: 'rebase',
          status: 'interrupted',
          count: 1,
          phase: 'resolve_conflicts',
        },
        actions: [{ id: 'resume', enabled: true, disabledReasons: [] }],
      }),
    });
    renderCockpit(mock);
    const user = userEvent.setup();

    const cycle = await screen.findByRole('region', { name: 'Rebase cycle' });
    expect(within(cycle).getByRole('heading', { name: 'Rebase cycle paused' })).toBeVisible();
    expect(screen.queryByRole('group', { name: 'Feature pipeline' })).not.toBeInTheDocument();

    await user.click(within(cycle).getByRole('button', { name: 'Resume cycle' }));

    expect(mock.api.dispatchFeatureAction).toHaveBeenCalledWith({
      featureId: FEATURE_ID,
      action: 'resume',
    });
  });

  it('moves focus into the inspector drawer and restores it after Escape', async () => {
    matchMediaState.narrowCockpit = true;
    renderCockpit();
    const user = userEvent.setup();
    const toggle = await screen.findByRole('button', { name: 'Inspector' });

    await user.click(toggle);
    expect(screen.getByRole('dialog', { name: 'Feature inspector' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Close inspector' })).toHaveFocus();
    await user.keyboard('{Escape}');

    expect(screen.queryByRole('dialog', { name: 'Feature inspector' })).not.toBeInTheDocument();
    await waitFor(() => expect(toggle).toHaveFocus());
    dispatchMediaChange('(max-width: 900px)', false);
  });
});

describe('FeatureCockpit failure and retry', () => {
  function failedSnapshot() {
    return featureSnapshot({
      status: 'Failed',
      failure: { type: 'worktree_setup', message: 'Setup failed in repo-a.' },
      setup: {
        status: 'failed',
        attempt: 1,
        lastError: 'clone failed',
        tasks: [
          {
            key: 'worktree:repo-a',
            kind: 'worktree',
            label: 'Create worktree',
            repo: 'repo-a',
            status: 'done',
            attempt: 1,
          },
          {
            key: 'kb:repo-a',
            kind: 'kb',
            label: 'Build knowledge base',
            repo: 'repo-a',
            status: 'failed',
            attempt: 2,
            error: 'kb build exited with status 1',
          },
        ],
      },
      actions: [
        { id: 'setup', enabled: true, disabledReasons: [] },
        {
          id: 'start',
          enabled: false,
          disabledReasons: [
            { code: 'setup_failed', message: 'setup must succeed first' },
            { code: 'stale_run', message: 'refresh the current run' },
          ],
        },
      ],
    });
  }

  it('keeps the feature failure visible without the durable setup block', async () => {
    const mock = installAgenticoMock();
    mock.api.getFeature.mockResolvedValue(failedSnapshot());
    renderCockpit(mock);
    await screen.findByRole('heading', { name: 'Search revamp' });

    expect(screen.getByText('Setup failed in repo-a.')).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'Durable setup' })).not.toBeInTheDocument();
    expect(screen.getByRole('group', { name: 'Feature pipeline' })).toHaveAttribute(
      'data-tone',
      'error',
    );
  });

  it('retries via the server-authorized setup action on the SAME feature', async () => {
    const mock = installAgenticoMock();
    mock.api.getFeature.mockResolvedValue(failedSnapshot());
    renderCockpit(mock);
    const user = userEvent.setup();
    await screen.findByRole('heading', { name: 'Search revamp' });

    const retry = screen.getByRole('button', { name: 'Retry setup' });
    expect(retry).toBeEnabled();
    const callsBefore = mock.api.getFeature.mock.calls.length;
    await user.click(retry);

    expect(mock.api.dispatchFeatureSetup).toHaveBeenCalledWith(FEATURE_ID);
    expect(mock.api.createFeature).not.toHaveBeenCalled();
    // The same feature is refreshed rather than duplicated.
    await waitFor(() => expect(mock.api.getFeature.mock.calls.length).toBeGreaterThan(callsBefore));
  });

  it('attributes the server-provided reason to Start when the action is unavailable', async () => {
    const mock = installAgenticoMock();
    mock.api.getFeature.mockResolvedValue(failedSnapshot());
    renderCockpit(mock);
    const user = userEvent.setup();
    await screen.findByRole('heading', { name: 'Search revamp' });
    // An unavailable verb lives in the overflow menu, greyed, carrying its reason.
    await user.click(screen.getByLabelText('More actions'));
    expect(screen.getByRole('menuitem', { name: 'Start' })).toBeDisabled();
    expect(screen.getByText('setup must succeed first')).toBeInTheDocument();
    expect(screen.getByText('refresh the current run')).toBeInTheDocument();
  });
});

describe('FeatureCockpit ready-to-start', () => {
  function readySnapshotDetail() {
    return featureSnapshot({
      status: 'Created',
      setup: {
        status: 'done',
        attempt: 1,
        tasks: [
          {
            key: 'worktree:repo-a',
            kind: 'worktree',
            label: 'Create worktree',
            repo: 'repo-a',
            status: 'done',
            branch: 'feature/search-revamp',
            attempt: 1,
          },
        ],
      },
      actions: [
        {
          id: 'setup',
          enabled: false,
          disabledReasons: [{ code: 'no_pending_setup', message: 'nothing to retry' }],
        },
        { id: 'start', enabled: true, disabledReasons: [] },
        { id: 'cleanup', enabled: true, disabledReasons: [] },
      ],
    });
  }

  it('offers Start only from the authoritative action catalogue', async () => {
    const mock = installAgenticoMock();
    mock.api.getFeature.mockResolvedValue(readySnapshotDetail());
    mock.api.preflightCompletion.mockResolvedValue({
      featureId: FEATURE_ID,
      sourceRevision: 'ready-revision',
      canMarkDone: false,
      repos: [{ repo: 'repo-a', publishable: false, touched: false, status: 'eligible' }],
    });
    (
      mock.api as unknown as { dispatchFeatureAction: ReturnType<typeof vi.fn> }
    ).dispatchFeatureAction = vi.fn(() => new Promise(() => {}));
    renderCockpit(mock);
    const user = userEvent.setup();
    await screen.findByRole('heading', { name: 'Search revamp' });

    await waitFor(() => expect(mock.api.preflightCompletion).toHaveBeenCalled());
    await waitFor(() => expect(screen.getByText('Ready to start')).toBeInTheDocument());
    const start = screen.getByRole('button', { name: 'Start' });
    await user.click(start);
    await user.click(start);
    expect(
      (mock.api as unknown as { dispatchFeatureAction: ReturnType<typeof vi.fn> })
        .dispatchFeatureAction,
    ).toHaveBeenCalledTimes(1);
    expect(
      (mock.api as unknown as { dispatchFeatureAction: ReturnType<typeof vi.fn> })
        .dispatchFeatureAction,
    ).toHaveBeenCalledWith({ featureId: FEATURE_ID, action: 'start' });
    expect(start).toBeDisabled();
  });

  it('refreshes after a structured Start rejection and exposes safe recovery detail', async () => {
    const mock = installAgenticoMock();
    mock.api.getFeature.mockResolvedValue(readySnapshotDetail());
    (
      mock.api as unknown as { dispatchFeatureAction: ReturnType<typeof vi.fn> }
    ).dispatchFeatureAction = vi.fn(() =>
      Promise.reject(new Error('conflict: Start is no longer available. Refresh and try again.')),
    );
    renderCockpit(mock);
    const user = userEvent.setup();
    await screen.findByRole('heading', { name: 'Search revamp' });
    const callsBefore = mock.api.getFeature.mock.calls.length;

    await user.click(screen.getByRole('button', { name: 'Start' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Start was rejected — Start is no longer available. Refresh and try again.',
    );
    await waitFor(() => expect(mock.api.getFeature.mock.calls.length).toBeGreaterThan(callsBefore));
  });

  it('observes successful Start through a refreshed snapshot instead of optimistic status', async () => {
    const mock = installAgenticoMock();
    mock.api.getFeature.mockResolvedValueOnce(readySnapshotDetail()).mockResolvedValue(
      featureSnapshot({
        status: 'Planning',
        currentPhase: 'Plan',
        setup: { status: 'done', attempt: 1, tasks: [] },
        actions: [],
      }),
    );
    (
      mock.api as unknown as { dispatchFeatureAction: ReturnType<typeof vi.fn> }
    ).dispatchFeatureAction = vi.fn(() => Promise.resolve({ result: 'started' }));
    renderCockpit(mock);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Start' });

    await user.click(screen.getByRole('button', { name: 'Start' }));

    const statusBadge = screen.getByRole('status', { name: 'Current feature status' });
    expect(await within(statusBadge).findByText('Planning')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Start' })).not.toBeInTheDocument();
  });
});

describe('FeatureCockpit convergence', () => {
  it('moves an open live surface to Aftercare when the feature comes to rest', async () => {
    const active = featureSnapshot({
      status: 'Implementing',
      currentPhase: 'Implement',
      setup: { status: 'done', attempt: 1, tasks: [] },
      actions: [
        { id: 'pause-stop', enabled: true, disabledReasons: [] },
        { id: 'cleanup', enabled: true, disabledReasons: [] },
      ],
    });
    const published = featureSnapshot({
      status: 'Published',
      currentPhase: 'Publish',
      activeRun: 8,
      setup: { status: 'done', attempt: 1, tasks: [] },
      actions: [
        { id: 'rebase', enabled: true, disabledReasons: [] },
        { id: 'cleanup', enabled: true, disabledReasons: [] },
      ],
    });
    const mock = installAgenticoMock({ feature: active });
    mock.api.getFeature.mockResolvedValueOnce(active).mockResolvedValue(published);
    mock.api.preflightCompletion.mockResolvedValue({
      featureId: FEATURE_ID,
      sourceRevision: 'rev-transition',
      canMarkDone: true,
      repos: [{ repo: 'repo-a', publishable: true, touched: true, status: 'eligible' }],
    });
    mock.api.getRun.mockResolvedValue({
      runNumber: 8,
      artifactCount: 3,
      timing: { totalSeconds: 120, byPhase: {} },
      cost: { totalUsd: 1.2, byPhase: {} },
    });
    renderCockpit(mock);
    const user = userEvent.setup();

    const liveTab = await screen.findByRole('tab', { name: 'Live activity' });
    await user.click(liveTab);
    expect(liveTab).toHaveAttribute('aria-selected', 'true');

    mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: FEATURE_ID });

    expect(await screen.findByRole('region', { name: 'Feature aftercare' })).toBeVisible();
    expect(screen.queryByRole('tablist', { name: 'Stage view' })).not.toBeInTheDocument();
    expect(
      screen.getByRole('heading', { name: 'Published. Choose what comes next.' }),
    ).toBeVisible();
  });

  it('shows a completed receipt when a running rebase returns to rest', async () => {
    const running = featureSnapshot({
      status: 'Published',
      cycle: {
        type: 'rebase',
        status: 'running',
        count: 2,
        phase: 'publish',
        startedAt: '2026-07-26T10:00:00Z',
      },
      actions: [{ id: 'pause-stop', enabled: true, disabledReasons: [] }],
    });
    const atRest = featureSnapshot({
      status: 'Published',
      actions: [{ id: 'rebase', enabled: true, disabledReasons: [] }],
    });
    const mock = installAgenticoMock({ feature: running });
    mock.api.getFeature.mockResolvedValueOnce(running).mockResolvedValue(atRest);
    renderCockpit(mock);

    await screen.findByRole('region', { name: 'Rebase cycle' });
    mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: FEATURE_ID });

    expect(await screen.findByRole('region', { name: 'Feature aftercare' })).toBeVisible();
    expect(await screen.findByText('Rebase cycle complete.')).toBeVisible();
  });

  it('keeps an interrupted rebase in cycle ownership without a completion receipt', async () => {
    const running = featureSnapshot({
      status: 'Published',
      cycle: { type: 'rebase', status: 'running', count: 1, phase: 'final_review' },
      actions: [{ id: 'pause-stop', enabled: true, disabledReasons: [] }],
    });
    const interrupted = featureSnapshot({
      status: 'Interrupted',
      cycle: { type: 'rebase', status: 'interrupted', count: 1, phase: 'final_review' },
      actions: [{ id: 'resume', enabled: true, disabledReasons: [] }],
    });
    const mock = installAgenticoMock({ feature: running });
    mock.api.getFeature.mockResolvedValueOnce(running).mockResolvedValue(interrupted);
    renderCockpit(mock);
    const user = userEvent.setup();

    await screen.findByRole('region', { name: 'Rebase cycle' });
    mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: FEATURE_ID });

    expect(await screen.findByRole('heading', { name: 'Rebase cycle paused' })).toBeVisible();
    expect(screen.queryByText('Rebase cycle complete.')).not.toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'Feature aftercare' })).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Return to Aftercare' }));
    expect(await screen.findByRole('region', { name: 'Feature aftercare' })).toBeVisible();
    expect(screen.getByText('Cycle stopped · No completion action was dispatched.')).toBeVisible();
  });

  it('turns a dismissed failed cycle into an actionable Aftercare receipt', async () => {
    const failed = featureSnapshot({
      status: 'Published',
      cycle: {
        type: 'rebase',
        status: 'failed',
        count: 4,
        phase: 'publish',
        lastError: 'Remote rejected the force push.',
      },
      actions: [{ id: 'retry', enabled: true, disabledReasons: [] }],
    });
    renderCockpit(installAgenticoMock({ feature: failed }));
    const user = userEvent.setup();

    await user.click(
      within(await screen.findByRole('region', { name: 'Rebase cycle' })).getByRole('button', {
        name: 'Return to Aftercare',
      }),
    );

    const receipt = await screen.findByRole('alert');
    expect(receipt).toHaveTextContent('Rebase cycle needs attention.');
    expect(receipt).toHaveTextContent('Remote rejected the force push.');
    expect(within(receipt).getByRole('button', { name: 'Retry cycle' })).toBeEnabled();
    expect(within(receipt).getByRole('button', { name: 'Reopen cycle' })).toBeVisible();
  });

  it('refetches on invalidations for this feature and on resync, ignoring others', async () => {
    const { mock } = renderCockpit();
    await screen.findByRole('heading', { name: 'Search revamp' });
    const base = mock.api.getFeature.mock.calls.length;

    mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: 'other-id' });
    expect(mock.api.getFeature.mock.calls.length).toBe(base);

    mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: FEATURE_ID });
    await waitFor(() => expect(mock.api.getFeature.mock.calls.length).toBe(base + 1));

    mock.emitAppEvent({ type: 'invalidated', kind: 'resync' });
    await waitFor(() => expect(mock.api.getFeature.mock.calls.length).toBe(base + 2));
  });

  it('ignores an older snapshot that resolves after a newer invalidation', async () => {
    const mock = installAgenticoMock();
    let resolveOlder!: (snapshot: ReturnType<typeof featureSnapshot>) => void;
    let resolveNewer!: (snapshot: ReturnType<typeof featureSnapshot>) => void;
    mock.api.getFeature
      .mockResolvedValueOnce(featureSnapshot())
      .mockImplementationOnce(
        () => new Promise((resolve) => (resolveOlder = resolve as typeof resolveOlder)),
      )
      .mockImplementationOnce(
        () => new Promise((resolve) => (resolveNewer = resolve as typeof resolveNewer)),
      );
    renderCockpit(mock);
    await screen.findByRole('heading', { name: 'Search revamp' });

    mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: FEATURE_ID });
    mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: FEATURE_ID });
    await waitFor(() => expect(mock.api.getFeature).toHaveBeenCalledTimes(3));
    await act(async () => resolveNewer(featureSnapshot({ name: 'Newest snapshot' })));
    expect(await screen.findByRole('heading', { name: 'Newest snapshot' })).toBeInTheDocument();
    await act(async () => resolveOlder(featureSnapshot({ name: 'Stale snapshot' })));
    expect(screen.queryByRole('heading', { name: 'Stale snapshot' })).not.toBeInTheDocument();
  });

  it('keeps a failed run transcript available for inspection', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Failed',
        setup: { status: 'done', attempt: 1, tasks: [] },
        actions: [],
      }),
      sessions: [
        {
          id: 'failed-session',
          featureId: FEATURE_ID,
          runNumber: 1,
          phase: 'Plan',
          kind: 'planner',
          status: 'failed',
          startedAt: '2026-07-15T10:00:00Z',
          taskActivities: [],
          runningTaskCount: 0,
          usage: {},
        },
      ],
      transcript: {
        sessionId: 'failed-session',
        cursor: { total: 1, start: 0, end: 1 },
        messages: [{ index: 0, role: 'system', type: 'error', text: 'Agent failed safely' }],
      },
    });
    renderCockpit(mock);

    // The failed run's raw transcript stays inspectable via the signal-trace view.
    await userEvent.click(await screen.findByRole('button', { name: 'Signal trace' }));
    expect(await screen.findByText('Agent failed safely')).toBeInTheDocument();
  });

  it('shows a refreshing indicator while the event stream is stale', async () => {
    const { mock } = renderCockpit();
    await screen.findByRole('heading', { name: 'Search revamp' });

    mock.emitAppEvent({ type: 'status', stream: 'stale' });
    expect(await screen.findByText('Refreshing from the runtime…')).toBeInTheDocument();

    mock.emitAppEvent({ type: 'status', stream: 'live' });
    await waitFor(() =>
      expect(screen.queryByText('Refreshing from the runtime…')).not.toBeInTheDocument(),
    );
  });

  it('renders a close affordance instead of crashing when the feature vanished', async () => {
    const mock = installAgenticoMock();
    mock.api.getFeature.mockRejectedValue(new Error('not_found: feature not found'));
    const { onClose } = renderCockpit(mock);
    const user = userEvent.setup();

    expect(
      await screen.findByText('This feature no longer exists on the server.'),
    ).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Close tab' }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('saves floating gate drafts while leaving the agent paused', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Implementing',
        currentPhase: 'Implement',
        setup: { status: 'done', attempt: 1, tasks: [] },
        actions: [],
      }),
    });
    mock.api.saveGateDraft.mockResolvedValue({ result: 'drafted' });
    function Harness() {
      const [drafts, setDrafts] = useState(emptyAttentionDrafts);
      return (
        <FeatureCockpit
          featureId={FEATURE_ID}
          titleHint="Search revamp"
          onClose={vi.fn()}
          onLoadedName={vi.fn()}
          attentionItems={[gateAttention]}
          refreshAttention={() => Promise.resolve([gateAttention])}
          attentionDrafts={drafts}
          setAttentionDrafts={setDrafts}
          attentionPreviewRequest={{ requestId: 1, attentionId: gateAttention.id }}
        />
      );
    }
    render(<Harness />);
    const user = userEvent.setup();

    const request = await screen.findByRole('dialog', { name: 'Agent needs your input' });
    await user.type(
      within(request).getByLabelText(/Which deployment window should implementation use/),
      'After packaged attention evidence passes.',
    );
    await user.click(within(request).getByRole('button', { name: 'Answer later' }));

    expect(mock.api.saveGateDraft).toHaveBeenCalledWith({
      featureId: FEATURE_ID,
      answers: {
        '1': 'After packaged attention evidence passes.',
      },
    });
    expect(mock.api.resolveGate).not.toHaveBeenCalled();
    await waitFor(() =>
      expect(
        screen.queryByRole('dialog', { name: 'Agent needs your input' }),
      ).not.toBeInTheDocument(),
    );
  });

  it('sends a help reply from the expanded conversation footer', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Implementing',
        currentPhase: 'Implement',
        setup: { status: 'done', attempt: 1, tasks: [] },
        actions: [],
      }),
    });
    let latest: AttentionItem[] = [helpAttention];
    mock.api.sendHelp.mockImplementation((request: unknown) => {
      latest = [];
      return Promise.resolve({ result: 'sent', request });
    });
    function Harness() {
      const [items, setItems] = useState<AttentionItem[]>([helpAttention]);
      const [drafts, setDrafts] = useState(emptyAttentionDrafts);
      return (
        <FeatureCockpit
          featureId={FEATURE_ID}
          titleHint="Search revamp"
          onClose={vi.fn()}
          onLoadedName={vi.fn()}
          attentionItems={items}
          refreshAttention={() => {
            setItems(latest);
            return Promise.resolve(latest);
          }}
          attentionDrafts={drafts}
          setAttentionDrafts={setDrafts}
          attentionPreviewRequest={{ requestId: 1, attentionId: helpAttention.id }}
        />
      );
    }
    render(<Harness />);
    const user = userEvent.setup();

    const preview = await screen.findByRole('dialog', { name: 'Live agent preview' });
    const request = within(preview).getByRole('region', { name: 'Agent request' });
    await user.type(
      within(request).getByLabelText('Help reply'),
      'Continue with the cockpit evidence path.',
    );
    await user.click(within(request).getByRole('button', { name: 'Send reply' }));

    expect(mock.api.sendHelp).toHaveBeenCalledWith({
      featureId: FEATURE_ID,
      sessionId: 'help-session',
      message: 'Continue with the cockpit evidence path.',
    });
    await waitFor(() =>
      expect(
        within(preview).queryByRole('region', { name: 'Agent request' }),
      ).not.toBeInTheDocument(),
    );
  });

  it('answers a pending feature request from the embedded live preview', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Implementing',
        currentPhase: 'Implement',
        setup: { status: 'done', attempt: 1, tasks: [] },
        actions: [],
      }),
    });
    let latest: AttentionItem[] = [helpAttention];
    mock.api.sendHelp.mockImplementation((request: unknown) => {
      latest = [];
      return Promise.resolve({ result: 'sent', request });
    });

    function Harness() {
      const [items, setItems] = useState<AttentionItem[]>([helpAttention]);
      const [drafts, setDrafts] = useState(emptyAttentionDrafts);
      return (
        <FeatureCockpit
          featureId={FEATURE_ID}
          titleHint="Search revamp"
          onClose={vi.fn()}
          onLoadedName={vi.fn()}
          attentionItems={items}
          refreshAttention={() => {
            setItems(latest);
            return Promise.resolve(latest);
          }}
          attentionDrafts={drafts}
          setAttentionDrafts={setDrafts}
        />
      );
    }

    render(<Harness />);
    const user = userEvent.setup();

    const inspection = await screen.findByRole('region', { name: 'Current run inspection' });
    const request = within(inspection).getByRole('region', { name: 'Agent request' });
    expect(screen.queryByRole('dialog', { name: 'Live agent preview' })).not.toBeInTheDocument();

    await user.type(
      within(request).getByLabelText('Help reply'),
      'Continue with the embedded evidence path.',
    );
    await user.click(within(request).getByRole('button', { name: 'Send reply' }));

    expect(mock.api.sendHelp).toHaveBeenCalledWith({
      featureId: FEATURE_ID,
      sessionId: 'help-session',
      message: 'Continue with the embedded evidence path.',
    });
    await waitFor(() =>
      expect(
        within(inspection).queryByRole('region', { name: 'Agent request' }),
      ).not.toBeInTheDocument(),
    );
  });

  it('opens a new floating modal when the next gate becomes active', async () => {
    installAgenticoMock({
      feature: featureSnapshot({
        status: 'Implementing',
        currentPhase: 'Implement',
        setup: { status: 'done', attempt: 1, tasks: [] },
        actions: [],
      }),
    });
    const nextGate: AttentionItem = {
      ...gateAttention,
      id: `${FEATURE_ID}::next`,
      summary: 'Choose the follow-up deployment window.',
      questions: [
        {
          index: 1,
          prompt: 'Which follow-up deployment window should implementation use?',
          answer: '',
        },
      ],
    };

    function Harness() {
      const [items, setItems] = useState<AttentionItem[]>([gateAttention, nextGate]);
      const [drafts, setDrafts] = useState(emptyAttentionDrafts);
      return (
        <>
          <button type="button" onClick={() => setItems([nextGate])}>
            Advance attention
          </button>
          <FeatureCockpit
            featureId={FEATURE_ID}
            titleHint="Search revamp"
            onClose={vi.fn()}
            onLoadedName={vi.fn()}
            attentionItems={items}
            refreshAttention={() => Promise.resolve(items)}
            attentionDrafts={drafts}
            setAttentionDrafts={setDrafts}
          />
        </>
      );
    }

    render(<Harness />);
    const user = userEvent.setup();

    const request = await screen.findByRole('dialog', { name: 'Agent needs your input' });
    await user.click(within(request).getByRole('button', { name: 'Answer later' }));
    expect(
      screen.queryByRole('dialog', { name: 'Agent needs your input' }),
    ).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Advance attention' }));

    expect(
      within(await screen.findByRole('dialog', { name: 'Agent needs your input' })).getByLabelText(
        /Which follow-up deployment window should implementation use/,
      ),
    ).toBeVisible();
  });

  it('skips review items when selecting an embedded feature response', async () => {
    installAgenticoMock({
      feature: featureSnapshot({
        status: 'Implementing',
        currentPhase: 'Implement',
        setup: { status: 'done', attempt: 1, tasks: [] },
        actions: [],
      }),
    });
    const reviewAttention: AttentionItem = {
      kind: 'review',
      id: `review:${FEATURE_ID}:4:plan:PhasePlanNeedsReview`,
      featureId: FEATURE_ID,
      waitingSince: '2026-07-15T10:00:00.000Z',
      reviewKind: 'Phase plan',
      phase: 'plan',
    };

    function Harness() {
      const [drafts, setDrafts] = useState(emptyAttentionDrafts);
      return (
        <FeatureCockpit
          featureId={FEATURE_ID}
          titleHint="Search revamp"
          onClose={vi.fn()}
          onLoadedName={vi.fn()}
          attentionItems={[reviewAttention, helpAttention]}
          refreshAttention={() => Promise.resolve([reviewAttention, helpAttention])}
          attentionDrafts={drafts}
          setAttentionDrafts={setDrafts}
        />
      );
    }

    render(<Harness />);

    const inspection = await screen.findByRole('region', { name: 'Current run inspection' });
    const request = within(inspection).getByRole('region', { name: 'Agent request' });
    expect(within(request).getByLabelText('Help reply')).toBeVisible();
    expect(within(request).queryByRole('button', { name: 'Open review' })).not.toBeInTheDocument();
  });
});

describe('FeatureCockpit Restart', () => {
  it('extends the budget when restarting a max-iteration failure', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Failed',
        currentPhase: 'Implement',
        failure: { type: 'max_iterations', message: 'reached maximum iteration count' },
        actions: [{ id: 'restart', enabled: true, disabledReasons: [] }],
      }),
    });
    renderCockpit(mock);
    const user = userEvent.setup();

    await user.click(await screen.findByLabelText('More actions'));
    await user.click(screen.getByRole('menuitem', { name: 'Restart' }));
    const dialog = await screen.findByRole('dialog', { name: 'Restart Search revamp?' });
    expect(dialog).toHaveTextContent('maximum-iteration restart');
    await user.click(within(dialog).getByRole('button', { name: 'Confirm restart' }));

    await waitFor(() =>
      expect(mock.api.dispatchFeatureAction).toHaveBeenCalledWith({
        featureId: FEATURE_ID,
        action: 'restart',
        body: {
          max_iterations_delta: 10,
          max_plan_iterations_delta: 2,
        },
      }),
    );
  });

  it('keeps ordinary restarts bodyless and does not claim a budget extension', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Interrupted',
        currentPhase: 'Implement',
        actions: [{ id: 'restart', enabled: true, disabledReasons: [] }],
      }),
    });
    renderCockpit(mock);
    const user = userEvent.setup();

    await user.click(await screen.findByLabelText('More actions'));
    await user.click(screen.getByRole('menuitem', { name: 'Restart' }));
    const dialog = await screen.findByRole('dialog', { name: 'Restart Search revamp?' });
    expect(dialog).not.toHaveTextContent('maximum-iteration restart');
    await user.click(within(dialog).getByRole('button', { name: 'Confirm restart' }));

    await waitFor(() =>
      expect(mock.api.dispatchFeatureAction).toHaveBeenCalledWith({
        featureId: FEATURE_ID,
        action: 'restart',
      }),
    );
  });
});

describe('FeatureCockpit Stop', () => {
  function activeSnapshot() {
    return featureSnapshot({
      status: 'Implementing',
      currentPhase: 'Implement',
      setup: { status: 'done', attempt: 1, tasks: [] },
      actions: [{ id: 'pause-stop', enabled: true, disabledReasons: [] }],
    });
  }

  it('names the feature, phase, and live sessions; Escape cancels without mutation', async () => {
    const mock = installAgenticoMock({
      feature: activeSnapshot(),
      sessions: [
        {
          id: 'session-1',
          featureId: FEATURE_ID,
          runNumber: 1,
          phase: 'Implement',
          kind: 'implementer',
          status: 'running',
          startedAt: '2026-07-15T10:00:00Z',
          taskActivities: [],
          runningTaskCount: 0,
          usage: {},
        },
        {
          id: 'sealed-session',
          featureId: FEATURE_ID,
          runNumber: 0,
          phase: 'Implement',
          kind: 'implementer',
          status: 'running',
          startedAt: '2026-07-15T11:00:00Z',
          taskActivities: [],
          runningTaskCount: 0,
          usage: {},
        },
      ],
    });
    renderCockpit(mock);
    const user = userEvent.setup();
    const stop = await screen.findByRole('button', { name: 'Stop' });
    await user.click(stop);

    const dialog = screen.getByRole('dialog', { name: 'Stop Search revamp?' });
    expect(dialog).toHaveTextContent('Implement');
    expect(dialog).toHaveTextContent('1 live session');
    const cancel = within(dialog).getByRole('button', { name: 'Keep running' });
    const confirm = within(dialog).getByRole('button', { name: 'Confirm stop' });
    expect(cancel).toHaveFocus();
    await user.keyboard('{Shift>}{Tab}{/Shift}');
    expect(confirm).toHaveFocus();
    await user.tab();
    expect(cancel).toHaveFocus();
    await user.click(cancel);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(mock.api.dispatchFeatureAction).not.toHaveBeenCalled();
    expect(stop).toHaveFocus();

    await user.click(stop);
    expect(await screen.findByRole('dialog', { name: 'Stop Search revamp?' })).toBeInTheDocument();
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(mock.api.dispatchFeatureAction).not.toHaveBeenCalled();
    expect(stop).toHaveFocus();
  });

  it('confirms exactly once and waits for authoritative terminal state', async () => {
    const mock = installAgenticoMock({ feature: activeSnapshot() });
    mock.api.getFeature
      .mockResolvedValueOnce(activeSnapshot())
      .mockResolvedValue(featureSnapshot({ status: 'Interrupted', actions: [] }));
    let resolveStop!: () => void;
    mock.api.dispatchFeatureAction.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveStop = () =>
            resolve({
              featureId: FEATURE_ID,
              action: 'pause-stop',
              result: 'stopped',
              sessionIds: [],
            });
        }),
    );
    renderCockpit(mock);
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Stop' }));
    const confirm = screen.getByRole('button', { name: 'Confirm stop' });
    await user.click(confirm);
    await user.click(confirm);
    expect(mock.api.dispatchFeatureAction).toHaveBeenCalledTimes(1);
    expect(mock.api.dispatchFeatureAction).toHaveBeenCalledWith({
      featureId: FEATURE_ID,
      action: 'pause-stop',
    });
    expect(confirm).toBeDisabled();
    resolveStop();
    const statusBadge = screen.getByRole('status', { name: 'Current feature status' });
    expect(await within(statusBadge).findByText('Interrupted')).toBeInTheDocument();
  });

  it('refreshes a rejected Stop and presents the structured safe error', async () => {
    const mock = installAgenticoMock({ feature: activeSnapshot() });
    mock.api.dispatchFeatureAction.mockRejectedValue(
      new Error('conflict: The Stop action is stale. Refresh and try again.'),
    );
    renderCockpit(mock);
    const user = userEvent.setup();
    const callsBefore = mock.api.getFeature.mock.calls.length;

    await user.click(await screen.findByRole('button', { name: 'Stop' }));
    await user.click(screen.getByRole('button', { name: 'Confirm stop' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Stop was rejected — The Stop action is stale. Refresh and try again.',
    );
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    await waitFor(() => expect(mock.api.getFeature.mock.calls.length).toBeGreaterThan(callsBefore));
  });
});

describe('FeatureCockpit cycle gates', () => {
  it('opens the floating NeedUserInput modal for a feature-level rebase gate', async () => {
    const cycleGate: AttentionItem = {
      ...gateAttention,
      id: `${FEATURE_ID}::rebase`,
      scope: 'cycle',
      cycleType: 'rebase',
      summary: 'Choose how to resolve the rebase conflict.',
    };
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Published',
        cycle: {
          type: 'rebase',
          status: 'need_user_input',
          count: 2,
          iteration: 3,
          phase: 'resolve_conflicts',
        },
        actions: [],
      }),
    });
    const drafts = emptyAttentionDrafts();
    const setDrafts = vi.fn();
    render(
      <FeatureCockpit
        featureId={FEATURE_ID}
        titleHint="Search revamp"
        onClose={vi.fn()}
        onLoadedName={vi.fn()}
        attentionItems={[cycleGate]}
        refreshAttention={() => Promise.resolve([cycleGate])}
        attentionDrafts={drafts}
        setAttentionDrafts={setDrafts}
      />,
    );

    const modal = await screen.findByRole('dialog', { name: 'Agent needs your input' });
    expect(modal).toHaveTextContent('Choose how to resolve the rebase conflict.');
    expect(mock.api.getFeature).toHaveBeenCalledWith(FEATURE_ID);
  });
});

describe('FeatureCockpit delete', () => {
  it('allows ordinary feature deletion when no relationship projection applies', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Done',
        actions: [{ id: 'delete', enabled: true, disabledReasons: [] }],
      }),
    });
    mock.api.deleteFeatureCascade.mockResolvedValue({
      featureId: FEATURE_ID,
      operationId: 'delete-ordinary',
      status: 'completed',
      diagnostics: [],
    });
    renderCockpit(mock);
    const user = userEvent.setup();

    await user.click(await screen.findByLabelText('More actions'));
    await user.click(screen.getByRole('menuitem', { name: 'Delete feature' }));
    const dialog = await screen.findByRole('dialog', { name: /Delete Search revamp/ });
    expect(dialog).toHaveTextContent(
      'This removes the feature record and any remaining worktrees.',
    );
    await user.click(within(dialog).getByRole('button', { name: 'Delete feature' }));

    await waitFor(() =>
      expect(mock.api.deleteFeatureCascade).toHaveBeenCalledWith({ featureId: FEATURE_ID }),
    );
  });

  it('fails closed when a relationship delete projection is missing', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Done',
        parentId: 'parent-feature',
        actions: [{ id: 'delete', enabled: true, disabledReasons: [] }],
      }),
    });
    renderCockpit(mock);
    const user = userEvent.setup();

    await user.click(await screen.findByLabelText('More actions'));
    await user.click(screen.getByRole('menuitem', { name: 'Delete feature' }));
    const dialog = await screen.findByRole('dialog', { name: /Delete Search revamp/ });
    expect(dialog).toHaveTextContent('Impact projection is missing or stale');
    expect(within(dialog).getByRole('button', { name: 'Delete feature' })).toBeDisabled();
  });

  it('deletes the feature after confirmation and closes the tab', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Done',
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
      }),
    });
    mock.api.deleteFeatureCascade.mockResolvedValue({
      featureId: FEATURE_ID,
      operationId: 'delete-1',
      status: 'completed',
      diagnostics: [],
    });
    const { onClose } = renderCockpit(mock);
    const user = userEvent.setup();

    await user.click(await screen.findByLabelText('More actions'));
    await user.click(screen.getByRole('menuitem', { name: 'Delete feature' }));
    const dialog = await screen.findByRole('dialog', { name: /Delete Search revamp/ });
    await user.click(within(dialog).getByRole('button', { name: 'Delete feature' }));

    await waitFor(() =>
      expect(mock.api.deleteFeatureCascade).toHaveBeenCalledWith({ featureId: FEATURE_ID }),
    );
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1));
  });

  it('keeps the feature when delete is disabled while work is running', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        actions: [
          {
            id: 'delete',
            enabled: false,
            disabledReasons: [
              { code: 'repo_cycle_running', message: 'delete is disabled while work is running' },
            ],
          },
        ],
      }),
    });
    renderCockpit(mock);
    const user = userEvent.setup();
    await user.click(await screen.findByLabelText('More actions'));
    expect(screen.getByRole('menuitem', { name: 'Delete feature' })).toBeDisabled();
  });
});

describe('FeatureCockpit review-feedback aftercare', () => {
  it('offers Address review feedback on the aftercare runway when the catalog enables it', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Published',
        actions: [{ id: 'review-feedback', enabled: true, disabledReasons: [] }],
      }),
    });
    renderCockpit(mock);
    const aftercare = await screen.findByRole('region', { name: 'Feature aftercare' });
    expect(
      within(aftercare).getByRole('button', { name: /Address review feedback/ }),
    ).toBeVisible();
    // An enabled action lives on the runway, not duplicated in the aftercare overflow.
    const user = userEvent.setup();
    await user.click(screen.getByLabelText('More actions'));
    expect(screen.queryByRole('menuitem', { name: 'Address review feedback' })).toBeNull();
  });

  it('drops a disabled review-feedback action from the runway and lists it in the overflow with reasons', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Published',
        actions: [
          {
            id: 'review-feedback',
            enabled: false,
            disabledReasons: [
              { code: 'no_pull_request', message: 'review feedback requires a pull request' },
            ],
          },
        ],
      }),
    });
    renderCockpit(mock);
    const aftercare = await screen.findByRole('region', { name: 'Feature aftercare' });
    expect(
      within(aftercare).queryByRole('button', { name: /Address review feedback/ }),
    ).not.toBeInTheDocument();
    const user = userEvent.setup();
    await user.click(screen.getByLabelText('More actions'));
    const item = screen.getByRole('menuitem', { name: 'Address review feedback' });
    expect(item).toBeDisabled();
    expect(screen.getByText('review feedback requires a pull request')).toBeVisible();
  });

  it('opens the review-feedback modal from the regular cockpit overflow (second render site)', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Implementing',
        actions: [{ id: 'review-feedback', enabled: true, disabledReasons: [] }],
      }),
    });
    mock.api.getFeatureConfig.mockResolvedValue(featureConfigSnapshot({}));
    mock.api.fetchReviewFeedback.mockResolvedValue({
      featureId: FEATURE_ID,
      repos: [
        {
          repo: 'repo-a',
          prUrl: 'https://github.com/org/repo-a/pull/1',
          comments: [{ repo: 'repo-a', id: 1, type: 'review', body: 'fix' }],
        },
      ],
    });
    renderCockpit(mock);
    const user = userEvent.setup();
    await user.click(await screen.findByLabelText('More actions'));
    await user.click(screen.getByRole('menuitem', { name: 'Address review feedback' }));
    expect(await screen.findByRole('dialog', { name: 'Address review feedback' })).toBeVisible();
    expect(screen.getByText('repo-a')).toBeVisible();
  });

  it('routes an active review-feedback child to the pass workspace (kind-agnostic)', async () => {
    const childId = 'child1234ef567890';
    const parent = featureSnapshot({
      id: FEATURE_ID,
      status: 'Published',
      actions: [],
      activeChild: {
        id: childId,
        name: 'Address feedback',
        kind: 'review-feedback',
        displayToken: `review-feedback:${childId}`,
        displayState: 'Active — Created',
        pipeline: 'medium',
        status: 'Created',
        relationshipState: 'active',
        startedAt: '2026-07-30T10:00:00Z',
        cost: { totalUsd: 0, byPhase: {} },
        integrationState: 'pending',
        attention: [],
        cleanupWarnings: [],
      },
    });
    const child = featureSnapshot({
      id: childId,
      name: 'Address feedback',
      status: 'Created',
      setupComplete: true,
      setup: { status: 'done', attempt: 1, tasks: [] },
      actions: [{ id: 'start', enabled: true, disabledReasons: [] }],
    });
    const mock = installAgenticoMock({ feature: parent });
    mock.api.getFeature.mockImplementation((id: string) =>
      Promise.resolve(id === childId ? child : parent),
    );
    renderCockpit(mock);
    expect(await screen.findByRole('region', { name: 'Review feedback pass' })).toBeVisible();
    expect(screen.queryByRole('region', { name: 'Feature aftercare' })).not.toBeInTheDocument();
  });

  it('arms auto-start with the returned child id after launching review feedback', async () => {
    const childId = 'child1234ef567890';
    const baseParent = featureSnapshot({
      id: FEATURE_ID,
      status: 'Published',
      repos: ['repo-a'],
      actions: [{ id: 'review-feedback', enabled: true, disabledReasons: [] }],
    });
    const parentWithChild: FeatureSnapshot = {
      ...baseParent,
      actions: [],
      activeChild: {
        id: childId,
        name: 'Address feedback',
        kind: 'review-feedback',
        displayToken: `review-feedback:${childId}`,
        displayState: 'Active — Created',
        pipeline: 'medium',
        status: 'Created',
        relationshipState: 'active',
        startedAt: '2026-07-30T10:00:00Z',
        cost: { totalUsd: 0, byPhase: {} },
        integrationState: 'pending',
        attention: [],
        cleanupWarnings: [],
      },
    };
    const child = featureSnapshot({
      id: childId,
      name: 'Address feedback',
      status: 'Created',
      setupComplete: true,
      setup: { status: 'done', attempt: 1, tasks: [] },
      actions: [{ id: 'start', enabled: true, disabledReasons: [] }],
    });
    let currentParent = baseParent;
    const mock = installAgenticoMock({ feature: baseParent });
    mock.api.getFeature.mockImplementation((id: string) =>
      Promise.resolve(id === childId ? child : currentParent),
    );
    mock.api.getFeatureConfig.mockResolvedValue(featureConfigSnapshot({}));
    mock.api.fetchReviewFeedback.mockResolvedValue({
      featureId: FEATURE_ID,
      repos: [
        {
          repo: 'repo-a',
          prUrl: 'https://github.com/org/repo-a/pull/1',
          comments: [{ repo: 'repo-a', id: 1, type: 'review', body: 'fix the query' }],
        },
      ],
    });
    mock.api.launchReviewFeedbackChild.mockImplementation(async () => {
      currentParent = parentWithChild;
      return { childId, parentId: FEATURE_ID, result: 'created' };
    });
    renderCockpit(mock);
    const user = userEvent.setup();

    const aftercare = await screen.findByRole('region', { name: 'Feature aftercare' });
    await user.click(within(aftercare).getByRole('button', { name: /Address review feedback/ }));
    expect(await screen.findByRole('dialog', { name: 'Address review feedback' })).toBeVisible();
    await user.click(await screen.findByRole('button', { name: /^Launch child/ }));
    await waitFor(() => expect(mock.api.launchReviewFeedbackChild).toHaveBeenCalledOnce());
    expect(mock.api.launchReviewFeedbackChild).toHaveBeenCalledWith({
      parentId: FEATURE_ID,
      comments: [{ repo: 'repo-a', id: 1, type: 'review', body: 'fix the query' }],
      gate: true,
    });
    // The returned child arms auto-start: start fires on the child without a manual click.
    await waitFor(() =>
      expect(mock.api.dispatchFeatureAction).toHaveBeenCalledWith({
        featureId: childId,
        action: 'start',
      }),
    );
    // The cockpit routes the active child to the pass workspace.
    expect(await screen.findByRole('region', { name: 'Review feedback pass' })).toBeVisible();
  });
});
