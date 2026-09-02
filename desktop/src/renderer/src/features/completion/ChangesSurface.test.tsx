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

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { act, render, screen, fireEvent, waitFor } from '@testing-library/react';
import { ChangesSurface } from './ChangesSurface';
import { installAgenticoMock } from '../../test/agenticoMock';
import type {
  CompletionPreflightResult,
  ConnectionState,
  RepositoryDiffResult,
} from '../../../../shared/ipc';

const READY_LOCAL: ConnectionState = {
  status: 'ready',
  stage: 'ready',
  detail: 'Runtime ready.',
  ownership: 'app-owned',
  kind: 'local',
};

const READY_REMOTE: ConnectionState = {
  status: 'ready',
  stage: 'ready',
  detail: 'Runtime ready.',
  ownership: 'external',
  kind: 'remote',
};

const preflight: CompletionPreflightResult = {
  featureId: 'f',
  sourceRevision: 'r',
  canMarkDone: true,
  repos: [{ repo: 'repo-a', publishable: true, touched: true, status: 'eligible' }],
};
const diff: RepositoryDiffResult = {
  featureId: 'f',
  repo: 'repo-a',
  files: [{ path: 'src/foo.go', operation: 'modify', addedLines: 3, removedLines: 1 }],
};

function props(over?: Partial<Parameters<typeof ChangesSurface>[0]>) {
  return {
    featureId: 'f',
    preflight,
    loading: false,
    error: null,
    onRetry: vi.fn(),
    getRepositoryDiff: vi.fn(() => Promise.resolve(diff)),
    openExternal: vi.fn(() => Promise.resolve({ ok: true })),
    revealPath: vi.fn(() => Promise.resolve({ ok: true })),
    copyText: vi.fn(() => Promise.resolve({ ok: true })),
    ...over,
  };
}

describe('ChangesSurface', () => {
  // The surface reads locality from the live connection state; default to a
  // ready local runtime, individual tests override.
  beforeEach(() => {
    installAgenticoMock({ connection: READY_LOCAL });
  });

  it('presents a change manifest and loads the first repository immediately', async () => {
    const getRepositoryDiff = vi.fn(() => Promise.resolve(diff));
    render(<ChangesSurface {...props({ getRepositoryDiff })} />);
    expect(screen.getByRole('heading', { name: 'Change manifest' })).toBeVisible();
    expect(screen.getByRole('tab', { name: /repo-a/ })).toBeInTheDocument();
    await waitFor(() => expect(getRepositoryDiff).toHaveBeenCalledWith('f', 'repo-a'));
  });

  it('loads a repo diff on selection', async () => {
    const multiRepoPreflight: CompletionPreflightResult = {
      ...preflight,
      repos: [
        ...preflight.repos,
        { repo: 'repo-b', publishable: true, touched: true, status: 'eligible' },
      ],
    };
    const getRepositoryDiff = vi.fn(() => Promise.resolve(diff));
    render(<ChangesSurface {...props({ preflight: multiRepoPreflight, getRepositoryDiff })} />);
    fireEvent.click(screen.getByRole('tab', { name: /repo-b/ }));
    await waitFor(() => expect(getRepositoryDiff).toHaveBeenCalledWith('f', 'repo-b'));
    expect(await screen.findByText('src/foo.go')).toBeInTheDocument();
  });

  it('uses a stable manifest skeleton while repository data is loading', () => {
    render(<ChangesSurface {...props({ loading: true, preflight: null })} />);
    expect(screen.getByLabelText('Loading change manifest')).toBeVisible();
  });

  it('shows an error with retry', () => {
    const onRetry = vi.fn();
    render(<ChangesSurface {...props({ error: 'nope', preflight: null, onRetry })} />);
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(onRetry).toHaveBeenCalled();
  });

  it('renders the diff warning as a status surface the no-changes message yields to', async () => {
    const warningDiff: RepositoryDiffResult = {
      featureId: 'f',
      repo: 'repo-a',
      files: [],
      error: {
        code: 'repository_worktree_unavailable',
        class: 'warning',
        title: 'Worktree unavailable',
        summary: 'The worktree for repository "repo-a" is not available.',
        context: { repositories: [{ name: 'repo-a' }] },
      },
    };
    const getRepositoryDiff = vi.fn(() => Promise.resolve(warningDiff));
    render(<ChangesSurface {...props({ getRepositoryDiff })} />);

    const codeTag = await screen.findByText('repository_worktree_unavailable');
    expect(codeTag).toHaveClass('error-surface__code');
    const surface = codeTag.closest('.error-surface');
    expect(surface).not.toBeNull();
    expect(surface).toHaveAttribute('role', 'status');
    expect(surface?.querySelector('.error-surface__label')).toHaveTextContent('Warning');
    expect(surface?.querySelector('.error-surface__action')).toBeNull();
    expect(screen.getByText('Worktree unavailable')).toBeVisible();

    // The no-changes message yields to the warning, and the old
    // partial-failure span is gone.
    expect(screen.queryByText('No local changes in this repository.')).not.toBeInTheDocument();
    expect(document.querySelector('.completion-workspace__partial-failure')).toBeNull();
  });
});

