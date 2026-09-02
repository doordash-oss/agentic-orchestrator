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

import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import type {
  CompletionPreflightResult,
  FeatureActionResult,
  FeatureActionView,
} from '../../../../shared/ipc';
import type { CanonicalError } from '../../../../shared/api/parse';
import { PublishModal } from './PublishModal';

const featureId = 'abcd1234ef567890';
const result: FeatureActionResult = {
  featureId,
  action: 'publish',
  result: 'published',
  sessionIds: [],
};

const publishAction: FeatureActionView = { id: 'publish', enabled: true, disabledReasons: [] };

const newPrPreflight: CompletionPreflightResult = {
  featureId,
  sourceRevision: 'rev-1',
  canMarkDone: true,
  repos: [{ repo: 'web', publishable: true, touched: true, status: 'eligible' }],
};

/** The canonical object the server renders for a stored publish-failure record. */
const prFailedError: CanonicalError = {
  code: 'publish_pull_request_failed',
  class: 'needs_action',
  title: 'Pull-request creation failed',
  summary: 'Creating the pull request for repository "web" failed.',
  remediation: { hint: 'Check GitHub access, then retry.', actions: ['publish'] },
  context: { repositories: [{ name: 'web', branch: 'agentico/search-revamp' }] },
  diagnostics: 'creating pull request: POST /repos/e2e/web/pulls: 502 Bad Gateway',
};

function props(over: Partial<React.ComponentProps<typeof PublishModal>> = {}) {
  return {
    featureId,
    preflight: newPrPreflight,
    actions: [publishAction] as readonly FeatureActionView[],
    dispatchAction: vi.fn().mockResolvedValue(result),
    generatePublishDescription: vi
      .fn()
      .mockResolvedValue({ featureId, title: 'Title', body: 'Body' }),
    openExternal: vi.fn().mockResolvedValue({ ok: true }),
    onDispatched: vi.fn(),
    onClose: vi.fn(),
    publishTimeoutLocked: false,
    setPublishTimeoutLocked: vi.fn(),
    ...over,
  };
}

function preflightWith(over: Partial<CompletionPreflightResult>): CompletionPreflightResult {
  return { ...newPrPreflight, ...over };
}

function failedRepoRow(): HTMLElement {
  return screen
    .getByRole('checkbox', { name: 'web' })
    .closest('.completion-workspace__publish-repo') as HTMLElement;
}

