import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type {
  CompletionPreflightResult,
  RelationshipChildView,
  RunDetailView,
} from '../../../shared/ipc';
import { featureSnapshot } from '../test/agenticoMock';
import { AftercareWorkspace, type AftercareWorkspaceProps } from './AftercareWorkspace';
import type { AftercareEvidence } from './useAftercareEvidence';

afterEach(cleanup);

const completedRun: RunDetailView = {
  runNumber: 8,
  artifactCount: 5,
  timing: { totalSeconds: 14700, byPhase: { Implement: 5400 } },
  cost: { totalUsd: 95.18, byPhase: {} },
};

const closedPass: RelationshipChildView = {
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
};

const diffEvidence: AftercareEvidence = {
  diffs: [
    {
      featureId: 'abcd1234ef567890',
      repo: 'agentic-orchestrator',
      files: [
        { path: 'a.ts', operation: 'modify', addedLines: 30, removedLines: 10 },
        { path: 'b.ts', operation: 'add', addedLines: 70 },
      ],
    },
  ],
  reviewFeedback: null,
};

function renderWorkspace(props: Partial<AftercareWorkspaceProps> = {}): AftercareWorkspaceProps {
  const merged: AftercareWorkspaceProps = {
    snapshot: featureSnapshot({ status: 'Published', activeRun: 8, actions: [] }),
    run: completedRun,
    onAction: vi.fn(),
    onOpenRunRecord: vi.fn(),
    onOpenChanges: vi.fn(),
    onOpenConfiguration: vi.fn(),
    onOpenPullRequest: vi.fn(),
    ...props,
  };
  render(<AftercareWorkspace {...merged} />);
  return merged;
}

