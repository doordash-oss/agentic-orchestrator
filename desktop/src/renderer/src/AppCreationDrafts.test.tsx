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
import { afterEach, describe, expect, it } from 'vitest';
import App from './App';
import type { ConnectionState } from '../../shared/ipc';
import {
  cloneOperation,
  creationDefaults,
  installAgenticoMock,
  mockRepoIdentity,
  readySnapshot,
  type AgenticoMock,
} from './test/agenticoMock';
import {
  freshCreationDraft,
  retainableDraft,
  CreationDraftsStore,
  type CreationDraftState,
} from './features/creationDrafts';

afterEach(cleanup);

function readyConnection(serverKey: string): ConnectionState {
  return {
    status: 'ready',
    stage: 'ready',
    detail: 'Runtime ready.',
    ownership: 'external',
    kind: 'local',
    serverKey,
    serverName: serverKey,
  };
}

function offlineConnection(): ConnectionState {
  return {
    status: 'crashed',
    stage: 'connect',
    detail: 'The runtime exited unexpectedly.',
    ownership: 'app-owned',
    error: {
      code: 'E_SERVER_CRASHED',
      class: 'blocking',
      title: 'The app-managed runtime crashed',
      summary: 'The app-managed Agentico runtime exited unexpectedly.',
    },
  };
}

function installReady(
  serverKey: string,
  overrides: Parameters<typeof installAgenticoMock>[0] = {},
): AgenticoMock {
  return installAgenticoMock({
    connection: readyConnection(serverKey),
    readiness: readySnapshot(),
    defaults: creationDefaults(),
    ...overrides,
  });
}

/**
 * Emits a connection change and keeps the initial-status IPC in sync: a
 * remounted ready tree re-reads the connection from the main process, so a
 * stale initial reply would resurrect the previous server's shell.
 */
function emitConnection(mock: AgenticoMock, state: ConnectionState): void {
  mock.api.getConnectionStatus.mockResolvedValue(state);
  act(() => mock.emitConnection(state));
}

async function openSheet(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('button', { name: 'New feature' }));
  return screen.findByRole('form', { name: /create a feature/i });
}

async function fillDraftBasics(user: ReturnType<typeof userEvent.setup>, name: string) {
  await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
  await user.click(screen.getByRole('radio', { name: 'Current branches' }));
  await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
  await user.type(screen.getByLabelText('Name'), name);
}

