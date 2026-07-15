import { describe, expect, it } from 'vitest';
import { featureSnapshot } from '../test/agenticoMock';
import {
  actionById,
  featureBranch,
  fieldForCreationError,
  isReadyToStart,
  setupProgress,
  spineActiveIndex,
  spineStages,
  spineTone,
} from './featureView';

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
  it('requires Created status, completed setup, and a server-enabled start', () => {
    const ready = featureSnapshot({
      status: 'Created',
      setup: { status: 'done', attempt: 1, tasks: [] },
      actions: [{ id: 'start', enabled: true, disabledReasons: [] }],
    });
    expect(isReadyToStart(ready)).toBe(true);
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
