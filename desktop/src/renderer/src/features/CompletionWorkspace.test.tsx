import { describe, it, expect, vi, beforeEach } from 'vitest';
import { act, render, screen, fireEvent, waitFor } from '@testing-library/react';
import { CompletionWorkspace } from './CompletionWorkspace';
import type { CompletionPreflightResult, RepositoryDiffResult } from '../../../shared/ipc';
import { matchMediaState } from '../test/setup';

const mockPreflight: CompletionPreflightResult = {
  featureId: 'feat-test',
  sourceRevision: 'rev-abc',
  canMarkDone: true,
  repos: [
    { repo: 'repo-a', publishable: true, touched: true, status: 'eligible' },
    {
      repo: 'repo-b',
      publishable: true,
      touched: true,
      status: 'already_published',
      prUrl: 'https://github.example/repo-b/pull/1',
    },
    { repo: 'local-only', publishable: false, touched: true, status: 'eligible' },
    { repo: 'repo-c', publishable: false, touched: false, status: 'ineligible' },
  ],
};

const mockDiff: RepositoryDiffResult = {
  featureId: 'feat-test',
  repo: 'repo-a',
  files: [
    { path: 'src/foo.go', operation: 'modify', addedLines: 10, removedLines: 2 },
    { path: 'src/bar.go', operation: 'add', addedLines: 50 },
    { path: 'README.md', operation: 'delete', removedLines: 5 },
  ],
};

const mockFileDiff: RepositoryDiffResult = {
  featureId: 'feat-test',
  repo: 'repo-a',
  files: [],
  fileDiff: 'diff --git a/src/foo.go b/src/foo.go\n@@ -1,3 +1,4 @@\n+new line',
};

function makeProps(overrides?: Partial<Parameters<typeof CompletionWorkspace>[0]>) {
  return {
    featureId: 'feat-test',
    featureName: 'Test Feature',
    onClose: vi.fn(),
    preflightCompletion: vi.fn(() => Promise.resolve(mockPreflight)),
    getRepositoryDiff: vi.fn(() => Promise.resolve(mockDiff)),
    dispatchAction: vi.fn(() => Promise.resolve({ result: 'published' })),
    generatePublishDescription: vi.fn(() =>
      Promise.resolve({ featureId: 'feat-test', title: 'Generated title', body: 'Generated body' }),
    ),
    openExternal: vi.fn(() => Promise.resolve({ ok: true })),
    revealPath: vi.fn(() => Promise.resolve({ ok: true })),
    ...overrides,
  };
}

