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
import { featureSnapshot } from '../test/agenticoMock';
import type { OwnedError } from '../../../shared/ipc';
import {
  actionById,
  catalogErrorAction,
  childStatusSpineIndex,
  dashboardState,
  displayFeatureMessage,
  displayPhaseLabel,
  displayStatusLabel,
  featureBranch,
  fieldForCreationError,
  formatDuration,
  formatElapsed,
  highestSeverityError,
  isReadyToStart,
  isRunAtRest,
  orderDashboardFeatures,
  runningPhaseSubline,
  setupProgress,
  spineActiveIndex,
  spineActiveIndexForPhase,
  spineStages,
  spineTone,
} from './featureView';

function ownedError(
  scope: 'run' | 'transaction',
  errorClass: 'blocking' | 'needs_action',
  featureId: string,
): OwnedError {
  const code =
    errorClass === 'blocking' ? 'iteration_budget_exhausted' : 'integration_parent_dirty';
  return {
    ref: { scope, code, featureId },
    error: {
      code,
      class: errorClass,
      title: errorClass === 'blocking' ? 'Iteration budget exhausted' : 'Parent worktree is dirty',
      summary:
        errorClass === 'blocking'
          ? 'The Implement phase exhausted its iteration budget.'
          : 'The parent worktree for repository "repo-a" has 1 uncommitted change.',
    },
  };
}

describe('displayStatusLabel', () => {
  it('translates server enum spellings into user-facing status labels', () => {
    expect(displayStatusLabel('SettingUpWorktrees')).toBe('Setting up worktrees');
    expect(displayStatusLabel('BuildingKB')).toBe('Building knowledge base');
    expect(displayStatusLabel('CodeReady')).toBe('Code ready');
    expect(displayStatusLabel('NeedUserInput')).toBe('Input needed');
    expect(displayStatusLabel('ResearchNeedsReview')).toBe('Research needs review');
  });

  it('keeps unknown status text readable without losing words', () => {
    expect(displayStatusLabel('AwaitingExternalGate')).toBe('Awaiting external gate');
  });
});

describe('displayFeatureMessage', () => {
  it('translates embedded server status tokens without rewriting the explanation', () => {
    expect(displayFeatureMessage('action unavailable while feature status is BuildingKB')).toBe(
      'action unavailable while feature status is Building knowledge base',
    );
  });
});

describe('catalogErrorAction', () => {
  it('uses the shared friendly reason copy for every error surface', () => {
    const snapshot = featureSnapshot({
      actions: [
        {
          id: 'publish',
          enabled: false,
          disabledReasons: [{ code: 'worktree_state_unknown', message: 'raw server wording' }],
        },
      ],
    });
    expect(catalogErrorAction(snapshot, 'publish', 'Retry publish')).toEqual({
      enabled: false,
      label: 'Retry publish',
      disabledReason:
        'Could not read the repository worktrees — check that they still exist and are a valid checkout.',
    });
  });
});

