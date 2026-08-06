import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { AttentionItem } from '../../../shared/ipc';
import { ACTIVE_STATUSES } from './featureView';
import {
  classifyHold,
  formatWaitingDuration,
  railSegmentLabel,
  railSegments,
  railTrio,
  type RailHold,
} from './phaseRail';
import { featureSnapshot } from '../test/agenticoMock';

const NOW = '2026-08-06T12:00:00.000Z';

function permissionItem(overrides: Partial<Extract<AttentionItem, { kind: 'permission' }>> = {}) {
  return {
    kind: 'permission',
    id: 'perm-1',
    featureId: 'abcd1234ef567890',
    toolName: 'Bash',
    waitingSince: '2026-08-06T11:55:00.000Z',
    ...overrides,
  } satisfies Extract<AttentionItem, { kind: 'permission' }>;
}

function questionsItem(overrides: Partial<Extract<AttentionItem, { kind: 'questions' }>> = {}) {
  return {
    kind: 'questions',
    id: 'questions-1',
    featureId: 'abcd1234ef567890',
    waitingSince: '2026-08-06T11:50:00.000Z',
    questions: [{ key: 'k', header: 'h', multiSelect: false, options: [] }],
    ...overrides,
  } satisfies Extract<AttentionItem, { kind: 'questions' }>;
}

function helpItem(overrides: Partial<Extract<AttentionItem, { kind: 'help' }>> = {}) {
  return {
    kind: 'help',
    id: 'help-1',
    featureId: 'abcd1234ef567890',
    waitingSince: '2026-08-06T11:45:00.000Z',
    prompt: 'What now?',
    ...overrides,
  } satisfies Extract<AttentionItem, { kind: 'help' }>;
}

function reviewItem(overrides: Partial<Extract<AttentionItem, { kind: 'review' }>> = {}) {
  return {
    kind: 'review',
    id: 'review-1',
    featureId: 'abcd1234ef567890',
    waitingSince: '2026-08-06T11:40:00.000Z',
    reviewKind: 'Phase plan',
    phase: 'plan',
    ...overrides,
  } satisfies Extract<AttentionItem, { kind: 'review' }>;
}

function recoveryItem(overrides: Partial<Extract<AttentionItem, { kind: 'recovery' }>> = {}) {
  return {
    kind: 'recovery',
    id: 'recovery-1',
    waitingSince: '2026-08-06T00:00:00.000Z',
    liveCount: 1,
    deadCount: 0,
    ...overrides,
  } satisfies Extract<AttentionItem, { kind: 'recovery' }>;
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date(NOW));
});

afterEach(() => {
  vi.useRealTimers();
});

