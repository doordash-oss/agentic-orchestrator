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
 * Pure presentation logic for the unified phase rail: segment states, the
 * hold (waiting/paused) classification, and the right-aligned Elapsed / Cost
 * / Context trio (substituted by Waiting/Paused while held). Nothing here
 * renders anything — the rail component, archive mode, and the sidebar
 * sub-lines all compute from this module so they can never disagree.
 */
import { isSyntheticHelpItem, type AttentionItem, type FeatureSnapshot } from '../../../shared/ipc';
import {
  ACTIVE_STATUSES,
  formatDuration,
  isRunAtRest,
  spineActiveIndex,
  spineStages,
  type SpineStage,
} from './featureView';

// --- Segment states ---------------------------------------------------------

export type RailSegmentState = 'completed' | 'current' | 'upcoming';

export interface RailSegment {
  id: string;
  /** Rail-scale label, already passed through `railSegmentLabel`. */
  label: string;
  state: RailSegmentState;
  /** True only on the current segment while a hold is open. */
  held: boolean;
  /** Carries the segment's state (and hold) for accessible names. */
  accessibleName: string;
}

/**
 * Per-segment completed/current/upcoming state for Setup plus the profile's
 * phases (Final Review maps onto the Review segment via `spineActiveIndex`
 * — it never becomes a tenth segment). A run at rest (CodeReady/Published/
 * Done) reads its current position as completed, mirroring the deleted
 * ladder's rule: the server keeps `currentPhase` at the last worked phase in
 * those statuses, so treating it as "current" would misreport a finished run
 * as still in flight.
 */
export function railSegments(snapshot: FeatureSnapshot, hold: RailHold | null): RailSegment[] {
  const stages = spineStages(snapshot.pipeline);
  const activeIndex = spineActiveIndex(snapshot, stages);
  const atRest = isRunAtRest(snapshot.status);
  return buildRailSegments(stages, activeIndex, atRest, hold);
}

function buildRailSegments(
  stages: readonly SpineStage[],
  activeIndex: number,
  atRest: boolean,
  hold: RailHold | null,
): RailSegment[] {
  return stages.map((stage, index) => {
    const state: RailSegmentState =
      index < activeIndex || (atRest && index === activeIndex)
        ? 'completed'
        : index === activeIndex
          ? 'current'
          : 'upcoming';
    const held = state === 'current' && hold !== null;
    return {
      id: stage.id,
      label: railSegmentLabel(stage.label),
      state,
      held,
      accessibleName: accessibleSegmentName(stage.label, state, held),
    };
  });
}

/**
 * Segment states for a sealed archive run: always rendered at rest — the
 * reached segment reads completed, never current/held — regardless of the
 * status the run carried when it was sealed. Archive mode has no live
 * status to consult, only the recorded phase, so this is a distinct entry
 * point from `railSegments` rather than a variant call with a synthesized
 * snapshot.
 */
export function archiveRailSegments(
  stages: readonly SpineStage[],
  activeIndex: number,
): RailSegment[] {
  return buildRailSegments(stages, activeIndex, true, null);
}

function accessibleSegmentName(label: string, state: RailSegmentState, held: boolean): string {
  return held ? `${label}, held` : `${label}, ${state}`;
}

/**
 * Segment states for a bare, non-roadmap step list — the connection shell's
 * six-stage lifecycle and the setup wizard's variable step count. Neither
 * has an at-rest concept (the active step stays `current` even on the last
 * step) and neither ever holds, so this is `buildRailSegments` with those
 * two inputs fixed, kept as the one place both consumers get the
 * completed/current/upcoming ternary and the accessible-name format from.
 */
export function stepSegments(
  steps: readonly { id: string; label: string }[],
  activeIndex: number,
): RailSegment[] {
  return buildRailSegments(steps, activeIndex, false, null);
}

// --- Rail-scale label helper -------------------------------------------------

const RAIL_LABEL_OVERRIDES: Readonly<Record<string, string>> = {
  'Knowledge Base': 'Knowledge',
};

/**
 * The single compact-label code path left in the renderer: shortens labels
 * that read too wide at rail scale, verbatim otherwise. The caller applies
 * CSS ellipsis for any label that still overflows its segment.
 */
export function railSegmentLabel(label: string): string {
  return RAIL_LABEL_OVERRIDES[label] ?? label;
}

// --- Hold classification -----------------------------------------------------

export type HoldKind = 'waiting' | 'paused';

export interface RailHold {
  kind: HoldKind;
  /** The oldest open attention item's `waitingSince`, when one carries it. */
  waitingSince?: string;
}

/** Attention kinds that represent a human being asked something directly. */
const HUMAN_BLOCKING_KINDS = new Set<AttentionItem['kind']>(['permission', 'questions', 'help']);

/**
 * Classifies the current hold from the snapshot's status and its open
 * attention items (already filtered to this feature — see
 * `attentionOwnerFeatureId`). Precedence, per the settled mapping:
 *
 * - `Failed` is never a hold: the rail shows its error tone instead, even if
 *   a stale attention item is still open.
 * - `waiting`: the run is still executing (status in `ACTIVE_STATUSES`) and
 *   a human-blocking item (question/permission/help) is open.
 * - `paused`: the run is parked for a human — `NeedUserInput`, any
 *   `*NeedsReview` checkpoint, or `Interrupted` — regardless of whether an
 *   attention item happens to be open (a plain `Interrupted` still pauses).
 * - Anything else (at-rest, `Created`, setup-in-progress with no open item,
 *   active with no open item) is not a hold.
 */
