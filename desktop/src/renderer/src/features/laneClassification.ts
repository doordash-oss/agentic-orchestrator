/**
 * Pure lane classification for the Bench: every feature maps to exactly one
 * of five lanes so the sidebar and the Overview pane can group and count
 * features without re-deriving dashboard rules. Builds strictly on top of
 * featureView.ts's dashboard classification — it never duplicates the
 * intervention/active status logic that already lives there.
 */
import type { FeatureSnapshot } from '../../../shared/ipc';
import { ACTIVE_STATUSES, dashboardState, orderDashboardFeatures } from './featureView';

/**
 * The five Bench lanes, in the order they should render: active work above
 * archival lanes. This display order is independent of `classifyLane`'s
 * first-match precedence (waiting, running, published, done, at-rest) —
 * don't conflate the two.
 */
export const LANES = ['waiting', 'running', 'at-rest', 'published', 'done'] as const;
export type Lane = (typeof LANES)[number];

const LANE_LABELS: Readonly<Record<Lane, string>> = {
  waiting: 'Waiting on you',
  running: 'Running',
  'at-rest': 'At rest',
  published: 'Published',
  done: 'Done',
};

/** User-facing copy for a lane. */
export function laneLabel(lane: Lane): string {
  return LANE_LABELS[lane];
}

/**
 * True when the feature's active child pass carries something for the
 * human to resolve: an explicit attention entry, or the integration
 * transaction itself parked in its "attention" phase.
 */
function childNeedsAttention(snapshot: FeatureSnapshot): boolean {
  const child = snapshot.activeChild;
  if (child === undefined) return false;
  return child.attention.length > 0 || child.integrationState === 'attention';
}

/**
 * True when the active child pass is itself running work. Mirrors
 * dashboardState's child branch: a pass that failed, needs attention, or
 * hasn't started yet (`Created`) is not "running" — only a pass mid-flight
 * is.
 */
function childIsRunning(snapshot: FeatureSnapshot): boolean {
  const child = snapshot.activeChild;
  if (child === undefined) return false;
  if (childNeedsAttention(snapshot)) return false;
  if (child.status === 'Failed' || child.status === 'Created') return false;
  return true;
}

/**
 * Classifies a single feature into exactly one Bench lane. Precedence
 * (first match wins):
 *
 * 1. `waiting` ("Waiting on you") — dashboardState's intervention bucket
 *    (status Failed/Interrupted/`*NeedsReview`/NeedUserInput, or an active
 *    child pass that itself failed or needs attention), OR the active
 *    child pass carries a pending attention entry / attention integration
 *    state even in combinations dashboardState's bucket wouldn't itself
 *    reach (e.g. a Done parent with an unresolved child pass).
 * 2. `running` — a top-level status in ACTIVE_STATUSES, durable setup
 *    still running, or an active child pass genuinely in flight. This is
 *    checked before the Published/Done checks below, so a Published
 *    parent with a running refactor/rebase pass classifies as Running, not
 *    Published.
 * 3. `published` — status is exactly `Published` (and nothing above matched).
 * 4. `done` — status is exactly `Done` (and nothing above matched).
 * 5. `at-rest` — everything else (CodeReady, Created, the `*Ready` statuses,
 *    and any other non-active, non-terminal status).
 */
export function classifyLane(snapshot: FeatureSnapshot): Lane {
  if (dashboardState(snapshot).bucket === 'intervention' || childNeedsAttention(snapshot)) {
    return 'waiting';
  }
  if (
    ACTIVE_STATUSES.has(snapshot.status) ||
    snapshot.setup?.status === 'running' ||
    childIsRunning(snapshot)
  ) {
    return 'running';
  }
  if (snapshot.status === 'Published') return 'published';
  if (snapshot.status === 'Done') return 'done';
  return 'at-rest';
}

/** One lane's worth of features, ordered newest-first within the lane. */
export type LaneGroups = Record<Lane, FeatureSnapshot[]>;

function emptyLaneGroups(): LaneGroups {
  return { waiting: [], running: [], 'at-rest': [], published: [], done: [] };
}

/**
 * Groups features into their Bench lanes. Each lane's features keep the
 * same stable newest-first ordering `orderDashboardFeatures` already
 * applies within a bucket — this reuses that ordering rather than
 * re-sorting. Every lane key is always present, even when empty, so
 * callers can render a fixed set of sections without existence checks.
 */
export function classifyFeaturesByLane(snapshots: readonly FeatureSnapshot[]): LaneGroups {
  const groups = emptyLaneGroups();
  for (const snapshot of orderDashboardFeatures(snapshots)) {
    groups[classifyLane(snapshot)].push(snapshot);
  }
  return groups;
}

export type LaneCounts = Record<Lane, number>;

/**
 * Per-lane counts, derived from the same classification as the groupings.
 * This is the raw per-snapshot classification only — it does not apply the
 * sidebar's `reclassifyWithPendingAttention` post-pass, so a consumer that
 * needs counts consistent with the rendered sidebar (e.g. an Overview that
 * also surfaces pending-attention features under "Waiting on you") must
 * apply that same re-bucketing rather than reading these counts directly.
 */
export function laneCounts(snapshots: readonly FeatureSnapshot[]): LaneCounts {
  const counts: LaneCounts = { waiting: 0, running: 0, 'at-rest': 0, published: 0, done: 0 };
  for (const snapshot of snapshots) {
    counts[classifyLane(snapshot)] += 1;
  }
  return counts;
}
