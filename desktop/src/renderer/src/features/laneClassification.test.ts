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
import type { FeatureSnapshot, RelationshipChildView } from '../../../shared/ipc';
import {
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
    attention: [],
    cleanupWarnings: [],
    ...overrides,
  };
}

// Every status the server defines (internal/feature/feature.go Status enum),
// mapped to the lane it should classify into with no active child and
// durable setup already done.
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
  ['Failed', 'waiting'],
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

describe('classifyLane — active child pass', () => {
  it('classifies a Published parent with a running child pass as running, not published', () => {
    const feature = snapshot('Published', { activeChild: child({ status: 'Implementing' }) });
    expect(classifyLane(feature)).toBe('running');
  });

  it('classifies a Published parent with a not-yet-started child pass as published', () => {
    const feature = snapshot('Published', { activeChild: child({ status: 'Created' }) });
    expect(classifyLane(feature)).toBe('published');
  });

  it('classifies a Published parent with a failed child pass as waiting', () => {
    const feature = snapshot('Published', { activeChild: child({ status: 'Failed' }) });
    expect(classifyLane(feature)).toBe('waiting');
  });
});

describe('classifyLane — pending attention', () => {
  it('classifies any feature with a pending child attention entry as waiting, regardless of status', () => {
    const feature = snapshot('CodeReady', {
      activeChild: child({
        status: 'Implementing',
        attention: [{ code: 'x', message: 'needs a look' }],
      }),
    });
    expect(classifyLane(feature)).toBe('waiting');
  });

  it('classifies an active child integration state of "attention" as waiting', () => {
    const feature = snapshot('Published', {
      activeChild: child({ status: 'Implementing', integrationState: 'attention' }),
    });
    expect(classifyLane(feature)).toBe('waiting');
  });

  // Most adversarial representable combination: the schema allows `status`
  // and `activeChild` to vary independently, so a parent already marked
  // Done can still carry an unresolved child pass. Attention wins.
  it('classifies a Done parent with a pending child attention entry as waiting', () => {
    const feature = snapshot('Done', {
      activeChild: child({
        status: 'Implementing',
        attention: [{ code: 'x', message: 'needs a look' }],
      }),
    });
    expect(classifyLane(feature)).toBe('waiting');
  });
});

describe('classifyFeaturesByLane', () => {
  it('groups features into every lane key, ordered newest-first within each lane', () => {
    const features = [
      snapshot('Done', { id: 'done-old', createdAt: '2026-07-10T08:00:00Z' }),
      snapshot('Failed', { id: 'failed-new', createdAt: '2026-07-15T08:00:00Z' }),
      snapshot('Published', { id: 'published-old', createdAt: '2026-07-11T08:00:00Z' }),
      snapshot('Implementing', { id: 'running-new', createdAt: '2026-07-14T08:00:00Z' }),
      snapshot('Failed', { id: 'failed-old', createdAt: '2026-07-12T08:00:00Z' }),
      snapshot('CodeReady', { id: 'rest-new', createdAt: '2026-07-13T08:00:00Z' }),
    ];

    const grouped = classifyFeaturesByLane(features);

    expect(Object.keys(grouped).sort()).toStrictEqual(
      ['at-rest', 'done', 'published', 'running', 'waiting'].sort(),
    );
    expect(grouped.waiting.map((f) => f.id)).toStrictEqual(['failed-new', 'failed-old']);
    expect(grouped.running.map((f) => f.id)).toStrictEqual(['running-new']);
    expect(grouped.published.map((f) => f.id)).toStrictEqual(['published-old']);
    expect(grouped.done.map((f) => f.id)).toStrictEqual(['done-old']);
    expect(grouped['at-rest'].map((f) => f.id)).toStrictEqual(['rest-new']);
  });

  it('returns empty arrays for lanes with no features', () => {
    const grouped = classifyFeaturesByLane([snapshot('Done')]);
    expect(grouped.waiting).toStrictEqual([]);
    expect(grouped.running).toStrictEqual([]);
    expect(grouped.published).toStrictEqual([]);
    expect(grouped['at-rest']).toStrictEqual([]);
  });
});

describe('laneCounts', () => {
  it('counts features per lane using the same classification', () => {
    const features = [
      snapshot('Done'),
      snapshot('Done'),
      snapshot('Failed'),
      snapshot('Implementing'),
      snapshot('Published'),
      snapshot('CodeReady'),
    ];

    expect(laneCounts(features)).toStrictEqual({
      waiting: 1,
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

  it('returns the base classification untouched when nothing has pending attention', () => {
    const running = snapshot('Implementing', { id: 'running-1' });
    const base = classifyFeaturesByLane([running]);
    const grouped = classifyFeaturesByLaneWithAttention([running], new Map());
    expect(grouped).toStrictEqual(base);
  });
});

describe('purity', () => {
  it('returns the same result for the same input and never mutates the snapshot', () => {
    const feature = snapshot('Published', { activeChild: child({ status: 'Implementing' }) });
    const before = JSON.parse(JSON.stringify(feature));

    const first = classifyLane(feature);
    const second = classifyLane(feature);

    expect(first).toBe(second);
    expect(feature).toStrictEqual(before);
  });

  it('does not mutate inputs when grouping or counting a batch', () => {
    const features = [snapshot('Done'), snapshot('Failed'), snapshot('Implementing')];
    const before = JSON.parse(JSON.stringify(features));

    classifyFeaturesByLane(features);
    laneCounts(features);

    expect(features).toStrictEqual(before);
  });
});
