/**
 * Pure presentation logic for the feature creation flow and cockpit. All
 * inputs are the strict renderer-facing views; nothing here talks to the
 * preload API or stores state.
 */
import type { FeatureSnapshot, FeatureSetupView, ModelCatalogue } from '../../../shared/ipc';

export type DashboardBucket = 'intervention' | 'active' | 'startable' | 'inactive';
export type DashboardTone = 'danger' | 'attention' | 'active' | 'ready' | 'quiet';
export type DashboardGroupId = 'in-progress' | 'published' | 'done';

export interface DashboardState {
  bucket: DashboardBucket;
  label: string;
  tone: DashboardTone;
}

export interface DashboardGroup {
  id: DashboardGroupId;
  label: string;
  features: FeatureSnapshot[];
}

const STATUS_LABELS: Readonly<Record<string, string>> = {
  BuildingKB: 'Building knowledge base',
  CodeReady: 'Code ready',
  NeedUserInput: 'Input needed',
  SettingUpWorktrees: 'Setting up worktrees',
};

/** User-facing copy for the server's stable enum spelling. */
export function displayStatusLabel(status: string): string {
  const known = STATUS_LABELS[status];
  if (known !== undefined) return known;

  const words = status
    .replace(/([a-z\d])([A-Z])/g, '$1 $2')
    .replace(/([A-Z]+)([A-Z][a-z])/g, '$1 $2');
  return words.length === 0 ? status : words[0] + words.slice(1).toLocaleLowerCase();
}

/** Replaces stable server status tokens when they appear inside explanatory copy. */
export function displayFeatureMessage(message: string): string {
  return Object.entries(STATUS_LABELS).reduce(
    (copy, [status, label]) => copy.replaceAll(status, label),
    message,
  );
}

/** Top-level statuses that represent actively executing phase work. */
export const ACTIVE_STATUSES = new Set([
  'SettingUpWorktrees',
  'BuildingKB',
  'Inquiring',
  'Researching',
  'Designing',
  'Planning',
  'Implementing',
  'Reviewing',
  'FinalReviewing',
]);

const BUCKET_ORDER: Record<DashboardBucket, number> = {
  intervention: 0,
  active: 1,
  startable: 2,
  inactive: 3,
};

/** Human state and priority derived from server status and catalogue only. */
export function dashboardState(snapshot: FeatureSnapshot): DashboardState {
  const child = snapshot.activeChild;
  if (child !== undefined) {
    // A parent with an active refactor pass is in progress even though its
    // stored status stays Published/CodeReady while the pass is labeled "Refactoring".
    if (child.attention.length > 0 || child.integrationState === 'attention') {
      return { bucket: 'intervention', label: 'Refactoring — needs attention', tone: 'attention' };
    }
    if (child.status === 'Failed') {
      return { bucket: 'intervention', label: 'Refactoring — pass failed', tone: 'danger' };
    }
    // A pass launched without auto-start hasn't run anything yet; a live
    // "Refactoring" badge would claim work that never began.
    if (child.status === 'Created') {
      return { bucket: 'startable', label: 'Pass ready to start', tone: 'ready' };
    }
    return { bucket: 'active', label: 'Refactoring', tone: 'active' };
  }
  if (snapshot.status === 'Failed') {
    return { bucket: 'intervention', label: 'Failed', tone: 'danger' };
  }
  if (snapshot.status === 'Interrupted') {
    return { bucket: 'intervention', label: 'Interrupted', tone: 'attention' };
  }
  if (snapshot.status.endsWith('NeedsReview')) {
    return { bucket: 'intervention', label: 'Review needed', tone: 'attention' };
  }
  if (snapshot.status === 'NeedUserInput') {
    return { bucket: 'intervention', label: 'Input needed', tone: 'attention' };
  }
  if (ACTIVE_STATUSES.has(snapshot.status) || snapshot.setup?.status === 'running') {
    return { bucket: 'active', label: 'Active', tone: 'active' };
  }
  if (actionById(snapshot, 'start')?.enabled === true) {
    return { bucket: 'startable', label: 'Ready to start', tone: 'ready' };
  }
  return { bucket: 'inactive', label: 'Inactive', tone: 'quiet' };
}