describe('PublishModal', () => {
  it('keeps an existing pull-request update focused on work users can control', () => {
    render(
      <PublishModal
        {...props({
          preflight: preflightWith({
            repos: [
              {
                repo: 'api',
                publishable: true,
                touched: true,
                status: 'unpublished_changes',
                pendingCommits: 1,
                pushMode: 'rewrite',
              },
            ],
          }),
        })}
      />,
    );

    expect(screen.getByRole('dialog', { name: 'Publish reviewed changes' })).toBeVisible();
    expect(screen.queryByLabelText('PR title')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('PR body')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Generate PR narrative' })).not.toBeInTheDocument();
    expect(screen.getByText('Rewrites the pull-request branch with a safety lease.')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Publish updates' })).toBeEnabled();
  });

  it('uses the sheet family and a cancel-first footer', () => {
    render(<PublishModal {...props({ preflight: preflightWith({ repos: [] }) })} />);

    const dialog = screen.getByRole('dialog', { name: 'Publish reviewed changes' });
    expect(dialog).toHaveClass('sheet', 'completion-publish-sheet');
    const footer = dialog.querySelector('.sheet__footer');
    expect(
      within(footer as HTMLElement)
        .getAllByRole('button')
        .map((button) => button.textContent),
    ).toEqual(['Cancel', 'Publish updates']);
  });

  it('dispatches the typed request for an existing pull request', async () => {
    const user = userEvent.setup();
    const dispatchAction = vi.fn().mockResolvedValue(result);
    render(
      <PublishModal
        {...props({
          dispatchAction,
          preflight: preflightWith({
            repos: [
              {
                repo: 'api',
                publishable: true,
                touched: true,
                status: 'unpublished_changes',
                pendingCommits: 1,
              },
            ],
          }),
        })}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Publish updates' }));
    expect(dispatchAction).toHaveBeenCalledWith({
      featureId,
      action: 'publish',
      body: { source_revision: 'rev-1', repos: ['api'] },
    });
  });

  it('shows required and optional PR fields only while a new PR is selected', async () => {
    const user = userEvent.setup();
    render(
      <PublishModal
        {...props({
          preflight: preflightWith({
            repos: [
              { repo: 'web', publishable: true, touched: true, status: 'eligible' },
              {
                repo: 'api',
                publishable: true,
                touched: true,
                status: 'unpublished_changes',
                pendingCommits: 1,
              },
            ],
          }),
        })}
      />,
    );

    expect(screen.getByText('Required')).toBeVisible();
    expect(screen.getByText('Optional')).toBeVisible();
    await user.click(screen.getByRole('checkbox', { name: 'web' }));
    expect(screen.queryByLabelText('PR title')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('PR body')).not.toBeInTheDocument();
  });

  it('requires confirmation before committing selected dirty files', async () => {
    const user = userEvent.setup();
    render(
      <PublishModal
        {...props({
          preflight: preflightWith({
            repos: [
              {
                repo: 'api',
                publishable: true,
                touched: true,
                status: 'unpublished_changes',
                pendingDirty: true,
                pendingDirtyFiles: ['src/a.ts'],
                pendingDirtyFileTotal: 1,
              },
            ],
          }),
        })}
      />,
    );

    const publish = screen.getByRole('button', { name: 'Publish updates' });
    expect(publish).toBeDisabled();
    await user.click(screen.getByRole('checkbox', { name: 'Commit uncommitted files' }));
    expect(publish).toBeEnabled();
  });

  it('renders the diverged rejection through the catalog text without a raw push command', async () => {
    const user = userEvent.setup();
    const dispatchAction = vi.fn().mockRejectedValue(
      Object.assign(
        new Error('publish_remote_diverged: pull-request branch contains remote work'),
        {
          canonical: {
            code: 'publish_remote_diverged',
            class: 'needs_action',
            title: 'Pull-request branch diverged',
            summary:
              'The pull-request branch for "api" contains 2 remote commits that are not in this workspace.',
            remediation: {
              hint: 'Review and reconcile the branch on GitHub, then refresh and retry.',
              actions: ['publish'],
            },
            context: {
              repositories: [{ name: 'api', branch: 'feature/x', remote_only_commits: 2 }],
            },
          } satisfies CanonicalError,
        },
      ),
    );
    render(
      <PublishModal
        {...props({
          dispatchAction,
          preflight: preflightWith({
            repos: [
              {
                repo: 'api',
                publishable: true,
                touched: true,
                status: 'unpublished_changes',
                pendingCommits: 1,
              },
            ],
          }),
        })}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Publish updates' }));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveClass('error-surface--compact');
    expect(alert).toHaveTextContent('The pull-request branch for "api" contains 2 remote commits');
    expect(alert).not.toHaveTextContent('git push');
  });

  it('closes through Escape, the scrim, and Cancel while idle', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(<PublishModal {...props({ onClose, preflight: preflightWith({ repos: [] }) })} />);

    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    fireEvent.mouseDown(screen.getByRole('dialog').parentElement as HTMLElement);
    fireEvent.keyDown(window, { key: 'Escape' });
    expect(onClose).toHaveBeenCalledTimes(3);
  });

  it('does not close through Cancel, scrim, or Escape while publishing', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    const dispatchAction = vi.fn(() => new Promise<FeatureActionResult>(() => undefined));
    render(
      <PublishModal
        {...props({
          onClose,
          dispatchAction,
          preflight: preflightWith({
            repos: [
              {
                repo: 'api',
                publishable: true,
                touched: true,
                status: 'unpublished_changes',
                pendingCommits: 1,
              },
            ],
          }),
        })}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Publish updates' }));
    expect(screen.getByRole('button', { name: 'Publishing…' })).toBeDisabled();
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    fireEvent.mouseDown(screen.getByRole('dialog').parentElement as HTMLElement);
    fireEvent.keyDown(window, { key: 'Escape' });
    expect(onClose).not.toHaveBeenCalled();
  });

  it('uses Publish and sends title metadata when a selected repo needs a new pull request', async () => {
    const user = userEvent.setup();
    const dispatchAction = vi.fn().mockResolvedValue(result);
    render(<PublishModal {...props({ dispatchAction })} />);

    expect(screen.getByRole('button', { name: 'Publish' })).toBeDisabled();
    const details = screen.getByRole('region', { name: 'Pull request details' });
    expect(within(details).getByRole('button', { name: 'Generate narrative' })).toBeVisible();
    await user.type(screen.getByLabelText('PR title'), 'Ship reviewed work');
    await user.type(screen.getByLabelText('PR body'), 'A compact description.');
    await user.click(screen.getByRole('button', { name: 'Publish' }));

    expect(dispatchAction).toHaveBeenCalledWith({
      featureId,
      action: 'publish',
      body: {
        source_revision: 'rev-1',
        repos: ['web'],
        title: 'Ship reviewed work',
        body: 'A compact description.',
      },
    });
  });

  it('keeps Publish disabled until the required title is nonblank', async () => {
    const user = userEvent.setup();
    render(<PublishModal {...props()} />);

    const publish = screen.getByRole('button', { name: 'Publish' });
    expect(publish).toBeDisabled();

    await user.click(screen.getByLabelText('PR title'));
    await user.tab();
    expect(screen.getByText('Add a title to create the pull request.')).toBeVisible();

    await user.type(screen.getByLabelText('PR title'), 'Ship reviewed work');
    expect(publish).toBeEnabled();
  });

  it('moves focus to a narrative-generation failure surface', async () => {
    const user = userEvent.setup();
    render(
      <PublishModal
        {...props({
          generatePublishDescription: vi.fn().mockRejectedValue(new Error('generation failed')),
        })}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Generate narrative' }));
    const surface = await screen.findByRole('alert');
    expect(surface).toHaveClass('error-surface--compact');
    expect(screen.getByText('Narrative generation was rejected')).toBeVisible();
    await waitFor(() => expect(surface).toHaveFocus());
  });

  it('keeps the publish mutation locked after a swallowed refresh following a timeout', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    const setPublishTimeoutLocked = vi.fn();
    const dispatchAction = vi
      .fn()
      .mockRejectedValue(new Error('E_REQUEST_TIMEOUT: publish did not answer before the bound'));
    render(
      <PublishModal
        {...props({
          dispatchAction,
          onClose,
          setPublishTimeoutLocked,
          // Completion refresh swallows an IPC fetch failure and resolves, just
          // as the production preflight hook does.
          onDispatched: vi.fn().mockResolvedValue(undefined),
          preflight: preflightWith({
            repos: [
              { repo: 'api', publishable: true, touched: true, status: 'unpublished_changes' },
            ],
          }),
        })}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Publish updates' }));
    expect(await screen.findByRole('status')).toHaveTextContent('may still be running');
    expect(screen.getByRole('button', { name: 'Reconciling…' })).toBeDisabled();
    expect(setPublishTimeoutLocked).toHaveBeenCalledWith(true);
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onClose).toHaveBeenCalledOnce();
    expect(dispatchAction).toHaveBeenCalledOnce();
  });

  it('marks a blank required title invalid after local validation', async () => {
    const user = userEvent.setup();
    render(<PublishModal {...props()} />);

    await user.click(screen.getByLabelText('PR title'));
    await user.tab();

    const title = screen.getByLabelText('PR title');
    expect(title).toHaveAttribute('aria-invalid', 'true');
    expect(screen.getByText('Add a title to create the pull request.')).toBeVisible();
  });

  it('moves focus to a rejected publish surface', async () => {
    const user = userEvent.setup();
    const dispatchAction = vi.fn().mockRejectedValue(new Error('publish failed safely'));
    render(
      <PublishModal
        {...props({
          dispatchAction,
          preflight: preflightWith({
            repos: [
              {
                repo: 'api',
                publishable: true,
                touched: true,
                status: 'unpublished_changes',
              },
            ],
          }),
        })}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Publish updates' }));
    const surface = await screen.findByRole('alert');
    expect(surface).toHaveClass('error-surface--compact');
    await waitFor(() => expect(surface).toHaveFocus());
  });

  it('renders one full ErrorSurface card in the failed repository row with a repo-scoped retry', async () => {
    const user = userEvent.setup();
    const dispatchAction = vi.fn().mockResolvedValue(result);
    render(
      <PublishModal
        {...props({
          dispatchAction,
          preflight: preflightWith({
            repos: [
              {
                repo: 'web',
                publishable: true,
                touched: true,
                status: 'eligible',
                error: prFailedError,
              },
            ],
          }),
        })}
      />,
    );

    const row = failedRepoRow();
    // Exactly one alert-role ErrorSurface, and it lives in the repository row.
    expect(screen.getAllByRole('alert')).toHaveLength(1);
    const card = within(row).getByRole('alert');
    expect(card).toHaveClass('error-surface', 'error-surface--full', 'error-surface--needs-action');
    expect(within(card).getByText('Needs your action')).toBeVisible();
    expect(within(card).getByText('publish_pull_request_failed')).toBeVisible();
    expect(within(card).getByText('Pull-request creation failed')).toBeVisible();
    expect(
      within(card).getByText('Creating the pull request for repository "web" failed.'),
    ).toBeVisible();
    expect(within(card).getByText('Check GitHub access, then retry.')).toBeVisible();

    // The repository rides under the Details disclosure.
    const details = within(card).getByText('Details').closest('details');
    expect(details).not.toBeNull();
    expect(details).not.toHaveAttribute('open');
    await user.click(within(card).getByText('Details'));
    expect(within(details as HTMLElement).getByText('web')).toBeVisible();
    expect(within(details as HTMLElement).getByText('agentico/search-revamp')).toBeVisible();

    // Raw diagnostics stay behind the second disclosure.
    const diagnostics = within(card).getByText('Diagnostics').closest('details');
    expect(diagnostics).not.toBeNull();
    expect(diagnostics).not.toHaveAttribute('open');
    await user.click(within(card).getByText('Diagnostics'));
    expect(within(diagnostics as HTMLElement).getByText(/502 Bad Gateway/)).toBeVisible();

    // With the required title in place, the card's button retries only this
    // repository with the form's current title and body.
    await user.type(screen.getByLabelText('PR title'), 'Ship reviewed work');
    await user.type(screen.getByLabelText('PR body'), 'A compact description.');
    const retry = within(card).getByRole('button', { name: 'Retry publish' });
    expect(retry).toBeEnabled();
    await user.click(retry);
    expect(dispatchAction).toHaveBeenCalledWith({
      featureId,
      action: 'publish',
      body: {
        source_revision: 'rev-1',
        repos: ['web'],
        title: 'Ship reviewed work',
        body: 'A compact description.',
      },
    });
  });

  it('replaces the row card retry with its disabled reason while the required title is empty', () => {
    render(
      <PublishModal
        {...props({
          preflight: preflightWith({
            repos: [
              {
                repo: 'web',
                publishable: true,
                touched: true,
                status: 'eligible',
                error: prFailedError,
              },
            ],
          }),
        })}
      />,
    );

    const row = failedRepoRow();
    expect(within(row).queryByRole('button', { name: 'Retry publish' })).not.toBeInTheDocument();
    expect(within(row).getByText('Add a PR title to retry this publish.')).toBeVisible();
  });

  it('renders no card for a repository without a stored record and no legacy outcome detail', () => {
    render(<PublishModal {...props()} />);

    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(document.querySelector('.completion-workspace__repo-outcome-detail')).toBeNull();
    expect(document.querySelector('.completion-publish-sheet__failure')).toBeNull();
    expect(screen.queryByText("Agentico couldn't prepare this publish.")).not.toBeInTheDocument();
  });

  it('renders no rejection surface when the refreshed preflight shows a repository record', async () => {
    const user = userEvent.setup();
    const dispatchAction = vi
      .fn()
      .mockRejectedValue(new Error('publish_partial_failure: web pull request failed'));
    const initial = preflightWith({
      repos: [{ repo: 'web', publishable: true, touched: true, status: 'eligible' }],
    });
    const refreshed = preflightWith({
      repos: [
        {
          repo: 'web',
          publishable: true,
          touched: true,
          status: 'eligible',
          error: prFailedError,
        },
      ],
    });
    function PartialPublishHarness() {
      const [preflight, setPreflight] = useState(initial);
      return (
        <PublishModal
          {...props({
            dispatchAction,
            preflight,
            onDispatched: () => setPreflight(refreshed),
          })}
        />
      );
    }
    render(<PartialPublishHarness />);

    // The repository needs a new pull request, so the publish requires a
    // title before it can be dispatched and rejected.
    await user.type(screen.getByLabelText('PR title'), 'Ship reviewed work');
    await user.click(screen.getByRole('button', { name: 'Publish' }));
    // The refreshed row card owns the condition: no rejection surface, and
    // the row card is the only alert on the page.
    const card = await screen.findByRole('alert');
    expect(card).toHaveClass('error-surface--full');
    expect(screen.getAllByRole('alert')).toHaveLength(1);
    expect(screen.queryByText('Publish was rejected')).not.toBeInTheDocument();
    expect(within(failedRepoRow()).getByRole('button', { name: 'Retry publish' })).toBeEnabled();
  });

  it('renders a stale-preflight rejection through one compact ErrorSurface and focuses it', async () => {
    const user = userEvent.setup();
    const dispatchAction = vi.fn().mockRejectedValue(
      Object.assign(new Error('conflict: stale completion preflight'), {
        canonical: {
          code: 'conflict',
          class: 'blocking',
          title: 'Conflict',
          summary: 'The request conflicts with the current state of the feature.',
          remediation: { hint: 'Refresh the feature and retry.' },
        } satisfies CanonicalError,
      }),
    );
    render(
      <PublishModal
        {...props({
          dispatchAction,
          preflight: preflightWith({
            repos: [
              {
                repo: 'api',
                publishable: true,
                touched: true,
                status: 'unpublished_changes',
                pendingCommits: 1,
              },
            ],
          }),
        })}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Publish updates' }));
    const surface = await screen.findByRole('alert');
    expect(surface).toHaveClass('error-surface--compact');
    expect(within(surface).getByText('conflict')).toBeVisible();
    expect(within(surface).getByText('Publish was rejected')).toBeVisible();
    await waitFor(() => expect(surface).toHaveFocus());
  });

  it('keeps raw diagnostics collapsed until requested', async () => {
    const user = userEvent.setup();
    const dispatchAction = vi.fn().mockRejectedValue(
      Object.assign(new Error('safe diagnostic detail'), {
        canonical: {
          code: 'publish_push_failed',
          class: 'needs_action',
          title: 'Repository publish failed',
          summary: 'Publishing repository "api" failed.',
          diagnostics: 'safe diagnostic detail',
        } satisfies CanonicalError,
      }),
    );
    render(
      <PublishModal
        {...props({
          dispatchAction,
          preflight: preflightWith({
            repos: [
              {
                repo: 'api',
                publishable: true,
                touched: true,
                status: 'unpublished_changes',
              },
            ],
          }),
        })}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Publish updates' }));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Publishing repository "api" failed.');
    expect(screen.queryByText('Show details')).not.toBeInTheDocument();
    const more = within(alert).getByText('More detail').closest('details');
    expect(more).not.toBeNull();
    expect(more).not.toHaveAttribute('open');
    await user.click(within(alert).getByText('More detail'));
    expect(within(more as HTMLElement).getByText('safe diagnostic detail')).toBeVisible();
  });

  it('renders structured remediation for an unknown publish failure', async () => {
    const user = userEvent.setup();
    const failure = Object.assign(new Error('publish_partial_failure: one repository failed'), {
      remediation: 'Resolve the repository failure, then retry the remaining work.',
    });
    render(
      <PublishModal
        {...props({
          dispatchAction: vi.fn().mockRejectedValue(failure),
          preflight: preflightWith({
            repos: [
              {
                repo: 'api',
                publishable: true,
                touched: true,
                status: 'unpublished_changes',
              },
            ],
          }),
        })}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Publish updates' }));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent(
      'Resolve the repository failure, then retry the remaining work.',
    );
    expect(screen.queryByText('More detail')).not.toBeInTheDocument();
    expect(screen.queryByText('Show details')).not.toBeInTheDocument();
  });

  it('refreshes partial progress before retrying only the repository that still failed', async () => {
    const user = userEvent.setup();
    const dispatchAction = vi
      .fn()
      .mockRejectedValueOnce(new Error('publish_partial_failure: web push failed'))
      .mockResolvedValueOnce(result);
    const initial: CompletionPreflightResult = {
      featureId,
      sourceRevision: 'rev-1',
      canMarkDone: true,
      repos: [
        { repo: 'api', publishable: true, touched: true, status: 'unpublished_changes' },
        { repo: 'web', publishable: true, touched: true, status: 'unpublished_changes' },
      ],
    };
    const refreshed: CompletionPreflightResult = {
      ...initial,
      repos: [
        { repo: 'api', publishable: true, touched: true, status: 'already_published' },
        { repo: 'web', publishable: true, touched: true, status: 'unpublished_changes' },
      ],
    };
    function PartialPublishHarness() {
      const [preflight, setPreflight] = useState(initial);
      return (
        <PublishModal
          {...props({
            dispatchAction,
            preflight,
            onDispatched: () => setPreflight(refreshed),
          })}
        />
      );
    }
    render(<PartialPublishHarness />);

    await user.click(screen.getByRole('button', { name: 'Publish updates' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('web push failed');
    await user.click(screen.getByRole('button', { name: 'Publish updates' }));

    expect(dispatchAction).toHaveBeenLastCalledWith({
      featureId,
      action: 'publish',
      body: { source_revision: 'rev-1', repos: ['web'] },
    });
  });

  it('preserves the publish failure when its best-effort refresh also fails', async () => {
    const user = userEvent.setup();
    let rejectRefresh: ((reason: Error) => void) | undefined;
    const onDispatched = vi.fn(
      () =>
        new Promise<void>((_resolve, reject) => {
          rejectRefresh = reject;
        }),
    );
    render(
      <PublishModal
        {...props({
          dispatchAction: vi.fn().mockRejectedValue(new Error('publish failed safely')),
          onDispatched,
          preflight: preflightWith({
            repos: [
              {
                repo: 'api',
                publishable: true,
                touched: true,
                status: 'unpublished_changes',
              },
            ],
          }),
        })}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Publish updates' }));
    expect(screen.getByRole('button', { name: 'Publishing…' })).toBeDisabled();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();

    rejectRefresh?.(new Error('refresh unavailable'));
    await waitFor(() =>
      expect(screen.getByRole('alert')).toHaveTextContent('publish failed safely'),
    );
    expect(screen.getByRole('alert')).not.toHaveTextContent('refresh unavailable');
    expect(screen.getByRole('button', { name: 'Publish updates' })).toBeEnabled();
  });

  it('announces success and retries only repositories still publishable after refresh', async () => {
    const user = userEvent.setup();
    const dispatchAction = vi.fn().mockResolvedValue(result);
    const initial: CompletionPreflightResult = {
      featureId,
      sourceRevision: 'rev-1',
      canMarkDone: true,
      repos: [
        { repo: 'api', publishable: true, touched: true, status: 'unpublished_changes' },
        { repo: 'web', publishable: true, touched: true, status: 'unpublished_changes' },
      ],
    };
    const refreshed: CompletionPreflightResult = {
      ...initial,
      repos: [
        { repo: 'api', publishable: true, touched: true, status: 'already_published' },
        { repo: 'web', publishable: true, touched: true, status: 'unpublished_changes' },
      ],
    };
    const view = render(<PublishModal {...props({ dispatchAction, preflight: initial })} />);

    await user.click(screen.getByRole('button', { name: 'Publish updates' }));
    expect(await screen.findByRole('status')).toHaveTextContent('published');
    view.rerender(<PublishModal {...props({ dispatchAction, preflight: refreshed })} />);
    await user.click(screen.getByRole('button', { name: 'Publish updates' }));

    await waitFor(() =>
      expect(dispatchAction).toHaveBeenLastCalledWith({
        featureId,
        action: 'publish',
        body: { source_revision: 'rev-1', repos: ['web'] },
      }),
    );
  });

  it('announces a timeout without claiming completion and lets the user dismiss after refresh', async () => {
    const user = userEvent.setup();
    let finishRefresh: (() => void) | undefined;
    const onDispatched = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          finishRefresh = resolve;
        }),
    );
    const onClose = vi.fn();
    const dispatchAction = vi
      .fn()
      .mockRejectedValue(new Error('E_REQUEST_TIMEOUT: publish did not answer before the bound'));
    render(
      <PublishModal
        {...props({
          dispatchAction,
          onDispatched,
          onClose,
          preflight: preflightWith({
            repos: [
              {
                repo: 'api',
                publishable: true,
                touched: true,
                status: 'unpublished_changes',
              },
            ],
          }),
        })}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Publish updates' }));
    expect(await screen.findByRole('status')).toHaveTextContent(
      'Refreshing the latest publish state',
    );
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onClose).not.toHaveBeenCalled();

    finishRefresh?.();
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Reconciling…' })).toBeDisabled(),
    );
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onClose).toHaveBeenCalledOnce();
  });

  it('keeps a timeout locked when its refresh callback rejects', async () => {
    const user = userEvent.setup();
    const dispatchAction = vi
      .fn()
      .mockRejectedValue(new Error('E_REQUEST_TIMEOUT: publish did not answer before the bound'));
    render(
      <PublishModal
        {...props({
          dispatchAction,
          onDispatched: vi.fn().mockRejectedValue(new Error('refresh unavailable')),
          preflight: preflightWith({
            repos: [
              {
                repo: 'api',
                publishable: true,
                touched: true,
                status: 'unpublished_changes',
              },
            ],
          }),
        })}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Publish updates' }));
    expect(await screen.findByRole('status')).toHaveTextContent('may still be running');
    expect(screen.getByRole('status')).toHaveTextContent(
      'Publish may still be running. Quit and reopen Agentico before publishing again.',
    );
    expect(screen.getByRole('button', { name: 'Reconciling…' })).toBeDisabled();
  });

  it('groups repository metadata and the pull-request link in the manifest row', () => {
    render(
      <PublishModal
        {...props({
          preflight: preflightWith({
            repos: [
              {
                repo: 'api',
                publishable: true,
                touched: true,
                status: 'unpublished_changes',
                pendingCommits: 3,
                prUrl: 'https://example.test/pr/1',
              },
            ],
          }),
        })}
      />,
    );

    const row = screen
      .getByRole('checkbox', { name: 'api' })
      .closest('.completion-workspace__publish-repo');
    expect(row?.querySelector('.completion-workspace__publish-repo-meta')).toHaveTextContent(
      '3 commits',
    );
    expect(within(row as HTMLElement).getByRole('button', { name: 'PR ↗' })).toBeVisible();
  });
});
