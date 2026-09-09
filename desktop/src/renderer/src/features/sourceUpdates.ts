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
 * successful behind comparison plus current eligibility — the advisory
 * `updateEligible` flag alone is never permission, and a row whose checkout
 * identity could not be observed can never bind the request. Two update
 * paths share one action: an unoccupied branch moves only its ref through
 * the compare-and-swap path, while a branch held only by the original
 * checkout also advances that repository's index and working files after
 * the server proves the checkout clean and safe to fast-forward.
 */
import type {
  CanonicalError,
  RepositoryIdentity,
  RepositoryOriginStatusSnapshot,
  RepositorySourceReconcileRequest,
  RepositorySourceReconcileResult,
  RepositoryUpdateSourceRequest,
  RepositoryUpdateSourceResult,
} from '../../../shared/ipc';
import { sameRepoIdentity } from './repoSelections';

/** The row-scoped update action's lifecycle, kept separate from selection
 *  and origin-comparison state. */
export type SourceUpdateAction =
  { phase: 'idle' } | { phase: 'active'; record: SourceUpdateUncertainty };

/**
 * One Update-from-origin attempt whose outcome is unknown: the response was
 * lost, timed out, crossed a server switch, or the server proved nothing
 * either way. The record is keyed by its originating server and repository
 * identity, survives draft navigation and reconnection within the live
 * session, and blocks acceptance of its source until a settlement read on
 * the originating server establishes the branch's state.
 */
export interface SourceUpdateUncertainty {
  /** The server the attempt was sent to; null is a resolving runtime. */
  serverKey: string | null;
  repoKey: string;
  identity: RepositoryIdentity;
  mode: 'default' | 'current';
  branch: string;
  originBranch: string;
  expectedLocalSha: string;
  expectedOriginSha: string;
  /** The observed checkout HEAD reference the attempt bound to (refs/heads/... or detached). */
  checkoutHeadRef: string;
  /** The observed checkout HEAD commit the attempt bound to. */
  checkoutHeadSha: string;
  /** Monotonic attempt sequence, fencing late callbacks by attempt. */
  attempt: number;
}

/**
 * Canonical codes whose update result proves nothing about the branch: the
 * server could not prove an outcome, or the response never definitively
 * arrived from the originating server. These record uncertainty; every
 * other rejection is a definitive refusal that becomes a warning.
 */
const UNCERTAIN_UPDATE_ERROR_CODES: ReadonlySet<string> = new Set([
  'source_update_unavailable',
  'E_REQUEST_TIMEOUT',
  'E_SERVER_SWITCHED',
  'E_IPC_UNREACHABLE',
  'E_NOT_CONNECTED',
  'E_GATEWAY',
  'E_REMOTE_SERVER_LOST_REPROBING',
  'E_REMOTE_UNREACHABLE',
  'E_SERVER_EXITED',
  'E_EXTERNAL_RUNTIME_UNRESPONSIVE',
]);

/** True when a rejected update leaves its outcome unknown. */
export function isUncertainUpdateError(error: CanonicalError): boolean {
  return UNCERTAIN_UPDATE_ERROR_CODES.has(error.code);
}

/**
 * True when a rejected settlement read leaves the outcome unknown: the same
 * transport-loss codes, plus the server's own unprovability code. A
 * definitive rejection (a replaced repository, a malformed request) is the
 * only settlement failure that retires the record.
 */
export function isUnsettledReconcileError(error: CanonicalError): boolean {
  return error.code === 'source_reconcile_unavailable' || isUncertainUpdateError(error);
}

/** The settlement request for one uncertain attempt: the same displayed
 *  binding the update carried, unchanged. */
export function uncertaintyRequestFor(
  record: SourceUpdateUncertainty,
): RepositorySourceReconcileRequest {
  return {
    repoKey: record.repoKey,
    identity: record.identity,
    mode: record.mode,
    branch: record.branch,
    originBranch: record.originBranch,
    expectedLocalSha: record.expectedLocalSha,
    expectedOriginSha: record.expectedOriginSha,
    checkoutHeadRef: record.checkoutHeadRef,
    checkoutHeadSha: record.checkoutHeadSha,
  };
}

/** One settled update outcome a row should announce and the review should
 *  carry: successes confirm the advance; warnings preserve definitive
 *  failures (typed refusals and canonical rejections) through the review. */
export interface SourceUpdateNotice {
  repoKey: string;
  identity: RepositoryIdentity;
  tone: 'success' | 'warning';
  text: string;
}

/** The exact displayed expectations the update request binds. */
export interface SourceUpdateTarget {
  request: RepositoryUpdateSourceRequest;
  branch: string;
  originBranch: string;
  /** True when the branch is held only by the row's own original checkout. */
  originalCheckout: boolean;
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
 * the observed checkout identity) and current eligibility holds. An
 * unoccupied branch and a branch held only by the row's own original
 * checkout are both eligible; the server revalidates eligibility and the
 * checkout's safety at execution.
 */
