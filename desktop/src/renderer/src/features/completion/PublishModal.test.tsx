import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { PublishModalBody } from './PublishModal';
import type { CompletionPreflightResult } from '../../../../shared/ipc';

const preflight: CompletionPreflightResult = {
  featureId: 'f',
  sourceRevision: 'rev-1',
  canMarkDone: false,
  repos: [{ repo: 'repo-a', publishable: true, touched: true, status: 'eligible' }],
};
function props(over?: Partial<Parameters<typeof PublishModalBody>[0]>) {
  return {
    featureId: 'f',
    preflight,
    dispatchAction: vi.fn(() => Promise.resolve({ result: 'published repo-a' })),
    generatePublishDescription: vi.fn(() =>
      Promise.resolve({ featureId: 'f', title: 'T', body: 'B' }),
    ),
    openExternal: vi.fn(() => Promise.resolve({ ok: true })),
    onDispatched: vi.fn(),
    ...over,
  };
}

describe('PublishModalBody', () => {
  it('disables Publish until a title is entered', () => {
    render(<PublishModalBody {...props()} />);
    expect(screen.getByRole('button', { name: 'Publish' })).toBeDisabled();
    fireEvent.change(screen.getByLabelText('PR title'), { target: { value: 'My PR' } });
    expect(screen.getByRole('button', { name: 'Publish' })).toBeEnabled();
  });

  it('dispatches publish with source_revision, repos, and title', async () => {
    const dispatchAction = vi.fn(() => Promise.resolve({ result: 'ok' }));
    const onDispatched = vi.fn();
    render(<PublishModalBody {...props({ dispatchAction, onDispatched })} />);
    fireEvent.change(screen.getByLabelText('PR title'), { target: { value: 'My PR' } });
    fireEvent.click(screen.getByRole('button', { name: 'Publish' }));
    await waitFor(() =>
      expect(dispatchAction).toHaveBeenCalledWith('f', 'publish', {
        source_revision: 'rev-1',
        repos: ['repo-a'],
        title: 'My PR',
      }),
    );
    await waitFor(() => expect(onDispatched).toHaveBeenCalled());
  });

  it('fills title/body from generate', async () => {
    render(<PublishModalBody {...props()} />);
    fireEvent.click(screen.getByRole('button', { name: 'Generate PR narrative' }));
    await waitFor(() =>
      expect((screen.getByLabelText('PR title') as HTMLInputElement).value).toBe('T'),
    );
  });

  it('preselects repositories with undelivered work and publishes without a title', async () => {
    const dispatchAction = vi.fn().mockResolvedValue({ result: 'published' });
    render(
      <PublishModalBody
        featureId="abcd1234ef567890"
        preflight={{
          featureId: 'abcd1234ef567890',
          sourceRevision: 'rev-1',
          canMarkDone: true,
          repos: [
            {
              repo: 'api',
              publishable: true,
              touched: true,
              status: 'unpublished_changes',
              pendingCommits: 3,
              pendingDirty: true,
              pushMode: 'rewrite',
              prUrl: 'https://example/pull/1',
            },
          ],
        }}
        dispatchAction={dispatchAction}
        generatePublishDescription={vi.fn()}
        openExternal={vi.fn()}
        onDispatched={vi.fn()}
      />,
    );

    expect(screen.getByRole('heading', { name: 'Unpublished changes' })).toBeInTheDocument();
    expect(screen.getByText('3 commits · uncommitted changes')).toBeInTheDocument();
    expect(screen.getByText('Force-updates the pull-request branch.')).toBeInTheDocument();
    expect(screen.getByRole('checkbox', { name: 'api' })).toBeChecked();

    // pendingDirty is true with no enumerated files (nil Worktrees or an
    // InspectCleanliness error upstream) — publish still requires explicit
    // confirmation rather than silently sweeping up unknown files.
    expect(
      screen.getByText('Could not list the files this publish would commit.'),
    ).toBeInTheDocument();
    const submit = screen.getByRole('button', { name: 'Publish updates' });
    expect(submit).toBeDisabled();
    await userEvent.click(screen.getByRole('checkbox', { name: 'Commit uncommitted files' }));
    expect(submit).toBeEnabled();
    await userEvent.click(submit);

    expect(dispatchAction).toHaveBeenCalledWith('abcd1234ef567890', 'publish', {
      source_revision: 'rev-1',
      repos: ['api'],
    });
  });

  it('still requires a title when a selected repository has no pull request', async () => {
    render(
      <PublishModalBody
        featureId="abcd1234ef567890"
        preflight={{
          featureId: 'abcd1234ef567890',
          sourceRevision: 'rev-1',
          canMarkDone: true,
          repos: [
            { repo: 'web', publishable: true, touched: true, status: 'eligible' },
            {
              repo: 'api',
              publishable: true,
              touched: true,
              status: 'unpublished_changes',
              pendingCommits: 1,
            },
          ],
        }}
        dispatchAction={vi.fn()}
        generatePublishDescription={vi.fn()}
        openExternal={vi.fn()}
        onDispatched={vi.fn()}
      />,
    );

    expect(screen.getByRole('button', { name: 'Publish' })).toBeDisabled();
    await userEvent.type(screen.getByLabelText('PR title'), 'Ship it');
    expect(screen.getByRole('button', { name: 'Publish' })).toBeEnabled();
  });

  it('lists dirty files and gates publish on the confirmation checkbox', async () => {
    const dispatchAction = vi.fn().mockResolvedValue({ result: 'published' });
    render(
      <PublishModalBody
        featureId="abcd1234ef567890"
        preflight={{
          featureId: 'abcd1234ef567890',
          sourceRevision: 'rev-1',
          canMarkDone: true,
          repos: [
            {
              repo: 'api',
              publishable: true,
              touched: true,
              status: 'unpublished_changes',
              pendingCommits: 3,
              pendingDirty: true,
              pushMode: 'rewrite',
              prUrl: 'https://example/pull/1',
              pendingDirtyFiles: ['src/a.ts', 'src/b.ts'],
              pendingDirtyFileTotal: 2,
            },
          ],
        }}
        dispatchAction={dispatchAction}
        generatePublishDescription={vi.fn()}
        openExternal={vi.fn()}
        onDispatched={vi.fn()}
      />,
    );

    expect(screen.getByText('src/a.ts')).toBeInTheDocument();
    expect(screen.getByText('src/b.ts')).toBeInTheDocument();
    expect(
      screen.getByText('api — 2 uncommitted files will be committed and pushed:'),
    ).toBeInTheDocument();
    expect(screen.queryByText(/more$/)).not.toBeInTheDocument();

    const submit = screen.getByRole('button', { name: 'Publish updates' });
    expect(submit).toBeDisabled();

    await userEvent.click(screen.getByRole('checkbox', { name: 'Commit uncommitted files' }));
    expect(submit).toBeEnabled();

    await userEvent.click(submit);
    expect(dispatchAction).toHaveBeenCalledWith('abcd1234ef567890', 'publish', {
      source_revision: 'rev-1',
      repos: ['api'],
    });
  });

  it('renders no dirty notice and needs no confirmation for a clean repo', () => {
    render(
      <PublishModalBody
        featureId="abcd1234ef567890"
        preflight={{
          featureId: 'abcd1234ef567890',
          sourceRevision: 'rev-1',
          canMarkDone: true,
          repos: [
            {
              repo: 'api',
              publishable: true,
              touched: true,
              status: 'unpublished_changes',
              pendingCommits: 3,
            },
          ],
        }}
        dispatchAction={vi.fn()}
        generatePublishDescription={vi.fn()}
        openExternal={vi.fn()}
        onDispatched={vi.fn()}
      />,
    );

    expect(screen.queryByText('Uncommitted changes')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Publish updates' })).toBeEnabled();
  });

  it('renders a +N more line when the dirty file sample is truncated', () => {
    render(
      <PublishModalBody
        featureId="abcd1234ef567890"
        preflight={{
          featureId: 'abcd1234ef567890',
          sourceRevision: 'rev-1',
          canMarkDone: true,
          repos: [
            {
              repo: 'api',
              publishable: true,
              touched: true,
              status: 'unpublished_changes',
              pendingDirty: true,
              pendingDirtyFiles: ['src/a.ts', 'src/b.ts'],
              pendingDirtyFileTotal: 5,
            },
          ],
        }}
        dispatchAction={vi.fn()}
        generatePublishDescription={vi.fn()}
        openExternal={vi.fn()}
        onDispatched={vi.fn()}
      />,
    );

    expect(screen.getByText('+3 more')).toBeInTheDocument();
  });

  it('shows a fallback notice and still requires confirmation when the preflight could not enumerate', async () => {
    render(
      <PublishModalBody
        featureId="abcd1234ef567890"
        preflight={{
          featureId: 'abcd1234ef567890',
          sourceRevision: 'rev-1',
          canMarkDone: true,
          repos: [
            {
              repo: 'api',
              publishable: true,
              touched: true,
              status: 'unpublished_changes',
              pendingDirty: true,
            },
          ],
        }}
        dispatchAction={vi.fn()}
        generatePublishDescription={vi.fn()}
        openExternal={vi.fn()}
        onDispatched={vi.fn()}
      />,
    );

    expect(
      screen.getByText('Could not list the files this publish would commit.'),
    ).toBeInTheDocument();
    const submit = screen.getByRole('button', { name: 'Publish updates' });
    expect(submit).toBeDisabled();
    await userEvent.click(screen.getByRole('checkbox', { name: 'Commit uncommitted files' }));
    expect(submit).toBeEnabled();
  });

  it('invalidates the confirmation when the dirty selection changes', async () => {
    const dispatchAction = vi.fn().mockResolvedValue({ result: 'published' });
    render(
      <PublishModalBody
        featureId="abcd1234ef567890"
        preflight={{
          featureId: 'abcd1234ef567890',
          sourceRevision: 'rev-1',
          canMarkDone: true,
          repos: [
            {
              repo: 'api',
              publishable: true,
              touched: true,
              status: 'unpublished_changes',
              pendingDirty: true,
              pendingDirtyFiles: ['a.ts'],
              pendingDirtyFileTotal: 1,
            },
            {
              repo: 'core',
              publishable: true,
              touched: true,
              status: 'unpublished_changes',
              pendingDirty: true,
              pendingDirtyFiles: ['b.ts', 'c.ts', 'd.ts'],
              pendingDirtyFileTotal: 3,
            },
          ],
        }}
        dispatchAction={dispatchAction}
        generatePublishDescription={vi.fn()}
        openExternal={vi.fn()}
        onDispatched={vi.fn()}
      />,
    );

    const submit = screen.getByRole('button', { name: 'Publish updates' });
    const confirm = screen.getByRole('checkbox', { name: 'Commit uncommitted files' });
    const coreCheckbox = screen.getByRole('checkbox', { name: 'core' });

    // Both dirty repos start selected. Deselect one, so only "api" (1 file)
    // remains dirty-selected.
    await userEvent.click(coreCheckbox);
    expect(submit).toBeDisabled();

    // Confirm against that smaller set.
    await userEvent.click(confirm);
    expect(submit).toBeEnabled();

    // Re-select "core" (3 files) — the confirmed set was only ever "api",
    // so the tick must not carry over to authorize the larger set.
    await userEvent.click(coreCheckbox);
    expect(submit).toBeDisabled();
  });
});
