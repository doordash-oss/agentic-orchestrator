import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  featureConfigSnapshot,
  featureSnapshot,
  installAgenticoMock,
  type AgenticoMock,
} from '../../test/agenticoMock';
import { ReviewFeedbackLauncher } from './ReviewFeedbackLauncher';
import type { FetchReviewFeedbackResult } from '../../../../shared/ipc';

afterEach(cleanup);

const PARENT_ID = 'abcd1234ef567890';

function fetchResult(): FetchReviewFeedbackResult {
  return {
    featureId: PARENT_ID,
    repos: [
      {
        repo: 'repo-a',
        prUrl: 'https://github.com/org/repo-a/pull/1',
        comments: [
          {
            repo: 'repo-a',
            id: 41,
            type: 'review',
            path: 'src/query.ts',
            line: 12,
            author: 'octocat',
            body: 'Rewrite this to avoid the bearer token.',
            inReplyToId: 39,
          },
          { repo: 'repo-a', id: 42, type: 'issue', body: 'Consider extracting the parser.' },
        ],
      },
      {
        repo: 'repo-b',
        prUrl: 'https://github.com/org/repo-b/pull/7',
        comments: [{ repo: 'repo-b', id: 90, type: 'review_body', author: 'reviewer' }],
      },
    ],
  };
}

async function renderLauncher({
  mock = installAgenticoMock(),
  snapshot = featureSnapshot({ repos: ['repo-a', 'repo-b'] }),
  onCancel = vi.fn(),
  onDispatched = vi.fn(),
  roadmapReview = true,
}: {
  mock?: AgenticoMock;
  snapshot?: ReturnType<typeof featureSnapshot>;
  onCancel?: ReturnType<typeof vi.fn>;
  onDispatched?: ReturnType<typeof vi.fn>;
  roadmapReview?: boolean;
} = {}) {
  mock.api.getFeatureConfig.mockResolvedValue(
    featureConfigSnapshot({
      current: {
        checkpoints: {
          inquiryReview: false,
          researchReview: false,
          designReview: false,
          roadmapReview,
          phasePlanReview: true,
          manualPublish: false,
          draftPublish: false,
        },
      },
    }),
  );
  mock.api.fetchReviewFeedback.mockResolvedValue(fetchResult());
  render(
    <ReviewFeedbackLauncher
      featureId={PARENT_ID}
      snapshot={snapshot}
      onCancel={onCancel}
      onDispatched={onDispatched}
    />,
  );
  await waitFor(() => expect(mock.api.fetchReviewFeedback).toHaveBeenCalledOnce());
  return { mock, onCancel, onDispatched, user: userEvent.setup() };
}

