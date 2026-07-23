import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { CleanupConfirm } from './CleanupConfirm';
import type { CompletionPreflightResult } from '../../../../shared/ipc';

const preflight: CompletionPreflightResult = {
  featureId: 'f',
  sourceRevision: 'rev-1',
  canMarkDone: false,
  repos: [],
};

describe('CleanupConfirm', () => {
  it('dispatches cleanup with source_revision and worktrees target', async () => {
    const dispatchAction = vi.fn(() => Promise.resolve({ result: 'cleaned' }));
    const onDispatched = vi.fn();
    render(
      <CleanupConfirm
        featureId="f"
        preflight={preflight}
        dispatchAction={dispatchAction}
        onClose={vi.fn()}
        onDispatched={onDispatched}
      />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Clean worktrees' }));
    await waitFor(() =>
      expect(dispatchAction).toHaveBeenCalledWith('f', 'cleanup', {
        source_revision: 'rev-1',
        target: 'worktrees',
      }),
    );
    await waitFor(() => expect(onDispatched).toHaveBeenCalled());
  });

  it('closes on cancel / Escape', () => {
    const onClose = vi.fn();
    render(
      <CleanupConfirm
        featureId="f"
        preflight={preflight}
        dispatchAction={vi.fn()}
        onClose={onClose}
        onDispatched={vi.fn()}
      />,
    );
    fireEvent.keyDown(window, { key: 'Escape' });
    expect(onClose).toHaveBeenCalled();
  });
});
