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
import { ipcError } from '../../test/agenticoMock';
import { ExplainChatProvider } from '../../explainChat';
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
    expect(screen.queryByRole('button', { name: 'Generate narrative' })).not.toBeInTheDocument();
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

  it('renders no PR narrative controls even while a new pull request is selected', async () => {
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

    // The repository checkbox is the only per-repository control; the
    // narrative (title, body, generate) is server-generated, never edited.
    expect(screen.getByRole('checkbox', { name: 'web' })).toBeVisible();
    expect(screen.queryByLabelText('PR title')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('PR body')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Generate narrative' })).not.toBeInTheDocument();
    expect(screen.queryByText('Required')).not.toBeInTheDocument();
    expect(screen.queryByText('Optional')).not.toBeInTheDocument();
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

  it('sends only repos and source_revision when a selected repo needs a new pull request', async () => {
    const user = userEvent.setup();
    const dispatchAction = vi.fn().mockResolvedValue(result);
    render(<PublishModal {...props({ dispatchAction })} />);

    const publish = screen.getByRole('button', { name: 'Publish updates' });
    expect(publish).toBeEnabled();
    await user.click(publish);

    expect(dispatchAction).toHaveBeenCalledWith({
      featureId,
      action: 'publish',
      body: { source_revision: 'rev-1', repos: ['web'] },
    });
  });

  it('keeps the publish mutation locked after a swallowed refresh following a timeout', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    const setPublishTimeoutLocked = vi.fn();
    const dispatchAction = vi.fn().mockRejectedValue(
      ipcError('E_REQUEST_TIMEOUT', 'publish did not answer before the bound', {
        class: 'warning',
      }),
    );
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

  it('renders the timeout lock as a warning-class E_REQUEST_TIMEOUT ErrorSurface with the sheet disarmed', async () => {
    const user = userEvent.setup();
    const dispatchAction = vi.fn().mockRejectedValue(
      ipcError('E_REQUEST_TIMEOUT', 'publish did not answer before the bound', {
        class: 'warning',
      }),
    );
    render(
      <PublishModal
        {...props({
          dispatchAction,
          onDispatched: vi.fn().mockRejectedValue(new Error('refresh unavailable')),
          preflight: preflightWith({
            repos: [
              { repo: 'api', publishable: true, touched: true, status: 'unpublished_changes' },
            ],
          }),
        })}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Publish updates' }));
    // The locked state is the request-timeout canonical warning: the sheet's
    // own status line never carries the failure text. Wait on the catalog
    // title — the transient "Refreshing…" progress line is a plain status.
    const title = await screen.findByText('Request timed out');
    const surface = title.closest('.error-surface') as HTMLElement;
    expect(surface).toHaveClass(
      'error-surface',
      'error-surface--compact',
      'error-surface--warning',
    );
    expect(within(surface).getByText('E_REQUEST_TIMEOUT')).toHaveClass('error-surface__code');
    expect(surface).toHaveTextContent(
      'Publish may still be running. Quit and reopen Agentico before publishing again.',
    );
    // The publish control stays disarmed by the existing lock logic.
    expect(screen.getByRole('button', { name: 'Reconciling…' })).toBeDisabled();
  });

  it('moves focus to a rejected publish surface', async () => {
    const user = userEvent.setup();
    const dispatchAction = vi
      .fn()
      .mockRejectedValue(ipcError('E_INTERNAL', 'publish failed safely'));
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

    // The card's button retries only this repository, with no narrative
    // fields in the payload.
    const retry = within(card).getByRole('button', { name: 'Retry publish' });
    expect(retry).toBeEnabled();
    await user.click(retry);
    expect(dispatchAction).toHaveBeenCalledWith({
      featureId,
      action: 'publish',
      body: { source_revision: 'rev-1', repos: ['web'] },
    });
  });

  it('keeps the row card retry available whenever the catalog publish action is enabled', () => {
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
    expect(within(row).getByRole('button', { name: 'Retry publish' })).toBeEnabled();
    expect(
      within(row).queryByText('Add a PR title to retry this publish.'),
    ).not.toBeInTheDocument();
  });

  it('renders no card for a repository without a stored record and no legacy outcome detail', () => {
    render(<PublishModal {...props()} />);

    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(document.querySelector('.completion-workspace__repo-outcome-detail')).toBeNull();
    expect(document.querySelector('.completion-publish-sheet__failure')).toBeNull();
    expect(screen.queryByText("Agentico couldn't prepare this publish.")).not.toBeInTheDocument();
  });

  it('routes the failed repository card as a repository reference with no feature clause', async () => {
    const user = userEvent.setup();
    const requestRoute = vi.fn();
    render(
      <ExplainChatProvider requestRoute={requestRoute}>
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
        />
      </ExplainChatProvider>,
    );

    const row = failedRepoRow();
    const card = within(row).getByRole('alert');
    await user.click(within(card).getByRole('button', { name: 'Explain in chat' }));
    expect(requestRoute).toHaveBeenCalledTimes(1);
    expect(requestRoute).toHaveBeenCalledWith({
      target: 'ama',
      draft:
        'Explain the "Pull-request creation failed" error (publish_pull_request_failed) and what I should do next.',
      autoSubmit: true,
      chatContext: {
        scope: 'repository',
        code: 'publish_pull_request_failed',
        featureId,
        repository: 'web',
      },
    });
  });

  it('renders no rejection surface when the refreshed preflight shows a repository record', async () => {
    const user = userEvent.setup();
    const dispatchAction = vi
      .fn()
      .mockRejectedValue(ipcError('publish_partial_failure', 'web pull request failed'));
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

    await user.click(screen.getByRole('button', { name: 'Publish updates' }));
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
    const failure = ipcError('publish_partial_failure', 'one repository failed', {
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
      .mockRejectedValueOnce(ipcError('publish_partial_failure', 'web push failed'))
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
          dispatchAction: vi
            .fn()
            .mockRejectedValue(ipcError('E_INTERNAL', 'publish failed safely')),
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
    const dispatchAction = vi.fn().mockRejectedValue(
      ipcError('E_REQUEST_TIMEOUT', 'publish did not answer before the bound', {
        class: 'warning',
      }),
    );
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
    const dispatchAction = vi.fn().mockRejectedValue(
      ipcError('E_REQUEST_TIMEOUT', 'publish did not answer before the bound', {
        class: 'warning',
      }),
    );
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
                pullRequests: [
                  {
                    position: 1,
                    title: 'Bootstrap',
                    branch: 'feature/x/1-bootstrap',
                    url: 'https://example.test/pr/1',
                    state: 'open',
                    noCommits: false,
                    pushedUpToDate: false,
                    pushMode: 'fast_forward',
                  },
                ],
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

  it('previews the stack under the repository row with a verb per layer', () => {
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
                pendingCommits: 2,
                pullRequests: [
                  {
                    position: 1,
                    title: 'Bootstrap',
                    url: 'https://example.test/pr/1',
                    state: 'open',
                    noCommits: false,
                    pushedUpToDate: true,
                    pushMode: 'none',
                  },
                  {
                    position: 2,
                    title: 'Search revamp',
                    state: 'none',
                    noCommits: false,
                    pushedUpToDate: false,
                    pushMode: 'create',
                  },
                  {
                    position: 3,
                    title: 'Index revamp',
                    url: 'https://example.test/pr/3',
                    state: 'open',
                    noCommits: false,
                    pushedUpToDate: false,
                    pushMode: 'rewrite',
                  },
                  {
                    position: 4,
                    title: 'Teardown',
                    url: 'https://example.test/pr/4',
                    state: 'open',
                    noCommits: false,
                    pushedUpToDate: false,
                    pushMode: 'fast_forward',
                  },
                  {
                    position: 5,
                    title: 'Empty layer',
                    state: 'none',
                    noCommits: true,
                    pushedUpToDate: false,
                    pushMode: 'none',
                  },
                ],
              },
            ],
          }),
        })}
      />,
    );

    const row = screen
      .getByRole('checkbox', { name: 'api' })
      .closest('.completion-workspace__publish-repo') as HTMLElement;
    const preview = row.querySelector('.completion-workspace__stack-preview') as HTMLElement;
    const positions = preview.querySelectorAll('.completion-workspace__stack-preview-position');
    expect(Array.from(positions).map((node) => node.textContent)).toEqual([
      'Layer 1',
      'Layer 2',
      'Layer 3',
      'Layer 4',
      'Layer 5',
    ]);
    expect(within(preview).getByText('up to date')).toBeVisible();
    expect(within(preview).getByText('will create')).toBeVisible();
    expect(within(preview).getByText('will rewrite')).toBeVisible();
    expect(within(preview).getByText('will update')).toBeVisible();
    expect(within(preview).getByText('no changes in this repository')).toBeVisible();
    // Only the layers with a published PR carry the external link.
    expect(within(preview).getAllByRole('button', { name: 'PR ↗' })).toHaveLength(3);
    // The repository checkbox stays the row's only per-repository control.
    expect(within(row).getByRole('checkbox', { name: 'api' })).toBeVisible();
  });

  it('previews the stack in the already-published group with merged and closed verbs', () => {
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
                status: 'already_published',
                pullRequests: [
                  {
                    position: 1,
                    title: 'Bootstrap',
                    url: 'https://example.test/pr/1',
                    state: 'merged',
                    noCommits: false,
                    pushedUpToDate: true,
                    pushMode: 'none',
                  },
                  {
                    position: 2,
                    title: 'Search revamp',
                    state: 'closed',
                    noCommits: false,
                    pushedUpToDate: false,
                    pushMode: 'none',
                  },
                ],
              },
            ],
          }),
        })}
      />,
    );

    const group = document.querySelector('.completion-workspace__published-repos') as HTMLElement;
    expect(within(group).getByText('api')).toBeVisible();
    expect(within(group).getByText('merged')).toBeVisible();
    expect(within(group).getByText('closed')).toBeVisible();
    expect(within(group).getAllByRole('button', { name: 'PR ↗' })).toHaveLength(1);
  });
});
