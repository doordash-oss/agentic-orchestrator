import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AttentionItem, FeatureSnapshot } from '../../../shared/ipc';
import { featureSnapshot, featureConfigSnapshot, installAgenticoMock } from '../test/agenticoMock';
import { dispatchMediaChange, matchMediaState } from '../test/setup';
import { emptyAttentionDrafts } from './AttentionInbox';
import { FeatureCockpit } from './FeatureCockpit';
import { runFeatureCommand, toggleActiveInspector } from './featureCommands';

// The Review doc surface instantiates Monaco, which needs no real editor in
// jsdom; the stub keeps the segmented-control tests light.
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
  vi.useRealTimers();
  // Narrow-width tests must never leak their viewport into the next test.
  matchMediaState.narrowCockpit = false;
});

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

function renderCockpit(mock = installAgenticoMock(), active = true) {
  const onClose = vi.fn();
  const onLoadedName = vi.fn();
  const view = render(
    <FeatureCockpit
      featureId={FEATURE_ID}
      titleHint="Search revamp"
      onClose={onClose}
      onLoadedName={onLoadedName}
      attentionItems={[]}
      refreshAttention={() => Promise.resolve([])}
      attentionDrafts={emptyAttentionDrafts()}
      setAttentionDrafts={vi.fn()}
      active={active}
    />,
  );
  return {
    mock,
    onClose,
    onLoadedName,
    setActive(next: boolean) {
      view.rerender(
        <FeatureCockpit
          featureId={FEATURE_ID}
          titleHint="Search revamp"
          onClose={onClose}
          onLoadedName={onLoadedName}
          attentionItems={[]}
          refreshAttention={() => Promise.resolve([])}
          attentionDrafts={emptyAttentionDrafts()}
          setAttentionDrafts={vi.fn()}
          active={next}
        />,
      );
    },
  };
}

/**
 * Wide layout: the inspector's trailing split-view pane starts closed (Task
 * 5). Tests that assert on its content (identity facts, pipeline ladder,
 * durable setup, repository PR link, run totals) open it explicitly via the
 * same toggle the toolbar's slot portals in real usage — rendered inline here
 * since `renderCockpit` supplies no `inspectorToggleHost`.
 */
async function openInspector() {
  const user = userEvent.setup();
  await user.click(await screen.findByRole('button', { name: 'Toggle inspector' }));
}

