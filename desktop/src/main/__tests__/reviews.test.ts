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
import { ReviewService } from '../reviews';
import type { ServerTransport } from '../serverClient';

const session = {
  api_version: 'v1',
  feature_id: 'feature-1',
  review_id: 'phase-plan',
  review_mode: 'phase-plan',
  target_phase: 'plan',
  run_number: 1,
  artifact_id: 'phase-plan.md',
  text: '# Plan',
  draft_revision: 'draft-a',
  source_revision: 'source-a',
  can_iterate: true,
};

function transport(body: unknown, status = 200): ServerTransport & { calls: unknown[][] } {
  const calls: unknown[][] = [];
  return {
    calls,
    apiRequest: (...args) => {
      calls.push(args);
      return Promise.resolve({ status, body });
    },
  };
}

describe('ReviewService', () => {
  it('reads the active session without invoking the reopening mutation', async () => {
    const client = transport(session);
    await expect(new ReviewService(client).read('feature-1')).resolves.toMatchObject({
      draft_revision: 'draft-a',
    });
    expect(client.calls).toEqual([['/api/v1/features/feature-1/reviews', undefined]]);
  });

  it('sends the exact base revision when saving', async () => {
    const client = transport({ ...session, text: '# Changed', draft_revision: 'draft-b' });
    await expect(
      new ReviewService(client).save({
        featureId: 'feature-1',
        reviewId: 'phase-plan',
        baseRevision: 'draft-a',
        text: '# Changed',
      }),
    ).resolves.toMatchObject({ type: 'saved', session: { draft_revision: 'draft-b' } });
    expect(client.calls).toEqual([
      [
        '/api/v1/features/feature-1/reviews/phase-plan/draft',
        { method: 'PUT', body: { base_revision: 'draft-a', text: '# Changed' } },
      ],
    ]);
  });

  it('maps a stale response into a typed conflict without leaking its message', async () => {
    const client = transport(
      {
        api_version: 'v1',
        error: {
          code: 'conflict',
          message: 'review draft revision is stale',
          target: {
            review_id: 'phase-plan',
            current_revision: 'draft-server',
            expected_revision: 'draft-server',
          },
        },
      },
      409,
    );
    await expect(
      new ReviewService(client).save({
        featureId: 'feature-1',
        reviewId: 'phase-plan',
        baseRevision: 'draft-mine',
        text: '# Mine',
      }),
    ).resolves.toEqual({
      type: 'conflict',
      expectedRevision: 'draft-mine',
      currentRevision: 'draft-server',
    });
  });

  it('returns a validation result bound to the submitted text revision', async () => {
    const client = transport({
      api_version: 'v1',
      feature_id: 'feature-1',
      review_id: 'phase-plan',
      applicable: true,
      valid: false,
      revision: 'content-hash',
      findings: [{ code: 'missing-task', message: 'Add a task.' }],
    });
    await expect(
      new ReviewService(client).validate({
        featureId: 'feature-1',
        reviewId: 'phase-plan',
        text: '# Plan',
      }),
    ).resolves.toMatchObject({ valid: false, revision: 'content-hash' });
  });

  it('throws on a non-conflict error without replaying the mutation', async () => {
    const client = transport(
      {
        api_version: 'v1',
        error: { code: 'not_found', message: 'review session gone' },
      },
      404,
    );
    await expect(
      new ReviewService(client).save({
        featureId: 'feature-1',
        reviewId: 'phase-plan',
        baseRevision: 'draft-a',
        text: '# Mine',
      }),
    ).rejects.toThrow();
    // Only one network call — the mutation must not be replayed for error mapping.
    expect(client.calls).toHaveLength(1);
  });
});
