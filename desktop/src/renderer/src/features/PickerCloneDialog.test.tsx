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
import type {
  CloneOperation,
  ClonePublication,
  ReadinessSnapshot,
  RepositoryIdentity,
  RepositoryState,
} from '../../../shared/ipc';
import {
  cloneOperation,
  creationDefaults,
  installAgenticoMock,
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

function publication(
  repoKey: string,
  identity: RepositoryIdentity | undefined,
  hasHead = true,
): ClonePublication {
  return {
    repoKey,
    path: identity?.path ?? '/work/space/widget',
    hasHead,
    publishedAt: '2026-09-04T12:00:05Z',
    ...(identity === undefined ? {} : { identity }),
  };
}

/** A succeeded snapshot for the attempt the draft actually submitted. */
function succeededOperation(
  mock: ReturnType<typeof installAgenticoMock>,
  published: ClonePublication,
): CloneOperation {
  const calls = mock.api.startClone.mock.calls as Array<[{ idempotencyKey: string }, ...unknown[]]>;
  const key = calls[calls.length - 1]?.[0]?.idempotencyKey;
  return cloneOperation({ id: 'clone-1', state: 'succeeded', idempotencyKey: key, published });
}

async function openCloneView(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('button', { name: /clone a repository/i }));
  return screen.findByRole('dialog', { name: 'Clone a repository' });
}

async function submitClone(
  user: ReturnType<typeof userEvent.setup>,
  mock: ReturnType<typeof installAgenticoMock>,
  url = 'https://example.com/acme/widget.git',
): Promise<{ idempotencyKey: string }> {
  // The started operation echoes the request's idempotency key, exactly
  // like the server: the draft's association and the tracked snapshot stay
  // the same attempt. Both mocks are staged before the click so no tracker
  // refresh can observe a mismatched snapshot.
  const startedKeyed = (id: string) => {
    const calls = mock.api.startClone.mock.calls as Array<
      [{ idempotencyKey: string }, ...unknown[]]
    >;
    const key = calls[calls.length - 1]?.[0]?.idempotencyKey;
    return cloneOperation({
      id,
      state: 'running',
      ...(key === undefined ? {} : { idempotencyKey: key }),
    });
  };
  mock.api.startClone.mockImplementation((input: { idempotencyKey: string }) =>
    Promise.resolve(
      cloneOperation({ id: 'clone-1', state: 'running', idempotencyKey: input.idempotencyKey }),
    ),
  );
  mock.api.getCloneOperation.mockImplementation(() => Promise.resolve(startedKeyed('clone-1')));
  await user.type(screen.getByLabelText('Repository URL'), url);
  await user.click(screen.getByRole('button', { name: 'Clone repository' }));
  await waitFor(() => expect(mock.api.startClone).toHaveBeenCalled());
  const input = mock.api.startClone.mock.calls[0]?.[0] as { idempotencyKey: string };
  return { idempotencyKey: input.idempotencyKey };
}

/** Emits the server's invalidations for a finished clone: the operation event, then discovery. */
function emitCloneCompletion(
  mock: ReturnType<typeof installAgenticoMock>,
  operation: CloneOperation,
  snapshot: ReadinessSnapshot,
): void {
  mock.api.getCloneOperation.mockResolvedValue(operation);
  mock.api.getReadiness.mockResolvedValue(snapshot);
  mock.emitAppEvent({ type: 'invalidated', kind: 'clone.updated' });
  mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });
}

