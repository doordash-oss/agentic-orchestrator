import { describe, expect, it } from 'vitest';
import { featureSnapshot } from '../test/agenticoMock';
import {
  aftercareActions,
  cyclePresentation,
  resolvePostImplementationMode,
} from './postImplementationModel';

describe('postImplementationModel', () => {
  it.each(['CodeReady', 'Published', 'Done'])('%s resolves to aftercare', (status) => {
    expect(resolvePostImplementationMode(featureSnapshot({ status })).kind).toBe('aftercare');
  });

  it('lets a running cycle own a published feature', () => {
    const mode = resolvePostImplementationMode(
      featureSnapshot({
        status: 'Published',
        cycle: {
          type: 'review-comments',
          status: 'running',
          count: 2,
          iteration: 1,
          phase: 'address_validate',
        },
      }),
    );
    expect(mode.kind).toBe('cycle');
  });

  it('orders publish before available cycle actions', () => {
    const snapshot = featureSnapshot({
      status: 'CodeReady',
      actions: ['refactor', 'review-comments', 'publish', 'rebase'].map((id) => ({
        id,
        enabled: true,
        disabledReasons: [],
      })),
    });
    expect(aftercareActions(snapshot).map((action) => action.id)).toEqual([
      'publish',
      'rebase',
      'review-comments',
      'refactor',
    ]);
  });

  it('projects the server-owned phase into a truthful cycle spine', () => {
    const presentation = cyclePresentation(
      featureSnapshot({
        cycle: {
          type: 'rebase',
          status: 'reviewing',
          count: 2,
          phase: 'final_review',
        },
      }),
    );
    expect(presentation?.current).toBe('Final review');
    expect(presentation?.next).toBe('Publish');
    expect(presentation?.stages.map((stage) => stage.state)).toEqual([
      'done',
      'done',
      'active',
      'upcoming',
    ]);
  });

  it('makes need-user-input the current cycle state', () => {
    expect(
      cyclePresentation(
        featureSnapshot({
          cycle: { type: 'refactor', status: 'need_user_input', phase: 'plan_refactor' },
        }),
      ),
    ).toMatchObject({
      headline: 'Agent is waiting for your input',
      current: 'Waiting for input',
      next: 'Implement & validate',
    });
  });
});
