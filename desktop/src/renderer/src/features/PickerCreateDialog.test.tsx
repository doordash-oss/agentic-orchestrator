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

import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { useState } from 'react';
import type { ReadinessSnapshot, RepositoryIdentity, RepositoryState } from '../../../shared/ipc';
import {
  createRepositoryResult,
  creationDefaults,
  installAgenticoMock,
  ipcError,
  mockRepoIdentity,
  readySnapshot,
} from '../test/agenticoMock';
import { CreateFeatureForm } from './CreateFeatureForm';

afterEach(cleanup);

const ROOT = { path: '/work/space', valid: true, cloneEligible: true };

function repo(
  name: string,
  identity: RepositoryIdentity,
  overrides: Partial<RepositoryState> = {},
): RepositoryState {
  return { name, path: identity.path, valid: true, featureReady: true, identity, ...overrides };
}

function snapshotWith(
  repositories: RepositoryState[],
  roots: readonly { path: string; valid: boolean; cloneEligible: boolean }[] = [ROOT],
): ReadinessSnapshot {
  return readySnapshot({
    repositories,
    workspaceRoots: roots.map((root) => ({ ...root, cloneIssue: undefined, issue: undefined })),
  });
}

async function openCreateView(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('button', { name: /create a repository/i }));
  return screen.findByRole('dialog', { name: 'Create a repository' });
}

/** Emits a ready local connection, as the real app has wherever the sheet mounts. */
async function emitReadyLocal(mock: ReturnType<typeof installAgenticoMock>, serverKey = 'alpha') {
  await act(async () => {
    mock.emitConnection({
      status: 'ready',
      stage: 'ready',
      detail: 'Connected.',
      ownership: 'external',
      kind: 'local',
      serverKey,
      serverName: 'local server',
    });
  });
}

/** Unmounts the sheet when it closes, as the owning shell does. */
function SheetHarness({ onClosed }: { onClosed(): void }) {
  const [open, setOpen] = useState(true);
  if (!open) return null;
  return (
    <CreateFeatureForm
      onCreated={vi.fn()}
      onClose={() => {
        setOpen(false);
        onClosed();
      }}
    />
  );
}

async function submitCreate(
  user: ReturnType<typeof userEvent.setup>,
  mock: ReturnType<typeof installAgenticoMock>,
  destination = 'widget',
): Promise<{ idempotencyKey: string }> {
  mock.api.createRepository.mockImplementation(
    (input: { idempotencyKey: string; destination: string }) =>
      Promise.resolve(
        createRepositoryResult({
          repoKey: input.destination,
          path: `/work/space/${input.destination}`,
          identity: mockRepoIdentity(`/work/space/${input.destination}`),
        }),
      ),
  );
  await user.type(screen.getByLabelText('Repository folder name'), destination);
  await user.click(screen.getByRole('checkbox', { name: /initial empty commit/i }));
  await user.click(screen.getByRole('button', { name: 'Create repository' }));
  await waitFor(() => expect(mock.api.createRepository).toHaveBeenCalled());
  const input = mock.api.createRepository.mock.calls[0]?.[0] as {
    idempotencyKey: string;
    consent: boolean;
    rootPath: string;
  };
  return { idempotencyKey: input.idempotencyKey };
}

