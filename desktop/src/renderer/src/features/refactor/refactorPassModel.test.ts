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
import { featureSnapshot } from '../../test/agenticoMock';
import type { FeatureSnapshot, RelationshipChildView } from '../../../../shared/ipc';
import {
  custodyStations,
  passActions,
  passKindLabel,
  passState,
  refactoringStatusChip,
} from './refactorPassModel';

function childView(overrides: Partial<RelationshipChildView> = {}): RelationshipChildView {
  return {
    id: 'child1234ef567890',
    name: 'Slop removal pass',
    kind: 'refactor',
    displayToken: 'refactor:child1234ef567890',
    displayState: 'Active — Created',
    pipeline: 'large',
    status: 'Created',
    relationshipState: 'active',
    startedAt: '2026-07-30T10:00:00Z',
    cost: { totalUsd: 0, byPhase: {} },
    integrationState: 'pending',
    warnings: [],
    ...overrides,
  };
}

const doneSetup: FeatureSnapshot['setup'] = { status: 'done', attempt: 1, tasks: [] };

/** Canonical integration-attention error as the renderer receives it. */
const integrationAttention = {
  code: 'integration_merge_conflict',
  class: 'needs_action' as const,
  title: 'Integration merge conflict',
  summary: 'The merge candidate for repository "repo-a" conflicted on 2 files.',
  remediation: {
    hint: 'Resolve the conflict in the pass worktree and retry; the pass re-enters final review if its code changed.',
    actions: ['retry'],
  },
  context: {
    repositories: [{ name: 'repo-a', branch: 'main', conflict_files: ['internal/api.go'] }],
  },
  diagnostics: 'repo-a: merge conflict: [internal/api.go]',
};

describe('passState', () => {
  it('reports setup until the worktrees are ready', () => {
    const state = passState(
      featureSnapshot({ status: 'SettingUpWorktrees', setupComplete: false }),
    );
    expect(state.id).toBe('setup');
    expect(state.sentence).toContain('Start unlocks when setup completes');
  });

  it('reports a failed setup as retryable with no sentence', () => {
    const state = passState(
      featureSnapshot({
        status: 'Failed',
        setupComplete: false,
        setup: { status: 'failed', attempt: 1, tasks: [] },
      }),
    );
    expect(state).toMatchObject({ id: 'setup-failed', tone: 'danger' });
    // The owning task's canonical error renders as the workspace's single
    // full error card; the state sentence stays empty.
    expect(state.sentence).toBe('');
  });

  it('renders the canonical summary for a failed pass and the fallback without a failure record', () => {
    const failed = passState(
      featureSnapshot({
        status: 'Failed',
        failure: {
          code: 'iteration_budget_exhausted',
          class: 'blocking',
          title: 'Iteration budget exhausted',
          summary: 'The Implement phase exhausted its iteration budget at iteration 3.',
          remediation: {
            hint: 'Restart the phase with an extended iteration budget, or revise the plan so the work converges.',
            actions: ['restart'],
          },
          context: { phase: { name: 'implement', iteration: 3 } },
          diagnostics: 'reached maximum iteration count',
        },
      }),
    );
    expect(failed.id).toBe('failed');
    expect(failed.sentence).toBe(
      'The Implement phase exhausted its iteration budget at iteration 3.',
    );

    const noRecord = passState(featureSnapshot({ status: 'Failed' }));
    expect(noRecord.id).toBe('failed');
    expect(noRecord.sentence).toBe('The pass stopped on a failure.');
  });

  it('reports ready when the catalogue enables start', () => {
    const state = passState(
      featureSnapshot({
        status: 'Created',
        setupComplete: true,
        setup: doneSetup,
        actions: [{ id: 'start', enabled: true, disabledReasons: [] }],
      }),
    );
    expect(state).toMatchObject({ id: 'ready', tone: 'quiet' });
    expect(state.sentence).toContain('inherited repository');
  });

  it('reports the live phase while the pass runs', () => {
    const state = passState(
      featureSnapshot({ status: 'Implementing', setupComplete: true, setup: doneSetup }),
    );
    expect(state).toMatchObject({ id: 'working', sentence: 'Implementing.', tone: 'live' });
  });

  it('routes pauses, input requests, and review gates to attention', () => {
    const base = { setupComplete: true, setup: doneSetup };
    expect(passState(featureSnapshot({ ...base, status: 'Interrupted' })).id).toBe('interrupted');
    expect(passState(featureSnapshot({ ...base, status: 'NeedUserInput' })).id).toBe('input');
    expect(passState(featureSnapshot({ ...base, status: 'FinalReviewNeedsReview' })).id).toBe(
      'review',
    );
  });

  it('owns the surface during integration, including the attention park', () => {
    const base = { status: 'ReviewPassed', setupComplete: true, setup: doneSetup };
    expect(
      passState(featureSnapshot({ ...base, transaction: { phase: 'applying' } })),
    ).toMatchObject({ id: 'integrating', tone: 'live' });
    expect(
      passState(featureSnapshot({ ...base, transaction: { phase: 'attention' } })),
    ).toMatchObject({ id: 'integration-attention', tone: 'attention' });
  });
});

