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
import { AttentionService } from '../attention';
import type { ServerTransport } from '../serverClient';
import { CanonicalErrorException } from '../../shared/errors';
import {
  actionableAttentionCount,
  AttentionSnapshotSchema,
  attentionOwnerFeatureId,
  isSupervisorAttentionItem,
} from '../../shared/ipc';

/** One canonical catalog-rendered rejection body, as the server now emits. */
function canonicalBody(code: string): Record<string, unknown> {
  const catalog: Record<string, { title: string; summary: string }> = {
    conflict: {
      title: 'Conflict',
      summary: 'The request conflicts with the current state of the feature.',
    },
    not_found: { title: 'Not found', summary: 'The requested resource was not found.' },
    bad_request: { title: 'Bad request', summary: 'The request was not valid.' },
  };
  const { title, summary } = catalog[code] ?? { title: 'Error', summary: 'The request failed.' };
  return { api_version: 'v1', error: { code, class: 'blocking', title, summary } };
}

describe('AttentionService mutations', () => {
  it.each([
    { code: 'conflict', status: 409 },
    { code: 'not_found', status: 404 },
  ])(
    'propagates the canonical $code server error instead of masking it as resolved',
    async ({ code, status }) => {
      const service = new AttentionService({
        apiRequest: () => Promise.resolve({ status, body: canonicalBody(code) }),
      } satisfies ServerTransport);

      const err = await service
        .answerQuestions({ requestId: 'ask-1', answers: { prompt: 'answer' } })
        .catch((e: unknown) => e);
      expect(err).toBeInstanceOf(CanonicalErrorException);
      expect((err as CanonicalErrorException).canonical.code).toBe(code);
    },
  );

  it('posts testing-contract waivers and reads the revised contract envelope', async () => {
    const apiRequest = vi.fn(() =>
      Promise.resolve({
        status: 200,
        body: {
          api_version: 'v1',
          feature_id: 'abcd1234ef567890',
          result: 'waived',
          contract_revision: 3,
          waived_items: ['deploy-smoke', 'ui-capture'],
        },
      }),
    );
    const service = new AttentionService({ apiRequest } satisfies ServerTransport);

    await expect(
      service.waiveTestingContract({
        featureId: 'abcd1234ef567890',
        itemIds: ['deploy-smoke', 'ui-capture'],
        reason: 'Vendor UI is unreachable from CI.',
        activeRun: 1,
        roadmapPhase: 2,
        contractRevision: 2,
      }),
    ).resolves.toEqual({
      result: 'waived',
      contractRevision: 3,
      waivedItems: ['deploy-smoke', 'ui-capture'],
    });
    expect(apiRequest).toHaveBeenCalledWith(
      '/api/v1/features/abcd1234ef567890/actions/testing-contract-waive',
      expect.objectContaining({
        method: 'POST',
        body: {
          item_ids: ['deploy-smoke', 'ui-capture'],
          reason: 'Vendor UI is unreachable from CI.',
          active_run: 1,
          roadmap_phase: 2,
          contract_revision: 2,
        },
      }),
    );
  });

  it('reads the testing contract and maps rows to the renderer shape', async () => {
    const apiRequest = vi.fn(() =>
      Promise.resolve({
        status: 200,
        body: {
          api_version: 'v1',
          feature_id: 'abcd1234ef567890',
          active_run: 1,
          roadmap_phase: 2,
          revision: 3,
          items: [
            {
              item_id: 'deploy-smoke',
              source: 'plan',
              owner: 'harness',
              repo: 'svc',
              name: 'Deployment smoke test',
              command: 'make smoke',
              required: true,
              allow_substitution: true,
              allow_blocked: false,
              allow_waiver: true,
              disposition: { status: 'waived', reason: 'Offline.', changed_by: 'user' },
              capabilities: ['network'],
            },
            {
              item_id: 'unit',
              source: 'plan',
              owner: 'agent',
              name: 'Unit tests',
              command: 'make test',
              required: true,
              allow_substitution: false,
              allow_blocked: false,
              allow_waiver: false,
              capabilities: [],
            },
          ],
        },
      }),
    );
    const service = new AttentionService({ apiRequest } satisfies ServerTransport);

    await expect(service.getTestingContract({ featureId: 'abcd1234ef567890' })).resolves.toEqual({
      available: true,
      featureId: 'abcd1234ef567890',
      activeRun: 1,
      roadmapPhase: 2,
      revision: 3,
      items: [
        {
          itemId: 'deploy-smoke',
          source: 'plan',
          owner: 'harness',
          repo: 'svc',
          name: 'Deployment smoke test',
          command: 'make smoke',
          required: true,
          allowSubstitution: true,
          allowBlocked: false,
          allowWaiver: true,
          disposition: { status: 'waived', reason: 'Offline.', changedBy: 'user' },
          capabilities: ['network'],
        },
        {
          itemId: 'unit',
          source: 'plan',
          owner: 'agent',
          name: 'Unit tests',
          command: 'make test',
          required: true,
          allowSubstitution: false,
          allowBlocked: false,
          allowWaiver: false,
          capabilities: [],
        },
      ],
    });
    expect(apiRequest).toHaveBeenCalledWith(
      '/api/v1/features/abcd1234ef567890/testing-contract',
      undefined,
    );
  });

  it('reports a missing testing contract as unavailable instead of throwing', async () => {
    const service = new AttentionService({
      apiRequest: () => Promise.resolve({ status: 404, body: canonicalBody('not_found') }),
    } satisfies ServerTransport);

    await expect(service.getTestingContract({ featureId: 'abcd1234ef567890' })).resolves.toEqual({
      available: false,
    });
  });

  it('propagates other testing-contract read failures and rejects malformed rows', async () => {
    const conflict = new AttentionService({
      apiRequest: () => Promise.resolve({ status: 409, body: canonicalBody('conflict') }),
    } satisfies ServerTransport);
    const err = await conflict
      .getTestingContract({ featureId: 'abcd1234ef567890' })
      .catch((e: unknown) => e);
    expect(err).toBeInstanceOf(CanonicalErrorException);
    expect((err as CanonicalErrorException).canonical.code).toBe('conflict');

    const malformed = new AttentionService({
      apiRequest: () =>
        Promise.resolve({
          status: 200,
          body: {
            api_version: 'v1',
            feature_id: 'abcd1234ef567890',
            active_run: 1,
            roadmap_phase: 0,
            items: [],
          },
        }),
    } satisfies ServerTransport);
    await expect(malformed.getTestingContract({ featureId: 'abcd1234ef567890' })).rejects.toThrow();
  });

  it('rejects testing-contract waivers without items, a reason, or a contract binding before any request', async () => {
    const apiRequest = vi.fn(() => Promise.resolve({ status: 200, body: {} }));
    const service = new AttentionService({ apiRequest } satisfies ServerTransport);

    await expect(
      service.waiveTestingContract({
        featureId: 'abcd1234ef567890',
        itemIds: [],
        reason: 'x',
        activeRun: 1,
        roadmapPhase: 1,
        contractRevision: 1,
      }),
    ).rejects.toThrow();
    await expect(
      service.waiveTestingContract({
        featureId: 'abcd1234ef567890',
        itemIds: ['deploy-smoke'],
        reason: '   ',
        activeRun: 1,
        roadmapPhase: 1,
        contractRevision: 1,
      }),
    ).rejects.toThrow();
    await expect(
      service.waiveTestingContract({
        featureId: 'abcd1234ef567890',
        itemIds: ['deploy-smoke'],
        reason: 'x',
        activeRun: 1,
        roadmapPhase: 0,
        contractRevision: 1,
      }),
    ).rejects.toThrow();
    await expect(
      service.waiveTestingContract({
        featureId: 'abcd1234ef567890',
        itemIds: ['deploy-smoke'],
        reason: 'x',
        activeRun: 0,
        roadmapPhase: 1,
        contractRevision: 1,
      }),
    ).rejects.toThrow();
    expect(apiRequest).not.toHaveBeenCalled();
  });

  it('does not classify a plain error message as an already-resolved item', async () => {
    const service = new AttentionService({
      apiRequest: () => Promise.reject(new Error('conflict while submitting attention response')),
    } satisfies ServerTransport);

    await expect(
      service.answerQuestions({ requestId: 'ask-1', answers: { prompt: 'answer' } }),
    ).rejects.toThrow('conflict while submitting attention response');
  });
});

