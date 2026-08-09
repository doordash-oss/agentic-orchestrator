import { describe, it, expect } from 'vitest';
import {
  pendingDeliverySummary,
  pendingDeliveryDetail,
  pendingDeliveryTotals,
  pendingDeliveryFact,
} from './pendingDelivery';
import type { CompletionPreflightResult } from '../../../../shared/ipc';

function pf(repos: CompletionPreflightResult['repos']): CompletionPreflightResult {
  return { featureId: 'f', sourceRevision: 'r', canMarkDone: false, repos };
}

describe('pendingDeliverySummary', () => {
  it('splits unpublished and unmerged repositories', () => {
    const pending = pendingDeliverySummary(
      pf([
        {
          repo: 'api',
          publishable: true,
          touched: true,
          status: 'unpublished_changes',
          pendingCommits: 3,
          pushMode: 'rewrite',
          prUrl: 'https://example/pull/1',
        },
        {
          repo: 'core',
          publishable: false,
          touched: true,
          status: 'unmerged_changes',
          pendingCommits: 1,
          baseBranch: 'main',
        },
        { repo: 'web', publishable: true, touched: true, status: 'already_published' },
      ]),
    );
    expect(pending.publishRepos).toEqual([
      {
        repo: 'api',
        commits: 3,
        dirty: false,
        pushMode: 'rewrite',
        prUrl: 'https://example/pull/1',
        dirtyFiles: [],
        dirtyFileTotal: 0,
      },
    ]);
    expect(pending.mergeRepos).toEqual([
      {
        repo: 'core',
        commits: 1,
        dirty: false,
        baseBranch: 'main',
        dirtyFiles: [],
        dirtyFileTotal: 0,
      },
    ]);
  });

  it('defaults dirtyFiles and dirtyFileTotal when the server omits them', () => {
    const pending = pendingDeliverySummary(
      pf([{ repo: 'api', publishable: true, touched: true, status: 'unpublished_changes' }]),
    );
    expect(pending.publishRepos[0]!.dirtyFiles).toEqual([]);
    expect(pending.publishRepos[0]!.dirtyFileTotal).toBe(0);
  });

  it('carries the dirty file sample and true total from the preflight', () => {
    const pending = pendingDeliverySummary(
      pf([
        {
          repo: 'api',
          publishable: true,
          touched: true,
          status: 'unpublished_changes',
          pendingDirtyFiles: ['a.go', 'b.go'],
          pendingDirtyFileTotal: 5,
        },
      ]),
    );
    expect(pending.publishRepos[0]!.dirtyFiles).toEqual(['a.go', 'b.go']);
    expect(pending.publishRepos[0]!.dirtyFileTotal).toBe(5);
  });

  it('is empty for a null preflight', () => {
    const pending = pendingDeliverySummary(null);
    expect(pending.publishRepos).toEqual([]);
    expect(pending.mergeRepos).toEqual([]);
    expect(pending.initialMergeRepos).toEqual([]);
  });

  it('collects publish-eligible repositories for the runway delivery row', () => {
    const pending = pendingDeliverySummary(
      pf([
        { repo: 'api', publishable: true, touched: true, status: 'eligible' },
        // Untouched, non-publishable, or already published are not eligible.
        { repo: 'idle', publishable: true, touched: false, status: 'eligible' },
        { repo: 'local', publishable: false, touched: true, status: 'eligible' },
        { repo: 'web', publishable: true, touched: true, status: 'already_published' },
      ]),
    );
    expect(pending.publishEligibleRepos.map((repo) => repo.repo)).toEqual(['api']);
  });

  it('collects local repositories whose work has never been merged', () => {
    const pending = pendingDeliverySummary(
      pf([
        { repo: 'local-core', publishable: false, touched: true, status: 'eligible' },
        // Already merged, and merged-with-new-commits, are not first merges.
        { repo: 'local-done', publishable: false, touched: true, status: 'completed' },
        { repo: 'local-again', publishable: false, touched: true, status: 'unmerged_changes' },
        // Publishable and untouched repositories never merge locally.
        { repo: 'api', publishable: true, touched: true, status: 'eligible' },
        { repo: 'idle', publishable: false, touched: false, status: 'eligible' },
      ]),
    );
    expect(pending.initialMergeRepos.map((repo) => repo.repo)).toEqual(['local-core']);
  });
});

describe('pendingDeliveryDetail', () => {
  it('pluralises commits and names uncommitted work', () => {
    expect(pendingDeliveryDetail({ commits: 1, dirty: false })).toBe('1 commit');
    expect(pendingDeliveryDetail({ commits: 3, dirty: false })).toBe('3 commits');
    expect(pendingDeliveryDetail({ commits: 3, dirty: true })).toBe(
      '3 commits · uncommitted changes',
    );
    expect(pendingDeliveryDetail({ commits: 0, dirty: true })).toBe('uncommitted changes');
    expect(pendingDeliveryDetail({ commits: 0, dirty: false })).toBe('');
  });
});

describe('pendingDeliveryTotals', () => {
  it('sums commits and ors dirtiness', () => {
    expect(
      pendingDeliveryTotals([
        { repo: 'a', commits: 2, dirty: false, dirtyFiles: [], dirtyFileTotal: 0 },
        { repo: 'b', commits: 1, dirty: true, dirtyFiles: [], dirtyFileTotal: 0 },
      ]),
    ).toEqual({ commits: 3, dirty: true });
  });
});

describe('pendingDeliveryFact', () => {
  it('labels unpublished work and prefers it over unmerged', () => {
    expect(
      pendingDeliveryFact({
        publishRepos: [{ repo: 'a', commits: 3, dirty: false, dirtyFiles: [], dirtyFileTotal: 0 }],
        mergeRepos: [{ repo: 'b', commits: 1, dirty: false, dirtyFiles: [], dirtyFileTotal: 0 }],
        initialMergeRepos: [],
        publishEligibleRepos: [],
      }),
    ).toEqual({ label: 'Unpublished', value: '3 commits' });
  });

  it('labels unmerged work when nothing is unpublished', () => {
    expect(
      pendingDeliveryFact({
        publishRepos: [],
        mergeRepos: [{ repo: 'b', commits: 1, dirty: true, dirtyFiles: [], dirtyFileTotal: 0 }],
        initialMergeRepos: [],
        publishEligibleRepos: [],
      }),
    ).toEqual({ label: 'Unmerged', value: '1 commit · uncommitted changes' });
  });

  it('is null when everything landed', () => {
    expect(
      pendingDeliveryFact({
        publishRepos: [],
        mergeRepos: [],
        initialMergeRepos: [],
        publishEligibleRepos: [],
      }),
    ).toBeNull();
  });
});
