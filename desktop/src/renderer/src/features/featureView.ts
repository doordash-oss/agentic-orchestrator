/**
 * Pure presentation logic for the feature creation flow and cockpit. All
 * inputs are the strict renderer-facing views; nothing here talks to the
 * preload API or stores state.
 */
import type { FeatureSnapshot, FeatureSetupView } from '../../../shared/ipc';

export interface SpineStage {
  id: string;
  label: string;
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
  const label = snapshot.currentPhase === 'Final Review' ? 'Review' : snapshot.currentPhase;
  const index = stages.findIndex((stage) => stage.label === label);
  return index >= 0 ? index : 0;
}

export function spineTone(snapshot: FeatureSnapshot): 'progress' | 'error' {
  return snapshot.setup?.status === 'failed' || snapshot.status === 'Failed' ? 'error' : 'progress';
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
  if (snapshot.status !== 'Created') {
    return false;
  }
  if (snapshot.setup !== undefined && snapshot.setup.status !== 'done') {
    return false;
  }
  return actionById(snapshot, 'start')?.enabled === true;
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