describe('classifyHold', () => {
  it('never holds on Failed, even with a stale attention item open', () => {
    expect(classifyHold('Failed', [permissionItem()])).toBeNull();
    expect(classifyHold('Failed', [])).toBeNull();
  });

  describe('active statuses', () => {
    for (const status of ACTIVE_STATUSES) {
      it(`${status}: waits while a human-blocking item is open`, () => {
        const hold = classifyHold(status, [permissionItem()]);
        expect(hold).toEqual({ kind: 'waiting', waitingSince: '2026-08-06T11:55:00.000Z' });
      });

      it(`${status}: is not a hold with no open item`, () => {
        expect(classifyHold(status, [])).toBeNull();
      });

      it(`${status}: is not a hold when only a non-human-blocking item is open`, () => {
        expect(classifyHold(status, [reviewItem()])).toBeNull();
      });
    }
  });

  it('active status waiting takes the oldest open human-blocking item', () => {
    const hold = classifyHold('Implementing', [permissionItem(), helpItem(), questionsItem()]);
    expect(hold).toEqual({ kind: 'waiting', waitingSince: '2026-08-06T11:45:00.000Z' });
  });

  it('NeedUserInput pauses', () => {
    expect(classifyHold('NeedUserInput', [])).toEqual({ kind: 'paused' });
  });

  it('NeedUserInput pauses with the open item duration when one is present', () => {
    expect(classifyHold('NeedUserInput', [reviewItem()])).toEqual({
      kind: 'paused',
      waitingSince: '2026-08-06T11:40:00.000Z',
    });
  });

  it('a *NeedsReview checkpoint pauses', () => {
    expect(classifyHold('PhasePlanNeedsReview', [reviewItem()])).toEqual({
      kind: 'paused',
      waitingSince: '2026-08-06T11:40:00.000Z',
    });
  });

  it('a *NeedsReview checkpoint pauses even with no open item carrying a duration', () => {
    expect(classifyHold('ResearchNeedsReview', [])).toEqual({ kind: 'paused' });
  });

  it('Interrupted pauses without a duration when no open item has one', () => {
    expect(classifyHold('Interrupted', [])).toEqual({ kind: 'paused' });
  });

  it('Interrupted pauses with a duration when an open item carries one', () => {
    expect(classifyHold('Interrupted', [helpItem()])).toEqual({
      kind: 'paused',
      waitingSince: '2026-08-06T11:45:00.000Z',
    });
  });

  it('at-rest statuses are never a hold', () => {
    for (const status of ['CodeReady', 'Published', 'Done']) {
      expect(classifyHold(status, [permissionItem()])).toBeNull();
    }
  });

  it('Created is never a hold', () => {
    expect(classifyHold('Created', [permissionItem()])).toBeNull();
  });

  it('setup-in-progress (SettingUpWorktrees) follows the active-status rule', () => {
    expect(classifyHold('SettingUpWorktrees', [])).toBeNull();
    expect(classifyHold('SettingUpWorktrees', [helpItem()])).toEqual({
      kind: 'waiting',
      waitingSince: '2026-08-06T11:45:00.000Z',
    });
  });

  it('ignores recovery items entirely — they carry no feature-specific hold', () => {
    expect(classifyHold('Implementing', [recoveryItem()])).toBeNull();
    expect(classifyHold('NeedUserInput', [recoveryItem()])).toEqual({ kind: 'paused' });
  });
});

describe('railSegments', () => {
  it('marks completed/current/upcoming from the snapshot spine', () => {
    const snapshot = featureSnapshot({
      status: 'Implementing',
      currentPhase: 'Implement',
      pipeline: 'medium',
      setup: undefined,
    });
    const segments = railSegments(snapshot, null);
    expect(segments.map((segment) => segment.id)).toEqual([
      'setup',
      'plan',
      'implement',
      'review',
      'publish',
    ]);
    expect(segments.map((segment) => segment.state)).toEqual([
      'completed',
      'completed',
      'current',
      'upcoming',
      'upcoming',
    ]);
    expect(segments.every((segment) => !segment.held)).toBe(true);
  });

  it('renders Final Review as the held current Review segment, never a tenth segment', () => {
    const snapshot = featureSnapshot({
      status: 'FinalReviewing',
      currentPhase: 'Final Review',
      pipeline: 'large',
      setup: undefined,
    });
    const hold: RailHold = { kind: 'waiting', waitingSince: '2026-08-06T11:00:00.000Z' };
    const segments = railSegments(snapshot, hold);
    expect(segments).toHaveLength(9);
    const review = segments.find((segment) => segment.id === 'review');
    expect(review?.state).toBe('current');
    expect(review?.held).toBe(true);
    expect(review?.accessibleName).toBe('Review, held');
    expect(segments.some((segment) => segment.label === 'Final Review')).toBe(false);
  });

  it('reads the current position as completed once the run is at rest', () => {
    const snapshot = featureSnapshot({
      status: 'CodeReady',
      currentPhase: 'Implement',
      pipeline: 'medium',
      setup: undefined,
    });
    const segments = railSegments(snapshot, null);
    const implement = segments.find((segment) => segment.id === 'implement');
    expect(implement?.state).toBe('completed');
  });

  it('shortens Knowledge Base at rail scale via the shared label helper', () => {
    const snapshot = featureSnapshot({
      status: 'BuildingKB',
      currentPhase: 'Knowledge Base',
      pipeline: 'large',
      setup: undefined,
    });
    const segments = railSegments(snapshot, null);
    const kb = segments.find((segment) => segment.id === 'knowledge-base');
    expect(kb?.label).toBe('Knowledge');
  });
});

