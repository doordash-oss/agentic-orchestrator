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

import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { CreateRepositorySection } from './CreateRepositorySection';
import {
  createRepositoryResult,
  installAgenticoMock,
  ipcError,
  readySnapshot,
  type AgenticoMock,
} from '../test/agenticoMock';
import type { ConnectionState, ReadinessSnapshot } from '../../../shared/ipc';

afterEach(cleanup);

let mock: AgenticoMock;

function readyConnection(serverKey: string, kind: 'local' | 'remote' = 'local'): ConnectionState {
  return {
    status: 'ready',
    stage: 'ready',
    detail: 'Connected.',
    ownership: 'external',
    kind,
    serverKey,
    serverName: kind === 'remote' ? 'remote' : 'local server',
  };
}

const eligibleRoots: ReadinessSnapshot['workspaceRoots'] = [
  { path: '/work/space', valid: true, cloneEligible: true },
];

beforeEach(() => {
  mock = installAgenticoMock({ readiness: readySnapshot({ workspaceRoots: eligibleRoots }) });
});

function renderSection(
  connection: ConnectionState = readyConnection('local-1'),
  onReadinessChanged: (snapshot: ReadinessSnapshot) => void = vi.fn(),
) {
  return render(
    <CreateRepositorySection
      readiness={readySnapshot({ workspaceRoots: eligibleRoots })}
      connection={connection}
      onReadinessChanged={onReadinessChanged}
    />,
  );
}