describe('ReviewFeedbackLauncher', () => {
  it('fetches on open and renders comments grouped by repo with every checkbox pre-selected', async () => {
    const { mock } = await renderLauncher();
    expect(mock.api.fetchReviewFeedback).toHaveBeenCalledWith({ featureId: PARENT_ID });
    expect(screen.getByText('repo-a')).toBeVisible();
    expect(screen.getByText('repo-b')).toBeVisible();
    const prLinks = screen.getAllByRole('link', { name: 'View pull request' });
    expect(prLinks[0]?.getAttribute('href')).toBe('https://github.com/org/repo-a/pull/1');
    const checkboxes = screen.getAllByRole('checkbox');
    // Three comment checkboxes (one per comment) plus the gate toggle.
    expect(checkboxes).toHaveLength(4);
    for (const box of checkboxes.slice(0, 3)) expect(box).toBeChecked();
  });

  it('seeds the gate toggle from the parent Roadmap-review setting', async () => {
    await renderLauncher({ roadmapReview: false });
    expect(
      screen.getByRole('checkbox', { name: /Pause for Roadmap and Phase plan review/ }),
    ).not.toBeChecked();
  });

  it('leaves the confirm control inert while zero comments are selected', async () => {
    const { mock, user } = await renderLauncher();
    // Deselect every comment via the per-repo "Clear repo" controls.
    const clearButtons = screen.getAllByRole('button', { name: 'Clear repo' });
    for (const button of clearButtons) await user.click(button);
    const launch = screen.getByRole('button', { name: 'Select comments to launch' });
    expect(launch).toBeDisabled();
    await user.click(launch);
    expect(mock.api.launchReviewFeedbackChild).not.toHaveBeenCalled();
  });

  it('renders an explicit all-clear state when there are no unaddressed comments', async () => {
    const mock = installAgenticoMock();
    mock.api.getFeatureConfig.mockResolvedValue(featureConfigSnapshot({}));
    mock.api.fetchReviewFeedback.mockResolvedValue({ featureId: PARENT_ID, repos: [] });
    render(
      <ReviewFeedbackLauncher
        featureId={PARENT_ID}
        snapshot={featureSnapshot()}
        onCancel={vi.fn()}
        onDispatched={vi.fn()}
      />,
    );
    expect(await screen.findByText(/No unaddressed comments/)).toBeVisible();
  });

  it('renders the fetch error naming the failing repo and refetches on retry', async () => {
    const mock = installAgenticoMock();
    mock.api.getFeatureConfig.mockResolvedValue(featureConfigSnapshot({}));
    mock.api.fetchReviewFeedback
      .mockRejectedValueOnce(
        new Error('review_feedback_fetch_failed: repo "web" could not be reached'),
      )
      .mockResolvedValueOnce(fetchResult());
    render(
      <ReviewFeedbackLauncher
        featureId={PARENT_ID}
        snapshot={featureSnapshot()}
        onCancel={vi.fn()}
        onDispatched={vi.fn()}
      />,
    );
    expect(await screen.findByText(/could not be reached/)).toBeVisible();
    await userEvent.click(screen.getByRole('button', { name: 'Try again' }));
    await waitFor(() => expect(mock.api.fetchReviewFeedback).toHaveBeenCalledTimes(2));
    expect(await screen.findByText('repo-a')).toBeVisible();
  });

  it('dispatches launch with exactly the selected payloads and the gate, then reports the child and closes', async () => {
    const { mock, onDispatched, onCancel, user } = await renderLauncher({ roadmapReview: true });
    mock.api.launchReviewFeedbackChild.mockResolvedValue({
      childId: 'child1234ef567890',
      parentId: PARENT_ID,
      result: 'created',
    });
    // Deselect one comment so the launch carries exactly the kept selection.
    const comment42 = screen.getByLabelText(/Consider extracting the parser/);
    await user.click(comment42);
    expect(comment42).not.toBeChecked();

    await user.click(screen.getByRole('button', { name: /^Launch child/ }));
    await waitFor(() => expect(onDispatched).toHaveBeenCalledOnce());
    expect(onDispatched).toHaveBeenCalledWith({
      childId: 'child1234ef567890',
      autoStart: true,
    });
    expect(mock.api.launchReviewFeedbackChild).toHaveBeenCalledWith({
      parentId: PARENT_ID,
      comments: [
        {
          repo: 'repo-a',
          id: 41,
          type: 'review',
          path: 'src/query.ts',
          line: 12,
          author: 'octocat',
          body: 'Rewrite this to avoid the bearer token.',
          inReplyToId: 39,
        },
        { repo: 'repo-b', id: 90, type: 'review_body', author: 'reviewer' },
      ],
      gate: true,
    });
    expect(onCancel).toHaveBeenCalledOnce();
  });

  it('sends the gate value as false when the toggle is unchecked', async () => {
    const { mock, user } = await renderLauncher({ roadmapReview: true });
    mock.api.launchReviewFeedbackChild.mockResolvedValue({
      childId: 'child1234ef567890',
      parentId: PARENT_ID,
      result: 'created',
    });
    await user.click(
      screen.getByRole('checkbox', { name: /Pause for Roadmap and Phase plan review/ }),
    );
    await user.click(screen.getByRole('button', { name: /^Launch child/ }));
    await waitFor(() => expect(mock.api.launchReviewFeedbackChild).toHaveBeenCalledOnce());
    expect(mock.api.launchReviewFeedbackChild).toHaveBeenCalledWith(
      expect.objectContaining({ gate: false }),
    );
  });
});
