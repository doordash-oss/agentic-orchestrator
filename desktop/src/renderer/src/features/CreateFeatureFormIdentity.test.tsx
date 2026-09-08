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

import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { RepositoryIdentity, RepositoryState, ReadinessSnapshot } from '../../../shared/ipc';
import {
  creationDefaults,
  installAgenticoMock,
  ipcError,
  mockRepoIdentity,
  readySnapshot,
} from '../test/agenticoMock';
import { CreateFeatureForm } from './CreateFeatureForm';

afterEach(cleanup);

function repo(
  name: string,
  identity: RepositoryIdentity,
  overrides: Partial<RepositoryState> = {},
): RepositoryState {
  return { name, path: identity.path, valid: true, featureReady: true, identity, ...overrides };
}

function catalogSnapshot(repositories: RepositoryState[]): ReadinessSnapshot {
  return readySnapshot({ repositories });
}

async function renderForm(mock = installAgenticoMock()) {
  const onCreated = vi.fn();
  const onClose = vi.fn();
  render(<CreateFeatureForm onCreated={onCreated} onClose={onClose} />);
  await screen.findByRole('button', { name: 'Next: Describe' });
  return { mock, onCreated, onClose, user: userEvent.setup() };
}

describe('the creation sheet repository identity reconciliation', () => {
  it('moves a selection to its renamed key when a same-named repository appears in another root', async () => {
    const service = mockRepoIdentity('/root-a/service');
    const other = mockRepoIdentity('/root-b/service');
    const unrelated = mockRepoIdentity('/root-b/widget');
    const mock = installAgenticoMock({
      defaults: creationDefaults({
        repositories: [repo('service', service), repo('widget', unrelated)],
      }),
    });
    const { user } = await renderForm(mock);

    await user.click(screen.getByRole('checkbox', { name: /^service\b/ }));
    await user.click(screen.getByRole('checkbox', { name: /^widget\b/ }));

    // A same-named repository in another root changes the collision-safe key.
    mock.api.getReadiness.mockResolvedValue(
      catalogSnapshot([
        repo('root-a/service', service),
        repo('root-b/service', other),
        repo('widget', unrelated),
      ]),
    );
    mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });

    const renamed = await screen.findByRole('checkbox', { name: /root-a\/service/ });
    expect(renamed).toBeChecked();
    // The colliding newcomer is not selected and the selection did not
    // duplicate: exactly one checked row for the identity.
    expect(screen.getByRole('checkbox', { name: /root-b\/service/ })).not.toBeChecked();
    expect(screen.getByRole('checkbox', { name: /^widget\b/ })).toBeChecked();
    const checked = screen
      .getAllByRole('checkbox')
      .filter((element) => (element as HTMLInputElement).checked);
    expect(checked).toHaveLength(2);

    await reachContractFrom(user);
    await vi.waitFor(() => expect(mock.api.createFeature).toHaveBeenCalled());
    expect(mock.api.createFeature).toHaveBeenCalledWith(
      expect.objectContaining({ repoKeys: ['root-a/service', 'widget'] }),
    );
  });

  it('moves a selection back to the bare key when the colliding repository goes away', async () => {
    const service = mockRepoIdentity('/root-a/service');
    const other = mockRepoIdentity('/root-b/service');
    const mock = installAgenticoMock({
      defaults: creationDefaults({
        repositories: [repo('root-a/service', service), repo('root-b/service', other)],
      }),
    });
    const { user } = await renderForm(mock);

    await user.click(screen.getByRole('checkbox', { name: /root-a\/service/ }));

    mock.api.getReadiness.mockResolvedValue(catalogSnapshot([repo('service', service)]));
    mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });

    expect(await screen.findByRole('checkbox', { name: /^service\b/ })).toBeChecked();
  });

  it('marks a removed repository as needing reselection and blocks continuing until resolved', async () => {
    const service = mockRepoIdentity('/root-a/service');
    const widget = mockRepoIdentity('/root-b/widget');
    const mock = installAgenticoMock({
      defaults: creationDefaults({
        repositories: [repo('service', service), repo('widget', widget)],
      }),
    });
    const { user } = await renderForm(mock);

    await user.click(screen.getByRole('checkbox', { name: /^service\b/ }));
    await user.click(screen.getByRole('checkbox', { name: /^widget\b/ }));
    mock.api.getReadiness.mockResolvedValue(catalogSnapshot([repo('widget', widget)]));
    mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });

    expect(await screen.findByText(/needs reselection/i)).toBeVisible();
    // Continuing with the unresolved selection is refused.
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    expect(screen.getByText(/resolve or remove the repositories/i)).toBeVisible();
    expect(screen.queryByRole('heading', { name: 'Define the work' })).toBeNull();
    // The unrelated usable repository stays available.
    expect(screen.getByRole('checkbox', { name: /^widget\b/ })).toBeChecked();

    // Removing the stale selection unblocks the draft.
    await user.click(screen.getByRole('button', { name: 'Remove' }));
    expect(screen.queryByText(/needs reselection/i)).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    expect(screen.getByRole('heading', { name: 'Define the work' })).toBeVisible();
  });

  it('never selects a replacement that carries the old key or path', async () => {
    const original = mockRepoIdentity('/root-a/service');
    const replacement = mockRepoIdentity('/root-a/service', { inode: '9999' });
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('service', original)] }),
    });
    const { user } = await renderForm(mock);

    await user.click(screen.getByRole('checkbox', { name: /^service\b/ }));
    mock.api.getReadiness.mockResolvedValue(catalogSnapshot([repo('service', replacement)]));
    mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });

    expect(await screen.findByText(/needs reselection/i)).toBeVisible();
    // The replacement keeps the old key and path but is not selected.
    expect(screen.getByRole('checkbox', { name: /^service\b/ })).not.toBeChecked();
  });

  it('keeps invalid, unborn, and identity-less repositories visible but unselectable', async () => {
    const healthy = mockRepoIdentity('/root-a/widget');
    const invalid = mockRepoIdentity('/root-a/broken');
    const unborn = mockRepoIdentity('/root-a/unborn');
    const identityless = mockRepoIdentity('/root-a/ghost');
    const mock = installAgenticoMock({
      defaults: creationDefaults({
        repositories: [
          repo('widget', healthy),
          repo('broken', invalid, {
            valid: false,
            featureReady: false,
            issue: {
              code: 'invalid_repository',
              class: 'blocking',
              title: 'Invalid repository',
              summary: 'A configured repository path is not a git repository.',
            },
          }),
          repo('unborn', unborn, { featureReady: false }),
          repo('ghost', identityless, { identity: undefined }),
        ],
      }),
    });
    const { user } = await renderForm(mock);

    expect(screen.getByRole('checkbox', { name: /^widget\b/ })).toBeEnabled();
    expect(screen.getByRole('checkbox', { name: /^broken\b/ })).toBeDisabled();
    expect(screen.getByRole('checkbox', { name: /^unborn\b/ })).toBeDisabled();
    expect(screen.getByRole('checkbox', { name: /^ghost\b/ })).toBeDisabled();
    expect(screen.getByText(/no commits yet/i)).toBeVisible();
    expect(screen.getByText(/could not resolve this repository's identity/i)).toBeVisible();
    // The unrelated usable repository stays selectable.
    await user.click(screen.getByRole('checkbox', { name: /^widget\b/ }));
    expect(screen.getByRole('checkbox', { name: /^widget\b/ })).toBeChecked();
  });

  it('refreshes the catalog without resetting any draft value', async () => {
    const service = mockRepoIdentity('/root-a/service');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('service', service)] }),
    });
    const { user } = await renderForm(mock);

    await user.click(screen.getByRole('checkbox', { name: /^service\b/ }));
    await user.click(screen.getByRole('radio', { name: 'Current branches' }));
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    await user.type(screen.getByLabelText('Name'), 'Kept draft');
    await user.type(
      screen.getByLabelText('Description'),
      'Repository-file search stays scoped while discovery changes.',
    );

    mock.api.getReadiness.mockResolvedValue(catalogSnapshot([repo('renamed/service', service)]));
    mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });

    await user.click(screen.getByRole('button', { name: 'Back' }));
    expect(await screen.findByRole('checkbox', { name: /renamed\/service/ })).toBeChecked();
    expect(screen.getByRole('radio', { name: 'Current branches' })).toBeChecked();
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    expect(screen.getByLabelText('Name')).toHaveValue('Kept draft');
    expect(screen.getByLabelText('Description')).toHaveValue(
      'Repository-file search stays scoped while discovery changes.',
    );
  });

  it('keeps the last authoritative catalog when a refresh fails, with a retry', async () => {
    const service = mockRepoIdentity('/root-a/service');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('service', service)] }),
    });
    const { user } = await renderForm(mock);

    await user.click(screen.getByRole('checkbox', { name: /^service\b/ }));
    mock.api.getReadiness.mockRejectedValueOnce(
      ipcError('network_unreachable', 'The server could not be reached.', { title: 'Offline' }),
    );
    mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });

    const alert = await screen.findByRole('alert');
    expect(within(alert).getByText('Offline')).toBeVisible();
    // The failed refresh never masquerades as an authoritative empty catalog.
    expect(screen.getByRole('checkbox', { name: /^service\b/ })).toBeChecked();

    mock.api.getReadiness.mockResolvedValueOnce(
      catalogSnapshot([repo('renamed/service', service)]),
    );
    await user.click(within(alert).getByRole('button', { name: 'Retry' }));
    expect(await screen.findByRole('checkbox', { name: /renamed\/service/ })).toBeChecked();
  });

  it('suppresses a stale refresh reply so an older catalog cannot win', async () => {
    const stale = mockRepoIdentity('/root-a/stale');
    const fresh = mockRepoIdentity('/root-a/fresh');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('stale', stale)] }),
    });
    await renderForm(mock);

    let resolveStale!: (snapshot: ReadinessSnapshot) => void;
    mock.api.getReadiness
      .mockImplementationOnce(
        () =>
          new Promise<ReadinessSnapshot>((resolve) => {
            resolveStale = resolve;
          }),
      )
      .mockResolvedValueOnce(catalogSnapshot([repo('fresh', fresh)]));
    mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });
    mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });
    // The stale (older) reply resolves after the newer request started.
    resolveStale(catalogSnapshot([repo('stale', stale)]));

    expect(await screen.findByRole('checkbox', { name: /^fresh\b/ })).toBeVisible();
    expect(screen.queryByRole('checkbox', { name: /^stale\b/ })).toBeNull();
  });

  it('submits slash-containing keys as structured values that follow the identity', async () => {
    const service = mockRepoIdentity('/work/space/service');
    const mock = installAgenticoMock({
      defaults: creationDefaults({
        repositories: [repo('work/space/service', service)],
      }),
    });
    const { user } = await renderForm(mock);

    await user.click(screen.getByRole('checkbox', { name: /work\/space\/service/ }));
    mock.api.getReadiness.mockResolvedValue(catalogSnapshot([repo('service', service)]));
    mock.emitAppEvent({ type: 'invalidated', kind: 'resync' });

    expect(await screen.findByRole('checkbox', { name: /^service\b/ })).toBeChecked();
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    await user.type(screen.getByLabelText('Name'), 'Slash keys');
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));
    await user.click(screen.getByRole('button', { name: /Create/ }));

    await vi.waitFor(() => expect(mock.api.createFeature).toHaveBeenCalled());
    expect(mock.api.createFeature).toHaveBeenCalledWith(
      expect.objectContaining({ repoKeys: ['service'] }),
    );
  });
});

