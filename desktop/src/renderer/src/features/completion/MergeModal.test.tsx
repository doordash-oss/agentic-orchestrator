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

import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MergeModalBody } from './MergeModal';
import { STATUS_LABELS } from './completionShared';
import { ipcError } from '../../test/agenticoMock';
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

  it('renders a failed merge as a compact ErrorSurface whose remediation hint is the rebase handoff', async () => {
    const dispatchAction = vi.fn(() =>
      Promise.reject(ipcError('merge_conflict', 'The merge conflicted with the base branch.')),
    );
    render(<MergeModalBody {...props({ dispatchAction })} />);
    fireEvent.click(screen.getByRole('button', { name: 'Merge' }));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveClass('error-surface', 'error-surface--compact');
    expect(screen.getByText('merge_conflict')).toHaveClass('error-surface__code');
    expect(screen.getByText('The merge conflicted with the base branch.')).toBeVisible();
    // The hand-written rebase handoff prose is now the failure card's remediation hint.
    const hint = screen.getByText(/Use Start rebase pass in the feature's aftercare workspace/);
    expect(hint).toHaveClass('error-surface__remediation-hint');
    expect(screen.queryByRole('button', { name: /Hand off to rebase/i })).not.toBeInTheDocument();
    // The legacy failure-result markup is gone.
    expect(document.querySelector('.completion-workspace__result--failure')).toBeNull();
    expect(document.querySelector('.completion-workspace__merge-handoff-hint')).toBeNull();
  });

  it('shows the empty state when nothing is mergeable', () => {
    render(<MergeModalBody {...props({ preflight: { ...preflight, repos: [] } })} />);
    expect(screen.getByText(/No local repositories to merge/)).toBeInTheDocument();
  });

  it('reports unmerged commits and labels the action as an update', () => {
    render(
      <MergeModalBody
        featureId="abcd1234ef567890"
        preflight={{
          featureId: 'abcd1234ef567890',
          sourceRevision: 'rev-1',
          canMarkDone: false,
          repos: [
            {
              repo: 'core',
              publishable: false,
              touched: true,
              status: 'unmerged_changes',
              pendingCommits: 2,
              baseBranch: 'main',
              branch: 'feature/x',
            },
          ],
        }}
        dispatchAction={vi.fn()}
        onDispatched={vi.fn()}
      />,
    );

    expect(screen.getByText('2 commits not in main')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Merge updates' })).toBeInTheDocument();
    // The status chip must read as prose, never as the wire token.
    expect(screen.getByText('Unmerged changes')).toBeInTheDocument();
    expect(screen.queryByText('unmerged_changes')).not.toBeInTheDocument();
  });

  it('labels every undelivered-work status without falling back to the token', () => {
    expect(STATUS_LABELS.unpublished_changes).toBe('Unpublished changes');
    expect(STATUS_LABELS.unmerged_changes).toBe('Unmerged changes');
  });
});
