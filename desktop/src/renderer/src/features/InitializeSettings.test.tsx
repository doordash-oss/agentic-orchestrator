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

import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { ConnectionState, ReadinessSnapshot, RepositoryState } from '../../../shared/ipc';
import {
  cloneOperation,
  installAgenticoMock,
  ipcError,
  mockRepoIdentity,
  readySnapshot,
  type AgenticoMock,
} from '../test/agenticoMock';
import { CloneRepositorySection } from './CloneRepositorySection';
import { WorkspaceRepositoriesSection } from './WorkspaceRepositoriesSection';

afterEach(cleanup);

const ROOTS = [{ path: '/work/space', valid: true, cloneEligible: true }];

function readyConnection(serverKey: string, kind: 'local' | 'remote' = 'local'): ConnectionState {
  return {
    status: 'ready',
    stage: 'ready',
    detail: 'Connected.',
    ownership: 'external',
    kind,
    serverKey,
    serverName: 'local server',
  };
}

function unbornRepo(name: string, path: string): RepositoryState {
  return {
    name,
    path,
    valid: true,
    featureReady: false,
    identity: mockRepoIdentity(path),
  };
}

function snapshotWith(repositories: RepositoryState[]): ReadinessSnapshot {
  return readySnapshot({ repositories, workspaceRoots: ROOTS });
}

/** A succeeded unborn clone operation, as the server reports it. */
function unbornSucceededOperation() {
  return cloneOperation({
    id: 'clone-1',
    state: 'succeeded',
    destination: 'empty-clone',
    destinationPath: '/work/space/empty-clone',
    published: {
      repoKey: 'empty-clone',
      path: '/work/space/empty-clone',
      hasHead: false,
      publishedAt: '2026-09-04T12:00:05Z',
      identity: mockRepoIdentity('/work/space/empty-clone'),
    },
  });
}

function renderCloneSection(mockUnused?: AgenticoMock, readiness?: ReadinessSnapshot) {
  const onReadinessChanged = vi.fn();
  const view = render(
    <CloneRepositorySection
      readiness={readiness ?? snapshotWith([])}
      connection={readyConnection('local-1')}
      onReadinessChanged={onReadinessChanged}
    />,
  );
  return { onReadinessChanged, rerender: view.rerender };
}

function renderRepositoriesSection(readiness: ReadinessSnapshot) {
  const onReadinessChanged = vi.fn();
  render(
    <WorkspaceRepositoriesSection
      readiness={readiness}
      connection={readyConnection('local-1')}
      onReadinessChanged={onReadinessChanged}
    />,
  );
  return { onReadinessChanged };
}

