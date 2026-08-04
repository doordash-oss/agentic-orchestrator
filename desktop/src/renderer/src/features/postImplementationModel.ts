import type { FeatureSnapshot } from '../../../shared/ipc';

/** Aftercare launch surfaces: the rebase pass and the refactor pass. */
export type AftercareActionId = 'publish' | 'rebase' | 'refactor' | 'review-feedback';

/** Aftercare modal ids for launcher modals (refactor, review-feedback). */
export type AftercareModalId = 'refactor' | 'review-feedback';

export type PostImplementationMode = { kind: 'regular' } | { kind: 'aftercare' };

export interface AftercareAction {
  id: AftercareActionId;
  label: string;
  title: string;
  description: string;
  disabledReason?: string;
}

const AFTERCARE_STATUSES = new Set(['CodeReady', 'Published', 'Done']);
const ACTION_ORDER: AftercareActionId[] = ['publish', 'rebase', 'refactor', 'review-feedback'];

export function resolvePostImplementationMode(snapshot: FeatureSnapshot): PostImplementationMode {
  return AFTERCARE_STATUSES.has(snapshot.status) ? { kind: 'aftercare' } : { kind: 'regular' };
}

export function aftercareActions(snapshot: FeatureSnapshot): AftercareAction[] {
  return ACTION_ORDER.flatMap((id) => {
    const action = snapshot.actions.find((candidate) => candidate.id === id);
    if (action?.enabled !== true) return [];
    return [aftercareAction(id)];
  });
}

function aftercareAction(id: AftercareActionId): AftercareAction {
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
        label: 'Start rebase pass',
        title: 'Bring branches up to date',
        description:
          'Starts a pass immediately that merges each behind repository’s target branch into the feature. No setup needed.',
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
