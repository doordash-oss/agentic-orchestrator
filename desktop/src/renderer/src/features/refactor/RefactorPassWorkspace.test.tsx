import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { FeatureSnapshot, RelationshipChildView } from '../../../../shared/ipc';
import { featureSnapshot, installAgenticoMock } from '../../test/agenticoMock';
import { emptyAttentionDrafts } from '../AttentionInbox';
import { RefactorPassWorkspace } from './RefactorPassWorkspace';

afterEach(cleanup);

const CHILD_ID = 'child1234ef567890';

function childView(overrides: Partial<RelationshipChildView> = {}): RelationshipChildView {
  return {
    id: CHILD_ID,
    name: 'Slop removal pass',
    kind: 'refactor',
    displayToken: 'refactor:child1234ef567890',
    displayState: 'Active — Created',
    pipeline: 'large',
    status: 'Created',
    relationshipState: 'active',
    startedAt: '2026-07-30T10:00:00Z',
    cost: { totalUsd: 1.25, byPhase: {} },
    integrationState: 'pending',
    attention: [],
    cleanupWarnings: [],
    ...overrides,
  };
}

function readyChild(overrides: Partial<FeatureSnapshot> = {}): FeatureSnapshot {
  return featureSnapshot({
    id: CHILD_ID,
    name: 'Slop removal pass',
    status: 'Created',
    setupComplete: true,
    setup: { status: 'done', attempt: 1, tasks: [] },
    actions: [
      { id: 'start', enabled: true, disabledReasons: [] },
      {
        id: 'pause-stop',
        enabled: false,
        disabledReasons: [{ code: 'idle', message: 'not running' }],
      },
      { id: 'resume', enabled: false, disabledReasons: [] },
      { id: 'restart', enabled: false, disabledReasons: [] },
      {
        id: 'discard',
        enabled: true,
        disabledReasons: [],
        impactPreview: {
          kind: 'child_discard',
          subject: { id: CHILD_ID, name: 'Slop removal pass' },
          categories: [
            { key: 'sessions', label: 'Sessions stopped', items: [] },
            { key: 'worktrees', label: 'Disposable worktrees removed', items: ['repo-a … pass'] },
          ],
          retained: ['Review configuration retained'],
        },
      },
    ],
    ...overrides,
  });
}

function renderWorkspace(parent: FeatureSnapshot, onChanged = vi.fn()) {
  const onEditPairedReview = vi.fn();
  render(
    <RefactorPassWorkspace
      parent={parent}
      onChanged={onChanged}
      onEditPairedReview={onEditPairedReview}
      attentionItems={[]}
      refreshAttention={() => Promise.resolve([])}
      attentionDrafts={emptyAttentionDrafts()}
      setAttentionDrafts={vi.fn()}
    />,
  );
  return { onChanged, onEditPairedReview };
}

function parentWith(child: Partial<RelationshipChildView> = {}): FeatureSnapshot {
  return featureSnapshot({
    id: 'parent1234ef5678',
    name: 'Electron app',
    status: 'Published',
    activeChild: childView(child),
  });
}

