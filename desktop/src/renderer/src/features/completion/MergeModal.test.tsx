import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MergeModalBody } from './MergeModal';
import type { CompletionPreflightResult } from '../../../../shared/ipc';

const preflight: CompletionPreflightResult = {
  featureId: 'f',
  sourceRevision: 'rev-1',
  canMarkDone: false,
  repos: [
    {
      repo: 'local',
      publishable: false,
      touched: true,
      status: 'eligible',
      baseBranch: 'main',
      branch: 'feat/x',
    },
  ],
};
function props(over?: Partial<Parameters<typeof MergeModalBody>[0]>) {
  return {
    featureId: 'f',
    preflight,
    dispatchAction: vi.fn(() => Promise.resolve({ result: 'merged' })),
    onDispatched: vi.fn(),
    ...over,
  };
}

describe('MergeModalBody', () => {
  it('dispatches merge with source_revision', async () => {
    const dispatchAction = vi.fn(() => Promise.resolve({ result: 'merged' }));
    render(<MergeModalBody {...props({ dispatchAction })} />);
    fireEvent.click(screen.getByRole('button', { name: 'Merge' }));
    await waitFor(() =>
      expect(dispatchAction).toHaveBeenCalledWith('f', 'merge', { source_revision: 'rev-1' }),
    );
  });

  it('shows the aftercare rebase hint after a failed merge with no launch button', async () => {
    const dispatchAction = vi.fn(() => Promise.reject(new Error('conflict')));
    render(<MergeModalBody {...props({ dispatchAction })} />);
    fireEvent.click(screen.getByRole('button', { name: 'Merge' }));
    await screen.findByText(/Open the Rebase card in the feature's aftercare workspace/);
    expect(screen.queryByRole('button', { name: /Hand off to rebase/i })).not.toBeInTheDocument();
  });

  it('shows the empty state when nothing is mergeable', () => {
    render(<MergeModalBody {...props({ preflight: { ...preflight, repos: [] } })} />);
    expect(screen.getByText(/No local repositories to merge/)).toBeInTheDocument();
  });
});
