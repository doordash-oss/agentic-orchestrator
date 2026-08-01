import { describe, expect, it } from 'vitest';
import { AttentionService } from '../attention';
import type { ServerTransport } from '../serverClient';
import { ATTENTION_ALREADY_RESOLVED_NOTICE, CHAT_SESSION_ID } from '../../shared/ipc';

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
  it('normalizes supported actions and keeps all-unknown actions on generic fallback', async () => {
    const service = new AttentionService({
      apiRequest: (path) => {
        const body =
          path === '/api/v1/prompts'
            ? {
                api_version: 'v1',
                ask_user_questions: [],
                help_queue: [],
                need_user_inputs: [
                  {
                    feature_id: 'feature-1',
                    repo_name: 'unknown-action',
                    open: true,
                    questions: [{ index: 1, prompt: 'How should Agentico continue?', answer: '' }],
                    verification: {
                      blockers: [
                        {
                          item_id: 'deploy',
                          name: 'Deployment smoke test',
                          command: 'make deploy-smoke',
                          reason: 'a newer server needs another decision',
                          capabilities: [],
                          remediation: 'Choose a supported action or answer the generic prompt.',
                        },
                      ],
                      allowed_actions: ['REQUEST_ADMIN_ESCALATION', 'ASK_OWNER'],
                    },
                  },
                  {
                    feature_id: 'feature-1',
                    repo_name: 'supported-actions',
                    open: true,
                    questions: [{ index: 1, prompt: 'How should Agentico continue?', answer: '' }],
                    verification: {
                      blockers: [
                        {
                          item_id: 'codesign',
                          name: 'Package signature',
                          command: 'make package-verify',
                          reason: 'keychain access is unavailable',
                          capabilities: [],
                          remediation: 'Grant access or waive the blocked check.',
                        },
                      ],
                      allowed_actions: [' retry_after_auth ', 'WAIVE'],
                    },
                  },
                ],
              }
            : path === '/api/v1/permissions'
              ? { api_version: 'v1', requests: [] }
              : {
                  api_version: 'v1',
                  features: [
                    {
                      id: 'feature-1',
                      name: 'Active feature',
                      slug: 'active-feature',
                      status: 'NeedUserInput',
                      current_phase: 'implement',
                      repos: ['unknown-action', 'supported-actions'],
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
    const unknownActionGate = snapshot.items.find(
      (item) => item.kind === 'gate' && item.repoName === 'unknown-action',
    );
    const supportedActionsGate = snapshot.items.find(
      (item) => item.kind === 'gate' && item.repoName === 'supported-actions',
    );
    if (unknownActionGate?.kind !== 'gate' || supportedActionsGate?.kind !== 'gate') {
      throw new Error('expected both verification gates');
    }

    expect(unknownActionGate.verification?.allowedActions).toEqual([]);
    expect(supportedActionsGate.verification?.allowedActions).toEqual([
      'RETRY_AFTER_AUTH',
      'WAIVE',
    ]);
  });

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
                  {
                    feature_id: CHAT_SESSION_ID,
                    question: 'chat help',
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
                    verification: {
                      blockers: [
                        {
                          item_id: 'deploy',
                          name: 'Deployment smoke test',
                          repo_name: 'repo-a',
                          command: 'make deploy-smoke',
                          reason: 'missing declared capability "Okta session"',
                          capabilities: ['Okta session'],
                          remediation: 'Make Okta session available, then retry verification.',
                        },
                      ],
                      allowed_actions: ['WAIVE', 'RETRY_AFTER_AUTH'],
                    },
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
        `${CHAT_SESSION_ID}:`,
        'feature-1::',
        'feature-1:session-2',
        'perm-active',
        'perm-runtime',
      ]),
    );
    expect(ids).toHaveLength(6);
    expect(ids).not.toContain('ask-orphan');
    expect(ids).not.toContain('missing-feature::');
    expect(ids).not.toContain('missing-feature:session-1');
    expect(ids).not.toContain('perm-orphan');
    expect(snapshot.items).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          kind: 'help',
          id: `${CHAT_SESSION_ID}:`,
          sessionId: CHAT_SESSION_ID,
        }),
        expect.objectContaining({
          kind: 'gate',
          id: 'feature-1::',
          questions: [{ index: 1, prompt: 'active gate', answer: '' }],
          verification: {
            blockers: [
              {
                itemId: 'deploy',
                name: 'Deployment smoke test',
                repoName: 'repo-a',
                command: 'make deploy-smoke',
                reason: 'missing declared capability "Okta session"',
                capabilities: ['Okta session'],
                remediation: 'Make Okta session available, then retry verification.',
              },
            ],
            allowedActions: ['WAIVE', 'RETRY_AFTER_AUTH'],
          },
        }),
      ]),
    );
    expect(snapshot.items.find((item) => item.id === `${CHAT_SESSION_ID}:`)).not.toHaveProperty(
      'featureId',
    );
  });

  it("routes a refactor pass's prompts to the parent instead of dropping them", async () => {
    const service = new AttentionService({
      apiRequest: (path) => {
        const body =
          path === '/api/v1/prompts'
            ? {
                api_version: 'v1',
                ask_user_questions: [
                  {
                    request_id: 'ask-pass',
                    feature_id: 'child-1',
                    session_id: 'child-1-fix-01',
                    tool_name: 'ask-user',
                    status: 'pending',
                    questions: [{ question: 'Which language should the body keep?' }],
                  },
                ],
                help_queue: [
                  {
                    feature_id: 'child-1',
                    session_id: 'child-1-fix-01',
                    question: 'pass help',
                    pending: true,
                  },
                ],
                need_user_inputs: [
                  { feature_id: 'child-1', open: true, questions: [{ prompt: 'pass gate' }] },
                ],
              }
            : path === '/api/v1/permissions'
              ? { api_version: 'v1', requests: [] }
              : {
                  api_version: 'v1',
                  features: [
                    {
                      id: 'parent-1',
                      name: 'Parent feature',
                      slug: 'parent-feature',
                      status: 'Published',
                      current_phase: 'Publish',
                      repos: ['repo-a'],
                      created_at: '2026-07-16T10:00:00Z',
                      active_run: 1,
                      run_count: 1,
                      progress: {},
                      active_child: {
                        id: 'child-1',
                        name: 'Fix pass',
                        kind: 'refactor',
                        display_token: 'refactor:child-1',
                        display_state: 'Active — FinalReviewing',
                        pipeline: 'medium',
                        status: 'FinalReviewing',
                        started_at: '2026-07-16T11:00:00Z',
                        cost: { total_usd: 1.2, by_phase: {} },
                        integration_state: 'pending',
                        attention: [],
                        cleanup_warnings: [],
                      },
                    },
                  ],
                };
        return Promise.resolve({ status: 200, body });
      },
    } satisfies ServerTransport);

    const snapshot = await service.getSnapshot();
    expect(snapshot.items.map((item) => item.id).sort()).toEqual([
      'ask-pass',
      'child-1::',
      'child-1:child-1-fix-01',
    ]);
    // Prompts keep the child's featureId (the session owner) and carry the
    // parent so tabs, badges, and jumps route to the tab that exists.
    for (const item of snapshot.items) {
      expect(item).toMatchObject({ featureId: 'child-1', parentFeatureId: 'parent-1' });
    }
  });

  it('returns the stale-resolution response for a missing pending request', async () => {
    const service = new AttentionService({
      apiRequest: () =>
        Promise.resolve({
          status: 400,
          body: {
            api_version: 'v1',
            error: { code: 'bad_request', message: 'pending request perm-stale not found' },
          },
        }),
    } satisfies ServerTransport);

    await expect(
      service.answerPermission({ requestId: 'perm-stale', decision: 'allow_once' }),
    ).resolves.toEqual({
      result: 'Already resolved.',
      alreadyResolved: true,
      notice: ATTENTION_ALREADY_RESOLVED_NOTICE,
    });
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