describe('FeatureCockpit snapshot rendering', () => {
  it('always reloads the feature from the server and reports its name', async () => {
    const { mock, onLoadedName } = renderCockpit();
    await screen.findByRole('region', { name: 'Feature Search revamp' });
    expect(mock.api.getFeature).toHaveBeenCalledWith(FEATURE_ID);
    expect(onLoadedName).toHaveBeenCalledWith('Search revamp');
  });

  it('omits durable setup from the inspector', async () => {
    renderCockpit();
    await openInspector();
    await screen.findByRole('region', { name: 'Feature Search revamp' });
    expect(screen.queryByRole('region', { name: 'Durable setup' })).not.toBeInTheDocument();
  });

  it('renders only status and branch in the mono header facts', async () => {
    renderCockpit();
    await openInspector();
    await screen.findByRole('region', { name: 'Feature Search revamp' });
    const header = screen.getByText('Status').closest('dl');
    expect(header).not.toBeNull();
    expect(within(header!).getByLabelText('SettingUpWorktrees')).toBeInTheDocument();
    expect(within(header!).getByText('feature/search-revamp')).toBeInTheDocument();
    expect(within(header!).queryByText('Auto-approve safe commands')).not.toBeInTheDocument();
    expect(within(header!).queryByText('Repository')).not.toBeInTheDocument();
  });

  it('keeps rewind in the overflow menu', async () => {
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
    // The run/iteration popup is the sole run switcher now — no history menu item.
    expect(
      within(actions).queryByRole('menuitem', { name: 'View run history' }),
    ).not.toBeInTheDocument();
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

  it('opens the publish modal from the aftercare runway delivery row', async () => {
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

    await user.click(await screen.findByRole('button', { name: /Publish this feature/ }));

    expect(
      await screen.findByRole('dialog', { name: 'Publish reviewed changes' }),
    ).toBeInTheDocument();
    expect(mock.api.preflightCompletion).toHaveBeenCalledWith({ featureId: FEATURE_ID });
  });

  it('disables the Changes segment while completion actions are present but disabled', async () => {
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

    await screen.findByRole('region', { name: 'Feature Search revamp' });

    // Fixed segments never hide — the unavailable one renders disabled.
    expect(screen.getByRole('tab', { name: 'Changes' })).toBeDisabled();
    expect(screen.queryByRole('button', { name: 'Publish' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Merge' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Clean up' })).not.toBeInTheDocument();
    expect(mock.api.preflightCompletion).not.toHaveBeenCalled();
  });

  it('renders the Live transcript with no framed panel and its controls in the stage bar', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Running',
        actions: [{ id: 'stop', enabled: true, disabledReasons: [] }],
      }),
    });
    renderCockpit(mock);

    await screen.findByRole('region', { name: 'Feature Search revamp' });
    await screen.findByRole('region', { name: 'Current run inspection' });

    // No frame between the content pane and the transcript: the reading column
    // is a direct child of the preview container, with no bar or caption above.
    expect(document.querySelector('.live-preview')?.parentElement).toHaveClass(
      'current-inspection__preview',
    );
    expect(screen.queryByText('Live activity')).not.toBeInTheDocument();

    const trailing = document.querySelector('.cockpit__stage-bar-trailing');
    expect(trailing).not.toBeNull();
    expect(trailing!.querySelector('[aria-label="Preview view"]')).not.toBeNull();
    const refresh = trailing!.querySelector('[aria-label="Refresh current run inspection"]');
    expect(refresh).not.toBeNull();
    // The OS delays a native `title` by ~1s with no way to tune it, so the
    // hover hint rides on data-hint instead.
    expect(refresh!.getAttribute('data-hint')).toBe('Refresh');
    expect(refresh!.hasAttribute('title')).toBe(false);
  });

  it('relocates the full-screen expand icon to the stage-bar row, not an inline surface bar', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Running',
        actions: [
          { id: 'stop', enabled: true, disabledReasons: [] },
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
    const user = userEvent.setup();

    await screen.findByRole('region', { name: 'Feature Search revamp' });
    const expandButton = await screen.findByRole('button', {
      name: 'Expand live preview to full screen',
    });
    expect(expandButton.closest('.cockpit__stage-bar-trailing')).not.toBeNull();
    expect(expandButton.closest('.live-preview__bar-controls')).toBeNull();

    await user.click(expandButton);
    expect(await screen.findByRole('dialog', { name: 'Live agent preview' })).toBeVisible();
  });

  it('opens configuration from the overflow menu', async () => {
    renderCockpit();
    const user = userEvent.setup();
    await screen.findByRole('region', { name: 'Feature Search revamp' });
    await user.click(screen.getByLabelText('More actions'));
    await user.click(screen.getByRole('menuitem', { name: 'Edit configuration…' }));
    expect(
      await screen.findByRole('dialog', { name: 'Feature configuration' }),
    ).toBeInTheDocument();
  });

  it('offers the fixed Review doc and Live segments during a pending review', async () => {
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
    const documentTab = within(tablist).getByRole('tab', { name: 'Review doc' });
    const liveTab = within(tablist).getByRole('tab', { name: 'Live' });
    // The four segments are fixed and never hidden; Changes has no surface
    // to show yet, so it renders present-but-disabled. Files shares Live's
    // availability, so it stays enabled even though Live isn't selected.
    expect(within(tablist).getByRole('tab', { name: 'Changes' })).toBeDisabled();
    expect(within(tablist).getByRole('tab', { name: 'Files' })).toBeEnabled();
    expect(documentTab).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('region', { name: 'Review editor' })).toBeInTheDocument();

    await user.click(liveTab);
    expect(liveTab).toHaveAttribute('aria-selected', 'true');
    expect(
      await screen.findByRole('region', { name: 'Current run inspection' }),
    ).toBeInTheDocument();
  });

  it.each([
    ['CodeReady', 'The work is ready to go out'],
    ['Published', 'The work is in service'],
    ['Done', 'This feature is closed out'],
  ])(
    'defaults %s features to Aftercare while retaining the run record',
    async (status, headline) => {
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
      expect(screen.getByRole('button', { name: 'View run record' })).toBeVisible();
      expect(screen.getByRole('heading', { name: headline })).toBeVisible();
      await waitFor(() =>
        expect(mock.api.getRun).toHaveBeenCalledWith({ featureId: FEATURE_ID, runNumber: 8 }),
      );
    },
  );

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
    const alert = await within(aftercare).findByRole('alert');
    expect(alert).toHaveTextContent('rebase_already_up_to_date');
    expect(alert).toHaveTextContent('Already up to date');
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

    const alert = await within(aftercare).findByRole('alert');
    expect(alert).toHaveTextContent('rebase_target_resolution_failed');
    expect(alert).toHaveTextContent(/Could not resolve a target branch/);

    // A new launch attempt clears the previous error.
    await user.click(within(aftercare).getByRole('button', { name: /Start rebase pass/ }));
    await waitFor(() => expect(mock.api.launchRebaseChild).toHaveBeenCalledTimes(2));
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
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

    // The toolbar's inspector toggle docks/undocks the pass inspector as the
    // trailing split-view pane — the aside never renders inline while closed.
    expect(screen.queryByRole('complementary', { name: 'Pass inspector' })).not.toBeInTheDocument();
    await user.click(within(bar).getByRole('button', { name: 'Toggle inspector' }));
    expect(screen.getByRole('complementary', { name: 'Pass inspector' })).toBeVisible();
    await user.click(within(bar).getByRole('button', { name: 'Toggle inspector' }));
    expect(screen.queryByRole('complementary', { name: 'Pass inspector' })).not.toBeInTheDocument();

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

    await user.click(await screen.findByRole('button', { name: 'View run record' }));

    expect(
      await screen.findByRole('region', { name: 'Current run inspection' }),
    ).toBeInTheDocument();
    expect(screen.getByRole('dialog', { name: 'Run record' })).toHaveClass(
      'cockpit__modal--workspace',
    );
  });

  it('keeps the aftercare facts behind the inspector toggle, closed by default', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Published',
        activeRun: 8,
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

    await screen.findByRole('region', { name: 'Feature aftercare' });
    // Closed on every visit: no facts pane until the toolbar toggle opens it.
    expect(screen.queryByRole('region', { name: 'Feature facts' })).not.toBeInTheDocument();
    const toggle = screen.getByRole('button', { name: 'Toggle inspector' });
    expect(toggle).toHaveAttribute('aria-pressed', 'false');

    await user.click(toggle);
    expect(toggle).toHaveAttribute('aria-pressed', 'true');
    const facts = screen.getByRole('complementary', { name: 'Feature inspector' });
    expect(within(facts).getByRole('region', { name: 'Feature facts' })).toBeVisible();

    await user.click(within(facts).getByRole('button', { name: 'Open pull request' }));
    expect(mock.api.openExternal).toHaveBeenCalledWith({
      url: 'https://github.com/doordash-oss/agentic-orchestrator/pull/107',
    });

    await user.click(toggle);
    expect(screen.queryByRole('region', { name: 'Feature facts' })).not.toBeInTheDocument();
  });

  it('presents the aftercare facts as a drawer at narrow widths', async () => {
    matchMediaState.narrowCockpit = true;
    const mock = installAgenticoMock({
      feature: featureSnapshot({ status: 'Published', activeRun: 8, actions: [] }),
    });
    renderCockpit(mock);
    const user = userEvent.setup();

    await screen.findByRole('region', { name: 'Feature aftercare' });
    await user.click(screen.getByRole('button', { name: 'Inspector' }));
    const drawer = screen.getByRole('dialog', { name: 'Feature inspector' });
    expect(within(drawer).getByRole('region', { name: 'Feature facts' })).toBeVisible();

    await user.click(within(drawer).getByRole('button', { name: 'Close inspector' }));
    expect(screen.queryByRole('dialog', { name: 'Feature inspector' })).not.toBeInTheDocument();
  });

  it('reduces the aftercare toolbar to the wrap-up verbs with a prominent Mark done', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'CodeReady',
        activeRun: 8,
        actions: [
          { id: 'publish', enabled: true, disabledReasons: [] },
          { id: 'merge', enabled: true, disabledReasons: [] },
          { id: 'mark-done', enabled: true, disabledReasons: [] },
          { id: 'cleanup', enabled: true, disabledReasons: [] },
        ],
      }),
    });
    mock.api.preflightCompletion.mockResolvedValue({
      featureId: FEATURE_ID,
      sourceRevision: 'rev-aftercare',
      canMarkDone: false,
      markDoneBlocker: 'repo-a has unpublished commits',
      repos: [{ repo: 'repo-a', publishable: false, touched: true, status: 'eligible' }],
    });
    renderCockpit(mock);

    const actions = await screen.findByRole('group', { name: 'Feature actions' });
    const markDone = await within(actions).findByRole('button', { name: 'Mark done' });
    // Prominent but disabled, with the preflight blocker read out beside it.
    expect(markDone).toHaveClass('cockpit__completion-button');
    expect(markDone).toBeDisabled();
    expect(within(actions).getByText('repo-a has unpublished commits')).toBeVisible();
    expect(within(actions).getByRole('button', { name: 'Clean up' })).toBeVisible();
    // Delivery left the toolbar: it lives on the runway only.
    expect(within(actions).queryByRole('button', { name: 'Publish' })).not.toBeInTheDocument();
    expect(within(actions).queryByRole('button', { name: 'Merge' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Merge this feature/ })).toBeVisible();
  });

  it('carries the reduced verb set into the narrow wrap-up menu', async () => {
    matchMediaState.narrowCockpit = true;
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Published',
        activeRun: 8,
        actions: [
          { id: 'publish', enabled: true, disabledReasons: [] },
          { id: 'merge', enabled: true, disabledReasons: [] },
          { id: 'mark-done', enabled: true, disabledReasons: [] },
          { id: 'cleanup', enabled: true, disabledReasons: [] },
        ],
      }),
    });
    mock.api.preflightCompletion.mockResolvedValue({
      featureId: FEATURE_ID,
      sourceRevision: 'rev-aftercare',
      canMarkDone: true,
      repos: [{ repo: 'repo-a', publishable: true, touched: true, status: 'already_published' }],
    });
    renderCockpit(mock);
    const user = userEvent.setup();

    const actions = await screen.findByRole('group', { name: 'Feature actions' });
    const wrapUp = await waitFor(() => {
      const summary = actions.querySelector<HTMLElement>('.cockpit__wrapup-summary');
      expect(summary).not.toBeNull();
      return summary!;
    });
    await user.click(wrapUp);
    const menu = within(actions).getByRole('menu', { name: 'Wrap up' });
    expect(
      within(menu)
        .getAllByRole('menuitem')
        .map((item) => item.textContent),
    ).toEqual(['Clean up', 'Mark done']);
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
    await openInspector();
    const user = userEvent.setup();

    await user.click(await screen.findByRole('button', { name: 'Open pull request' }));

    expect(mock.api.openExternal).toHaveBeenCalledWith({
      url: 'https://github.com/doordash-oss/agentic-orchestrator/pull/109',
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

describe('FeatureCockpit wide-layout inspector', () => {
  it('starts closed by default and opens/closes as a trailing split-view pane', async () => {
    renderCockpit();
    await screen.findByRole('region', { name: 'Feature Search revamp' });

    // Closed on first view: no split-view pane, and the content column has
    // not opened its second track.
    expect(screen.queryByRole('heading', { name: 'Search revamp' })).not.toBeInTheDocument();
    expect(document.querySelector('.cockpit__content--inspector-open')).toBeNull();
    const toggle = screen.getByRole('button', { name: 'Toggle inspector' });
    expect(toggle).toHaveAttribute('aria-pressed', 'false');

    const user = userEvent.setup();
    await user.click(toggle);

    // Toggling on renders the pane and flips the container's modifier class.
    expect(await screen.findByRole('heading', { name: 'Search revamp' })).toBeVisible();
    expect(document.querySelector('.cockpit__content--inspector-open')).not.toBeNull();
    expect(toggle).toHaveAttribute('aria-pressed', 'true');

    await user.click(toggle);

    // Toggling again closes it.
    expect(screen.queryByRole('heading', { name: 'Search revamp' })).not.toBeInTheDocument();
    expect(document.querySelector('.cockpit__content--inspector-open')).toBeNull();
    expect(toggle).toHaveAttribute('aria-pressed', 'false');
  });

  it('portals the toggle into a toolbar-owned host when one is supplied', async () => {
    installAgenticoMock();
    const host = document.createElement('div');
    document.body.appendChild(host);
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
        inspectorToggleHost={host}
      />,
    );
    await screen.findByRole('region', { name: 'Feature Search revamp' });

    const toggle = await screen.findByRole('button', { name: 'Toggle inspector' });
    expect(host.contains(toggle)).toBe(true);
    host.remove();
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
    await openInspector();
    await screen.findByRole('region', { name: 'Feature Search revamp' });

    expect(screen.getByText('Setup failed in repo-a.')).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'Durable setup' })).not.toBeInTheDocument();
  });

  it('retries via the server-authorized setup action on the SAME feature', async () => {
    const mock = installAgenticoMock();
    mock.api.getFeature.mockResolvedValue(failedSnapshot());
    renderCockpit(mock);
    const user = userEvent.setup();
    await screen.findByRole('region', { name: 'Feature Search revamp' });

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
    await screen.findByRole('region', { name: 'Feature Search revamp' });
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
    await screen.findByRole('region', { name: 'Feature Search revamp' });

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
    await screen.findByRole('region', { name: 'Feature Search revamp' });
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

    const liveTab = await screen.findByRole('tab', { name: 'Live' });
    await user.click(liveTab);
    expect(liveTab).toHaveAttribute('aria-selected', 'true');

    mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: FEATURE_ID });

    expect(await screen.findByRole('region', { name: 'Feature aftercare' })).toBeVisible();
    expect(screen.queryByRole('tablist', { name: 'Stage view' })).not.toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'The work is in service' })).toBeVisible();
  });

  it('refetches on invalidations for this feature and on resync, ignoring others', async () => {
    const { mock } = renderCockpit();
    await screen.findByRole('region', { name: 'Feature Search revamp' });
    const base = mock.api.getFeature.mock.calls.length;

    mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: 'other-id' });
    expect(mock.api.getFeature.mock.calls.length).toBe(base);

    mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: FEATURE_ID });
    await waitFor(() => expect(mock.api.getFeature.mock.calls.length).toBe(base + 1));

    mock.emitAppEvent({ type: 'invalidated', kind: 'resync' });
    await waitFor(() => expect(mock.api.getFeature.mock.calls.length).toBe(base + 2));
  });

  it('refreshes pending delivery when a rebase relationship settles', async () => {
    const published = featureSnapshot({
      status: 'Published',
      actions: [
        { id: 'rebase', enabled: true, disabledReasons: [] },
        { id: 'cleanup', enabled: true, disabledReasons: [] },
      ],
    });
    const mock = installAgenticoMock({ feature: published });
    mock.api.preflightCompletion
      .mockResolvedValueOnce({
        featureId: FEATURE_ID,
        sourceRevision: 'before-rebase',
        canMarkDone: true,
        repos: [{ repo: 'repo-a', publishable: true, touched: true, status: 'completed' }],
      })
      .mockResolvedValue({
        featureId: FEATURE_ID,
        sourceRevision: 'after-rebase',
        canMarkDone: true,
        repos: [
          {
            repo: 'repo-a',
            publishable: true,
            touched: true,
            status: 'unpublished_changes',
            pendingCommits: 1,
            pushMode: 'fast_forward',
          },
        ],
      });
    renderCockpit(mock);

    const aftercare = await screen.findByRole('region', { name: 'Feature aftercare' });
    await waitFor(() => expect(mock.api.preflightCompletion).toHaveBeenCalledTimes(1));
    expect(within(aftercare).queryByRole('button', { name: /Publish new commits/ })).toBeNull();

    mock.emitAppEvent({
      type: 'invalidated',
      kind: 'lifecycle.updated',
      resourceType: 'relationship',
      resourceId: `${FEATURE_ID}:rebase1234ef567890`,
      parentId: FEATURE_ID,
      childId: 'rebase1234ef567890',
    });

    expect(
      await within(aftercare).findByRole('button', { name: /Publish new commits/ }),
    ).toHaveTextContent('Publish updates');
    expect(mock.api.preflightCompletion).toHaveBeenCalledTimes(2);
  });

  it('delays and coalesces invalidations while inactive, then flushes on activation', async () => {
    const mock = installAgenticoMock();
    const view = renderCockpit(mock, false);
    await screen.findByRole('region', { name: 'Feature Search revamp' });
    vi.useFakeTimers();
    const base = mock.api.getFeature.mock.calls.length;
    mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: FEATURE_ID });
    mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: FEATURE_ID });
    expect(mock.api.getFeature).toHaveBeenCalledTimes(base);
    await vi.advanceTimersByTimeAsync(4_999);
    expect(mock.api.getFeature).toHaveBeenCalledTimes(base);
    await act(async () => view.setActive(true));
    expect(mock.api.getFeature).toHaveBeenCalledTimes(base + 1);
  });

  it('coalesces focused session completion into one delayed silent refresh', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Implementing',
        currentPhase: 'Implement',
        setup: { status: 'done', attempt: 1, tasks: [] },
        actions: [{ id: 'pause-stop', enabled: true, disabledReasons: [] }],
      }),
      sessions: [
        {
          id: 'session-craft',
          featureId: FEATURE_ID,
          runNumber: 1,
          phase: 'Implement',
          kind: 'implementer',
          status: 'running',
          startedAt: '2026-08-08T10:00:00.000Z',
          taskActivities: [],
          runningTaskCount: 0,
          usage: {},
        },
      ],
    });
    renderCockpit(mock);
    await userEvent.click(await screen.findByRole('tab', { name: 'Live' }));
    await waitFor(() => expect(mock.api.openSessionOutput).toHaveBeenCalled());
    const initialRequests = mock.api.getFeature.mock.calls.length;
    vi.useFakeTimers();

    act(() => {
      mock.emitSessionOutput({
        subscriptionId: 'subscription-1',
        type: 'done',
        sessionId: 'session-craft',
        nextIndex: 1,
      });
      mock.emitSessionOutput({
        subscriptionId: 'subscription-1',
        type: 'done',
        sessionId: 'session-craft',
        nextIndex: 1,
      });
    });

    await act(async () => vi.advanceTimersByTimeAsync(499));
    expect(mock.api.getFeature).toHaveBeenCalledTimes(initialRequests);

    await act(async () => vi.advanceTimersByTimeAsync(1));
    expect(mock.api.getFeature).toHaveBeenCalledTimes(initialRequests + 1);
  });

  it('cancels a completion refresh when the retained view becomes inactive', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Implementing',
        currentPhase: 'Implement',
        setup: { status: 'done', attempt: 1, tasks: [] },
        actions: [{ id: 'pause-stop', enabled: true, disabledReasons: [] }],
      }),
      sessions: [
        {
          id: 'session-craft',
          featureId: FEATURE_ID,
          runNumber: 1,
          phase: 'Implement',
          kind: 'implementer',
          status: 'running',
          startedAt: '2026-08-08T10:00:00.000Z',
          taskActivities: [],
          runningTaskCount: 0,
          usage: {},
        },
      ],
    });
    const view = renderCockpit(mock);
    await userEvent.click(await screen.findByRole('tab', { name: 'Live' }));
    await waitFor(() => expect(mock.api.openSessionOutput).toHaveBeenCalled());
    const initialRequests = mock.api.getFeature.mock.calls.length;
    vi.useFakeTimers();

    act(() => {
      mock.emitSessionOutput({
        subscriptionId: 'subscription-1',
        type: 'done',
        sessionId: 'session-craft',
        nextIndex: 1,
      });
      view.setActive(false);
    });

    await act(async () => vi.advanceTimersByTimeAsync(500));
    expect(mock.api.getFeature).toHaveBeenCalledTimes(initialRequests);
  });

  it('cancels a completion refresh when the document becomes hidden', async () => {
    let visible = true;
    const visibilitySpy = vi
      .spyOn(document, 'visibilityState', 'get')
      .mockImplementation(() => (visible ? 'visible' : 'hidden'));
    try {
      const mock = installAgenticoMock({
        feature: featureSnapshot({
          status: 'Implementing',
          currentPhase: 'Implement',
          setup: { status: 'done', attempt: 1, tasks: [] },
          actions: [{ id: 'pause-stop', enabled: true, disabledReasons: [] }],
        }),
        sessions: [
          {
            id: 'session-craft',
            featureId: FEATURE_ID,
            runNumber: 1,
            phase: 'Implement',
            kind: 'implementer',
            status: 'running',
            startedAt: '2026-08-08T10:00:00.000Z',
            taskActivities: [],
            runningTaskCount: 0,
            usage: {},
          },
        ],
      });
      renderCockpit(mock);
      await userEvent.click(await screen.findByRole('tab', { name: 'Live' }));
      await waitFor(() => expect(mock.api.openSessionOutput).toHaveBeenCalled());
      const initialRequests = mock.api.getFeature.mock.calls.length;
      vi.useFakeTimers();

      act(() => {
        mock.emitSessionOutput({
          subscriptionId: 'subscription-1',
          type: 'done',
          sessionId: 'session-craft',
          nextIndex: 1,
        });
        visible = false;
        document.dispatchEvent(new Event('visibilitychange'));
      });

      await act(async () => vi.advanceTimersByTimeAsync(500));
      expect(mock.api.getFeature).toHaveBeenCalledTimes(initialRequests);
    } finally {
      visibilitySpy.mockRestore();
    }
  });

  it('cancels a completion refresh when the focused cockpit unmounts', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Implementing',
        currentPhase: 'Implement',
        setup: { status: 'done', attempt: 1, tasks: [] },
        actions: [{ id: 'pause-stop', enabled: true, disabledReasons: [] }],
      }),
      sessions: [
        {
          id: 'session-craft',
          featureId: FEATURE_ID,
          runNumber: 1,
          phase: 'Implement',
          kind: 'implementer',
          status: 'running',
          startedAt: '2026-08-08T10:00:00.000Z',
          taskActivities: [],
          runningTaskCount: 0,
          usage: {},
        },
      ],
    });
    renderCockpit(mock);
    await userEvent.click(await screen.findByRole('tab', { name: 'Live' }));
    await waitFor(() => expect(mock.api.openSessionOutput).toHaveBeenCalled());
    const initialRequests = mock.api.getFeature.mock.calls.length;
    vi.useFakeTimers();

    act(() => {
      mock.emitSessionOutput({
        subscriptionId: 'subscription-1',
        type: 'done',
        sessionId: 'session-craft',
        nextIndex: 1,
      });
    });
    expect(vi.getTimerCount()).toBe(1);

    cleanup();
    expect(vi.getTimerCount()).toBe(0);

    await act(async () => vi.advanceTimersByTimeAsync(500));
    expect(mock.api.getFeature).toHaveBeenCalledTimes(initialRequests);
  });

  it('propagates document visibility to pause and restore live child work', async () => {
    vi.useFakeTimers();
    let visible = true;
    const visibilitySpy = vi
      .spyOn(document, 'visibilityState', 'get')
      .mockImplementation(() => (visible ? 'visible' : 'hidden'));
    const hiddenSpy = vi.spyOn(document, 'hidden', 'get').mockImplementation(() => !visible);
    try {
      const activeSnapshot = featureSnapshot({
        status: 'Researching',
        currentPhase: 'Research',
        setup: { status: 'done', attempt: 1, tasks: [] },
        actions: [{ id: 'pause-stop', enabled: true, disabledReasons: [] }],
      });
      const mock = installAgenticoMock({
        feature: activeSnapshot,
        sessions: [
          {
            id: 'research-session',
            featureId: FEATURE_ID,
            runNumber: 1,
            phase: 'Research',
            kind: 'phase',
            status: 'running',
            startedAt: '2026-08-04T10:00:00.000Z',
            taskActivities: [],
            runningTaskCount: 0,
            usage: {},
          },
        ],
      });
      mock.api.getRun.mockResolvedValue({ runNumber: 1, artifactCount: 0 });
      renderCockpit(mock);
      await vi.waitFor(() => expect(mock.api.openSessionOutput).toHaveBeenCalledTimes(1));

      visible = false;
      act(() => document.dispatchEvent(new Event('visibilitychange')));
      await vi.waitFor(() => expect(mock.api.cancelSessionOutput).toHaveBeenCalledTimes(1));
      expect(mock.sessionOutputListenerCount()).toBe(0);
      const hiddenCalls = {
        run: mock.api.getRun.mock.calls.length,
        preview: mock.api.getLivePreview.mock.calls.length,
        sessions: mock.api.listRunSessions.mock.calls.length,
        output: mock.api.openSessionOutput.mock.calls.length,
      };

      act(() => mock.emitAppEvent({ type: 'invalidated', kind: 'session.updated' }));
      await act(() => vi.advanceTimersByTimeAsync(6_000));
      expect(mock.api.getRun).toHaveBeenCalledTimes(hiddenCalls.run);
      expect(mock.api.getLivePreview).toHaveBeenCalledTimes(hiddenCalls.preview);
      expect(mock.api.listRunSessions).toHaveBeenCalledTimes(hiddenCalls.sessions);
      expect(mock.api.openSessionOutput).toHaveBeenCalledTimes(hiddenCalls.output);

      visible = true;
      act(() => document.dispatchEvent(new Event('visibilitychange')));
      await vi.waitFor(() =>
        expect(mock.api.getLivePreview).toHaveBeenCalledTimes(hiddenCalls.preview + 1),
      );
      await vi.waitFor(() =>
        expect(mock.api.listRunSessions).toHaveBeenCalledTimes(hiddenCalls.sessions + 1),
      );
      await vi.waitFor(() =>
        expect(mock.api.openSessionOutput).toHaveBeenCalledTimes(hiddenCalls.output + 1),
      );
    } finally {
      visibilitySpy.mockRestore();
      hiddenSpy.mockRestore();
    }
  });

  it('keeps the loaded snapshot visible when a silent refresh fails', async () => {
    const mock = installAgenticoMock();
    renderCockpit(mock, true);
    expect(await screen.findByRole('region', { name: 'Feature Search revamp' })).toBeVisible();
    await openInspector();
    mock.api.getFeature.mockRejectedValueOnce(new Error('unavailable: runtime busy'));
    mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: FEATURE_ID });
    expect(await screen.findByText('Refreshing from the runtime…')).toBeVisible();
    expect(screen.getByRole('region', { name: 'Feature Search revamp' })).toBeVisible();
    expect(screen.queryByText(/Loading Search revamp from the runtime/)).not.toBeInTheDocument();
  });

  it('shows the missing state when a silent refresh reports not_found', async () => {
    const mock = installAgenticoMock();
    renderCockpit(mock, true);
    await screen.findByRole('region', { name: 'Feature Search revamp' });
    mock.api.getFeature.mockRejectedValueOnce(new Error('not_found: feature not found'));
    mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: FEATURE_ID });
    expect(await screen.findByText('This feature no longer exists on the server.')).toBeVisible();
  });

  it('does not compete with an unresolved initial load after an invalidation', async () => {
    const mock = installAgenticoMock();
    let resolveInitial!: (snapshot: ReturnType<typeof featureSnapshot>) => void;
    mock.api.getFeature
      .mockImplementationOnce(
        () => new Promise((resolve) => (resolveInitial = resolve as typeof resolveInitial)),
      )
      .mockRejectedValueOnce(new Error('unavailable: runtime busy'));
    renderCockpit(mock);
    await waitFor(() => expect(mock.api.getFeature).toHaveBeenCalledTimes(1));

    mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: FEATURE_ID });
    expect(mock.api.getFeature).toHaveBeenCalledTimes(1);

    await act(async () => resolveInitial(featureSnapshot()));
    await openInspector();
    expect(await screen.findByText('Refreshing from the runtime…')).toBeVisible();
    expect(await screen.findByRole('region', { name: 'Feature Search revamp' })).toBeVisible();
    expect(screen.queryByText(/Loading Search revamp from the runtime/)).not.toBeInTheDocument();
  });

  it('runs a trailing refresh when invalidated during a current refresh', async () => {
    const mock = installAgenticoMock();
    let resolveFirstRefresh!: (snapshot: ReturnType<typeof featureSnapshot>) => void;
    let resolveTrailingRefresh!: (snapshot: ReturnType<typeof featureSnapshot>) => void;
    mock.api.getFeature
      .mockResolvedValueOnce(featureSnapshot())
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => (resolveFirstRefresh = resolve as typeof resolveFirstRefresh)),
      )
      .mockImplementationOnce(
        () =>
          new Promise(
            (resolve) => (resolveTrailingRefresh = resolve as typeof resolveTrailingRefresh),
          ),
      );
    renderCockpit(mock);
    await screen.findByRole('region', { name: 'Feature Search revamp' });

    mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: FEATURE_ID });
    mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: FEATURE_ID });
    await waitFor(() => expect(mock.api.getFeature).toHaveBeenCalledTimes(2));
    await act(async () => resolveFirstRefresh(featureSnapshot({ name: 'First refresh' })));
    await waitFor(() => expect(mock.api.getFeature).toHaveBeenCalledTimes(3));
    await act(async () => resolveTrailingRefresh(featureSnapshot({ name: 'Newest snapshot' })));
    expect(
      await screen.findByRole('region', { name: 'Feature Newest snapshot' }),
    ).toBeInTheDocument();
  });

  it('serializes an action refresh with invalidations and converges through one trailing refresh', async () => {
    const ready = featureSnapshot({
      status: 'Created',
      setup: { status: 'done', attempt: 1, tasks: [] },
      actions: [{ id: 'start', enabled: true, disabledReasons: [] }],
    });
    const converged = featureSnapshot({
      name: 'Converged snapshot',
      status: 'Planning',
      currentPhase: 'Plan',
      setup: { status: 'done', attempt: 1, tasks: [] },
      actions: [],
    });
    let resolveActionRefresh!: (snapshot: FeatureSnapshot) => void;
    let resolveTrailingRefresh!: (snapshot: FeatureSnapshot) => void;
    const mock = installAgenticoMock({ feature: ready });
    mock.api.getFeature
      .mockResolvedValueOnce(ready)
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => (resolveActionRefresh = resolve as typeof resolveActionRefresh)),
      )
      .mockImplementationOnce(
        () =>
          new Promise(
            (resolve) => (resolveTrailingRefresh = resolve as typeof resolveTrailingRefresh),
          ),
      );
    mock.api.dispatchFeatureAction.mockResolvedValue({
      featureId: FEATURE_ID,
      action: 'start',
      result: 'started',
      sessionIds: [],
    });
    renderCockpit(mock);
    const user = userEvent.setup();
    await user.click(await screen.findByRole('button', { name: 'Start' }));
    await waitFor(() => expect(mock.api.getFeature).toHaveBeenCalledTimes(2));

    act(() => {
      mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: FEATURE_ID });
      mock.emitAppEvent({ type: 'invalidated', kind: 'feature.updated', featureId: FEATURE_ID });
    });
    expect(mock.api.getFeature).toHaveBeenCalledTimes(2);

    await act(async () => resolveActionRefresh(ready));
    await waitFor(() => expect(mock.api.getFeature).toHaveBeenCalledTimes(3));
    await act(async () => resolveTrailingRefresh(converged));
    expect(
      await screen.findByRole('region', { name: 'Feature Converged snapshot' }),
    ).toBeInTheDocument();
    expect(mock.api.getFeature).toHaveBeenCalledTimes(3);
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
    await screen.findByRole('region', { name: 'Feature Search revamp' });
    await openInspector();

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

    const request = await screen.findByRole('dialog', { name: 'Answer one question to resume' });
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
        screen.queryByRole('dialog', { name: 'Answer one question to resume' }),
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

    const request = await screen.findByRole('dialog', { name: 'Answer one question to resume' });
    await user.click(within(request).getByRole('button', { name: 'Answer later' }));
    expect(
      screen.queryByRole('dialog', { name: 'Answer one question to resume' }),
    ).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Advance attention' }));

    expect(
      within(
        await screen.findByRole('dialog', { name: 'Answer one question to resume' }),
      ).getByLabelText(/Which follow-up deployment window should implementation use/),
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
              { code: 'running', message: 'delete is disabled while work is running' },
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

  it('keeps a blocked review-feedback action on the runway with its reason and out of the overflow', async () => {
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
    const card = within(aftercare).getByRole('button', { name: /Address review feedback/ });
    expect(card).toBeDisabled();
    expect(card).toHaveTextContent('review feedback requires a pull request');
    const user = userEvent.setup();
    await user.click(screen.getByLabelText('More actions'));
    expect(screen.queryByRole('menuitem', { name: 'Address review feedback' })).toBeNull();
  });

  it('opens the review-feedback workspace from the regular cockpit overflow (second render site)', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Implementing',
        actions: [{ id: 'review-feedback', enabled: true, disabledReasons: [] }],
      }),
    });
    mock.api.getFeatureConfig.mockResolvedValue(featureConfigSnapshot({}));
    mock.api.fetchReviewFeedback.mockResolvedValue({
      revision: 4,
      snapshotId: 'snap-1',
      repos: [
        {
          repo: 'repo-a',
          prUrl: 'https://github.com/org/repo-a/pull/1',
          comments: [
            {
              stableRef: 'repo-a:review:1',
              selected: true,
              repo: 'repo-a',
              id: 1,
              type: 'review',
              body: 'fix',
            },
          ],
        },
      ],
    });
    renderCockpit(mock);
    const user = userEvent.setup();
    await user.click(await screen.findByLabelText('More actions'));
    await user.click(screen.getByRole('menuitem', { name: 'Address review feedback' }));
    expect(await screen.findByRole('dialog', { name: 'Address review feedback' })).toBeVisible();
    expect(screen.getAllByText('repo-a').length).toBeGreaterThan(0);
  });

  it('loads a preserved diff from the detail route when only the flag arrived', async () => {
    const user = userEvent.setup();
    const closed = {
      id: 'child0000ef567890',
      name: 'Earlier pass',
      kind: 'refactor',
      displayToken: 'refactor:child0000ef567890',
      displayState: 'Closed — Completed',
      pipeline: 'medium',
      status: 'Done',
      relationshipState: 'closed',
      outcome: 'completed' as const,
      startedAt: '2026-07-28T10:00:00Z',
      closedAt: '2026-07-29T10:00:00Z',
      cost: { totalUsd: 1, byPhase: {} },
      integrationState: 'merged',
      attention: [],
      cleanupWarnings: [],
      hasDiffSummary: true,
    };
    const withoutBody = featureSnapshot({
      status: 'Published',
      actions: [],
      childHistory: [closed],
    });
    const mock = installAgenticoMock({ feature: withoutBody });
    renderCockpit(mock);

    await user.click(await screen.findByText('Pass history'));
    await user.click(screen.getByText('Preserved diff (read-only)'));
    mock.api.getFeature.mockResolvedValue({
      ...withoutBody,
      childHistory: [{ ...closed, diffSummary: 'Repository: repo-a\n3 files changed' }],
    });
    await user.click(screen.getByRole('button', { name: 'Load diff' }));
    expect(await screen.findByText(/3 files changed/)).toBeVisible();
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
      revision: 5,
      snapshotId: 'snap-1',
      repos: [
        {
          repo: 'repo-a',
          prUrl: 'https://github.com/org/repo-a/pull/1',
          comments: [
            {
              stableRef: 'repo-a:review:1',
              selected: true,
              repo: 'repo-a',
              id: 1,
              type: 'review',
              body: 'fix the query',
            },
          ],
        },
      ],
    });
    mock.api.launchReviewFeedbackChild.mockImplementation(async () => {
      currentParent = parentWithChild;
      return {
        featureId: childId,
        parentId: FEATURE_ID,
        result: 'created',
        changed: 2,
        omitted: 1,
      };
    });
    renderCockpit(mock);
    const user = userEvent.setup();

    const aftercare = await screen.findByRole('region', { name: 'Feature aftercare' });
    await user.click(within(aftercare).getByRole('button', { name: /Address review feedback/ }));
    expect(await screen.findByRole('dialog', { name: 'Address review feedback' })).toBeVisible();
    await user.click(await screen.findByRole('button', { name: /Address comments \(1\)/ }));
    await waitFor(() => expect(mock.api.launchReviewFeedbackChild).toHaveBeenCalledOnce());
    // Constant-size launch: revision + gate only, never comment payloads.
    expect(mock.api.launchReviewFeedbackChild).toHaveBeenCalledWith({
      parentId: FEATURE_ID,
      expectedRevision: 5,
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
    // The launch receipt counts surface in the child-pass banner.
    expect(await screen.findByText('2 changed, 1 omitted since review')).toBeVisible();
  });
});

