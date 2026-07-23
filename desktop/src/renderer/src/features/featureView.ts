/**
 * Pure presentation logic for the feature creation flow and cockpit. All
 * inputs are the strict renderer-facing views; nothing here talks to the
 * preload API or stores state.
 */
import type { FeatureSnapshot, FeatureSetupView } from '../../../shared/ipc';

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

const ACTIVE_STATUSES = new Set([
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
  { id: 'in-progress', label: 'Running now' },
  { id: 'published', label: 'Published' },
  { id: 'done', label: 'Done' },
];

export function dashboardGroupId(snapshot: FeatureSnapshot): DashboardGroupId {
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
 * Human elapsed time for the queue: seconds under a minute, "Mm SSs" under an
 * hour, then "Hh Mm". Returns null when there is no run time to show yet.
 */
export function formatElapsed(snapshot: FeatureSnapshot): string | null {
  const total = snapshot.timing?.totalSeconds ?? 0;
  if (total <= 0) return null;
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
