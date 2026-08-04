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
      { repo: 'api', commits: 3, dirty: false, pushMode: 'rewrite', prUrl: 'https://example/pull/1' },
    ]);
    expect(pending.mergeRepos).toEqual([{ repo: 'core', commits: 1, dirty: false, baseBranch: 'main' }]);
  });

  it('is empty for a null preflight', () => {
    const pending = pendingDeliverySummary(null);
    expect(pending.publishRepos).toEqual([]);
    expect(pending.mergeRepos).toEqual([]);
  });
});

describe('pendingDeliveryDetail', () => {
  it('pluralises commits and names uncommitted work', () => {
    expect(pendingDeliveryDetail({ commits: 1, dirty: false })).toBe('1 commit');
    expect(pendingDeliveryDetail({ commits: 3, dirty: false })).toBe('3 commits');
    expect(pendingDeliveryDetail({ commits: 3, dirty: true })).toBe('3 commits · uncommitted changes');
    expect(pendingDeliveryDetail({ commits: 0, dirty: true })).toBe('uncommitted changes');
    expect(pendingDeliveryDetail({ commits: 0, dirty: false })).toBe('');
  });
});

describe('pendingDeliveryTotals', () => {
  it('sums commits and ors dirtiness', () => {
    expect(
      pendingDeliveryTotals([
        { repo: 'a', commits: 2, dirty: false },
        { repo: 'b', commits: 1, dirty: true },
      ]),
    ).toEqual({ commits: 3, dirty: true });
  });
});

describe('pendingDeliveryFact', () => {
  it('labels unpublished work and prefers it over unmerged', () => {
    expect(
      pendingDeliveryFact({
        publishRepos: [{ repo: 'a', commits: 3, dirty: false }],
        mergeRepos: [{ repo: 'b', commits: 1, dirty: false }],
      }),
    ).toEqual({ label: 'Unpublished', value: '3 commits' });
  });

  it('labels unmerged work when nothing is unpublished', () => {
    expect(
      pendingDeliveryFact({ publishRepos: [], mergeRepos: [{ repo: 'b', commits: 1, dirty: true }] }),
    ).toEqual({ label: 'Unmerged', value: '1 commit · uncommitted changes' });
  });

  it('is null when everything landed', () => {
    expect(pendingDeliveryFact({ publishRepos: [], mergeRepos: [] })).toBeNull();
  });
});
