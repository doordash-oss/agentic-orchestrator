/*
Copyright 2026 DoorDash, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

/**
 * Pure presentation and request-building logic for the per-row "Update from
 * origin" action in the creation sheet. Availability is derived from a
 * successful behind comparison plus current unoccupied-branch eligibility —
 * the advisory `updateEligible` flag alone is never permission: a branch
 * whose own repository has it checked out (even clean) is unavailable in
 * this phase, and a row whose checkout identity could not be observed can
 * never bind the compare-and-swap request.
 */
import type {
  CanonicalError,
  RepositoryIdentity,
  RepositoryOriginStatusSnapshot,
  RepositoryUpdateSourceRequest,
  RepositoryUpdateSourceResult,
} from '../../../shared/ipc';
import { sameRepoIdentity } from './repoSelections';

/** The row-scoped update action's lifecycle, kept separate from selection
 *  and origin-comparison state. */
export type SourceUpdateAction =
  { phase: 'idle' } | { phase: 'active'; repoKey: string; identity: RepositoryIdentity };

/** One settled update outcome a row should announce and the review should
 *  carry: successes confirm the advance; warnings preserve definitive
 *  failures (typed refusals and canonical rejections) through the review. */
export interface SourceUpdateNotice {
  repoKey: string;
  identity: RepositoryIdentity;
  tone: 'success' | 'warning';
  text: string;
}

/** The exact displayed expectations the compare-and-swap request binds. */
export interface SourceUpdateTarget {
  request: RepositoryUpdateSourceRequest;
  branch: string;
  originBranch: string;
}

const FALLBACK_SERVER_LABEL = 'the connected server';

/**
 * Two catalog entries that share one git common directory are the same
 * repository store under another entry (a linked-worktree checkout
 * registered separately): a mutation through either contends for the same
 * refs, so an active update on one conflicts with the other.
 */
export function sharesCommonDirectory(a: RepositoryIdentity, b: RepositoryIdentity): boolean {
  return a.commonDir === b.commonDir && a.device === b.device;
}

/** The connected server's display name, or a truthful fallback. */
export function serverLabelFor(connection: { status: string; serverName?: string | null }): string {
  if (
    connection.status === 'ready' &&
    connection.serverName != null &&
    connection.serverName !== ''
  ) {
    return connection.serverName;
  }
  return FALLBACK_SERVER_LABEL;
}

/**
 * A row can be updated only when a successful behind comparison is displayed
 * with every expectation the server binds (branch, mapping, both tips, and
 * the observed checkout identity) and the target branch is unoccupied: the
 * advisory eligibility never covers a branch checked out in the row's own
 * repository, which the mutation refuses in this phase even when clean.
 */
export function updateTargetFor(row: RepositoryOriginStatusSnapshot): SourceUpdateTarget | null {
  if (row.status !== 'behind') return null;
  if (row.kind !== 'branch') return null;
  if (row.updateEligible !== true) return null;
  if (row.branch === undefined || row.originBranch === undefined) return null;
  if (row.localSha === undefined || row.fetchedSha === undefined) return null;
  if (row.checkoutHeadRef === undefined || row.checkoutHeadSha === undefined) return null;
  if (row.checkoutHeadRef === `refs/heads/${row.branch}`) return null;
  return {
    request: {
      repoKey: row.repoKey,
      identity: row.identity,
      mode: row.mode,
      branch: row.branch,
      originBranch: row.originBranch,
      expectedLocalSha: row.localSha,
      expectedOriginSha: row.fetchedSha,
      checkoutHeadRef: row.checkoutHeadRef,
      checkoutHeadSha: row.checkoutHeadSha,
    },
    branch: row.branch,
    originBranch: row.originBranch,
  };
}

/**
 * Why a behind target cannot be updated from here, or null when there is
 * nothing truthful to add (the comparison line already says it). Order
 * matters: a clean original-checkout holder emits no advisory blocker at
 * all, so the checkout identity is checked before the blockers.
 */
export function updateBlockedExplanation(
  row: RepositoryOriginStatusSnapshot,
  serverLabel: string,
): string | null {
  if (row.status !== 'behind' || row.kind !== 'branch' || row.branch === undefined) return null;
  if (row.checkoutHeadRef === undefined || row.checkoutHeadSha === undefined) {
    return `${row.branch} cannot be updated from here — the repository's checkout could not be inspected, so it cannot be proven unoccupied.`;
  }
  if (row.checkoutHeadRef === `refs/heads/${row.branch}`) {
    return `${row.branch} is checked out in the original repository — updating it from here is unavailable in this phase; update that checkout yourself.`;
  }
  const blockers = row.updateBlockers ?? [];
  if (blockers.includes('branch_checked_out_in_worktree')) {
    return `${row.branch} is checked out in a linked worktree — update that worktree's checkout on ${serverLabel} yourself.`;
  }
  if (blockers.includes('dirty_target_checkout')) {
    return `${row.branch} is checked out with uncommitted changes — commit or stash them in that checkout before updating.`;
  }
  if (blockers.includes('git_operation_in_progress')) {
    return `${row.branch} cannot be updated right now — another Agentico git operation is running for this repository. Try again once it finishes.`;
  }
  return null;
}

