import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { featureSnapshot, installAgenticoMock } from '../../test/agenticoMock';
import { ReviewCommentsModal } from './ReviewCommentsModal';

afterEach(cleanup);

function props(repos = ['repo-a']) {
  return {
    featureId: 'abcd1234ef567890',
    snapshot: featureSnapshot({
      status: 'Published',
      repos,
      actions: [
        {
          id: 'review-comments',
          enabled: true,
          disabledReasons: [],
          inputs: [{ name: 'mode', options: ['auto', 'address_all'] }],
        },
      ],
    }),
    onCancel: vi.fn(),
    onDispatched: vi.fn(),
  };
}

describe('ReviewCommentsModal', () => {
  it('auto-fetches a single repository and renders feedback metadata and type badges', async () => {
    const mock = installAgenticoMock();
    mock.api.fetchReviewComments.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      repo: 'repo-a',
      comments: [
        {
          id: 10,
          type: 'issue',
          author: 'octocat',
          file: 'desktop/src/main/features.ts',
          line: 312,
          body: 'Keep the conversation feedback in scope.',
        },
      ],
    });
    render(<ReviewCommentsModal {...props()} />);

    expect(mock.api.fetchReviewComments).toHaveBeenCalledWith({
      featureId: 'abcd1234ef567890',
      repo: 'repo-a',
    });
    expect(await screen.findByText('Conversation')).toBeVisible();
    expect(screen.getByText('octocat')).toBeVisible();
    expect(screen.getByText('desktop/src/main/features.ts:312')).toBeVisible();
    expect(screen.queryByRole('combobox', { name: 'Repository' })).not.toBeInTheDocument();
  });

  it('fetches only after a repository is selected when there are multiple repositories', async () => {
    const mock = installAgenticoMock();
    mock.api.fetchReviewComments.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      repo: 'repo-b',
      comments: [],
    });
    render(<ReviewCommentsModal {...props(['repo-a', 'repo-b'])} />);

    expect(mock.api.fetchReviewComments).not.toHaveBeenCalled();
    fireEvent.change(screen.getByRole('combobox', { name: 'Repository' }), {
      target: { value: 'repo-b' },
    });
    await waitFor(() =>
      expect(mock.api.fetchReviewComments).toHaveBeenCalledWith({
        featureId: 'abcd1234ef567890',
        repo: 'repo-b',
      }),
    );
  });

  it('shows an all-clear success state without an alert when no comments remain', async () => {
    const mock = installAgenticoMock();
    mock.api.fetchReviewComments.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      repo: 'repo-a',
      comments: [],
    });
    const modalProps = props();
    render(<ReviewCommentsModal {...modalProps} />);

    expect(await screen.findByRole('heading', { name: 'All comments addressed' })).toBeVisible();
    expect(screen.getByText('Nothing to do for repo-a.')).toBeVisible();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(modalProps.onCancel).toHaveBeenCalled();
  });

  it('starts the selected repository and action-published mode', async () => {
    const mock = installAgenticoMock();
    mock.api.fetchReviewComments.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      repo: 'repo-a',
      comments: [{ id: 11, type: 'review', body: 'Inline feedback.' }],
    });
    mock.api.startReviewComments.mockResolvedValue({
      featureId: 'abcd1234ef567890',
      cycleType: 'review-comments',
      result: 'started',
    });
    const modalProps = props();
    render(<ReviewCommentsModal {...modalProps} />);

    await screen.findByRole('button', { name: 'Start review comments (1)' });
    fireEvent.change(screen.getByRole('combobox', { name: 'Mode' }), {
      target: { value: 'address_all' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Start review comments (1)' }));
    await waitFor(() =>
      expect(mock.api.startReviewComments).toHaveBeenCalledWith({
        featureId: 'abcd1234ef567890',
        repo: 'repo-a',
        mode: 'address_all',
      }),
    );
    expect(modalProps.onDispatched).toHaveBeenCalled();
    expect(modalProps.onCancel).toHaveBeenCalled();
  });
});
