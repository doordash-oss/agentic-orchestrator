import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  featureConfigSnapshot,
  featureSnapshot,
  installAgenticoMock,
  type AgenticoMock,
} from '../../test/agenticoMock';
import { ReviewFeedbackWorkspace } from './ReviewFeedbackWorkspace';
import type { ReviewFeedbackDraftView } from './reviewFeedbackDraftApi';

afterEach(cleanup);

const PARENT_ID = 'abcd1234ef567890';

function draftView(overrides: Partial<ReviewFeedbackDraftView> = {}): ReviewFeedbackDraftView {
  return {
    revision: 7,
    snapshotId: 'snap-1',
    repos: [
      {
        repo: 'repo-a',
        prUrl: 'https://github.com/org/repo-a/pull/1',
        comments: [
          {
            stableRef: 'repo-a:review:41',
            selected: true,
            repo: 'repo-a',
            id: 41,
            type: 'review',
            path: 'src/query.ts',
            line: 12,
            author: 'octocat',
            body: 'Rewrite this to avoid the bearer token.',
            inReplyToId: 39,
            createdAt: '2026-07-20T10:00:00Z',
          },
          {
            stableRef: 'repo-a:issue:42',
            selected: true,
            repo: 'repo-a',
            id: 42,
            type: 'issue',
            body: 'Consider extracting the parser.',
          },
        ],
      },
      {
        repo: 'repo-b',
        prUrl: 'https://github.com/org/repo-b/pull/7',
        comments: [
          {
            stableRef: 'repo-b:review_body:90',
            selected: false,
            repo: 'repo-b',
            id: 90,
            type: 'review_body',
            author: 'reviewer',
          },
        ],
      },
    ],
    ...overrides,
  };
}

