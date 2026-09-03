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
