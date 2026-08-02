/**
 * Pure presentation model for the refactor pass workspace. Everything here is
 * derived from the parent snapshot, the child snapshot, and the server action
 * catalogue — the custody strip, the pass state sentence, and the contextual
 * action set never invent availability the server did not grant.
 */
import {
  isPendingReviewStatus,
  type FeatureSnapshot,
  type RelationshipChildView,
} from '../../../../shared/ipc';
import { actionById, displayStatusLabel, isReadyToStart } from '../featureView';

export type PassStateId =
  | 'setup'
  | 'setup-failed'
  | 'ready'
  | 'working'
  | 'review'
  | 'input'
  | 'interrupted'
  | 'failed'
  | 'integrating'
  | 'integration-attention';

export interface PassState {
  id: PassStateId;
  /** One sentence under the pass name: what is happening and what comes next. */
  sentence: string;
  tone: 'quiet' | 'live' | 'attention' | 'danger';
}

const ACTIVE_TRANSACTION_PHASES = new Set(['preparing', 'prepared', 'applying', 'applied']);

export function passState(child: FeatureSnapshot): PassState {
  const transactionPhase = child.transaction?.phase;
  if (transactionPhase === 'attention') {
    return {
      id: 'integration-attention',
      sentence: 'Integration needs attention. Review the repository details below.',
      tone: 'attention',
    };
  }
  if (transactionPhase !== undefined && ACTIVE_TRANSACTION_PHASES.has(transactionPhase)) {
    return {
      id: 'integrating',
      sentence: 'Final review approved. Merging the pass back into the parent branches.',
      tone: 'live',
    };
  }
  if (child.setup?.status === 'failed') {
    return {
      id: 'setup-failed',
      sentence: 'Worktree setup failed. Retry setup to continue.',
      tone: 'danger',
    };
  }
  if (child.setupComplete === false || child.status === 'SettingUpWorktrees') {
    return {
      id: 'setup',
      sentence: 'Preparing worktrees. Start unlocks when setup completes.',
      tone: 'live',
    };
  }
  if (child.status === 'Failed') {
    return {
      id: 'failed',
      sentence: child.failure?.message ?? 'The pass stopped on a failure.',
      tone: 'danger',
    };
  }
  if (child.status === 'Interrupted') {
    return {
      id: 'interrupted',
      sentence: 'The pass is paused. Resume to continue where it stopped.',
      tone: 'attention',
    };
  }
  if (child.status === 'NeedUserInput') {
    return { id: 'input', sentence: 'The agent is waiting for your input.', tone: 'attention' };
  }
  if (isPendingReviewStatus(child.status)) {
    return {
      id: 'review',
      sentence: 'A review gate is waiting for your decision.',
      tone: 'attention',
    };
  }
  if (isReadyToStart(child)) {
    const repoCount = child.repos.length;
    return {
      id: 'ready',
      sentence: `Ready to start. The pass runs its own pipeline across ${
        repoCount === 1 ? 'the inherited repository' : `all ${repoCount} inherited repositories`
      }.`,
      tone: 'quiet',
    };
  }
  return { id: 'working', sentence: `${displayStatusLabel(child.status)}.`, tone: 'live' };
}

export type StationState = 'locked' | 'pending' | 'live' | 'attention' | 'done';

export interface CustodyStation {
  id: 'parent' | 'pass' | 'integration';
  eyebrow: string;
  title: string;
  detail: string;
  state: StationState;
}

/** Short label for the pass station; the long sentence lives in the header. */
function passStationDetail(state: PassState, child: FeatureSnapshot): string {
  switch (state.id) {
    case 'setup':
      return 'Setting up worktrees';
    case 'setup-failed':
      return 'Setup failed';
    case 'ready':
      return 'Ready to start';
    case 'review':
      return 'Review waiting';
    case 'input':
      return 'Input needed';
    case 'interrupted':
      return 'Paused';
    case 'failed':
      return 'Failed';
    case 'integrating':
    case 'integration-attention':
      return 'Review passed';
    case 'working':
      return displayStatusLabel(child.status);
  }
}