describe('Settings: explicit initialization after an unborn clone success', () => {
  it('offers both actions with the shared consent wording and mutates nothing on Not now', async () => {
    const mock = installAgenticoMock({
      readiness: snapshotWith([unbornRepo('empty-clone', '/work/space/empty-clone')]),
      cloneOperations: [unbornSucceededOperation()],
    });
    void renderCloneSection();
    const user = userEvent.setup();

    const region = screen.getByRole('region', { name: 'Clone repositories' });
    expect(await within(region).findByText(/no commits yet/i)).toBeVisible();
    const offer = await waitFor(() => {
      const node = region.querySelector('[data-initialize-offer]');
      expect(node).not.toBeNull();
      return node as HTMLElement;
    });
    expect(within(offer).getByText(/one empty local commit/i)).toBeVisible();
    within(offer).getByRole('button', { name: 'Create initial commit' });
    within(offer).getByRole('button', { name: 'Not now' });

    // Not now hides the offer and mutates nothing: no request, and the
    // clone's success presentation stays.
    await user.click(within(region).getByRole('button', { name: 'Not now' }));
    await waitFor(() =>
      expect(
        within(region).queryByRole('button', { name: 'Create initial commit' }),
      ).not.toBeInTheDocument(),
    );
    expect(mock.api.initializeRepository).not.toHaveBeenCalled();
    expect(within(region).getByText('Succeeded')).toBeVisible();
  });

  it('sends the consented selector and announces the refreshed repository', async () => {
    const mock = installAgenticoMock({
      readiness: snapshotWith([unbornRepo('empty-clone', '/work/space/empty-clone')]),
      cloneOperations: [unbornSucceededOperation()],
    });
    const { onReadinessChanged } = renderCloneSection();
    const user = userEvent.setup();
    const region = screen.getByRole('region', { name: 'Clone repositories' });

    mock.api.initializeRepository.mockResolvedValue({
      result: 'initialized',
      repoKey: 'empty-clone',
      path: '/work/space/empty-clone',
      hasHead: true,
      root: '/work/space',
      identity: mockRepoIdentity('/work/space/empty-clone'),
    });
    const refreshed = snapshotWith([
      {
        name: 'empty-clone',
        path: '/work/space/empty-clone',
        valid: true,
        featureReady: true,
        identity: mockRepoIdentity('/work/space/empty-clone'),
      },
    ]);
    mock.api.getReadiness.mockResolvedValue(refreshed);

    await user.click(await within(region).findByRole('button', { name: 'Create initial commit' }));

    await waitFor(() => expect(mock.api.initializeRepository).toHaveBeenCalledTimes(1));
    expect(mock.api.initializeRepository).toHaveBeenCalledWith({
      repoKey: 'empty-clone',
      identity: mockRepoIdentity('/work/space/empty-clone'),
      path: '/work/space/empty-clone',
      consent: true,
    });
    expect(
      await within(region).findByText(
        /Initialized empty-clone at \/work\/space\/empty-clone on this computer/,
      ),
    ).toBeVisible();
    await waitFor(() => expect(onReadinessChanged).toHaveBeenCalledWith(refreshed));
  });

  it('announces a refresh-only success without another commit claim', async () => {
    const mock = installAgenticoMock({
      readiness: snapshotWith([unbornRepo('empty-clone', '/work/space/empty-clone')]),
      cloneOperations: [unbornSucceededOperation()],
    });
    void renderCloneSection();
    const user = userEvent.setup();
    const region = screen.getByRole('region', { name: 'Clone repositories' });

    mock.api.initializeRepository.mockResolvedValue({
      result: 'already_initialized',
      repoKey: 'empty-clone',
      path: '/work/space/empty-clone',
      hasHead: true,
      root: '/work/space',
      identity: mockRepoIdentity('/work/space/empty-clone'),
    });

    await user.click(await within(region).findByRole('button', { name: 'Create initial commit' }));
    expect(
      await within(region).findByText(/already had an initial commit on this computer/),
    ).toBeVisible();
  });

  it('surfaces the canonical refusal on the offer and keeps the clone visible', async () => {
    const mock = installAgenticoMock({
      readiness: snapshotWith([unbornRepo('empty-clone', '/work/space/empty-clone')]),
      cloneOperations: [unbornSucceededOperation()],
    });
    const { onReadinessChanged } = renderCloneSection();
    const user = userEvent.setup();
    const region = screen.getByRole('region', { name: 'Clone repositories' });

    mock.api.initializeRepository.mockRejectedValue(
      ipcError(
        'initialize_content_present',
        'The repository has staged, unstaged or untracked files, so the server will not create its initial commit.',
      ),
    );
    await user.click(await within(region).findByRole('button', { name: 'Create initial commit' }));

    expect(await within(region).findByText(/staged, unstaged or untracked files/i)).toBeVisible();
    expect(within(region).getByText('Succeeded')).toBeVisible();
    // The offer stays available for an explicit retry.
    expect(within(region).getByRole('button', { name: 'Create initial commit' })).toBeEnabled();
    await waitFor(() => expect(onReadinessChanged).toHaveBeenCalled());
  });

  it('keeps an initialization error scoped to the operation that failed', async () => {
    const second = cloneOperation({
      ...unbornSucceededOperation(),
      id: 'clone-2',
      destination: 'other-empty',
      destinationPath: '/work/space/other-empty',
      published: {
        repoKey: 'other-empty',
        path: '/work/space/other-empty',
        hasHead: false,
        publishedAt: '2026-09-04T12:00:06Z',
        identity: mockRepoIdentity('/work/space/other-empty'),
      },
    });
    const mock = installAgenticoMock({
      readiness: snapshotWith([]),
      cloneOperations: [unbornSucceededOperation(), second],
    });
    void renderCloneSection();
    const user = userEvent.setup();
    const region = screen.getByRole('region', { name: 'Clone repositories' });
    const firstRow = (await within(region).findByText('empty-clone')).closest('li') as HTMLElement;
    const secondRow = within(region).getByText('other-empty').closest('li') as HTMLElement;

    mock.api.initializeRepository.mockRejectedValue(
      ipcError('initialize_content_present', 'Only empty-clone has user content.'),
    );
    await user.click(within(firstRow).getByRole('button', { name: 'Create initial commit' }));

    expect(await within(firstRow).findByText(/Only empty-clone has user content/)).toBeVisible();
    expect(within(secondRow).queryByText(/Only empty-clone has user content/)).toBeNull();
  });

  it('does not carry a declined operation offer across a server switch', async () => {
    const operation = unbornSucceededOperation();
    const mock = installAgenticoMock({ readiness: snapshotWith([]), cloneOperations: [operation] });
    const { rerender } = renderCloneSection();
    const user = userEvent.setup();
    const region = screen.getByRole('region', { name: 'Clone repositories' });
    await user.click(await within(region).findByRole('button', { name: 'Not now' }));
    expect(within(region).queryByRole('button', { name: 'Create initial commit' })).toBeNull();

    mock.api.listCloneOperations.mockResolvedValue({ operations: [operation] });
    rerender(
      <CloneRepositorySection
        readiness={snapshotWith([])}
        connection={readyConnection('remote-2', 'remote')}
        onReadinessChanged={vi.fn()}
      />,
    );

    expect(
      await within(region).findByRole('button', { name: 'Create initial commit' }),
    ).toBeVisible();
  });

  it('never offers initialization for a clone that has commits', async () => {
    const mock = installAgenticoMock({
      readiness: snapshotWith([]),
      cloneOperations: [
        cloneOperation({
          id: 'clone-2',
          state: 'succeeded',
          destination: 'widget',
          destinationPath: '/work/space/widget',
          published: {
            repoKey: 'widget',
            path: '/work/space/widget',
            hasHead: true,
            publishedAt: '2026-09-04T12:00:05Z',
            identity: mockRepoIdentity('/work/space/widget'),
          },
        }),
      ],
    });
    void renderCloneSection();
    const region = screen.getByRole('region', { name: 'Clone repositories' });
    await within(region).findByText('Succeeded');
    expect(
      within(region).queryByRole('button', { name: 'Create initial commit' }),
    ).not.toBeInTheDocument();
    expect(mock.api.initializeRepository).not.toHaveBeenCalled();
  });
});