describe('passState post-review lifecycle', () => {
  const base = { setupComplete: true, setup: doneSetup };
  const startable = [{ id: 'start', enabled: true, disabledReasons: [] }];

  it('reports final review distinctly while it runs', () => {
    expect(passState(featureSnapshot({ ...base, status: 'FinalReviewing' }))).toMatchObject({
      id: 'final-reviewing',
      tone: 'live',
    });
  });

  it('never contradicts the sidebar: review passed without a transaction is not startable', () => {
    const child = featureSnapshot({ ...base, status: 'ReviewPassed', actions: startable });
    const state = passState(child);
    expect(state).toMatchObject({ id: 'review-passed', tone: 'live' });
    expect(state.sentence).not.toContain('Ready to start');
    expect(passActions(child)).toEqual([]);
  });

  it('keeps start exclusive to a pass that has not run yet', () => {
    const child = featureSnapshot({ ...base, status: 'Created', actions: startable });
    expect(passState(child).id).toBe('ready');
    expect(passActions(child)).toEqual([{ id: 'start', label: 'Start pass', kind: 'primary' }]);
  });

  it('renders no sentence for the attention state; the card owns the condition', () => {
    const state = passState(
      featureSnapshot({
        ...base,
        status: 'ReviewPassed',
        transaction: { phase: 'attention', attention: integrationAttention },
      }),
    );
    expect(state).toEqual({ id: 'integration-attention', sentence: '', tone: 'attention' });
  });

  it('keeps a neutral progress sentence for rollback states with no diagnostics', () => {
    const rollingBack = passState(
      featureSnapshot({
        ...base,
        status: 'ReviewPassed',
        transaction: {
          phase: 'rolling_back',
          attention: integrationAttention,
          entries: [{ repo: 'repo-a', applyState: 'attention', pendingSync: true }],
        },
      }),
    );
    expect(rollingBack.id).toBe('integration-attention');
    expect(rollingBack.sentence).toBe('Integration is rolling back after a failed apply.');

    const rolledBack = passState(
      featureSnapshot({
        ...base,
        status: 'ReviewPassed',
        transaction: { phase: 'rolled_back', attention: integrationAttention },
      }),
    );
    expect(rolledBack.id).toBe('integration-attention');
    expect(rolledBack.sentence).toBe(
      'Integration rolled back after a failed apply. Retry runs it again.',
    );
  });

  it('walks applied, merged, and discarded closures without reverting to ready', () => {
    const closing = featureSnapshot({
      ...base,
      status: 'ReviewPassed',
      actions: startable,
      transaction: { phase: 'applied' },
    });
    expect(passState(closing)).toMatchObject({ id: 'closing', tone: 'live' });
    expect(passActions(closing)).toEqual([]);

    const merged = featureSnapshot({
      ...base,
      status: 'Done',
      actions: startable,
      transaction: { phase: 'merged' },
    });
    expect(passState(merged)).toMatchObject({ id: 'merged', tone: 'quiet' });
    expect(passActions(merged)).toEqual([]);

    expect(
      passState(featureSnapshot({ ...base, status: 'Done', closeOutcome: 'completed' })).id,
    ).toBe('merged');

    const discarded = featureSnapshot({
      ...base,
      status: 'Done',
      actions: startable,
      closeOutcome: 'discarded',
    });
    expect(passState(discarded)).toMatchObject({ id: 'closed', tone: 'quiet' });
    expect(passActions(discarded)).toEqual([]);
  });

  it('marks the pass and integration stations done once merged', () => {
    const parent = featureSnapshot({ status: 'Published' });
    const child = featureSnapshot({ ...base, status: 'Done', transaction: { phase: 'merged' } });
    const [, passStation, integration] = custodyStations(parent, child, childView());
    expect(passStation).toMatchObject({ detail: 'Merged', state: 'done' });
    expect(integration).toMatchObject({ detail: 'Merged into the parent', state: 'done' });
  });
});