describe('the picker clone view', () => {
  it('adopts a usable clone into its own draft once and returns focus to the selected row', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const widget = mockRepoIdentity('/work/space/widget');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('repo-a', repoA)] }),
      readiness: snapshotWith([repo('repo-a', repoA)]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });

    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
    await user.click(screen.getByRole('radio', { name: 'Current branch' }));
    // A search filter that would hide the focus target.
    await user.type(screen.getByRole('searchbox', { name: 'Search repositories' }), 'zzz');

    await openCloneView(user);
    const { idempotencyKey } = await submitClone(user, mock);
    expect(mock.api.startClone).toHaveBeenCalledWith(
      expect.objectContaining({ idempotencyKey, rootPath: ROOT.path, destination: 'widget' }),
    );
    expect(await screen.findByText('Cloning')).toBeVisible();

    emitCloneCompletion(
      mock,
      succeededOperation(mock, publication('widget', widget)),
      snapshotWith([repo('repo-a', repoA), repo('widget', widget)]),
    );

    // The nested view closes, the published repository is selected under
    // its current key, and focus lands on its row.
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Clone a repository' })).not.toBeInTheDocument(),
    );
    const widgetCheckbox = await screen.findByRole('checkbox', { name: /^widget\b/ });
    await waitFor(() => expect(widgetCheckbox).toBeChecked());
    await waitFor(() => expect(document.activeElement).toBe(widgetCheckbox));
    expect(screen.getByRole('checkbox', { name: /repo-a/ })).toBeChecked();
    expect(screen.getByRole('radio', { name: 'Current branch' })).toBeChecked();
    expect(screen.getByText('Cloned widget and selected it.')).toBeVisible();
    // The filter that would have hidden the row was cleared.
    expect(screen.getByRole('searchbox', { name: 'Search repositories' })).toHaveValue('');

    // Repeated invalidations never re-adopt or duplicate the selection.
    mock.emitAppEvent({ type: 'invalidated', kind: 'clone.updated' });
    mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });
    await new Promise((resolve) => setTimeout(resolve, 50));
    const checked = screen
      .getAllByRole('checkbox')
      .filter((element) => (element as HTMLInputElement).checked);
    expect(checked).toHaveLength(2);
  });

  it('keeps a succeeded unborn clone visible with guidance and never selects it', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const widget = mockRepoIdentity('/work/space/widget');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('repo-a', repoA)] }),
      readiness: snapshotWith([repo('repo-a', repoA)]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await openCloneView(user);
    await submitClone(user, mock);

    emitCloneCompletion(
      mock,
      succeededOperation(mock, publication('widget', widget, false)),
      snapshotWith([repo('repo-a', repoA), repo('widget', widget, { featureReady: false })]),
    );

    const dialog = screen.getByRole('dialog', { name: 'Clone a repository' });
    expect(await within(dialog).findByText(/no commits yet/i)).toBeVisible();
    expect(within(dialog).getByText('Succeeded')).toBeVisible();
    // The view stays open with the guidance; nothing is selected.
    expect(screen.getByRole('dialog', { name: 'Clone a repository' })).toBeInTheDocument();
    await waitFor(() => {
      const unborn = screen.getByRole('checkbox', { name: /^widget\b/ });
      expect(unborn).not.toBeChecked();
      expect(unborn).toBeDisabled();
    });
  });

  it('never adopts a publication without provable identity', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('repo-a', repoA)] }),
      readiness: snapshotWith([repo('repo-a', repoA)]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await openCloneView(user);
    await submitClone(user, mock);

    // An older record whose publication carries no identity: viewable,
    // never auto-adopted by its key or path.
    emitCloneCompletion(
      mock,
      succeededOperation(mock, publication('widget', undefined)),
      snapshotWith([repo('repo-a', repoA)]),
    );

    expect(await screen.findByText('Succeeded')).toBeVisible();
    expect(screen.getByText(/published as widget/i)).toBeVisible();
    expect(screen.getByRole('dialog', { name: 'Clone a repository' })).toBeInTheDocument();
    expect(screen.queryByRole('checkbox', { name: /^widget\b/ })).not.toBeInTheDocument();
  });

  it('adopts identity-safely when the clone duplicates an existing repository name', async () => {
    const existing = mockRepoIdentity('/root-b/widget');
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const clone = mockRepoIdentity('/work/space/widget');
    const before = snapshotWith(
      [repo('widget', existing), repo('repo-a', repoA)],
      [ROOT, { path: '/root-b', valid: true, cloneEligible: true }],
    );
    const mock = installAgenticoMock({
      defaults: creationDefaults({
        repositories: [repo('widget', existing), repo('repo-a', repoA)],
      }),
      readiness: before,
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await user.click(screen.getByRole('checkbox', { name: /^widget\b/ }));
    await openCloneView(user);
    await submitClone(user, mock);

    // Publication renames both same-named repositories into their
    // root-qualified keys; the existing selection follows its identity.
    emitCloneCompletion(
      mock,
      succeededOperation(mock, publication('space/widget', clone)),
      snapshotWith(
        [repo('root-b/widget', existing), repo('space/widget', clone), repo('repo-a', repoA)],
        [ROOT, { path: '/root-b', valid: true, cloneEligible: true }],
      ),
    );

    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Clone a repository' })).not.toBeInTheDocument(),
    );
    expect(screen.getByRole('checkbox', { name: /root-b\/widget/ })).toBeChecked();
    expect(screen.getByRole('checkbox', { name: /space\/widget/ })).toBeChecked();
    const checked = screen
      .getAllByRole('checkbox')
      .filter((element) => (element as HTMLInputElement).checked);
    expect(checked).toHaveLength(2);
  });

  it('preserves the draft through failure, cancellation, and Close', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('repo-a', repoA)] }),
      readiness: snapshotWith([repo('repo-a', repoA)]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
    await openCloneView(user);
    await submitClone(user, mock);

    // Explicit cancellation uses the server cancellation contract.
    mock.api.cancelCloneOperation.mockResolvedValue(
      cloneOperation({ id: 'clone-1', state: 'cancelling', cancelRequested: true }),
    );
    await user.click(await screen.findByRole('button', { name: 'Cancel clone' }));
    await waitFor(() => expect(mock.api.cancelCloneOperation).toHaveBeenCalledWith('clone-1'));

    mock.api.getCloneOperation.mockResolvedValue(
      cloneOperation({ id: 'clone-1', state: 'cancelled' }),
    );
    mock.emitAppEvent({ type: 'invalidated', kind: 'clone.updated' });
    expect(await screen.findByText('Cancelled')).toBeVisible();

    // Close detaches the view; the draft underneath is untouched.
    await user.click(screen.getByRole('button', { name: 'Close' }));
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Clone a repository' })).not.toBeInTheDocument(),
    );
    expect(screen.getByRole('form', { name: /create a feature/i })).toBeInTheDocument();
    expect(screen.getByRole('checkbox', { name: /repo-a/ })).toBeChecked();
  });

  it('routes Escape to the clone view only and never discards the draft', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('repo-a', repoA)] }),
      readiness: snapshotWith([repo('repo-a', repoA)]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
    const dialog = await openCloneView(user);

    await user.keyboard('{Escape}');

    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Clone a repository' })).not.toBeInTheDocument(),
    );
    expect(screen.getByRole('form', { name: /create a feature/i })).toBeInTheDocument();
    expect(screen.getByRole('checkbox', { name: /repo-a/ })).toBeChecked();
    expect(
      within(dialog as HTMLElement).queryByRole('button', { name: 'Discard draft' }),
    ).toBeNull();
  });

  it('retries with a fresh attempt that stays associated with the same draft', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const widget = mockRepoIdentity('/work/space/widget');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('repo-a', repoA)] }),
      readiness: snapshotWith([repo('repo-a', repoA)]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await openCloneView(user);
    await submitClone(user, mock);

    mock.api.getCloneOperation.mockResolvedValue(
      cloneOperation({ id: 'clone-1', state: 'failed' }),
    );
    mock.emitAppEvent({ type: 'invalidated', kind: 'clone.updated' });
    expect(await screen.findByText('Failed')).toBeVisible();

    // Fresh retry: a new operation and idempotency key, same live draft.
    const fresh = cloneOperation({
      id: 'clone-2',
      state: 'running',
      idempotencyKey: 'retry-fresh-key',
    });
    mock.api.retryCloneOperation.mockResolvedValue(fresh);
    mock.api.getCloneOperation.mockResolvedValue(fresh);
    await user.click(screen.getByRole('button', { name: 'Retry clone' }));
    await waitFor(() => expect(mock.api.retryCloneOperation).toHaveBeenCalledWith('clone-1'));
    expect(await screen.findByText('Cloning')).toBeVisible();

    mock.api.getCloneOperation.mockResolvedValue(
      cloneOperation({
        id: 'clone-2',
        state: 'succeeded',
        idempotencyKey: 'retry-fresh-key',
        published: publication('widget', widget),
      }),
    );
    mock.api.getReadiness.mockResolvedValue(
      snapshotWith([repo('repo-a', repoA), repo('widget', widget)]),
    );
    mock.emitAppEvent({ type: 'invalidated', kind: 'clone.updated' });
    mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });

    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Clone a repository' })).not.toBeInTheDocument(),
    );
    expect(screen.getByRole('checkbox', { name: /^widget\b/ })).toBeChecked();
  });

  it('recovers a lost acceptance by idempotency key and adopts the result', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const widget = mockRepoIdentity('/work/space/widget');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('repo-a', repoA)] }),
      readiness: snapshotWith([repo('repo-a', repoA)]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await openCloneView(user);

    // The start reply is lost: the association keeps only its key.
    let rejectStart!: (error: Error) => void;
    mock.api.startClone.mockImplementation(
      () =>
        new Promise((_resolve, reject) => {
          rejectStart = reject;
        }),
    );
    mock.api.listCloneOperations.mockResolvedValue({ operations: [] });
    await user.type(screen.getByLabelText('Repository URL'), 'https://example.com/acme/widget.git');
    await user.click(screen.getByRole('button', { name: 'Clone repository' }));
    const input = await waitFor(() => {
      const call = mock.api.startClone.mock.calls[0]?.[0] as { idempotencyKey: string };
      expect(call).toBeDefined();
      return call;
    });

    // The server did accept: arm the keyed lookup with the captured key
    // before releasing the lost reply.
    const accepted = cloneOperation({
      id: 'clone-9',
      state: 'running',
      idempotencyKey: input.idempotencyKey,
    });
    mock.api.listCloneOperations.mockResolvedValue({ operations: [accepted] });
    mock.api.getCloneOperation.mockResolvedValue(accepted);
    rejectStart(new Error('transport lost'));
    expect(await screen.findByText('Cloning')).toBeVisible();

    mock.api.getCloneOperation.mockResolvedValue(
      cloneOperation({
        id: 'clone-9',
        state: 'succeeded',
        idempotencyKey: input.idempotencyKey,
        published: publication('widget', widget),
      }),
    );
    mock.api.getReadiness.mockResolvedValue(
      snapshotWith([repo('repo-a', repoA), repo('widget', widget)]),
    );
    mock.emitAppEvent({ type: 'invalidated', kind: 'clone.updated' });
    mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });

    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Clone a repository' })).not.toBeInTheDocument(),
    );
    expect(screen.getByRole('checkbox', { name: /^widget\b/ })).toBeChecked();
  });
});