describe('Settings: later opt-in on the repository catalog row', () => {
  it('offers the shared wording beside an unborn row and initializes on demand', async () => {
    const mock = installAgenticoMock({
      readiness: snapshotWith([unbornRepo('empty-clone', '/work/space/empty-clone')]),
    });
    const { onReadinessChanged } = renderRepositoriesSection(
      snapshotWith([unbornRepo('empty-clone', '/work/space/empty-clone')]),
    );
    const user = userEvent.setup();

    const region = screen.getByRole('region', { name: 'Repositories' });
    const row = within(region).getByText('empty-clone').closest('li') as HTMLElement;
    expect(within(row).getByText(/No commits yet/i)).toBeVisible();

    await user.click(within(row).getByRole('button', { name: /Create initial commit…/ }));
    // The inline offer carries the shared consent wording and both actions.
    expect(within(row).getByText(/one empty local commit/i)).toBeVisible();
    await within(row).findByRole('button', { name: /^Not now$/ });

    mock.api.initializeRepository.mockResolvedValue({
      result: 'initialized',
      repoKey: 'empty-clone',
      path: '/work/space/empty-clone',
      hasHead: true,
      root: '/work/space',
      identity: mockRepoIdentity('/work/space/empty-clone'),
    });
    const refreshed = snapshotWith([
      {
        name: 'empty-clone',
        path: '/work/space/empty-clone',
        valid: true,
        featureReady: true,
        identity: mockRepoIdentity('/work/space/empty-clone'),
      },
    ]);
    mock.api.getReadiness.mockResolvedValue(refreshed);

    await user.click(within(row).getByRole('button', { name: /^Create initial commit$/ }));

    expect(await mock.api.initializeRepository.mock.calls[0]?.[0]).toMatchObject({
      repoKey: 'empty-clone',
      identity: mockRepoIdentity('/work/space/empty-clone'),
      path: '/work/space/empty-clone',
      consent: true,
    });
    expect(
      await within(region).findByText(/Initialized empty-clone at \/work\/space\/empty-clone/),
    ).toBeVisible();
    await waitFor(() => expect(onReadinessChanged).toHaveBeenCalledWith(refreshed));
  });

  it('reconciles a rejected attempt and does not carry its error to another row', async () => {
    const repositories = [
      unbornRepo('empty-clone', '/work/space/empty-clone'),
      unbornRepo('other-empty', '/work/space/other-empty'),
    ];
    const mock = installAgenticoMock({ readiness: snapshotWith(repositories) });
    const { onReadinessChanged } = renderRepositoriesSection(snapshotWith(repositories));
    const user = userEvent.setup();
    const region = screen.getByRole('region', { name: 'Repositories' });
    const firstRow = within(region).getByText('empty-clone').closest('li') as HTMLElement;
    const secondRow = within(region).getByText('other-empty').closest('li') as HTMLElement;
    const reconciled = snapshotWith(repositories);
    mock.api.getReadiness.mockResolvedValue(reconciled);
    mock.api.initializeRepository.mockRejectedValue(
      ipcError('initialize_unavailable', 'The response was lost after initialization started.'),
    );

    await user.click(within(firstRow).getByRole('button', { name: /Create initial commit…/ }));
    await user.click(within(firstRow).getByRole('button', { name: /^Create initial commit$/ }));
    expect(await within(firstRow).findByText(/response was lost/i)).toBeVisible();
    await waitFor(() => expect(onReadinessChanged).toHaveBeenCalledWith(reconciled));

    await user.click(within(secondRow).getByRole('button', { name: /Create initial commit…/ }));
    expect(within(secondRow).queryByText(/response was lost/i)).toBeNull();
  });

  it('keeps a feature-ready row actionless and announces refresh-only success', async () => {
    installAgenticoMock({
      readiness: snapshotWith([]),
    });
    const ready = snapshotWith([
      {
        name: 'widget',
        path: '/work/space/widget',
        valid: true,
        featureReady: true,
        identity: mockRepoIdentity('/work/space/widget'),
      },
    ]);
    void renderRepositoriesSection(ready);
    const region = screen.getByRole('region', { name: 'Repositories' });
    expect(within(region).getByText(/Ready for feature work/i)).toBeVisible();
    expect(
      within(region).queryByRole('button', { name: /Create initial commit/ }),
    ).not.toBeInTheDocument();
  });
});