describe('custodyStations', () => {
  it('always locks the parent and mirrors the child state on the pass station', () => {
    const parent = featureSnapshot({ status: 'Published', name: 'Electron app' });
    const child = featureSnapshot({
      status: 'Created',
      setupComplete: true,
      setup: doneSetup,
      actions: [{ id: 'start', enabled: true, disabledReasons: [] }],
    });
    const [parentStation, passStation, integration] = custodyStations(parent, child, childView());
    expect(parentStation).toMatchObject({ state: 'locked', title: 'Electron app' });
    expect(parentStation.detail).toBe('Published · locked while the pass runs');
    expect(passStation).toMatchObject({ detail: 'Ready to start', state: 'pending' });
    expect(integration).toMatchObject({ detail: 'After final review approval', state: 'pending' });
  });

  it('lights the integration station from the transaction journal, not a hardcoded label', () => {
    const parent = featureSnapshot({ status: 'Published' });
    const child = featureSnapshot({
      status: 'ReviewPassed',
      setupComplete: true,
      setup: doneSetup,
      transaction: { phase: 'attention' },
    });
    const [, passStation, integration] = custodyStations(parent, child, childView());
    expect(passStation.state).toBe('attention');
    expect(integration).toMatchObject({ detail: 'Needs attention', state: 'attention' });
  });

  it('falls back to the relationship view while the child snapshot loads', () => {
    const parent = featureSnapshot({ status: 'Published' });
    const [, passStation] = custodyStations(parent, null, childView());
    expect(passStation.detail).toBe('Active — Created');
  });
});

describe('passActions', () => {
  it('renders only catalogue-enabled verbs — never a dead button row', () => {
    const child = featureSnapshot({
      status: 'Created',
      setupComplete: true,
      setup: doneSetup,
      actions: [
        { id: 'start', enabled: true, disabledReasons: [] },
        { id: 'pause-stop', enabled: false, disabledReasons: [{ code: 'x', message: 'no' }] },
        { id: 'resume', enabled: false, disabledReasons: [] },
        { id: 'restart', enabled: false, disabledReasons: [] },
        { id: 'discard', enabled: true, disabledReasons: [] },
      ],
    });
    expect(passActions(child)).toEqual([{ id: 'start', label: 'Start pass', kind: 'primary' }]);
  });

  it('offers Stop while running and Resume plus a quiet Restart when paused', () => {
    const running = featureSnapshot({
      status: 'Implementing',
      actions: [{ id: 'pause-stop', enabled: true, disabledReasons: [] }],
    });
    expect(passActions(running)).toEqual([{ id: 'pause-stop', label: 'Stop', kind: 'primary' }]);

    const paused = featureSnapshot({
      status: 'Interrupted',
      actions: [
        { id: 'resume', enabled: true, disabledReasons: [] },
        { id: 'restart', enabled: true, disabledReasons: [] },
      ],
    });
    expect(passActions(paused)).toEqual([
      { id: 'resume', label: 'Resume', kind: 'primary' },
      { id: 'restart', label: 'Restart', kind: 'secondary' },
    ]);
  });

  it('labels the retry verb after a failed setup', () => {
    const child = featureSnapshot({
      status: 'Failed',
      setup: { status: 'failed', attempt: 1, tasks: [] },
      actions: [{ id: 'retry', enabled: true, disabledReasons: [] }],
    });
    expect(passActions(child)).toEqual([{ id: 'retry', label: 'Retry setup', kind: 'primary' }]);
  });
});

