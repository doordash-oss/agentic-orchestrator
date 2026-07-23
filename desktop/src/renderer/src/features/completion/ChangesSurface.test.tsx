import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { ChangesSurface } from './ChangesSurface';
import type { CompletionPreflightResult, RepositoryDiffResult } from '../../../../shared/ipc';

const preflight: CompletionPreflightResult = {
  featureId: 'f', sourceRevision: 'r', canMarkDone: true,
  repos: [{ repo: 'repo-a', publishable: true, touched: true, status: 'eligible' }],
};
const diff: RepositoryDiffResult = {
  featureId: 'f', repo: 'repo-a',
  files: [{ path: 'src/foo.go', operation: 'modify', addedLines: 3, removedLines: 1 }],
};

function props(over?: Partial<Parameters<typeof ChangesSurface>[0]>) {
  return {
    featureId: 'f', preflight, loading: false, error: null, onRetry: vi.fn(),
    getRepositoryDiff: vi.fn(() => Promise.resolve(diff)),
    openExternal: vi.fn(() => Promise.resolve({ ok: true })),
    revealPath: vi.fn(() => Promise.resolve({ ok: true })),
    ...over,
  };
}

describe('ChangesSurface', () => {
  it('lists repositories from preflight', () => {
    render(<ChangesSurface {...props()} />);
    expect(screen.getByText('repo-a')).toBeInTheDocument();
  });

  it('loads a repo diff on selection', async () => {
    const getRepositoryDiff = vi.fn(() => Promise.resolve(diff));
    render(<ChangesSurface {...props({ getRepositoryDiff })} />);
    fireEvent.click(screen.getByText('repo-a'));
    await waitFor(() => expect(getRepositoryDiff).toHaveBeenCalledWith('f', 'repo-a'));
    expect(await screen.findByText('src/foo.go')).toBeInTheDocument();
  });

  it('shows an error with retry', () => {
    const onRetry = vi.fn();
    render(<ChangesSurface {...props({ error: 'nope', preflight: null, onRetry })} />);
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(onRetry).toHaveBeenCalled();
  });
});