describe('creation drafts across readiness flips (App-level)', () => {
  it('retains and restores a draft through an actual readiness-gate unmount', async () => {
    const mock = installReady('server-a');
    render(<App />);
    const user = userEvent.setup();
    await openSheet(user);

    await fillDraftBasics(user, 'Alpha draft');

    // The ready tree unmounts: the sheet is gone, not merely hidden.
    emitConnection(mock, offlineConnection());
    expect(screen.queryByRole('form', { name: /create a feature/i })).not.toBeInTheDocument();

    // Reconnecting to the same server restores its draft: values, branch
    // mode, wizard position, and the sheet itself come back together.
    emitConnection(mock, readyConnection('server-a'));
    const sheet = await screen.findByRole('form', { name: /create a feature/i });
    expect(within(sheet).getByLabelText('Name')).toHaveValue('Alpha draft');
    await user.click(within(sheet).getByRole('button', { name: 'Back' }));
    expect(within(sheet).getByRole('checkbox', { name: /repo-a/ })).toBeChecked();
    expect(within(sheet).getByRole('radio', { name: 'Current branches' })).toBeChecked();
  });

  it('keeps two servers’ drafts independent across an A→B→A switch', async () => {
    const mock = installReady('server-a');
    render(<App />);
    const user = userEvent.setup();
    await openSheet(user);
    await fillDraftBasics(user, 'Alpha draft');

    // Switch to server B: the switch passes through a non-ready state, so
    // the whole ready tree — and with it the sheet — unmounts.
    emitConnection(mock, offlineConnection());
    emitConnection(mock, readyConnection('server-b'));
    await screen.findByRole('option', { name: 'Overview' });
    // Server B has no draft: nothing steals focus or reopens the sheet.
    expect(screen.queryByRole('form', { name: /create a feature/i })).not.toBeInTheDocument();

    await openSheet(user);
    await fillDraftBasics(user, 'Beta draft');
    emitConnection(mock, offlineConnection());
    emitConnection(mock, readyConnection('server-a'));

    const restored = await screen.findByRole('form', { name: /create a feature/i });
    expect(within(restored).getByLabelText('Name')).toHaveValue('Alpha draft');

    emitConnection(mock, offlineConnection());
    emitConnection(mock, readyConnection('server-b'));
    const beta = await screen.findByRole('form', { name: /create a feature/i });
    expect(within(beta).getByLabelText('Name')).toHaveValue('Beta draft');
  });

  it('keeps the connection shell while a reconnect keeps failing, then restores', async () => {
    const mock = installReady('server-a');
    render(<App />);
    const user = userEvent.setup();
    await openSheet(user);
    await fillDraftBasics(user, 'Alpha draft');

    emitConnection(mock, offlineConnection());
    // A retry that still fails keeps the connection shell; the retained
    // draft never leaks into it.
    mock.api.retryConnection.mockResolvedValue(offlineConnection());
    emitConnection(mock, offlineConnection());
    expect(screen.queryByRole('form', { name: /create a feature/i })).not.toBeInTheDocument();
    expect(screen.getByLabelText(/agentico connection/i)).toBeInTheDocument();

    emitConnection(mock, readyConnection('server-a'));
    const restored = await screen.findByRole('form', { name: /create a feature/i });
    expect(within(restored).getByLabelText('Name')).toHaveValue('Alpha draft');
  });

  it('retires the draft on explicit discard and starts a fresh one afterwards', async () => {
    installReady('server-a');
    render(<App />);
    const user = userEvent.setup();
    await openSheet(user);
    await fillDraftBasics(user, 'Alpha draft');

    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    await user.click(await screen.findByRole('button', { name: 'Discard draft' }));
    await waitFor(() =>
      expect(screen.queryByRole('form', { name: /create a feature/i })).not.toBeInTheDocument(),
    );

    // The next draft on the same server starts completely fresh.
    await openSheet(user);
    const sheet = screen.getByRole('form', { name: /create a feature/i });
    expect(within(sheet).getByRole('heading', { name: 'Choose repositories' })).toBeVisible();
    expect(within(sheet).queryByRole('checkbox', { name: /repo-a/ })).not.toBeChecked();
  });

  it('reuses the creation idempotency key after a lost creation response', async () => {
    const mock = installReady('server-a');
    render(<App />);
    const user = userEvent.setup();
    await openSheet(user);
    await fillDraftBasics(user, 'Alpha draft');
    await user.click(screen.getByRole('button', { name: 'Next: Depth' }));
    await user.click(screen.getByRole('button', { name: 'Next: Contract' }));
    const sheet = screen.getByRole('form', { name: /create a feature/i });

    // The creation reply is lost: the request reaches the server, the
    // connection drops mid-flight, and no response ever arrives.
    mock.api.createFeature.mockImplementationOnce(
      () => new Promise<{ featureId: string }>(() => {}),
    );
    // Scoped to the sheet: the Overview empty-state CTA also matches /Create/.
    await user.click(within(sheet).getByRole('button', { name: /Create/ }));
    await waitFor(() => expect(mock.api.createFeature).toHaveBeenCalled());
    emitConnection(mock, offlineConnection());
    emitConnection(mock, readyConnection('server-a'));

    // The restored draft retries with the same idempotency key, so the
    // server returns its original creation instead of a duplicate.
    mock.api.createFeature.mockResolvedValue({ featureId: 'feature0123456789ab' });
    const restored = await screen.findByRole('form', { name: /create a feature/i });
    await user.click(within(restored).getByRole('button', { name: /Create/ }));
    await waitFor(() => expect(mock.api.createFeature).toHaveBeenCalledTimes(2));
    const first = mock.api.createFeature.mock.calls[0]?.[0] as { idempotencyKey: string };
    const second = mock.api.createFeature.mock.calls[1]?.[0] as { idempotencyKey: string };
    expect(second.idempotencyKey).toBe(first.idempotencyKey);

    // The created feature retires the draft: the sheet closes for good.
    await waitFor(() =>
      expect(screen.queryByRole('form', { name: /create a feature/i })).not.toBeInTheDocument(),
    );
  });

  it('never restores drafts across a fresh app mount (nothing is persisted)', async () => {
    const mock = installReady('server-a');
    const { unmount } = render(<App />);
    const user = userEvent.setup();
    await openSheet(user);
    await fillDraftBasics(user, 'Alpha draft');
    emitConnection(mock, offlineConnection());
    unmount();
    cleanup();

    // A relaunch is a brand-new app session: no draft anywhere.
    installReady('server-a');
    render(<App />);
    await screen.findByRole('option', { name: 'Overview' });
    expect(screen.queryByRole('form', { name: /create a feature/i })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'New feature' }));
    const sheet = await screen.findByRole('form', { name: /create a feature/i });
    expect(within(sheet).getByRole('heading', { name: 'Choose repositories' })).toBeVisible();
  });
});