/** Stable deterministic sort; input order is never mutated. */
export function orderDashboardFeatures(features: readonly FeatureSnapshot[]): FeatureSnapshot[] {
  return features
    .map((feature, index) => ({ feature, index }))
    .sort((left, right) => {
      const bucketDelta =
        BUCKET_ORDER[dashboardState(left.feature).bucket] -
        BUCKET_ORDER[dashboardState(right.feature).bucket];
      if (bucketDelta !== 0) {
        return bucketDelta;
      }
      const createdDelta = Date.parse(right.feature.createdAt) - Date.parse(left.feature.createdAt);
      if (Number.isFinite(createdDelta) && createdDelta !== 0) {
        return createdDelta;
      }
      const idDelta = left.feature.id.localeCompare(right.feature.id);
      return idDelta === 0 ? left.index - right.index : idDelta;
    })
    .map(({ feature }) => feature);
}

const DASHBOARD_GROUPS: readonly { id: DashboardGroupId; label: string }[] = [
  { id: 'in-progress', label: 'In progress' },
  { id: 'published', label: 'Published' },
  { id: 'done', label: 'Done' },
];

export function dashboardGroupId(snapshot: FeatureSnapshot): DashboardGroupId {
  if (snapshot.activeChild !== undefined) return 'in-progress';
  if (snapshot.status === 'Done') return 'done';
  if (snapshot.status === 'Published') return 'published';
  return 'in-progress';
}

/** Groups already-ordered dashboard features into the home screen sections. */
export function groupDashboardFeatures(features: readonly FeatureSnapshot[]): DashboardGroup[] {
  return DASHBOARD_GROUPS.map((group) => ({
    ...group,
    features: features.filter((feature) => dashboardGroupId(feature) === group.id),
  })).filter((group) => group.features.length > 0);
}

export interface SpineStage {
  id: string;
  label: string;
}

/**
 * Rail labels stay full when a pipeline has few stages, but the full profile
 * (nine stages) can't fit spelled-out labels in a card, so they compact:
 * multi-word labels to initials, single words to their opening letters.
 */
export function railStageLabel(label: string, totalStages: number): string {
  if (totalStages <= 5) return label;
  const words = label.trim().split(/\s+/);
  if (words.length > 1) {
    return words.map((word) => word.charAt(0)).join('');
  }
  return label.length <= 4 ? label : label.slice(0, 3);
}

/** Phase order per pipeline profile (internal/feature/pipeline.go). */
const MEDIUM_PHASES = ['Plan', 'Implement', 'Review', 'Publish'] as const;
const FULL_PHASES = [
  'Knowledge Base',
  'Inquire',
  'Research',
  'Design',
  'Plan',
  'Implement',
  'Review',
  'Publish',
] as const;

/** The cockpit spine: durable setup first, then the profile's phases. */
export function spineStages(pipeline: string | undefined): SpineStage[] {
  const phases = pipeline === 'medium' ? MEDIUM_PHASES : FULL_PHASES;
  return [
    { id: 'setup', label: 'Setup' },
    ...phases.map((label) => ({ id: label.toLowerCase().replace(/\s+/g, '-'), label })),
  ];
}

/**
 * Renders known storage phase ids with the same vocabulary as the cockpit
 * spine. Unknown values are retained verbatim: this helper also receives
 * artifact metadata, where title-casing an identifier would corrupt it.
 */
export function displayPhaseLabel(phase: string): string {
  const normalized = phase
    .trim()
    .toLocaleLowerCase()
    .replace(/[_\s]+/g, '-');
  return spineStages('large').find((stage) => stage.id === normalized)?.label ?? phase;
}

/**
 * Where the needle points: on Setup until durable setup completes, on the
 * next upcoming phase once the feature is Created and startable, and on the
 * server-reported current phase after later-phase work begins.
 */
