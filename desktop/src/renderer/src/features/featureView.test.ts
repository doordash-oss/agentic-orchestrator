import { describe, expect, it } from 'vitest';
import { featureSnapshot } from '../test/agenticoMock';
import {
  actionById,
  dashboardState,
  displayFeatureMessage,
  displayPhaseLabel,
  displayStatusLabel,
  featureBranch,
  fieldForCreationError,
  isReadyToStart,
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
