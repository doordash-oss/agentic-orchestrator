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
  initializeRepositoryResult,
  installAgenticoMock,
  ipcError,
  mockRepoIdentity,
  readySnapshot,
  type AgenticoMock,
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

function snapshotWith(repositories: RepositoryState[]): ReadinessSnapshot {
  return readySnapshot({ repositories, workspaceRoots: [ROOT] });
}

/** Emits a ready local connection, as the real app has wherever the sheet mounts. */
async function emitReadyLocal(mock: AgenticoMock, serverKey = 'alpha') {
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

function unbornPublication(repoKey: string, identity: RepositoryIdentity): ClonePublication {
  return {
    repoKey,
    path: identity.path,
    hasHead: false,
    publishedAt: '2026-09-04T12:00:05Z',
    identity,
  };
}

async function openCloneView(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('button', { name: /clone a repository/i }));
  return screen.findByRole('dialog', { name: 'Clone a repository' });
}

/** Submits the clone and completes it as an unborn success, like an empty remote. */
async function cloneUnborn(
  user: ReturnType<typeof userEvent.setup>,
  mock: AgenticoMock,
  widget: RepositoryIdentity,
  repoA: RepositoryState,
): Promise<void> {
  mock.api.startClone.mockImplementation((input: { idempotencyKey: string }) =>
    Promise.resolve(
      cloneOperation({ id: 'clone-1', state: 'running', idempotencyKey: input.idempotencyKey }),
    ),
  );
  mock.api.getCloneOperation.mockImplementation(() =>
    Promise.resolve(
      cloneOperation({
        id: 'clone-1',
        state: 'running',
        idempotencyKey: (
          mock.api.startClone.mock.calls[0]?.[0] as { idempotencyKey: string } | undefined
        )?.idempotencyKey,
      }),
    ),
  );
  await user.type(screen.getByLabelText('Repository URL'), 'https://example.com/acme/widget.git');
  await user.click(screen.getByRole('button', { name: 'Clone repository' }));
  await waitFor(() => expect(mock.api.startClone).toHaveBeenCalled());

  const key = (mock.api.startClone.mock.calls[0]?.[0] as { idempotencyKey: string }).idempotencyKey;
  const succeeded: CloneOperation = cloneOperation({
    id: 'clone-1',
    state: 'succeeded',
    idempotencyKey: key,
    destination: 'widget',
    destinationPath: widget.path,
    published: unbornPublication('widget', widget),
  });
  mock.api.getCloneOperation.mockResolvedValue(succeeded);
  mock.api.getReadiness.mockResolvedValue(
    snapshotWith([repoA, repo('widget', widget, { featureReady: false })]),
  );
  mock.emitAppEvent({ type: 'invalidated', kind: 'clone.updated' });
  mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });
  const dialog = await screen.findByRole('dialog', { name: 'Clone a repository' });
  await within(dialog).findByText(/no commits yet/i);
}

function widgetInitializeResult(
  widget: RepositoryIdentity,
  result: 'initialized' | 'already_initialized' = 'initialized',
  repoKey = 'widget',
) {
  return initializeRepositoryResult({
    result,
    repoKey,
    path: widget.path,
    root: ROOT.path,
    identity: widget,
  });
}

