import { act, cleanup, render, renderHook, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AttentionItem, FeatureSnapshot, RelationshipChildView } from '../../../../shared/ipc';
import { featureSnapshot, installAgenticoMock } from '../../test/agenticoMock';
import { emptyAttentionDrafts } from '../AttentionInbox';
import {
  RefactorPassWorkspace,
  useRefactorPass,
  type RefactorPassController,
} from './RefactorPassWorkspace';
import { passActions } from './refactorPassModel';

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

function parentWith(child: Partial<RelationshipChildView> = {}): FeatureSnapshot {
  return featureSnapshot({
    id: 'parent1234ef5678',
    name: 'Electron app',
    status: 'Published',
    activeChild: childView(child),
  });
}

function controllerFor(
  parent: FeatureSnapshot,
  child: FeatureSnapshot | null,
  overrides: Partial<RefactorPassController> = {},
): RefactorPassController {
  return {
    view: parent.activeChild,
    childState: child === null ? { phase: 'loading' } : { phase: 'loaded', child },
    child,
    actions: child === null ? [] : passActions(child),
    discardAction: child?.actions.find((action) => action.id === 'discard'),
    busy: false,
    notice: null,
    discardOpen: false,
    dispatch: vi.fn(() => Promise.resolve()),
    armAutoStart: vi.fn(),
    openDiscard: vi.fn(),
    closeDiscard: vi.fn(),
    discard: vi.fn(() => Promise.resolve()),
    reload: vi.fn(),
    ...overrides,
  };
}

function renderWorkspace(
  parent: FeatureSnapshot,
  pass: RefactorPassController,
  attentionItems: AttentionItem[] = [],
) {
  render(
    <RefactorPassWorkspace
      parent={parent}
      pass={pass}
      attentionItems={attentionItems}
      refreshAttention={() => Promise.resolve([])}
      attentionDrafts={emptyAttentionDrafts()}
      setAttentionDrafts={vi.fn()}
    />,
  );
}