describe('AttentionService waiting sessions', () => {
  const featuresBody = {
    api_version: 'v1',
    features: [
      {
        id: 'feature-1',
        name: 'Knowledge base build',
        slug: 'knowledge-base-build',
        status: 'BuildingKB',
        current_phase: 'Knowledge Base',
        repos: ['repo-a'],
        created_at: '2026-07-16T10:00:00Z',
        active_run: 1,
        run_count: 1,
        progress: {},
      },
    ],
  };
  const sessionsBody = {
    api_version: 'v1',
    sessions: [
      {
        id: 'kb1234567890abcdef',
        feature_id: 'feature-1',
        run_number: 1,
        phase: 'Knowledge Base',
        kind: 'phase',
        status: 'waiting',
        turn_state: 'waiting_input',
        started_at: '2026-07-16T10:05:00Z',
        task_activities: [
          {
            task_id: 'task-1',
            state: 'running',
            description: 'Indexing repository layout',
            started_at: '2026-07-16T10:06:00Z',
            updated_at: '2026-07-16T10:07:00Z',
          },
          {
            task_id: 'task-2',
            state: 'completed',
            description: 'Collecting build commands',
            started_at: '2026-07-16T10:05:30Z',
            updated_at: '2026-07-16T10:06:30Z',
          },
        ],
        running_task_count: 1,
        usage: {},
      },
    ],
  };

  function transport(question: string, sessions: unknown, kind?: string): ServerTransport {
    return {
      apiRequest: (path) => {
        if (path === '/api/v1/sessions' && sessions instanceof Error) {
          return Promise.reject(sessions);
        }
        const body =
          path === '/api/v1/prompts'
            ? {
                api_version: 'v1',
                ask_user_questions: [],
                help_queue: [
                  {
                    feature_id: 'feature-1',
                    question,
                    ...(kind === undefined ? {} : { kind }),
                    pending: true,
                    time: '2026-07-16T10:05:00Z',
                  },
                ],
                need_user_inputs: [],
              }
            : path === '/api/v1/permissions'
              ? { api_version: 'v1', requests: [] }
              : path === '/api/v1/sessions'
                ? sessions
                : featuresBody;
        return Promise.resolve({ status: 200, body });
      },
    };
  }

  it('classifies the synthetic waiting prompt and enriches session provenance', async () => {
    const service = new AttentionService(transport('Agent has a question', sessionsBody));

    const snapshot = await service.getSnapshot();
    expect(snapshot.items).toEqual([
      expect.objectContaining({
        kind: 'help',
        featureId: 'feature-1',
        sessionId: 'kb1234567890abcdef',
        phase: 'Knowledge Base',
        waitingKind: 'coordinating',
        runningTasks: ['Indexing repository layout'],
      }),
    ]);
  });

  it('trusts the wire kind over prompt text when the server sends one', async () => {
    const synthetic = new AttentionService(
      transport('Coordinating next steps', sessionsBody, 'coordinating'),
    );
    expect((await synthetic.getSnapshot()).items).toEqual([
      expect.objectContaining({ kind: 'help', waitingKind: 'coordinating' }),
    ]);

    // An unrecognized kind (such as the retired chat 'input') falls back to
    // the prompt text.
    const retired = new AttentionService(transport('Agent has a question', sessionsBody, 'input'));
    expect((await retired.getSnapshot()).items).toEqual([
      expect.objectContaining({ kind: 'help', waitingKind: 'coordinating' }),
    ]);

    const question = new AttentionService(
      transport('Agent has a question', sessionsBody, 'question'),
    );
    expect((await question.getSnapshot()).items).toEqual([
      expect.objectContaining({ kind: 'help', waitingKind: 'question' }),
    ]);
  });

  it('keeps real help prompts framed as questions while still naming the session', async () => {
    const service = new AttentionService(transport('Which deploy target?', sessionsBody));

    const snapshot = await service.getSnapshot();
    expect(snapshot.items).toEqual([
      expect.objectContaining({
        kind: 'help',
        prompt: 'Which deploy target?',
        sessionId: 'kb1234567890abcdef',
        phase: 'Knowledge Base',
        waitingKind: 'question',
      }),
    ]);
  });

  it('still lists waiting help when the sessions endpoint fails', async () => {
    const service = new AttentionService(
      transport('Agent has a question', new Error('sessions unavailable')),
    );

    const snapshot = await service.getSnapshot();
    expect(snapshot.items).toEqual([
      expect.objectContaining({
        kind: 'help',
        featureId: 'feature-1',
        waitingKind: 'coordinating',
      }),
    ]);
    expect(snapshot.items[0]).not.toHaveProperty('sessionId');
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
                    // A retired chat idle-wait is as unlisted as any orphan.
                    feature_id: '__chat__',
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
        'feature-1:',
        'feature-1:session-2',
        'perm-active',
        'perm-runtime',
      ]),
    );
    expect(ids).toHaveLength(5);
    expect(ids).not.toContain('__chat__:');
    expect(ids).not.toContain('ask-orphan');
    expect(ids).not.toContain('missing-feature::');
    expect(ids).not.toContain('missing-feature:session-1');
    expect(ids).not.toContain('perm-orphan');
    expect(snapshot.items).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          kind: 'gate',
          id: 'feature-1:',
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
  });

  it("routes a refactor pass's prompts to the parent instead of dropping them", async () => {
    const longOptionLabel =
      '**Service-level row and byte caps — Recommended (High confidence).** Configure global maximum rows and decoded bytes, stop scanning as soon as either limit is exceeded, and expose classified overflow telemetry. This protects memory predictably without expanding table metadata or cross-repo configuration contracts.';
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
                    questions: [
                      {
                        question: 'What scope should control the maximum snapshot size?',
                        options: [{ label: longOptionLabel }],
                      },
                    ],
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
                        warnings: [],
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
      'child-1:',
      'child-1:child-1-fix-01',
    ]);
    // Prompts keep the child's featureId (the session owner) and carry the
    // parent so tabs, badges, and jumps route to the tab that exists.
    for (const item of snapshot.items) {
      expect(item).toMatchObject({ featureId: 'child-1', parentFeatureId: 'parent-1' });
    }
    expect(snapshot.items.find((item) => item.id === 'ask-pass')).toMatchObject({
      kind: 'questions',
      questions: [{ options: [{ label: longOptionLabel }] }],
    });
  });

  it('maps the canonical bad_request for a missing pending request to already resolved', async () => {
    const service = new AttentionService({
      apiRequest: () =>
        Promise.resolve({
          status: 400,
          body: {
            api_version: 'v1',
            error: {
              code: 'bad_request',
              class: 'blocking',
              title: 'Bad request',
              summary: 'The request was not valid.',
              remediation: { hint: 'Check the request details and try again.' },
              diagnostics: 'pending request perm-stale not found',
            },
          },
        }),
    } satisfies ServerTransport);

    // The canonical era moved the stale marker from the plain-text body into
    // diagnostics; a submission that raced the item's resolution still reads
    // as already resolved rather than surfacing the raw rejection.
    await expect(
      service.answerPermission({ requestId: 'perm-stale', decision: 'allow_once' }),
    ).resolves.toEqual({
      result: 'Already resolved.',
      alreadyResolved: true,
      notice: 'This item was already resolved. The inbox has been refreshed.',
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

describe('AttentionService error items', () => {
  const parentFeature = {
    id: 'parent1234ef567890',
    name: 'Parent feature',
    slug: 'parent-feature',
    status: 'Failed',
    current_phase: 'implement',
    repos: ['repo-a'],
    created_at: '2026-07-16T10:00:00Z',
    active_run: 1,
    run_count: 1,
    progress: {},
  };
  const activeChild = {
    id: 'child1234ef567890',
    name: 'Rework auth',
    kind: 'refactor',
    display_token: 'refactor:child1234ef567890',
    display_state: 'Active — Implementing',
    pipeline: 'large',
    status: 'Implementing',
    started_at: '2026-07-16T10:05:00Z',
    cost: { total_usd: 0, by_phase: {} },
    integration_state: 'attention',
    warnings: [],
  };
  const runEntry = {
    ref: { scope: 'run', code: 'iteration_budget_exhausted', feature_id: 'parent1234ef567890' },
    error: {
      code: 'iteration_budget_exhausted',
      class: 'blocking',
      title: 'Iteration budget exhausted',
      summary: 'The Implement phase exhausted its iteration budget.',
    },
  };
  const transactionEntry = {
    ref: {
      scope: 'transaction',
      code: 'integration_parent_dirty',
      feature_id: 'child1234ef567890',
    },
    error: {
      code: 'integration_parent_dirty',
      class: 'needs_action',
      title: 'Parent worktree is dirty',
      summary: 'The parent worktree for repository "repo-a" has 1 uncommitted change.',
    },
  };

  function errorTransport(errors: unknown[] | undefined): ServerTransport {
    return {
      apiRequest: (path) => {
        const body =
          path === '/api/v1/prompts'
            ? { api_version: 'v1', ask_user_questions: [], help_queue: [], need_user_inputs: [] }
            : path === '/api/v1/permissions'
              ? { api_version: 'v1', requests: [] }
              : path === '/api/v1/sessions'
                ? { api_version: 'v1', sessions: [] }
                : {
                    api_version: 'v1',
                    features: [
                      {
                        ...parentFeature,
                        active_child: activeChild,
                        ...(errors === undefined ? {} : { errors }),
                      },
                    ],
                  };
        return Promise.resolve({ status: 200, body });
      },
    } satisfies ServerTransport;
  }

  it('builds one item per owned error, parent-owned for child-scoped entries', async () => {
    const service = new AttentionService(errorTransport([runEntry, transactionEntry]));

    const snapshot = await service.getSnapshot();
    const errorItems = snapshot.items.filter((item) => item.kind === 'error');
    expect(errorItems).toHaveLength(2);

    const runItem = errorItems.find((item) => item.kind === 'error' && item.ref.scope === 'run');
    expect(runItem).toMatchObject({
      kind: 'error',
      id: 'error:parent1234ef567890:run::iteration_budget_exhausted',
      featureId: 'parent1234ef567890',
      class: 'blocking',
      code: 'iteration_budget_exhausted',
      title: 'Iteration budget exhausted',
      ref: { scope: 'run', code: 'iteration_budget_exhausted', featureId: 'parent1234ef567890' },
    });
    expect(runItem).not.toHaveProperty('parentFeatureId');

    // The child-keyed transaction entry routes to the listed parent.
    const transactionItem = errorItems.find(
      (item) => item.kind === 'error' && item.ref.scope === 'transaction',
    );
    expect(transactionItem).toMatchObject({
      id: 'error:child1234ef567890:transaction::integration_parent_dirty',
      featureId: 'child1234ef567890',
      parentFeatureId: 'parent1234ef567890',
      class: 'needs_action',
      title: 'Parent worktree is dirty',
    });
    if (transactionItem?.kind !== 'error') throw new Error('expected error item');
    expect(attentionOwnerFeatureId(transactionItem)).toBe('parent1234ef567890');
  });

  it('keeps waitingSince stable across polls and refreshes it after the entry disappears', async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-08-05T12:00:00Z'));
    try {
      const withErrors = errorTransport([runEntry]);
      const withoutErrors = errorTransport(undefined);
      let transport = withErrors;
      const service = new AttentionService({
        apiRequest: (path) => transport.apiRequest(path),
      } satisfies ServerTransport);

      const first = await service.getSnapshot();
      const firstSince = first.items.find((item) => item.kind === 'error')?.waitingSince;

      vi.setSystemTime(new Date('2026-08-05T12:05:00Z'));
      const second = await service.getSnapshot();
      expect(second.items.find((item) => item.kind === 'error')?.waitingSince).toBe(firstSince);

      // The entry disappears: the id is pruned from the first-observed map.
      transport = withoutErrors;
      const cleared = await service.getSnapshot();
      expect(cleared.items.filter((item) => item.kind === 'error')).toHaveLength(0);

      // It returns later and reads as freshly waiting.
      transport = withErrors;
      vi.setSystemTime(new Date('2026-08-05T12:10:00Z'));
      const returned = await service.getSnapshot();
      const returnedSince = returned.items.find((item) => item.kind === 'error')?.waitingSince;
      expect(returnedSince).not.toBe(firstSince);
      expect(returnedSince).toBe('2026-08-05T12:10:00.000Z');
    } finally {
      vi.useRealTimers();
    }
  });

  it('never produces items from warning-class entries: the boundary rejects them', async () => {
    const warningEntry = {
      ref: { scope: 'run', code: 'rewind_worktree_reset', feature_id: 'parent1234ef567890' },
      error: {
        code: 'rewind_worktree_reset',
        class: 'warning',
        title: 'Worktree reset to anchor',
        summary: 'The worktree was reset.',
      },
    };
    const service = new AttentionService(errorTransport([warningEntry]));
    await expect(service.getSnapshot()).rejects.toThrow();
  });
});

describe('AttentionService supervisor items', () => {
  const SUPERVISOR_SESSION = '__supervisor__.0b9c6f2e-1d2a-4c55-9e1f-2a3b4c5d6e7f.3';

  function supervisorTransport(apiRequest?: ServerTransport['apiRequest']): ServerTransport {
    return {
      apiRequest:
        apiRequest ??
        ((path) => {
          const body =
            path === '/api/v1/prompts'
              ? {
                  api_version: 'v1',
                  ask_user_questions: [
                    {
                      request_id: 'ask-sup',
                      session_id: SUPERVISOR_SESSION,
                      tool_name: 'AskUserQuestion',
                      status: 'pending',
                      waiting_since: '2026-10-06T10:00:02Z',
                      questions: [{ question: 'Which feature first?', options: [{ label: 'A' }] }],
                    },
                    {
                      request_id: 'ask-feature',
                      feature_id: 'feature-1',
                      session_id: 'session-f',
                      tool_name: 'AskUserQuestion',
                      status: 'pending',
                      waiting_since: '2026-10-06T10:00:03Z',
                      questions: [{ question: 'Feature question?' }],
                    },
                  ],
                  help_queue: [],
                  need_user_inputs: [],
                }
              : path === '/api/v1/permissions'
                ? {
                    api_version: 'v1',
                    requests: [
                      {
                        request_id: 'perm-sup',
                        session_id: SUPERVISOR_SESSION,
                        feature_id: '__supervisor__',
                        tool_name: 'Bash',
                        status: 'pending',
                        waiting_since: '2026-10-06T10:00:00Z',
                        input: { command: 'git status' },
                      },
                      {
                        // A feature-id-only supervisor request is still recognized.
                        request_id: 'perm-sup-feature-only',
                        feature_id: '__supervisor__',
                        tool_name: 'Read',
                        status: 'pending',
                        waiting_since: '2026-10-06T10:00:01Z',
                      },
                      {
                        request_id: 'perm-sup-resolved',
                        session_id: SUPERVISOR_SESSION,
                        tool_name: 'Bash',
                        status: 'allowed',
                      },
                      {
                        request_id: 'perm-feature',
                        feature_id: 'feature-1',
                        session_id: 'session-f',
                        tool_name: 'Edit',
                        status: 'pending',
                        waiting_since: '2026-10-06T10:00:04Z',
                      },
                      {
                        request_id: 'perm-orphan',
                        feature_id: 'missing-feature',
                        tool_name: 'Bash',
                        status: 'pending',
                      },
                    ],
                  }
                : path === '/api/v1/sessions'
                  ? { api_version: 'v1', sessions: [] }
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
        }),
    };
  }

  it('keeps supervisor permissions and questions, targeted at the Supervisor with no feature', async () => {
    const snapshot = await new AttentionService(supervisorTransport()).getSnapshot();
    const byId = new Map(snapshot.items.map((item) => [item.id, item]));

    expect(byId.get('perm-sup')).toEqual({
      kind: 'permission',
      id: 'perm-sup',
      target: 'supervisor',
      sessionId: SUPERVISOR_SESSION,
      toolName: 'Bash',
      input: { command: 'git status' },
      waitingSince: '2026-10-06T10:00:00Z',
    });
    expect(byId.get('perm-sup-feature-only')).toMatchObject({
      kind: 'permission',
      target: 'supervisor',
    });
    expect(byId.get('ask-sup')).toEqual({
      kind: 'questions',
      id: 'ask-sup',
      target: 'supervisor',
      sessionId: SUPERVISOR_SESSION,
      waitingSince: '2026-10-06T10:00:02Z',
      questions: [
        {
          key: 'Which feature first?',
          header: 'Which feature first?',
          multiSelect: false,
          options: [{ label: 'A' }],
        },
      ],
    });
    for (const id of ['perm-sup', 'perm-sup-feature-only', 'ask-sup']) {
      const item = byId.get(id)!;
      expect(isSupervisorAttentionItem(item)).toBe(true);
      expect(attentionOwnerFeatureId(item)).toBeUndefined();
    }
    // Resolved supervisor requests and orphaned feature requests stay out.
    expect(byId.has('perm-sup-resolved')).toBe(false);
    expect(byId.has('perm-orphan')).toBe(false);
  });

  it('leaves feature items exactly as before', async () => {
    const snapshot = await new AttentionService(supervisorTransport()).getSnapshot();
    const byId = new Map(snapshot.items.map((item) => [item.id, item]));

    expect(byId.get('perm-feature')).toEqual({
      kind: 'permission',
      id: 'perm-feature',
      featureId: 'feature-1',
      sessionId: 'session-f',
      toolName: 'Edit',
      waitingSince: '2026-10-06T10:00:04Z',
    });
    expect(byId.get('ask-feature')).toMatchObject({ kind: 'questions', featureId: 'feature-1' });
    expect(byId.get('ask-feature')).not.toHaveProperty('target');
    for (const item of snapshot.items) {
      if (item.kind === 'permission' || item.kind === 'questions') {
        expect(isSupervisorAttentionItem(item)).toBe(item.id.includes('-sup'));
      }
    }
  });

  it('tags supervisor requests with their origin and leaves feature requests untagged', async () => {
    const base = supervisorTransport();
    const childIds = new Set(['perm-sup', 'ask-sup', 'perm-feature']);
    type WireRequest = { request_id: string } & Record<string, unknown>;
    const tag = (request: WireRequest): WireRequest =>
      childIds.has(request.request_id)
        ? { ...request, origin: 'child', child_session_id: 'agent_sub_1' }
        : { ...request, origin: 'root' };
    const transport: ServerTransport = {
      apiRequest: async (path, init) => {
        const result = await base.apiRequest(path, init);
        const body = result.body as Record<string, unknown>;
        if (path === '/api/v1/permissions') {
          return {
            ...result,
            body: { ...body, requests: (body['requests'] as WireRequest[]).map(tag) },
          };
        }
        if (path === '/api/v1/prompts') {
          return {
            ...result,
            body: {
              ...body,
              ask_user_questions: (body['ask_user_questions'] as WireRequest[]).map(tag),
            },
          };
        }
        return result;
      },
    };
    const snapshot = await new AttentionService(transport).getSnapshot();
    const byId = new Map(snapshot.items.map((item) => [item.id, item]));
    expect(byId.get('perm-sup')).toMatchObject({
      target: 'supervisor',
      origin: 'child',
      childSessionId: 'agent_sub_1',
    });
    expect(byId.get('ask-sup')).toMatchObject({
      kind: 'questions',
      target: 'supervisor',
      origin: 'child',
      childSessionId: 'agent_sub_1',
    });
    expect(byId.get('perm-sup-feature-only')).toMatchObject({ origin: 'root' });
    expect(byId.get('perm-sup-feature-only')).not.toHaveProperty('childSessionId');
    expect(byId.get('perm-feature')).not.toHaveProperty('origin');
    expect(byId.get('perm-feature')).not.toHaveProperty('childSessionId');
    expect(AttentionSnapshotSchema.safeParse(snapshot).success).toBe(true);
  });

  it('counts supervisor permissions and questions as actionable', async () => {
    const snapshot = await new AttentionService(supervisorTransport()).getSnapshot();
    const supervisorItems = snapshot.items.filter(isSupervisorAttentionItem);
    expect(supervisorItems).toHaveLength(3);
    expect(actionableAttentionCount(supervisorItems)).toBe(3);
    // Feature permission + feature question + three supervisor items.
    expect(actionableAttentionCount(snapshot.items)).toBe(5);
  });

  it('answers supervisor requests through the existing routes keyed by the session id', async () => {
    const apiRequest = vi.fn(() => Promise.resolve({ status: 200, body: { result: 'ok' } }));
    const service = new AttentionService({ apiRequest } satisfies ServerTransport);

    await service.answerPermission({
      requestId: 'perm-sup',
      sessionId: SUPERVISOR_SESSION,
      decision: 'allow_once',
    });
    await service.answerQuestions({
      requestId: 'ask-sup',
      sessionId: SUPERVISOR_SESSION,
      answers: { 'Which feature first?': 'A' },
    });

    expect(apiRequest.mock.calls).toEqual([
      [
        '/api/v1/permissions/answer',
        {
          method: 'POST',
          body: { request_id: 'perm-sup', session_id: SUPERVISOR_SESSION, decision: 'allow_once' },
        },
      ],
      [
        '/api/v1/prompts/ask-user/answer',
        {
          method: 'POST',
          body: {
            request_id: 'ask-sup',
            session_id: SUPERVISOR_SESSION,
            answers: { 'Which feature first?': 'A' },
          },
        },
      ],
    ]);
  });
});
