import { isEligibleForPublish } from './completionShared';
import { isInitialMerge, UNMERGED_CHANGES, UNPUBLISHED_CHANGES } from './pendingDelivery';
import type { CompletionPreflightResult } from '../../../../shared/ipc';

export type CompletionVerb = 'publish' | 'merge' | 'mark-done' | 'cleanup';

export interface CompletionVerbModel {
  verb: CompletionVerb;
  label: string;
  state: 'available' | 'done' | 'blocked';
  primary: boolean;
  blocker?: string;
}

const LABELS: Record<CompletionVerb, { available: string; done: string; updates?: string }> = {
  publish: { available: 'Publish', done: 'Published', updates: 'Publish updates' },
  merge: { available: 'Merge', done: 'Merged', updates: 'Merge updates' },
  'mark-done': { available: 'Mark done', done: 'Done' },
  cleanup: { available: 'Clean up', done: 'Cleaned' },
};

// Fixed left-to-right bar order; primary emphasis is a separate pass.
const ORDER: CompletionVerb[] = ['publish', 'merge', 'cleanup', 'mark-done'];

type Draft = { state: 'available' | 'done' | 'blocked'; blocker?: string; label?: string } | null;

export function completionBarModel(
  preflight: CompletionPreflightResult,
  candidates: ReadonlySet<CompletionVerb>,
): CompletionVerbModel[] {
  const repos = preflight.repos;
  const eligible = repos.filter(isEligibleForPublish);
  const published = repos.filter((r) => r.status === 'already_published');
  const unpublished = repos.filter((r) => r.status === UNPUBLISHED_CHANGES);
  const unmerged = repos.filter((r) => r.status === UNMERGED_CHANGES);
  // One classification for the first local merge, shared with the aftercare
  // runway's initial-merge delivery row (see `isInitialMerge`).
  const localMerge = repos.filter(isInitialMerge);
  const merged = repos.filter((r) => !r.publishable && r.touched && r.status === 'completed');

  const draft = (verb: CompletionVerb): Draft => {
    if (!candidates.has(verb)) return null;
    switch (verb) {
      case 'publish':
        if (eligible.length > 0) return { state: 'available' };
        if (unpublished.length > 0) return { state: 'available', label: LABELS.publish.updates };
        if (published.length > 0) return { state: 'done' };
        return null;
      case 'merge':
        if (localMerge.length > 0) return { state: 'available' };
        if (unmerged.length > 0) return { state: 'available', label: LABELS.merge.updates };
        if (merged.length > 0) return { state: 'done' };
        return null;
      case 'mark-done':
        return preflight.canMarkDone
          ? { state: 'available' }
          : { state: 'blocked', blocker: preflight.markDoneBlocker };
      case 'cleanup':
        return { state: 'available' };
    }
  };

  const models: CompletionVerbModel[] = [];
  for (const verb of ORDER) {
    const d = draft(verb);
    if (d === null) continue;
    models.push({
      verb,
      label: d.label ?? (d.state === 'done' ? LABELS[verb].done : LABELS[verb].available),
      state: d.state,
      primary: false,
      ...(d.blocker !== undefined ? { blocker: d.blocker } : {}),
    });
  }

  // Primary emphasis: first available verb in spec priority order.
  for (const verb of ['publish', 'merge', 'mark-done', 'cleanup'] as CompletionVerb[]) {
    const m = models.find((x) => x.verb === verb && x.state === 'available');
    if (m !== undefined) {
      m.primary = true;
      break;
    }
  }

  return models;
}