async function reachContractFrom(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
  await user.type(screen.getByLabelText('Name'), 'Identity work');
  await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
  await user.click(screen.getByRole('button', { name: 'Next: Contract' }));
  await user.click(screen.getByRole('button', { name: /Create/ }));
}

describe('repository-file references stay bound to their repository', () => {
  async function attachReference(
    user: ReturnType<typeof userEvent.setup>,
    mock: ReturnType<typeof installAgenticoMock>,
    identity: RepositoryIdentity,
  ) {
    mock.api.searchCreationFiles.mockImplementation((request: { requestId: string }) =>
      Promise.resolve({
        requestId: request.requestId,
        files: [{ repoKey: 'service', path: 'src/query.ts', identity }],
        truncated: false,
        cancelled: false,
      }),
    );
    await user.click(screen.getByRole('checkbox', { name: /^service\b/ }));
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    await user.type(screen.getByLabelText('Name'), 'References');
    await user.type(screen.getByLabelText('Description'), 'See @que');
    const option = await screen.findByRole('option', { name: /service.*src\/query\.ts/ });
    await user.click(option);
    expect(await screen.findByText('@service/src/query.ts')).toBeVisible();
  }

  it('moves a reference and its repository to the same renamed key, retaining paths and description text', async () => {
    const service = mockRepoIdentity('/root-a/service');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('service', service)] }),
    });
    const { user } = await renderForm(mock);
    await attachReference(user, mock, service);

    mock.api.getReadiness.mockResolvedValue(catalogSnapshot([repo('renamed/service', service)]));
    mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });

    expect(await screen.findByText('@renamed/service/src/query.ts')).toBeVisible();
    // The description text stays verbatim: only the transmitted key moved.
    expect(screen.getByLabelText('Description')).toHaveValue('See @service/src/query.ts ');
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));
    await user.click(screen.getByRole('button', { name: /Create/ }));

    await vi.waitFor(() => expect(mock.api.createFeature).toHaveBeenCalled());
    expect(mock.api.createFeature).toHaveBeenCalledWith(
      expect.objectContaining({
        repoKeys: ['renamed/service'],
        repositoryFiles: [{ repoKey: 'renamed/service', path: 'src/query.ts', identity: service }],
        description: 'See @service/src/query.ts ',
      }),
    );
  });

  it('blocks submission on a replacement detected at submission, without losing the draft', async () => {
    const service = mockRepoIdentity('/root-a/service');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('service', service)] }),
    });
    const { user } = await renderForm(mock);
    await attachReference(user, mock, service);

    // The renderer's catalog is still the last authoritative snapshot, so
    // the draft looks complete; the replacement is detected where the file
    // resolves — at submission, against fresh authorized discovery.
    mock.api.createFeature.mockRejectedValue(
      ipcError(
        'E_REPOSITORY_FILE_UNRESOLVED',
        'A referenced repository file belongs to a repository that is no longer available on the server.',
        {
          title: 'The referenced repository is no longer available',
          remediation:
            'Remove the reference, reselect the repository and reference the file again, then retry.',
        },
      ),
    );
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));
    await user.click(screen.getByRole('button', { name: /Create/ }));

    // The failure routes back to the Describe step with the draft intact.
    const alert = await screen.findByRole('alert');
    expect(
      within(alert).getByText('The referenced repository is no longer available'),
    ).toBeVisible();
    expect(screen.getByRole('heading', { name: 'Define the work' })).toBeVisible();
    expect(screen.getByLabelText('Name')).toHaveValue('References');
    expect(screen.getByLabelText('Description')).toHaveValue('See @service/src/query.ts ');

    // The user can remove the stale reference explicitly.
    await user.click(screen.getByRole('button', { name: 'Remove reference service/src/query.ts' }));
    expect(screen.queryByText(/needs reselection/i)).toBeNull();
  });

  it('prunes references when their repository is deliberately deselected', async () => {
    const service = mockRepoIdentity('/root-a/service');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('service', service)] }),
    });
    const { user } = await renderForm(mock);
    await attachReference(user, mock, service);

    await user.click(screen.getByRole('button', { name: 'Back' }));
    await user.click(screen.getByRole('checkbox', { name: /^service\b/ }));
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    expect(screen.queryByText('@service/src/query.ts')).toBeNull();
  });
});
