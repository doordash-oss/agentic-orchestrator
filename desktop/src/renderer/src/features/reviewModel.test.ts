import { describe, expect, it } from 'vitest';
import type { SessionSummary } from '../../../shared/ipc';
import { orderedReviewStatuses, orderRunSessions, selectInitialRunSession } from './reviewModel';

const base: SessionSummary = {
  id: 'base',
  featureId: 'abcd1234ef567890',
  runNumber: 2,
  phase: 'Final Review',
  kind: 'validator',
  status: 'failed',
  startedAt: '2026-07-21T12:00:00Z',
  taskActivities: [],
  runningTaskCount: 0,
  usage: {},
};

describe('reviewModel', () => {
  it('orders implementation before review axes and uses the canonical axis order', () => {
    const sessions = orderRunSessions([
      { ...base, id: 'design', label: 'Design' },
      { ...base, id: 'clean', label: 'Cleanliness' },
      { ...base, id: 'impl', phase: 'Implement', kind: 'phase', label: undefined },
      { ...base, id: 'func', label: 'Functionality/Evidence' },
    ]);

    expect(sessions.map((session) => session.id)).toEqual(['impl', 'func', 'clean', 'design']);
    expect(
      orderedReviewStatuses({ Design: 'running', Craft: 'APPROVED', QA: 'running' }).map(
        ([name]) => name,
      ),
    ).toEqual(['Craft', 'QA', 'Design']);
  });

  it('focuses a running axis instead of an earlier failed axis', () => {
    const sessions = orderRunSessions([
      { ...base, id: 'design', label: 'Design' },
      { ...base, id: 'func', label: 'Functionality/Evidence', status: 'running' },
    ]);

    expect(selectInitialRunSession(sessions)?.id).toBe('func');
  });
});