describe('railSegmentLabel', () => {
  it('shortens Knowledge Base to Knowledge', () => {
    expect(railSegmentLabel('Knowledge Base')).toBe('Knowledge');
  });

  it('passes every other label through verbatim', () => {
    for (const label of [
      'Setup',
      'Inquire',
      'Research',
      'Design',
      'Plan',
      'Implement',
      'Review',
      'Publish',
    ]) {
      expect(railSegmentLabel(label)).toBe(label);
    }
  });
});

describe('formatWaitingDuration', () => {
  it('renders the Nm/Nh/Nd shape shared with the attention inbox', () => {
    expect(formatWaitingDuration('2026-08-06T11:59:30.000Z')).toBe('<1m');
    expect(formatWaitingDuration('2026-08-06T11:45:00.000Z')).toBe('15m');
    expect(formatWaitingDuration('2026-08-06T09:00:00.000Z')).toBe('3h');
    expect(formatWaitingDuration('2026-08-01T12:00:00.000Z')).toBe('5d');
  });

  it('returns null for a missing or unparseable timestamp', () => {
    expect(formatWaitingDuration(undefined)).toBeNull();
    expect(formatWaitingDuration('not-a-date')).toBeNull();
  });
});

describe('railTrio', () => {
  it('assembles Elapsed/Cost/Context when not held, omitting absent data', () => {
    const entries = railTrio({
      totalSeconds: 125,
      totalUsd: 4.5,
      contextPercentage: 42,
      hold: null,
    });
    expect(entries).toEqual([
      { kind: 'elapsed', label: 'Elapsed', value: '2m 05s', attention: false },
      { kind: 'cost', label: 'Cost', value: '$4.50', attention: false },
      { kind: 'context', label: 'Context', value: '42%', attention: false },
    ]);
  });

  it('substitutes Waiting for Context while held, dropping Context but keeping Elapsed and Cost', () => {
    const entries = railTrio({
      totalSeconds: 300,
      totalUsd: 1,
      contextPercentage: 80,
      hold: { kind: 'waiting', waitingSince: '2026-08-06T11:45:00.000Z' },
    });
    expect(entries.map((entry) => entry.kind)).toEqual(['waiting', 'elapsed', 'cost']);
    expect(entries[0]).toEqual({
      kind: 'waiting',
      label: 'Waiting',
      value: '15m',
      attention: true,
    });
    expect(entries.some((entry) => entry.kind === 'context')).toBe(false);
  });

  it('renders Paused with no duration value when no open item carries a waitingSince', () => {
    const entries = railTrio({ totalSeconds: 60, totalUsd: 0, hold: { kind: 'paused' } });
    expect(entries[0]).toEqual({ kind: 'paused', label: 'Paused', value: '', attention: true });
  });

  it('never invents a number: every entry is omitted when its datum is unavailable', () => {
    const entries = railTrio({ hold: null });
    expect(entries).toEqual([]);
  });

  it('omits Elapsed and Cost individually when only one is available', () => {
    expect(railTrio({ totalUsd: 2, hold: null })).toEqual([
      { kind: 'cost', label: 'Cost', value: '$2.00', attention: false },
    ]);
    expect(railTrio({ totalSeconds: 10, hold: null })).toEqual([
      { kind: 'elapsed', label: 'Elapsed', value: '10s', attention: false },
    ]);
  });
});