describe('FeatureCockpit run switcher popup', () => {
  function runSummary(runNumber: number) {
    return { runNumber, artifactCount: 0, sealedAt: `2026-07-${10 + runNumber}T10:00:00Z` };
  }

  function renderWithSwitcher() {
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
  }

  async function openSwitcher() {
    const user = userEvent.setup();
    const summary = await screen.findByText('Plan', { selector: '.cockpit__run-switcher-summary' });
    await user.click(summary);
    return { user, menu: await screen.findByRole('menu', { name: 'Switch run' }) };
  }

  it('appends older sealed runs on Load older and drops the control at the last page', async () => {
    const mock = installAgenticoMock();
    mock.api.listRuns.mockImplementation(({ page }: { page: number }) =>
      Promise.resolve(
        page === 1
          ? { runs: [runSummary(9), runSummary(8)], page: 1, pageSize: 8, total: 3, totalPages: 2 }
          : { runs: [runSummary(7)], page: 2, pageSize: 8, total: 3, totalPages: 2 },
      ),
    );
    renderWithSwitcher();

    const { user, menu } = await openSwitcher();
    expect((await within(menu).findAllByRole('menuitem')).map((item) => item.textContent)).toEqual([
      'Plan · current',
      'Run 9 · sealed',
      'Run 8 · sealed',
    ]);

    await user.click(within(menu).getByRole('button', { name: 'Load older' }));

    await waitFor(() =>
      expect(
        within(menu)
          .getAllByRole('menuitem')
          .map((item) => item.textContent),
      ).toEqual(['Plan · current', 'Run 9 · sealed', 'Run 8 · sealed', 'Run 7 · sealed']),
    );
    // Last page reached: the affordance retires rather than requesting page 3.
    expect(within(menu).queryByRole('button', { name: 'Load older' })).not.toBeInTheDocument();
    expect(mock.api.listRuns).toHaveBeenCalledTimes(2);
    expect(mock.api.listRuns).toHaveBeenLastCalledWith({
      featureId: FEATURE_ID,
      page: 2,
      pageSize: 8,
    });
  });

  it('resets to the first page when the menu is reopened', async () => {
    const mock = installAgenticoMock();
    mock.api.listRuns.mockImplementation(({ page }: { page: number }) =>
      Promise.resolve(
        page === 1
          ? { runs: [runSummary(9)], page: 1, pageSize: 8, total: 2, totalPages: 2 }
          : { runs: [runSummary(8)], page: 2, pageSize: 8, total: 2, totalPages: 2 },
      ),
    );
    renderWithSwitcher();

    const { user, menu } = await openSwitcher();
    await user.click(within(menu).getByRole('button', { name: 'Load older' }));
    await waitFor(() => expect(within(menu).getAllByRole('menuitem')).toHaveLength(3));

    const summary = screen.getByText('Plan', { selector: '.cockpit__run-switcher-summary' });
    await user.click(summary);
    await user.click(summary);

    await waitFor(() =>
      expect(
        within(menu)
          .getAllByRole('menuitem')
          .map((item) => item.textContent),
      ).toEqual(['Plan · current', 'Run 9 · sealed']),
    );
    expect(within(menu).getByRole('button', { name: 'Load older' })).toBeInTheDocument();
  });

  it('reports a failed run-history read inside the menu without losing the current run', async () => {
    const mock = installAgenticoMock();
    mock.api.listRuns.mockRejectedValue(new Error('history unavailable'));
    renderWithSwitcher();

    const { menu } = await openSwitcher();
    expect(
      await within(menu).findByText(/Could not load run history — history unavailable/),
    ).toBeVisible();
    expect(within(menu).getByRole('menuitem', { name: 'Plan · current' })).toBeVisible();
  });
});

