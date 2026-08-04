/**
 * Undelivered-work model: turns the server-authored completion preflight into
 * the repository lists and display strings the bar, modals, facts rail, and
 * aftercare runway render. Git truth stays on the server; nothing here derives
 * repository state.
 */
import type { CompletionPreflightRepo, CompletionPreflightResult } from '../../../../shared/ipc';

export const UNPUBLISHED_CHANGES = 'unpublished_changes';
export const UNMERGED_CHANGES = 'unmerged_changes';

export interface PendingDeliveryRepo {
  repo: string;
  commits: number;
  dirty: boolean;
  pushMode?: 'fast_forward' | 'rewrite';
  baseBranch?: string;
  prUrl?: string;
}

export interface PendingDelivery {
  publishRepos: PendingDeliveryRepo[];
  mergeRepos: PendingDeliveryRepo[];
}

export function pendingDeliverySummary(
  preflight: CompletionPreflightResult | null,
): PendingDelivery {
  const repos = preflight?.repos ?? [];
  return {
    publishRepos: repos.filter((r) => r.status === UNPUBLISHED_CHANGES).map(pendingRepo),
    mergeRepos: repos.filter((r) => r.status === UNMERGED_CHANGES).map(pendingRepo),
  };
}

export function pendingDeliveryDetail(work: { commits: number; dirty: boolean }): string {
  const parts: string[] = [];
  if (work.commits > 0) parts.push(`${work.commits} commit${work.commits === 1 ? '' : 's'}`);
  if (work.dirty) parts.push('uncommitted changes');
  return parts.join(' · ');
}

export function pendingDeliveryTotals(repos: PendingDeliveryRepo[]): {
  commits: number;
  dirty: boolean;
} {
  return {
    commits: repos.reduce((total, r) => total + r.commits, 0),
    dirty: repos.some((r) => r.dirty),
  };
}

export function pendingDeliveryFact(
  pending: PendingDelivery,
): { label: string; value: string } | null {
  if (pending.publishRepos.length > 0) {
    return { label: 'Unpublished', value: pendingDeliveryDetail(pendingDeliveryTotals(pending.publishRepos)) };
  }
  if (pending.mergeRepos.length > 0) {
    return { label: 'Unmerged', value: pendingDeliveryDetail(pendingDeliveryTotals(pending.mergeRepos)) };
  }
  return null;
}

function pendingRepo(repo: CompletionPreflightRepo): PendingDeliveryRepo {
  return {
    repo: repo.repo,
    commits: repo.pendingCommits ?? 0,
    dirty: repo.pendingDirty ?? false,
    ...(repo.pushMode === undefined ? {} : { pushMode: repo.pushMode }),
    ...(repo.baseBranch === undefined ? {} : { baseBranch: repo.baseBranch }),
    ...(repo.prUrl === undefined ? {} : { prUrl: repo.prUrl }),
  };
}