describe('CompletionWorkspace', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    matchMediaState.narrowCockpit = false;
  });

  it('fetches and displays the completion preflight on mount', async () => {
    const props = makeProps();
    render(<CompletionWorkspace {...props} />);
    await waitFor(() => {
      expect(screen.getByText('repo-a')).toBeInTheDocument();
    });
    expect(props.preflightCompletion).toHaveBeenCalledWith('feat-test');
  });

  it('shows eligible and already-published repos with status labels', async () => {
    render(<CompletionWorkspace {...makeProps()} />);
    await waitFor(() => {
      expect(screen.getAllByText('Eligible')).toHaveLength(2);
      expect(screen.getByText('Already published')).toBeInTheDocument();
      expect(screen.getByText('Local only')).toBeInTheDocument();
    });
  });

  it('loads repository diff when a repo is selected', async () => {
    const props = makeProps();
    render(<CompletionWorkspace {...props} />);
    await waitFor(() => {
      expect(screen.getByText('repo-a')).toBeInTheDocument();
    });
    fireEvent.click(screen.getByText('repo-a'));
    await waitFor(() => {
      expect(props.getRepositoryDiff).toHaveBeenCalledWith('feat-test', 'repo-a');
    });
  });

  it('shows PR link for already-published repos', async () => {
    render(<CompletionWorkspace {...makeProps()} />);
    await waitFor(() => {
      expect(screen.getByText('PR ↗')).toBeInTheDocument();
    });
  });

  it('reveals a server-approved repository path from the repo list', async () => {
    const props = makeProps();
    render(<CompletionWorkspace {...props} />);
    await waitFor(() => {
      expect(screen.getByText('repo-a')).toBeInTheDocument();
    });
    const revealButton = screen.getAllByText('Reveal')[0];
    if (revealButton === undefined) throw new Error('Reveal button not found');
    fireEvent.click(revealButton);

    expect(props.revealPath).toHaveBeenCalledWith('feat-test', 'repo-a');
  });

  it('preselects eligible repos in the publish step', async () => {
    const props = makeProps();
    render(<CompletionWorkspace {...props} />);
    await waitFor(() => {
      expect(screen.getByText('repo-a')).toBeInTheDocument();
    });
    fireEvent.click(screen.getByRole('button', { name: 'Completion step: publish' }));
    await waitFor(() => {
      const checkbox = screen.getByRole('checkbox', { name: 'repo-a' }) as HTMLInputElement;
      expect(checkbox.checked).toBe(true);
      expect(screen.queryByRole('checkbox', { name: 'local-only' })).not.toBeInTheDocument();
    });
  });

  it('lists local-only touched repos in the merge step instead of publish scope', async () => {
    render(<CompletionWorkspace {...makeProps()} />);
    await waitFor(() => {
      expect(screen.getByText('repo-a')).toBeInTheDocument();
    });
    fireEvent.click(screen.getByRole('button', { name: 'Completion step: merge' }));
    await waitFor(() => {
      expect(screen.getByText('local-only')).toBeInTheDocument();
    });
  });

  it('disables publish when no title is provided', async () => {
    render(<CompletionWorkspace {...makeProps()} />);
    await waitFor(() => {
      expect(screen.getByText('repo-a')).toBeInTheDocument();
    });
    fireEvent.click(screen.getByRole('button', { name: 'Completion step: publish' }));
    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Publish' })).toBeDisabled();
    });
  });

  it('enables publish when title is provided and repos are selected', async () => {
    const props = makeProps();
    render(<CompletionWorkspace {...props} />);
    await waitFor(() => {
      expect(screen.getByText('repo-a')).toBeInTheDocument();
    });
    fireEvent.click(screen.getByRole('button', { name: 'Completion step: publish' }));
    await waitFor(() => {
      expect(screen.getByPlaceholderText('Enter PR title')).toBeInTheDocument();
    });
    fireEvent.change(screen.getByPlaceholderText('Enter PR title'), {
      target: { value: 'Test PR Title' },
    });
    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Publish' })).toBeEnabled();
    });
  });

  it('generates and applies a server-authored PR narrative', async () => {
    const props = makeProps();
    render(<CompletionWorkspace {...props} />);
    await waitFor(() => {
      expect(screen.getByText('repo-a')).toBeInTheDocument();
    });
    fireEvent.click(screen.getByRole('button', { name: 'Completion step: publish' }));
    await waitFor(() => {
      expect(screen.getByText('Generate PR narrative')).toBeInTheDocument();
    });

    fireEvent.click(screen.getByText('Generate PR narrative'));

    await waitFor(() => {
      expect(props.generatePublishDescription).toHaveBeenCalledWith('feat-test', ['repo-a']);
      expect(screen.getByDisplayValue('Generated title')).toBeInTheDocument();
      expect(screen.getByDisplayValue('Generated body')).toBeInTheDocument();
    });
  });

  it('dispatches publish action with selected repos and title', async () => {
    const props = makeProps();
    render(<CompletionWorkspace {...props} />);
    await waitFor(() => {
      expect(screen.getByText('repo-a')).toBeInTheDocument();
    });
    fireEvent.click(screen.getByRole('button', { name: 'Completion step: publish' }));
    await waitFor(() => {
      expect(screen.getByPlaceholderText('Enter PR title')).toBeInTheDocument();
    });
    fireEvent.change(screen.getByPlaceholderText('Enter PR title'), {
      target: { value: 'Test PR Title' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Publish' }));
    await waitFor(() => {
      expect(props.dispatchAction).toHaveBeenCalledWith('feat-test', 'publish', {
        source_revision: 'rev-abc',
        repos: ['repo-a'],
        title: 'Test PR Title',
      });
    });
  });

  it('shows mark-done as available when canMarkDone is true', async () => {
    render(<CompletionWorkspace {...makeProps()} />);
    await waitFor(() => {
      expect(screen.getByText('repo-a')).toBeInTheDocument();
    });
    fireEvent.click(screen.getByRole('button', { name: 'Completion step: done' }));
    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Mark Done' })).toBeEnabled();
    });
  });

  it('shows mark-done blocker when canMarkDone is false', async () => {
    const props = makeProps({
      preflightCompletion: vi.fn(() =>
        Promise.resolve({ ...mockPreflight, canMarkDone: false, markDoneBlocker: 'not ready' }),
      ),
    });
    render(<CompletionWorkspace {...props} />);
    await waitFor(() => {
      expect(screen.getByText('repo-a')).toBeInTheDocument();
    });
    fireEvent.click(screen.getByRole('button', { name: 'Completion step: done' }));
    await waitFor(() => {
      expect(screen.getByText('not ready')).toBeInTheDocument();
    });
  });

  it('requires exact feature name match for delete', async () => {
    const props = makeProps();
    render(<CompletionWorkspace {...props} />);
    await waitFor(() => {
      expect(screen.getByText('repo-a')).toBeInTheDocument();
    });
    fireEvent.click(screen.getByRole('button', { name: 'Completion step: delete' }));
    await waitFor(() => {
      expect(screen.getByPlaceholderText('Test Feature')).toBeInTheDocument();
    });
    const deleteBtn = screen.getByRole('button', { name: 'Delete feature' });
    expect(deleteBtn).toBeDisabled();
    fireEvent.change(screen.getByPlaceholderText('Test Feature'), {
      target: { value: 'Wrong Name' },
    });
    expect(deleteBtn).toBeDisabled();
    fireEvent.change(screen.getByPlaceholderText('Test Feature'), {
      target: { value: 'Test Feature' },
    });
    expect(deleteBtn).not.toBeDisabled();
  });

  it('keeps the workspace open and shows the failure when delete fails', async () => {
    const props = makeProps({
      dispatchAction: vi.fn(() => Promise.reject(new Error('E_IPC: stale source revision'))),
    });
    render(<CompletionWorkspace {...props} />);
    await waitFor(() => {
      expect(screen.getByText('repo-a')).toBeInTheDocument();
    });
    fireEvent.click(screen.getByRole('button', { name: 'Completion step: delete' }));
    fireEvent.change(screen.getByPlaceholderText('Test Feature'), {
      target: { value: 'Test Feature' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Delete feature' }));
    await waitFor(() => {
      expect(screen.getByText(/stale source revision/)).toBeInTheDocument();
    });
    expect(props.onClose).not.toHaveBeenCalled();
  });

  it('refreshes after cleanup and closes after successful delete', async () => {
    const props = makeProps();
    render(<CompletionWorkspace {...props} />);
    await waitFor(() => {
      expect(screen.getByText('repo-a')).toBeInTheDocument();
    });
    fireEvent.click(screen.getByRole('button', { name: 'Completion step: cleanup' }));
    await waitFor(() => {
      expect(screen.getByRole('heading', { name: 'Clean worktrees' })).toBeInTheDocument();
    });
    fireEvent.click(screen.getByRole('button', { name: 'Clean worktrees' }));
    await waitFor(() => {
      expect(props.dispatchAction).toHaveBeenCalledWith('feat-test', 'cleanup', {
        source_revision: 'rev-abc',
        target: 'worktrees',
      });
      expect(props.preflightCompletion).toHaveBeenCalledTimes(2);
    });

    fireEvent.click(screen.getByRole('button', { name: 'Completion step: delete' }));
    fireEvent.change(screen.getByPlaceholderText('Test Feature'), {
      target: { value: 'Test Feature' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Delete feature' }));
    await waitFor(() => {
      expect(props.dispatchAction).toHaveBeenCalledWith('feat-test', 'delete', {
        source_revision: 'rev-abc',
      });
      expect(props.onClose).toHaveBeenCalled();
    });
  });

  it('keeps delete disabled until cleanup refreshes the source revision', async () => {
    let resolveRefresh!: (result: CompletionPreflightResult) => void;
    const refreshedPreflight = { ...mockPreflight, sourceRevision: 'rev-after-cleanup' };
    const refreshPromise = new Promise<CompletionPreflightResult>((resolve) => {
      resolveRefresh = resolve;
    });
    const props = makeProps({
      preflightCompletion: vi
        .fn()
        .mockResolvedValueOnce(mockPreflight)
        .mockReturnValueOnce(refreshPromise),
      dispatchAction: vi.fn((_featureId, action) =>
        Promise.resolve({ result: action === 'cleanup' ? 'cleaned' : 'deleted' }),
      ),
    });
    render(<CompletionWorkspace {...props} />);
    await waitFor(() => {
      expect(screen.getByText('repo-a')).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole('button', { name: 'Completion step: cleanup' }));
    fireEvent.click(screen.getByRole('button', { name: 'Clean worktrees' }));
    await waitFor(() => {
      expect(props.preflightCompletion).toHaveBeenCalledTimes(2);
    });

    fireEvent.click(screen.getByRole('button', { name: 'Completion step: delete' }));
    fireEvent.change(screen.getByPlaceholderText('Test Feature'), {
      target: { value: 'Test Feature' },
    });
    expect(screen.getByRole('button', { name: 'Delete feature' })).toBeDisabled();

    await act(async () => {
      resolveRefresh(refreshedPreflight);
      await refreshPromise;
    });
    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Delete feature' })).not.toBeDisabled();
    });

    fireEvent.click(screen.getByRole('button', { name: 'Delete feature' }));
    await waitFor(() => {
      expect(props.dispatchAction).toHaveBeenCalledWith('feat-test', 'delete', {
        source_revision: 'rev-after-cleanup',
      });
    });
  });

  it('shows side-by-side toggle in diff view', async () => {
    const props = makeProps();
    render(<CompletionWorkspace {...props} />);
    await waitFor(() => {
      expect(screen.getByText('repo-a')).toBeInTheDocument();
    });
    fireEvent.click(screen.getByText('repo-a'));
    await waitFor(() => {
      expect(screen.getByLabelText('Side-by-side')).toBeInTheDocument();
    });
    expect((screen.getByLabelText('Side-by-side') as HTMLInputElement).checked).toBe(true);
  });

  it('defaults constrained diff view to unified until the user overrides it', async () => {
    matchMediaState.narrowCockpit = true;
    const props = makeProps();
    render(<CompletionWorkspace {...props} />);
    await waitFor(() => {
      expect(screen.getByText('repo-a')).toBeInTheDocument();
    });
    fireEvent.click(screen.getByText('repo-a'));
    await waitFor(() => {
      expect(screen.getByLabelText('Side-by-side')).toBeInTheDocument();
    });
    const toggle = screen.getByLabelText('Side-by-side') as HTMLInputElement;
    expect(toggle.checked).toBe(false);
    fireEvent.click(toggle);
    expect(toggle.checked).toBe(true);
  });

  it('shows error and retry when preflight fails', async () => {
    const props = makeProps({
      preflightCompletion: vi.fn(() => Promise.reject(new Error('E_IPC: server unavailable'))),
    });
    render(<CompletionWorkspace {...props} />);
    await waitFor(() => {
      expect(screen.getByText('server unavailable')).toBeInTheDocument();
    });
    expect(screen.getByText('Retry')).toBeInTheDocument();
  });

  it('shows no changes message for repos with empty diff', async () => {
    const props = makeProps({
      getRepositoryDiff: vi.fn(() =>
        Promise.resolve({ featureId: 'feat-test', repo: 'repo-a', files: [] }),
      ),
    });
    render(<CompletionWorkspace {...props} />);
    await waitFor(() => {
      expect(screen.getByText('repo-a')).toBeInTheDocument();
    });
    fireEvent.click(screen.getByText('repo-a'));
    await waitFor(() => {
      expect(screen.getByText('No changes')).toBeInTheDocument();
    });
  });

  it('shows partial failure message when diff inspection fails', async () => {
    const props = makeProps({
      getRepositoryDiff: vi.fn(() =>
        Promise.resolve({
          featureId: 'feat-test',
          repo: 'repo-x',
          files: [],
          partialFailure: 'worktree not available',
        }),
      ),
    });
    render(<CompletionWorkspace {...props} />);
    await waitFor(() => {
      expect(screen.getByText('repo-a')).toBeInTheDocument();
    });
    fireEvent.click(screen.getByText('repo-a'));
    await waitFor(() => {
      expect(screen.getByText('worktree not available')).toBeInTheDocument();
    });
  });

  it('ignores stale repository diff responses after the selected repo changes', async () => {
    let resolveRepoA!: (value: typeof mockDiff) => void;
    let resolveRepoB!: (value: typeof mockDiff) => void;
    const props = makeProps({
      getRepositoryDiff: vi.fn((_featureId: string, repo: string) => {
        if (repo === 'repo-a') {
          return new Promise<RepositoryDiffResult>((resolve) => {
            resolveRepoA = resolve;
          });
        }
        return new Promise<RepositoryDiffResult>((resolve) => {
          resolveRepoB = resolve;
        });
      }),
    });
    render(<CompletionWorkspace {...props} />);
    await screen.findByText('repo-a');

    fireEvent.click(screen.getByRole('button', { name: /repo-a/i }));
    fireEvent.click(screen.getByRole('button', { name: /repo-b/i }));

    await act(async () => {
      resolveRepoB({
        ...mockDiff,
        repo: 'repo-b',
        files: [{ path: 'current.ts', operation: 'modify', addedLines: 1, removedLines: 1 }],
      });
    });
    expect(await screen.findByText('current.ts')).toBeInTheDocument();

    await act(async () => {
      resolveRepoA({
        ...mockDiff,
        files: [{ path: 'stale.ts', operation: 'modify', addedLines: 9, removedLines: 9 }],
      });
    });
    expect(screen.queryByText('stale.ts')).not.toBeInTheDocument();
  });

  it('ignores stale file diff responses after the selected file changes', async () => {
    let resolveFoo!: (value: typeof mockFileDiff) => void;
    let resolveBar!: (value: typeof mockFileDiff) => void;
    const props = makeProps({
      getRepositoryDiff: vi.fn((_featureId: string, repo: string, filePath?: string) => {
        if (filePath === 'src/foo.go') {
          return new Promise<RepositoryDiffResult>((resolve) => {
            resolveFoo = resolve;
          });
        }
        if (filePath === 'src/bar.go') {
          return new Promise<RepositoryDiffResult>((resolve) => {
            resolveBar = resolve;
          });
        }
        return Promise.resolve({ ...mockDiff, repo });
      }),
    });
    render(<CompletionWorkspace {...props} />);
    await screen.findByText('repo-a');
    fireEvent.click(screen.getByRole('button', { name: /repo-a/i }));
    await screen.findByText('src/foo.go');

    fireEvent.click(screen.getByRole('button', { name: /src\/foo.go/i }));
    fireEvent.click(screen.getByRole('button', { name: /src\/bar.go/i }));

    await act(async () => {
      resolveBar({
        ...mockFileDiff,
        fileDiff: 'bar-current',
      });
    });
    await waitFor(() => {
      expect(screen.queryByText('Loading file diff…')).not.toBeInTheDocument();
    });
    expect(
      screen.getByRole('button', { name: /src\/bar.go/i }).classList.contains('is-selected'),
    ).toBe(true);

    await act(async () => {
      resolveFoo({
        ...mockFileDiff,
        fileDiff: 'foo-stale',
      });
    });
    expect(screen.queryByText('foo-stale')).not.toBeInTheDocument();
  });
});
