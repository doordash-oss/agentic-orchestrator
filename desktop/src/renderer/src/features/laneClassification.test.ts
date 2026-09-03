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

import { describe, expect, it } from 'vitest';
import { canonicalWarning, featureSnapshot } from '../test/agenticoMock';
import type { CanonicalError } from '../../../shared/api/parse';
import type { FeatureSnapshot, OwnedError, RelationshipChildView } from '../../../shared/ipc';
import {
  LANES,
  classifyFeaturesByLane,
  classifyFeaturesByLaneWithAttention,
  classifyLane,
  laneCounts,
  type Lane,
} from './laneClassification';

function snapshot(status: string, overrides: Partial<FeatureSnapshot> = {}): FeatureSnapshot {
  return featureSnapshot({
    id: `feature-${status}`,
    status,
    setup: { status: 'done', attempt: 1, tasks: [] },
    actions: [],
    ...overrides,
  });
}

/** Canonical integration-attention error as the renderer receives it. */
function parkedAttention(): RelationshipChildView['attention'] {
  return {
    code: 'integration_parent_dirty',
    class: 'needs_action',
    title: 'Parent worktree is dirty',
    summary: 'The parent worktree for repository "repo-a" has 1 uncommitted change.',
    remediation: {
      hint: 'Commit or stash the parent worktree changes and retry.',
      actions: ['retry'],
    },
    context: { repositories: [{ name: 'repo-a', dirty_files: ['stray.txt'] }] },
  };
}