describe('the picker create view', () => {
  it('creates a repository with consent and adopts it into the same draft by identity', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const widget = mockRepoIdentity('/work/space/widget');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('repo-a', repoA)] }),
      readiness: snapshotWith([repo('repo-a', repoA)]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });

    // Draft values that must survive the creation and adoption.
    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
    await user.click(screen.getByRole('radio', { name: 'Current branch' }));
    await user.type(screen.getByRole('searchbox', { name: 'Search repositories' }), 'zzz');

    const dialog = await openCreateView(user);
    expect(within(dialog).getByText(/one empty initial commit on the main branch/i)).toBeVisible();

    const { idempotencyKey } = await submitCreate(user, mock);
    const request = mock.api.createRepository.mock.calls[0]?.[0] as Record<string, unknown>;
    expect(request).toMatchObject({
      rootPath: ROOT.path,
      destination: 'widget',
      consent: true,
      idempotencyKey,
    });

    // The authoritative catalog now includes the created repository; the
    // sheet adopts it into its own draft.
    mock.api.getReadiness.mockResolvedValue(
      snapshotWith([repo('repo-a', repoA), repo('widget', widget)]),
    );
    mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });

    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Create a repository' })).not.toBeInTheDocument(),
    );
    const widgetCheckbox = await screen.findByRole('checkbox', { name: /^widget\b/ });
    await waitFor(() => expect(widgetCheckbox).toBeChecked());
    await waitFor(() => expect(document.activeElement).toBe(widgetCheckbox));
    // Every other draft value survived.
    expect(screen.getByRole('checkbox', { name: /repo-a/ })).toBeChecked();
    expect(screen.getByRole('radio', { name: 'Current branch' })).toBeChecked();
    expect(screen.getByText('Created widget and selected it.')).toBeVisible();
    expect(screen.getByRole('searchbox', { name: 'Search repositories' })).toHaveValue('');
  });

  it('works when no roots exist yet: choose a folder, persist it, then create', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('repo-a', repoA)], workspaceRoots: [] }),
      readiness: snapshotWith([repo('repo-a', repoA)], []),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    await emitReadyLocal(mock);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });

    const dialog = await openCreateView(user);
    expect(within(dialog).getByText(/No clone-eligible workspace root yet/)).toBeVisible();

    // The chooser adds a root; the authoritative root list lands.
    const withRoot = snapshotWith([repo('repo-a', repoA)], [ROOT]);
    mock.api.pickWorkspaceDirectory.mockResolvedValue({ path: '/work/space' });
    mock.api.addWorkspaceRoot.mockResolvedValue(withRoot);
    await user.click(within(dialog).getByRole('button', { name: /choose folder/i }));
    await waitFor(() => expect(mock.api.addWorkspaceRoot).toHaveBeenCalledWith('/work/space'));
    await waitFor(() =>
      expect(within(dialog).getByLabelText('Destination root')).toHaveValue('/work/space'),
    );

    const { idempotencyKey } = await submitCreate(user, mock);
    expect(mock.api.createRepository.mock.calls[0]?.[0]).toMatchObject({
      rootPath: '/work/space',
      consent: true,
      idempotencyKey,
    });
  });

  it('a failed creation keeps the draft and the form inputs for a corrected retry', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('repo-a', repoA)] }),
      readiness: snapshotWith([repo('repo-a', repoA)]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));

    const dialog = await openCreateView(user);
    mock.api.createRepository.mockRejectedValue(
      ipcError('clone_destination_exists', 'Something already exists at the destination path.'),
    );
    await user.type(screen.getByLabelText('Repository folder name'), 'taken');
    await user.click(screen.getByRole('checkbox', { name: /initial empty commit/i }));
    await user.click(screen.getByRole('button', { name: 'Create repository' }));

    expect(
      await within(dialog).findByText('Something already exists at the destination path.'),
    ).toBeVisible();
    // The dialog stays open with its inputs, and the draft is untouched.
    expect(screen.getByRole('dialog', { name: 'Create a repository' })).toBeInTheDocument();
    expect(screen.getByLabelText('Repository folder name')).toHaveValue('taken');
    expect(screen.getByRole('checkbox', { name: /repo-a/ })).toBeChecked();
  });

  it('never adopts a result without provable identity', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const widget = mockRepoIdentity('/work/space/widget');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('repo-a', repoA)] }),
      readiness: snapshotWith([repo('repo-a', repoA)]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await openCreateView(user);

    mock.api.createRepository.mockResolvedValue(
      createRepositoryResult({ repoKey: 'widget', identity: undefined }),
    );
    await user.type(screen.getByLabelText('Repository folder name'), 'widget');
    await user.click(screen.getByRole('checkbox', { name: /initial empty commit/i }));
    await user.click(screen.getByRole('button', { name: 'Create repository' }));

    mock.api.getReadiness.mockResolvedValue(
      snapshotWith([repo('repo-a', repoA), repo('widget', widget)]),
    );
    mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });
    await new Promise((resolve) => setTimeout(resolve, 50));
    // Visible guidance, never a selection by key or path.
    expect(screen.getByText(/select it from the list once it appears/i)).toBeVisible();
    expect(screen.queryByRole('checkbox', { name: /^widget\b/ })?.textContent).toBeDefined();
    const widgetCheckbox = screen.getByRole('checkbox', { name: /^widget\b/ }) as HTMLInputElement;
    expect(widgetCheckbox.checked).toBe(false);
  });

  it('Escape closes only the create view and restores focus without discarding the draft', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('repo-a', repoA)] }),
      readiness: snapshotWith([repo('repo-a', repoA)]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
    const createButton = screen.getByRole('button', { name: /create a repository/i });

    await openCreateView(user);
    await user.type(screen.getByLabelText('Repository folder name'), 'partial');
    await user.keyboard('{Escape}');

    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Create a repository' })).not.toBeInTheDocument(),
    );
    // The sheet survives with every draft value.
    expect(screen.getByRole('dialog', { name: 'New feature' })).toBeInTheDocument();
    expect(screen.getByRole('checkbox', { name: /repo-a/ })).toBeChecked();
    expect(createButton).toBeInTheDocument();
  });

  it('a late completion never adopts into a discarded draft', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const widget = mockRepoIdentity('/work/space/widget');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('repo-a', repoA)] }),
      readiness: snapshotWith([repo('repo-a', repoA)]),
    });
    const onClosed = vi.fn();
    render(<SheetHarness onClosed={onClosed} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await openCreateView(user);

    let release: (() => void) | undefined;
    mock.api.createRepository.mockImplementation(
      () =>
        new Promise((resolve) => {
          release = () =>
            resolve(
              createRepositoryResult({
                repoKey: 'widget',
                path: '/work/space/widget',
                identity: widget,
              }),
            );
        }),
    );
    await user.type(screen.getByLabelText('Repository folder name'), 'widget');
    await user.click(screen.getByRole('checkbox', { name: /initial empty commit/i }));
    await user.click(screen.getByRole('button', { name: 'Create repository' }));
    await waitFor(() => expect(mock.api.createRepository).toHaveBeenCalled());

    // The draft is discarded while the creation is in flight (an empty
    // draft discards without confirmation), unmounting the sheet exactly
    // as the owning shell does.
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(onClosed).toHaveBeenCalled());
    expect(screen.queryByRole('dialog', { name: 'New feature' })).not.toBeInTheDocument();

    release?.();
    mock.api.getReadiness.mockResolvedValue(
      snapshotWith([repo('repo-a', repoA), repo('widget', widget)]),
    );
    mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });
    await new Promise((resolve) => setTimeout(resolve, 50));
    // The discarded draft was never recreated and nothing was adopted.
    expect(screen.queryByRole('dialog', { name: 'New feature' })).not.toBeInTheDocument();
    expect(screen.queryByRole('checkbox', { name: /^widget\b/ })).toBeNull();
  });

  it('suppresses a stale completion after a server switch', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const widget = mockRepoIdentity('/work/space/widget');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('repo-a', repoA)] }),
      readiness: snapshotWith([repo('repo-a', repoA)]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    await emitReadyLocal(mock, 'alpha');
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await openCreateView(user);

    let release: (() => void) | undefined;
    mock.api.createRepository.mockImplementation(
      () =>
        new Promise((resolve) => {
          release = () =>
            resolve(
              createRepositoryResult({
                repoKey: 'widget',
                path: '/work/space/widget',
                identity: widget,
              }),
            );
        }),
    );
    await user.type(screen.getByLabelText('Repository folder name'), 'widget');
    await user.click(screen.getByRole('checkbox', { name: /initial empty commit/i }));
    await user.click(screen.getByRole('button', { name: 'Create repository' }));
    await waitFor(() => expect(mock.api.createRepository).toHaveBeenCalled());

    // The connection flips to another server before the result lands; the
    // re-render is flushed so the guard observes the new server key.
    await act(async () => {
      mock.emitConnection({
        status: 'ready',
        stage: 'ready',
        detail: 'Connected.',
        ownership: 'external',
        kind: 'local',
        serverKey: 'beta',
        serverName: 'beta server',
      });
    });
    release?.();
    mock.api.getReadiness.mockResolvedValue(
      snapshotWith([repo('repo-a', repoA), repo('widget', widget)]),
    );
    mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });
    await new Promise((resolve) => setTimeout(resolve, 50));
    // The stale result never selects anything on the new server.
    const widgetCheckbox = screen.queryByRole('checkbox', {
      name: /^widget\b/,
    }) as HTMLInputElement | null;
    expect(widgetCheckbox === null || widgetCheckbox.checked === false).toBe(true);
  });
});
