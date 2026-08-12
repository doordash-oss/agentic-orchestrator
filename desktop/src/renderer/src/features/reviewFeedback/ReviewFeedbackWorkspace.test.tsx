import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  featureConfigSnapshot,
  featureSnapshot,
  installAgenticoMock,
  type AgenticoMock,
} from '../../test/agenticoMock';
import { matchMediaState } from '../../test/setup';
import { ReviewFeedbackWorkspace } from './ReviewFeedbackWorkspace';
import type {
  ReviewFeedbackDraftCommentView,
  ReviewFeedbackDraftView,
} from './reviewFeedbackDraftApi';

afterEach(cleanup);

beforeEach(() => {
  matchMediaState.narrowCockpit = false;
});

const PARENT_ID = 'abcd1234ef567890';

function comment(
  overrides: Partial<ReviewFeedbackDraftCommentView> &
    Pick<ReviewFeedbackDraftCommentView, 'stableRef' | 'repo' | 'id' | 'type'>,
): ReviewFeedbackDraftCommentView {
  return { selected: true, ...overrides };
}

function draftView(overrides: Partial<ReviewFeedbackDraftView> = {}): ReviewFeedbackDraftView {
  return {
    revision: 7,
    snapshotId: 'snap-1',
    repos: [
      {
        repo: 'repo-a',
        prUrl: 'https://github.com/org/repo-a/pull/1',
        comments: [
          comment({
            stableRef: 'repo-a:review:41',
            repo: 'repo-a',
            id: 41,
            type: 'review',
            path: 'src/query.ts',
            line: 12,
            author: 'Octocat',
            body: [
              '# Security fix',
              '',
              'Rewrite this to avoid the bearer token.',
              '',
              '- [ ] follow up on caching',
              '- [x] confirm token rotation',
              '',
              '```go',
              'return newQueryCodec()',
              '```',
              '',
              'See [hardening guide](https://docs.example.com/harden), never',
              '[this mirror](https://user:pass@mirror.example.com/x). Inline',
              'HTML like <script> stays inert.',
            ].join('\n'),
            diffHunk: '@@ -1 +1,2 @@\n-oldCodec()\n+newQueryCodec()',
            inReplyToId: 39,
            createdAt: '2026-07-20T10:00:00Z',
          }),
          comment({
            stableRef: 'repo-a:issue:42',
            repo: 'repo-a',
            id: 42,
            type: 'issue',
            author: 'hubot',
            body: [
              'Consider extracting the parser.',
              '',
              '| path | note |',
              '| --- | --- |',
              '| a.go | hot |',
              '',
              '- keep the visible instruction',
              '',
              '![Screen Recording](https://attachments.example.com/icon.svg)',
              '![failing screenshot](https://attachments.example.com/shot.png)',
            ].join('\n'),
            diffHunk: '@@ -5,2 +5,3 @@\n keep\n-drop\n+add',
          }),
        ],
      },
      {
        repo: 'repo-b',
        prUrl: 'https://github.com/org/repo-b/pull/7',
        comments: [
          comment({
            stableRef: 'repo-b:review_body:90',
            repo: 'repo-b',
            id: 90,
            type: 'review_body',
            author: 'Reviewer',
            body: 'Overall looks good.',
            selected: false,
          }),
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
  await screen.findAllByText(/of \d+ comments visible/);
  return { mock, onBack, onDispatched, user: userEvent.setup() };
}

function ackWith(revision: number, draft = draftView({ revision })) {
  return { revision, repos: draft.repos };
}

/** The visible-result summary rendered above the feed. */
function summary(text: string | RegExp): HTMLElement {
  return within(screen.getByRole('main', { name: 'Review feedback' })).getByText(text, {
    selector: '.review-feedback-feedbar__summary',
  });
}

describe('true-empty workspace', () => {
  it('explains the state and offers Refresh feedback plus Back as the directed actions', async () => {
    const mock = installAgenticoMock();
    mock.api.getFeatureConfig.mockResolvedValue(featureConfigSnapshot({}));
    mock.api.fetchReviewFeedback.mockResolvedValue(draftView({ repos: [] }));
    const onBack = vi.fn();
    const onDispatched = vi.fn();
    render(
      <ReviewFeedbackWorkspace
        featureId={PARENT_ID}
        snapshot={featureSnapshot({ repos: ['repo-a', 'repo-b'] })}
        onBack={onBack}
        onDispatched={onDispatched}
      />,
    );
    const user = userEvent.setup();

    await waitFor(() =>
      expect(
        screen
          .getAllByRole('status')
          .some((el) => /No unaddressed comments/.test(el.textContent ?? '')),
      ).toBe(true),
    );
    // No launch control exists without comments, and Refresh feedback is the
    // visible recovery action wired to the reload path.
    expect(screen.queryByRole('button', { name: /Launch/ })).not.toBeInTheDocument();
    const refresh = screen.getByRole('button', { name: 'Refresh feedback' });
    expect(refresh).toBeEnabled();
    await user.click(refresh);
    await waitFor(() => expect(mock.api.fetchReviewFeedback).toHaveBeenCalledTimes(2));

    // Back remains the secondary exit: both the header and the empty-state
    // control leave the workspace.
    for (const back of screen.getAllByRole('button', { name: 'Back' })) {
      expect(back).toBeEnabled();
      await user.click(back);
    }
    expect(onBack).toHaveBeenCalledTimes(2);
  });
});

describe('rich review feedback cards', () => {
  /** Expand one card's collapsible content if its control exists. */
  async function expandAll(user: ReturnType<typeof userEvent.setup>) {
    for (const control of screen.queryAllByRole('button', { name: 'Show full feedback' })) {
      await user.click(control);
    }
  }

  it('renders approved GFM as a bounded rich body without page-level headings', async () => {
    const { mock, user } = await renderWorkspace();
    await expandAll(user);
    const cards = screen.getAllByRole('article');
    expect(
      within(cards[0]!).getByRole('heading', { level: 4, name: 'Security fix' }),
    ).toBeVisible();
    expect(
      within(cards[0]!).getByRole('checkbox', { name: 'Task completed (read-only)' }),
    ).toBeDisabled();
    expect(within(cards[1]!).getByRole('table')).toBeInTheDocument();
    expect(within(cards[1]!).getByText('keep the visible instruction')).toBeInTheDocument();
    expect(screen.getByText(/<script>/)).toBeInTheDocument();
    expect(document.querySelector('script')).toBeNull();
    expect(screen.queryByRole('heading', { level: 1 })).not.toBeInTheDocument();
    expect(screen.queryAllByRole('heading', { level: 3 })).toHaveLength(2);
    const link = within(cards[0]!).getByRole('button', {
      name: 'Open link externally: hardening guide (docs.example.com)',
    });
    expect(link).toHaveTextContent('docs.example.com');
    await user.click(link);
    expect(mock.api.openExternal).toHaveBeenCalledWith({ url: 'https://docs.example.com/harden' });
    expect(
      within(cards[1]!).getByRole('checkbox', { name: /Consider extracting the parser/ }),
    ).toBeChecked();
    // The selection checkbox is standalone: rich content is never its descendant.
    for (const card of cards) {
      for (const checkbox of within(card).getAllByRole('checkbox', { name: /Select feedback/ })) {
        expect(checkbox.closest('label')).toBeNull();
        expect(checkbox.contains(link)).toBe(false);
      }
    }
  });

  it('blocks authored links that carry credentials', async () => {
    const { user } = await renderWorkspace();
    await expandAll(user);
    expect(screen.getByText('Link blocked')).toBeInTheDocument();
    expect(screen.getByText('this mirror')).toBeInTheDocument();
    expect(screen.queryByText(/user:pass/)).not.toBeInTheDocument();
  });

  it('renders remote images as inert placeholders — blocked, or an external action that never navigates selection', async () => {
    const { mock, user } = await renderWorkspace();
    await expandAll(user);
    const cards = screen.getAllByRole('article');
    expect(document.querySelector('img')).toBeNull();
    expect(within(cards[1]!).getByText('Image blocked')).toBeInTheDocument();
    expect(within(cards[1]!).getByText('Screen Recording')).toBeInTheDocument();
    expect(within(cards[1]!).getByText('attachments.example.com')).toBeInTheDocument();
    const action = within(cards[1]!).getByRole('button', {
      name: 'Open image externally: failing screenshot (attachments.example.com)',
    });
    await user.click(action);
    expect(mock.api.openExternal).toHaveBeenCalledWith({
      url: 'https://attachments.example.com/shot.png',
    });
    expect(
      within(cards[1]!).getByRole('checkbox', { name: /Consider extracting the parser/ }),
    ).toBeChecked();
    expect(mock.api.updateReviewFeedbackSelection).not.toHaveBeenCalled();
  });

  it('renders raw diff hunks as labelled, semantically classified text lines', async () => {
    const { user } = await renderWorkspace();
    await expandAll(user);
    const cards = screen.getAllByRole('article');
    const regions = cards.flatMap((card) => within(card).queryAllByRole('group', { name: 'Diff' }));
    expect(regions).toHaveLength(2);
    for (const region of regions) {
      expect(region.querySelector('[data-kind="hunk"]')?.textContent).toMatch(/@@ /);
      expect(region.querySelector('[data-kind="add"]')).toHaveTextContent('Added line:');
      expect(region.querySelector('[data-kind="remove"]')).toHaveTextContent('Removed line:');
    }
    // Context lines keep their own label and intact text.
    expect(regions[1]!.querySelector('[data-kind="context"]')).toHaveTextContent('Context line:');
    expect(regions[1]!.querySelector('[data-kind="context"]')).toHaveTextContent('keep');
    expect(document.querySelector('pre code.hljs')).toHaveTextContent('newQueryCodec()');
  });

  it('collapses long feedback deterministically and expands it for the rest of the entry', async () => {
    const { user } = await renderWorkspace();
    const cards = screen.getAllByRole('article');
    const control = within(cards[0]!).getByRole('button', { name: 'Show full feedback' });
    const content = document.querySelector('#review-feedback-content-repo-a-review-41');
    expect(control).toHaveAttribute('aria-expanded', 'false');
    expect(control).toHaveAttribute('aria-controls', 'review-feedback-content-repo-a-review-41');
    expect(content).toHaveAttribute('data-collapsed', 'true');
    await user.click(control);
    expect(within(cards[0]!).getByRole('button', { name: 'Show less' })).toHaveAttribute(
      'aria-expanded',
      'true',
    );
    expect(content).toHaveAttribute('data-collapsed', 'false');
    // Hiding the card behind a scope change, then showing it again, keeps the
    // expansion for this entry.
    await user.click(screen.getByRole('radio', { name: /^repo-b/ }));
    expect(
      screen.queryByLabelText(/Rewrite this to avoid the bearer token/),
    ).not.toBeInTheDocument();
    await user.click(screen.getByRole('radio', { name: /All feedback/ }));
    expect(
      within(screen.getAllByRole('article')[0]!).getByRole('button', { name: 'Show less' }),
    ).toBeInTheDocument();
  });

  it('omits the expansion control for short feedback and keeps its content expanded', async () => {
    await renderWorkspace({
      draft: draftView({
        repos: [
          {
            repo: 'repo-a',
            prUrl: '',
            comments: [
              comment({
                stableRef: 'a:issue:1',
                repo: 'a',
                id: 1,
                type: 'issue',
                body: 'Short note.',
              }),
            ],
          },
        ],
      }),
    });
    expect(screen.queryByRole('button', { name: 'Show full feedback' })).not.toBeInTheDocument();
    expect(
      screen.getByText('Short note.').closest('[data-collapsed]')?.getAttribute('data-collapsed'),
    ).toBe('false');
  });

  it('collapses purely on diff length when the body is short', async () => {
    await renderWorkspace({
      draft: draftView({
        repos: [
          {
            repo: 'repo-a',
            prUrl: '',
            comments: [
              comment({
                stableRef: 'a:review:2',
                repo: 'a',
                id: 2,
                type: 'review',
                body: 'Brief.',
                diffHunk: Array.from({ length: 21 }, (_, i) => ` line ${i}`).join('\n'),
              }),
            ],
          },
        ],
      }),
    });
    expect(screen.getByRole('button', { name: 'Show full feedback' })).toBeInTheDocument();
  });
});

describe('ReviewFeedbackWorkspace', () => {
  it('renders repository sections in server order with ledgers, PR links, and the scope rail', async () => {
    const { mock, user } = await renderWorkspace();
    expect(mock.api.fetchReviewFeedback).toHaveBeenCalledWith({ featureId: PARENT_ID });
    expect(screen.getByRole('dialog', { name: 'Address review feedback' })).toBeVisible();
    expect(screen.getByText('2 of 3 selected', { selector: 'p' })).toBeVisible();

    // Scope rail ledger: All feedback plus one row per repo, parent order.
    const rail = screen.getByRole('navigation', { name: 'Feedback scope' });
    const radios = within(rail).getAllByRole('radio');
    expect(
      radios.map(
        (radio) =>
          within(radio.closest('label')!).getByText(/.*/, {
            selector: '.review-feedback-workspace__scope-name',
          }).textContent,
      ),
    ).toEqual(['All feedback', 'repo-a', 'repo-b']);
    expect(radios[0]).toBeChecked();
    // Accessible ratios accompany the visible ones.
    expect(within(rail).getByText('2 of 3 selected')).toBeVisible();
    expect(within(rail).getByText('2 of 2 selected')).toBeVisible();
    expect(within(rail).getByText('0 of 1 selected')).toBeVisible();

    // Feed: one labelled section per repository, server order, oldest-first cards.
    const sections = screen.getAllByRole('region', { name: /^repo-/ });
    expect(sections.map((section) => section.getAttribute('aria-label'))).toEqual([
      'repo-a',
      'repo-b',
    ]);
    expect(within(sections[0]!).getByRole('heading', { name: 'repo-a' })).toBeVisible();
    expect(within(sections[0]!).getByText('2 of 2 selected')).toBeVisible();
    expect(within(sections[1]!).getByText('0 of 1 selected')).toBeVisible();

    // Cards: author, type label, path:line, humanized creation time.
    expect(within(sections[0]!).getByText('Octocat')).toBeVisible();
    expect(within(sections[0]!).getByText('Review comment')).toBeVisible();
    expect(screen.getByText('src/query.ts:12')).toBeVisible();
    expect(screen.getByText(/\d+ days ago/)).toBeVisible();
    expect(screen.getByLabelText(/Consider extracting the parser/)).toBeChecked();
    expect(screen.getByLabelText(/Overall looks good/)).not.toBeChecked();

    // PR links route through the privileged external-browser boundary.
    await user.click(within(sections[1]!).getByRole('button', { name: 'Open pull request' }));
    expect(mock.api.openExternal).toHaveBeenCalledWith({
      url: 'https://github.com/org/repo-b/pull/7',
    });
  });

  it('choosing one repository narrows the feed without touching selections anywhere', async () => {
    const { user } = await renderWorkspace();
    await user.click(screen.getByRole('radio', { name: /repo-b/ }));
    expect(screen.getByRole('radio', { name: /repo-b/ })).toBeChecked();
    expect(screen.queryByText(/Rewrite this to avoid the bearer token/)).not.toBeInTheDocument();
    const reviewBody = screen.getByLabelText(/Overall looks good/);
    expect(reviewBody).not.toBeChecked();
    // The header ledger still counts the full draft, including hidden scopes.
    expect(screen.getByText('2 of 3 selected', { selector: 'p' })).toBeVisible();
    // And the visible summary reflects the scoped count.
    expect(summary('1 of 1 comments visible')).toBeVisible();
    // Returning to All feedback restores both sections.
    await user.click(screen.getByRole('radio', { name: /All feedback/ }));
    expect(screen.getByText(/Rewrite this to avoid the bearer token/)).toBeVisible();
  });

  it('combines author and type facets with OR inside and AND across, with path substring matching', async () => {
    const { user } = await renderWorkspace();
    // Author facet: one of two authors in scope.
    await user.click(screen.getByRole('checkbox', { name: 'Octocat' }));
    expect(summary('1 of 3 comments visible')).toBeVisible();
    expect(screen.getByText('Author: Octocat')).toBeVisible();
    // OR within the facet.
    await user.click(screen.getByRole('checkbox', { name: 'hubot' }));
    expect(summary('2 of 3 comments visible')).toBeVisible();
    // AND across facets: of the two selected authors, only Octocat wrote a
    // review comment; hubot's issue comment drops out.
    await user.click(screen.getByRole('checkbox', { name: 'Review comment' }));
    expect(summary('1 of 3 comments visible')).toBeVisible();
    expect(screen.queryByLabelText(/Consider extracting the parser/)).not.toBeInTheDocument();
    // Path query is a case-insensitive substring over the displayed path.
    await user.type(screen.getByRole('searchbox', { name: 'File path' }), 'QUERY.TS');
    expect(summary('1 of 3 comments visible')).toBeVisible();
    expect(
      await screen.findByLabelText(/Rewrite this to avoid the bearer token/),
    ).toBeInTheDocument();
    // The pathless comment 42 is in the selected author facet but has no path,
    // so it never matches a non-empty path query.
    expect(screen.queryByLabelText(/Consider extracting the parser/)).not.toBeInTheDocument();
  });

  it('prunes facet values that leave the scope while keeping the path query', async () => {
    const { user } = await renderWorkspace();
    await user.click(screen.getByRole('checkbox', { name: 'Octocat' }));
    await user.type(screen.getByRole('searchbox', { name: 'File path' }), 'src/');
    await user.click(screen.getByRole('radio', { name: /repo-b/ }));
    // Octocat is not an author in repo-b: the chip and constraint are gone...
    expect(screen.queryByText('Author: Octocat')).not.toBeInTheDocument();
    expect(screen.queryByRole('checkbox', { name: 'Octocat' })).not.toBeInTheDocument();
    // ...but the path query is retained and applied to the new scope.
    expect(screen.getByRole('searchbox', { name: 'File path' })).toHaveValue('src/');
    expect(screen.getByText('Path: src/')).toBeVisible();
    // The pathless repo-b comment does not match the retained path query.
    expect(summary('0 of 1 comments visible')).toBeVisible();
    expect(screen.getByText(/No comments match the active filters/)).toBeVisible();
  });

  it('removes filters via chips or Clear all without changing scope or selections', async () => {
    const { user } = await renderWorkspace();
    await user.click(screen.getByRole('checkbox', { name: 'Octocat' }));
    await user.type(screen.getByRole('searchbox', { name: 'File path' }), 'query');
    await user.click(screen.getByRole('button', { name: 'Remove author filter: Octocat' }));
    expect(screen.queryByText('Author: Octocat')).not.toBeInTheDocument();
    expect(summary('1 of 3 comments visible')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Clear all filters' }));
    expect(summary('3 of 3 comments visible')).toBeVisible();
    expect(screen.getByRole('radio', { name: /All feedback/ })).toBeChecked();
    expect(screen.getByLabelText(/Overall looks good/)).not.toBeChecked();
  });

  it('shows a zero-result state that preserves the full draft and launch availability', async () => {
    const { user } = await renderWorkspace();
    await user.click(screen.getByRole('checkbox', { name: 'Octocat' }));
    await user.click(screen.getByRole('checkbox', { name: 'Issue' }));
    const empty = screen.getByRole('status');
    expect(empty).toHaveTextContent(/No comments match the active filters/);
    // Launch stays based on the full committed draft, not the empty feed.
    expect(screen.getByRole('button', { name: 'Launch child (2)' })).toBeEnabled();
    await user.click(within(empty).getByRole('button', { name: 'Clear all filters' }));
    expect(summary('3 of 3 comments visible')).toBeVisible();
  });

  it('Select visible and Clear visible act only on the visible set, with counted labels', async () => {
    const { mock, user } = await renderWorkspace();
    // "Clear visible (2)": both selected repo-a/repo-b-visible comments; the
    // deselected review body is not a clear target.
    const clear = screen.getByRole('button', { name: 'Clear visible (2)' });
    expect(screen.getByRole('button', { name: 'Select visible (1)' })).toBeEnabled();
    const ack = draftView({ revision: 8 });
    ack.repos[0]!.comments[0] = { ...ack.repos[0]!.comments[0]!, selected: false };
    ack.repos[0]!.comments[1] = { ...ack.repos[0]!.comments[1]!, selected: false };
    mock.api.updateReviewFeedbackSelection.mockResolvedValue(ackWith(8, ack));
    await user.click(clear);
    await waitFor(() => expect(mock.api.updateReviewFeedbackSelection).toHaveBeenCalledOnce());
    // Only the visible state-changing references were sent — hidden (none here)
    // and already-deselected comments are excluded.
    expect(mock.api.updateReviewFeedbackSelection.mock.calls[0]![0]).toEqual({
      featureId: PARENT_ID,
      expectedRevision: 7,
      updates: [
        { stableRef: 'repo-a:review:41', selected: false },
        { stableRef: 'repo-a:issue:42', selected: false },
      ],
    });
    // Hidden selections are unaffected: the draft stays 0/3 selected after ack.
    expect(screen.getByText('0 of 3 selected', { selector: 'p' })).toBeVisible();
    // Disabled at zero once there is nothing left to clear.
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Clear visible (0)' })).toBeDisabled(),
    );
  });

  it('splits bulk targets into sequential batches of at most 512 chained revisions', async () => {
    const many: ReviewFeedbackDraftCommentView[] = Array.from({ length: 1025 }, (_, index) =>
      comment({
        stableRef: `repo-a:review:${index + 1}`,
        repo: 'repo-a',
        id: index + 1,
        type: 'review',
        author: 'octocat',
      }),
    );
    const draft = draftView({
      repos: [{ repo: 'repo-a', prUrl: '', comments: many }],
    });
    const { mock, user } = await renderWorkspace({ draft });
    const first = deferred<{ revision: number; repos: ReviewFeedbackDraftView['repos'] }>();
    mock.api.updateReviewFeedbackSelection
      .mockReturnValueOnce(first.promise)
      .mockResolvedValue(ackWith(9));
    await user.click(screen.getByRole('button', { name: 'Clear visible (1025)' }));
    await waitFor(() => expect(mock.api.updateReviewFeedbackSelection).toHaveBeenCalledOnce());
    const batch1 = mock.api.updateReviewFeedbackSelection.mock.calls[0]![0] as {
      expectedRevision: number;
      updates: ReviewFeedbackDraftCommentView[];
    };
    expect(batch1.expectedRevision).toBe(7);
    expect(batch1.updates).toHaveLength(512);
    // The second batch cannot start before the first batch's revision returns.
    expect(mock.api.updateReviewFeedbackSelection).toHaveBeenCalledOnce();
    first.resolve({ revision: 8, repos: draft.repos });
    await waitFor(() => expect(mock.api.updateReviewFeedbackSelection).toHaveBeenCalledTimes(3));
    const batch2 = mock.api.updateReviewFeedbackSelection.mock.calls[1]![0] as {
      expectedRevision: number;
      updates: unknown[];
    };
    expect(batch2.expectedRevision).toBe(8);
    expect(batch2.updates).toHaveLength(512);
    const batch3 = mock.api.updateReviewFeedbackSelection.mock.calls[2]![0] as {
      expectedRevision: number;
      updates: unknown[];
    };
    expect(batch3.expectedRevision).toBe(9);
    expect(batch3.updates).toHaveLength(1);
  });

  it('stops later bulk batches on a conflict and converges via refetch with a focused explanation', async () => {
    const many: ReviewFeedbackDraftCommentView[] = Array.from({ length: 600 }, (_, index) =>
      comment({
        stableRef: `repo-a:review:${index + 1}`,
        repo: 'repo-a',
        id: index + 1,
        type: 'review',
      }),
    );
    const draft = draftView({ repos: [{ repo: 'repo-a', prUrl: '', comments: many }] });
    const { mock, user } = await renderWorkspace({ draft });
    mock.api.updateReviewFeedbackSelection
      .mockResolvedValueOnce({ revision: 8, repos: draft.repos })
      .mockRejectedValueOnce(
        Object.assign(new Error('stale'), { code: 'review_feedback_revision_conflict' }),
      );
    await user.click(screen.getByRole('button', { name: 'Clear visible (600)' }));
    await waitFor(() => expect(mock.api.updateReviewFeedbackSelection).toHaveBeenCalledTimes(2));
    // No third batch is ever sent against the stale revision.
    await waitFor(() => expect(mock.api.fetchReviewFeedback).toHaveBeenCalledTimes(2));
    expect(mock.api.updateReviewFeedbackSelection).toHaveBeenCalledTimes(2);
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Selections reloaded');
    expect(alert).toHaveTextContent(/stale/);
    expect(alert).toHaveFocus();
  });

  it('toggles update the visible choice immediately and send a reference-only mutation', async () => {
    const { mock, user } = await renderWorkspace();
    const ack = draftView({ revision: 8 });
    ack.repos[0]!.comments[1] = { ...ack.repos[0]!.comments[1]!, selected: false };
    mock.api.updateReviewFeedbackSelection.mockResolvedValue(ackWith(8, ack));
    const comment42 = screen.getByLabelText(/Consider extracting the parser/);
    await user.click(comment42);
    expect(comment42).not.toBeChecked();
    await waitFor(() => expect(mock.api.updateReviewFeedbackSelection).toHaveBeenCalledOnce());
    const request = mock.api.updateReviewFeedbackSelection.mock.calls[0]![0] as {
      featureId: string;
      expectedRevision: number;
      updates: unknown[];
    };
    expect(request.featureId).toBe(PARENT_ID);
    expect(request.expectedRevision).toBe(7);
    expect(request.updates).toEqual([{ stableRef: 'repo-a:issue:42', selected: false }]);
  });

  it('serializes rapid edits: each request uses the revision from the previous ack', async () => {
    const { mock, user } = await renderWorkspace();
    const first = deferred<{ revision: number; repos: ReviewFeedbackDraftView['repos'] }>();
    mock.api.updateReviewFeedbackSelection
      .mockReturnValueOnce(first.promise)
      .mockResolvedValueOnce(ackWith(9));
    await user.click(screen.getByLabelText(/Rewrite this to avoid the bearer token/));
    await waitFor(() => expect(mock.api.updateReviewFeedbackSelection).toHaveBeenCalledOnce());
    await user.click(screen.getByLabelText(/Consider extracting the parser/));
    expect(mock.api.updateReviewFeedbackSelection).toHaveBeenCalledOnce();
    first.resolve(ackWith(8));
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
    const comment90 = screen.getByLabelText(/Overall looks good/);
    await user.click(comment90);
    await waitFor(() => expect(mock.api.fetchReviewFeedback).toHaveBeenCalledTimes(2));
    expect(comment90).not.toBeChecked();
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Selections reloaded');
    expect(alert).toHaveTextContent(/stale/);
    expect(alert).toHaveFocus();
  });

  describe('conflict reload failure recovery', () => {
    it('a failed conflict refetch surfaces a focused actionable retry alert and never claims the workspace is reloaded', async () => {
      const { mock, user } = await renderWorkspace();
      mock.api.updateReviewFeedbackSelection.mockRejectedValueOnce(
        Object.assign(new Error('stale'), { code: 'review_feedback_revision_conflict' }),
      );
      mock.api.fetchReviewFeedback.mockRejectedValueOnce(new Error('fetch unavailable'));
      const comment90 = screen.getByLabelText(/Overall looks good/);
      await user.click(comment90);
      await waitFor(() => expect(mock.api.fetchReviewFeedback).toHaveBeenCalledTimes(2));
      const alert = await screen.findByRole('alert');
      expect(alert).toHaveTextContent('Selections could not be reloaded');
      expect(alert).toHaveTextContent(/fetch unavailable/);
      expect(alert).toHaveFocus();
      expect(screen.getByRole('button', { name: 'Retry reload' })).toBeEnabled();
      // The premature conflict notice is never published without the adopted view.
      expect(screen.queryByText(/Selections reloaded/)).not.toBeInTheDocument();
      // Mutations stay frozen while the draft is unreconciled.
      expect(comment90).toBeDisabled();
      expect(screen.getByRole('button', { name: /Launch child/ })).toBeDisabled();
    });

    it('retrying a failed conflict reload recovers and then shows the focused conflict notice', async () => {
      const { mock, user } = await renderWorkspace();
      mock.api.updateReviewFeedbackSelection.mockRejectedValueOnce(
        Object.assign(new Error('stale'), { code: 'review_feedback_revision_conflict' }),
      );
      mock.api.fetchReviewFeedback.mockRejectedValueOnce(new Error('fetch unavailable'));
      await user.click(screen.getByLabelText(/Overall looks good/));
      const failedAlert = await screen.findByRole('alert');
      expect(failedAlert).toHaveTextContent('Selections could not be reloaded');
      mock.api.fetchReviewFeedback.mockResolvedValueOnce(draftView({ revision: 9 }));
      await user.click(screen.getByRole('button', { name: 'Retry reload' }));
      await waitFor(() => expect(mock.api.fetchReviewFeedback).toHaveBeenCalledTimes(3));
      const notice = await screen.findByRole('alert');
      expect(notice).toHaveTextContent('Selections reloaded');
      expect(notice).toHaveTextContent(/stale/);
      expect(notice).toHaveFocus();
      // Reconciled: mutations are live again off the adopted revision.
      expect(screen.getByLabelText(/Overall looks good/)).toBeEnabled();
      expect(screen.getByRole('button', { name: /Launch child \(2\)/ })).toBeEnabled();
    });
  });

  it('disables launch while a selection save is pending and re-enables after the ack', async () => {
    const { mock, user } = await renderWorkspace();
    const pending = deferred<{ revision: number; repos: ReviewFeedbackDraftView['repos'] }>();
    mock.api.updateReviewFeedbackSelection.mockReturnValueOnce(pending.promise);
    await user.click(screen.getByLabelText(/Consider extracting the parser/));
    const launch = screen.getByRole('button', { name: /^Launch child/ });
    expect(launch).toBeDisabled();
    pending.resolve(ackWith(8));
    await waitFor(() => expect(launch).toBeEnabled());
  });

  it('waits for a pending save before leaving when Back is pressed', async () => {
    const { mock, onBack, user } = await renderWorkspace();
    const pending = deferred<{ revision: number; repos: ReviewFeedbackDraftView['repos'] }>();
    mock.api.updateReviewFeedbackSelection.mockReturnValueOnce(pending.promise);
    await user.click(screen.getByLabelText(/Consider extracting the parser/));
    await user.click(screen.getByRole('button', { name: 'Back' }));
    expect(onBack).not.toHaveBeenCalled();
    pending.resolve(ackWith(8));
    await waitFor(() => expect(onBack).toHaveBeenCalledOnce());
  });

  it('restores the committed selection on re-entry with view state reset', async () => {
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
    // Header reports the committed 1-of-3 draft selection.
    expect(screen.getByText('1 of 3 selected', { selector: 'p' })).toBeVisible();
    // Fresh entry: All feedback scope, no filters.
    expect(screen.getByRole('radio', { name: /All feedback/ })).toBeChecked();
    expect(screen.queryByRole('list', { name: 'Active filters' })).not.toBeInTheDocument();
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

  it('recovers a zero-launchable rejection by adopting the latest feedback, then focuses the notice', async () => {
    const { mock, user } = await renderWorkspace();
    mock.api.launchReviewFeedbackChild.mockRejectedValue(
      Object.assign(new Error('review_feedback_zero_launchable_selection: nothing launchable'), {
        code: 'review_feedback_zero_launchable_selection',
      }),
    );
    await user.click(screen.getByRole('button', { name: /Launch child \(2\)/ }));
    // The draft is preserved and the latest authoritative feedback is
    // adopted before the explanatory notice takes focus — never a repeat of
    // the same launch against the unchanged stale view.
    await waitFor(() => expect(mock.api.fetchReviewFeedback).toHaveBeenCalledTimes(2));
    const notice = await screen.findByRole('alert');
    expect(notice).toHaveTextContent('Selected feedback is gone');
    expect(notice).toHaveTextContent(/nothing launchable/);
    expect(notice).toHaveFocus();
    // The adopted view (not the stale launch view) is live again.
    expect(screen.getByLabelText(/Consider extracting the parser/)).toBeChecked();
    expect(screen.getByRole('button', { name: /Launch child \(2\)/ })).toBeEnabled();
  });

  it('keeps a focused retry when the zero-launchable refresh itself fails', async () => {
    const { mock, user } = await renderWorkspace();
    mock.api.launchReviewFeedbackChild.mockRejectedValue(
      Object.assign(new Error('review_feedback_zero_launchable_selection: nothing launchable'), {
        code: 'review_feedback_zero_launchable_selection',
      }),
    );
    mock.api.fetchReviewFeedback.mockRejectedValueOnce(new Error('fetch unavailable'));
    await user.click(screen.getByRole('button', { name: /Launch child \(2\)/ }));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Selections could not be reloaded');
    expect(alert).toHaveTextContent(/still launchable/);
    expect(alert).toHaveFocus();
    // The generic launch path never returns; the focused retry reconciles.
    expect(screen.queryByText(/fix the issue and choose Launch again/)).not.toBeInTheDocument();
    mock.api.fetchReviewFeedback.mockResolvedValueOnce(draftView({ revision: 9 }));
    await user.click(screen.getByRole('button', { name: 'Retry reload' }));
    const notice = await screen.findByRole('alert');
    expect(notice).toHaveTextContent('Selected feedback is gone');
    expect(screen.getByRole('button', { name: /Launch child \(2\)/ })).toBeEnabled();
  });

  describe('unsaved-choice recovery', () => {
    it('keeps a failed save visible as an unsaved choice, focuses the recovery alert, and freezes mutations', async () => {
      const { mock, user } = await renderWorkspace();
      mock.api.updateReviewFeedbackSelection.mockRejectedValueOnce(new Error('socket dropped'));
      const comment41 = screen.getByLabelText(/Rewrite this to avoid the bearer token/);
      await user.click(comment41);
      const alert = await screen.findByRole('alert');
      expect(alert).toHaveTextContent('Choices not saved');
      expect(alert).toHaveTextContent(/Retry save/);
      expect(alert).toHaveTextContent(/Reload saved selections/);
      expect(alert).toHaveFocus();
      // The user's choice stays visible as unsaved; the server view was not
      // adopted over it (no refetch).
      expect(comment41).not.toBeChecked();
      expect(screen.getByText('Unsaved choice')).toBeVisible();
      expect(screen.getByText(/includes unsaved choices/)).toBeVisible();
      expect(mock.api.fetchReviewFeedback).toHaveBeenCalledOnce();
      expect(alert).toHaveTextContent(/E_IPC/); // internal code stays secondary
      // Selection, bulk, gate, Launch, and Back freeze…
      expect(screen.getByLabelText(/Consider extracting the parser/)).toBeDisabled();
      expect(screen.getByLabelText(/Overall looks good/)).toBeDisabled();
      expect(screen.getByRole('button', { name: /Clear visible/ })).toBeDisabled();
      expect(screen.getByRole('button', { name: /Select visible/ })).toBeDisabled();
      expect(
        screen.getByRole('checkbox', { name: /Pause for Roadmap and Phase plan review/ }),
      ).toBeDisabled();
      expect(
        screen.getByRole('button', { name: 'Unsaved choices — retry or reload' }),
      ).toBeDisabled();
      expect(screen.getByRole('button', { name: 'Back' })).toBeDisabled();
      // …while scope, filters, expansion, and PR actions stay operable.
      expect(screen.getByRole('radio', { name: /All feedback/ })).toBeEnabled();
      expect(screen.getByRole('searchbox', { name: 'File path' })).toBeEnabled();
      expect(screen.getAllByRole('button', { name: 'Show full feedback' })[0]).toBeEnabled();
      expect(screen.getAllByRole('button', { name: 'Open pull request' })[0]).toBeEnabled();
      expect(screen.getByRole('button', { name: 'Retry save' })).toBeEnabled();
      expect(screen.getByRole('button', { name: 'Reload saved selections' })).toBeEnabled();
    });

    it('does not move focus or announce beyond the polite ledger for a background save', async () => {
      const { mock, user } = await renderWorkspace();
      const inflight = deferred<{ revision: number; repos: ReviewFeedbackDraftView['repos'] }>();
      mock.api.updateReviewFeedbackSelection.mockReturnValueOnce(inflight.promise);
      const comment42 = screen.getByLabelText(/Consider extracting the parser/);
      await user.click(comment42);
      expect(comment42).toHaveFocus();
      expect(comment42).not.toBeChecked();
      // The ledger stays the only announcement: no alert ever takes focus.
      expect(screen.getByText('1 of 3 selected · saving…')).toBeVisible();
      const ack = draftView({ revision: 8 });
      ack.repos[0]!.comments = ack.repos[0]!.comments.map((entry) =>
        entry.stableRef === 'repo-a:issue:42' ? { ...entry, selected: false } : entry,
      );
      inflight.resolve(ackWith(8, ack));
      await waitFor(() =>
        expect(screen.getByText('1 of 3 selected', { selector: 'p' })).toBeVisible(),
      );
      expect(screen.queryByRole('alert')).not.toBeInTheDocument();
      expect(comment42).toHaveFocus();
    });

    it('does not send a later edit queued behind a failing save', async () => {
      const { mock, user } = await renderWorkspace();
      const first = deferred<{ revision: number; repos: ReviewFeedbackDraftView['repos'] }>();
      mock.api.updateReviewFeedbackSelection.mockReturnValueOnce(first.promise);
      await user.click(screen.getByLabelText(/Rewrite this to avoid the bearer token/));
      await waitFor(() => expect(mock.api.updateReviewFeedbackSelection).toHaveBeenCalledOnce());
      await user.click(screen.getByLabelText(/Consider extracting the parser/));
      first.reject(new Error('socket dropped'));
      const alert = await screen.findByRole('alert');
      // The queue stopped: the second edit was never sent and both choices
      // remain visible as unsaved over the last acknowledged revision.
      expect(mock.api.updateReviewFeedbackSelection).toHaveBeenCalledOnce();
      expect(screen.getAllByText('Unsaved choice')).toHaveLength(2);
      // Retry sends both outstanding references once, in seq order, from the
      // acknowledged revision.
      const acked = draftView({ revision: 8 });
      acked.repos[0]!.comments = acked.repos[0]!.comments.map((entry) => ({
        ...entry,
        selected: false,
      }));
      mock.api.updateReviewFeedbackSelection.mockResolvedValueOnce({
        revision: 8,
        repos: acked.repos,
      });
      await user.click(within(alert).getByRole('button', { name: 'Retry save' }));
      await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument());
      expect(mock.api.updateReviewFeedbackSelection).toHaveBeenCalledTimes(2);
      expect(mock.api.updateReviewFeedbackSelection.mock.calls[1]![0]).toEqual({
        featureId: PARENT_ID,
        expectedRevision: 7,
        updates: [
          { stableRef: 'repo-a:review:41', selected: false },
          { stableRef: 'repo-a:issue:42', selected: false },
        ],
      });
      expect(screen.queryByText('Unsaved choice')).not.toBeInTheDocument();
      expect(screen.getByText('0 of 3 selected', { selector: 'p' })).toBeVisible();
      expect(screen.getByRole('button', { name: 'Back' })).toBeEnabled();
    });

    it('Retry save resubmits outstanding references in bounded batches from the latest acknowledged revision', async () => {
      const many: ReviewFeedbackDraftCommentView[] = Array.from({ length: 600 }, (_, index) =>
        comment({
          stableRef: `repo-a:review:${index + 1}`,
          repo: 'repo-a',
          id: index + 1,
          type: 'review',
        }),
      );
      const draft = draftView({ repos: [{ repo: 'repo-a', prUrl: '', comments: many }] });
      const { mock, user } = await renderWorkspace({ draft });
      mock.api.updateReviewFeedbackSelection
        .mockResolvedValueOnce({ revision: 8, repos: draft.repos })
        .mockRejectedValueOnce(new Error('socket dropped'));
      await user.click(screen.getByRole('button', { name: 'Clear visible (600)' }));
      await waitFor(() => expect(mock.api.updateReviewFeedbackSelection).toHaveBeenCalledTimes(2));
      // Batch one committed: 88 choices of the failed batch stay unsaved.
      const alert = await screen.findByRole('alert');
      expect(screen.getAllByText('Unsaved choice')).toHaveLength(88);
      mock.api.updateReviewFeedbackSelection.mockResolvedValueOnce({
        revision: 9,
        repos: draft.repos,
      });
      await user.click(within(alert).getByRole('button', { name: 'Retry save' }));
      await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument());
      expect(mock.api.updateReviewFeedbackSelection).toHaveBeenCalledTimes(3);
      const retryCall = mock.api.updateReviewFeedbackSelection.mock.calls[2]![0] as {
        expectedRevision: number;
        updates: Array<{ stableRef: string; selected: boolean }>;
      };
      expect(retryCall.expectedRevision).toBe(8);
      expect(retryCall.updates).toHaveLength(88);
      expect(retryCall.updates[0]).toEqual({ stableRef: 'repo-a:review:513', selected: false });
      expect(retryCall.updates.every((update) => update.selected === false)).toBe(true);
      // Everything committed again: controls unfreeze with no unsaved markers.
      expect(screen.queryByText('Unsaved choice')).not.toBeInTheDocument();
      expect(screen.getByRole('button', { name: 'Back' })).toBeEnabled();
      expect(screen.getByRole('button', { name: /Launch child/ })).toBeEnabled();
    });

    it('returns to the same recoverable state when a retry fails transiently', async () => {
      const { mock, user } = await renderWorkspace();
      mock.api.updateReviewFeedbackSelection.mockRejectedValueOnce(new Error('first drop'));
      const comment90 = screen.getByLabelText(/Overall looks good/);
      await user.click(comment90);
      const firstAlert = await screen.findByRole('alert');
      mock.api.updateReviewFeedbackSelection.mockRejectedValueOnce(new Error('second drop'));
      await user.click(within(firstAlert).getByRole('button', { name: 'Retry save' }));
      // Same state: unsaved marker intact, recovery actions focused and live.
      const secondAlert = await screen.findByText('Choices not saved');
      expect(secondAlert).toBeVisible();
      expect(screen.getByText('Unsaved choice')).toBeVisible();
      expect(comment90).toBeDisabled();
      const acked = draftView({ revision: 8 });
      acked.repos[1]!.comments[0] = { ...acked.repos[1]!.comments[0]!, selected: true };
      mock.api.updateReviewFeedbackSelection.mockResolvedValueOnce({
        revision: 8,
        repos: acked.repos,
      });
      await user.click(screen.getByRole('button', { name: 'Retry save' }));
      await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument());
      expect(comment90).toBeChecked();
      expect(screen.queryByText('Unsaved choice')).not.toBeInTheDocument();
    });

    it('Reload saved selections adopts the authoritative draft, resets the view, and announces politely', async () => {
      const { mock, user } = await renderWorkspace();
      mock.api.updateReviewFeedbackSelection.mockRejectedValueOnce(new Error('socket dropped'));
      await user.click(screen.getByLabelText(/Consider extracting the parser/));
      await screen.findByRole('alert');
      // View state the reload must reset: a scope/filters/expansion mix.
      await user.click(screen.getByRole('checkbox', { name: 'Octocat' }));
      await user.click(screen.getAllByRole('button', { name: 'Show full feedback' })[0]!);
      mock.api.fetchReviewFeedback.mockResolvedValue(draftView({ revision: 9 }));
      await user.click(screen.getByRole('button', { name: 'Reload saved selections' }));
      await waitFor(() => expect(mock.api.fetchReviewFeedback).toHaveBeenCalledTimes(2));
      await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument());
      // Overlay abandoned: the authoritative selection is back, unsaved marks gone.
      expect(screen.getByLabelText(/Consider extracting the parser/)).toBeChecked();
      expect(screen.queryByText('Unsaved choice')).not.toBeInTheDocument();
      expect(screen.getByText('2 of 3 selected', { selector: 'p' })).toBeVisible();
      // View reset: All feedback, no filters, collapsed cards.
      expect(screen.getByRole('radio', { name: /All feedback/ })).toBeChecked();
      expect(screen.queryByRole('list', { name: 'Active filters' })).not.toBeInTheDocument();
      expect(screen.getAllByRole('button', { name: 'Show full feedback' })[0]).toBeInTheDocument();
      // Polite, non-focused announcement.
      expect(screen.getByRole('status')).toHaveTextContent(/Saved selections reloaded/);
      expect(screen.getByRole('button', { name: 'Back' })).toBeEnabled();
    });

    it('a failed reload keeps the unsaved choices and both recovery actions intact', async () => {
      const { mock, user } = await renderWorkspace();
      mock.api.updateReviewFeedbackSelection.mockRejectedValueOnce(new Error('socket dropped'));
      await user.click(screen.getByLabelText(/Consider extracting the parser/));
      await screen.findByRole('alert');
      mock.api.fetchReviewFeedback.mockRejectedValueOnce(new Error('fetch unavailable'));
      await user.click(screen.getByRole('button', { name: 'Reload saved selections' }));
      await waitFor(() => expect(mock.api.fetchReviewFeedback).toHaveBeenCalledTimes(2));
      expect(await screen.findByRole('alert')).toHaveTextContent('Choices not saved');
      expect(screen.getByText('Unsaved choice')).toBeVisible();
      expect(screen.getByLabelText(/Consider extracting the parser/)).not.toBeChecked();
      expect(screen.getByRole('button', { name: 'Retry save' })).toBeEnabled();
      expect(screen.getByRole('button', { name: 'Reload saved selections' })).toBeEnabled();
    });

    it('keeps Back from leaving while unsaved choices are unresolved', async () => {
      const { mock, onBack, user } = await renderWorkspace();
      mock.api.updateReviewFeedbackSelection.mockRejectedValueOnce(new Error('socket dropped'));
      await user.click(screen.getByLabelText(/Consider extracting the parser/));
      await screen.findByRole('alert');
      expect(screen.getByRole('button', { name: 'Back' })).toBeDisabled();
      expect(onBack).not.toHaveBeenCalled();
    });
  });

  describe('launch busy state and recovery', () => {
    it('exposes a single busy state that blocks duplicate dispatch and selection mutation', async () => {
      const { mock, user } = await renderWorkspace();
      const inFlight = deferred<{
        featureId?: string;
        childId?: string;
        parentId: string;
      }>();
      mock.api.launchReviewFeedbackChild.mockReturnValueOnce(inFlight.promise);
      await user.click(screen.getByRole('button', { name: /Launch child \(2\)/ }));
      await waitFor(() => expect(mock.api.launchReviewFeedbackChild).toHaveBeenCalledOnce());
      expect(screen.getByRole('button', { name: 'Launching…' })).toBeDisabled();
      expect(screen.getByRole('dialog', { name: 'Address review feedback' })).toHaveAttribute(
        'aria-busy',
        'true',
      );
      expect(screen.getByLabelText(/Rewrite this to avoid the bearer token/)).toBeDisabled();
      expect(screen.getByRole('button', { name: 'Back' })).toBeDisabled();
      inFlight.resolve({ childId: 'child1234ef567890', parentId: PARENT_ID });
      await waitFor(() => expect(mock.api.launchReviewFeedbackChild).toHaveBeenCalledOnce());
    });

    it('surfaces a launch failure in a focused plain-language alert and re-enables an explicit retry', async () => {
      const { mock, onDispatched, user } = await renderWorkspace();
      mock.api.launchReviewFeedbackChild.mockRejectedValueOnce(new Error('github unavailable'));
      await user.click(screen.getByRole('button', { name: /Launch child \(2\)/ }));
      const alert = await screen.findByRole('alert');
      expect(alert).toHaveTextContent('Launch failed');
      expect(alert).toHaveTextContent(/github unavailable/);
      expect(alert).toHaveFocus();
      const retry = screen.getByRole('button', { name: /Launch child \(2\)/ });
      expect(retry).toBeEnabled();
      mock.api.launchReviewFeedbackChild.mockResolvedValueOnce({
        childId: 'child1234ef567890',
        parentId: PARENT_ID,
        changed: 2,
        omitted: 0,
        deferred: 1,
      });
      await user.click(retry);
      await waitFor(() =>
        expect(onDispatched).toHaveBeenCalledWith({
          childId: 'child1234ef567890',
          receipt: '2 changed, 1 deferred since review',
        }),
      );
      expect(mock.api.launchReviewFeedbackChild).toHaveBeenCalledTimes(2);
      // The retry reused the same expected revision and gate.
      expect(mock.api.launchReviewFeedbackChild.mock.calls[1]![0]).toEqual({
        parentId: PARENT_ID,
        expectedRevision: 7,
        gate: true,
      });
    });
  });

  describe('workspace accessibility contract', () => {
    it('exposes exactly one page-level heading with labelled navigation and feed regions', async () => {
      await renderWorkspace();
      expect(screen.getAllByRole('heading', { level: 2 })).toHaveLength(1);
      expect(
        screen.getByRole('heading', { level: 2, name: 'Address review feedback' }),
      ).toBeVisible();
      expect(screen.getByRole('navigation', { name: 'Feedback scope' })).toBeInTheDocument();
      expect(screen.getByRole('main', { name: 'Review feedback' })).toBeInTheDocument();
      const sections = screen.getAllByRole('region', { name: /^repo-/ });
      for (const section of sections) {
        expect(within(section).getByRole('heading', { level: 3 })).toBeInTheDocument();
      }
    });

    it('announces background saving politely through the ledger without stealing focus', async () => {
      const { mock, user } = await renderWorkspace();
      const pendingSave = deferred<{ revision: number; repos: ReviewFeedbackDraftView['repos'] }>();
      mock.api.updateReviewFeedbackSelection.mockReturnValueOnce(pendingSave.promise);
      const comment42 = screen.getByLabelText(/Consider extracting the parser/);
      await user.click(comment42);
      // The ledger is the polite announce target…
      const ledger = screen.getByText(/saving…/);
      expect(ledger).toHaveAttribute('aria-live', 'polite');
      // …and focus never leaves the control the user is operating.
      expect(comment42).toHaveFocus();
      expect(screen.queryByRole('alert')).not.toBeInTheDocument();
      pendingSave.resolve(ackWith(8));
      await waitFor(() => expect(screen.queryByText(/saving…/)).not.toBeInTheDocument());
    });

    it('expansion is keyboard-operable and never changes selection', async () => {
      const { mock, user } = await renderWorkspace();
      const expand = screen.getAllByRole('button', { name: 'Show full feedback' })[0]!;
      expand.focus();
      await user.keyboard('{Enter}');
      expect(expand).toHaveAttribute('aria-expanded', 'true');
      expect(screen.getByLabelText(/Rewrite this to avoid the bearer token/)).toBeChecked();
      expect(mock.api.updateReviewFeedbackSelection).not.toHaveBeenCalled();
    });

    it('Back is the first tab stop and every recovery control stays keyboard-reachable', async () => {
      const { mock, user } = await renderWorkspace();
      await user.tab();
      expect(screen.getByRole('button', { name: 'Back' })).toHaveFocus();
      mock.api.updateReviewFeedbackSelection.mockRejectedValueOnce(new Error('socket dropped'));
      await user.click(screen.getByLabelText(/Consider extracting the parser/));
      await screen.findByRole('alert');
      // Focus order from the alert: Retry save, then Reload saved selections.
      await user.tab();
      expect(screen.getByRole('button', { name: 'Retry save' })).toHaveFocus();
      await user.tab();
      expect(screen.getByRole('button', { name: 'Reload saved selections' })).toHaveFocus();
    });
  });

  describe('narrow layout', () => {
    beforeEach(() => {
      matchMediaState.narrowCockpit = true;
    });

    it('hides the rail and exposes the same controls in a focus-managed drawer', async () => {
      const { user } = await renderWorkspace();
      expect(screen.queryByRole('navigation', { name: 'Feedback scope' })).not.toBeInTheDocument();
      const opener = screen.getByRole('button', { name: 'Repositories and filters' });
      expect(opener).toHaveAttribute('aria-expanded', 'false');
      await user.click(opener);
      expect(opener).toHaveAttribute('aria-expanded', 'true');
      const drawer = screen.getByRole('dialog', { name: 'Repositories and filters' });
      // The same panel: scope ledger, facets, path input, summary, bulk actions.
      expect(within(drawer).getByRole('radio', { name: /All feedback/ })).toBeChecked();
      expect(within(drawer).getByRole('checkbox', { name: 'Octocat' })).toBeInTheDocument();
      expect(within(drawer).getByRole('searchbox', { name: 'File path' })).toBeInTheDocument();
      expect(within(drawer).getByText('3 of 3 comments visible')).toBeVisible();
      expect(within(drawer).getByRole('button', { name: 'Clear visible (2)' })).toBeEnabled();
      // Focus moved into the drawer on opening.
      expect(within(drawer).getByRole('button', { name: 'Close filters' })).toHaveFocus();
      // The wide rail is not mounted while the drawer is open (no duplicates).
      expect(screen.queryByRole('navigation', { name: 'Feedback scope' })).not.toBeInTheDocument();
      // Escape closes and restores focus to the opener.
      await user.keyboard('{Escape}');
      expect(
        screen.queryByRole('dialog', { name: 'Repositories and filters' }),
      ).not.toBeInTheDocument();
      expect(opener).toHaveFocus();
    });

    it('keeps the drawer open while filtering and closes it via its close control', async () => {
      const { user } = await renderWorkspace();
      await user.click(screen.getByRole('button', { name: 'Repositories and filters' }));
      const drawer = screen.getByRole('dialog', { name: 'Repositories and filters' });
      await user.click(within(drawer).getByRole('checkbox', { name: 'Octocat' }));
      await user.type(within(drawer).getByRole('searchbox', { name: 'File path' }), 'query');
      // Composing filters never dismisses the drawer...
      expect(drawer).toBeInTheDocument();
      expect(within(drawer).getByText('1 of 3 comments visible')).toBeVisible();
      // ...and the feed behind it updates without stealing focus back.
      expect(screen.queryByLabelText(/Consider extracting the parser/)).not.toBeInTheDocument();
      await user.click(within(drawer).getByRole('button', { name: 'Close filters' }));
      expect(
        screen.queryByRole('dialog', { name: 'Repositories and filters' }),
      ).not.toBeInTheDocument();
      expect(screen.getByRole('button', { name: 'Repositories and filters' })).toHaveFocus();
    });

    it('keeps the drawer mounted through a recovery transition and restores focus to the opener', async () => {
      const { mock, user } = await renderWorkspace();
      const opener = screen.getByRole('button', { name: 'Repositories and filters' });
      await user.click(opener);
      const drawer = screen.getByRole('dialog', { name: 'Repositories and filters' });
      expect(within(drawer).getByRole('button', { name: 'Close filters' })).toHaveFocus();
      // A bulk failure through the drawer moves focus to the recovery alert
      // without unmounting the drawer.
      mock.api.updateReviewFeedbackSelection.mockRejectedValueOnce(new Error('socket dropped'));
      await user.click(within(drawer).getByRole('button', { name: 'Clear visible (2)' }));
      expect(await screen.findByRole('alert')).toHaveFocus();
      expect(drawer).toBeInTheDocument();
      mock.api.updateReviewFeedbackSelection.mockResolvedValueOnce(ackWith(8, draftView()));
      await user.click(screen.getByRole('button', { name: 'Retry save' }));
      await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument());
      // Escape still closes the drawer and returns focus to its opener.
      await user.keyboard('{Escape}');
      expect(
        screen.queryByRole('dialog', { name: 'Repositories and filters' }),
      ).not.toBeInTheDocument();
      expect(opener).toHaveFocus();
    });

    it('cycles tab focus inside the modal drawer', async () => {
      const { user } = await renderWorkspace();
      await user.click(screen.getByRole('button', { name: 'Repositories and filters' }));
      const drawer = screen.getByRole('dialog', { name: 'Repositories and filters' });
      // Tabbing from the last control wraps back to the first (the close action).
      const bulk = within(drawer).getByRole('button', { name: 'Clear visible (2)' });
      bulk.focus();
      await user.tab();
      expect(within(drawer).getByRole('button', { name: 'Close filters' })).toHaveFocus();
      // Shift+Tab from the first wraps to the last enabled control.
      await user.tab({ shift: true });
      expect(within(drawer).getByRole('button', { name: 'Clear visible (2)' })).toHaveFocus();
    });
  });
});