export function classifyHold(
  status: string,
  openAttentionItems: readonly AttentionItem[],
): RailHold | null {
  if (status === 'Failed') return null;

  // Synthetic help items (an interactive session idling between turns) are
  // inline status, not a human being asked something — they neither hold the
  // rail nor feed the oldest-waiting timestamp.
  const items = openAttentionItems.filter(
    (item) => item.kind !== 'recovery' && !isSyntheticHelpItem(item),
  );

  if (ACTIVE_STATUSES.has(status)) {
    if (!items.some((item) => HUMAN_BLOCKING_KINDS.has(item.kind))) return null;
    return { kind: 'waiting', waitingSince: oldestWaitingSince(items) };
  }

  if (status === 'NeedUserInput' || status === 'Interrupted' || status.endsWith('NeedsReview')) {
    const waitingSince = oldestWaitingSince(items);
    return waitingSince === undefined ? { kind: 'paused' } : { kind: 'paused', waitingSince };
  }

  return null;
}

function oldestWaitingSince(items: readonly AttentionItem[]): string | undefined {
  let oldestValue: string | undefined;
  let oldestMs = Number.POSITIVE_INFINITY;
  for (const item of items) {
    const ms = Date.parse(item.waitingSince);
    if (!Number.isFinite(ms)) continue;
    if (ms < oldestMs) {
      oldestMs = ms;
      oldestValue = item.waitingSince;
    }
  }
  return oldestValue;
}

// --- Waiting-duration formatter ---------------------------------------------

/** One bucket of elapsed time: exactly one field is set. */
export type ElapsedBucket =
  | { unit: 'sub-minute' }
  | { unit: 'minutes'; value: number }
  | { unit: 'hours'; value: number }
  | { unit: 'days'; value: number };

/**
 * Parse-and-bucket, shared so every surface that reports "how long has this
 * been waiting" agrees on which unit a given hold falls into — only the
 * wording differs (`4h` on the rail, `4 hours ago` in the gate sheet's lede).
 * Returns null for a missing or unparseable timestamp; callers decide how to
 * word "no duration known" for their surface.
 */
export function bucketElapsedSince(value: string | undefined): ElapsedBucket | null {
  if (value === undefined) return null;
  const since = Date.parse(value);
  if (!Number.isFinite(since)) return null;
  const minutes = Math.floor(Math.max(Date.now() - since, 0) / 60_000);
  if (minutes < 1) return { unit: 'sub-minute' };
  if (minutes < 60) return { unit: 'minutes', value: minutes };
  const hours = Math.floor(minutes / 60);
  if (hours < 48) return { unit: 'hours', value: hours };
  return { unit: 'days', value: Math.floor(hours / 24) };
}

/**
 * The shared `Nm`/`Nh`/`Nd` shape, promoted out of the attention inbox so
 * both it and the rail render identical durations from one source. Returns
 * null for a missing or unparseable timestamp — callers decide how to word
 * "no duration known" for their surface.
 */
export function formatWaitingDuration(value: string | undefined): string | null {
  const bucket = bucketElapsedSince(value);
  if (bucket === null) return null;
  switch (bucket.unit) {
    case 'sub-minute':
      return '<1m';
    case 'minutes':
      return `${bucket.value}m`;
    case 'hours':
      return `${bucket.value}h`;
    case 'days':
      return `${bucket.value}d`;
  }
}

// --- Trio assembly ------------------------------------------------------------

export type TrioEntryKind = 'waiting' | 'paused' | 'elapsed' | 'cost' | 'context';

export interface TrioEntry {
  kind: TrioEntryKind;
  label: string;
  /** Display value; '' for a waiting/paused entry with no known duration. */
  value: string;
  /** True for the attention-colored hold entry; false for the plain trio. */
  attention: boolean;
}

export interface RailTrioInputs {
  /** Run-total elapsed seconds; omitted (undefined) when there is no run yet. */
  totalSeconds?: number;
  /** Run-total cost in USD; omitted when there is no run yet. */
  totalUsd?: number;
  /** Live context percentage (0-100); omitted when there is no live session. */
  contextPercentage?: number;
  hold: RailHold | null;
}

/**
 * `[Elapsed, Cost, Context]` while at rest, becoming `[Waiting|Paused,
 * Elapsed, Cost]` while held (Context drops, never Elapsed). Every entry is
 * omitted when its datum is unavailable — this never invents a placeholder
 * value.
 */
export function railTrio(inputs: RailTrioInputs): TrioEntry[] {
  const entries: TrioEntry[] = [];

  if (inputs.hold !== null) {
    entries.push({
      kind: inputs.hold.kind,
      label: inputs.hold.kind === 'waiting' ? 'Waiting' : 'Paused',
      value: formatWaitingDuration(inputs.hold.waitingSince) ?? '',
      attention: true,
    });
  }

  // `0` means "no timing recorded yet" for elapsed (a just-started run has
  // no meaningful duration to show), whereas `$0.00` is a real, displayable
  // cost — so elapsed guards on `> 0` while cost guards on `>= 0`.
  if (inputs.totalSeconds !== undefined && inputs.totalSeconds > 0) {
    entries.push({
      kind: 'elapsed',
      label: 'Elapsed',
      value: formatDuration(inputs.totalSeconds),
      attention: false,
    });
  }

  if (inputs.totalUsd !== undefined && inputs.totalUsd >= 0) {
    entries.push({
      kind: 'cost',
      label: 'Cost',
      value: `$${inputs.totalUsd.toFixed(2)}`,
      attention: false,
    });
  }

  if (
    inputs.hold === null &&
    inputs.contextPercentage !== undefined &&
    inputs.contextPercentage >= 0
  ) {
    entries.push({
      kind: 'context',
      label: 'Context',
      value: `${inputs.contextPercentage}%`,
      attention: false,
    });
  }

  return entries;
}
