import { describe, expect, it } from 'vitest';
import { featureSnapshot } from '../test/agenticoMock';
import {
  actionById,
  childStatusSpineIndex,
  dashboardGroupId,
  dashboardState,
  displayFeatureMessage,
  displayModelName,
  displayPhaseLabel,
  displayStatusLabel,
  featureBranch,
  fieldForCreationError,
  formatDuration,
  formatElapsed,
  phaseMetric,
  groupDashboardFeatures,
  isReadyToStart,
  isRunAtRest,
  orderDashboardFeatures,
  setupProgress,
  spineActiveIndex,
  spineActiveIndexForPhase,
  spineStages,
  spineTone,
} from './featureView';

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
      snapshot('failed-old', 'Failed', '2026-07-12T08:00:00Z'),
      snapshot('review', 'PlanNeedsReview', '2026-07-14T08:00:00Z'),
      snapshot('input', 'NeedUserInput', '2026-07-13T08:00:00Z'),
      snapshot('failed-new', 'Failed', '2026-07-15T08:00:00Z'),
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
    expect(dashboardState(snapshot('failed', 'Failed', '2026-07-15T08:00:00Z'))).toStrictEqual({
      bucket: 'intervention',
      label: 'Failed',
      tone: 'danger',
    });
    expect(
      dashboardState(snapshot('review', 'ResearchNeedsReview', '2026-07-15T08:00:00Z')),
    ).toStrictEqual({ bucket: 'intervention', label: 'Review needed', tone: 'attention' });
    expect(
      dashboardState(snapshot('ready', 'UnexpectedStatus', '2026-07-15T08:00:00Z', true)),
    ).toStrictEqual({ bucket: 'startable', label: 'Ready to start', tone: 'ready' });
  });

  it('surfaces an active refactor pass as Refactoring in the in-progress group', () => {
    const child = {
      id: 'child1234ef567890',
      name: 'Slop removal pass',
      kind: 'refactor',
      displayToken: 'refactor:child1234ef567890',
      displayState: 'Active — Created',
      pipeline: 'large',
      status: 'Created',
      startedAt: '2026-07-30T10:00:00Z',
      cost: { totalUsd: 0, byPhase: {} },
      integrationState: 'pending',
      attention: [],
      cleanupWarnings: [],
    };
    const refactoring = featureSnapshot({ status: 'Published', activeChild: child });
    expect(dashboardState(refactoring)).toStrictEqual({
      bucket: 'active',
      label: 'Refactoring',
      tone: 'active',
    });
    expect(dashboardGroupId(refactoring)).toBe('in-progress');
    expect(
      dashboardState(
        featureSnapshot({
          status: 'Published',
          activeChild: { ...child, integrationState: 'attention' },
        }),
      ),
    ).toStrictEqual({
      bucket: 'intervention',
      label: 'Refactoring — needs attention',
      tone: 'attention',
    });
  });

  it('places a child on the spine only when its status names a phase', () => {
    const stages = spineStages('large');
    expect(childStatusSpineIndex('SettingUpWorktrees', stages)).toBe(0);
    expect(childStatusSpineIndex('Created', stages)).toBe(1);
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

  it('groups dashboard rows into in-progress, published, and done sections', () => {
    const features = [
      snapshot('published', 'Published', '2026-07-15T08:00:00Z'),
      snapshot('failed', 'Failed', '2026-07-14T08:00:00Z'),
      snapshot('done', 'Done', '2026-07-13T08:00:00Z'),
      snapshot('ready', 'CodeReady', '2026-07-12T08:00:00Z'),
    ];

    expect(
      groupDashboardFeatures(features).map((group) => ({
        id: group.id,
        label: group.label,
        featureIds: group.features.map((feature) => feature.id),
      })),
    ).toStrictEqual([
      { id: 'in-progress', label: 'In progress', featureIds: ['failed', 'ready'] },
      { id: 'published', label: 'Published', featureIds: ['published'] },
      { id: 'done', label: 'Done', featureIds: ['done'] },
    ]);
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

describe('phaseMetric', () => {
  it('reads the active roadmap implementation and planning accounting keys', () => {
    expect(phaseMetric({ 'phase-5-impl': 760 }, 'Implement', 5)).toBe(760);
    expect(phaseMetric({ 'phase-5-impl': 12.4 }, 'Review', 5)).toBe(12.4);
    expect(phaseMetric({ 'phase-5-plan': 95 }, 'Plan', 5)).toBe(95);
  });

  it('retains phase-name and final-review fallbacks', () => {
    expect(phaseMetric({ Implement: 120, Plan: 30 }, 'Implement')).toBe(120);
    expect(phaseMetric({ implement: 120 }, 'Implement')).toBe(120);
    expect(phaseMetric({ Review: 45 }, 'Final Review')).toBe(45);
    expect(phaseMetric({ Plan: 5 }, 'Implement')).toBeUndefined();
    expect(phaseMetric(undefined, 'Implement', 2)).toBeUndefined();
  });
});

describe('displayModelName', () => {
  const catalogue = {
    providerOrder: ['opencode'],
    providerModels: {
      opencode: [
        {
          id: 'portkey/@fireworks/accounts/fireworks/models/glm-5p2[1.04M]',
          displayName: 'GLM 5.2 (1.04M)',
        },
      ],
    },
    phaseDefaults: {},
    phaseProviderModels: {},
  };

  it('uses catalogue display metadata for bare and provider-qualified ids', () => {
    const model = 'portkey/@fireworks/accounts/fireworks/models/glm-5p2[1.04M]';
    expect(displayModelName(model, catalogue)).toBe('GLM 5.2 (1.04M)');
    expect(displayModelName(`opencode:${model}`, catalogue)).toBe('GLM 5.2 (1.04M)');
  });

  it('preserves colon tags in bare model ids when display metadata is unavailable', () => {
    expect(
      displayModelName('portkey/@fireworks/accounts/fireworks/models/glm-5p2[1.04M]', null),
    ).toBe('glm-5p2[1.04M]');
    expect(displayModelName('ollama/llama3.1:8b', null)).toBe('llama3.1:8b');
    expect(displayModelName('claude-sonnet-5', null)).toBe('claude-sonnet-5');
  });

  it('strips a provider prefix without stripping the canonical colon tag', () => {
    const taggedCatalogue = {
      providerOrder: ['opencode'],
      providerModels: {
        opencode: [{ id: 'ollama/llama3.1:8b', displayName: 'Llama 3.1 8B' }],
      },
      phaseDefaults: {},
      phaseProviderModels: {},
    };

    expect(displayModelName('opencode:ollama/llama3.1:8b', taggedCatalogue)).toBe('Llama 3.1 8B');
    expect(displayModelName('opencode:ollama/llama3.1:8b', null)).toBe('llama3.1:8b');
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

describe('fieldForCreationError', () => {
  it('routes structured rejections to the owning control', () => {
    expect(fieldForCreationError({ code: 'not_ready', message: 'runtime is not ready' })).toBe(
      'form',
    );
    expect(fieldForCreationError({ code: 'bad_request', message: 'name is required' })).toBe(
      'name',
    );
    expect(fieldForCreationError({ code: 'bad_request', message: 'unknown repo "x"' })).toBe(
      'repos',
    );
    expect(fieldForCreationError({ code: 'E_HTTP_500', message: 'server exploded' })).toBe('form');
  });
});
