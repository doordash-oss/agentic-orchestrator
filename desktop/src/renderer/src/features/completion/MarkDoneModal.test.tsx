import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MarkDoneModalBody } from './MarkDoneModal';
import type { CompletionPreflightResult } from '../../../../shared/ipc';

function pf(over: Partial<CompletionPreflightResult>): CompletionPreflightResult {
  return { featureId: 'f', sourceRevision: 'rev-1', canMarkDone: true, repos: [], ...over };
}

describe('MarkDoneModalBody', () => {
  it('dispatches mark-done when allowed', async () => {
    const dispatchAction = vi.fn(() => Promise.resolve({ result: 'done' }));
    render(
      <MarkDoneModalBody
        featureId="f"
        preflight={pf({ canMarkDone: true })}
        dispatchAction={dispatchAction}
        onDispatched={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Mark Done' }));
    await waitFor(() =>
      expect(dispatchAction).toHaveBeenCalledWith('f', 'mark-done', { source_revision: 'rev-1' }),
    );
  });

  it('shows the blocker instead of the button when blocked', () => {
    render(
      <MarkDoneModalBody
        featureId="f"
        preflight={pf({ canMarkDone: false, markDoneBlocker: 'merge first' })}
        dispatchAction={vi.fn()}
        onDispatched={vi.fn()}
      />,
    );
    expect(screen.getByText('merge first')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Mark Done' })).toBeNull();
  });
});