function integrationStation(child: FeatureSnapshot | null): Omit<CustodyStation, 'id' | 'eyebrow'> {
  const phase = child?.transaction?.phase;
  if (phase === 'merged') {
    return { title: 'Integration', detail: 'Merged into the parent', state: 'done' };
  }
  if (phase === 'attention') {
    return { title: 'Integration', detail: 'Needs attention', state: 'attention' };
  }
  if (phase === 'rolling_back' || phase === 'rolled_back') {
    return { title: 'Integration', detail: displayPhase(phase), state: 'attention' };
  }
  if (phase !== undefined && ACTIVE_TRANSACTION_PHASES.has(phase)) {
    return { title: 'Integration', detail: displayPhase(phase), state: 'live' };
  }
  return { title: 'Integration', detail: 'After final review approval', state: 'pending' };
}

function displayPhase(phase: string): string {
  const normalized = phase.replace(/[_-]+/g, ' ').trim();
  return normalized === '' ? phase : normalized.charAt(0).toUpperCase() + normalized.slice(1);
}

/**
 * The custody strip: work leaves the locked parent, runs inside the pass, and
 * merges back through integration. Every station reflects live server state.
 */
export function custodyStations(
  parent: FeatureSnapshot,
  child: FeatureSnapshot | null,
  view: RelationshipChildView,
): [CustodyStation, CustodyStation, CustodyStation] {
  const state = child === null ? null : passState(child);
  const attention =
    view.attention.length > 0 || state?.tone === 'attention' || state?.tone === 'danger';
  return [
    {
      id: 'parent',
      eyebrow: 'Parent',
      title: parent.name,
      detail: `${displayStatusLabel(parent.status)} · locked while the pass runs`,
      state: 'locked',
    },
    {
      id: 'pass',
      eyebrow: 'Refactor pass',
      title: view.name,
      detail:
        state === null || child === null ? view.displayState : passStationDetail(state, child),
      state: attention
        ? 'attention'
        : state === null || state.tone === 'quiet'
          ? 'pending'
          : state.id === 'integrating'
            ? 'done'
            : 'live',
    },
    { id: 'integration', eyebrow: 'Merge back', ...integrationStation(child) },
  ];
}

export interface PassAction {
  id: 'start' | 'resume' | 'retry' | 'pause-stop' | 'restart';
  label: string;
  kind: 'primary' | 'secondary';
}

const PRIMARY_PRIORITY = ['start', 'resume', 'retry', 'pause-stop'] as const;

/**
 * The contextual verbs the pass invites right now: one primary from the
 * catalogue's enabled set, Restart as a quiet secondary when the server also
 * offers it. Verbs the server disabled never render as dead buttons — the
 * state sentence carries the why instead.
 */
export function passActions(child: FeatureSnapshot): PassAction[] {
  const enabled = (id: PassAction['id']): boolean => actionById(child, id)?.enabled === true;
  const actions: PassAction[] = [];
  const primary = PRIMARY_PRIORITY.find(enabled);
  if (primary !== undefined) {
    actions.push({ id: primary, label: passActionLabel(primary, child), kind: 'primary' });
  }
  if (enabled('restart')) {
    actions.push({ id: 'restart', label: 'Restart', kind: 'secondary' });
  }
  return actions;
}

function passActionLabel(id: PassAction['id'], child: FeatureSnapshot): string {
  switch (id) {
    case 'start':
      return 'Start pass';
    case 'resume':
      return 'Resume';
    case 'retry':
      return child.setup?.status === 'failed' ? 'Retry setup' : 'Retry';
    case 'pause-stop':
      return 'Stop';
    case 'restart':
      return 'Restart';
  }
}

/** The parent action bar chip while a refactor pass is active. */
export function refactoringStatusChip(view: RelationshipChildView): {
  label: string;
  tone: 'info' | 'attention';
} {
  const attention = view.attention.length > 0 || view.integrationState === 'attention';
  return attention
    ? { label: 'Refactoring — needs attention', tone: 'attention' }
    : { label: 'Refactoring', tone: 'info' };
}
