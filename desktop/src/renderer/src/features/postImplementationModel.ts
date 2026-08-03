import type { CycleView, FeatureSnapshot } from '../../../shared/ipc';

/** Aftercare launch surfaces: the in-feature rebase cycle plus the refactor pass. */
export type AftercareCycleId = 'rebase' | 'refactor';

/**
 * Aftercare modal ids. Review-feedback runs as a child pass (not a repo
 * cycle) and is opened from the same modal render sites as the cycles.
 */
export type AftercareModalId = AftercareCycleId | 'review-feedback';

/**
 * In-feature post-publish cycles the server reports through `cycle`. A
 * refactor is never one of these — it runs as a separate child feature.
 */
export type RepoCycleId = 'rebase';

export type PostImplementationMode =
  | { kind: 'regular' }
  | { kind: 'aftercare' }
  | { kind: 'cycle'; cycle: CyclePresentation; failed: boolean };

export type AftercareActionId = 'publish' | 'review-feedback' | AftercareCycleId;

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
  id: RepoCycleId;
  count: number;
  stages: CycleStage[];
  headline: string;
  current: string;
  next?: string;
}

export interface CycleReceipt {
  id: RepoCycleId;
  outcome: 'completed' | 'failed' | 'stopped';
  message: string;
  detail?: string;
}

const AFTERCARE_STATUSES = new Set(['CodeReady', 'Published', 'Done']);
export const OWNING_CYCLE_STATUSES = new Set([
  'running',
  'reviewing',
  'need_user_input',
  'failed',
  'interrupted',
]);
const ACTION_ORDER: AftercareActionId[] = ['publish', 'rebase', 'refactor', 'review-feedback'];

export function resolvePostImplementationMode(
  snapshot: FeatureSnapshot,
  dismissedCycleId?: string,
): PostImplementationMode {
  const cycle = cyclePresentation(snapshot);
  const status = snapshot.cycle?.status;
  const dismissedTerminalCycle =
    (status === 'failed' || status === 'interrupted') &&
    cycleIdentity(snapshot) === dismissedCycleId;
  if (
    cycle !== null &&
    status !== undefined &&
    OWNING_CYCLE_STATUSES.has(status) &&
    !dismissedTerminalCycle
  ) {
    return { kind: 'cycle', cycle, failed: status === 'failed' };
  }
  if (dismissedTerminalCycle) return { kind: 'aftercare' };
  return AFTERCARE_STATUSES.has(snapshot.status) ? { kind: 'aftercare' } : { kind: 'regular' };
}

export function ownsPostImplementationStage(cycle?: CycleView): boolean {
  return (
    cycle?.type !== undefined &&
    cycle.status !== undefined &&
    OWNING_CYCLE_STATUSES.has(cycle.status)
  );
}

export function cycleFailureDetail(snapshot: FeatureSnapshot): string {
  return (
    snapshot.cycle?.lastError ??
    snapshot.failure?.message ??
    snapshot.repoStatus?.find((repository) => repository.lastError !== undefined)?.lastError ??
    'The runtime stopped before the cycle reached its next checkpoint.'
  );
}

export function receiptForCycleEnd(
  previousCycle: CycleView | undefined,
  snapshot: FeatureSnapshot,
): CycleReceipt | undefined {
  const id = isRepoCycleId(previousCycle?.type) ? previousCycle.type : undefined;
  if (id === undefined) return undefined;
  if (previousCycle?.status === 'failed') {
    return {
      id,
      outcome: 'failed',
      message: `${cycleName(id)} cycle needs attention.`,
      detail: previousCycle.lastError ?? cycleFailureDetail(snapshot),
    };
  }
  if (previousCycle?.status === 'interrupted') {
    return {
      id,
      outcome: 'stopped',
      message: 'Cycle stopped · No completion action was dispatched.',
    };
  }
  return {
    id,
    outcome: 'completed',
    message: `${cycleName(id)} cycle complete.`,
  };
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
  if (!isRepoCycleId(cycle?.type)) return null;
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
  const interrupted = cycle.status === 'interrupted';
  return {
    id: cycle.type,
    count,
    stages,
    headline: failed
      ? `${cycleName(cycle.type)} cycle needs attention`
      : needsInput
        ? 'Agent is waiting for your input'
        : interrupted
          ? `${cycleName(cycle.type)} cycle paused`
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
    case 'refactor':
      return {
        id,
        label: 'Plan refactor',
        title: 'Start a refactor pass',
        description:
          'Describe the improvement and run it as a separate pass that merges back on approval.',
      };
    case 'review-feedback':
      return {
        id,
        label: 'Address review feedback',
        title: 'Address review feedback',
        description:
          'Launch a child pass to address unaddressed pull-request review comments across the parent repositories.',
      };
  }
}

function cycleStages(id: RepoCycleId): Array<Omit<CycleStage, 'state'>> {
  switch (id) {
    case 'rebase':
      return [
        { id: 'inspect_rebase', label: 'Inspect & rebase' },
        { id: 'resolve_conflicts', label: 'Resolve conflicts', conditional: true },
        { id: 'final_review', label: 'Final review' },
        { id: 'publish', label: 'Publish' },
      ];
  }
}

function normalizedCyclePhase(id: RepoCycleId, phase?: string): string {
  if (phase !== undefined && cycleStages(id).some((stage) => stage.id === phase)) return phase;
  switch (id) {
    case 'rebase':
      return 'inspect_rebase';
  }
}

function isRepoCycleId(value?: string): value is RepoCycleId {
  return value === 'rebase';
}

function cycleName(id: RepoCycleId): string {
  switch (id) {
    case 'rebase':
      return 'Rebase';
  }
}
