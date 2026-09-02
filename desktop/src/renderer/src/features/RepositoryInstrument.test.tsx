import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import type { RepoStatusView } from '../../../shared/ipc';
import { RepositoryInstrument } from './RepositoryInstrument';

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
    renderInstrument([{ name: 'publish-api', publishable: true, prUrl: 'https://x.test/pull/1' }]);

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
});
