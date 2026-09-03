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
 * The "What shipped" receipt model: every fact the aftercare group renders is
 * derived here from data the read model actually carries. Nothing in this
 * module invents a value — a datum the snapshot, the run detail, the
 * completion preflight, or an on-demand fetch does not carry becomes an
 * omitted row or an omitted clause, never a placeholder number.
 */
import type {
  CompletionPreflightResult,
  FeatureSnapshot,
  FetchReviewFeedbackResult,
  RepositoryDiffResult,
  RunDetailView,
  VerificationItemView,
} from '../../../shared/ipc';
import { spineStages } from './featureView';
import { verificationTone } from './verificationModel';

export interface ChangesFact {
  files: number;
  additions: number;
  deletions: number;
  /** Undelivered-commit phrase; present only where the preflight carries the field. */
  commitPhrase?: string;
}

/**
 * Aggregates the on-demand repository diffs across every repo. Returns null
 * while no diff data is available — not yet fetched, every fetch failed, or
 * the worktrees have been reclaimed — so the caller omits the row rather than
 * claiming an empty change set.
 */
export function changesFact(
  diffs: readonly RepositoryDiffResult[],
  preflight: CompletionPreflightResult | null,
): ChangesFact | null {
  let files = 0;
  let additions = 0;
  let deletions = 0;
  for (const diff of diffs) {
    files += diff.files.length;
    for (const file of diff.files) {
      additions += file.addedLines ?? 0;
      deletions += file.removedLines ?? 0;
    }
  }
  if (files === 0) return null;
  const commits = pendingCommitTotal(preflight);
  return {
    files,
    additions,
    deletions,
    ...(commits === null
      ? {}
      : { commitPhrase: `${commits} commit${commits === 1 ? '' : 's'} not delivered yet` }),
  };
}

/**
 * Total undelivered commits, or null when no repository carries the cheap
 * `pendingCommits` preflight field. A carried zero is a real fact, but it says
 * nothing worth a clause, so it reads as null too.
 */
function pendingCommitTotal(preflight: CompletionPreflightResult | null): number | null {
  if (preflight === null) return null;
  let total = 0;
  let carried = false;
  for (const repo of preflight.repos) {
    if (repo.pendingCommits === undefined) continue;
    carried = true;
    total += repo.pendingCommits;
  }
  return carried && total > 0 ? total : null;
}

export interface VerificationFact {
  summary: string;
  /** Check names, in contract order; empty when the snapshot carries none. */
  names: string[];
}

/**
 * The verification fact, or null when the snapshot carries no check states —
 * which is the common aftercare case, since the server clears the items the
 * moment harness verification finishes. The row itself always renders (it is
 * the run-record anchor); only this fact omits.
 */
export function verificationFact(
  items: readonly VerificationItemView[] | undefined,
): VerificationFact | null {
  if (items === undefined || items.length === 0) return null;
  const passed = items.filter((item) => verificationTone(item.state) === 'passed').length;
  return {
    summary: `${passed} of ${items.length} check${items.length === 1 ? '' : 's'} passed`,
    names: items.map((item) => item.name),
  };
}

export interface PullRequestRow {
  repo: string;
  url: string;
  /** `#412` when the URL carries a number; the bare host path otherwise. */
  number: string;
  /** Plain-language delivery state clauses, already ordered for display. */
  clauses: string[];
}

/**
 * One row per PR-bearing repository. The state clauses are plain language
 * derived from the preflight status and the read model's freshness phrase —
 * never a GitHub open/merged claim, and never an approvals count, neither of
 * which this app fetches. The unresolved-comment clause appears only when the
 * on-demand review-feedback fetch succeeded.
 */
export function pullRequestRows(
  snapshot: FeatureSnapshot,
  preflight: CompletionPreflightResult | null,
  feedback: FetchReviewFeedbackResult | null,
): PullRequestRow[] {
  const rows: PullRequestRow[] = [];
  for (const repo of snapshot.repoStatus ?? []) {
    const preflightRepo = preflight?.repos.find((candidate) => candidate.repo === repo.name);
    const url = repo.prUrl ?? preflightRepo?.prUrl;
    if (url === undefined || url === '') continue;
    const clauses: string[] = [];
    const state = deliveryState(preflightRepo?.status);
    if (state !== null) clauses.push(state);
    const freshness = freshnessClause(repo.freshness);
    if (freshness !== null) clauses.push(freshness);
    const unresolved = unresolvedClause(repo.name, feedback);
    if (unresolved !== null) clauses.push(unresolved);
    rows.push({ repo: repo.name, url, number: pullRequestNumber(url), clauses });
  }
  return rows;
}

export function pullRequestNumber(url: string): string {
  const match = /\/pull(?:s)?\/(\d+)/.exec(url);
  return match?.[1] === undefined ? url.replace(/^https?:\/\//, '') : `#${match[1]}`;
}

function deliveryState(status: string | undefined): string | null {
  switch (status) {
    case undefined:
      return null;
    case 'already_published':
      return 'Published from this run';
    case 'unpublished_changes':
      return 'New commits not pushed yet';
    case 'unmerged_changes':
      return 'Merged locally, not in the base branch yet';
    case 'eligible':
      return 'Ready to publish';
    case 'completed':
      return 'Merged into the base branch';
    default:
      return sentenceCase(status);
  }
}

// The read model's freshness is already a phrase ("in sync", "local changes");
// it is quoted, not re-worded, and "unknown" is left unsaid.
function freshnessClause(freshness: string | undefined): string | null {
  if (freshness === undefined || freshness.trim() === '') return null;
  const normalized = freshness.trim().toLocaleLowerCase();
  return normalized === 'unknown' ? null : normalized;
}

function unresolvedClause(repo: string, feedback: FetchReviewFeedbackResult | null): string | null {
  if (feedback === null) return null;
  const group = feedback.repos.find((candidate) => candidate.repo === repo);
  const count = group?.comments.length ?? 0;
  if (count === 0) return 'no unresolved comments';
  return `${count} unresolved comment${count === 1 ? '' : 's'}`;
}

export interface PhasesFact {
  /** Pipeline stage count, for the fully completed pip row. */
  stages: number;
  /** Plain-language summary, or null when the run detail carries no per-phase record. */
  summary: string | null;
}

/**
 * The phases-run fact: the pipeline's stage count (which drives the pip row)
 * plus a summary counted from the run detail's per-phase timing record. No
 * rewind or answered-checkpoint claim appears — the read model does not carry
 * either for a sealed run.
 */
export function phasesFact(snapshot: FeatureSnapshot, run: RunDetailView | null): PhasesFact {
  const stages = spineStages(snapshot.pipeline);
  const recorded = run?.timing?.byPhase ?? run?.cost?.byPhase;
  if (recorded === undefined) return { stages: stages.length, summary: null };
  const recordedLabels = new Set(
    Object.keys(recorded).map((key) => key.trim().toLocaleLowerCase()),
  );
  const ran = stages.filter((stage) =>
    recordedLabels.has(stage.label.trim().toLocaleLowerCase()),
  ).length;
  return {
    stages: stages.length,
    summary:
      ran === 0
        ? null
        : ran >= stages.length
          ? `All ${stages.length} phases recorded`
          : `${ran} of ${stages.length} phases recorded`,
  };
}

export function sentenceCase(value: string): string {
  const normalized = value.replace(/[_-]+/g, ' ').trim();
  return normalized === '' ? value : normalized.charAt(0).toUpperCase() + normalized.slice(1);
}
