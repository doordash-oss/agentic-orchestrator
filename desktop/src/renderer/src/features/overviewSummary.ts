/**
 * Pure headline/sub-line copy for the Overview pane's masthead. Both
 * functions read strictly from data the read model already carries — a
 * missing fact (no attention timestamp, no run cost) is always omitted from
 * the copy rather than replaced with a placeholder or an invented number.
 */
import type { AttentionItem, FeatureSnapshot } from '../../../shared/ipc';
import { formatDuration, formatElapsed } from './featureView';
import type { Lane, LaneCounts, LaneGroups } from './laneClassification';

const EMPTY_WORKSPACE_HEADLINE = 'Turn a goal into a supervised run.';
const EMPTY_WORKSPACE_SUBLINE =
  'Define the work, choose its repositories, set the pipeline, then review the exact run contract before anything is created.';

/**
 * The Overview masthead's headline, computed from the same lane counts the
 * sidebar renders — so the number in the headline always matches the
 * sidebar's lane counts for the same snapshot set.
 */
export function overviewHeadline(counts: LaneCounts, totalFeatures: number): string {
  if (totalFeatures === 0) return EMPTY_WORKSPACE_HEADLINE;
  const running = counts.running;
  const waiting = counts.waiting;
  if (running > 0 && waiting > 0) {
    return capitalize(
      `${spellNumber(running)} run${running === 1 ? '' : 's'} in flight, ${spellNumber(waiting)} waiting on you`,
    );
  }
  if (waiting > 0) {
    return capitalize(`${spellNumber(waiting)} feature${waiting === 1 ? '' : 's'} waiting on you`);
  }
  if (running > 0) {
    return capitalize(`${spellNumber(running)} run${running === 1 ? '' : 's'} in flight`);
  }
  return 'Nothing needs you right now';
}

function capitalize(sentence: string): string {
  return sentence.charAt(0).toUpperCase() + sentence.slice(1);
}

const NUMBER_WORDS = [
  'zero',
  'one',
  'two',
  'three',
  'four',
  'five',
  'six',
  'seven',
  'eight',
  'nine',
  'ten',
];

function spellNumber(n: number): string {
  return n >= 0 && n < NUMBER_WORDS.length ? NUMBER_WORDS[n]! : String(n);
}

function oldestByCreatedAt(features: readonly FeatureSnapshot[]): FeatureSnapshot | undefined {
  return features.reduce<FeatureSnapshot | undefined>((oldest, feature) => {
    if (oldest === undefined) return feature;
    return Date.parse(feature.createdAt) < Date.parse(oldest.createdAt) ? feature : oldest;
  }, undefined);
}

/** The oldest pending attention item's waiting duration, when one exists. */
function oldestWaitingClause(attentionItems: readonly AttentionItem[]): string | undefined {
  let oldestSince: string | undefined;
  for (const item of attentionItems) {
    if (item.kind === 'recovery') continue;
    const since = item.waitingSince;
    if (since === undefined || since === '') continue;
    const parsed = Date.parse(since);
    if (!Number.isFinite(parsed)) continue;
    if (oldestSince === undefined || parsed < Date.parse(oldestSince)) oldestSince = since;
  }
  if (oldestSince === undefined) return undefined;
  const elapsedSeconds = (Date.now() - Date.parse(oldestSince)) / 1000;
  if (!Number.isFinite(elapsedSeconds) || elapsedSeconds < 0) return undefined;
  return `The oldest has been waiting ${formatDuration(elapsedSeconds)}.`;
}

/** "Nothing has stalled", extended with the oldest run's elapsed time and cost when known. */
function runningClause(runningFeatures: readonly FeatureSnapshot[]): string {
  const oldest = oldestByCreatedAt(runningFeatures);
  const elapsed = oldest === undefined ? null : formatElapsed(oldest);
  const cost = oldest?.activeChild?.cost.totalUsd;
  let clause = 'Nothing has stalled.';
  if (elapsed !== null) {
    clause += ` The oldest run has been going ${elapsed}`;
    if (cost !== undefined) clause += ` and has cost $${cost.toFixed(2)}`;
    clause += '.';
  }
  return clause;
}

const RESTING_LANES: readonly Lane[] = ['at-rest', 'published', 'done'];
const RESTING_LANE_LABEL: Readonly<Record<Lane, string>> = {
  waiting: 'waiting on you',
  running: 'running',
  'at-rest': 'at rest',
  published: 'published',
  done: 'done',
};

/** Counts of every non-empty resting lane, e.g. "Three features at rest, five published." */
function restingLanesClause(laneGroups: LaneGroups): string | undefined {
  const parts: string[] = [];
  for (const lane of RESTING_LANES) {
    const count = laneGroups[lane].length;
    if (count === 0) continue;
    // Only the first clause names the noun ("features") explicitly; later
    // clauses read as a continuation of the same list, e.g.
    // "Three features at rest, five published."
    const noun = parts.length === 0 ? `feature${count === 1 ? '' : 's'} ` : '';
    parts.push(`${spellNumber(count)} ${noun}${RESTING_LANE_LABEL[lane]}`);
  }
  if (parts.length === 0) return undefined;
  return capitalize(`${parts.join(', ')}.`);
}

/**
 * The Overview masthead's sub-line, in priority order: the oldest pending
 * attention item's wait, then the oldest running run's elapsed time/cost,
 * then resting-lane counts, then (for an empty workspace) today's existing
 * empty-state description. Each tier is only used when it has real data to
 * report — a tier with nothing usable falls through to the next rather than
 * inventing a fact.
 */
export function overviewSubline(
  laneGroups: LaneGroups,
  attentionItems: readonly AttentionItem[],
  totalFeatures: number,
): string | undefined {
  if (totalFeatures === 0) return EMPTY_WORKSPACE_SUBLINE;
  if (laneGroups.waiting.length > 0) {
    const clause = oldestWaitingClause(attentionItems);
    if (clause !== undefined) return clause;
  }
  if (laneGroups.running.length > 0) {
    return runningClause(laneGroups.running);
  }
  return restingLanesClause(laneGroups);
}