describe('RefactorPassWorkspace', () => {
  it('shows a state-true custody strip and the pass inspector like a feature tab', () => {
    installAgenticoMock({ feature: readyChild() });
    const parent = parentWith();
    renderWorkspace(parent, controllerFor(parent, readyChild()));

    const custody = screen.getByRole('list', { name: 'Custody of the work' });
    expect(within(custody).getByText('Published · locked while the pass runs')).toBeVisible();
    expect(within(custody).getByText('Ready to start')).toBeVisible();
    expect(within(custody).getByText('After final review approval')).toBeVisible();

    const inspector = screen.getByRole('complementary', { name: 'Pass inspector' });
    expect(within(inspector).getByRole('heading', { name: 'Slop removal pass' })).toBeVisible();
    expect(within(inspector).getByLabelText('Feature pipeline')).toBeInTheDocument();
  });

  it('explains setup in the stage instead of rendering dead verbs', () => {
    installAgenticoMock({ feature: readyChild() });
    const setupChild = readyChild({
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
    });
    const parent = parentWith({
      status: 'SettingUpWorktrees',
      displayState: 'Active — SettingUpWorktrees',
    });
    const pass = controllerFor(parent, setupChild);
    renderWorkspace(parent, pass);

    expect(
      screen.getByText('Preparing worktrees. Start unlocks when setup completes.'),
    ).toBeVisible();
    expect(pass.actions).toEqual([]);
  });

  it('renders the discard impact projection verbatim and confirms through the controller', async () => {
    installAgenticoMock({ feature: readyChild() });
    const parent = parentWith();
    const pass = controllerFor(parent, readyChild(), { discardOpen: true });
    const user = userEvent.setup();
    renderWorkspace(parent, pass);

    const dialog = screen.getByRole('dialog', { name: /Discard Slop removal pass/ });
    expect(within(dialog).getByText(/Sessions stopped: none/)).toBeVisible();
    expect(within(dialog).getByText('repo-a … pass')).toBeVisible();
    expect(within(dialog).getByText('Review configuration retained')).toBeVisible();
    expect(within(dialog).getByText(/This cannot be undone/)).toBeVisible();

    await user.click(within(dialog).getByRole('button', { name: 'Discard pass' }));
    expect(pass.discard).toHaveBeenCalled();
  });

  it('fails closed when the discard projection is missing', () => {
    installAgenticoMock({ feature: readyChild() });
    const bareChild = readyChild({
      actions: [
        { id: 'start', enabled: true, disabledReasons: [] },
        { id: 'discard', enabled: true, disabledReasons: [] },
      ],
    });
    const parent = parentWith();
    renderWorkspace(parent, controllerFor(parent, bareChild, { discardOpen: true }));

    const dialog = screen.getByRole('dialog', { name: /Discard Slop removal pass/ });
    expect(within(dialog).getByRole('alert')).toHaveTextContent('Impact projection is unavailable');
    expect(within(dialog).getByRole('button', { name: 'Discard pass' })).toBeDisabled();
  });

  it('keeps integration state on the custody strip without a panel or history block', () => {
    installAgenticoMock({ feature: readyChild() });
    const integratingChild = readyChild({
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
    });
    const parent = featureSnapshot({
      id: 'parent1234ef5678',
      name: 'Electron app',
      status: 'Published',
      activeChild: childView({ integrationState: 'attention' }),
      childHistory: [
        childView({
          id: 'child0000ef567890',
          name: 'Earlier pass',
          displayState: 'Closed — Completed',
          outcome: 'completed',
          closedAt: '2026-07-29T10:00:00Z',
        }),
      ],
    });
    renderWorkspace(parent, controllerFor(parent, integratingChild));

    const custody = screen.getByRole('list', { name: 'Custody of the work' });
    expect(within(custody).getByText('Needs attention')).toBeVisible();
    // Integration detail and settled-pass history live off this tab; the main
    // column stays reserved for the live run.
    expect(screen.queryByRole('region', { name: 'Integration' })).not.toBeInTheDocument();
    expect(screen.queryByText('Refactor history')).not.toBeInTheDocument();
  });

  it('explains the lock and pairing on the parent card without duplicating the config verb', () => {
    installAgenticoMock({ feature: readyChild() });
    const parent = parentWith();
    renderWorkspace(parent, controllerFor(parent, readyChild()));

    const parentCard = screen.getByRole('region', { name: 'Parent feature' });
    expect(within(parentCard).getByText(/Locked while the pass runs/)).toBeVisible();
    expect(within(parentCard).getByText(/changes apply to both/i)).toBeVisible();
    // Edit configuration… in the action bar menu is the single config entry.
    expect(within(parentCard).queryByRole('button')).not.toBeInTheDocument();
  });

  it('dispatches an armed auto-start once the child becomes startable', async () => {
    const mock = installAgenticoMock({ feature: readyChild() });
    const { result } = renderHook(() => useRefactorPass(parentWith(), vi.fn()));

    act(() => result.current.armAutoStart(CHILD_ID));
    await waitFor(() =>
      expect(mock.api.dispatchFeatureAction).toHaveBeenCalledWith({
        featureId: CHILD_ID,
        action: 'start',
      }),
    );
    // Fires exactly once even though the snapshot reloads after the dispatch.
    await waitFor(() => expect(result.current.busy).toBe(false));
    expect(mock.api.dispatchFeatureAction).toHaveBeenCalledTimes(1);
  });

  it('holds an armed auto-start while the server still blocks start', async () => {
    const mock = installAgenticoMock({
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
    const { result } = renderHook(() => useRefactorPass(parentWith(), vi.fn()));

    act(() => result.current.armAutoStart(CHILD_ID));
    await waitFor(() => expect(result.current.child).not.toBeNull());
    expect(mock.api.dispatchFeatureAction).not.toHaveBeenCalled();
  });

  it("surfaces the pass session's pending question inline and answerable", () => {
    installAgenticoMock({ feature: readyChild() });
    const parent = parentWith({
      status: 'FinalReviewing',
      displayState: 'Active — FinalReviewing',
    });
    const question: AttentionItem = {
      kind: 'questions',
      id: 'ask-pass',
      featureId: CHILD_ID,
      parentFeatureId: parent.id,
      sessionId: `${CHILD_ID}-fix-01`,
      waitingSince: '2026-08-01T10:00:00.000Z',
      questions: [
        {
          key: 'Which language should the body keep?',
          header: 'Body language',
          multiSelect: false,
          options: [{ label: 'Italian (fork point)' }, { label: 'English (main)' }],
        },
      ],
    };
    renderWorkspace(parent, controllerFor(parent, readyChild()), [question]);

    const request = screen.getByRole('region', { name: 'Agent request' });
    expect(within(request).getByText('Which language should the body keep?')).toBeVisible();
    expect(within(request).getByText('Italian (fork point)')).toBeVisible();
  });

  it('reports loading until the child snapshot arrives', () => {
    installAgenticoMock({ feature: readyChild() });
    const parent = parentWith();
    renderWorkspace(parent, controllerFor(parent, null));

    expect(screen.getByText('Loading the pass from the runtime…')).toBeVisible();
    const inspector = screen.getByRole('complementary', { name: 'Pass inspector' });
    expect(within(inspector).getByText('Active — Created')).toBeVisible();
  });
});