describe('CreateRepositorySection', () => {
  it('explains the initial-commit contract and requires consent before submission', async () => {
    const user = userEvent.setup();
    renderSection();
    expect(screen.getByText(/one empty initial commit on the main branch/i)).toBeInTheDocument();
    expect(screen.getByText(/no origin remote and no push/i)).toBeInTheDocument();
    await user.type(screen.getByLabelText('Repository folder name'), 'widget');

    // The checkbox is the acknowledgement gate: without it nothing is sent
    // and the server would refuse the request anyway.
    const submit = screen.getByRole('button', { name: 'Create repository' });
    expect(submit).toBeDisabled();
    expect(mock.api.createRepository).not.toHaveBeenCalled();

    await user.click(screen.getByRole('checkbox', { name: /initial empty commit/i }));
    expect(submit).toBeEnabled();
    expect(mock.api.createRepository).not.toHaveBeenCalled();
  });

  it('sends the consented request with the correct server destination and announces the result', async () => {
    const user = userEvent.setup();
    mock.api.createRepository.mockImplementation((input: { destination: string }) =>
      Promise.resolve(
        createRepositoryResult({
          repoKey: input.destination,
          path: `/work/space/${input.destination}`,
        }),
      ),
    );
    const onReadinessChanged = vi.fn();
    renderSection(readyConnection('local-1'), onReadinessChanged);
    await user.type(screen.getByLabelText('Repository folder name'), 'widget');
    await user.click(screen.getByRole('checkbox', { name: /initial empty commit/i }));
    await user.click(screen.getByRole('button', { name: 'Create repository' }));

    await waitFor(() => expect(mock.api.createRepository).toHaveBeenCalled());
    const request = mock.api.createRepository.mock.calls[0]?.[0] as Record<string, unknown>;
    expect(request).toMatchObject({
      rootPath: '/work/space',
      destination: 'widget',
      consent: true,
    });
    expect(typeof request.idempotencyKey).toBe('string');
    // The authoritative catalog is re-read so every surface sees the new
    // repository; success announces the created repository.
    await waitFor(() => expect(mock.api.getReadiness).toHaveBeenCalled());
    await waitFor(() => expect(onReadinessChanged).toHaveBeenCalled());
    expect(
      await screen.findByText(/Created widget at \/work\/space\/widget on this computer/),
    ).toBeInTheDocument();
    // Settings success never opens a feature or touches a creation draft.
    expect(screen.queryByRole('dialog', { name: /new feature/i })).toBeNull();
  });

  it('rejects invalid child names client-side and keeps the form', async () => {
    const user = userEvent.setup();
    renderSection();
    await user.type(screen.getByLabelText('Repository folder name'), 'bad/name');
    await user.click(screen.getByRole('checkbox', { name: /initial empty commit/i }));
    await user.click(screen.getByRole('button', { name: 'Create repository' }));
    expect(mock.api.createRepository).not.toHaveBeenCalled();
    expect(screen.getByText(/single new folder name \(no separators/i)).toBeInTheDocument();
  });

  it('maps canonical destination rejections onto the folder field', async () => {
    const user = userEvent.setup();
    mock.api.createRepository.mockRejectedValue(
      ipcError('clone_destination_exists', 'Something already exists at the destination path.'),
    );
    renderSection();
    await user.type(screen.getByLabelText('Repository folder name'), 'taken');
    await user.click(screen.getByRole('checkbox', { name: /initial empty commit/i }));
    await user.click(screen.getByRole('button', { name: 'Create repository' }));
    expect(
      await screen.findByText('Something already exists at the destination path.'),
    ).toBeInTheDocument();
    // The form is retained for a corrected retry.
    expect(screen.getByLabelText('Repository folder name')).toHaveValue('taken');
    expect(screen.getByRole('checkbox', { name: /initial empty commit/i })).toBeChecked();
  });

  it('maps root rejections onto the destination-root control', async () => {
    const user = userEvent.setup();
    mock.api.createRepository.mockRejectedValue(
      ipcError(
        'clone_root_ineligible',
        'The selected workspace root cannot accept new repositories.',
      ),
    );
    renderSection();
    await user.type(screen.getByLabelText('Repository folder name'), 'fresh');
    await user.click(screen.getByRole('checkbox', { name: /initial empty commit/i }));
    await user.click(screen.getByRole('button', { name: 'Create repository' }));
    expect(
      await screen.findByText('The selected workspace root cannot accept new repositories.'),
    ).toBeInTheDocument();
  });

  it('keeps duplicate submits from starting a second creation', async () => {
    const user = userEvent.setup();
    let release: (() => void) | undefined;
    mock.api.createRepository.mockImplementation(
      () =>
        new Promise((resolve) => {
          release = () => resolve(createRepositoryResult());
        }),
    );
    renderSection();
    await user.type(screen.getByLabelText('Repository folder name'), 'widget');
    await user.click(screen.getByRole('checkbox', { name: /initial empty commit/i }));
    const submit = screen.getByRole('button', { name: 'Create repository' });
    await user.click(submit);
    expect(submit).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Creating…' })).toBeDisabled();
    release?.();
    // The in-flight submit is over: the button returns from Creating… (it
    // stays disabled until the consent is re-acknowledged for a new
    // repository) and exactly one creation was started.
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Create repository' })).toBeInTheDocument(),
    );
    expect(screen.queryByRole('button', { name: 'Creating…' })).toBeNull();
    expect(mock.api.createRepository).toHaveBeenCalledTimes(1);
  });

  it('works against a configured remote root without any chooser entry', async () => {
    const user = userEvent.setup();
    mock.api.createRepository.mockResolvedValue(
      createRepositoryResult({
        repoKey: 'widget',
        root: '/srv/work',
        path: '/srv/work/widget',
      }),
    );
    renderSection(readyConnection('remote-1', 'remote'));
    await user.type(screen.getByLabelText('Repository folder name'), 'widget');
    await user.click(screen.getByRole('checkbox', { name: /initial empty commit/i }));
    await user.click(screen.getByRole('button', { name: 'Create repository' }));
    await waitFor(() => expect(mock.api.createRepository).toHaveBeenCalled());
    expect(screen.queryByRole('button', { name: /choose folder/i })).toBeNull();
    expect(
      await screen.findByText(/Created widget at \/srv\/work\/widget on the remote server\./),
    ).toBeInTheDocument();
  });
});
