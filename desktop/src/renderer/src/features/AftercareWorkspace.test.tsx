import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { RunDetailView } from '../../../shared/ipc';
import { featureSnapshot } from '../test/agenticoMock';
import { AftercareWorkspace } from './AftercareWorkspace';

afterEach(cleanup);

const completedRun: RunDetailView = {
  runNumber: 8,
  artifactCount: 5,
  timing: { totalSeconds: 14700, byPhase: {} },
  cost: { totalUsd: 95.18, byPhase: {} },
};

describe('AftercareWorkspace', () => {
  it('shows CodeReady publication first and keeps only useful facts', () => {
    render(
      <AftercareWorkspace
        snapshot={featureSnapshot({
          name: 'Do not repeat me',
          status: 'CodeReady',
          activeRun: 8,
          actions: [
            { id: 'rebase', enabled: true, disabledReasons: [] },
            { id: 'publish', enabled: true, disabledReasons: [] },
          ],
        })}
        run={completedRun}
        onAction={vi.fn()}
        onOpenRunRecord={vi.fn()}
        onOpenChanges={vi.fn()}
        onOpenPullRequest={vi.fn()}
      />,
    );

    expect(screen.getByRole('heading', { name: 'Implementation complete.' })).toBeVisible();
    expect(screen.getAllByRole('button').map((button) => button.textContent)).toContainEqual(
      expect.stringContaining('Prepare publish'),
    );
    expect(screen.getByText('$95.18')).toBeVisible();
    expect(screen.queryByText('Durable setup')).not.toBeInTheDocument();
    expect(screen.queryByText('Do not repeat me')).not.toBeInTheDocument();
  });

  it('includes active and completed aftercare pass costs in the feature total', () => {
    render(
      <AftercareWorkspace
        snapshot={featureSnapshot({
          status: 'CodeReady',
          activeChild: {
            id: 'childactive567890',
            name: 'Active review feedback pass',
            kind: 'review-feedback',
            displayToken: 'review-feedback:childactive567890',
            displayState: 'Active',
            pipeline: 'medium',
            status: 'Implementing',
            relationshipState: 'active',
            startedAt: '2026-08-01T10:00:00Z',
            cost: { totalUsd: 0.32, byPhase: {} },
            integrationState: 'pending',
            attention: [],
            cleanupWarnings: [],
          },
          childHistory: [
            {
              id: 'childclosed567890',
              name: 'Completed refactor pass',
              kind: 'refactor',
              displayToken: 'refactor:childclosed567890',
              displayState: 'Closed — Completed',
              pipeline: 'large',
              status: 'Done',
              relationshipState: 'closed',
              startedAt: '2026-07-28T10:00:00Z',
              closedAt: '2026-07-29T10:00:00Z',
              outcome: 'completed',
              cost: { totalUsd: 1.25, byPhase: {} },
              integrationState: 'merged',
              attention: [],
              cleanupWarnings: [],
            },
          ],
        })}
        run={completedRun}
        onAction={vi.fn()}
        onOpenRunRecord={vi.fn()}
        onOpenChanges={vi.fn()}
        onOpenPullRequest={vi.fn()}
      />,
    );

    expect(screen.getByText('$96.75')).toBeVisible();
  });

  it('omits Publish after publication and routes compact archive actions', async () => {
    const onAction = vi.fn();
    const onOpenRunRecord = vi.fn();
    const onOpenChanges = vi.fn();
    const onOpenPullRequest = vi.fn();
    const user = userEvent.setup();
    render(
      <AftercareWorkspace
        snapshot={featureSnapshot({
          status: 'Published',
          actions: [{ id: 'refactor', enabled: true, disabledReasons: [] }],
          repoStatus: [
            {
              name: 'agentic-orchestrator',
              publishable: true,
              prUrl: 'https://github.com/doordash-oss/agentic-orchestrator/pull/107',
            },
          ],
        })}
        run={completedRun}
        onAction={onAction}
        onOpenRunRecord={onOpenRunRecord}
        onOpenChanges={onOpenChanges}
        onOpenPullRequest={onOpenPullRequest}
      />,
    );

    expect(screen.queryByText('Prepare publish')).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Run record' }));
    await user.click(screen.getByRole('button', { name: 'Changes' }));
    await user.click(screen.getByRole('button', { name: /Start a refactor pass/ }));
    await user.click(screen.getByRole('button', { name: 'Open pull request' }));
    expect(onOpenRunRecord).toHaveBeenCalledOnce();
    expect(onOpenChanges).toHaveBeenCalledOnce();
    expect(onAction).toHaveBeenCalledWith(expect.objectContaining({ id: 'refactor' }));
    expect(onOpenPullRequest).toHaveBeenCalledWith(
      'https://github.com/doordash-oss/agentic-orchestrator/pull/107',
    );
  });

  it('keeps settled passes as read-only history with the preserved diff', async () => {
    const user = userEvent.setup();
    render(
      <AftercareWorkspace
        snapshot={featureSnapshot({
          status: 'Published',
          actions: [],
          childHistory: [
            {
              id: 'child0000ef567890',
              name: 'Earlier pass',
              kind: 'refactor',
              displayToken: 'refactor:child0000ef567890',
              displayState: 'Closed — Completed',
              pipeline: 'large',
              status: 'Done',
              relationshipState: 'closed',
              startedAt: '2026-07-28T10:00:00Z',
              closedAt: '2026-07-29T10:00:00Z',
              outcome: 'completed',
              cost: { totalUsd: 1.25, byPhase: {} },
              integrationState: 'merged',
              attention: [],
              cleanupWarnings: [],
              diffSummary: 'Repository: repo-a\n3 files changed',
            },
          ],
        })}
        run={completedRun}
        onAction={vi.fn()}
        onOpenRunRecord={vi.fn()}
        onOpenChanges={vi.fn()}
        onOpenPullRequest={vi.fn()}
      />,
    );

    await user.click(screen.getByText('Pass history'));
    expect(screen.getByText('Closed — Completed')).toBeVisible();
    await user.click(screen.getByText('Preserved diff (read-only)'));
    expect(screen.getByText(/3 files changed/)).toBeVisible();
    const history = screen.getByText('Earlier pass').closest('li');
    expect(history).not.toBeNull();
    expect(within(history as HTMLElement).queryByRole('button')).not.toBeInTheDocument();
  });

  it('shows kind per entry in a mixed-kind pass history', async () => {
    const user = userEvent.setup();
    render(
      <AftercareWorkspace
        snapshot={featureSnapshot({
          status: 'Published',
          actions: [],
          childHistory: [
            {
              id: 'child0000ef567890',
              name: 'Earlier refactor pass',
              kind: 'refactor',
              displayToken: 'refactor:child0000ef567890',
              displayState: 'Closed — Completed',
              pipeline: 'large',
              status: 'Done',
              relationshipState: 'closed',
              startedAt: '2026-07-28T10:00:00Z',
              closedAt: '2026-07-29T10:00:00Z',
              outcome: 'completed',
              cost: { totalUsd: 1.25, byPhase: {} },
              integrationState: 'merged',
              attention: [],
              cleanupWarnings: [],
            },
            {
              id: 'child1111ef567890',
              name: 'Earlier review-feedback pass',
              kind: 'review-feedback',
              displayToken: 'review-feedback:child1111ef567890',
              displayState: 'Closed — Completed',
              pipeline: 'medium',
              status: 'Done',
              relationshipState: 'closed',
              startedAt: '2026-07-30T10:00:00Z',
              closedAt: '2026-07-31T10:00:00Z',
              outcome: 'completed',
              cost: { totalUsd: 0.5, byPhase: {} },
              integrationState: 'merged',
              attention: [],
              cleanupWarnings: [],
            },
          ],
        })}
        run={completedRun}
        onAction={vi.fn()}
        onOpenRunRecord={vi.fn()}
        onOpenChanges={vi.fn()}
        onOpenPullRequest={vi.fn()}
      />,
    );

    await user.click(screen.getByText('Pass history'));
    const refactorEntry = screen.getByText('Earlier refactor pass').closest('li');
    expect(refactorEntry).not.toBeNull();
    expect(within(refactorEntry as HTMLElement).getByText('Refactor')).toBeVisible();

    const rfEntry = screen.getByText('Earlier review-feedback pass').closest('li');
    expect(rfEntry).not.toBeNull();
    expect(within(rfEntry as HTMLElement).getByText('Review feedback')).toBeVisible();
  });

  it('surfaces review-feedback tail warnings on a closed entry', async () => {
    const user = userEvent.setup();
    render(
      <AftercareWorkspace
        snapshot={featureSnapshot({
          status: 'Published',
          actions: [],
          childHistory: [
            {
              id: 'child2222ef567890',
              name: 'Review feedback pass with tail issue',
              kind: 'review-feedback',
              displayToken: 'review-feedback:child2222ef567890',
              displayState: 'Closed — Completed',
              pipeline: 'medium',
              status: 'Done',
              relationshipState: 'closed',
              startedAt: '2026-07-30T10:00:00Z',
              closedAt: '2026-07-31T10:00:00Z',
              outcome: 'completed',
              cost: { totalUsd: 0.5, byPhase: {} },
              integrationState: 'merged',
              attention: [],
              cleanupWarnings: [
                {
                  message: 'review-feedback tail: no PR URL for review-feedback tail',
                  repo: 'org/repo-a',
                },
              ],
            },
          ],
        })}
        run={completedRun}
        onAction={vi.fn()}
        onOpenRunRecord={vi.fn()}
        onOpenChanges={vi.fn()}
        onOpenPullRequest={vi.fn()}
      />,
    );

    await user.click(screen.getByText('Pass history'));
    expect(screen.getByText(/review-feedback tail: no PR URL/)).toBeVisible();
  });

  it('surfaces undelivered work on the runway and in the facts rail', () => {
    render(
      <AftercareWorkspace
        snapshot={featureSnapshot({ status: 'CodeReady', activeRun: 8, actions: [] })}
        run={completedRun}
        pending={{ publishRepos: [{ repo: 'api', commits: 3, dirty: false }], mergeRepos: [] }}
        onAction={vi.fn()}
        onOpenRunRecord={vi.fn()}
        onOpenChanges={vi.fn()}
        onOpenPullRequest={vi.fn()}
      />,
    );

    expect(screen.getByRole('button', { name: /Publish new commits/ })).toBeVisible();
    const facts = screen.getByRole('complementary', { name: 'Feature facts' });
    expect(facts).toHaveTextContent('Unpublished');
    expect(facts).toHaveTextContent('3 commits');
  });
});
