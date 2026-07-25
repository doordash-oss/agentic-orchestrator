import type { FeatureSnapshot } from '../../../shared/ipc';
import type { AftercareCycleId } from './aftercareModel';

export type PostImplementationMode =
  | { kind: 'regular' }
  | { kind: 'aftercare' }
  | { kind: 'cycle'; cycle: CyclePresentation; failed: boolean };

export type AftercareActionId = 'publish' | AftercareCycleId;

export interface AftercareAction {
  id: AftercareActionId;
  label: string;
  title: string;
  description: string;
  disabledReason?: string;
}

export interface CycleStage {
  id: string;
  label: string;
  state: 'done' | 'active' | 'upcoming';
  conditional?: boolean;
}

export interface CyclePresentation {
  id: AftercareCycleId;
  count: number;
  stages: CycleStage[];
  headline: string;
  current: string;
  next?: string;
}

export interface CycleReceipt {
  id: AftercareCycleId;
  message: string;
}

const AFTERCARE_STATUSES = new Set(['CodeReady', 'Published', 'Done']);
const OWNING_CYCLE_STATUSES = new Set(['running', 'reviewing', 'need_user_input', 'failed']);
const ACTION_ORDER: AftercareActionId[] = ['publish', 'rebase', 'review-comments', 'refactor'];

export function resolvePostImplementationMode(
  snapshot: FeatureSnapshot,
  dismissedFailureId?: string,
): PostImplementationMode {
  const cycle = cyclePresentation(snapshot);
  const status = snapshot.cycle?.status;
  if (
    cycle !== null &&
    status !== undefined &&
    OWNING_CYCLE_STATUSES.has(status) &&
    !(status === 'failed' && cycleIdentity(snapshot) === dismissedFailureId)
  ) {
    return { kind: 'cycle', cycle, failed: status === 'failed' };
  }
  return AFTERCARE_STATUSES.has(snapshot.status) ? { kind: 'aftercare' } : { kind: 'regular' };
}

export function cycleIdentity(snapshot: FeatureSnapshot): string | null {
  const cycle = snapshot.cycle;
  if (cycle?.type === undefined) return null;
  return `${cycle.type}:${cycle.count ?? 1}:${cycle.status ?? ''}`;
}

export function aftercareActions(snapshot: FeatureSnapshot): AftercareAction[] {
  return ACTION_ORDER.flatMap((id) => {
    const action = snapshot.actions.find((candidate) => candidate.id === id);
    if (action?.enabled !== true) return [];
    return [aftercareAction(id, snapshot.repos.length)];
  });
}

export function cyclePresentation(snapshot: FeatureSnapshot): CyclePresentation | null {
  const cycle = snapshot.cycle;
  if (!isAftercareCycleId(cycle?.type)) return null;
  const count = cycle?.count ?? 1;
  const phase = normalizedCyclePhase(cycle.type, cycle.phase);
  const definitions = cycleStages(cycle.type);
  const activeIndex = Math.max(
    0,
    definitions.findIndex((stage) => stage.id === phase),
  );
  const stages = definitions.map<CycleStage>((stage, index) => ({
    ...stage,
    state: index < activeIndex ? 'done' : index === activeIndex ? 'active' : 'upcoming',
  }));
  const active = stages[activeIndex];
  const next = stages.slice(activeIndex + 1).find((stage) => !stage.conditional);
  const needsInput = cycle.status === 'need_user_input' || snapshot.status === 'NeedUserInput';
  const failed = cycle.status === 'failed';
  return {
    id: cycle.type,
    count,
    stages,
    headline: failed
      ? `${cycleName(cycle.type)} cycle needs attention`
      : needsInput
        ? 'Agent is waiting for your input'
        : `${cycleName(cycle.type)} cycle in progress`,
    current: needsInput ? 'Waiting for input' : (active?.label ?? 'Working'),
    ...(next === undefined ? {} : { next: next.label }),
  };
}

function aftercareAction(id: AftercareActionId, repoCount: number): AftercareAction {
  const scope = `${repoCount} ${repoCount === 1 ? 'repository' : 'repositories'}`;
  switch (id) {
    case 'publish':
      return {
        id,
        label: 'Prepare publish',
        title: 'Publish this feature',
        description: 'Run the existing publication checks and put the completed work into service.',
      };
    case 'rebase':
      return {
        id,
        label: 'Prepare rebase',
        title: 'Bring branches up to date',
        description: `Inspect and rebase ${scope} against their target branches.`,
      };
    case 'review-comments':
      return {
        id,
        label: 'Check comments',
        title: 'Address review feedback',
        description: 'Fetch open PR feedback and hand it to one focused agent session.',
      };
    case 'refactor':
      return {
        id,
        label: 'Plan refactor',
        title: 'Start another focused pass',
        description: 'Describe the improvement, resolve its scope, and run it as a separate cycle.',
      };
  }
}

function cycleStages(id: AftercareCycleId): Array<Omit<CycleStage, 'state'>> {
  switch (id) {
    case 'rebase':
      return [
        { id: 'inspect_rebase', label: 'Inspect & rebase' },
        { id: 'resolve_conflicts', label: 'Resolve conflicts', conditional: true },
        { id: 'final_review', label: 'Final review' },
        { id: 'publish', label: 'Publish' },
      ];
    case 'review-comments':
      return [
        { id: 'comments_ready', label: 'Comments ready' },
        { id: 'address_validate', label: 'Address & validate' },
        { id: 'push_reply', label: 'Push & reply' },
      ];
    case 'refactor':
      return [
        { id: 'plan_refactor', label: 'Plan refactor' },
        { id: 'implement_validate', label: 'Implement & validate' },
        { id: 'deliver', label: 'Deliver' },
      ];
  }
}

function normalizedCyclePhase(id: AftercareCycleId, phase?: string): string {
  if (phase !== undefined && cycleStages(id).some((stage) => stage.id === phase)) return phase;
  switch (id) {
    case 'rebase':
      return 'inspect_rebase';
    case 'review-comments':
      return 'address_validate';
    case 'refactor':
      return 'implement_validate';
  }
}

function isAftercareCycleId(value?: string): value is AftercareCycleId {
  return value === 'rebase' || value === 'review-comments' || value === 'refactor';
}

function cycleName(id: AftercareCycleId): string {
  switch (id) {
    case 'rebase':
      return 'Rebase';
    case 'review-comments':
      return 'Review comments';
    case 'refactor':
      return 'Refactor';
  }
}
