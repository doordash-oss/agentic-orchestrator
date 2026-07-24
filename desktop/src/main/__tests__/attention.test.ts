import { describe, expect, it } from 'vitest';
import { AttentionService } from '../attention';
import type { ServerTransport } from '../serverClient';
import { ATTENTION_ALREADY_RESOLVED_NOTICE } from '../../shared/ipc';

describe('AttentionService mutations', () => {
  it.each(['conflict', 'not_found'])(
    'returns the stale-resolution response for typed %s server errors',
    async (code) => {
      const service = new AttentionService({
        apiRequest: () =>
          Promise.resolve({
            status: 409,
            body: { api_version: 'v1', error: { code, message: 'already answered' } },
          }),
      } satisfies ServerTransport);

      await expect(
        service.answerQuestions({ requestId: 'ask-1', answers: { prompt: 'answer' } }),
      ).resolves.toEqual({
        result: 'Already resolved.',
        alreadyResolved: true,
        notice: ATTENTION_ALREADY_RESOLVED_NOTICE,
      });
    },
  );

  it('does not classify a plain error message as an already-resolved item', async () => {
    const service = new AttentionService({
      apiRequest: () => Promise.reject(new Error('conflict while submitting attention response')),
    } satisfies ServerTransport);

    await expect(
      service.answerQuestions({ requestId: 'ask-1', answers: { prompt: 'answer' } }),
    ).rejects.toThrow('conflict while submitting attention response');
  });
});

describe('AttentionService review items', () => {
  it('omits feature-scoped attention whose feature is no longer listed', async () => {
    const service = new AttentionService({
      apiRequest: (path) => {
        const body =
          path === '/api/v1/prompts'
            ? {
                api_version: 'v1',
                ask_user_questions: [
                  {
                    request_id: 'ask-orphan',
                    feature_id: 'missing-feature',
                    tool_name: 'ask-user',
                    status: 'pending',
                    questions: [{ question: 'Should not be actionable?' }],
                  },
                  {
                    request_id: 'ask-runtime',
                    tool_name: 'ask-user',
                    status: 'pending',
                    questions: [{ question: 'Runtime question remains?' }],
                  },
                ],
                help_queue: [
                  {
                    feature_id: 'missing-feature',
                    session_id: 'session-1',
                    question: 'orphaned help',
                    pending: true,
                  },
                  {
                    feature_id: 'feature-1',
                    session_id: 'session-2',
                    question: 'active help',
                    pending: true,
                  },
                ],
                need_user_inputs: [
                  {
                    feature_id: 'missing-feature',
                    open: true,
                    questions: [{ prompt: 'orphaned gate' }],
                  },
                  {
                    feature_id: 'feature-1',
                    open: true,
                    questions: [{ prompt: 'active gate' }],
                  },
                ],
              }
            : path === '/api/v1/permissions'
              ? {
                  api_version: 'v1',
                  requests: [
                    {
                      request_id: 'perm-orphan',
                      feature_id: 'missing-feature',
                      tool_name: 'Bash',
                      status: 'pending',
                    },
                    {
                      request_id: 'perm-active',
                      feature_id: 'feature-1',
                      tool_name: 'Bash',
                      status: 'pending',
                    },
                    {
                      request_id: 'perm-runtime',
                      tool_name: 'Bash',
                      status: 'pending',
                    },
                  ],
                }
              : {
                  api_version: 'v1',
                  features: [
                    {
                      id: 'feature-1',
                      name: 'Active feature',
                      slug: 'active-feature',
                      status: 'Running',
                      current_phase: 'implement',
                      repos: ['repo-a'],
                      created_at: '2026-07-16T10:00:00Z',
                      active_run: 1,
                      run_count: 1,
                      progress: {},
                    },
                  ],
                };
        return Promise.resolve({ status: 200, body });
      },
    } satisfies ServerTransport);

    const snapshot = await service.getSnapshot();
    const ids = snapshot.items.map((item) => item.id);

    expect(ids).toEqual(
      expect.arrayContaining([
        'ask-runtime',
        'feature-1::',
        'feature-1:session-2',
        'perm-active',
        'perm-runtime',
      ]),
    );
    expect(ids).toHaveLength(5);
    expect(ids).not.toContain('ask-orphan');
    expect(ids).not.toContain('missing-feature::');
    expect(ids).not.toContain('missing-feature:session-1');
    expect(ids).not.toContain('perm-orphan');
  });

  it('derives one stable inbox item from each authoritative pending review', async () => {
    const service = new AttentionService({
      apiRequest: (path) => {
        const body =
          path === '/api/v1/prompts'
            ? { api_version: 'v1', ask_user_questions: [], help_queue: [], need_user_inputs: [] }
            : path === '/api/v1/permissions'
              ? { api_version: 'v1', requests: [] }
              : {
                  api_version: 'v1',
                  features: [
                    {
                      id: 'feature-1',
                      name: 'Review attention',
                      slug: 'review-attention',
                      status: 'PhasePlanNeedsReview',
                      current_phase: 'plan',
                      repos: ['repo-a'],
                      created_at: '2026-07-16T10:00:00Z',
                      active_run: 4,
                      run_count: 4,
                      progress: {},
                    },
                    {
                      id: 'feature-2',
                      name: 'Running feature',
                      slug: 'running-feature',
                      status: 'Running',
                      current_phase: 'implement',
                      repos: ['repo-a'],
                      created_at: '2026-07-16T10:00:00Z',
                      active_run: 1,
                      run_count: 1,
                      progress: {},
                    },
                  ],
                };
        return Promise.resolve({ status: 200, body });
      },
    } satisfies ServerTransport);

    await expect(service.getSnapshot()).resolves.toEqual({
      items: [
        {
          kind: 'review',
          id: 'review:feature-1:4:plan:PhasePlanNeedsReview',
          featureId: 'feature-1',
          waitingSince: '2026-07-16T10:00:00Z',
          reviewKind: 'PhasePlan',
          phase: 'plan',
        },
      ],
    });
  });
});