export function updateTargetFor(row: RepositoryOriginStatusSnapshot): SourceUpdateTarget | null {
  if (row.status !== 'behind') return null;
  if (row.kind !== 'branch') return null;
  if (row.updateEligible !== true) return null;
  if (row.branch === undefined || row.originBranch === undefined) return null;
  if (row.localSha === undefined || row.fetchedSha === undefined) return null;
  if (row.checkoutHeadRef === undefined || row.checkoutHeadSha === undefined) return null;
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
    originalCheckout: row.checkoutHeadRef === `refs/heads/${row.branch}`,
  };
}

/**
 * Why a behind target cannot be updated from here, or null when there is
 * nothing truthful to add (the comparison line already says it). The
 * server-reported advisory blockers name the concrete unsafe state; an
 * unobservable checkout identity is the remaining local explanation.
 */
export function updateBlockedExplanation(
  row: RepositoryOriginStatusSnapshot,
  serverLabel: string,
): string | null {
  if (row.status !== 'behind' || row.kind !== 'branch' || row.branch === undefined) return null;
  const blockers = row.updateBlockers ?? [];
  if (blockers.includes('branch_checked_out_in_worktree')) {
    return `${row.branch} is checked out in a linked worktree — update that worktree's checkout on ${serverLabel} yourself.`;
  }
  if (blockers.includes('dirty_target_checkout')) {
    return `${row.branch} is checked out in the original repository, whose checkout has uncommitted or untracked files — commit or stash them outside Agentico before updating.`;
  }
  if (blockers.includes('checkout_operation_in_progress')) {
    return 'A merge, rebase, cherry-pick, or revert is in progress in the original repository — finish or abort it outside Agentico before updating.';
  }
  if (blockers.includes('ignored_path_collision')) {
    return 'Updating would overwrite ignored files or directories in the original repository — move or remove them outside Agentico before updating.';
  }
  if (blockers.includes('checkout_uninspectable')) {
    return "The original repository's checkout could not be inspected safely — resolve it outside Agentico, then check again.";
  }
  if (blockers.includes('git_operation_in_progress')) {
    return `${row.branch} cannot be updated right now — another Agentico git operation is running for this repository. Try again once it finishes.`;
  }
  if (row.checkoutHeadRef === undefined || row.checkoutHeadSha === undefined) {
    return `${row.branch} cannot be updated from here — the repository's checkout could not be inspected, so it cannot be proven unoccupied.`;
  }
  return null;
}

/** The impact line beside the action: exact names, original repository, server. */
export function updateImpactText(target: SourceUpdateTarget, serverLabel: string): string {
  if (target.originalCheckout) {
    return `Advances ${target.branch} and its checked-out files in the original repository on ${serverLabel} to origin/${target.originBranch}.`;
  }
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
  if (target.originalCheckout) {
    return `Updated ${target.branch} and its checked-out files to origin/${target.originBranch} on ${serverLabel}${
      sha === null ? '' : ` (now at ${sha})`
    }.`;
  }
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
      if (blockers.includes('branch_checked_out_in_worktree')) {
        return `${branch} was not updated — it is now checked out in a linked worktree. Update that worktree's checkout on ${serverLabel} yourself.`;
      }
      return `${branch} was not updated — it is now checked out in one of the repository's worktrees, which cannot be updated from here.`;
    }
    case 'dirty_checkout':
      return `${branch} was not updated — the original repository's checkout has uncommitted or untracked files. Commit or stash them outside Agentico, then check again.`;
    case 'checkout_operation_in_progress':
      return `${branch} was not updated — a merge, rebase, cherry-pick, or revert is in progress in the original repository. Finish or abort it outside Agentico, then check again.`;
    case 'ignored_path_collision':
      return `${branch} was not updated — updating would overwrite ignored files or directories in the original repository. Move or remove them outside Agentico, then check again.`;
    case 'checkout_conflict':
      return `${branch} was not updated — the original repository's working tree changed during the update and the attempt refused to touch it. Check again before updating.`;
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

/**
 * The status line while an attempt's outcome is unknown: names the branch
 * and the originating server, never claims a failure or rollback, and
 * states what unblocks acceptance.
 */
export function updateUncertainText(record: SourceUpdateUncertainty, serverLabel: string): string {
  if (record.checkoutHeadRef === `refs/heads/${record.branch}`) {
    return `The result of updating ${record.branch} and its checked-out files in the original repository on ${serverLabel} is unknown — it will be reconciled on ${serverLabel} before this repository can be accepted.`;
  }
  return `The result of updating ${record.branch} on ${serverLabel} is unknown — it will be reconciled on ${serverLabel} before this repository can be accepted.`;
}

/**
 * The row's announcement for one settlement outcome: a target observation
 * on an unoccupied branch, or on a clean original checkout whose HEAD is
 * the observed tip, is a success; anything else is a warning that never
 * claims the attempt was rolled back and points at the next explicit
 * action. The branch tip alone never proves the original checkout's files
 * advanced, so an original-checkout settlement only claims whole-checkout
 * completion from a clean, matching checkout observation.
 */