describe('CreationDraftsStore', () => {
  it('keeps one entry per server and never copies fields between them', () => {
    const store = new CreationDraftsStore();
    store.openDraft('a');
    store.persist('a', { ...freshCreationDraft(), name: 'Alpha' });
    store.openDraft('b');
    store.persist('b', { ...freshCreationDraft(), name: 'Beta' });
    expect(store.entry('a')?.state.name).toBe('Alpha');
    expect(store.entry('b')?.state.name).toBe('Beta');
    store.retire('a');
    expect(store.entry('a')).toBeUndefined();
    expect(store.entry('b')?.state.name).toBe('Beta');
  });

  it('marks in-flight uploads as failed and retryable when a sheet detaches', () => {
    const draft: CreationDraftState = {
      ...freshCreationDraft(),
      imageUploads: [
        {
          id: 'upload-1',
          kind: 'image',
          name: 'shot.png',
          sourcePath: '/tmp/shot.png',
          state: 'uploading',
        },
      ],
    };
    const retained = retainableDraft(draft);
    expect(retained.imageUploads[0]?.state).toBe('failed');
    expect(retained.imageUploads[0]?.message).toMatch(/interrupted/i);
    expect(retained.restored).toBe(true);
  });
});

describe('clone completion reconciles into only the owning server\u2019s draft', () => {
  it(
    'adopts each server\u2019s own result and never crosses drafts',
    { timeout: 30_000 },
    async () => {
      const mock = installReady('server-a');
      render(<App />);
      const user = userEvent.setup();
      await openSheet(user);
      await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));

      // Server A starts a picker clone and closes the nested view. The
      // started operation echoes the request's idempotency key, like the
      // server, so the tracked snapshot is the draft's own attempt.
      await user.click(screen.getByRole('button', { name: /clone a repository/i }));
      await screen.findByRole('dialog', { name: 'Clone a repository' });
      mock.api.startClone.mockImplementation((input: { idempotencyKey: string }) =>
        Promise.resolve(
          cloneOperation({
            id: 'clone-a',
            state: 'running',
            destination: 'widget-a',
            idempotencyKey: input.idempotencyKey,
          }),
        ),
      );
      mock.api.getCloneOperation.mockImplementation(() => {
        const calls = mock.api.startClone.mock.calls as Array<
          [{ idempotencyKey: string }, ...unknown[]]
        >;
        return Promise.resolve(
          cloneOperation({
            id: 'clone-a',
            state: 'running',
            destination: 'widget-a',
            idempotencyKey: calls[calls.length - 1]?.[0]?.idempotencyKey,
          }),
        );
      });
      await user.type(
        screen.getByLabelText('Repository URL'),
        'https://example.com/acme/widget-a.git',
      );
      await user.click(screen.getByRole('button', { name: 'Clone repository' }));
      await screen.findByText('Cloning');
      const keyA = (mock.api.startClone.mock.calls[0]?.[0] as { idempotencyKey: string })
        .idempotencyKey;
      await user.click(screen.getByRole('button', { name: 'Close' }));

      // Server B opens its own draft and starts its own clone.
      emitConnection(mock, offlineConnection());
      emitConnection(mock, readyConnection('server-b'));
      await screen.findByRole('option', { name: 'Overview' });
      await openSheet(user);
      await user.click(screen.getByRole('button', { name: /clone a repository/i }));
      await screen.findByRole('dialog', { name: 'Clone a repository' });
      mock.api.startClone.mockImplementation((input: { idempotencyKey: string }) =>
        Promise.resolve(
          cloneOperation({
            id: 'clone-b',
            state: 'running',
            destination: 'widget-b',
            idempotencyKey: input.idempotencyKey,
          }),
        ),
      );
      mock.api.getCloneOperation.mockImplementation(() => {
        const calls = mock.api.startClone.mock.calls as Array<
          [{ idempotencyKey: string }, ...unknown[]]
        >;
        return Promise.resolve(
          cloneOperation({
            id: 'clone-b',
            state: 'running',
            destination: 'widget-b',
            idempotencyKey: calls[calls.length - 1]?.[0]?.idempotencyKey,
          }),
        );
      });
      await user.type(
        screen.getByLabelText('Repository URL'),
        'https://example.com/acme/widget-b.git',
      );
      await user.click(screen.getByRole('button', { name: 'Clone repository' }));
      await screen.findByText('Cloning');
      const keyB = (mock.api.startClone.mock.calls[1]?.[0] as { idempotencyKey: string })
        .idempotencyKey;
      await user.click(screen.getByRole('button', { name: 'Close' }));

      // Back on server A: its draft (and its own clone) is restored.
      emitConnection(mock, offlineConnection());
      emitConnection(mock, readyConnection('server-a'));
      const sheetA = await screen.findByRole('form', { name: /create a feature/i });
      expect(within(sheetA).getByRole('checkbox', { name: /repo-a/ })).toBeChecked();

      // A's clone completes: the result adopts into A's draft only.
      const identityA = mockRepoIdentity('/work/space/widget-a');
      mock.api.getCloneOperation.mockResolvedValue(
        cloneOperation({
          id: 'clone-a',
          state: 'succeeded',
          destination: 'widget-a',
          idempotencyKey: keyA,
          published: {
            repoKey: 'widget-a',
            path: '/work/space/widget-a',
            hasHead: true,
            publishedAt: '2026-09-04T12:00:05Z',
            identity: identityA,
          },
        }),
      );
      mock.api.getReadiness.mockResolvedValue(
        readySnapshot({
          repositories: [
            {
              name: 'repo-a',
              path: '/work/space/repo-a',
              valid: true,
              featureReady: true,
              identity: mockRepoIdentity('/work/space/repo-a'),
            },
            {
              name: 'widget-a',
              path: '/work/space/widget-a',
              valid: true,
              featureReady: true,
              identity: identityA,
            },
          ],
          workspaceRoots: [{ path: '/work/space', valid: true, cloneEligible: true }],
        }),
      );
      mock.emitAppEvent({ type: 'invalidated', kind: 'clone.updated' });
      mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });
      await waitFor(
        () => expect(within(sheetA).getByRole('checkbox', { name: /widget-a/ })).toBeChecked(),
        { timeout: 5000 },
      );

      // Server B's draft is untouched by A's result: its own completion
      // adopts its own repository when B is active again.
      emitConnection(mock, offlineConnection());
      emitConnection(mock, readyConnection('server-b'));
      const sheetB = await screen.findByRole('form', { name: /create a feature/i });
      // B's draft is untouched by A's result: A's repository may appear in
      // the (shared) catalog, but it is never selected for B's draft, and
      // B's own repository has not completed yet.
      const widgetAOnB = within(sheetB).queryByRole('checkbox', { name: /widget-a/ });
      if (widgetAOnB !== null) expect(widgetAOnB).not.toBeChecked();
      expect(within(sheetB).queryByRole('checkbox', { name: /widget-b/ })).toBeNull();

      const identityB = mockRepoIdentity('/work/space/widget-b');
      mock.api.getCloneOperation.mockResolvedValue(
        cloneOperation({
          id: 'clone-b',
          state: 'succeeded',
          destination: 'widget-b',
          idempotencyKey: keyB,
          published: {
            repoKey: 'widget-b',
            path: '/work/space/widget-b',
            hasHead: true,
            publishedAt: '2026-09-04T12:00:06Z',
            identity: identityB,
          },
        }),
      );
      mock.api.getReadiness.mockResolvedValue(
        readySnapshot({
          repositories: [
            {
              name: 'repo-a',
              path: '/work/space/repo-a',
              valid: true,
              featureReady: true,
              identity: mockRepoIdentity('/work/space/repo-a'),
            },
            {
              name: 'widget-b',
              path: '/work/space/widget-b',
              valid: true,
              featureReady: true,
              identity: identityB,
            },
          ],
          workspaceRoots: [{ path: '/work/space', valid: true, cloneEligible: true }],
        }),
      );
      mock.emitAppEvent({ type: 'invalidated', kind: 'clone.updated' });
      mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });
      await waitFor(
        () => expect(within(sheetB).getByRole('checkbox', { name: /widget-b/ })).toBeChecked(),
        { timeout: 5000 },
      );

      // Returning to A keeps its own adoption exactly as it was. The shared
      // catalog now knows both published repositories; only A's own
      // selection restores — beta's adoption never leaks into alpha's draft.
      mock.api.getReadiness.mockResolvedValue(
        readySnapshot({
          repositories: [
            {
              name: 'repo-a',
              path: '/work/space/repo-a',
              valid: true,
              featureReady: true,
              identity: mockRepoIdentity('/work/space/repo-a'),
            },
            {
              name: 'widget-a',
              path: '/work/space/widget-a',
              valid: true,
              featureReady: true,
              identity: identityA,
            },
            {
              name: 'widget-b',
              path: '/work/space/widget-b',
              valid: true,
              featureReady: true,
              identity: identityB,
            },
          ],
          workspaceRoots: [{ path: '/work/space', valid: true, cloneEligible: true }],
        }),
      );
      emitConnection(mock, offlineConnection());
      emitConnection(mock, readyConnection('server-a'));
      const restoredA = await screen.findByRole('form', { name: /create a feature/i });
      await waitFor(
        () => expect(within(restoredA).getByRole('checkbox', { name: /widget-a/ })).toBeChecked(),
        { timeout: 5000 },
      );
      expect(within(restoredA).getByRole('checkbox', { name: /repo-a/ })).toBeChecked();
      expect(within(restoredA).getByRole('checkbox', { name: /widget-b/ })).not.toBeChecked();
    },
  );

  it(
    'never recreates a discarded draft from a late clone result',
    { timeout: 30_000 },
    async () => {
      const mock = installReady('server-a');
      render(<App />);
      const user = userEvent.setup();
      await openSheet(user);
      await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
      await user.click(screen.getByRole('button', { name: /clone a repository/i }));
      await screen.findByRole('dialog', { name: 'Clone a repository' });
      const running = cloneOperation({ id: 'clone-late', state: 'running' });
      mock.api.startClone.mockResolvedValue(running);
      mock.api.getCloneOperation.mockResolvedValue(running);
      await user.type(
        screen.getByLabelText('Repository URL'),
        'https://example.com/acme/widget.git',
      );
      await user.click(screen.getByRole('button', { name: 'Clone repository' }));
      await screen.findByText('Cloning');
      await user.click(screen.getByRole('button', { name: 'Close' }));

      // The draft is explicitly discarded: its association retires with it.
      await user.click(screen.getByRole('button', { name: 'Cancel' }));
      await user.click(await screen.findByRole('button', { name: 'Discard draft' }));
      await waitFor(() =>
        expect(screen.queryByRole('form', { name: /create a feature/i })).not.toBeInTheDocument(),
      );

      // A late success for the discarded draft's operation cannot recreate it.
      const identity = mockRepoIdentity('/work/space/widget');
      mock.api.getCloneOperation.mockResolvedValue(
        cloneOperation({
          id: 'clone-late',
          state: 'succeeded',
          published: {
            repoKey: 'widget',
            path: '/work/space/widget',
            hasHead: true,
            publishedAt: '2026-09-04T12:00:05Z',
            identity,
          },
        }),
      );
      mock.api.getReadiness.mockResolvedValue(
        readySnapshot({
          repositories: [
            {
              name: 'repo-a',
              path: '/work/space/repo-a',
              valid: true,
              featureReady: true,
              identity: mockRepoIdentity('/work/space/repo-a'),
            },
            {
              name: 'widget',
              path: '/work/space/widget',
              valid: true,
              featureReady: true,
              identity,
            },
          ],
          workspaceRoots: [{ path: '/work/space', valid: true, cloneEligible: true }],
        }),
      );
      mock.emitAppEvent({ type: 'invalidated', kind: 'clone.updated' });
      mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });
      await new Promise((resolve) => setTimeout(resolve, 100));
      expect(screen.queryByRole('form', { name: /create a feature/i })).not.toBeInTheDocument();

      // The next draft starts fresh: the published repository is available
      // but never auto-selected for a draft that did not initiate it.
      mock.api.getCreationDefaults.mockResolvedValue(
        creationDefaults({
          repositories: [
            {
              name: 'repo-a',
              path: '/work/space/repo-a',
              valid: true,
              featureReady: true,
              identity: mockRepoIdentity('/work/space/repo-a'),
            },
            {
              name: 'widget',
              path: '/work/space/widget',
              valid: true,
              featureReady: true,
              identity,
            },
          ],
        }),
      );
      await user.click(screen.getByRole('button', { name: 'New feature' }));
      const fresh = await screen.findByRole('form', { name: /create a feature/i });
      expect(within(fresh).getByRole('checkbox', { name: /widget/ })).not.toBeChecked();
    },
  );
});
