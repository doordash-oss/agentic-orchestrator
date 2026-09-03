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
 * Pure lane classification for the Bench: every feature maps to exactly one
 * of six lanes so the sidebar and the Overview pane can group and count
 * features without re-deriving dashboard rules. Builds strictly on top of
 * featureView.ts's dashboard classification — it never duplicates the
 * intervention/active status logic that already lives there.
 */
import type { FeatureSnapshot } from '../../../shared/ipc';
import { ACTIVE_STATUSES, dashboardState, orderDashboardFeatures } from './featureView';

/**
 * The six Bench lanes, in the order they should render: blocking failures
 * above active work above archival lanes. This display order is independent
 * of `classifyLane`'s first-match precedence (failed, waiting, running,
 * published, done, at-rest) — don't conflate the two.
 */
export const LANES = ['failed', 'waiting', 'running', 'at-rest', 'published', 'done'] as const;
export type Lane = (typeof LANES)[number];

const LANE_LABELS: Readonly<Record<Lane, string>> = {
  failed: 'Failed',
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
 * True when the active child pass is itself running work. Mirrors
 * dashboardState's child branch: a pass that owns an error arrives on the
 * snapshot's owned-error list and classifies through it, and a pass that
 * hasn't started yet (`Created`) is not "running" — only a pass mid-flight
 * is.
 */
function childIsRunning(snapshot: FeatureSnapshot): boolean {
  const child = snapshot.activeChild;
  if (child === undefined) return false;
  if (child.status === 'Created') return false;
  return true;
}

/**
 * Classifies a single feature into exactly one Bench lane. Precedence
 * (first match wins):
 *
 * 1. `failed` ("Failed") — the snapshot's owned-error projection carries a
 *    blocking-class entry: the feature or its active child pass owns an
 *    error that stopped work. Status strings never classify this lane.
 * 2. `waiting` ("Waiting on you") — dashboardState's intervention bucket (a
 *    needs_action owned error, or the status-driven pauses that are not
 *    errors: Interrupted, `*NeedsReview`, NeedUserInput).
 * 3. `running` — a top-level status in ACTIVE_STATUSES, durable setup
 *    still running, or an active child pass genuinely in flight. This is
 *    checked before the Published/Done checks below, so a Published
 *    parent with a running refactor/rebase pass classifies as Running, not
 *    Published.
 * 4. `published` — status is exactly `Published` (and nothing above matched).
 * 5. `done` — status is exactly `Done` (and nothing above matched).
 * 6. `at-rest` — everything else (CodeReady, Created, the `*Ready` statuses,
 *    and any other non-active, non-terminal status).
 */
export function classifyLane(snapshot: FeatureSnapshot): Lane {
  if (snapshot.errors.some((entry) => entry.error.class === 'blocking')) {
    return 'failed';
  }
  if (dashboardState(snapshot).bucket === 'intervention') {
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
  return { failed: [], waiting: [], running: [], 'at-rest': [], published: [], done: [] };
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
 * `classifyFeaturesByLaneWithAttention` post-pass below, so a consumer that
 * needs counts consistent with the rendered sidebar (e.g. an Overview that
 * also surfaces pending-attention features under "Waiting on you") must
 * apply that same re-bucketing rather than reading these counts directly.
 */
export function laneCounts(snapshots: readonly FeatureSnapshot[]): LaneCounts {
  const counts: LaneCounts = {
    failed: 0,
    waiting: 0,
    running: 0,
    'at-rest': 0,
    published: 0,
    done: 0,
  };
  for (const snapshot of snapshots) {
    counts[classifyLane(snapshot)] += 1;
  }
  return counts;
}

/**
 * `classifyLane` only sees a feature's own snapshot, which has no top-level
 * "pending attention" field for a standalone feature — the schema only
 * represents attention on an active child relationship
 * (`activeChild.attention`). A feature's own directly-owned attention item
 * (no child pass involved, e.g. a permission prompt or question) is tracked
 * separately in the app-wide attention list, which this function's second
 * argument carries. Any feature with a pending attention count classifies as
 * "Waiting on you" regardless of its status-derived lane, so re-bucket with
 * that list here rather than teach the pure classifier about component-level
 * state it was never given. A feature in the "Failed" lane stays there: a
 * blocking error outranks a pending prompt.
 */
export function classifyFeaturesByLaneWithAttention(
  snapshots: readonly FeatureSnapshot[],
  attentionByFeature: ReadonlyMap<string, number>,
): LaneGroups {
  const laneGroups = classifyFeaturesByLane(snapshots);
  if (attentionByFeature.size === 0) return laneGroups;
  const next: LaneGroups = {
    failed: [...laneGroups.failed],
    waiting: [...laneGroups.waiting],
    running: [],
    published: [],
    done: [],
    'at-rest': [],
  };
  for (const lane of LANES) {
    if (lane === 'waiting' || lane === 'failed') continue;
    for (const feature of laneGroups[lane]) {
      if ((attentionByFeature.get(feature.id) ?? 0) > 0) {
        next.waiting.push(feature);
      } else {
        next[lane].push(feature);
      }
    }
  }
  return next;
}
