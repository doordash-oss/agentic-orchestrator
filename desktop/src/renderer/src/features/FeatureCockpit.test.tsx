import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { featureSnapshot, installAgenticoMock } from '../test/agenticoMock';
import { FeatureCockpit } from './FeatureCockpit';

afterEach(cleanup);

const FEATURE_ID = 'abcd1234ef567890';

function renderCockpit(mock = installAgenticoMock()) {
  const onClose = vi.fn();
  const onLoadedName = vi.fn();
  render(
    <FeatureCockpit
      featureId={FEATURE_ID}
      titleHint="Search revamp"
      onClose={onClose}
      onLoadedName={onLoadedName}
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
    expect(within(header!).getByText('SettingUpWorktrees')).toBeInTheDocument();
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
          disabledReasons: [{ code: 'setup_failed', message: 'setup must succeed first' }],
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

  it('disables Start with the server-provided reason while setup is unfinished', async () => {
    const mock = installAgenticoMock();
    mock.api.getFeature.mockResolvedValue(failedSnapshot());
    renderCockpit(mock);
    await screen.findByRole('heading', { name: 'Search revamp' });
    const start = screen.getByRole('button', { name: 'Start' });
    expect(start).toBeDisabled();
    expect(screen.getByText('setup must succeed first')).toBeInTheDocument();
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

  it('labels the feature Ready to start and enables Start from the catalogue', async () => {
    const mock = installAgenticoMock();
    mock.api.getFeature.mockResolvedValue(readySnapshotDetail());
    renderCockpit(mock);
    await screen.findByRole('heading', { name: 'Search revamp' });

    expect(screen.getByText('Ready to start')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Start' })).toBeEnabled();
  });

  it('never invokes any action when Start is clicked in this phase', async () => {
    const mock = installAgenticoMock();
    mock.api.getFeature.mockResolvedValue(readySnapshotDetail());
    renderCockpit(mock);
    const user = userEvent.setup();
    await screen.findByRole('heading', { name: 'Search revamp' });

    const invocations = () =>
      mock.api.dispatchFeatureSetup.mock.calls.length + mock.api.createFeature.mock.calls.length;
    const before = invocations();
    await user.click(screen.getByRole('button', { name: 'Start' }));
    expect(invocations()).toBe(before);
    expect(
      screen.getByText("Nothing was started — starting isn't available yet."),
    ).toBeInTheDocument();
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
});
