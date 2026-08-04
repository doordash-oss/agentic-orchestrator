import type { FeatureSnapshot } from '../../../shared/ipc';
import {
  pendingDeliveryDetail,
  pendingDeliveryTotals,
  type PendingDelivery,
} from './completion/pendingDelivery';

/** Aftercare launch surfaces: delivery of pending work, then the passes. */
export type AftercareActionId =
  | 'publish'
  | 'publish-updates'
  | 'merge-updates'
  | 'rebase'
  | 'refactor'
  | 'review-feedback';

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
// The catalog ids only — undelivered-work ids are preflight-derived and never enter this list.
type CatalogActionId = 'publish' | 'rebase' | 'refactor' | 'review-feedback';
const ACTION_ORDER: CatalogActionId[] = ['publish', 'rebase', 'refactor', 'review-feedback'];

export function resolvePostImplementationMode(snapshot: FeatureSnapshot): PostImplementationMode {
  return AFTERCARE_STATUSES.has(snapshot.status) ? { kind: 'aftercare' } : { kind: 'regular' };
}

export function aftercareActions(
  snapshot: FeatureSnapshot,
  pending?: PendingDelivery,
): AftercareAction[] {
  const catalog = ACTION_ORDER.flatMap((id) => {
    const action = snapshot.actions.find((candidate) => candidate.id === id);
    if (action?.enabled !== true) return [];
    return [aftercareAction(id)];
  });
  return [...pendingDeliveryActions(pending), ...catalog];
}

// Undelivered work is preflight-derived, not a catalog action: the action
// catalog stays a pure feature-state projection.
function pendingDeliveryActions(pending?: PendingDelivery): AftercareAction[] {
  if (pending === undefined) return [];
  const out: AftercareAction[] = [];
  if (pending.publishRepos.length > 0) {
    out.push({
      id: 'publish-updates',
      label: 'Publish updates',
      title: 'Publish new commits',
      description: `Not on the pull-request branch yet: ${pendingDeliveryDetail(
        pendingDeliveryTotals(pending.publishRepos),
      )}.`,
    });
  }
  if (pending.mergeRepos.length > 0) {
    out.push({
      id: 'merge-updates',
      label: 'Merge updates',
      title: 'Merge new commits',
      description: `Not in the base branch yet: ${pendingDeliveryDetail(
        pendingDeliveryTotals(pending.mergeRepos),
      )}.`,
    });
  }
  return out;
}

function aftercareAction(id: CatalogActionId): AftercareAction {
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