describe('the picker: explicit initialization of an unborn clone', () => {
  it('adopts the initialized repository into the still-open draft exactly once', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const widget = mockRepoIdentity('/work/space/widget');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('repo-a', repoA)] }),
      readiness: snapshotWith([repo('repo-a', repoA)]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await emitReadyLocal(mock);

    // Draft values that must survive the initialization adoption.
    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
    await user.click(screen.getByRole('radio', { name: 'Current branch' }));
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    await user.type(screen.getByLabelText('Name'), 'Preserved initialization draft');
    await user.type(screen.getByLabelText('Description'), 'Keep this text through adoption.');
    await user.click(screen.getByRole('button', { name: 'Repositories' }));
    await user.type(screen.getByRole('searchbox', { name: 'Search repositories' }), 'zzz');

    await openCloneView(user);
    await cloneUnborn(user, mock, widget, repo('repo-a', repoA));

    mock.api.initializeRepository.mockResolvedValue(widgetInitializeResult(widget));
    mock.api.getReadiness.mockResolvedValue(
      snapshotWith([repo('repo-a', repoA), repo('widget', widget)]),
    );
    await user.click(screen.getByRole('button', { name: /^Create initial commit$/ }));

    await waitFor(() => expect(mock.api.initializeRepository).toHaveBeenCalledTimes(1));
    expect(mock.api.initializeRepository).toHaveBeenCalledWith({
      repoKey: 'widget',
      identity: widget,
      path: widget.path,
      consent: true,
    });

    // The clone view closes and the repository is selected under its
    // current key, with focus on its row and a polite announcement.
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Clone a repository' })).not.toBeInTheDocument(),
    );
    const widgetCheckbox = await screen.findByRole('checkbox', { name: /^widget\b/ });
    await waitFor(() => expect(widgetCheckbox).toBeChecked());
    await waitFor(() => expect(document.activeElement).toBe(widgetCheckbox));
    expect(screen.getByText('Initialized widget and selected it.')).toBeVisible();
    // The rest of the draft is intact.
    expect(screen.getByRole('checkbox', { name: /repo-a/ })).toBeChecked();
    expect(screen.getByRole('radio', { name: 'Current branch' })).toBeChecked();
    expect(screen.getByRole('searchbox', { name: 'Search repositories' })).toHaveValue('');
    await user.click(screen.getByRole('button', { name: 'Next: Describe' }));
    expect(screen.getByLabelText('Name')).toHaveValue('Preserved initialization draft');
    expect(screen.getByLabelText('Description')).toHaveValue('Keep this text through adoption.');
    await user.click(screen.getByRole('button', { name: 'Repositories' }));

    // Repeated invalidations — including the historical clone record that
    // still reports its publication as unborn — never re-select.
    mock.emitAppEvent({ type: 'invalidated', kind: 'clone.updated' });
    mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });
    await new Promise((resolve) => setTimeout(resolve, 50));
    const checked = screen
      .getAllByRole('checkbox')
      .filter((element) => (element as HTMLInputElement).checked);
    expect(checked).toHaveLength(2);
    expect(mock.api.initializeRepository).toHaveBeenCalledTimes(1);

    // The adopted terminal clone is historical now: reopening the nested
    // view starts a fresh clone instead of reviving the old success card.
    await user.click(screen.getByRole('button', { name: /clone a repository/i }));
    const freshCloneDialog = screen.getByRole('dialog', { name: 'Clone a repository' });
    expect(within(freshCloneDialog).getByLabelText('Destination root')).toBeVisible();
    expect(within(freshCloneDialog).queryByText('Succeeded')).toBeNull();
  });

  it('adopts a refresh-only result: another actor initialized the clone first', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const widget = mockRepoIdentity('/work/space/widget');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('repo-a', repoA)] }),
      readiness: snapshotWith([repo('repo-a', repoA)]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await emitReadyLocal(mock);
    await openCloneView(user);
    await cloneUnborn(user, mock, widget, repo('repo-a', repoA));

    mock.api.initializeRepository.mockResolvedValue(
      widgetInitializeResult(widget, 'already_initialized'),
    );
    mock.api.getReadiness.mockResolvedValue(
      snapshotWith([repo('repo-a', repoA), repo('widget', widget)]),
    );
    await user.click(screen.getByRole('button', { name: /^Create initial commit$/ }));

    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Clone a repository' })).not.toBeInTheDocument(),
    );
    expect(await screen.findByRole('checkbox', { name: /^widget\b/ })).toBeChecked();
    expect(screen.getByText('Initialized widget and selected it.')).toBeVisible();
  });

  it('keeps the clone, draft and offer after Not now, and adopts through the later row opt-in', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const widget = mockRepoIdentity('/work/space/widget');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('repo-a', repoA)] }),
      readiness: snapshotWith([repo('repo-a', repoA)]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await emitReadyLocal(mock);
    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
    await openCloneView(user);
    await cloneUnborn(user, mock, widget, repo('repo-a', repoA));

    // Not now hides the offer without any mutation.
    await user.click(screen.getByRole('button', { name: 'Not now' }));
    await waitFor(() =>
      expect(
        screen.queryByRole('button', { name: /^Create initial commit$/ }),
      ).not.toBeInTheDocument(),
    );
    expect(mock.api.initializeRepository).not.toHaveBeenCalled();
    // The dialog and the succeeded presentation stay.
    expect(screen.getByRole('dialog', { name: 'Clone a repository' })).toBeInTheDocument();
    expect(screen.getByText('Succeeded')).toBeVisible();

    // Close the clone view; the unborn row stays visible and unselectable.
    await user.click(screen.getByRole('button', { name: 'Close' }));
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Clone a repository' })).not.toBeInTheDocument(),
    );
    const unbornCheckbox = screen.getByRole('checkbox', { name: /^widget\b/ });
    expect(unbornCheckbox).toBeDisabled();
    expect(unbornCheckbox).not.toBeChecked();
    expect(screen.getByRole('checkbox', { name: /repo-a/ })).toBeChecked();

    // The later opt-in sits beside the row, outside its selection label:
    // expanding it never toggles the checkbox.
    const row = screen.getByRole('checkbox', { name: /^widget\b/ }).closest('li') as HTMLElement;
    const toggle = within(row).getByRole('button', { name: /Create initial commit…/ });
    await user.click(toggle);
    expect(screen.getByRole('checkbox', { name: /^widget\b/ })).not.toBeChecked();
    expect(within(row).getByText(/one empty local commit/i)).toBeVisible();
    expect(mock.api.initializeRepository).not.toHaveBeenCalled();

    // Confirming runs the shared operation with the same consent wording.
    mock.api.initializeRepository.mockResolvedValue(widgetInitializeResult(widget));
    mock.api.getReadiness.mockResolvedValue(
      snapshotWith([repo('repo-a', repoA), repo('widget', widget)]),
    );
    await user.click(within(row).getByRole('button', { name: /^Create initial commit$/ }));

    const adopted = await screen.findByRole('checkbox', { name: /^widget\b/ });
    await waitFor(() => expect(adopted).toBeChecked());
    await waitFor(() => expect(document.activeElement).toBe(adopted));
    expect(screen.getByText('Initialized widget and selected it.')).toBeVisible();
    expect(mock.api.initializeRepository).toHaveBeenCalledWith({
      repoKey: 'widget',
      identity: widget,
      path: widget.path,
      consent: true,
    });
  });

  it('works by keyboard without toggling the repository checkbox', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const widget = mockRepoIdentity('/work/space/widget');
    const mock = installAgenticoMock({
      defaults: creationDefaults({
        repositories: [repo('repo-a', repoA), repo('widget', widget, { featureReady: false })],
      }),
      readiness: snapshotWith([
        repo('repo-a', repoA),
        repo('widget', widget, { featureReady: false }),
      ]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await emitReadyLocal(mock);

    const row = screen.getByRole('checkbox', { name: /^widget\b/ }).closest('li') as HTMLElement;
    const toggle = within(row).getByRole('button', { name: /Create initial commit…/ });
    toggle.focus();
    await user.keyboard('{Enter}');
    expect(within(row).getByText(/one empty local commit/i)).toBeVisible();
    // The checkbox was never touched by the keyboard interaction.
    expect(screen.getByRole('checkbox', { name: /^widget\b/ })).not.toBeChecked();

    mock.api.initializeRepository.mockResolvedValue(widgetInitializeResult(widget));
    mock.api.getReadiness.mockResolvedValue(
      snapshotWith([repo('repo-a', repoA), repo('widget', widget)]),
    );
    await user.keyboard('{Tab}');
    await user.click(screen.getByRole('button', { name: /^Create initial commit$/ }));
    expect(await screen.findByRole('checkbox', { name: /^widget\b/ })).toBeChecked();
  });

  it('uses a valid unique aria ID reference for repository keys with whitespace and quotes', async () => {
    const unsafeKey = 'team/widget "preview"';
    const widget = mockRepoIdentity('/work/space/widget');
    const mock = installAgenticoMock({
      defaults: creationDefaults({
        repositories: [repo(unsafeKey, widget, { featureReady: false })],
      }),
      readiness: snapshotWith([repo(unsafeKey, widget, { featureReady: false })]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await emitReadyLocal(mock);

    const row = screen
      .getByRole('checkbox', { name: /team\/widget "preview"/ })
      .closest('li') as HTMLElement;
    const toggle = within(row).getByRole('button', { name: /Create initial commit…/ });
    await user.click(toggle);
    const controlledId = toggle.getAttribute('aria-controls');
    expect(controlledId).toBeTruthy();
    expect(controlledId).not.toMatch(/\s/);
    expect(document.getElementById(controlledId as string)).toBeVisible();
  });

  it('suppresses duplicate actions for the repository while initialization is pending', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const widget = mockRepoIdentity('/work/space/widget');
    const mock = installAgenticoMock({
      defaults: creationDefaults({
        repositories: [repo('repo-a', repoA), repo('widget', widget, { featureReady: false })],
      }),
      readiness: snapshotWith([
        repo('repo-a', repoA),
        repo('widget', widget, { featureReady: false }),
      ]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await emitReadyLocal(mock);

    let release!: (value: unknown) => void;
    mock.api.initializeRepository.mockImplementation(
      () =>
        new Promise((resolve) => {
          release = resolve;
        }),
    );

    const row = screen.getByRole('checkbox', { name: /^widget\b/ }).closest('li') as HTMLElement;
    await user.click(within(row).getByRole('button', { name: /Create initial commit…/ }));
    await user.click(within(row).getByRole('button', { name: /^Create initial commit$/ }));
    await waitFor(() => expect(mock.api.initializeRepository).toHaveBeenCalledTimes(1));

    // While pending, the same repository's affordances are suppressed:
    // the row toggle and the in-flight action both disable.
    await waitFor(() =>
      expect(within(row).getByRole('button', { name: /Create initial commit…/ })).toBeDisabled(),
    );
    expect(within(row).getByRole('button', { name: 'Creating…' })).toBeDisabled();
    await user.click(within(row).getByRole('button', { name: /Create initial commit…/ }));
    expect(mock.api.initializeRepository).toHaveBeenCalledTimes(1);

    release(widgetInitializeResult(widget));
    mock.api.getReadiness.mockResolvedValue(
      snapshotWith([repo('repo-a', repoA), repo('widget', widget)]),
    );
    // The pending resolution triggers the reconciliation read (via the
    // refreshCatalog call) and the adoption.
    await waitFor(() => expect(mock.api.getReadiness).toHaveBeenCalled());
    const adopted = await screen.findByRole('checkbox', { name: /^widget\b/ });
    await waitFor(() => expect(adopted).toBeChecked());
  });

  it('reconciles a lost response with a fresh read, never an automatic repeat', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const widget = mockRepoIdentity('/work/space/widget');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('repo-a', repoA)] }),
      readiness: snapshotWith([repo('repo-a', repoA)]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await emitReadyLocal(mock);
    await openCloneView(user);
    await cloneUnborn(user, mock, widget, repo('repo-a', repoA));

    const readsBefore = mock.api.getReadiness.mock.calls.length;
    mock.api.initializeRepository.mockRejectedValue(
      ipcError(
        'initialize_unavailable',
        'The connected server could not safely initialize the repository.',
      ),
    );
    await user.click(screen.getByRole('button', { name: /^Create initial commit$/ }));

    // The canonical refusal is scoped to the offer and names the server.
    expect(await screen.findByText(/could not safely initialize/i)).toBeVisible();
    expect(screen.getByRole('dialog', { name: 'Clone a repository' })).toBeInTheDocument();
    expect(screen.getByText('Succeeded')).toBeVisible();

    // A lost response is reconciled by a fresh authoritative read...
    await waitFor(() =>
      expect(mock.api.getReadiness.mock.calls.length).toBeGreaterThan(readsBefore),
    );
    // ...and repeated invalidations never repeat the mutation.
    mock.emitAppEvent({ type: 'invalidated', kind: 'clone.updated' });
    mock.emitAppEvent({ type: 'invalidated', kind: 'config.updated' });
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(mock.api.initializeRepository).toHaveBeenCalledTimes(1);

    // An explicit retry through the offer succeeds and adopts.
    mock.api.initializeRepository.mockResolvedValue(widgetInitializeResult(widget));
    mock.api.getReadiness.mockResolvedValue(
      snapshotWith([repo('repo-a', repoA), repo('widget', widget)]),
    );
    await user.click(screen.getByRole('button', { name: /^Create initial commit$/ }));
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Clone a repository' })).not.toBeInTheDocument(),
    );
    expect(await screen.findByRole('checkbox', { name: /^widget\b/ })).toBeChecked();
  });

  it('adopts after a lost response when the authoritative reconciliation proves success', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const widget = mockRepoIdentity('/work/space/widget');
    const mock = installAgenticoMock({
      defaults: creationDefaults({ repositories: [repo('repo-a', repoA)] }),
      readiness: snapshotWith([repo('repo-a', repoA)]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await emitReadyLocal(mock);
    await openCloneView(user);
    await cloneUnborn(user, mock, widget, repo('repo-a', repoA));

    mock.api.getReadiness.mockResolvedValue(
      snapshotWith([repo('repo-a', repoA), repo('widget', widget)]),
    );
    mock.api.initializeRepository.mockRejectedValue(
      ipcError('initialize_unavailable', 'The response was lost after initialization.'),
    );
    await user.click(screen.getByRole('button', { name: /^Create initial commit$/ }));

    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Clone a repository' })).not.toBeInTheDocument(),
    );
    expect(await screen.findByRole('checkbox', { name: /^widget\b/ })).toBeChecked();
    expect(mock.api.initializeRepository).toHaveBeenCalledTimes(1);
    expect(screen.queryByText(/response was lost/i)).not.toBeInTheDocument();
  });

  it('reconciles a renamed key by identity and safely restores focus for quoted keys', async () => {
    const widget = mockRepoIdentity('/work/space/widget');
    const renamedKey = 'widget "renamed"/v2';
    const mock = installAgenticoMock({
      defaults: creationDefaults({
        repositories: [repo('widget', widget, { featureReady: false })],
      }),
      readiness: snapshotWith([repo('widget', widget, { featureReady: false })]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await emitReadyLocal(mock);

    const row = screen.getByRole('checkbox', { name: /^widget\b/ }).closest('li') as HTMLElement;
    await user.click(within(row).getByRole('button', { name: /Create initial commit…/ }));
    mock.api.initializeRepository.mockResolvedValue(
      widgetInitializeResult(widget, 'initialized', renamedKey),
    );
    mock.api.getReadiness.mockResolvedValue(snapshotWith([repo(renamedKey, widget)]));
    await user.click(within(row).getByRole('button', { name: /^Create initial commit$/ }));

    const renamed = await screen.findByRole('checkbox', { name: /widget "renamed"\/v2/ });
    await waitFor(() => expect(renamed).toBeChecked());
    await waitFor(() => expect(document.activeElement).toBe(renamed));
    expect(screen.getByText(`Initialized ${renamedKey} and selected it.`)).toBeVisible();
  });

  it('does not adopt a late response after switching away and back to the same server', async () => {
    const widget = mockRepoIdentity('/work/space/widget');
    const mock = installAgenticoMock({
      defaults: creationDefaults({
        repositories: [repo('widget', widget, { featureReady: false })],
      }),
      readiness: snapshotWith([repo('widget', widget, { featureReady: false })]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await emitReadyLocal(mock);

    let release!: (value: ReturnType<typeof widgetInitializeResult>) => void;
    mock.api.initializeRepository.mockImplementation(
      () => new Promise((resolve) => (release = resolve)),
    );
    const row = screen.getByRole('checkbox', { name: /^widget\b/ }).closest('li') as HTMLElement;
    await user.click(within(row).getByRole('button', { name: /Create initial commit…/ }));
    await user.click(within(row).getByRole('button', { name: /^Create initial commit$/ }));
    await waitFor(() => expect(mock.api.initializeRepository).toHaveBeenCalledTimes(1));

    await emitReadyLocal(mock, 'beta');
    await emitReadyLocal(mock, 'alpha');
    mock.api.getReadiness.mockResolvedValue(snapshotWith([repo('widget', widget)]));
    release(widgetInitializeResult(widget));
    await act(async () => await Promise.resolve());

    expect(screen.getByRole('checkbox', { name: /^widget\b/ })).not.toBeChecked();
  });

  it('requires reselection when the repository identity was replaced during initialization', async () => {
    const widget = mockRepoIdentity('/work/space/widget');
    const replacement = mockRepoIdentity('/work/space/widget', { inode: '9999' });
    const mock = installAgenticoMock({
      defaults: creationDefaults({
        repositories: [repo('widget', widget, { featureReady: false })],
      }),
      readiness: snapshotWith([repo('widget', widget, { featureReady: false })]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await emitReadyLocal(mock);

    const row = screen.getByRole('checkbox', { name: /^widget\b/ }).closest('li') as HTMLElement;
    await user.click(within(row).getByRole('button', { name: /Create initial commit…/ }));
    mock.api.initializeRepository.mockResolvedValue(widgetInitializeResult(widget));
    mock.api.getReadiness.mockResolvedValue(snapshotWith([repo('widget', replacement)]));
    await user.click(within(row).getByRole('button', { name: /^Create initial commit$/ }));

    await waitFor(() =>
      expect(screen.getByText(/repository changed or is no longer available/i)).toBeVisible(),
    );
    const current = screen.getByRole('checkbox', { name: /^widget\b/ });
    expect(current).not.toBeChecked();
    expect(current).toBeEnabled();
    expect(mock.api.initializeRepository).toHaveBeenCalledTimes(1);
  });

  it('keeps an unborn row visible and unselectable while valid repositories stay available', async () => {
    const repoA = mockRepoIdentity('/work/space/repo-a');
    const widget = mockRepoIdentity('/work/space/widget');
    const mock = installAgenticoMock({
      defaults: creationDefaults({
        repositories: [repo('repo-a', repoA), repo('widget', widget, { featureReady: false })],
      }),
      readiness: snapshotWith([
        repo('repo-a', repoA),
        repo('widget', widget, { featureReady: false }),
      ]),
    });
    render(<CreateFeatureForm onCreated={vi.fn()} onClose={vi.fn()} />);
    const user = userEvent.setup();
    await screen.findByRole('button', { name: 'Next: Describe' });
    await emitReadyLocal(mock);

    const unborn = screen.getByRole('checkbox', { name: /^widget\b/ });
    expect(unborn).toBeDisabled();
    expect(screen.getByText(/No commits yet/i)).toBeVisible();
    // A valid repository stays selectable for feature creation.
    await user.click(screen.getByRole('checkbox', { name: /repo-a/ }));
    expect(screen.getByRole('checkbox', { name: /repo-a/ })).toBeChecked();
    expect(mock.api.initializeRepository).not.toHaveBeenCalled();
  });
});
