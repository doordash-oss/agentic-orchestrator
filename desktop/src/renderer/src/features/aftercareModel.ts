import type { FeatureSnapshot } from '../../../shared/ipc';

export type AftercareCycleId = 'rebase' | 'review-comments' | 'refactor';

export interface AftercareHeadline {
  statusLabel: string;
  heading: string;
  description: string;
}

export interface AftercareCycle {
  id: AftercareCycleId;
  title: string;
  description: string;
  scope: string;
  verb: string;
}

export interface AftercareRepository {
  name: string;
  freshness: string;
  pullRequest: string;
  publishability: string;
  prUrl?: string;
  cycle?: string;
}

const HEADLINES: Record<string, AftercareHeadline> = {
  CodeReady: {
    statusLabel: 'Code ready',
    heading: 'Implementation complete',
    description:
      'The run has come to rest. Publish, merge, or choose a maintenance cycle when the work needs another pass.',
  },
  Published: {
    statusLabel: 'Published',
    heading: 'Published and ready for what comes next',
    description:
      'The work is out in the world. Keep its branches current, collect review notes, or shape the next refinement.',
  },
  Done: {
    statusLabel: 'Done',
    heading: 'Work complete',
    description:
      'The feature is closed, but its run record and any available maintenance cycles remain within reach.',
  },
};

const CYCLE_ORDER: AftercareCycleId[] = ['rebase', 'review-comments', 'refactor'];

export function aftercareHeadline(status: string): AftercareHeadline {
  return (
    HEADLINES[status] ?? {
      statusLabel: status,
      heading: 'Run at rest',
      description: 'Review the completed work or choose an available maintenance cycle.',
    }
  );
}

export function availableAftercareCycles(snapshot: FeatureSnapshot): AftercareCycle[] {
  return CYCLE_ORDER.flatMap((id) => {
    const action = snapshot.actions.find((candidate) => candidate.id === id);
    if (action?.enabled !== true) return [];
    return [cycleFrom(snapshot, id)];
  });
}

export function aftercareRepositories(snapshot: FeatureSnapshot): AftercareRepository[] {
  const statusByName = new Map((snapshot.repoStatus ?? []).map((status) => [status.name, status]));
  return snapshot.repos.map((name) => {
    const status = statusByName.get(name);
    const cycle =
      status?.cycleType !== undefined && status.cycleStatus !== undefined
        ? `${sentenceCase(status.cycleType)} ${sentenceCase(status.cycleStatus).toLowerCase()}`
        : undefined;
    return {
      name,
      freshness:
        status?.freshness === undefined ? 'Freshness unavailable' : sentenceCase(status.freshness),
      pullRequest:
        status === undefined
          ? 'PR unavailable'
          : status.prUrl === undefined
            ? 'No pull request'
            : 'PR open',
      publishability:
        status === undefined
          ? 'Publishability unavailable'
          : status.publishable
            ? 'Publishable'
            : 'Not publishable',
      ...(status?.prUrl === undefined ? {} : { prUrl: status.prUrl }),
      ...(cycle === undefined ? {} : { cycle }),
    };
  });
}

function cycleFrom(snapshot: FeatureSnapshot, id: AftercareCycleId): AftercareCycle {
  const repoCount = snapshot.repos.length;
  const repositoryScope = `${repoCount} ${repoCount === 1 ? 'repository' : 'repositories'}`;
  switch (id) {
    case 'rebase':
      return {
        id,
        title: 'Rebase onto target branches',
        description: 'Bring every worktree forward through a fresh guarded preflight.',
        scope: repositoryScope,
        verb: 'Prepare rebase',
      };
    case 'review-comments':
      return {
        id,
        title: 'Check review comments',
        description: 'Fetch the current review notes before choosing exactly what to address.',
        scope: repositoryScope,
        verb: 'Check comments',
      };
    case 'refactor':
      return {
        id,
        title: 'Plan another pass',
        description: 'Start a focused refactor without reopening the completed implementation run.',
        scope: 'Choose one or all repositories',
        verb: 'Plan refactor',
      };
  }
}

function sentenceCase(value: string): string {
  const normalized = value.replace(/[_-]+/g, ' ').trim();
  return normalized === '' ? value : normalized.charAt(0).toUpperCase() + normalized.slice(1);
}
