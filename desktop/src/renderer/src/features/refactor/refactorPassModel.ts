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
  type RelationshipTransactionView,
  type ReviewFeedbackCommentView,
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
  | 'final-reviewing'
  | 'review-passed'
  | 'integrating'
  | 'integration-attention'
  | 'closing'
  | 'merged'
  | 'closed';

export interface PassState {
  id: PassStateId;
  /** One sentence under the pass name: what is happening and what comes next. */
  sentence: string;
  tone: 'quiet' | 'live' | 'attention' | 'danger';
  /** Repository-level diagnostics when integration parks (conflicts, dirt). */
  problems?: string[];
}

const ACTIVE_TRANSACTION_PHASES = new Set(['preparing', 'prepared', 'applying']);
const PARKED_TRANSACTION_PHASES = new Set(['attention', 'rolling_back', 'rolled_back']);

function transactionProblems(transaction: RelationshipTransactionView): string[] {
  const problems: string[] = [];
  if (transaction.attention !== undefined && transaction.attention !== '') {
    problems.push(transaction.attention);
  }
  for (const entry of transaction.entries ?? []) {
    const repo = entry.repo === undefined ? '' : `${entry.repo}: `;
    if (entry.conflictFiles !== undefined && entry.conflictFiles.length > 0) {
      problems.push(`${repo}conflicts in ${entry.conflictFiles.join(', ')}`);
    }
    if (entry.diagnostics !== undefined && entry.diagnostics !== '') {
      problems.push(`${repo}${entry.diagnostics}`);
    }
    if (entry.cleanupWarning !== undefined && entry.cleanupWarning !== '') {
      problems.push(`${repo}${entry.cleanupWarning}`);
    }
  }
  return problems;
}

export function passState(child: FeatureSnapshot): PassState {
  const transaction = child.transaction;
  const transactionPhase = transaction?.phase;
  if (transactionPhase === 'merged' || child.closeOutcome === 'completed') {
    return {
      id: 'merged',
      sentence: 'The pass is merged into the parent and closed.',
      tone: 'quiet',
    };
  }
  if (child.closeOutcome !== undefined && child.closeOutcome !== '') {
    return { id: 'closed', sentence: 'The pass is closed without merging.', tone: 'quiet' };
  }
  if (transactionPhase !== undefined && PARKED_TRANSACTION_PHASES.has(transactionPhase)) {
    return {
      id: 'integration-attention',
      sentence:
        transactionPhase === 'attention'
          ? 'Integration needs attention. Review the repository details below.'
          : 'Integration was rolled back. Review the repository details below.',
      tone: 'attention',
      problems: transaction === undefined ? [] : transactionProblems(transaction),
    };
  }
  if (transactionPhase === 'applied') {
    return {
      id: 'closing',
      sentence: 'Merges applied to the parent branches. Closing the pass.',
      tone: 'live',
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
  if (child.status === 'FinalReviewing') {
    return {
      id: 'final-reviewing',
      sentence: 'Final review is running before the merge back.',
      tone: 'live',
    };
  }
  if (child.status === 'ReviewPassed') {
    return {
      id: 'review-passed',
      sentence: 'Review passed. Integration into the parent starts next.',
      tone: 'live',
    };
  }
  // "Ready" exists only before the first run; a pass with any run behind it
  // must never read as startable again.
  if (child.status === 'Created' && isReadyToStart(child)) {
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
    case 'final-reviewing':
      return 'Final review';
    case 'review-passed':
    case 'integrating':
    case 'integration-attention':
    case 'closing':
      return 'Review passed';
    case 'merged':
      return 'Merged';
    case 'closed':
      return 'Closed';
    case 'working':
      return displayStatusLabel(child.status);
  }
}

function integrationStation(child: FeatureSnapshot | null): Omit<CustodyStation, 'id' | 'eyebrow'> {
  const phase = child?.transaction?.phase;
  if (phase === 'merged' || child?.closeOutcome === 'completed') {
    return { title: 'Integration', detail: 'Merged into the parent', state: 'done' };
  }
  if (phase === 'attention') {
    return { title: 'Integration', detail: 'Needs attention', state: 'attention' };
  }
  if (phase === 'rolling_back' || phase === 'rolled_back') {
    return { title: 'Integration', detail: displayPhase(phase), state: 'attention' };
  }
  if (phase === 'applied' || (phase !== undefined && ACTIVE_TRANSACTION_PHASES.has(phase))) {
    return { title: 'Integration', detail: displayPhase(phase), state: 'live' };
  }
  return { title: 'Integration', detail: 'After final review approval', state: 'pending' };
}

function displayPhase(phase: string): string {
  const normalized = phase.replace(/[_-]+/g, ' ').trim();
  return normalized === '' ? phase : normalized.charAt(0).toUpperCase() + normalized.slice(1);
}

/** Pass-station work that is behind it: integration onward reads as done. */
const DONE_PASS_STATES = new Set<PassStateId>(['integrating', 'closing', 'merged', 'closed']);

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
      eyebrow: passKindLabel(view.kind),
      title: view.name,
      detail:
        state === null || child === null ? view.displayState : passStationDetail(state, child),
      state: attention
        ? 'attention'
        : state !== null && DONE_PASS_STATES.has(state.id)
          ? 'done'
          : state === null || state.tone === 'quiet'
            ? 'pending'
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
 * state sentence carries the why instead. Start is additionally gated on the
 * "ready" state so a run beyond setup can never re-offer it.
 */
export function passActions(child: FeatureSnapshot): PassAction[] {
  const stateId = passState(child).id;
  const enabled = (id: PassAction['id']): boolean =>
    actionById(child, id)?.enabled === true && (id !== 'start' || stateId === 'ready');
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

/** The pass-kind label for region/eyebrow copy, switching on the child kind. */
export function passKindLabel(kind: string): string {
  return kind === 'review-feedback' ? 'Review feedback pass' : 'Refactor pass';
}

/** The active-pass status chip label, switching on the child kind. */
export function refactoringStatusChip(view: RelationshipChildView): {
  label: string;
  tone: 'info' | 'attention';
} {
  const attention = view.attention.length > 0 || view.integrationState === 'attention';
  const active = view.kind === 'review-feedback' ? 'Addressing review feedback' : 'Refactoring';
  return attention
    ? { label: `${active} — needs attention`, tone: 'attention' }
    : { label: active, tone: 'info' };
}

/** Human-readable label for each child kind, used by the closed-pass history. */
export const CHILD_KIND_LABEL: Record<string, string> = {
  refactor: 'Refactor',
  'review-feedback': 'Review feedback',
};

/** Human-readable label for each review-feedback comment type. */
export const COMMENT_TYPE_LABEL: Record<ReviewFeedbackCommentView['type'], string> = {
  review: 'Review comment',
  issue: 'Issue',
  review_body: 'Review body',
};

/** Stable identity key for a review-feedback comment, including type to avoid same-ID collisions across inline-review and issue-comment sequences. */
export function commentKey(comment: ReviewFeedbackCommentView): string {
  return `${comment.repo}:${comment.type}:${comment.id}`;
}
