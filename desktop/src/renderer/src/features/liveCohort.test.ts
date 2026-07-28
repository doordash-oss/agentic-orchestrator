import { describe, expect, it } from 'vitest';
import type { SessionSummary } from '../../../shared/ipc';
import {
  EMPTY_COHORT,
  cohortSections,
  cohortTabLabels,
  cohortTabStatus,
  computeCohort,
  resolveCohortSelection,
} from './liveCohort';

function session(overrides: Partial<SessionSummary> & Pick<SessionSummary, 'id'>): SessionSummary {
  return {
    featureId: 'feat',
    runNumber: 1,
    phase: 'implement',
    kind: 'validator',
    status: 'running',
    startedAt: '2026-07-22T00:00:00Z',
    taskActivities: [],
    runningTaskCount: 0,
    usage: {},
    ...overrides,
  };
}

describe('computeCohort', () => {
  it('starts with every active non-chat session and ignores chat', () => {
    const cohort = computeCohort(
      EMPTY_COHORT,
      [
        session({ id: 'impl', kind: 'repo-impl' }),
        session({ id: 'val', kind: 'validator', label: 'Craft' }),
        session({ id: '__chat__', kind: 'chat' }),
      ],
      'implement',
    );
    expect(cohort.sessionIds).toEqual(['impl', 'val']);
    expect(cohort.phase).toBe('implement');
  });

  it('retains a completed agent within the same phase (individual completion)', () => {
    const sessions = [
      session({ id: 'a', label: 'Craft', status: 'completed' }),
      session({ id: 'b', label: 'Security', status: 'running' }),
    ];
    const cohort = computeCohort(
      { sessionIds: ['a', 'b'], phase: 'implement' },
      sessions,
      'implement',
    );
    expect(cohort.sessionIds).toContain('a');
    expect(cohort.sessionIds).toContain('b');
  });

  it('adds a newly started parallel agent to the existing cohort', () => {
    const cohort = computeCohort(
      { sessionIds: ['a'], phase: 'implement' },
      [session({ id: 'a', status: 'running' }), session({ id: 'b', status: 'running' })],
      'implement',
    );
    expect(new Set(cohort.sessionIds)).toEqual(new Set(['a', 'b']));
  });

  it('keeps a fully-completed cohort when no new batch has started', () => {
    const sessions = [
      session({ id: 'a', status: 'completed' }),
      session({ id: 'b', status: 'failed' }),
    ];
    const cohort = computeCohort(
      { sessionIds: ['a', 'b'], phase: 'implement' },
      sessions,
      'implement',
    );
    expect(new Set(cohort.sessionIds)).toEqual(new Set(['a', 'b']));
  });

  it('replaces a terminal cohort with a disjoint active retry batch', () => {
    const sessions = [
      session({ id: 'a', status: 'completed' }),
      session({ id: 'b', status: 'completed' }),
      session({ id: 'c', status: 'running' }),
    ];
    const cohort = computeCohort(
      { sessionIds: ['a', 'b'], phase: 'implement' },
      sessions,
      'implement',
    );
    expect(cohort.sessionIds).toEqual(['c']);
  });

  it('still shows agents for a run with no active sessions (failed or sealed)', () => {
    const cohort = computeCohort(
      EMPTY_COHORT,
      [session({ id: 'a', status: 'failed' }), session({ id: 'b', status: 'completed' })],
      'implement',
    );
    expect(new Set(cohort.sessionIds)).toEqual(new Set(['a', 'b']));
  });

  it('resets retention when the feature phase changes', () => {
    const sessions = [
      session({ id: 'a', status: 'completed' }),
      session({ id: 'plan', kind: 'phase', status: 'running', phase: 'plan' }),
    ];
    const cohort = computeCohort({ sessionIds: ['a'], phase: 'implement' }, sessions, 'plan');
    expect(cohort.sessionIds).toEqual(['plan']);
  });
});

describe('resolveCohortSelection', () => {
  it('preserves the current selection while it survives', () => {
    const cohort = [session({ id: 'a' }), session({ id: 'b' })];
    expect(resolveCohortSelection(cohort, 'b')).toBe('b');
  });

  it('prefers an active agent when the selection is gone', () => {
    const cohort = [
      session({ id: 'a', status: 'completed' }),
      session({ id: 'b', status: 'running' }),
    ];
    expect(resolveCohortSelection(cohort, 'stale')).toBe('b');
  });

  it('falls back to the first tab when all are terminal', () => {
    const cohort = [
      session({ id: 'a', status: 'completed' }),
      session({ id: 'b', status: 'failed' }),
    ];
    expect(resolveCohortSelection(cohort, null)).toBe('a');
    expect(resolveCohortSelection([], null)).toBeNull();
  });
});

describe('cohortTabLabels', () => {
  it('uses the session label, then repo, then a phase fallback', () => {
    const labels = cohortTabLabels([
      session({ id: 'a', label: 'Craft' }),
      session({ id: 'b', label: undefined, repo: 'web' }),
      session({
        id: 'c',
        label: undefined,
        repo: undefined,
        phase: 'implement',
        kind: 'repo-impl',
      }),
    ]);
    expect(labels.get('a')).toBe('Craft');
    expect(labels.get('b')).toBe('web');
    expect(labels.get('c')).toBe('Implement');
  });

  it('disambiguates duplicate labels by repo, then ordinal', () => {
    const labels = cohortTabLabels([
      session({ id: 'a', label: 'Review', repo: 'web' }),
      session({ id: 'b', label: 'Review', repo: 'api' }),
      session({ id: 'c', label: 'Review', repo: 'web' }),
    ]);
    expect(labels.get('b')).toBe('Review · api');
    expect(labels.get('a')).toBe('Review #1');
    expect(labels.get('c')).toBe('Review #2');
  });
});

describe('cohortSections', () => {
  it('folds an ordered cohort into implementer and review-panel sections', () => {
    const sections = cohortSections([
      session({ id: 'impl', kind: 'phase', phase: 'Implement' }),
      session({ id: 'sec', label: 'Security' }),
      session({ id: 'craft', label: 'Craft' }),
    ]);
    expect(sections.map((entry) => entry.title)).toEqual(['Implementer', 'Review panel']);
    expect(sections[0]?.sessions.map((entry) => entry.id)).toEqual(['impl']);
    expect(sections[1]?.sessions.map((entry) => entry.id)).toEqual(['sec', 'craft']);
  });

  it('keeps a lone-group cohort in a single titled section', () => {
    const sections = cohortSections([
      session({ id: 'a', label: 'Security' }),
      session({ id: 'b', label: 'Craft' }),
    ]);
    expect(sections).toHaveLength(1);
    expect(sections[0]?.title).toBe('Review panel');
  });
});

describe('cohortTabStatus', () => {
  it('maps status strings to coarse markers', () => {
    expect(cohortTabStatus(session({ id: 'a', status: 'running' }))).toBe('running');
    expect(cohortTabStatus(session({ id: 'a', status: 'COMPLETED' }))).toBe('completed');
    expect(cohortTabStatus(session({ id: 'a', status: 'failed' }))).toBe('failed');
  });
});