describe('refactoringStatusChip', () => {
  it('returns the parent action bar labels', () => {
    expect(refactoringStatusChip(childView())).toEqual({ label: 'Refactoring', tone: 'info' });
    expect(refactoringStatusChip(childView({ attention: integrationAttention }))).toEqual({
      label: 'Refactoring — needs attention',
      tone: 'attention',
    });
    expect(refactoringStatusChip(childView({ integrationState: 'attention' }))).toEqual({
      label: 'Refactoring — needs attention',
      tone: 'attention',
    });
  });

  it('switches to review-feedback copy for a review-feedback child', () => {
    const rf = childView({ kind: 'review-feedback' });
    expect(refactoringStatusChip(rf)).toEqual({
      label: 'Addressing review feedback',
      tone: 'info',
    });
    expect(
      refactoringStatusChip(
        childView({ kind: 'review-feedback', attention: integrationAttention }),
      ),
    ).toEqual({
      label: 'Addressing review feedback — needs attention',
      tone: 'attention',
    });
  });

  it('switches to rebase copy for a rebase child', () => {
    expect(refactoringStatusChip(childView({ kind: 'rebase' }))).toEqual({
      label: 'Rebasing',
      tone: 'info',
    });
    expect(
      refactoringStatusChip(childView({ kind: 'rebase', attention: integrationAttention })),
    ).toEqual({ label: 'Rebasing — needs attention', tone: 'attention' });
  });

  it('falls back to a neutral verb for an unknown kind', () => {
    expect(refactoringStatusChip(childView({ kind: 'mystery' }))).toEqual({
      label: 'Working',
      tone: 'info',
    });
  });
});

describe('passKindLabel', () => {
  it('returns "Refactor pass" for refactor children', () => {
    expect(passKindLabel('refactor')).toBe('Refactor pass');
  });

  it('returns "Review feedback pass" for review-feedback children', () => {
    expect(passKindLabel('review-feedback')).toBe('Review feedback pass');
  });

  it('returns "Rebase pass" for rebase children', () => {
    expect(passKindLabel('rebase')).toBe('Rebase pass');
  });

  it('falls back to a neutral "Pass" for an unknown kind', () => {
    expect(passKindLabel('mystery')).toBe('Pass');
  });
});

describe('custodyStations kind-aware eyebrow', () => {
  it('uses "Refactor pass" eyebrow for refactor children', () => {
    const parent = featureSnapshot({ status: 'Published' });
    const child = featureSnapshot({ status: 'Created', setupComplete: true, setup: doneSetup });
    const [, passStation] = custodyStations(parent, child, childView({ kind: 'refactor' }));
    expect(passStation.eyebrow).toBe('Refactor pass');
  });

  it('uses "Review feedback pass" eyebrow for review-feedback children', () => {
    const parent = featureSnapshot({ status: 'Published' });
    const child = featureSnapshot({ status: 'Created', setupComplete: true, setup: doneSetup });
    const [, passStation] = custodyStations(parent, child, childView({ kind: 'review-feedback' }));
    expect(passStation.eyebrow).toBe('Review feedback pass');
  });

  it('uses "Rebase pass" eyebrow for rebase children', () => {
    const parent = featureSnapshot({ status: 'Published' });
    const child = featureSnapshot({ status: 'Created', setupComplete: true, setup: doneSetup });
    const [, passStation] = custodyStations(parent, child, childView({ kind: 'rebase' }));
    expect(passStation.eyebrow).toBe('Rebase pass');
  });
});
