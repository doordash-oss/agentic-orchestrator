import { describe, expect, it } from 'vitest';
import { featureSnapshot } from '../test/agenticoMock';
import type { FeatureSnapshot, OwnedError, RelationshipChildView } from '../../../shared/ipc';
import { errorStatusChip } from './statusChip';

const CHILD_ID = 'child1234ef567890';

function childView(overrides: Partial<RelationshipChildView> = {}): RelationshipChildView {
  return {
    id: CHILD_ID,
    name: 'Slop removal pass',
    kind: 'refactor',
    displayToken: `refactor:${CHILD_ID}`,
    displayState: 'Active — Implementing',
    pipeline: 'large',
    status: 'Implementing',
    startedAt: '2026-07-30T10:00:00Z',
    cost: { totalUsd: 0, byPhase: {} },
    integrationState: 'pending',
    warnings: [],
    ...overrides,
  };
}

function runError(featureId: string, errorClass: 'blocking' | 'needs_action'): OwnedError {
  return {
    ref: { scope: 'run', code: 'iteration_budget_exhausted', featureId },
    error: {
      code: 'iteration_budget_exhausted',
      class: errorClass,
      title: 'Iteration budget exhausted',
      summary: 'The Implement phase exhausted its iteration budget.',
    },
  };
}

function transactionError(childId: string): OwnedError {
  return {
    ref: { scope: 'transaction', code: 'integration_parent_dirty', featureId: childId },
    error: {
      code: 'integration_parent_dirty',
      class: 'needs_action',
      title: 'Parent worktree is dirty',
      summary: 'The parent worktree for repository "repo-a" has 1 uncommitted change.',
    },
  };
}

function setupError(featureId: string): OwnedError {
  return {
    ref: {
      scope: 'setup',
      code: 'worktree_setup_failed',
      featureId,
      taskKey: 'worktree:repo-a',
    },
    error: {
      code: 'worktree_setup_failed',
      class: 'blocking',
      title: 'Worktree setup failed',
      summary: 'The worktree for repository "repo-a" could not be created.',
    },
  };
}

function repositoryError(featureId: string): OwnedError {
  return {
    ref: {
      scope: 'repository',
      code: 'publish_rebase_conflict',
      featureId,
      repository: 'repo-a',
    },
    error: {
      code: 'publish_rebase_conflict',
      class: 'needs_action',
      title: 'Pull-rebase conflict',
      summary: 'The pull rebase for repository "repo-a" conflicted with its target branch.',
    },
  };
}

describe('errorStatusChip', () => {
  it('reads Rebasing — Needs your action for a parked rebase child', () => {
    const snapshot = featureSnapshot({
      status: 'Published',
      activeChild: childView({ kind: 'rebase' }),
      errors: [transactionError(CHILD_ID)],
    } as Partial<FeatureSnapshot>);
    expect(errorStatusChip(snapshot)).toMatchObject({
      label: 'Rebasing — Needs your action',
      tone: 'attention',
      title: 'Parent worktree is dirty',
    });
  });

  it('reads Refactoring — Failed for a failed refactor child', () => {
    const snapshot = featureSnapshot({
      status: 'Published',
      activeChild: childView({ kind: 'refactor', status: 'Failed' }),
      errors: [runError(CHILD_ID, 'blocking')],
    } as Partial<FeatureSnapshot>);
    expect(errorStatusChip(snapshot)).toMatchObject({
      label: 'Refactoring — Failed',
      tone: 'danger',
      title: 'Iteration budget exhausted',
    });
  });

  it('reads Setup — Failed for a setup entry', () => {
    const snapshot = featureSnapshot({
      status: 'Failed',
      errors: [setupError('abcd1234ef567890')],
    } as Partial<FeatureSnapshot>);
    expect(errorStatusChip(snapshot)).toMatchObject({
      label: 'Setup — Failed',
      tone: 'danger',
      title: 'Worktree setup failed',
    });
  });

  it('reads Publishing — Needs your action for a repository entry', () => {
    const snapshot = featureSnapshot({
      status: 'CodeReady',
      errors: [repositoryError('abcd1234ef567890')],
    } as Partial<FeatureSnapshot>);
    expect(errorStatusChip(snapshot)).toMatchObject({
      label: 'Publishing — Needs your action',
      tone: 'attention',
      title: 'Pull-rebase conflict',
    });
  });

  it('reads the bare class label for a top-level run entry', () => {
    const snapshot = featureSnapshot({
      status: 'Failed',
      errors: [runError('abcd1234ef567890', 'blocking')],
    } as Partial<FeatureSnapshot>);
    expect(errorStatusChip(snapshot)).toMatchObject({
      label: 'Failed',
      tone: 'danger',
      title: 'Iteration budget exhausted',
    });
  });

  it('yields no chip when the snapshot owns no errors', () => {
    expect(errorStatusChip(featureSnapshot({ status: 'Failed' }))).toBeUndefined();
    expect(errorStatusChip(featureSnapshot({ status: 'Published' }))).toBeUndefined();
  });

  it('reflects the blocking entry when both classes are present', () => {
    const snapshot = featureSnapshot({
      status: 'Failed',
      errors: [runError('abcd1234ef567890', 'blocking'), repositoryError('abcd1234ef567890')],
    } as Partial<FeatureSnapshot>);
    expect(errorStatusChip(snapshot)).toMatchObject({
      label: 'Failed',
      tone: 'danger',
    });
  });
});
