/*
Copyright 2026 DoorDash, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { RunDetailView } from '../../../shared/ipc';
import { featureSnapshot } from '../test/agenticoMock';
import { AftercareFacts } from './AftercareFacts';

afterEach(cleanup);

const completedRun: RunDetailView = {
  runNumber: 8,
  artifactCount: 5,
  timing: { totalSeconds: 14700, byPhase: {} },
  cost: { totalUsd: 95.18, byPhase: {} },
};

describe('AftercareFacts', () => {
  it('carries every fact the rail showed, computed identically', () => {
    render(
      <AftercareFacts
        snapshot={featureSnapshot({ status: 'Published', activeRun: 8 })}
        run={completedRun}
        onOpenPullRequest={vi.fn()}
      />,
    );
    const facts = screen.getByRole('region', { name: 'Feature facts' });
    expect(facts).toHaveTextContent('Published');
    expect(facts).toHaveTextContent('#8');
    expect(facts).toHaveTextContent('4h 05m');
    expect(facts).toHaveTextContent('$95.18');
    expect(facts).toHaveTextContent('Freshness');
  });

  it('adds active and completed pass costs to the run total', () => {
    render(
      <AftercareFacts
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
            warnings: [],
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
              warnings: [],
            },
          ],
        })}
        run={completedRun}
        onOpenPullRequest={vi.fn()}
      />,
    );
    expect(screen.getByText('$96.75')).toBeVisible();
  });

  it('opens the top pull request, counts PRs across repositories, and shows the pending-delivery fact', async () => {
    const user = userEvent.setup();
    const onOpenPullRequest = vi.fn();
    render(
      <AftercareFacts
        snapshot={featureSnapshot({
          status: 'CodeReady',
          repoStatus: [
            {
              name: 'agentic-orchestrator',
              publishable: true,
              pullRequests: [
                {
                  position: 1,
                  title: 'Phase 8 stack read model',
                  branch: 'feature/x/1-bootstrap',
                  url: 'https://github.com/doordash-oss/agentic-orchestrator/pull/107',
                  state: 'open',
                  noCommits: false,
                  pushedUpToDate: true,
                },
                {
                  position: 2,
                  title: 'Empty layer',
                  state: 'none',
                  noCommits: true,
                  pushedUpToDate: false,
                },
                {
                  position: 3,
                  title: 'Search revamp',
                  url: 'https://github.com/doordash-oss/agentic-orchestrator/pull/109',
                  state: 'open',
                  noCommits: false,
                  pushedUpToDate: true,
                },
              ],
              freshness: 'in sync',
            },
            {
              name: 'web',
              publishable: true,
              pullRequests: [
                {
                  position: 1,
                  title: 'Web revamp',
                  url: 'https://github.com/x/web/pull/12',
                  state: 'open',
                  noCommits: false,
                  pushedUpToDate: true,
                },
              ],
            },
          ],
        })}
        run={completedRun}
        pendingFact={{ label: 'Unpublished', value: '3 commits' }}
        onOpenPullRequest={onOpenPullRequest}
      />,
    );
    const facts = screen.getByRole('region', { name: 'Feature facts' });
    expect(facts).toHaveTextContent('Unpublished');
    expect(facts).toHaveTextContent('3 commits');
    expect(facts).toHaveTextContent('In sync');
    // The headline link is the highest layer's PR of the first repository
    // that has one, never a lower layer's.
    await user.click(screen.getByRole('button', { name: 'Open pull request' }));
    expect(onOpenPullRequest).toHaveBeenCalledWith(
      'https://github.com/doordash-oss/agentic-orchestrator/pull/109',
    );
    expect(facts).toHaveTextContent('3 pull requests across 2 repositories');
  });

  it('renders no pull-request fact when no repository has a PR', () => {
    render(
      <AftercareFacts
        snapshot={featureSnapshot({
          status: 'CodeReady',
          repoStatus: [
            {
              name: 'agentic-orchestrator',
              publishable: true,
              pullRequests: [
                {
                  position: 1,
                  title: 'Phase 8 stack read model',
                  state: 'none',
                  noCommits: false,
                  pushedUpToDate: false,
                },
              ],
              freshness: 'in sync',
            },
          ],
        })}
        run={completedRun}
        onOpenPullRequest={vi.fn()}
      />,
    );
    const facts = screen.getByRole('region', { name: 'Feature facts' });
    expect(facts).toHaveTextContent('In sync');
    expect(facts).not.toHaveTextContent('Pull request');
    expect(screen.queryByRole('button', { name: 'Open pull request' })).not.toBeInTheDocument();
  });
});
