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

import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import type { PullRequestEntryView, RepoStatusView } from '../../../shared/ipc';
import { RepositoryInstrument } from './RepositoryInstrument';

function pr(over: Partial<PullRequestEntryView> & { position: number }): PullRequestEntryView {
  return {
    title: 'Search revamp',
    state: 'none',
    noCommits: false,
    pushedUpToDate: false,
    ...over,
  };
}

const failedRepo: RepoStatusView = {
  name: 'publish-web',
  publishable: true,
  touched: true,
  error: {
    code: 'publish_pull_request_failed',
    class: 'needs_action',
    title: 'Pull-request creation failed',
    summary: 'Creating the pull request for repository "publish-web" failed.',
    remediation: { hint: 'Check GitHub access, then retry.', actions: ['publish'] },
    context: { repositories: [{ name: 'publish-web', branch: 'agentico/my-feature' }] },
    diagnostics: 'POST /repos/e2e/publish-web/pulls: 502 Bad Gateway',
  },
};

function renderInstrument(
  repos: RepoStatusView[],
  over: Partial<React.ComponentProps<typeof RepositoryInstrument>> = {},
) {
  return render(
    <RepositoryInstrument
      repos={repos}
      onOpenPullRequest={vi.fn()}
      onOpenPublish={vi.fn()}
      {...over}
    />,
  );
}

describe('RepositoryInstrument', () => {
  it('indicates a publish failure with the catalog title and an Open publish link, not an alert', async () => {
    const onOpenPublish = vi.fn();
    renderInstrument([failedRepo], { onOpenPublish });
    const user = userEvent.setup();

    const section = screen.getByRole('region', { name: 'Repository status' });
    // Indication only: no alert-role element, no summary, remediation, or
    // diagnostics from the repository's record.
    expect(within(section).queryByRole('alert')).not.toBeInTheDocument();
    expect(within(section).getByText('Pull-request creation failed')).toBeVisible();
    expect(within(section).queryByText('Check GitHub access, then retry.')).not.toBeInTheDocument();
    expect(within(section).queryByText('502 Bad Gateway')).not.toBeInTheDocument();

    const openPublish = within(section).getByRole('button', { name: 'Open publish' });
    await user.click(openPublish);
    expect(onOpenPublish).toHaveBeenCalledOnce();
  });

  it('renders no indication for a repository without a stored record', () => {
    renderInstrument([
      {
        name: 'publish-api',
        publishable: true,
        pullRequests: [
          {
            position: 1,
            title: 'Bootstrap',
            branch: 'feature/x/1-bootstrap',
            url: 'https://x.test/pull/1',
            state: 'open',
            noCommits: false,
            pushedUpToDate: true,
          },
        ],
      },
    ]);

    const section = screen.getByRole('region', { name: 'Repository status' });
    expect(within(section).queryByText('Open publish')).not.toBeInTheDocument();
    expect(document.querySelector('.repo-instrument__publish-attention')).toBeNull();
    expect(within(section).queryByRole('alert')).not.toBeInTheDocument();
  });

  it('renders the title without the link when no publish modal is available', () => {
    renderInstrument([failedRepo], { onOpenPublish: undefined });

    const section = screen.getByRole('region', { name: 'Repository status' });
    expect(within(section).getByText('Pull-request creation failed')).toBeVisible();
    expect(within(section).queryByRole('button', { name: 'Open publish' })).not.toBeInTheDocument();
  });

  it('renders one row per layer in position order with state badges, linking only published layers', async () => {
    const onOpenPullRequest = vi.fn();
    renderInstrument(
      [
        {
          name: 'publish-api',
          publishable: true,
          pullRequests: [
            pr({ position: 1, title: 'Bootstrap', url: 'https://x.test/pull/11', state: 'open' }),
            pr({ position: 2, title: 'Search revamp', noCommits: true }),
            pr({ position: 3, title: 'Teardown' }),
          ],
        },
      ],
      { onOpenPullRequest },
    );
    const user = userEvent.setup();

    const section = screen.getByRole('region', { name: 'Repository status' });
    const rows = section.querySelectorAll('.repo-instrument__pr-row');
    expect(rows).toHaveLength(3);
    const positions = section.querySelectorAll('.repo-instrument__pr-position');
    expect(Array.from(positions).map((node) => node.textContent)).toEqual([
      'Layer 1',
      'Layer 2',
      'Layer 3',
    ]);
    expect(within(section).getByText('Bootstrap')).toBeVisible();
    expect(within(section).getByText('Search revamp')).toBeVisible();
    expect(within(section).getByText('Teardown')).toBeVisible();
    expect(within(section).getByText('open')).toBeVisible();
    expect(within(section).getByText('no changes in this repository')).toBeVisible();
    expect(within(section).getByText('not published')).toBeVisible();

    // Only the layer with a published PR carries the external link.
    const link = within(section).getByRole('button', { name: 'Open pull request' });
    expect(within(section).getAllByRole('button', { name: 'Open pull request' })).toHaveLength(1);
    await user.click(link);
    expect(onOpenPullRequest).toHaveBeenCalledOnce();
    expect(onOpenPullRequest).toHaveBeenCalledWith('https://x.test/pull/11');
  });

  it('renders merged and closed layers with their state badges', () => {
    renderInstrument([
      {
        name: 'publish-api',
        publishable: true,
        pullRequests: [
          pr({ position: 1, title: 'Bootstrap', state: 'merged' }),
          pr({
            position: 2,
            title: 'Search revamp',
            state: 'closed',
            url: 'https://x.test/pull/22',
          }),
        ],
      },
    ]);

    const section = screen.getByRole('region', { name: 'Repository status' });
    expect(within(section).getByText('merged')).toBeVisible();
    expect(within(section).getByText('closed')).toBeVisible();
    expect(within(section).getAllByRole('button', { name: 'Open pull request' })).toHaveLength(1);
  });

  it('renders a one-layer repository as a single row through the same list', () => {
    renderInstrument([
      {
        name: 'publish-api',
        publishable: true,
        pullRequests: [pr({ position: 1, title: 'Search revamp', url: 'https://x.test/pull/1' })],
      },
    ]);

    const section = screen.getByRole('region', { name: 'Repository status' });
    expect(section.querySelectorAll('.repo-instrument__pr-list')).toHaveLength(1);
    expect(section.querySelectorAll('.repo-instrument__pr-row')).toHaveLength(1);
    expect(within(section).getByText('Layer 1')).toBeVisible();
  });

  it('renders no stack rows when the repository carries no pull requests', () => {
    renderInstrument([{ name: 'publish-api', publishable: true }]);

    const section = screen.getByRole('region', { name: 'Repository status' });
    expect(section.querySelector('.repo-instrument__pr-list')).toBeNull();
    expect(
      within(section).queryByRole('button', { name: 'Open pull request' }),
    ).not.toBeInTheDocument();
  });
});