export function spineActiveIndex(snapshot: FeatureSnapshot, stages: SpineStage[]): number {
  const setupIncomplete = snapshot.setup !== undefined && snapshot.setup.status !== 'done';
  if (setupIncomplete || snapshot.status === 'SettingUpWorktrees') {
    return 0;
  }
  if (snapshot.status === 'Created') {
    return Math.min(1, stages.length - 1);
  }
  return spineActiveIndexForPhase(snapshot.currentPhase, stages);
}

/** Maps a server phase label to the shared cockpit spine. */
export function spineActiveIndexForPhase(
  currentPhase: string,
  stages: readonly SpineStage[],
): number {
  const label = currentPhase === 'Final Review' ? 'Review' : currentPhase;
  const normalizedLabel = label.trim().toLocaleLowerCase();
  const index = stages.findIndex(
    (stage) => stage.label.trim().toLocaleLowerCase() === normalizedLabel,
  );
  return index >= 0 ? index : 0;
}

/** Phase a working/reviewing status runs in, for spine placement. */
const STATUS_PHASE_LABELS: Readonly<Record<string, string>> = {
  BuildingKB: 'Knowledge Base',
  Inquiring: 'Inquire',
  Inquiry: 'Inquire',
  Researching: 'Research',
  Research: 'Research',
  Designing: 'Design',
  Design: 'Design',
  Planning: 'Plan',
  Plan: 'Plan',
  PlanReady: 'Plan',
  Roadmap: 'Plan',
  PhasePlan: 'Plan',
  Implementing: 'Implement',
  Implementation: 'Implement',
  Reviewing: 'Review',
  FinalReviewing: 'Review',
  FinalReview: 'Review',
  ReviewPassed: 'Publish',
};

/**
 * Spine position for a relationship child known only by its status string
 * (the summary view carries no current phase). Returns -1 for a Created pass
 * that hasn't started — the rail renders with every stop upcoming and no
 * needle. Returns null when the status does not name a phase (paused, failed,
 * waiting on input) — an approximate needle there would lie.
 */
export function childStatusSpineIndex(
  status: string,
  stages: readonly SpineStage[],
): number | null {
  if (status === 'SettingUpWorktrees') return 0;
  if (status === 'Created') return -1;
  const key = status.endsWith('NeedsReview') ? status.slice(0, -'NeedsReview'.length) : status;
  const label = STATUS_PHASE_LABELS[key];
  if (label === undefined) return null;
  const index = stages.findIndex((stage) => stage.label === label);
  return index >= 0 ? index : null;
}

/**
 * The run finished and the feature is resting (awaiting publish/completion).
 * The server keeps current_phase at the last worked phase in these statuses,
 * so position alone would falsely read as "still working". Checkpoint pauses
 * (PlanReady, NeedUserInput, *NeedsReview, Interrupted) are NOT at rest —
 * those runs are genuinely parked at their phase.
 */
export function isRunAtRest(status: string): boolean {
  return status === 'CodeReady' || status === 'Published' || status === 'Done';
}

export function spineTone(snapshot: FeatureSnapshot): 'progress' | 'error' {
  return snapshot.setup?.status === 'failed' || snapshot.status === 'Failed' ? 'error' : 'progress';
}

/**
 * Human run duration: seconds under a minute, "Mm SSs" under an hour, then
 * "Hh MMm". Negative or fractional inputs are floored to whole seconds.
 */
export function formatDuration(totalSeconds: number): string {
  const total = Math.max(0, Math.floor(totalSeconds));
  if (total < 60) return `${total}s`;
  if (total < 3600) {
    const minutes = Math.floor(total / 60);
    const seconds = total % 60;
    return `${minutes}m ${seconds.toString().padStart(2, '0')}s`;
  }
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  return `${hours}h ${minutes.toString().padStart(2, '0')}m`;
}

/**
 * Reads a per-phase metric (duration seconds or USD) for the named phase from a
 * run's `by_phase` map. Prefers an active roadmap accounting key when supplied,
 * then matches the phase key exactly or case-insensitively, and finally maps
 * "Final Review" to the "Review" phase it is recorded under. Returns undefined
 * when the phase has no recorded value.
 */