describe('background completion reconciles into only the owning draft', () => {
  it('adopts a usable result that completes while the clone view is closed', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const widget = mockRepoIdentity('/work/space/widget');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('repo-a', repoA)] }),
      readiness: snapshotWith([repo('repo-a', repoA)]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await openCloneView(user);
    await submitClone(user, mock);
    await screen.findByText('Cloning');

    // Close detaches the view; the clone keeps running on the server.
    await user.click(screen.getByRole('button', { name: 'Close' }));
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Clone a repository' })).not.toBeInTheDocument(),
    );

    emitCloneCompletion(
      mock,
      succeededOperation(mock, publication('widget', widget)),
      snapshotWith([repo('repo-a', repoA), repo('widget', widget)]),
    );

    // The selection lands in the owning draft without reopening anything.
    const adopted = await screen.findByRole('checkbox', { name: /^widget\b/ });
    await waitFor(() => expect(adopted).toBeChecked());
    expect(screen.queryByRole('dialog', { name: 'Clone a repository' })).not.toBeInTheDocument();
  });

  it('applies a background completion without leaving the current wizard step', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const widget = mockRepoIdentity('/work/space/widget');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('repo-a', repoA)] }),
      readiness: snapshotWith([repo('repo-a', repoA)]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await openCloneView(user);
    await submitClone(user, mock);
    await user.click(screen.getByRole('button', { name: 'Close' }));

    // The draft moves on to later steps while the clone runs in background.
    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    await user.type(screen.getByLabelText('Name'), 'Background clone');
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));

    emitCloneCompletion(
      mock,
      succeededOperation(mock, publication('widget', widget)),
      snapshotWith([repo('repo-a', repoA), repo('widget', widget)]),
    );

    await waitFor(() =>
      expect(screen.queryByRole('heading', { name: 'Set the depth' })).toBeVisible(),
    );
    expect(screen.queryByRole('heading', { name: 'Choose repositories' })).toBeNull();

    // Returning to the picker lands focus on the adopted row.
    await user.click(screen.getByRole('button', { name: 'Back' }));
    await user.click(screen.getByRole('button', { name: 'Back' }));
    const widgetCheckbox = await screen.findByRole('checkbox', { name: /^widget\b/ });
    await waitFor(() => expect(widgetCheckbox).toBeChecked());
    await waitFor(() => expect(document.activeElement).toBe(widgetCheckbox));
    expect(screen.getByText('Cloned widget and selected it.')).toBeVisible();
  });

  it('never re-selects after the user deliberately deselected the adopted repository', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const widget = mockRepoIdentity('/work/space/widget');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('repo-a', repoA)] }),
      readiness: snapshotWith([repo('repo-a', repoA)]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await openCloneView(user);
    await submitClone(user, mock);
    await user.click(screen.getByRole('button', { name: 'Close' }));

    emitCloneCompletion(
      mock,
      succeededOperation(mock, publication('widget', widget)),
      snapshotWith([repo('repo-a', repoA), repo('widget', widget)]),
    );
    const adopted = await screen.findByRole('checkbox', { name: /^widget\b/ });
    await waitFor(() => expect(adopted).toBeChecked());

    // The user changes their mind; repeated success must not re-select.
    await user.click(screen.getByRole('checkbox', { name: /^widget\b/ }));
    expect(screen.getByRole('checkbox', { name: /^widget\b/ })).not.toBeChecked();
    mock.emitAppEvent({ type: 'invalidated', kind: 'clone.updated' });
    mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(screen.getByRole('checkbox', { name: /^widget\b/ })).not.toBeChecked();
  });

  it('never adopts a replacement that took over the published path', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const widget = mockRepoIdentity('/work/space/widget');
    const replacement = mockRepoIdentity('/work/space/widget', { inode: '9999' });
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('repo-a', repoA)] }),
      readiness: snapshotWith([repo('repo-a', repoA)]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await openCloneView(user);
    await submitClone(user, mock);
    await user.click(screen.getByRole('button', { name: 'Close' }));

    // The publication succeeded, but a replacement now sits at the same
    // path: the pinned identity cannot be satisfied, so nothing is adopted.
    emitCloneCompletion(
      mock,
      succeededOperation(mock, publication('widget', widget)),
      snapshotWith([repo('repo-a', repoA), repo('widget', replacement)]),
    );

    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(screen.getByRole('checkbox', { name: /^widget\b/ })).not.toBeChecked();
    expect(screen.queryByRole('dialog', { name: 'Clone a repository' })).not.toBeInTheDocument();
  });

  it('keeps the attempt association through list refresh failures', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('repo-a', repoA)] }),
      readiness: snapshotWith([repo('repo-a', repoA)]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await openCloneView(user);

    // The start reply is lost and the first list refresh fails: the
    // association survives for the next refresh to recover.
    let rejectStart!: (error: Error) => void;
    mock.api.startClone.mockImplementation(
      () =>
        new Promise((_resolve, reject) => {
          rejectStart = reject;
        }),
    );
    mock.api.listCloneOperations.mockRejectedValue(new Error('list unavailable'));
    await user.type(screen.getByLabelText('Repository URL'), 'https://example.com/acme/widget.git');
    await user.click(screen.getByRole('button', { name: 'Clone repository' }));
    const input = await waitFor(() => {
      const call = mock.api.startClone.mock.calls[0]?.[0] as { idempotencyKey: string };
      expect(call).toBeDefined();
      return call;
    });
    rejectStart(new Error('transport lost'));
    await new Promise((resolve) => setTimeout(resolve, 50));

    // The failed refresh never dropped the association: the next refresh
    // recovers the acceptance by key.
    const accepted = cloneOperation({
      id: 'clone-7',
      state: 'running',
      idempotencyKey: input.idempotencyKey,
    });
    mock.api.listCloneOperations.mockResolvedValue({ operations: [accepted] });
    mock.emitAppEvent({ type: 'invalidated', kind: 'clone.updated' });
    expect(await screen.findByText('Cloning')).toBeVisible();
  });
});