describe('ChangesSurface worktree affordance by locality', () => {
  it('reveals the worktree through the OS on a local server', async () => {
    installAgenticoMock({ connection: READY_LOCAL });
    const revealPath = vi.fn(() => Promise.resolve({ ok: true }));
    render(<ChangesSurface {...props({ revealPath })} />);

    const button = await screen.findByRole('button', { name: 'Reveal in Finder' });
    fireEvent.click(button);

    await waitFor(() => expect(revealPath).toHaveBeenCalledWith('f', 'repo-a'));
    // Local behavior is exactly fire-and-forget: no confirm, no error line.
    expect(screen.queryByText(/copied/i)).toBeNull();
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('copies the server-reported path and confirms it refers to the server host', async () => {
    installAgenticoMock({ connection: READY_REMOTE });
    const copyText = vi.fn(() => Promise.resolve({ ok: true }));
    const serverPath = '/srv/agentico/features/f/repo-a';
    const revealPath = vi.fn(() => Promise.resolve({ ok: true, path: serverPath }));
    render(<ChangesSurface {...props({ revealPath, copyText })} />);

    fireEvent.click(await screen.findByRole('button', { name: 'Copy Path' }));

    await waitFor(() => expect(copyText).toHaveBeenCalledWith(serverPath));
    const confirmation = await screen.findByRole('status');
    expect(confirmation).toHaveTextContent(serverPath);
    expect(confirmation).toHaveTextContent(/server host/i);
  });

  it('surfaces an actionable inline error when the server path call fails', async () => {
    installAgenticoMock({ connection: READY_REMOTE });
    const revealPath = vi.fn(() =>
      Promise.reject(new Error('not_found: The feature no longer exists on the server.')),
    );
    render(<ChangesSurface {...props({ revealPath })} />);

    fireEvent.click(await screen.findByRole('button', { name: 'Copy Path' }));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent(/could not copy the worktree path/i);
    expect(alert).toHaveTextContent('The feature no longer exists on the server.');
  });

  it('surfaces an actionable inline error when the clipboard write fails', async () => {
    installAgenticoMock({ connection: READY_REMOTE });
    const copyText = vi.fn(() => Promise.reject(new Error('denied')));
    const revealPath = vi.fn(() => Promise.resolve({ ok: true, path: '/srv/wt' }));
    render(<ChangesSurface {...props({ revealPath, copyText })} />);

    fireEvent.click(await screen.findByRole('button', { name: 'Copy Path' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(/could not copy the worktree path/i);
  });

  it('swaps the affordance with the connection kind without remounting', async () => {
    const mock = installAgenticoMock({ connection: READY_LOCAL });
    const revealPath = vi.fn(() => Promise.resolve({ ok: true, path: '/srv/wt' }));
    render(<ChangesSurface {...props({ revealPath })} />);

    expect(await screen.findByRole('button', { name: 'Reveal in Finder' })).toBeVisible();

    act(() => mock.emitConnection(READY_REMOTE));
    expect(await screen.findByRole('button', { name: 'Copy Path' })).toBeVisible();

    act(() => mock.emitConnection(READY_LOCAL));
    expect(await screen.findByRole('button', { name: 'Reveal in Finder' })).toBeVisible();
  });
});