export function phaseMetric(
  byPhase: Readonly<Record<string, number>> | undefined,
  phase: string,
  roadmapPhase?: number,
): number | undefined {
  if (byPhase === undefined) return undefined;
  const target = phase.trim().toLocaleLowerCase();
  if (roadmapPhase !== undefined && roadmapPhase > 0) {
    const suffix =
      target === 'plan' || target === 'planning'
        ? 'plan'
        : ['implement', 'implementation', 'review'].includes(target)
          ? 'impl'
          : null;
    if (suffix !== null) {
      const roadmapKey = `phase-${roadmapPhase}-${suffix}`;
      if (roadmapKey in byPhase) return byPhase[roadmapKey];
    }
  }
  if (phase in byPhase) return byPhase[phase];
  for (const [key, value] of Object.entries(byPhase)) {
    if (key.trim().toLocaleLowerCase() === target) return value;
  }
  if (target === 'final review') return phaseMetric(byPhase, 'Review');
  return undefined;
}

export function displayModelName(model: string, catalogue: ModelCatalogue | null): string {
  for (const [provider, models] of Object.entries(catalogue?.providerModels ?? {})) {
    const match = models.find((entry) => model === entry.id || model === `${provider}:${entry.id}`);
    if (match?.displayName !== undefined && match.displayName !== '') return match.displayName;
  }

  let canonical = model;
  for (const provider of Object.keys(catalogue?.providerModels ?? {})) {
    const prefix = `${provider}:`;
    if (model.startsWith(prefix)) {
      canonical = model.slice(prefix.length);
      break;
    }
  }
  if (canonical === model) {
    const providerSeparator = model.indexOf(':');
    const firstPathSeparator = model.indexOf('/');
    if (providerSeparator > 0 && firstPathSeparator > providerSeparator) {
      canonical = model.slice(providerSeparator + 1);
    }
  }
  return canonical.split('/').filter(Boolean).at(-1) ?? canonical;
}

/** Elapsed time for the queue card; null when there is no run time to show yet. */
export function formatElapsed(snapshot: FeatureSnapshot): string | null {
  const total = snapshot.timing?.totalSeconds ?? 0;
  if (total <= 0) return null;
  return formatDuration(total);
}

export interface SetupProgress {
  done: number;
  total: number;
}

export function setupProgress(setup: FeatureSetupView): SetupProgress {
  return {
    done: setup.tasks.filter((task) => task.status === 'done').length,
    total: setup.tasks.length,
  };
}

export function actionById(
  snapshot: FeatureSnapshot,
  id: string,
): FeatureSnapshot['actions'][number] | undefined {
  return snapshot.actions.find((action) => action.id === id);
}

/**
 * "Ready to start": durable setup succeeded and the authoritative catalogue
 * enables the (later-phase) start action. Purely derived — never stored.
 */
export function isReadyToStart(snapshot: FeatureSnapshot): boolean {
  return actionById(snapshot, 'start')?.enabled === true;
}

/** A timeline exists once the feature has moved beyond creation and setup. */
export function showsRun(snapshot: FeatureSnapshot): boolean {
  return !['Created', 'SettingUpWorktrees'].includes(snapshot.status);
}

/** The feature branch, surfaced from the server-owned setup task data. */
export function featureBranch(snapshot: FeatureSnapshot): string | null {
  for (const task of snapshot.setup?.tasks ?? []) {
    if (task.branch !== undefined && task.branch !== '') {
      return task.branch;
    }
  }
  return null;
}

export type CreationErrorField = 'name' | 'repos' | 'form';

/** Routes a structured server rejection to the control that owns it. */
export function fieldForCreationError(error: {
  code: string;
  message: string;
}): CreationErrorField {
  if (error.code === 'not_ready') {
    return 'form';
  }
  if (error.code === 'bad_request' && /\bname\b/i.test(error.message)) {
    return 'name';
  }
  if (/\brepo(sitor(y|ies))?s?\b/i.test(error.message)) {
    return 'repos';
  }
  return 'form';
}
