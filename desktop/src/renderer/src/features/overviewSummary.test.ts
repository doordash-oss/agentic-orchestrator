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

import { describe, expect, it, vi } from 'vitest';
import type { AttentionItem, FeatureSnapshot } from '../../../shared/ipc';
import { featureSnapshot } from '../test/agenticoMock';
import { classifyFeaturesByLane, laneCounts, type LaneGroups } from './laneClassification';
import { overviewHeadline, overviewSubline } from './overviewSummary';

function snapshot(status: string, overrides: Partial<FeatureSnapshot> = {}): FeatureSnapshot {
  return featureSnapshot({
    status,
    setup: { status: 'done', attempt: 1, tasks: [] },
    ...overrides,
  });
}

function emptyLanes(): LaneGroups {
  return classifyFeaturesByLane([]);
}

describe('overviewHeadline', () => {
  it('empty workspace', () => {
    expect(overviewHeadline(laneCounts([]), 0)).toBe('Turn a goal into a supervised run.');
  });

  it('running and waiting, plural', () => {
    const counts = laneCounts([
      snapshot('Implementing'),
      snapshot('Implementing'),
      snapshot('NeedUserInput'),
    ]);
    expect(overviewHeadline(counts, 3)).toBe('Two runs in flight, one waiting on you');
  });

  it('waiting only, singular', () => {
    const counts = laneCounts([snapshot('NeedUserInput')]);
    expect(overviewHeadline(counts, 1)).toBe('One feature waiting on you');
  });

  it('running only, singular', () => {
    const counts = laneCounts([snapshot('Implementing')]);
    expect(overviewHeadline(counts, 1)).toBe('One run in flight');
  });

  it('idle with features', () => {
    const counts = laneCounts([snapshot('CodeReady')]);
    expect(overviewHeadline(counts, 1)).toBe('Nothing needs you right now');
  });
});

describe('overviewSubline', () => {
  it('empty workspace: preserved empty-state description', () => {
    expect(overviewSubline(emptyLanes(), [], 0)).toBe(
      'Define the work, choose its repositories, set the pipeline, then review the exact run contract before anything is created.',
    );
  });

  it('waiting: uses the oldest pending attention item wait duration', () => {
    vi.useFakeTimers();
    try {
      vi.setSystemTime(new Date('2026-08-05T12:00:00Z'));
      const now = Date.now();
      const failing = snapshot('NeedUserInput', { id: 'waiting-1' });
      const lanes = classifyFeaturesByLane([failing]);
      const items: AttentionItem[] = [
        {
          kind: 'help',
          id: 'a1',
          featureId: 'waiting-1',
          waitingSince: new Date(now - 12 * 60 * 1000).toISOString(),
          prompt: 'need input',
        },
        {
          kind: 'help',
          id: 'a2',
          featureId: 'waiting-1',
          waitingSince: new Date(now - 3 * 60 * 1000).toISOString(),
          prompt: 'need input',
        },
      ];
      expect(overviewSubline(lanes, items, 1)).toBe('The oldest has been waiting 12m 00s.');
    } finally {
      vi.useRealTimers();
    }
  });

  it('waiting with no usable timestamp falls through to the resting-lane tier', () => {
    const failing = snapshot('NeedUserInput', { id: 'waiting-1' });
    const rested = snapshot('CodeReady', { id: 'rest-1' });
    const lanes = classifyFeaturesByLane([failing, rested]);
    expect(overviewSubline(lanes, [], 2)).toBe('One feature at rest.');
  });

  it('running: "Nothing has stalled" plus elapsed and cost when both are known', () => {
    const running = snapshot('Implementing', {
      id: 'run-1',
      timing: { totalSeconds: 2 * 3600 + 55 * 60 },
      activeChild: {
        id: 'child1234ef567890',
        name: 'Refactor pass',
        kind: 'refactor',
        displayToken: 'refactor:child1234ef567890',
        displayState: 'Active — Implementing',
        pipeline: 'large',
        status: 'Implementing',
        startedAt: '2026-07-30T10:00:00Z',
        cost: { totalUsd: 3.08, byPhase: {} },
        integrationState: 'pending',
        warnings: [],
      },
    });
    const lanes = classifyFeaturesByLane([running]);
    expect(overviewSubline(lanes, [], 1)).toBe(
      'Nothing has stalled. The oldest run has been going 2h 55m and has cost $3.08.',
    );
  });

  it('running: drops the cost clause when the run carries no cost data', () => {
    const running = snapshot('Implementing', {
      id: 'run-1',
      timing: { totalSeconds: 2 * 3600 + 55 * 60 },
    });
    const lanes = classifyFeaturesByLane([running]);
    expect(overviewSubline(lanes, [], 1)).toBe(
      'Nothing has stalled. The oldest run has been going 2h 55m.',
    );
  });

  it('running: drops both elapsed and cost clauses when neither is known', () => {
    const running = snapshot('Implementing', { id: 'run-1' });
    const lanes = classifyFeaturesByLane([running]);
    expect(overviewSubline(lanes, [], 1)).toBe('Nothing has stalled.');
  });

  it('idle: counts non-empty resting lanes, spelling out small numbers', () => {
    const rested = [1, 2, 3].map((n) => snapshot('CodeReady', { id: `rest-${n}` }));
    const published = [1, 2, 3, 4, 5].map((n) => snapshot('Published', { id: `pub-${n}` }));
    const lanes = classifyFeaturesByLane([...rested, ...published]);
    expect(overviewSubline(lanes, [], 8)).toBe('Three features at rest, five published.');
  });
});
