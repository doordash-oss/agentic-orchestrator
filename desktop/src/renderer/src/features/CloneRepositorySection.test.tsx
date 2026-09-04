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

import { describe, expect, it, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { CloneRepositorySection } from './CloneRepositorySection';
import {
  cloneOperation,
  installAgenticoMock,
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