/** The impact line beside the action: exact names, original repository, server. */
export function updateImpactText(target: SourceUpdateTarget, serverLabel: string): string {
  return `Advances ${target.branch} to origin/${target.originBranch} in the original repository on ${serverLabel}.`;
}

function shortSha(sha: string | undefined): string | null {
  if (sha === undefined) return null;
  return sha.slice(0, 7);
}

/** The success announcement for a definitive updated / already-up-to-date result. */
export function updateSuccessText(
  result: RepositoryUpdateSourceResult,
  target: SourceUpdateTarget,
  serverLabel: string,
): string {
  if (result.result === 'already_up_to_date') {
    return `${target.branch} is already at origin/${target.originBranch} on ${serverLabel}.`;
  }
  const sha = shortSha(result.localSha);
  return `Updated ${target.branch} to origin/${target.originBranch} on ${serverLabel}${
    sha === null ? '' : ` (now at ${sha})`
  }.`;
}

/**
 * The warning for a typed stale refusal: names what changed, states that
 * nothing was changed, and points at the explicit next action. A fresh
 * comparison arrives with the refusal's status row; the mutation is never
 * retried automatically with new SHAs.
 */
export function updateRefusalText(
  result: Pick<RepositoryUpdateSourceResult, 'reason' | 'status'>,
  target: SourceUpdateTarget,
  serverLabel: string,
): string {
  const branch = target.branch;
  const originRef = `origin/${target.originBranch}`;
  switch (result.reason) {
    case 'checkout_changed':
      return `${branch} was not updated on ${serverLabel} — the repository's checkout changed since the comparison. Check again, then update from the fresh comparison.`;
    case 'source_changed':
      return `${branch} was not updated on ${serverLabel} — the selected source changed since the comparison. Check again, then update from the fresh comparison.`;
    case 'mapping_changed':
      return `${branch} was not updated on ${serverLabel} — its origin tracking changed, so it no longer maps to ${originRef}. Check again to see the current mapping.`;
    case 'local_tip_changed':
      return `${branch} was not updated on ${serverLabel} — it moved since the comparison. Check again, then update from the fresh comparison.`;
    case 'origin_tip_changed':
      return `${branch} was not updated on ${serverLabel} — ${originRef} moved since the comparison. Check again, then update from the fresh comparison.`;
    case 'origin_branch_missing':
      return `${branch} was not updated on ${serverLabel} — ${originRef} no longer exists on the remote. Nothing was changed.`;
    case 'not_fast_forward':
      return `${branch} was not updated on ${serverLabel} — it has commits ${originRef} does not have, and only fast-forward updates are supported. Nothing was changed.`;
    case 'branch_checked_out': {
      const blockers = result.status?.updateBlockers ?? [];
      if (blockers.includes('branch_checked_out_in_original_checkout')) {
        return `${branch} was not updated — it is now checked out in the original repository, which this phase cannot update. Update that checkout yourself.`;
      }
      if (blockers.includes('branch_checked_out_in_worktree')) {
        return `${branch} was not updated — it is now checked out in a linked worktree. Update that worktree's checkout on ${serverLabel} yourself.`;
      }
      return `${branch} was not updated — it is now checked out in one of the repository's worktrees, which cannot be updated from here.`;
    }
    default:
      return `${branch} was not updated on ${serverLabel} — the displayed expectations no longer match. Check again, then update from the fresh comparison.`;
  }
}

/**
 * The warning for a canonical rejection: the server's own summary is
 * preserved verbatim (it already states the consequence, including that an
 * unprovable attempt must not be assumed rolled back).
 */
export function updateErrorText(
  target: SourceUpdateTarget,
  error: CanonicalError,
  serverLabel: string,
): string {
  return `${target.branch} was not updated on ${serverLabel} — ${error.summary}`;
}

/** The row's notice, when one belongs to this repository identity. */
export function noticeFor(
  notices: readonly SourceUpdateNotice[],
  identity: RepositoryIdentity,
): SourceUpdateNotice | undefined {
  return notices.find((notice) => sameRepoIdentity(notice.identity, identity));
}

/** Notices without one repository identity's entries. */
export function withoutNoticesFor(
  notices: readonly SourceUpdateNotice[],
  identity: RepositoryIdentity,
): readonly SourceUpdateNotice[] {
  return notices.filter((notice) => !sameRepoIdentity(notice.identity, identity));
}