export function reconcileNoticeText(
  result: RepositorySourceReconcileResult,
  record: SourceUpdateUncertainty,
  serverLabel: string,
): SourceUpdateNotice {
  const branch = record.branch;
  const sha = shortSha(result.localSha);
  const shaText = sha === null ? '' : ` (now at ${sha})`;
  // The outcome guarantees the observed tip; the fallback only guards a
  // malformed settlement from rendering a null SHA into the copy.
  const advanced = sha === null ? 'the update completed' : `the branch advanced to ${sha}`;
  const originalCheckout = record.checkoutHeadRef === `refs/heads/${record.branch}`;
  const checkout = result.checkout;
  const success = (text: string): SourceUpdateNotice => ({
    repoKey: record.repoKey,
    identity: record.identity,
    tone: 'success',
    text,
  });
  const warning = (text: string): SourceUpdateNotice => ({
    repoKey: record.repoKey,
    identity: record.identity,
    tone: 'warning',
    text,
  });
  switch (result.outcome) {
    case 'expected_target_present': {
      if (checkout === undefined) {
        if (originalCheckout) {
          return warning(
            `Reconciled ${branch} on ${serverLabel}: ${advanced}, but the original repository's checkout no longer holds it — refresh the repositories and reselect the source.`,
          );
        }
        return success(`Reconciled ${branch} on ${serverLabel}: the update completed${shaText}.`);
      }
      if (checkout.state === 'clean') {
        if (checkout.headSha === undefined || checkout.headSha === result.localSha) {
          return success(`Reconciled ${branch} on ${serverLabel}: the update completed${shaText}.`);
        }
        return warning(
          `Reconciled ${branch} on ${serverLabel}: ${advanced}, but the original repository's checkout no longer matches it — refresh the repositories and reselect the source.`,
        );
      }
      if (checkout.state === 'dirty') {
        return warning(
          `Reconciled ${branch} on ${serverLabel}: ${advanced}, but the original repository's checkout has uncommitted changes that may include partial update effects — resolve them outside Agentico before relying on its files.`,
        );
      }
      if (checkout.state === 'operation_in_progress') {
        return warning(
          `Reconciled ${branch} on ${serverLabel}: ${advanced}, but a git operation is in progress in the original repository — finish or abort it outside Agentico before relying on its files.`,
        );
      }
      return warning(
        `Reconciled ${branch} on ${serverLabel}: ${advanced}, but the original repository's checkout could not be inspected — resolve it outside Agentico before relying on its files.`,
      );
    }
    case 'original_tip_remains': {
      if (checkout?.state === 'dirty') {
        return warning(
          `Reconciled ${branch} on ${serverLabel}: the update did not complete, and ${branch} is unchanged${shaText}, but the original repository's checkout has uncommitted changes that may include partial update effects — resolve them outside Agentico, then check again.`,
        );
      }
      if (checkout?.state === 'operation_in_progress' || checkout?.state === 'unobserved') {
        return warning(
          `Reconciled ${branch} on ${serverLabel}: the update did not complete, and ${branch} is unchanged${shaText}, but the original repository's checkout needs attention outside Agentico — resolve it, then check again.`,
        );
      }
      return warning(
        `Reconciled ${branch} on ${serverLabel}: the update did not complete, and ${branch} is unchanged${shaText}. Check again before updating.`,
      );
    }
    case 'local_state_changed':
      return warning(
        `Reconciled ${branch} on ${serverLabel}: it is now at a commit that is neither the tip before the update nor the expected origin tip${shaText}. Check again before updating.`,
      );
    default:
      return warning(
        `Reconciled ${branch} on ${serverLabel}: the branch no longer exists. Refresh the repositories and reselect the source.`,
      );
  }
}

/**
 * The warning when the settlement read itself is definitively refused (a
 * replaced repository): the server's summary is preserved verbatim and the
 * source requires reselection.
 */
export function reconcileErrorText(
  record: SourceUpdateUncertainty,
  error: CanonicalError,
  serverLabel: string,
): string {
  return `Reconciling ${record.branch} on ${serverLabel} — ${error.summary}`;
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

/** The unsettled uncertainty record for one repository identity, when any. */
export function uncertaintyFor(
  records: readonly SourceUpdateUncertainty[],
  identity: RepositoryIdentity,
): SourceUpdateUncertainty | undefined {
  return records.find((record) => sameRepoIdentity(record.identity, identity));
}

/** Records without one repository identity's unsettled entry. */
export function withoutUncertaintyFor(
  records: readonly SourceUpdateUncertainty[],
  identity: RepositoryIdentity,
): readonly SourceUpdateUncertainty[] {
  return records.filter((record) => !sameRepoIdentity(record.identity, identity));
}