describe('RefactorPassWorkspace', () => {
  it('shows a state-true custody strip and only the verbs the catalogue enables', async () => {
    const mock = installAgenticoMock({ feature: readyChild() });
    const user = userEvent.setup();
    renderWorkspace(parentWith());

    const custody = await screen.findByRole('list', { name: 'Custody of the work' });
    expect(within(custody).getByText('Published · locked while the pass runs')).toBeVisible();
    expect(within(custody).getByText('Ready to start')).toBeVisible();
    expect(within(custody).getByText('After final review approval')).toBeVisible();

    expect(await screen.findByRole('button', { name: 'Start pass' })).toBeEnabled();
    expect(screen.queryByRole('button', { name: 'Stop' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Resume' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Restart' })).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Start pass' }));
    expect(mock.api.dispatchFeatureAction).toHaveBeenCalledWith({
      featureId: CHILD_ID,
      action: 'start',
    });
  });

  it('explains setup instead of rendering a dead Start button', async () => {
    installAgenticoMock({
      feature: readyChild({
        status: 'SettingUpWorktrees',
        setupComplete: false,
        setup: { status: 'running', attempt: 1, tasks: [] },
        actions: [
          {
            id: 'start',
            enabled: false,
            disabledReasons: [{ code: 'setup_incomplete', message: 'setup' }],
          },
          { id: 'discard', enabled: true, disabledReasons: [] },
        ],
      }),
    });
    renderWorkspace(
      parentWith({ status: 'SettingUpWorktrees', displayState: 'Active — SettingUpWorktrees' }),
    );

    expect(
      await screen.findByText('Preparing worktrees. Start unlocks when setup completes.'),
    ).toBeVisible();
    expect(screen.queryByRole('button', { name: 'Start pass' })).not.toBeInTheDocument();
  });

  it('renders the discard impact projection verbatim and dispatches on confirm', async () => {
    const mock = installAgenticoMock({ feature: readyChild() });
    mock.api.discardRefactorChild.mockResolvedValue({
      result: 'refactor child discarded',
      status: 'completed',
    });
    const user = userEvent.setup();
    renderWorkspace(parentWith());

    await user.click(await screen.findByRole('button', { name: 'Discard pass…' }));
    const dialog = screen.getByRole('dialog', { name: /Discard Slop removal pass/ });
    expect(within(dialog).getByText('Sessions stopped')).toBeVisible();
    expect(within(dialog).getAllByText('None').length).toBeGreaterThan(0);
    expect(within(dialog).getByText('repo-a … pass')).toBeVisible();
    expect(within(dialog).getByText('Review configuration retained')).toBeVisible();
    expect(within(dialog).getByText(/This cannot be undone/)).toBeVisible();

    await user.click(within(dialog).getByRole('button', { name: 'Discard pass' }));
    expect(mock.api.discardRefactorChild).toHaveBeenCalledWith({ childId: CHILD_ID });
  });

  it('fails closed when the discard projection is missing', async () => {
    installAgenticoMock({
      feature: readyChild({
        actions: [
          { id: 'start', enabled: true, disabledReasons: [] },
          { id: 'discard', enabled: true, disabledReasons: [] },
        ],
      }),
    });
    const user = userEvent.setup();
    renderWorkspace(parentWith());

    await user.click(await screen.findByRole('button', { name: 'Discard pass…' }));
    const dialog = screen.getByRole('dialog', { name: /Discard Slop removal pass/ });
    expect(within(dialog).getByRole('alert')).toHaveTextContent('Impact projection is unavailable');
    expect(within(dialog).getByRole('button', { name: 'Discard pass' })).toBeDisabled();
  });

  it('reconciles the integration panel and custody strip to the transaction journal', async () => {
    installAgenticoMock({
      feature: readyChild({
        status: 'ReviewPassed',
        actions: [{ id: 'discard', enabled: true, disabledReasons: [] }],
        transaction: {
          phase: 'attention',
          attention: 'parent tips moved',
          entries: [
            {
              repo: 'repo-a',
              prepState: 'prepared',
              applyState: 'failed',
              conflictFiles: ['main.go'],
            },
          ],
        },
      }),
    });
    renderWorkspace(parentWith({ integrationState: 'attention' }));

    const integration = await screen.findByRole('region', { name: 'Integration' });
    expect(within(integration).getByRole('alert')).toHaveTextContent('parent tips moved');
    expect(within(integration).getByText('prepared → failed')).toBeVisible();
    expect(within(integration).getByText('Conflicts: main.go')).toBeVisible();
    const custody = screen.getByRole('list', { name: 'Custody of the work' });
    expect(within(custody).getByText('Needs attention')).toBeVisible();
  });

  it('keeps settled passes as read-only history with the preserved diff', async () => {
    installAgenticoMock({ feature: readyChild() });
    const user = userEvent.setup();
    renderWorkspace(
      featureSnapshot({
        id: 'parent1234ef5678',
        name: 'Electron app',
        status: 'Published',
        activeChild: childView(),
        childHistory: [
          childView({
            id: 'child0000ef567890',
            name: 'Earlier pass',
            displayState: 'Closed — Completed',
            outcome: 'completed',
            closedAt: '2026-07-29T10:00:00Z',
            diffSummary: 'Repository: repo-a\n3 files changed',
          }),
        ],
      }),
    );

    await user.click(await screen.findByText('Refactor history'));
    expect(screen.getByText('Closed — Completed')).toBeVisible();
    await user.click(screen.getByText('Preserved diff (read-only)'));
    expect(screen.getByText(/3 files changed/)).toBeVisible();
    const history = screen.getByText('Earlier pass').closest('li');
    expect(history).not.toBeNull();
    expect(within(history as HTMLElement).queryByRole('button')).not.toBeInTheDocument();
  });

  it('routes paired review editing through the parent card', async () => {
    installAgenticoMock({ feature: readyChild() });
    const user = userEvent.setup();
    const { onEditPairedReview } = renderWorkspace(parentWith());

    const parentCard = await screen.findByRole('region', { name: 'Parent feature' });
    expect(within(parentCard).getByText(/changes apply to both/i)).toBeVisible();
    await user.click(within(parentCard).getByRole('button', { name: 'Edit paired review' }));
    expect(onEditPairedReview).toHaveBeenCalled();
  });

  it('waits for the child snapshot before offering any verb', async () => {
    const mock = installAgenticoMock({ feature: readyChild() });
    mock.api.getFeature.mockReturnValue(new Promise(() => {}));
    renderWorkspace(parentWith());

    expect(screen.getByText('Loading the pass from the runtime…')).toBeVisible();
    expect(screen.queryByRole('button', { name: 'Start pass' })).not.toBeInTheDocument();
    await waitFor(() => expect(mock.api.getFeature).toHaveBeenCalledWith(CHILD_ID));
  });
});
