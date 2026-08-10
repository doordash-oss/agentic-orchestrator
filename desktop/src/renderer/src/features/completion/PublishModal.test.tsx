import { fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import type { CompletionPreflightResult, FeatureActionResult } from '../../../../shared/ipc';
import { PublishModal } from './PublishModal';

const featureId = 'abcd1234ef567890';
const result: FeatureActionResult = {
  featureId,
  action: 'publish',
  result: 'published',
  sessionIds: [],
};

const newPrPreflight: CompletionPreflightResult = {
  featureId,
  sourceRevision: 'rev-1',
  canMarkDone: true,
  repos: [{ repo: 'web', publishable: true, touched: true, status: 'eligible' }],
};

function props(over: Partial<React.ComponentProps<typeof PublishModal>> = {}) {
  return {
    featureId,
    preflight: newPrPreflight,
    dispatchAction: vi.fn().mockResolvedValue(result),
    generatePublishDescription: vi
      .fn()
      .mockResolvedValue({ featureId, title: 'Title', body: 'Body' }),
    openExternal: vi.fn().mockResolvedValue({ ok: true }),
    onDispatched: vi.fn(),
    onClose: vi.fn(),
    ...over,
  };
}

describe('PublishModal', () => {
  it('keeps an existing pull-request update focused on work users can control', () => {
    render(
      <PublishModal
        {...props({
          preflight: {
            featureId,
            sourceRevision: 'rev-1',
            canMarkDone: true,
            repos: [
              {
                repo: 'api',
                publishable: true,
                touched: true,
                status: 'unpublished_changes',
                pendingCommits: 1,
                pushMode: 'rewrite',
              },
            ],
          },
        })}
      />,
    );

    expect(screen.getByRole('dialog', { name: 'Publish reviewed changes' })).toBeVisible();
    expect(screen.queryByLabelText('PR title')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('PR body')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Generate PR narrative' })).not.toBeInTheDocument();
    expect(screen.getByText('Rewrites the pull-request branch with a safety lease.')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Publish updates' })).toBeEnabled();
  });

  it('uses the sheet family and a cancel-first footer', () => {
    render(<PublishModal {...props({ preflight: { ...newPrPreflight, repos: [] } })} />);

    const dialog = screen.getByRole('dialog', { name: 'Publish reviewed changes' });
    expect(dialog).toHaveClass('sheet', 'completion-publish-sheet');
    const footer = dialog.querySelector('.sheet__footer');
    expect(
      within(footer as HTMLElement)
        .getAllByRole('button')
        .map((button) => button.textContent),
    ).toEqual(['Cancel', 'Publish updates']);
  });

  it('dispatches the typed request for an existing pull request', async () => {
    const user = userEvent.setup();
    const dispatchAction = vi.fn().mockResolvedValue(result);
    render(
      <PublishModal
        {...props({
          dispatchAction,
          preflight: {
            featureId,
            sourceRevision: 'rev-1',
            canMarkDone: true,
            repos: [
              {
                repo: 'api',
                publishable: true,
                touched: true,
                status: 'unpublished_changes',
                pendingCommits: 1,
              },
            ],
          },
        })}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Publish updates' }));
    expect(dispatchAction).toHaveBeenCalledWith({
      featureId,
      action: 'publish',
      body: { source_revision: 'rev-1', repos: ['api'] },
    });
  });

  it('shows required and optional PR fields only while a new PR is selected', async () => {
    const user = userEvent.setup();
    render(
      <PublishModal
        {...props({
          preflight: {
            featureId,
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
          },
        })}
      />,
    );

    expect(screen.getByText('Required')).toBeVisible();
    expect(screen.getByText('Optional')).toBeVisible();
    await user.click(screen.getByRole('checkbox', { name: 'web' }));
    expect(screen.queryByLabelText('PR title')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('PR body')).not.toBeInTheDocument();
  });

  it('requires confirmation before committing selected dirty files', async () => {
    const user = userEvent.setup();
    render(
      <PublishModal
        {...props({
          preflight: {
            featureId,
            sourceRevision: 'rev-1',
            canMarkDone: true,
            repos: [
              {
                repo: 'api',
                publishable: true,
                touched: true,
                status: 'unpublished_changes',
                pendingDirty: true,
                pendingDirtyFiles: ['src/a.ts'],
                pendingDirtyFileTotal: 1,
              },
            ],
          },
        })}
      />,
    );

    const publish = screen.getByRole('button', { name: 'Publish updates' });
    expect(publish).toBeDisabled();
    await user.click(screen.getByRole('checkbox', { name: 'Commit uncommitted files' }));
    expect(publish).toBeEnabled();
  });

  it('renders concise branch-divergence recovery without a raw push command', async () => {
    const user = userEvent.setup();
    const dispatchAction = vi
      .fn()
      .mockRejectedValue(
        new Error(
          'publish_remote_diverged: pull-request branch contains remote work that is not in this workspace Review and reconcile the pull-request branch on GitHub, then refresh and retry.',
        ),
      );
    render(
      <PublishModal
        {...props({
          dispatchAction,
          preflight: {
            featureId,
            sourceRevision: 'rev-1',
            canMarkDone: true,
            repos: [
              {
                repo: 'api',
                publishable: true,
                touched: true,
                status: 'unpublished_changes',
                pendingCommits: 1,
              },
            ],
          },
        })}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Publish updates' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(
      "The pull-request branch contains changes that aren't in this workspace.",
    );
    expect(screen.getByRole('alert')).not.toHaveTextContent('git push');
  });

  it('closes through Escape, the scrim, and Cancel while idle', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(<PublishModal {...props({ onClose, preflight: { ...newPrPreflight, repos: [] } })} />);

    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    fireEvent.mouseDown(screen.getByRole('dialog').parentElement as HTMLElement);
    fireEvent.keyDown(window, { key: 'Escape' });
    expect(onClose).toHaveBeenCalledTimes(3);
  });

  it('does not close through Cancel, scrim, or Escape while publishing', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    const dispatchAction = vi.fn(() => new Promise<FeatureActionResult>(() => undefined));
    render(
      <PublishModal
        {...props({
          onClose,
          dispatchAction,
          preflight: {
            featureId,
            sourceRevision: 'rev-1',
            canMarkDone: true,
            repos: [
              {
                repo: 'api',
                publishable: true,
                touched: true,
                status: 'unpublished_changes',
                pendingCommits: 1,
              },
            ],
          },
        })}
      />,
    );

    await user.click(screen.getByRole('button', { name: 'Publish updates' }));
    expect(screen.getByRole('button', { name: 'Publishing…' })).toBeDisabled();
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    fireEvent.mouseDown(screen.getByRole('dialog').parentElement as HTMLElement);
    fireEvent.keyDown(window, { key: 'Escape' });
    expect(onClose).not.toHaveBeenCalled();
  });
});
