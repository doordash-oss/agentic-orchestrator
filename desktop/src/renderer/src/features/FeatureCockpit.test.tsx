import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AttentionItem } from '../../../shared/ipc';
import { featureSnapshot, installAgenticoMock } from '../test/agenticoMock';
import { dispatchMediaChange, matchMediaState } from '../test/setup';
import { emptyAttentionDrafts } from './AttentionInbox';
import { FeatureCockpit } from './FeatureCockpit';

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

  it('shows the server-owned setup task order, status, repo, and attempt', async () => {
    renderCockpit();
    await screen.findByRole('heading', { name: 'Search revamp' });

    const setup = screen.getByRole('region', { name: 'Durable setup' });
    const rows = within(setup).getAllByRole('listitem');
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent('Create worktree');
    expect(rows[0]).toHaveTextContent('Done');
    expect(rows[0]).toHaveTextContent('repo-a');
    expect(rows[0]).toHaveTextContent('feature/search-revamp');
    expect(rows[1]).toHaveTextContent('Build knowledge base');
    expect(rows[1]).toHaveTextContent('Running');
    expect(setup).toHaveTextContent('1 of 2 tasks complete');
  });

  it('renders the mono header facts: status, branch, repositories', async () => {
    renderCockpit();
    await screen.findByRole('heading', { name: 'Search revamp' });
    const header = screen.getByText('Status').closest('dl');
    expect(header).not.toBeNull();
    expect(within(header!).getByLabelText('SettingUpWorktrees')).toBeInTheDocument();
    expect(within(header!).getByText('feature/search-revamp')).toBeInTheDocument();
    expect(within(header!).getByText('repo-a')).toBeInTheDocument();
  });

  it('shows the feature pipeline spine with the needle on Setup during setup', async () => {
    renderCockpit();
    await screen.findByRole('heading', { name: 'Search revamp' });
    const spine = screen.getByRole('group', { name: 'Feature pipeline' });
    const active = within(spine)
      .getAllByRole('listitem')
      .find((item) => item.getAttribute('aria-current') === 'step');
    expect(active).toHaveTextContent('Setup');
  });

  it('keeps rewind and run history in the feature action bar', async () => {
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
    expect(within(actions).getByRole('button', { name: 'Rewind feature' })).toBeVisible();
    expect(within(actions).getByRole('button', { name: 'View run history' })).toBeVisible();
    expect(mock.api.getFeature).toHaveBeenCalledWith(FEATURE_ID);
  });

  it('opens the completion workspace from server-advertised completion actions', async () => {
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

    await user.click(await screen.findByRole('button', { name: 'Open completion workspace' }));

    expect(await screen.findByRole('heading', { name: 'Completion' })).toBeInTheDocument();
    expect(mock.api.preflightCompletion).toHaveBeenCalledWith({ featureId: FEATURE_ID });
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

  it('keeps the feature visible with completed and failed tasks plus diagnostics', async () => {
    const mock = installAgenticoMock();
    mock.api.getFeature.mockResolvedValue(failedSnapshot());
    renderCockpit(mock);
    await screen.findByRole('heading', { name: 'Search revamp' });

    expect(screen.getByText('Setup failed in repo-a.')).toBeInTheDocument();
    const setup = screen.getByRole('region', { name: 'Durable setup' });
    expect(setup).toHaveTextContent('1 of 2 tasks complete — setup failed');
    expect(setup).toHaveTextContent('Done');
    expect(setup).toHaveTextContent('Failed');
    expect(setup).toHaveTextContent('kb build exited with status 1');
    // Diagnostics detail is expandable, not hidden.
    expect(setup).toHaveTextContent('Retry re-runs only unfinished tasks');
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
    await screen.findByRole('heading', { name: 'Search revamp' });
    expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled();
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
      ],
    });
  }

  it('offers Start only from the authoritative action catalogue', async () => {
    const mock = installAgenticoMock();
    mock.api.getFeature.mockResolvedValue(readySnapshotDetail());
    (
      mock.api as unknown as { dispatchFeatureAction: ReturnType<typeof vi.fn> }
    ).dispatchFeatureAction = vi.fn(() => new Promise(() => {}));
    renderCockpit(mock);
    const user = userEvent.setup();
    await screen.findByRole('heading', { name: 'Search revamp' });

    expect(screen.getByText('Ready to start')).toBeInTheDocument();
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

  it('saves inline gate drafts without disabling an immediate abort click', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Implementing',
        currentPhase: 'Implement',
        setup: { status: 'done', attempt: 1, tasks: [] },
        actions: [],
      }),
    });
    let resolveDraft!: () => void;
    mock.api.saveGateDraft.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveDraft = () => resolve({ result: 'drafted' });
        }),
    );
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
        />
      );
    }
    render(<Harness />);
    const user = userEvent.setup();

    const inlineAttention = await screen.findByRole('region', { name: 'Feature attention' });
    await user.type(
      within(inlineAttention).getByLabelText(/Which deployment window should implementation use/),
      'After packaged attention evidence passes.',
    );
    await user.click(within(inlineAttention).getByRole('button', { name: 'Abort gate' }));

    expect(mock.api.saveGateDraft).toHaveBeenCalledWith({
      featureId: FEATURE_ID,
      answers: {
        'Which deployment window should implementation use?':
          'After packaged attention evidence passes.',
      },
    });
    expect(screen.getByRole('dialog', { name: 'Confirm abort' })).toBeVisible();
    expect(mock.api.resolveGate).not.toHaveBeenCalled();

    resolveDraft();
    await waitFor(() => expect(screen.getByText('Draft saved.')).toBeVisible());
  });

  it('sends a help reply from the inline feature attention region', async () => {
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

    const inlineAttention = await screen.findByRole('region', { name: 'Feature attention' });
    await user.type(
      within(inlineAttention).getByLabelText('Help reply'),
      'Continue with the cockpit evidence path.',
    );
    await user.click(within(inlineAttention).getByRole('button', { name: 'Send reply' }));

    expect(mock.api.sendHelp).toHaveBeenCalledWith({
      featureId: FEATURE_ID,
      sessionId: 'help-session',
      message: 'Continue with the cockpit evidence path.',
    });
    await waitFor(() =>
      expect(screen.queryByRole('region', { name: 'Feature attention' })).not.toBeInTheDocument(),
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
  it('deletes the feature after confirmation and closes the tab', async () => {
    const mock = installAgenticoMock({
      feature: featureSnapshot({
        status: 'Created',
        actions: [{ id: 'delete', enabled: true, disabledReasons: [] }],
      }),
    });
    const { onClose } = renderCockpit(mock);
    const user = userEvent.setup();

    await user.click(await screen.findByRole('button', { name: 'Delete feature' }));
    const dialog = await screen.findByRole('dialog', { name: /Delete Search revamp/ });
    await user.click(within(dialog).getByRole('button', { name: 'Delete feature' }));

    await waitFor(() =>
      expect(mock.api.dispatchFeatureAction).toHaveBeenCalledWith({
        featureId: FEATURE_ID,
        action: 'delete',
        body: {},
      }),
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
    expect(await screen.findByRole('button', { name: 'Delete feature' })).toBeDisabled();
  });
});