/**
 * The funnel's other half: whatever invoked a feature command — the ⌘K
 * palette or the native Feature menu — it lands on the cockpit's own flow.
 * These drive the registered executor directly, which is exactly what the
 * palette entry and the routed menu click do.
 */
describe('FeatureCockpit feature-command funnel', () => {
  /** Mounts the cockpit — and with it the funnel registration — and settles. */
  async function mounted(mock: ReturnType<typeof installAgenticoMock>) {
    renderCockpit(mock);
    await screen.findByRole('region', { name: 'Feature Search revamp' });
    return mock;
  }

  function withFeature(snapshot: FeatureSnapshot) {
    return installAgenticoMock({ feature: snapshot });
  }

  it('presents the cockpit confirmation for Stop rather than dispatching straight away', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Implementing',
        actions: [{ id: 'pause-stop', enabled: true, disabledReasons: [] }],
      }),
    });
    mock.api.listSessions.mockResolvedValue([]);
    await mounted(mock);

    await act(async () => {
      expect(runFeatureCommand('feature.pause-stop', { featureId: FEATURE_ID })).toBe('executed');
    });

    expect(await screen.findByRole('dialog', { name: /stop/i })).toBeInTheDocument();
    expect(mock.api.dispatchFeatureAction).not.toHaveBeenCalled();
  });

  it('presents the cockpit confirmation for Delete rather than deleting straight away', async () => {
    const mock = await mounted(
      withFeature(
        featureSnapshot({
          status: 'CodeReady',
          actions: [{ id: 'delete', enabled: true, disabledReasons: [] }],
        }),
      ),
    );

    await act(async () => {
      runFeatureCommand('feature.delete', { featureId: FEATURE_ID });
    });

    expect(await screen.findByRole('dialog', { name: /delete/i })).toBeInTheDocument();
    expect(mock.api.deleteFeatureCascade).not.toHaveBeenCalled();
  });

  it('opens the completion preflight modal for Publish', async () => {
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
    await mounted(mock);

    await act(async () => {
      runFeatureCommand('feature.publish', { featureId: FEATURE_ID });
    });

    expect(
      await screen.findByRole('dialog', { name: 'Publish reviewed changes' }),
    ).toBeInTheDocument();
    expect(mock.api.preflightCompletion).toHaveBeenCalledWith({ featureId: FEATURE_ID });
  });

  it('keeps a timed-out publish locked after close and reopen when refreshes fail', async () => {
    const snapshot = featureSnapshot({
      status: 'CodeReady',
      actions: [{ id: 'publish', enabled: true, disabledReasons: [] }],
    });
    const preflight = {
      featureId: FEATURE_ID,
      sourceRevision: 'rev-complete',
      canMarkDone: true,
      repos: [{ repo: 'repo-a', publishable: true, touched: true, status: 'unpublished_changes' }],
    };
    const mock = installAgenticoMock({ feature: snapshot });
    mock.api.preflightCompletion
      .mockResolvedValueOnce(preflight)
      .mockResolvedValueOnce(preflight)
      .mockRejectedValue(new Error('refresh unavailable'));
    mock.api.getFeature
      .mockResolvedValueOnce(snapshot)
      .mockRejectedValue(new Error('refresh unavailable'));
    mock.api.dispatchFeatureAction.mockRejectedValue(
      new Error('E_REQUEST_TIMEOUT: publish did not answer before the bound'),
    );
    await mounted(mock);
    const user = userEvent.setup();

    await act(async () => {
      runFeatureCommand('feature.publish', { featureId: FEATURE_ID });
    });
    await user.click(await screen.findByRole('button', { name: 'Publish updates' }));
    expect(await screen.findByRole('button', { name: 'Reconciling…' })).toBeDisabled();

    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(
      screen.queryByRole('dialog', { name: 'Publish reviewed changes' }),
    ).not.toBeInTheDocument();

    await act(async () => {
      runFeatureCommand('feature.publish', { featureId: FEATURE_ID });
    });
    expect(
      await screen.findByText(
        'Publish may still be running. Quit and reopen Agentico before publishing again.',
      ),
    ).toBeVisible();
    expect(screen.getByRole('button', { name: 'Reconciling…' })).toBeDisabled();
    expect(mock.api.dispatchFeatureAction).toHaveBeenCalledOnce();
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(
      screen.queryByRole('dialog', { name: 'Publish reviewed changes' }),
    ).not.toBeInTheDocument();
  });

  it('opens the configuration editor for Configuration, with no server action behind it', async () => {
    await mounted(withFeature(featureSnapshot({ actions: [] })));

    await act(async () => {
      expect(runFeatureCommand('feature.configuration', { featureId: FEATURE_ID })).toBe(
        'executed',
      );
    });

    expect(
      await screen.findByRole('dialog', { name: 'Feature configuration' }),
    ).toBeInTheDocument();
  });

  it('dispatches Start immediately, exactly as the cockpit button does', async () => {
    const mock = await mounted(
      withFeature(
        featureSnapshot({
          status: 'Created',
          setup: { status: 'done', attempt: 1, tasks: [] },
          actions: [{ id: 'start', enabled: true, disabledReasons: [] }],
        }),
      ),
    );

    await act(async () => {
      runFeatureCommand('feature.start', { featureId: FEATURE_ID });
    });

    await waitFor(() =>
      expect(mock.api.dispatchFeatureAction).toHaveBeenCalledWith({
        featureId: FEATURE_ID,
        action: 'start',
      }),
    );
  });

  it('launches the rebase pass for Rebase, with no modal in between', async () => {
    const mock = await mounted(
      withFeature(
        featureSnapshot({
          status: 'Published',
          actions: [{ id: 'rebase', enabled: true, disabledReasons: [] }],
        }),
      ),
    );
    mock.api.launchRebaseChild.mockResolvedValue({ childId: 'child1234abcd5678' });

    await act(async () => {
      runFeatureCommand('feature.rebase', { featureId: FEATURE_ID });
    });

    await waitFor(() =>
      expect(mock.api.launchRebaseChild).toHaveBeenCalledWith({ featureId: FEATURE_ID }),
    );
  });

  it('flips the inspector from the View menu route', async () => {
    await mounted(withFeature(featureSnapshot()));

    await act(async () => {
      expect(toggleActiveInspector()).toBe(true);
    });
    expect(await screen.findByRole('button', { name: 'Toggle inspector' })).toHaveAttribute(
      'aria-pressed',
      'true',
    );
  });

  it('stops being a funnel target once it unmounts', async () => {
    await mounted(withFeature(featureSnapshot()));
    cleanup();
    expect(runFeatureCommand('feature.configuration', { featureId: FEATURE_ID })).toBe('no-target');
  });
});
