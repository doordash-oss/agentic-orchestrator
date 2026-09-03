/*
Copyright 2026 DoorDash, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

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
  | 'merge'
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

/**
 * Renderer copy for disabled-reason codes whose server message states a
 * machine fact instead of the reader's next move. `worktree_state_unknown` is
 * an unreadable worktree, not a dirty one — a probe that merely ran out of time
 * no longer disables anything, so retrying is not the remedy here.
 */
const DISABLED_REASON_COPY: Record<string, string> = {
  worktree_state_unknown:
    'Could not read the repository worktrees — check that they still exist and are a valid checkout.',
};

export function disabledReasonCopy(reason: { code: string; message: string }): string {
  return DISABLED_REASON_COPY[reason.code] ?? reason.message;
}

const AFTERCARE_STATUSES = new Set(['CodeReady', 'Published', 'Done']);
// The catalog ids only — undelivered-work ids are preflight-derived and never enter this list.
type CatalogActionId = 'publish' | 'rebase' | 'refactor' | 'review-feedback';
const ACTION_ORDER: CatalogActionId[] = ['publish', 'rebase', 'refactor', 'review-feedback'];
// publish is excluded: a blocked publish row would be noise on manual-publish
// features, which report it disabled constantly.
const PASS_ACTION_IDS: CatalogActionId[] = ['rebase', 'refactor', 'review-feedback'];

export function resolvePostImplementationMode(snapshot: FeatureSnapshot): PostImplementationMode {
  return AFTERCARE_STATUSES.has(snapshot.status) ? { kind: 'aftercare' } : { kind: 'regular' };
}

export function aftercareActions(
  snapshot: FeatureSnapshot,
  pending?: PendingDelivery,
): AftercareAction[] {
  const catalog = ACTION_ORDER.flatMap((id) => {
    const action = snapshot.actions.find((candidate) => candidate.id === id);
    if (action === undefined) return [];
    if (action.enabled) return [aftercareAction(id)];
    // A blocked pass stays on the runway carrying its reason; silently
    // dropping it is how the surface used to hide available work.
    if (!PASS_ACTION_IDS.includes(id)) return [];
    const reason = action.disabledReasons[0];
    if (reason === undefined) return [];
    return [{ ...aftercareAction(id), disabledReason: disabledReasonCopy(reason) }];
  });
  // Delivery rows the catalog already covers would only duplicate it — the
  // enabled catalog `publish` row and the preflight's publish-eligible row are
  // the same verb, the same copy, and the same modal.
  const delivery = pendingDeliveryActions(pending).filter(
    (action) => !catalog.some((existing) => existing.id === action.id),
  );
  return [...delivery, ...catalog];
}

// Delivery is preflight-derived, not a catalog action: the action catalog
// stays a pure feature-state projection. The runway is delivery's only home,
// so the first local merge belongs here too — the completion bar no longer
// offers it.
function pendingDeliveryActions(pending?: PendingDelivery): AftercareAction[] {
  if (pending === undefined) return [];
  const out: AftercareAction[] = [];
  // Publication eligibility is the preflight's call, not the action
  // catalogue's: the deleted completion bar offered Publish from exactly this
  // classification, so the runway has to as well or the verb loses its home.
  if (pending.publishEligibleRepos.length > 0) {
    out.push(aftercareAction('publish'));
  }
  if (pending.initialMergeRepos.length > 0) {
    out.push({
      id: 'merge',
      label: 'Merge',
      title: 'Merge this feature',
      description: `Merge the completed work into the base branch of ${repoPhrase(
        pending.initialMergeRepos.length,
      )}.`,
    });
  }
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

function repoPhrase(count: number): string {
  return count === 1 ? 'its local repository' : `all ${count} local repositories`;
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