describe('intervention-first dashboard ordering', () => {
  const snapshot = (id: string, status: string, createdAt: string, startEnabled = false) =>
    featureSnapshot({
      id,
      name: id,
      status,
      createdAt,
      setup: { status: 'done', attempt: 1, tasks: [] },
      actions: startEnabled ? [{ id: 'start', enabled: true, disabledReasons: [] }] : [],
    });

  it('orders intervention, active, startable, and inactive buckets newest-first', () => {
    const features = [
      snapshot('done', 'Done', '2026-07-15T08:00:00Z'),
      snapshot('start-old', 'Created', '2026-07-11T08:00:00Z', true),
      snapshot('active', 'Implementing', '2026-07-10T08:00:00Z'),
      featureSnapshot({
        ...snapshot('failed-old', 'Failed', '2026-07-12T08:00:00Z'),
        errors: [ownedError('run', 'blocking', 'failed-old')],
      }),
      snapshot('review', 'PlanNeedsReview', '2026-07-14T08:00:00Z'),
      snapshot('input', 'NeedUserInput', '2026-07-13T08:00:00Z'),
      featureSnapshot({
        ...snapshot('failed-new', 'Failed', '2026-07-15T08:00:00Z'),
        errors: [ownedError('run', 'blocking', 'failed-new')],
      }),
      snapshot('start-new', 'ImplementReady', '2026-07-14T08:00:00Z', true),
    ];

    expect(orderDashboardFeatures(features).map((feature) => feature.id)).toStrictEqual([
      'failed-new',
      'review',
      'input',
      'failed-old',
      'active',
      'start-new',
      'start-old',
      'done',
    ]);
  });

  it('labels blocked and operational states without recreating start eligibility', () => {
    expect(
      dashboardState(
        featureSnapshot({
          ...snapshot('failed', 'Failed', '2026-07-15T08:00:00Z'),
          errors: [ownedError('run', 'blocking', 'failed')],
        }),
      ),
    ).toStrictEqual({
      bucket: 'intervention',
      label: 'Failed',
      tone: 'danger',
    });
    expect(
      dashboardState(
        featureSnapshot({
          ...snapshot('parked', 'Published', '2026-07-15T08:00:00Z'),
          errors: [ownedError('transaction', 'needs_action', 'child1234ef567890')],
        }),
      ),
    ).toStrictEqual({
      bucket: 'intervention',
      label: 'Needs your action',
      tone: 'attention',
    });
    expect(
      dashboardState(snapshot('review', 'ResearchNeedsReview', '2026-07-15T08:00:00Z')),
    ).toStrictEqual({ bucket: 'intervention', label: 'Review needed', tone: 'attention' });
    expect(
      dashboardState(snapshot('ready', 'UnexpectedStatus', '2026-07-15T08:00:00Z', true)),
    ).toStrictEqual({ bucket: 'startable', label: 'Ready to start', tone: 'ready' });
  });

  it('derives the highest-severity owned error regardless of list order', () => {
    const blocking = ownedError('run', 'blocking', 'feature-1');
    const needsAction = ownedError('transaction', 'needs_action', 'child1234ef567890');
    expect(highestSeverityError([needsAction, blocking])).toBe(blocking);
    expect(highestSeverityError([needsAction])).toBe(needsAction);
    expect(highestSeverityError([])).toBeUndefined();
  });

  it('surfaces an active refactor pass as Refactoring in the in-progress group', () => {
    const child = {
      id: 'child1234ef567890',
      name: 'Slop removal pass',
      kind: 'refactor',
      displayToken: 'refactor:child1234ef567890',
      displayState: 'Active — Implementing',
      pipeline: 'large',
      status: 'Implementing',
      startedAt: '2026-07-30T10:00:00Z',
      cost: { totalUsd: 0, byPhase: {} },
      integrationState: 'pending',
      warnings: [],
    };
    const refactoring = featureSnapshot({ status: 'Published', activeChild: child });
    expect(dashboardState(refactoring)).toStrictEqual({
      bucket: 'active',
      label: 'Refactoring',
      tone: 'active',
    });
    // A parked pass arrives as the parent's needs_action transaction entry.
    expect(
      dashboardState(
        featureSnapshot({
          status: 'Published',
          activeChild: { ...child, integrationState: 'attention' },
          errors: [ownedError('transaction', 'needs_action', 'child1234ef567890')],
        }),
      ),
    ).toStrictEqual({
      bucket: 'intervention',
      label: 'Needs your action',
      tone: 'attention',
    });
    // A pass that was created but never started must not claim to be running.
    expect(
      dashboardState(
        featureSnapshot({ status: 'Published', activeChild: { ...child, status: 'Created' } }),
      ),
    ).toStrictEqual({
      bucket: 'startable',
      label: 'Pass ready to start',
      tone: 'ready',
    });
    // A failed pass arrives as the parent's child-keyed blocking entry.
    expect(
      dashboardState(
        featureSnapshot({
          status: 'Published',
          activeChild: { ...child, status: 'Failed' },
          errors: [ownedError('run', 'blocking', 'child1234ef567890')],
        }),
      ),
    ).toStrictEqual({
      bucket: 'intervention',
      label: 'Failed',
      tone: 'danger',
    });
  });

  it('places a child on the spine only when its status names a phase', () => {
    const stages = spineStages('large');
    expect(childStatusSpineIndex('SettingUpWorktrees', stages)).toBe(0);
    // Created = not started: -1 renders the rail with no needle and no fill.
    expect(childStatusSpineIndex('Created', stages)).toBe(-1);
    expect(childStatusSpineIndex('Implementing', stages)).toBe(
      stages.findIndex((stage) => stage.label === 'Implement'),
    );
    expect(childStatusSpineIndex('FinalReviewNeedsReview', stages)).toBe(
      stages.findIndex((stage) => stage.label === 'Review'),
    );
    // Paused/waiting states carry no phase; an approximate needle would lie.
    expect(childStatusSpineIndex('Interrupted', stages)).toBeNull();
    expect(childStatusSpineIndex('NeedUserInput', stages)).toBeNull();
  });
});

