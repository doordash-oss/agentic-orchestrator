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

import { describe, expect, it, beforeEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { useState } from 'react';
import userEvent from '@testing-library/user-event';
import { CloneRepositorySection } from './CloneRepositorySection';
import {
  cloneOperation,
  installAgenticoMock,
  ipcError,
  readySnapshot,
  type AgenticoMock,
} from '../test/agenticoMock';
import type { ConnectionState, ReadinessSnapshot } from '../../../shared/ipc';

let mock: AgenticoMock;

function readyConnection(
  serverKey: string,
  name = 'local server',
  kind: 'local' | 'remote' = 'local',
): ConnectionState {
  return {
    status: 'ready',
    stage: 'ready',
    detail: 'Connected.',
    ownership: 'external',
    kind,
    serverKey,
    serverName: name,
  };
}

function readinessWithRoots(roots: ReadinessSnapshot['workspaceRoots']): ReadinessSnapshot {
  return readySnapshot({ workspaceRoots: roots });
}

const eligibleRoots: ReadinessSnapshot['workspaceRoots'] = [
  { path: '/work/space', valid: true, cloneEligible: true },
];

beforeEach(() => {
  mock = installAgenticoMock({
    readiness: readinessWithRoots(eligibleRoots),
  });
});

function renderSection(
  readiness: ReadinessSnapshot = readinessWithRoots(eligibleRoots),
  connection: ConnectionState = readyConnection('local-1'),
) {
  return render(<CloneRepositorySection readiness={readiness} connection={connection} />);
}

describe('CloneRepositorySection form', () => {
  it('suggests a folder name from the remote and preserves manual edits', async () => {
    const user = userEvent.setup();
    renderSection();
    const remote = screen.getByLabelText('Repository URL');
    await user.type(remote, 'https://github.com/acme/widget.git');
    expect(screen.getByLabelText('Folder name')).toHaveValue('widget');
    // A later remote edit preserves a manually edited folder.
    await user.clear(screen.getByLabelText('Folder name'));
    await user.type(screen.getByLabelText('Folder name'), 'custom-name');
    await user.clear(remote);
    await user.type(remote, 'https://github.com/acme/other.git');
    expect(screen.getByLabelText('Folder name')).toHaveValue('custom-name');
    // Destination preview names the final path.
    expect(screen.getByText(/Will clone into/)).toHaveTextContent('/work/space/custom-name');
  });

  it('refuses an empty or malformed remote with a field error and no request', async () => {
    const user = userEvent.setup();
    renderSection();
    await user.click(screen.getByRole('button', { name: 'Clone repository' }));
    expect(screen.getByText('A repository URL is required.')).toBeInTheDocument();
    await user.type(screen.getByLabelText('Repository URL'), '/local/path');
    await user.click(screen.getByRole('button', { name: 'Clone repository' }));
    expect(screen.getByText(/https:\/\/, http:\/\//)).toBeInTheDocument();
    expect(mock.api.startClone).not.toHaveBeenCalled();
  });

  it('refuses a malformed destination name with a field error', async () => {
    const user = userEvent.setup();
    renderSection();
    await user.type(screen.getByLabelText('Repository URL'), 'https://github.com/acme/widget.git');
    await user.clear(screen.getByLabelText('Folder name'));
    await user.type(screen.getByLabelText('Folder name'), 'sub/dir');
    await user.click(screen.getByRole('button', { name: 'Clone repository' }));
    expect(screen.getByText(/single new folder name/)).toBeInTheDocument();
    expect(mock.api.startClone).not.toHaveBeenCalled();
  });

  it('starts a clone and announces live-server continuation', async () => {
    const user = userEvent.setup();
    renderSection();
    await user.type(screen.getByLabelText('Repository URL'), 'https://github.com/acme/widget.git');
    await user.click(screen.getByRole('button', { name: 'Clone repository' }));
    await waitFor(() => expect(mock.api.startClone).toHaveBeenCalledTimes(1));
    const request = mock.api.startClone.mock.calls[0]?.[0];
    expect(request?.destination).toBe('widget');
    expect(request?.rootPath).toBe('/work/space');
    expect(request?.idempotencyKey).toMatch(/^[0-9a-f-]{8,36}$/);
    expect(screen.getByText(/keeps running on the server/)).toBeInTheDocument();
    // The accepted operation appears in the list from the fresh read.
    expect(await screen.findByText('widget')).toBeInTheDocument();
  });

  it('associates destination rejections with the folder control', async () => {
    mock.api.startClone.mockImplementationOnce(() =>
      Promise.reject(
        Object.assign(new Error('x'), {
          canonical: {
            code: 'clone_destination_exists',
            class: 'blocking',
            title: 'Destination already exists',
            summary: 'Something already exists at the destination path.',
          },
        }),
      ),
    );
    const user = userEvent.setup();
    renderSection();
    await user.type(screen.getByLabelText('Repository URL'), 'https://github.com/acme/widget.git');
    await user.click(screen.getByRole('button', { name: 'Clone repository' }));
    expect(
      await screen.findByText('Something already exists at the destination path.'),
    ).toBeInTheDocument();
    expect(screen.getByLabelText('Folder name')).toHaveAttribute('aria-invalid', 'true');
  });

  it('shows administrator guidance when no root is clone-eligible', () => {
    renderSection(readinessWithRoots([{ path: '/work/space', valid: true, cloneEligible: false }]));
    expect(screen.getByText(/No clone-eligible workspace root/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Clone repository' })).toBeDisabled();
  });
});

describe('CloneRepositorySection operations list', () => {
  const stateCases: Array<{
    state: ReturnType<typeof cloneOperation>['state'];
    label: string;
    action?: string;
    overrides?: Partial<ReturnType<typeof cloneOperation>>;
  }> = [
    { state: 'accepted', label: 'Starting', action: 'Cancel clone' },
    { state: 'running', label: 'Cloning', action: 'Cancel clone' },
    { state: 'finalizing', label: 'Publishing', action: 'Cancel clone' },
    {
      state: 'cancelling',
      label: 'Cancelling',
      action: 'Cancelling…',
      overrides: { cancelRequested: true },
    },
    { state: 'succeeded', label: 'Succeeded' },
    { state: 'failed', label: 'Failed', action: 'Retry clone' },
    { state: 'cancelled', label: 'Cancelled', action: 'Retry clone' },
    { state: 'interrupted', label: 'Interrupted', action: 'Retry clone' },
    { state: 'cleanup_pending', label: 'Cleanup pending', action: 'Retry cleanup' },
  ];

  it.each(stateCases)(
    'shows $state with its authored label and actions',
    async ({ state, label, action, overrides }) => {
      mock.api.listCloneOperations.mockResolvedValue({
        operations: [cloneOperation({ state, destination: `dest-${state}`, ...overrides })],
      });
      renderSection();
      expect(await screen.findByText(`dest-${state}`)).toBeInTheDocument();
      expect(screen.getByText(label)).toBeInTheDocument();
      const progress = screen.queryByRole('progressbar', {
        name: `Clone progress for dest-${state}`,
      });
      if (['accepted', 'running', 'finalizing', 'cancelling'].includes(state)) {
        expect(progress).toBeVisible();
        expect(progress).not.toHaveAttribute('aria-valuenow');
      } else {
        expect(progress).not.toBeInTheDocument();
      }
      if (action === undefined) {
        expect(
          screen.queryByRole('button', { name: /Cancel clone|Retry cleanup|Retry clone/ }),
        ).not.toBeInTheDocument();
      } else {
        expect(screen.getByRole('button', { name: action })).toBeInTheDocument();
      }
    },
  );

  it('distinguishes Close/Cancel semantics and cancels explicitly', async () => {
    const user = userEvent.setup();
    mock.api.listCloneOperations.mockResolvedValueOnce({
      operations: [cloneOperation({ state: 'running' })],
    });
    renderSection();
    expect(await screen.findByText('widget')).toBeInTheDocument();
    expect(
      screen.getByText(/Closing this view or Settings never stops a clone/),
    ).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Cancel clone' }));
    await waitFor(() =>
      expect(mock.api.cancelCloneOperation).toHaveBeenCalledWith('clone-0123456789abcdef'),
    );
  });

  it('retries cleanup and fresh retry from their own actions', async () => {
    const user = userEvent.setup();
    mock.api.listCloneOperations.mockResolvedValue({
      operations: [
        cloneOperation({ state: 'cleanup_pending', destination: 'stuck' }),
        cloneOperation({ state: 'failed', destination: 'broken' }),
      ],
    });
    renderSection();
    expect(await screen.findByText('stuck')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Retry cleanup' }));
    await waitFor(() =>
      expect(mock.api.retryCloneCleanup).toHaveBeenCalledWith('clone-0123456789abcdef'),
    );
    await user.click(screen.getByRole('button', { name: 'Retry clone' }));
    await waitFor(() =>
      expect(mock.api.retryCloneOperation).toHaveBeenCalledWith('clone-0123456789abcdef'),
    );
  });

  it('reloads on clone invalidations and on resync', async () => {
    renderSection();
    await screen.findByText('widget');
    expect(mock.api.listCloneOperations).toHaveBeenCalledTimes(1);
    mock.emitAppEvent({ type: 'invalidated', kind: 'clone.updated' });
    await waitFor(() => expect(mock.api.listCloneOperations).toHaveBeenCalledTimes(2));
    mock.emitAppEvent({ type: 'invalidated', kind: 'resync' });
    await waitFor(() => expect(mock.api.listCloneOperations).toHaveBeenCalledTimes(3));
  });

  it('a failed list request never erases known work and stays recoverable', async () => {
    renderSection();
    await screen.findByText('widget');
    mock.api.listCloneOperations.mockRejectedValueOnce(
      Object.assign(new Error('x'), {
        canonical: {
          code: 'E_REQUEST_TIMEOUT',
          class: 'warning',
          title: 'Request timed out',
          summary: 'The runtime did not answer within the request bound.',
        },
      }),
    );
    mock.emitAppEvent({ type: 'invalidated', kind: 'clone.updated' });
    // Known work stays visible next to the recoverable error.
    expect(await screen.findByText('widget')).toBeInTheDocument();
    expect(screen.getByText('Request timed out')).toBeInTheDocument();
    const callsBefore = mock.api.listCloneOperations.mock.calls.length;
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }));
    await waitFor(() =>
      expect(mock.api.listCloneOperations.mock.calls.length).toBeGreaterThan(callsBefore),
    );
  });

  it('replaces the list when the connected server changes', async () => {
    const { rerender } = renderSection(
      readinessWithRoots(eligibleRoots),
      readyConnection('server-a'),
    );
    await screen.findByText('widget');
    mock.api.listCloneOperations.mockResolvedValueOnce({ operations: [] });
    rerender(
      <CloneRepositorySection
        readiness={readinessWithRoots(eligibleRoots)}
        connection={readyConnection('server-b')}
      />,
    );
    await waitFor(() => expect(screen.queryByText('widget')).not.toBeInTheDocument());
    expect(mock.api.listCloneOperations.mock.calls.length).toBeGreaterThanOrEqual(2);
  });

  it('announces success including the empty-remote explanation', async () => {
    mock.api.listCloneOperations.mockResolvedValueOnce({
      operations: [
        cloneOperation({
          state: 'succeeded',
          destination: 'empty-clone',
          published: {
            repoKey: 'empty-clone',
            path: '/work/space/empty-clone',
            hasHead: false,
            publishedAt: '2026-09-04T12:02:00Z',
          },
        }),
      ],
    });
    renderSection();
    expect(await screen.findByText('empty-clone')).toBeInTheDocument();
    expect(
      screen.getByText(/an initial commit is required before feature use/),
    ).toBeInTheDocument();
  });

  it('names the owning server for remote connections', async () => {
    mock.api.listCloneOperations.mockResolvedValue({
      operations: [cloneOperation({ state: 'cleanup_pending', destination: 'remote-stuck' })],
    });
    renderSection(
      readinessWithRoots(eligibleRoots),
      readyConnection('remote-1', 'remote', 'remote'),
    );
    expect(await screen.findByText('remote-stuck')).toBeInTheDocument();
    expect(screen.getAllByText(/the remote server/).length).toBeGreaterThan(0);
  });
});

describe('CloneRepositorySection local root selection', () => {
  function rootEntry(
    path: string,
    overrides: Partial<{ valid: boolean; cloneEligible: boolean }> = {},
  ) {
    return { path, valid: true, cloneEligible: true, ...overrides };
  }

  /** A stateful harness mirroring how Settings applies authoritative snapshots. */
  function SectionHarness({
    initial,
    connection,
  }: {
    initial: ReadinessSnapshot;
    connection: ConnectionState;
  }) {
    const [readiness, setReadiness] = useState(initial);
    return (
      <CloneRepositorySection
        readiness={readiness}
        connection={connection}
        onReadinessChanged={setReadiness}
      />
    );
  }

  it('persists a chosen folder as a root, refreshes, and selects the authoritative entry', async () => {
    const user = userEvent.setup();
    const added = readinessWithRoots([rootEntry('/work/space'), rootEntry('/work/new')]);
    mock.api.pickWorkspaceDirectory.mockResolvedValue({ path: '/work/new' });
    mock.api.addWorkspaceRoot.mockResolvedValue(added);
    render(
      <SectionHarness
        initial={readinessWithRoots(eligibleRoots)}
        connection={readyConnection('local-1')}
      />,
    );
    await screen.findByLabelText('Repository URL');

    await user.click(screen.getByRole('button', { name: /choose folder/i }));

    // Root persistence completes and the authoritative snapshot reaches
    // the owning surface; the new root becomes the selection.
    await waitFor(() => expect(mock.api.addWorkspaceRoot).toHaveBeenCalledWith('/work/new'));
    const select = screen.getByLabelText('Destination root') as HTMLSelectElement;
    await waitFor(() => expect(select.value).toBe('/work/new'));
    expect([...select.options].map((option) => option.value)).toEqual(['/work/space', '/work/new']);
  });

  it('cancelling the chooser preserves the root, form inputs, and starts nothing', async () => {
    const user = userEvent.setup();
    mock.api.pickWorkspaceDirectory.mockResolvedValue({ path: null });
    renderSection();
    await screen.findByLabelText('Repository URL');
    await user.type(screen.getByLabelText('Repository URL'), 'https://example.com/acme/x.git');
    await user.clear(screen.getByLabelText('Folder name'));
    await user.type(screen.getByLabelText('Folder name'), 'kept');

    await user.click(screen.getByRole('button', { name: /choose folder/i }));

    await waitFor(() => expect(mock.api.pickWorkspaceDirectory).toHaveBeenCalled());
    expect(mock.api.addWorkspaceRoot).not.toHaveBeenCalled();
    expect(mock.api.startClone).not.toHaveBeenCalled();
    expect(screen.getByLabelText('Repository URL')).toHaveValue('https://example.com/acme/x.git');
    expect(screen.getByLabelText('Folder name')).toHaveValue('kept');
    const select = screen.getByLabelText('Destination root') as HTMLSelectElement;
    expect(select.value).toBe('/work/space');
  });

  it('a failed save keeps the active configuration and retains the form for retry', async () => {
    const user = userEvent.setup();
    mock.api.pickWorkspaceDirectory.mockResolvedValue({ path: '/work/new' });
    mock.api.addWorkspaceRoot.mockRejectedValue(
      ipcError('invalid_workspace_root', 'Some workspace roots do not resolve.'),
    );
    renderSection();
    await screen.findByLabelText('Repository URL');
    await user.type(screen.getByLabelText('Folder name'), 'kept');

    await user.click(screen.getByRole('button', { name: /choose folder/i }));

    expect(await screen.findByText('Some workspace roots do not resolve.')).toBeInTheDocument();
    // The form (and every input) is retained for a retry.
    expect(screen.getByLabelText('Folder name')).toHaveValue('kept');
    const select = screen.getByLabelText('Destination root') as HTMLSelectElement;
    expect(select.value).toBe('/work/space');

    // A retry after the failure succeeds through the same boundary.
    mock.api.addWorkspaceRoot.mockResolvedValue(
      readinessWithRoots([rootEntry('/work/space'), rootEntry('/work/new')]),
    );
    await user.click(screen.getByRole('button', { name: /choose folder/i }));
    await waitFor(() => expect(mock.api.addWorkspaceRoot).toHaveBeenCalledTimes(2));
  });

  it('selects an already-configured root without adding a duplicate entry', async () => {
    const user = userEvent.setup();
    // The chooser returns a path that is already configured: the server
    // reports the same single entry and nothing is duplicated.
    mock.api.pickWorkspaceDirectory.mockResolvedValue({ path: '/work/space' });
    mock.api.addWorkspaceRoot.mockResolvedValue(readinessWithRoots(eligibleRoots));
    renderSection();
    await screen.findByLabelText('Repository URL');

    await user.click(screen.getByRole('button', { name: /choose folder/i }));

    await waitFor(() => expect(mock.api.addWorkspaceRoot).toHaveBeenCalledWith('/work/space'));
    const select = await screen.findByLabelText('Destination root');
    await waitFor(() => expect((select as HTMLSelectElement).value).toBe('/work/space'));
    expect([...(select as HTMLSelectElement).options]).toHaveLength(1);
  });

  it('shows the clone issue of an added root that is not clone-eligible and selects nothing', async () => {
    const user = userEvent.setup();
    mock.api.pickWorkspaceDirectory.mockResolvedValue({ path: '/work/is-a-repo' });
    mock.api.addWorkspaceRoot.mockResolvedValue(
      readySnapshot({
        workspaceRoots: [
          rootEntry('/work/space'),
          {
            path: '/work/is-a-repo',
            valid: true,
            cloneEligible: false,
            cloneIssue: {
              code: 'root_is_repository',
              class: 'blocking',
              title: 'Root is a repository',
              summary: 'This workspace root is itself a git repository.',
            },
          },
        ],
      }),
    );
    renderSection();
    await screen.findByLabelText('Repository URL');

    await user.click(screen.getByRole('button', { name: /choose folder/i }));

    expect(
      await screen.findByText('This workspace root is itself a git repository.'),
    ).toBeInTheDocument();
    const select = screen.getByLabelText('Destination root') as HTMLSelectElement;
    expect(select.value).toBe('/work/space');
  });

  it('discards the chooser result when the server switches mid-sequence', async () => {
    const user = userEvent.setup();
    mock.api.pickWorkspaceDirectory.mockResolvedValue({ path: '/work/new' });
    let release: ((value: ReadinessSnapshot) => void) | undefined;
    mock.api.addWorkspaceRoot.mockImplementation(
      () =>
        new Promise<ReadinessSnapshot>((resolve) => {
          release = resolve;
        }),
    );
    const onReadinessChanged = vi.fn();
    const view = render(
      <CloneRepositorySection
        readiness={readinessWithRoots(eligibleRoots)}
        connection={readyConnection('alpha')}
        onReadinessChanged={onReadinessChanged}
      />,
    );
    await screen.findByLabelText('Repository URL');

    await user.click(screen.getByRole('button', { name: /choose folder/i }));
    await waitFor(() => expect(mock.api.addWorkspaceRoot).toHaveBeenCalled());
    // The connection flips to another server while the save is in flight.
    view.rerender(
      <CloneRepositorySection
        readiness={readinessWithRoots(eligibleRoots)}
        connection={readyConnection('beta', 'beta server')}
        onReadinessChanged={onReadinessChanged}
      />,
    );
    release?.(readinessWithRoots([rootEntry('/work/space'), rootEntry('/work/new')]));

    // The stale result never reaches the new server's UI and nothing is
    // selected there.
    await new Promise((resolve) => setTimeout(resolve, 25));
    expect(onReadinessChanged).not.toHaveBeenCalled();
    const select = screen.getByLabelText('Destination root') as HTMLSelectElement;
    expect(select.value).toBe('/work/space');
  });

  it('exposes no chooser or typed-path entry on a remote connection', async () => {
    renderSection(
      readinessWithRoots(eligibleRoots),
      readyConnection('remote-1', 'remote', 'remote'),
    );
    await screen.findByLabelText('Repository URL');
    expect(screen.queryByRole('button', { name: /choose folder/i })).toBeNull();
    expect(screen.queryByRole('textbox', { name: /folder path on the server/i })).toBeNull();
    // The root list itself is still offered from the server's config.
    expect(screen.getByLabelText('Destination root')).toHaveValue('/work/space');
  });

  it('explains the administrator action when no roots are usable on a remote server', async () => {
    renderSection(readinessWithRoots([]), readyConnection('remote-1', 'remote', 'remote'));
    expect(
      await screen.findByText(/ask the server administrator to configure a writable/i),
    ).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /choose folder/i })).toBeNull();
    expect(screen.getByRole('button', { name: 'Clone repository' })).toBeDisabled();
  });

  it('offers the chooser when no roots exist yet on a local server', async () => {
    renderSection(readinessWithRoots([]));
    expect(await screen.findByText(/No clone-eligible workspace root yet/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /choose folder/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Clone repository' })).toBeDisabled();
  });
});