describe('AftercareWorkspace runway', () => {
  it('states the situation per status without an eyebrow or constraint chip', () => {
    renderWorkspace({
      snapshot: featureSnapshot({
        name: 'Do not repeat me',
        status: 'CodeReady',
        activeRun: 8,
        actions: [
          { id: 'rebase', enabled: true, disabledReasons: [] },
          { id: 'publish', enabled: true, disabledReasons: [] },
        ],
      }),
    });

    expect(screen.getByRole('heading', { name: 'The work is ready to go out' })).toBeVisible();
    expect(
      screen.getByText(
        'Run #8 finished 4h 05m of work. Pick one follow-up, or leave the run at rest.',
      ),
    ).toBeVisible();
    // The uppercase eyebrow and the constraint chip die with the uppercase voice.
    expect(screen.queryByText(/Aftercare ·/)).not.toBeInTheDocument();
    expect(document.querySelector('.aftercare-workspace__constraint')).toBeNull();
    expect(screen.queryByText('Do not repeat me')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Publish this feature/ })).toHaveTextContent(
      'Prepare publish',
    );
  });

  it('uses the published headline and copy for a published feature', () => {
    renderWorkspace();
    expect(screen.getByRole('heading', { name: 'The work is in service' })).toBeVisible();
    expect(
      screen.getByText(
        'Run #8 published 4h 05m of work. Pick one follow-up, or leave the feature at rest.',
      ),
    ).toBeVisible();
  });

  it('keeps the Done lede distinction', () => {
    renderWorkspace({
      snapshot: featureSnapshot({ status: 'Done', activeRun: 8, actions: [] }),
    });
    expect(screen.getByRole('heading', { name: 'This feature is closed out' })).toBeVisible();
    expect(
      screen.getByText(/The record stays available whenever another focused pass/),
    ).toBeVisible();
  });

  it('drops the duration clause when no timing is carried', () => {
    renderWorkspace({ run: null });
    expect(screen.getByText(/^Run #8 is published\./)).toBeVisible();
  });

  it('renders each action as an unnumbered row with a leading symbol', () => {
    renderWorkspace({
      snapshot: featureSnapshot({
        status: 'Published',
        actions: [
          { id: 'rebase', enabled: true, disabledReasons: [] },
          { id: 'refactor', enabled: true, disabledReasons: [] },
        ],
      }),
    });

    const rows = screen
      .getAllByRole('listitem')
      .filter((li) => li.classList.contains('aftercare-workspace__row'));
    expect(rows).toHaveLength(2);
    for (const row of rows) {
      expect(row.querySelector('svg.aftercare-workspace__symbol')).not.toBeNull();
      expect(row.textContent).not.toMatch(/\b0[123]\b/);
    }
    expect(screen.getByRole('button', { name: /Bring branches up to date/ })).toHaveTextContent(
      'Start rebase pass',
    );
  });

  it('shows a blocked pass with its reason and no launch affordance', () => {
    renderWorkspace({
      snapshot: featureSnapshot({
        status: 'CodeReady',
        activeRun: 8,
        actions: [
          {
            id: 'refactor',
            enabled: false,
            disabledReasons: [
              { code: 'dirty_parent', message: 'worktree has uncommitted changes' },
            ],
          },
        ],
      }),
    });
    const row = screen.getByRole('button', { name: /Start a refactor pass/ });
    expect(row).toBeDisabled();
    expect(row).toHaveTextContent('worktree has uncommitted changes');
    expect(row.querySelector('.aftercare-workspace__action-blocked')).not.toBeNull();
    expect(row.querySelector('.aftercare-workspace__action-label')).toBeNull();
  });

  it('explains an unverified worktree state on a blocked pass', () => {
    renderWorkspace({
      snapshot: featureSnapshot({
        status: 'CodeReady',
        activeRun: 8,
        actions: [
          {
            id: 'refactor',
            enabled: false,
            disabledReasons: [
              {
                code: 'worktree_state_unknown',
                message: 'worktree state could not be determined',
              },
            ],
          },
        ],
      }),
    });
    const row = screen.getByRole('button', { name: /Start a refactor pass/ });
    expect(row).toBeDisabled();
    expect(row).toHaveTextContent(
      'Could not read the repository worktrees — check that they still exist and are a valid checkout.',
    );
  });

  it('renders the busy label while a one-click launch is dispatching', () => {
    renderWorkspace({
      snapshot: featureSnapshot({
        status: 'Published',
        actions: [{ id: 'rebase', enabled: true, disabledReasons: [] }],
      }),
      busyAction: { id: 'rebase', label: 'Starting rebase pass' },
    });
    const row = screen.getByRole('button', { name: /Bring branches up to date/ });
    expect(row).toBeDisabled();
    expect(row).toHaveAttribute('aria-busy', 'true');
    expect(row).toHaveTextContent('Starting rebase pass…');
  });

  it('keeps the action-error alert and the empty state', () => {
    renderWorkspace({
      snapshot: featureSnapshot({ status: 'Published', actions: [] }),
      actionError: {
        action: 'Rebase',
        error: { code: 'rebase_already_up_to_date', message: 'Nothing to merge.' },
      },
    });
    expect(screen.getByRole('alert')).toHaveTextContent('Already up to date: Nothing to merge.');
    expect(screen.getByText('No action is needed right now.')).toBeVisible();
  });

  it('offers the first local merge as a runway row', async () => {
    const user = userEvent.setup();
    const props = renderWorkspace({
      snapshot: featureSnapshot({ status: 'CodeReady', actions: [] }),
      pending: {
        publishRepos: [],
        mergeRepos: [],
        initialMergeRepos: [
          { repo: 'local-core', commits: 0, dirty: false, dirtyFiles: [], dirtyFileTotal: 0 },
        ],
        publishEligibleRepos: [],
      },
    });
    await user.click(screen.getByRole('button', { name: /Merge this feature/ }));
    expect(props.onAction).toHaveBeenCalledWith(expect.objectContaining({ id: 'merge' }));
  });

  it('surfaces undelivered publish work on the runway', () => {
    renderWorkspace({
      snapshot: featureSnapshot({ status: 'CodeReady', activeRun: 8, actions: [] }),
      pending: {
        publishRepos: [
          { repo: 'api', commits: 3, dirty: false, dirtyFiles: [], dirtyFileTotal: 0 },
        ],
        mergeRepos: [],
        initialMergeRepos: [],
        publishEligibleRepos: [],
      },
    });
    expect(screen.getByRole('button', { name: /Publish new commits/ })).toBeVisible();
  });

  it('no longer renders the always-open facts rail or the footer link nav', () => {
    renderWorkspace();
    expect(screen.queryByRole('complementary', { name: 'Feature facts' })).not.toBeInTheDocument();
    expect(
      screen.queryByRole('navigation', { name: 'Completed run resources' }),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Run record' })).not.toBeInTheDocument();
  });
});

describe('AftercareWorkspace pass history', () => {
  it('keeps settled passes as read-only history with the preserved diff', async () => {
    const user = userEvent.setup();
    renderWorkspace({
      snapshot: featureSnapshot({
        status: 'Published',
        actions: [],
        childHistory: [{ ...closedPass, diffSummary: 'Repository: repo-a\n3 files changed' }],
      }),
    });

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
    renderWorkspace({
      snapshot: featureSnapshot({
        status: 'Published',
        actions: [],
        childHistory: [
          { ...closedPass, name: 'Earlier refactor pass' },
          {
            ...closedPass,
            id: 'child1111ef567890',
            name: 'Earlier review-feedback pass',
            kind: 'review-feedback',
            displayToken: 'review-feedback:child1111ef567890',
          },
        ],
      }),
    });

    await user.click(screen.getByText('Pass history'));
    const refactorEntry = screen.getByText('Earlier refactor pass').closest('li');
    expect(within(refactorEntry as HTMLElement).getByText('Refactor')).toBeVisible();
    const rfEntry = screen.getByText('Earlier review-feedback pass').closest('li');
    expect(within(rfEntry as HTMLElement).getByText('Review feedback')).toBeVisible();
  });

  it('surfaces review-feedback tail warnings on a closed entry', async () => {
    const user = userEvent.setup();
    renderWorkspace({
      snapshot: featureSnapshot({
        status: 'Published',
        actions: [],
        childHistory: [
          {
            ...closedPass,
            kind: 'review-feedback',
            cleanupWarnings: [
              {
                message: 'review-feedback tail: no PR URL for review-feedback tail',
                repo: 'org/repo-a',
              },
            ],
          },
        ],
      }),
    });

    await user.click(screen.getByText('Pass history'));
    expect(screen.getByText(/review-feedback tail: no PR URL/)).toBeVisible();
  });

  it('loads a list-projected diff body on demand', async () => {
    const user = userEvent.setup();
    const onLoadFullChildHistory = vi
      .fn()
      .mockResolvedValue([{ ...closedPass, hasDiffSummary: true, diffSummary: '3 files changed' }]);
    renderWorkspace({
      snapshot: featureSnapshot({
        status: 'Published',
        actions: [],
        childHistory: [{ ...closedPass, hasDiffSummary: true }],
      }),
      onLoadFullChildHistory,
    });

    await user.click(screen.getByText('Pass history'));
    await user.click(screen.getByText('Preserved diff (read-only)'));
    await user.click(screen.getByRole('button', { name: 'Load diff' }));
    expect(onLoadFullChildHistory).toHaveBeenCalledTimes(1);
    expect(await screen.findByText(/3 files changed/)).toBeVisible();
  });

  it('never lets a bounded history read as the whole record', async () => {
    const user = userEvent.setup();
    renderWorkspace({
      snapshot: featureSnapshot({
        status: 'Published',
        actions: [],
        childHistory: [closedPass],
        childHistoryTotal: 12,
        childHistoryTruncated: true,
      }),
    });

    await user.click(screen.getByText('Pass history'));
    expect(screen.getByText('1 of 12')).toBeVisible();
  });
});

describe('AftercareWorkspace What shipped', () => {
  const publishedPreflight: CompletionPreflightResult = {
    featureId: 'abcd1234ef567890',
    sourceRevision: 'rev',
    canMarkDone: true,
    repos: [
      {
        repo: 'agentic-orchestrator',
        publishable: true,
        touched: true,
        status: 'already_published',
        prUrl: 'https://github.com/doordash-oss/agentic-orchestrator/pull/107',
        pendingCommits: 2,
      },
    ],
  };

  const publishedSnapshot = () =>
    featureSnapshot({
      status: 'Published',
      activeRun: 8,
      actions: [],
      pipeline: 'medium',
      repoStatus: [
        {
          name: 'agentic-orchestrator',
          publishable: true,
          prUrl: 'https://github.com/doordash-oss/agentic-orchestrator/pull/107',
          freshness: 'in sync',
        },
      ],
      verificationItems: [
        { name: 'npm run check', state: 'passed' },
        { name: 'npm test', state: 'passed' },
      ],
    });

  it('renders every carried fact and routes each row to its surface', async () => {
    const user = userEvent.setup();
    const props = renderWorkspace({
      snapshot: publishedSnapshot(),
      preflight: publishedPreflight,
      evidence: {
        ...diffEvidence,
        reviewFeedback: {
          featureId: 'abcd1234ef567890',
          repos: [
            {
              repo: 'agentic-orchestrator',
              prUrl: 'https://github.com/doordash-oss/agentic-orchestrator/pull/107',
              comments: [{ repo: 'agentic-orchestrator', id: 1, type: 'review' }],
            },
          ],
        },
      },
    });

    const shipped = screen.getByRole('region', { name: 'What shipped' });
    expect(within(shipped).getByText('2 files')).toBeVisible();
    expect(within(shipped).getByText('+100')).toBeVisible();
    expect(within(shipped).getByText('−10')).toBeVisible();
    expect(within(shipped).getByText('2 commits not delivered yet')).toBeVisible();
    expect(within(shipped).getByText('2 of 2 checks passed')).toBeVisible();
    expect(within(shipped).getByText('npm run check · npm test')).toBeVisible();
    expect(within(shipped).getByText('#107')).toBeVisible();
    expect(
      within(shipped).getByText('Published from this run · in sync · 1 unresolved comment'),
    ).toBeVisible();
    expect(within(shipped).getByRole('group', { name: 'Phases run' })).toBeVisible();

    await user.click(within(shipped).getByRole('button', { name: 'View changes' }));
    await user.click(within(shipped).getByRole('button', { name: 'View run record' }));
    await user.click(within(shipped).getByRole('button', { name: /Open on GitHub/ }));
    await user.click(within(shipped).getByRole('button', { name: 'Configuration' }));
    expect(props.onOpenChanges).toHaveBeenCalledOnce();
    expect(props.onOpenRunRecord).toHaveBeenCalledOnce();
    expect(props.onOpenConfiguration).toHaveBeenCalledOnce();
    expect(props.onOpenPullRequest).toHaveBeenCalledWith(
      'https://github.com/doordash-oss/agentic-orchestrator/pull/107',
    );
  });

  it('renders the +/− bar proportional to the aggregated totals', () => {
    renderWorkspace({ snapshot: publishedSnapshot(), evidence: diffEvidence });
    const added = document.querySelector<HTMLElement>('.aftercare-shipped__bar-added');
    const removed = document.querySelector<HTMLElement>('.aftercare-shipped__bar-removed');
    expect(added?.style.width).toBe('91%');
    expect(removed?.style.width).toBe('9%');
    expect(document.querySelectorAll('.aftercare-shipped__bar')).toHaveLength(1);
  });

  it('omits the Changes and pull-request rows and claims nothing about checks', () => {
    renderWorkspace({
      snapshot: featureSnapshot({ status: 'Published', activeRun: 8, actions: [] }),
    });

    const shipped = screen.getByRole('region', { name: 'What shipped' });
    expect(within(shipped).queryByText('Changes')).not.toBeInTheDocument();
    expect(within(shipped).queryByText('Pull request')).not.toBeInTheDocument();
    expect(within(shipped).getByText('Check results stay in the run record')).toBeVisible();
    expect(within(shipped).getByRole('button', { name: 'View run record' })).toBeVisible();
    expect(shipped.textContent).not.toMatch(/approval|rewind/i);
  });

  it('labels one pull-request row per repository when the feature spans several', () => {
    renderWorkspace({
      snapshot: featureSnapshot({
        status: 'Published',
        activeRun: 8,
        actions: [],
        repos: ['api', 'web'],
        repoStatus: [
          { name: 'api', publishable: true, prUrl: 'https://example.test/api/pull/12' },
          { name: 'web', publishable: true, prUrl: 'https://example.test/web/pull/34' },
        ],
      }),
    });
    const shipped = screen.getByRole('region', { name: 'What shipped' });
    expect(within(shipped).getByText('Pull request · api')).toBeVisible();
    expect(within(shipped).getByText('Pull request · web')).toBeVisible();
    expect(within(shipped).getByText('#12')).toBeVisible();
    expect(within(shipped).getByText('#34')).toBeVisible();
  });

  it('omits the unresolved-comment clause when the review-feedback fetch failed', () => {
    renderWorkspace({
      snapshot: publishedSnapshot(),
      preflight: publishedPreflight,
      evidence: { diffs: [], reviewFeedback: null },
    });
    const shipped = screen.getByRole('region', { name: 'What shipped' });
    expect(within(shipped).getByText('Published from this run · in sync')).toBeVisible();
    expect(shipped.textContent).not.toMatch(/unresolved/);
  });
});