describe('formatDuration', () => {
  it('scales the unit to the magnitude', () => {
    expect(formatDuration(45)).toBe('45s');
    expect(formatDuration(760)).toBe('12m 40s');
    expect(formatDuration(2462)).toBe('41m 02s');
    expect(formatDuration(4811)).toBe('1h 20m');
    expect(formatDuration(5400)).toBe('1h 30m');
    expect(formatDuration(0)).toBe('0s');
    expect(formatDuration(-5)).toBe('0s');
  });
});

describe('formatElapsed', () => {
  it('formats run time and hides it when there is none', () => {
    expect(formatElapsed(featureSnapshot({ timing: { totalSeconds: 760 } }))).toBe('12m 40s');
    expect(formatElapsed(featureSnapshot({ timing: { totalSeconds: 0 } }))).toBeNull();
    expect(formatElapsed(featureSnapshot({}))).toBeNull();
  });
});

describe('spineStages', () => {
  it('prepends Setup to the medium profile phases', () => {
    expect(spineStages('medium').map((stage) => stage.label)).toEqual([
      'Setup',
      'Plan',
      'Implement',
      'Review',
      'Publish',
    ]);
  });

  it('uses the full phase order for large/moonshot/unknown profiles', () => {
    for (const pipeline of ['large', 'moonshot', undefined]) {
      expect(spineStages(pipeline).map((stage) => stage.label)).toEqual([
        'Setup',
        'Knowledge Base',
        'Inquire',
        'Research',
        'Design',
        'Plan',
        'Implement',
        'Review',
        'Publish',
      ]);
    }
  });
});

describe('displayPhaseLabel', () => {
  it('uses cockpit labels for known storage phase ids and preserves unknown values', () => {
    expect(displayPhaseLabel('knowledge-base')).toBe('Knowledge Base');
    expect(displayPhaseLabel('implement')).toBe('Implement');
    expect(displayPhaseLabel('History-Run-6.Md')).toBe('History-Run-6.Md');
  });
});

describe('spineActiveIndex', () => {
  it('points at Setup while durable setup runs or fails', () => {
    const stages = spineStages('medium');
    expect(spineActiveIndex(featureSnapshot(), stages)).toBe(0);
    const failed = featureSnapshot({
      status: 'Failed',
      setup: { status: 'failed', attempt: 1, tasks: [] },
    });
    expect(spineActiveIndex(failed, stages)).toBe(0);
    expect(spineTone(failed)).toBe('error');
  });

  it('points at the next phase once the feature is Created and setup is done', () => {
    const stages = spineStages('medium');
    const created = featureSnapshot({
      status: 'Created',
      setup: { status: 'done', attempt: 1, tasks: [] },
    });
    expect(spineActiveIndex(created, stages)).toBe(1);
    expect(spineTone(created)).toBe('progress');
  });

  it('follows the server-reported current phase for later lifecycles', () => {
    const stages = spineStages('medium');
    const implementing = featureSnapshot({
      status: 'Implementing',
      currentPhase: 'Implement',
      setup: { status: 'done', attempt: 1, tasks: [] },
    });
    expect(spineActiveIndex(implementing, stages)).toBe(2);
  });

  it('uses the same phase mapping for archived and live cockpit spines', () => {
    const stages = spineStages('large');
    expect(spineActiveIndexForPhase('Final Review', stages)).toBe(7);
    expect(spineActiveIndexForPhase('Implement', stages)).toBe(6);
    expect(spineActiveIndexForPhase('implement', stages)).toBe(6);
    expect(spineActiveIndexForPhase('Unknown', stages)).toBe(0);
  });
});