function deferred<T>(): {
  promise: Promise<T>;
  resolve(value: T): void;
  reject(err: unknown): void;
} {
  let resolve!: (value: T) => void;
  let reject!: (err: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

async function renderWorkspace({
  mock = installAgenticoMock(),
  draft = draftView(),
  onBack = vi.fn(),
  onDispatched = vi.fn(),
}: {
  mock?: AgenticoMock;
  draft?: ReviewFeedbackDraftView;
  onBack?: ReturnType<typeof vi.fn>;
  onDispatched?: ReturnType<typeof vi.fn>;
} = {}) {
  mock.api.getFeatureConfig.mockResolvedValue(featureConfigSnapshot({}));
  mock.api.fetchReviewFeedback.mockResolvedValue(draft);
  render(
    <ReviewFeedbackWorkspace
      featureId={PARENT_ID}
      snapshot={featureSnapshot({ repos: ['repo-a', 'repo-b'] })}
      onBack={onBack}
      onDispatched={onDispatched}
    />,
  );
  await waitFor(() => expect(mock.api.fetchReviewFeedback).toHaveBeenCalledOnce());
  await screen.findByText(/Rewrite this to avoid the bearer token/);
  return { mock, onBack, onDispatched, user: userEvent.setup() };
}

describe('ReviewFeedbackWorkspace', () => {
  it('renders the scope rail with counts and the feed cards in server order', async () => {
    const { mock } = await renderWorkspace();
    expect(mock.api.fetchReviewFeedback).toHaveBeenCalledWith({ featureId: PARENT_ID });
    const dialog = screen.getByRole('dialog', { name: 'Address review feedback' });
    expect(dialog).toBeVisible();
    // Header ledger counts the committed draft selection (repo-b is deselected).
    expect(screen.getByText('2 of 3 selected')).toBeVisible();
    // Scope rail: All feedback plus one entry per repo in parent order.
    const rail = screen.getByRole('navigation', { name: 'Feedback scope' });
    const scopeNames = Array.from(
      rail.querySelectorAll('.review-feedback-workspace__scope-name'),
    ).map((node) => node.textContent);
    expect(scopeNames).toEqual(['All feedback', 'repo-a', 'repo-b']);
    const scopeCounts = Array.from(
      rail.querySelectorAll('.review-feedback-workspace__scope-count'),
    ).map((node) => node.textContent);
    expect(scopeCounts).toEqual(['2/3', '2/2', '0/1']);
    // Cards: repo, author, type label, path:line, humanized creation time.
    expect(screen.getAllByText('repo-a').length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText('octocat')).toBeVisible();
    expect(screen.getByText('Review comment')).toBeVisible();
    expect(screen.getByText('src/query.ts:12')).toBeVisible();
    expect(screen.getByText(/\d+ days ago/)).toBeVisible();
    const comment42 = screen.getByLabelText(/Consider extracting the parser/);
    expect(comment42).toBeChecked();
    expect(screen.getByLabelText(/Review body/)).not.toBeChecked();
  });

  it('toggles update the visible choice immediately and send a reference-only mutation', async () => {
    const { mock, user } = await renderWorkspace();
    // The ack carries the authoritative view with the toggle committed.
    const ack = draftView({ revision: 8 });
    ack.repos[0]!.comments[1] = { ...ack.repos[0]!.comments[1]!, selected: false };
    mock.api.updateReviewFeedbackSelection.mockResolvedValue({ revision: 8, repos: ack.repos });
    const comment42 = screen.getByLabelText(/Consider extracting the parser/);
    await user.click(comment42);
    // Optimistic: unchecked; the ack keeps it that way (committed server-side).
    expect(comment42).not.toBeChecked();
    await waitFor(() => expect(mock.api.updateReviewFeedbackSelection).toHaveBeenCalledOnce());
    const request = mock.api.updateReviewFeedbackSelection.mock.calls[0]![0] as {
      featureId: string;
      expectedRevision: number;
      updates: unknown[];
    };
    expect(request.featureId).toBe(PARENT_ID);
    expect(request.expectedRevision).toBe(7);
    // Reference-only: no comment content is ever sent.
    expect(request.updates).toEqual([{ stableRef: 'repo-a:issue:42', selected: false }]);
  });

  it('serializes rapid edits: each request uses the revision from the previous ack', async () => {
    const { mock, user } = await renderWorkspace();
    const first = deferred<{ revision: number; repos: ReviewFeedbackDraftView['repos'] }>();
    mock.api.updateReviewFeedbackSelection
      .mockReturnValueOnce(first.promise)
      .mockResolvedValueOnce({ revision: 9, repos: draftView({ revision: 9 }).repos });
    await user.click(screen.getByLabelText(/Rewrite this to avoid the bearer token/));
    await waitFor(() => expect(mock.api.updateReviewFeedbackSelection).toHaveBeenCalledOnce());
    await user.click(screen.getByLabelText(/Consider extracting the parser/));
    // The second edit waits for the first acknowledgement.
    expect(mock.api.updateReviewFeedbackSelection).toHaveBeenCalledOnce();
    first.resolve({ revision: 8, repos: draftView({ revision: 8 }).repos });
    await waitFor(() => expect(mock.api.updateReviewFeedbackSelection).toHaveBeenCalledTimes(2));
    expect(mock.api.updateReviewFeedbackSelection.mock.calls[1]![0]).toEqual({
      featureId: PARENT_ID,
      expectedRevision: 8,
      updates: [{ stableRef: 'repo-a:issue:42', selected: false }],
    });
  });

  it('refetches and adopts the server view on a revision conflict', async () => {
    const { mock, user } = await renderWorkspace();
    const conflict = Object.assign(new Error('stale'), {
      code: 'review_feedback_revision_conflict',
    });
    mock.api.updateReviewFeedbackSelection.mockRejectedValueOnce(conflict);
    const comment90 = screen.getByLabelText(/Review body/);
    await user.click(comment90);
    // The conflict triggers a refetch.
    await waitFor(() => expect(mock.api.fetchReviewFeedback).toHaveBeenCalledTimes(2));
    // The optimistic toggle is rolled back to the committed server value.
    expect(comment90).not.toBeChecked();
    expect(await screen.findByRole('alert')).toHaveTextContent(/stale/);
  });

  it('disables launch while a selection save is pending and re-enables after the ack', async () => {
    const { mock, user } = await renderWorkspace();
    const pending = deferred<{ revision: number; repos: ReviewFeedbackDraftView['repos'] }>();
    mock.api.updateReviewFeedbackSelection.mockReturnValueOnce(pending.promise);
    mock.api.launchReviewFeedbackChild.mockResolvedValue({
      featureId: 'child1234ef567890',
      parentId: PARENT_ID,
    });
    await user.click(screen.getByLabelText(/Consider extracting the parser/));
    const launch = screen.getByRole('button', { name: /^Launch child/ });
    expect(launch).toBeDisabled();
    pending.resolve({ revision: 8, repos: draftView({ revision: 8 }).repos });
    await waitFor(() => expect(launch).toBeEnabled());
  });

  it('waits for a pending save before leaving when Back is pressed', async () => {
    const { mock, onBack, user } = await renderWorkspace();
    const pending = deferred<{ revision: number; repos: ReviewFeedbackDraftView['repos'] }>();
    mock.api.updateReviewFeedbackSelection.mockReturnValueOnce(pending.promise);
    await user.click(screen.getByLabelText(/Consider extracting the parser/));
    await user.click(screen.getByRole('button', { name: 'Back' }));
    expect(onBack).not.toHaveBeenCalled();
    pending.resolve({ revision: 8, repos: draftView({ revision: 8 }).repos });
    await waitFor(() => expect(onBack).toHaveBeenCalledOnce());
  });

  it('restores the committed selection on re-entry', async () => {
    const mock = installAgenticoMock();
    mock.api.getFeatureConfig.mockResolvedValue(featureConfigSnapshot({}));
    const committed = draftView({ revision: 8 });
    committed.repos[0]!.comments[1] = {
      ...committed.repos[0]!.comments[1]!,
      selected: false,
    };
    mock.api.fetchReviewFeedback.mockResolvedValue(committed);
    render(
      <ReviewFeedbackWorkspace
        featureId={PARENT_ID}
        snapshot={featureSnapshot({ repos: ['repo-a', 'repo-b'] })}
        onBack={vi.fn()}
        onDispatched={vi.fn()}
      />,
    );
    const comment42 = await screen.findByLabelText(/Consider extracting the parser/);
    expect(comment42).not.toBeChecked();
    expect(screen.getByText('1 of 3 selected')).toBeVisible();
  });

  it('launches with only the expected revision and gate, then reports the child and receipt', async () => {
    const { mock, onBack, onDispatched, user } = await renderWorkspace();
    mock.api.launchReviewFeedbackChild.mockResolvedValue({
      featureId: 'child1234ef567890',
      parentId: PARENT_ID,
      result: 'created',
      changed: 2,
      omitted: 1,
      deferred: 3,
    });
    await user.click(screen.getByRole('button', { name: /Launch child \(2\)/ }));
    await waitFor(() => expect(mock.api.launchReviewFeedbackChild).toHaveBeenCalledOnce());
    expect(mock.api.launchReviewFeedbackChild).toHaveBeenCalledWith({
      parentId: PARENT_ID,
      expectedRevision: 7,
      gate: true,
    });
    await waitFor(() =>
      expect(onDispatched).toHaveBeenCalledWith({
        childId: 'child1234ef567890',
        receipt: '2 changed, 1 omitted, 3 deferred since review',
      }),
    );
    expect(onBack).toHaveBeenCalledOnce();
  });

  it('surfaces a zero-launchable-selection rejection without discarding the draft', async () => {
    const { mock, user } = await renderWorkspace();
    mock.api.launchReviewFeedbackChild.mockRejectedValue(
      Object.assign(new Error('review_feedback_zero_launchable_selection: nothing launchable'), {
        code: 'review_feedback_zero_launchable_selection',
      }),
    );
    await user.click(screen.getByRole('button', { name: /Launch child \(2\)/ }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/nothing launchable/);
    // The workspace stays open with the draft view intact.
    expect(screen.getByLabelText(/Consider extracting the parser/)).toBeChecked();
  });
});
