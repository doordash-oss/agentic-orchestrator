import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { ChangesSurface } from './ChangesSurface';
import type { CompletionPreflightResult, RepositoryDiffResult } from '../../../../shared/ipc';

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
    ...over,
  };
}

describe('ChangesSurface', () => {
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
});