describe('setupProgress / featureBranch / actions', () => {
  it('counts completed tasks against the server-owned total', () => {
    const snapshot = featureSnapshot();
    expect(setupProgress(snapshot.setup!)).toEqual({ done: 1, total: 2 });
  });

  it('surfaces the branch recorded on the setup tasks', () => {
    expect(featureBranch(featureSnapshot())).toBe('feature/search-revamp');
    expect(featureBranch(featureSnapshot({ setup: undefined }))).toBeNull();
  });

  it('reads actions from the authoritative catalogue only', () => {
    const snapshot = featureSnapshot();
    expect(actionById(snapshot, 'start')?.enabled).toBe(false);
    expect(actionById(snapshot, 'unknown')).toBeUndefined();
  });
});

describe('isReadyToStart', () => {
  it('uses only the server-enabled start action', () => {
    const ready = featureSnapshot({
      status: 'Created',
      setup: { status: 'done', attempt: 1, tasks: [] },
      actions: [{ id: 'start', enabled: true, disabledReasons: [] }],
    });
    expect(isReadyToStart(ready)).toBe(true);
    expect(
      isReadyToStart(
        featureSnapshot({
          status: 'UnexpectedNewServerStatus',
          setup: undefined,
          actions: [{ id: 'start', enabled: true, disabledReasons: [] }],
        }),
      ),
    ).toBe(true);
    expect(isReadyToStart(featureSnapshot())).toBe(false);
    expect(
      isReadyToStart(
        featureSnapshot({
          status: 'Created',
          setup: { status: 'done', attempt: 1, tasks: [] },
          actions: [{ id: 'start', enabled: false, disabledReasons: [] }],
        }),
      ),
    ).toBe(false);
  });
});

describe('isRunAtRest', () => {
  it('treats only finished-run statuses as resting', () => {
    expect(isRunAtRest('CodeReady')).toBe(true);
    expect(isRunAtRest('Published')).toBe(true);
    expect(isRunAtRest('Done')).toBe(true);
  });

  it('keeps active and parked statuses at their phase', () => {
    for (const status of [
      'Implementing',
      'FinalReviewing',
      'NeedUserInput',
      'PlanNeedsReview',
      'Interrupted',
      'Failed',
      'PlanReady',
      'ImplementReady',
    ]) {
      expect(isRunAtRest(status)).toBe(false);
    }
  });
});

describe('runningPhaseSubline', () => {
  it('renders the bare phase when there is no roadmap or iteration data', () => {
    expect(runningPhaseSubline('Implement', undefined, undefined, undefined)).toBe('Implement');
  });

  it('appends the iteration when there is no roadmap', () => {
    expect(runningPhaseSubline('Implement', undefined, undefined, 3)).toBe(
      'Implement · iteration 3',
    );
  });

  it('appends the roadmap phase-of-total when there is no iteration', () => {
    expect(runningPhaseSubline('Implement', 2, 5, undefined)).toBe('Implement · phase 2/5');
  });

  it('appends both the roadmap phase-of-total and the iteration when both are present', () => {
    expect(runningPhaseSubline('Implement', 2, 5, 3)).toBe('Implement · phase 2/5 · iteration 3');
  });

  it('never renders a placeholder for a missing phase', () => {
    expect(runningPhaseSubline(undefined, 2, 5, 3)).toBeUndefined();
    expect(runningPhaseSubline('', 2, 5, 3)).toBeUndefined();
  });

  it('omits the roadmap fragment unless both phase-of-total numbers are present', () => {
    expect(runningPhaseSubline('Implement', 2, undefined, undefined)).toBe('Implement');
    expect(runningPhaseSubline('Implement', undefined, 5, undefined)).toBe('Implement');
  });
});

describe('fieldForCreationError', () => {
  it('routes structured rejections to the owning control', () => {
    expect(fieldForCreationError({ code: 'not_ready', summary: 'runtime is not ready' })).toBe(
      'form',
    );
    expect(fieldForCreationError({ code: 'bad_request', summary: 'name is required' })).toBe(
      'name',
    );
    expect(fieldForCreationError({ code: 'bad_request', summary: 'unknown repo "x"' })).toBe(
      'repos',
    );
    expect(
      fieldForCreationError({
        code: 'E_HTTP_REJECTED',
        summary: 'The runtime rejected the request (HTTP 500).',
      }),
    ).toBe('form');
  });
});