function child(overrides: Partial<RelationshipChildView> = {}): RelationshipChildView {
  return {
    id: 'child1234ef567890',
    name: 'Refactor pass',
    kind: 'refactor',
    displayToken: 'refactor:child1234ef567890',
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

/** A blocking run-failure entry as the summary projection carries it. */
function blockingRunError(featureId: string): OwnedError {
  return {
    ref: { scope: 'run', code: 'iteration_budget_exhausted', featureId },
    error: {
      code: 'iteration_budget_exhausted',
      class: 'blocking',
      title: 'Iteration budget exhausted',
      summary: 'The Implement phase exhausted its iteration budget.',
    },
  };
}

/** A blocking setup-failure entry naming the owning task. */
function blockingSetupError(featureId: string): OwnedError {
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

/** A needs_action transaction entry keyed by the active child. */
function needsActionTransactionError(childId: string): OwnedError {
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

/** A needs_action repository publish-failure entry. */
function needsActionRepositoryError(featureId: string): OwnedError {
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

// Every status the server defines (internal/feature/feature.go Status enum),
// mapped to the lane it should classify into with no active child, no owned
// errors, and durable setup already done. A bare Failed status carries no
// projected error, so it never classifies the Failed lane.
const STATUS_LANE_MATRIX: Array<[string, Lane]> = [
  ['Created', 'at-rest'],
  ['Researching', 'running'],
  ['PlanReady', 'at-rest'],
  ['Planning', 'running'],
  ['ImplementReady', 'at-rest'],
  ['Implementing', 'running'],
  ['ReviewPassed', 'at-rest'],
  ['CodeReady', 'at-rest'],
  ['Published', 'published'],
  ['Failed', 'at-rest'],
  ['Interrupted', 'waiting'],
  ['Done', 'done'],
  ['BuildingKB', 'running'],
  ['PlanNeedsReview', 'waiting'],
  ['Inquiring', 'running'],
  ['InquireReady', 'at-rest'],
  ['DesignReady', 'at-rest'],
  ['Designing', 'running'],
  ['PromptNeedsReview', 'waiting'],
  ['InquiryNeedsReview', 'waiting'],
  ['ResearchNeedsReview', 'waiting'],
  ['DesignNeedsReview', 'waiting'],
  ['Reviewing', 'running'],
  ['NeedUserInput', 'waiting'],
  ['FinalReviewing', 'running'],
  ['SettingUpWorktrees', 'running'],
];

describe('classifyLane — full status matrix', () => {
  it.each(STATUS_LANE_MATRIX)('maps status %s to lane %s', (status, expectedLane) => {
    expect(classifyLane(snapshot(status))).toBe(expectedLane);
  });

  it('covers every status the matrix table lists exactly once', () => {
    const statuses = STATUS_LANE_MATRIX.map(([status]) => status);
    expect(new Set(statuses).size).toBe(statuses.length);
    expect(statuses).toHaveLength(26);
  });
});

describe('classifyLane — setup running', () => {
  it('classifies a startable-looking status as running while durable setup is still running', () => {
    const feature = snapshot('CodeReady', { setup: { status: 'running', attempt: 1, tasks: [] } });
    expect(classifyLane(feature)).toBe('running');
  });
});

describe('classifyLane — owned errors drive presence', () => {
  it('classifies a blocking run entry as failed regardless of the status string', () => {
    for (const [status] of STATUS_LANE_MATRIX) {
      const feature = snapshot(status, {
        id: `blocked-${status}`,
        errors: [blockingRunError(`blocked-${status}`)],
      });
      expect(classifyLane(feature)).toBe('failed');
    }
  });

  it('classifies a blocking setup entry as failed', () => {
    const feature = snapshot('Failed', { errors: [blockingSetupError('feature-Failed')] });
    expect(classifyLane(feature)).toBe('failed');
  });

  it('classifies a needs_action transaction entry (a parked child) as waiting', () => {
    const feature = snapshot('Published', {
      activeChild: child({ status: 'Implementing', attention: parkedAttention() }),
      errors: [needsActionTransactionError('child1234ef567890')],
    });
    expect(classifyLane(feature)).toBe('waiting');
  });

  it('classifies a needs_action repository entry as waiting', () => {
    const feature = snapshot('CodeReady', {
      errors: [needsActionRepositoryError('feature-CodeReady')],
    });
    expect(classifyLane(feature)).toBe('waiting');
  });

  it('keeps the status-driven pauses in waiting: NeedUserInput, NeedsReview, Interrupted', () => {
    expect(classifyLane(snapshot('NeedUserInput'))).toBe('waiting');
    expect(classifyLane(snapshot('PlanNeedsReview'))).toBe('waiting');
    expect(classifyLane(snapshot('Interrupted'))).toBe('waiting');
  });

  it('does not classify a bare Failed status (no projected entries) as failed', () => {
    expect(classifyLane(snapshot('Failed'))).not.toBe('failed');
  });
});

describe('classifyLane — active child pass', () => {
  it('classifies a Published parent with a running child pass as running, not published', () => {
    const feature = snapshot('Published', { activeChild: child({ status: 'Implementing' }) });
    expect(classifyLane(feature)).toBe('running');
  });

  it('classifies a Published parent with a not-yet-started child pass as published', () => {
    const feature = snapshot('Published', { activeChild: child({ status: 'Created' }) });
    expect(classifyLane(feature)).toBe('published');
  });

  it('classifies a parent whose child run failed as failed, through the child-keyed blocking entry', () => {
    const feature = snapshot('Published', {
      activeChild: child({ status: 'Failed' }),
      errors: [blockingRunError('child1234ef567890')],
    });
    expect(classifyLane(feature)).toBe('failed');
  });

  // Most adversarial representable combination: the schema allows `status`
  // and `activeChild` to vary independently, so a parent already marked Done
  // can still carry an unresolved child pass. The owned error wins.
  it('classifies a Done parent with a parked child as waiting, through the transaction entry', () => {
    const feature = snapshot('Done', {
      activeChild: child({
        status: 'Implementing',
        attention: parkedAttention(),
        integrationState: 'attention',
      }),
      errors: [needsActionTransactionError('child1234ef567890')],
    });
    expect(classifyLane(feature)).toBe('waiting');
  });
});

describe('classifyLane — warnings never change the lane', () => {
  const snapshotWarning = canonicalWarning();
  const childWarning: CanonicalError = canonicalWarning({
    code: 'child_cleanup_incomplete',
    title: 'Cleanup incomplete',
    summary: 'The worktree for repository "repo-a" could not be removed.',
    context: { repositories: [{ name: 'repo-a', branch: 'agentico/pass-3' }] },
  });

  it('classifies a snapshot carrying feature warnings to exactly the lane the same snapshot takes without them, across every status', () => {
    for (const [status] of STATUS_LANE_MATRIX) {
      const bare = snapshot(status);
      const laden = snapshot(status, { warnings: [snapshotWarning] });
      expect(classifyLane(laden)).toBe(classifyLane(bare));
    }
  });

  it('classifies an active child carrying relationship warnings to exactly the lane the same child takes without them', () => {
    for (const [status] of STATUS_LANE_MATRIX) {
      const bare = snapshot(status, {
        activeChild: child({ status: 'Implementing', warnings: [] }),
      });
      const laden = snapshot(status, {
        activeChild: child({ status: 'Implementing', warnings: [childWarning] }),
      });
      expect(classifyLane(laden)).toBe(classifyLane(bare));
    }
  });

  it('keeps warnings from moving an error-carrying feature out of its lane', () => {
    const bare = snapshot('Implementing', {
      errors: [blockingRunError('feature-Implementing')],
    });
    const laden = snapshot('Implementing', {
      errors: [blockingRunError('feature-Implementing')],
      warnings: [snapshotWarning],
      activeChild: child({ status: 'Implementing', warnings: [childWarning] }),
    });
    expect(classifyLane(laden)).toBe(classifyLane(bare));
  });

  // Rewind action-result warnings and repository-diff errors travel on their
  // response objects, which classifyLane never receives; the two fields above
  // are the only warning carriers a snapshot can represent.
  it('keeps lane groups and counts identical for a batch carrying warnings', () => {
    const bare = [
      snapshot('Published', {
        activeChild: child({ status: 'Implementing', warnings: [] }),
        errors: [needsActionTransactionError('child1234ef567890')],
      }),
      snapshot('Failed', { errors: [blockingRunError('feature-Failed')] }),
      snapshot('Implementing'),
    ];
    const laden = bare.map((feature) => ({
      ...feature,
      warnings: [snapshotWarning],
      ...(feature.activeChild === undefined
        ? {}
        : { activeChild: { ...feature.activeChild, warnings: [childWarning] } }),
    }));
    const laneIds = (groups: ReturnType<typeof classifyFeaturesByLane>) =>
      LANES.map((lane) => groups[lane].map((feature) => feature.id));
    expect(laneIds(classifyFeaturesByLane(laden))).toStrictEqual(
      laneIds(classifyFeaturesByLane(bare)),
    );
    expect(laneCounts(laden)).toStrictEqual(laneCounts(bare));
  });
});

describe('classifyFeaturesByLane', () => {
  it('groups features into every lane key, ordered newest-first within each lane', () => {
    const features = [
      snapshot('Done', { id: 'done-old', createdAt: '2026-07-10T08:00:00Z' }),
      snapshot('Failed', {
        id: 'failed-new',
        createdAt: '2026-07-15T08:00:00Z',
        errors: [blockingRunError('failed-new')],
      }),
      snapshot('Published', { id: 'published-old', createdAt: '2026-07-11T08:00:00Z' }),
      snapshot('Implementing', { id: 'running-new', createdAt: '2026-07-14T08:00:00Z' }),
      snapshot('Failed', {
        id: 'failed-old',
        createdAt: '2026-07-12T08:00:00Z',
        errors: [blockingRunError('failed-old')],
      }),
      snapshot('CodeReady', { id: 'rest-new', createdAt: '2026-07-13T08:00:00Z' }),
    ];

    const grouped = classifyFeaturesByLane(features);

    expect(Object.keys(grouped).sort()).toStrictEqual(
      ['at-rest', 'done', 'failed', 'published', 'running', 'waiting'].sort(),
    );
    expect(grouped.failed.map((f) => f.id)).toStrictEqual(['failed-new', 'failed-old']);
    expect(grouped.running.map((f) => f.id)).toStrictEqual(['running-new']);
    expect(grouped.published.map((f) => f.id)).toStrictEqual(['published-old']);
    expect(grouped.done.map((f) => f.id)).toStrictEqual(['done-old']);
    expect(grouped['at-rest'].map((f) => f.id)).toStrictEqual(['rest-new']);
  });

  it('returns empty arrays for lanes with no features', () => {
    const grouped = classifyFeaturesByLane([snapshot('Done')]);
    expect(grouped.failed).toStrictEqual([]);
    expect(grouped.waiting).toStrictEqual([]);
    expect(grouped.running).toStrictEqual([]);
    expect(grouped.published).toStrictEqual([]);
    expect(grouped['at-rest']).toStrictEqual([]);
  });
});

describe('laneCounts', () => {
  it('counts features per lane using the same classification, failed included', () => {
    const features = [
      snapshot('Done'),
      snapshot('Done'),
      snapshot('Failed', { errors: [blockingRunError('feature-Failed')] }),
      snapshot('Implementing'),
      snapshot('Published'),
      snapshot('CodeReady'),
    ];

    expect(laneCounts(features)).toStrictEqual({
      failed: 1,
      waiting: 0,
      running: 1,
      published: 1,
      done: 2,
      'at-rest': 1,
    });
  });
});

describe('classifyFeaturesByLaneWithAttention', () => {
  it('re-buckets a feature with a pending attention count into waiting regardless of its own lane', () => {
    const running = snapshot('Implementing', { id: 'running-1' });
    const rested = snapshot('CodeReady', { id: 'rested-1' });
    const attentionByFeature = new Map([['running-1', 1]]);

    const grouped = classifyFeaturesByLaneWithAttention([running, rested], attentionByFeature);

    expect(grouped.waiting.map((f) => f.id)).toStrictEqual(['running-1']);
    expect(grouped.running).toStrictEqual([]);
    expect(grouped['at-rest'].map((f) => f.id)).toStrictEqual(['rested-1']);
  });

  it('keeps a failed feature in the Failed lane even when it also carries pending attention', () => {
    const blocked = snapshot('Implementing', {
      id: 'blocked-1',
      errors: [blockingRunError('blocked-1')],
    });
    const grouped = classifyFeaturesByLaneWithAttention([blocked], new Map([['blocked-1', 1]]));
    expect(grouped.failed.map((f) => f.id)).toStrictEqual(['blocked-1']);
    expect(grouped.waiting).toStrictEqual([]);
  });

  it('returns the base classification untouched when nothing has pending attention', () => {
    const running = snapshot('Implementing', { id: 'running-1' });
    const base = classifyFeaturesByLane([running]);
    const grouped = classifyFeaturesByLaneWithAttention([running], new Map());
    expect(grouped).toStrictEqual(base);
  });
});

describe('purity', () => {
  it('returns the same result for the same input and never mutates the snapshot', () => {
    const feature = snapshot('Published', {
      activeChild: child({ status: 'Implementing' }),
      errors: [needsActionTransactionError('child1234ef567890')],
    });
    const before = JSON.parse(JSON.stringify(feature));

    const first = classifyLane(feature);
    const second = classifyLane(feature);

    expect(first).toBe(second);
    expect(feature).toStrictEqual(before);
  });

  it('does not mutate inputs when grouping or counting a batch', () => {
    const features = [
      snapshot('Done'),
      snapshot('Failed', { errors: [blockingRunError('feature-Failed')] }),
      snapshot('Implementing'),
    ];
    const before = JSON.parse(JSON.stringify(features));

    classifyFeaturesByLane(features);
    laneCounts(features);

    expect(features).toStrictEqual(before);
  });
});
