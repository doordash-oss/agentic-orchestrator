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
import { z } from 'zod';
import {
  CanonicalErrorResponseSchema,
  CanonicalErrorSchema,
  CompletionPreflightRepoSchema,
  DIFF_SUMMARY_MAX_BYTES,
  FeatureListResponseSchema,
  HealthResponseSchema,
  parseServerJson,
  PromptSnapshotResponseSchema,
  RepositoryDiffResponseSchema,
  RewindActionResponseSchema,
  ServerFeatureDetailSchema,
  ServerFeatureSummarySchema,
  ServerRecoveryItemSchema,
  ServerRelationshipChildSchema,
  ServerRepoStatusSchema,
  ServerSetupSchema,
  ServerSetupTaskSchema,
} from './parse';
import { SafeErrorException } from '../errors';
import { MAX_PAYLOAD_BYTES } from '../sanitize';

const healthFixture = {
  api_version: 'v1',
  status: 'ok',
  runtime: { runtime_dir: '/tmp/rt', state_dir: '/tmp/rt/features', config_path: '/tmp/c.yaml' },
  launch_policy: { resolved: true, providers: ['claude'], dangerously_skip_permissions: false },
  started_at: '2026-07-14T00:00:00Z',
  owner: { pid: 123, started_at: '2026-07-14T00:00:00Z' },
  server_time: '2026-07-14T00:00:01Z',
  compatibility: {
    api_version: 'v1',
    schema_version: 1,
    min_client_schema: 1,
    runtime_policy: 'loopback-bearer-v1',
    server_build: { version: 'v0.9.0' },
  },
};

function failure(fn: () => unknown): { code: string; message: string; remediation?: string } {
  try {
    fn();
  } catch (err) {
    if (err instanceof SafeErrorException) return err.safe;
    throw err;
  }
  throw new Error('expected parse to fail closed');
}

describe('parseServerJson', () => {
  it('parses a well-formed health response', () => {
    const parsed = parseServerJson(JSON.stringify(healthFixture), HealthResponseSchema);
    expect(parsed.status).toBe('ok');
    expect(parsed.owner.pid).toBe(123);
  });

  it('accepts a health payload carrying the optional bounded server name', () => {
    const parsed = parseServerJson(
      JSON.stringify({ ...healthFixture, name: 'frothy-macchiato' }),
      HealthResponseSchema,
    );
    expect(parsed.name).toBe('frothy-macchiato');
    // Name-less payloads from older servers stay valid.
    expect(
      parseServerJson(JSON.stringify(healthFixture), HealthResponseSchema).name,
    ).toBeUndefined();
  });

  it('drops an oversized or malformed server name instead of failing the health parse', () => {
    for (const bad of ['x'.repeat(65), 42, { nested: true }]) {
      const parsed = parseServerJson(
        JSON.stringify({ ...healthFixture, name: bad }),
        HealthResponseSchema,
      );
      expect(parsed.name).toBeUndefined();
      expect(parsed.status).toBe('ok');
    }
    // Exactly at the bound is accepted.
    const atBound = parseServerJson(
      JSON.stringify({ ...healthFixture, name: 'x'.repeat(64) }),
      HealthResponseSchema,
    );
    expect(atBound.name).toBe('x'.repeat(64));
  });

  it('fails closed on malformed JSON without echoing the payload', () => {
    const safe = failure(() => parseServerJson('{"secretfragment": tru', HealthResponseSchema));
    expect(safe.code).toBe('E_MALFORMED_RESPONSE');
    expect(JSON.stringify(safe)).not.toContain('secretfragment');
  });

  it('fails closed on oversized payloads before JSON parsing', () => {
    const huge = `{"pad":"${'x'.repeat(MAX_PAYLOAD_BYTES)}"}`;
    const safe = failure(() => parseServerJson(huge, HealthResponseSchema));
    expect(safe.code).toBe('E_PAYLOAD_TOO_LARGE');
  });

  it('fails closed on prototype-polluting payloads', () => {
    const polluted = JSON.stringify(healthFixture).replace('"status"', '"__proto__":{},"status"');
    const safe = failure(() => parseServerJson(polluted, HealthResponseSchema));
    expect(safe.code).toBe('E_UNSAFE_PAYLOAD');
  });

  it('fails closed on incompatible api_version with remediation', () => {
    const incompatible = JSON.stringify({ ...healthFixture, api_version: 'v9' });
    const safe = failure(() => parseServerJson(incompatible, HealthResponseSchema));
    expect(safe.code).toBe('E_API_VERSION_INCOMPATIBLE');
    expect(safe.remediation).toBeTruthy();
  });

  it('fails closed on schema mismatch, reporting paths but never values', () => {
    const bad = JSON.stringify({ ...healthFixture, owner: { pid: 'not-a-pid-hunter2' } });
    const safe = failure(() => parseServerJson(bad, HealthResponseSchema));
    expect(safe.code).toBe('E_SCHEMA_MISMATCH');
    expect(safe.message).toContain('owner');
    expect(JSON.stringify(safe)).not.toContain('hunter2');
  });

  it('works for arbitrary schemas at the IPC boundary too', () => {
    const schema = z.object({ n: z.number() });
    expect(parseServerJson('{"n": 4}', schema)).toEqual({ n: 4 });
    expect(failure(() => parseServerJson('{"n": "4"}', schema)).code).toBe('E_SCHEMA_MISMATCH');
  });

  it('accepts bounded unknown verification actions but rejects unbounded ones', () => {
    const promptFixture = {
      api_version: 'v1',
      ask_user_questions: [],
      help_queue: [],
      need_user_inputs: [
        {
          feature_id: 'feature-1',
          open: true,
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
            allowed_actions: ['WAIVE', 'x'.repeat(50)],
          },
        },
      ],
    };

    const parsed = parseServerJson(JSON.stringify(promptFixture), PromptSnapshotResponseSchema);
    expect(parsed.need_user_inputs[0]?.verification?.allowed_actions).toEqual([
      'WAIVE',
      'x'.repeat(50),
    ]);

    promptFixture.need_user_inputs[0]!.verification.allowed_actions = ['x'.repeat(51)];
    expect(
      failure(() => parseServerJson(JSON.stringify(promptFixture), PromptSnapshotResponseSchema))
        .code,
    ).toBe('E_SCHEMA_MISMATCH');
  });

  it('accepts aggregate-fallback blockers only with an empty capability array', () => {
    const fallbackFixture = {
      api_version: 'v1',
      ask_user_questions: [],
      help_queue: [],
      need_user_inputs: [
        {
          feature_id: 'feature-1',
          open: true,
          questions: [{ index: 1, prompt: 'Choose an action.', answer: '' }],
          verification: {
            blockers: [
              {
                item_id: 'capability-heavy',
                name: 'Capability-heavy check',
                command: 'make verify',
                reason: 'missing access',
                capabilities: [],
                remediation: 'Grant access and retry.',
              },
            ],
            allowed_actions: ['WAIVE', 'RETRY_AFTER_AUTH'],
          },
        },
      ],
    };

    expect(
      parseServerJson(JSON.stringify(fallbackFixture), PromptSnapshotResponseSchema)
        .need_user_inputs[0]?.verification?.blockers[0]?.capabilities,
    ).toEqual([]);
    fallbackFixture.need_user_inputs[0]!.verification.blockers[0]!.capabilities = null as never;
    expect(
      failure(() => parseServerJson(JSON.stringify(fallbackFixture), PromptSnapshotResponseSchema))
        .code,
    ).toBe('E_SCHEMA_MISMATCH');
  });

  it('accepts generic verification fallback only with an empty action array', () => {
    const fallbackFixture = {
      api_version: 'v1',
      ask_user_questions: [],
      help_queue: [],
      need_user_inputs: [
        {
          feature_id: 'feature-1',
          open: true,
          questions: [{ index: 1, prompt: 'Choose an action.', answer: '' }],
          verification: {
            blockers: [
              {
                item_id: 'future-action',
                name: 'Future verification check',
                command: 'make verify',
                reason: 'the server did not recognize a legacy action',
                capabilities: [],
                remediation: 'Answer the generic prompt.',
              },
            ],
            allowed_actions: [],
          },
        },
      ],
    };

    expect(
      parseServerJson(JSON.stringify(fallbackFixture), PromptSnapshotResponseSchema)
        .need_user_inputs[0]?.verification?.allowed_actions,
    ).toEqual([]);
    fallbackFixture.need_user_inputs[0]!.verification.allowed_actions = null as never;
    expect(
      failure(() => parseServerJson(JSON.stringify(fallbackFixture), PromptSnapshotResponseSchema))
        .code,
    ).toBe('E_SCHEMA_MISMATCH');
  });
});

describe('ServerRelationshipChildSchema diff_summary bound', () => {
  const child = (diffSummary: string) => ({
    id: 'abcd1234ef567890',
    name: 'Refactor pass',
    kind: 'refactor',
    display_token: 'R1',
    display_state: 'Completed',
    pipeline: 'medium',
    status: 'Done',
    started_at: '2026-07-14T00:00:00Z',
    cost: { total_usd: 0, by_phase: {} },
    integration_state: 'merged',
    warnings: [],
    diff_summary: diffSummary,
  });

  it('accepts a summary inside the server budget', () => {
    const parsed = ServerRelationshipChildSchema.safeParse(
      child('x'.repeat(DIFF_SUMMARY_MAX_BYTES)),
    );
    expect(parsed.success).toBe(true);
  });

  it('rejects the oversized field itself rather than the whole payload', () => {
    const parsed = ServerRelationshipChildSchema.safeParse(
      child('x'.repeat(DIFF_SUMMARY_MAX_BYTES + 1)),
    );
    expect(parsed.success).toBe(false);
    expect(parsed.error?.issues[0]?.path).toEqual(['diff_summary']);
  });

  it('accepts the list projection: has_diff_summary with no body', () => {
    const { diff_summary: _omitted, ...summary } = child('');
    const parsed = ServerRelationshipChildSchema.safeParse({
      ...summary,
      has_diff_summary: true,
    });
    expect(parsed.success).toBe(true);
    expect(parsed.data?.has_diff_summary).toBe(true);
    expect(parsed.data?.diff_summary).toBeUndefined();
  });
});

describe('ServerFeatureSummarySchema bounded child history', () => {
  it('carries the true total and the truncation flag', () => {
    const parsed = ServerFeatureSummarySchema.safeParse({
      id: 'abcd1234ef567890',
      name: 'Search revamp',
      slug: 'search-revamp',
      status: 'Published',
      current_phase: 'Done',
      repos: ['repo-a'],
      created_at: '2026-07-14T10:00:00Z',
      active_run: 1,
      run_count: 1,
      progress: {},
      child_history: [],
      child_history_total: 12,
      child_history_truncated: true,
    });
    expect(parsed.success).toBe(true);
    expect(parsed.data?.child_history_total).toBe(12);
    expect(parsed.data?.child_history_truncated).toBe(true);
  });
});

describe('integration attention single owner', () => {
  const canonicalAttention = {
    code: 'integration_merge_conflict',
    class: 'needs_action',
    title: 'Integration merge conflict',
    summary: 'The merge candidate for repository "repo-a" conflicted on 1 file.',
    remediation: {
      hint: 'Resolve the conflict in the pass worktree and retry.',
      actions: ['retry'],
    },
    context: { repositories: [{ name: 'repo-a', branch: 'main', conflict_files: ['query.ts'] }] },
    diagnostics: 'repo-a: merge candidate conflict: [query.ts]',
  };

  it('accepts a canonical transaction attention and rejects the old free-form string', () => {
    const transactionField = ServerFeatureDetailSchema.pick({ transaction: true });
    const ok = transactionField.safeParse({
      transaction: {
        phase: 'attention',
        attention: canonicalAttention,
        entries: [{ repo: 'repo-a', prep_state: 'failed', pending_sync: false }],
      },
    });
    expect(ok.success).toBe(true);
    expect(ok.data?.transaction?.attention?.code).toBe('integration_merge_conflict');
    expect(ok.data?.transaction?.entries?.[0]?.pending_sync).toBe(false);

    const legacyString = transactionField.safeParse({
      transaction: { phase: 'attention', attention: 'Integration needs recovery' },
    });
    expect(legacyString.success).toBe(false);
  });

  it('rejects the deleted entry diagnostics, conflict-file, dirty, and cleanup-warning shapes', () => {
    const transactionField = ServerFeatureDetailSchema.pick({ transaction: true });
    for (const entry of [
      { repo: 'repo-a', diagnostics: 'merge conflict' },
      { repo: 'repo-a', conflict_files: ['query.ts'] },
      { repo: 'repo-a', dirty: [{ path: '/safe/repo-a', staged_total: 1 }] },
      { repo: 'repo-a', cleanup_warning: 'worktree removal failed' },
    ]) {
      const parsed = transactionField.safeParse({
        transaction: { phase: 'attention', attention: canonicalAttention, entries: [entry] },
      });
      expect(parsed.success).toBe(false);
    }
  });

  it('accepts a canonical relationship attention and rejects the old array-of-items shape', () => {
    const ok = ServerRelationshipChildSchema.safeParse({
      id: 'abcd1234ef567890',
      name: 'Refactor pass',
      kind: 'refactor',
      display_token: 'R1',
      display_state: 'Active — ReviewPassed',
      pipeline: 'medium',
      status: 'ReviewPassed',
      started_at: '2026-07-14T00:00:00Z',
      cost: { total_usd: 0, by_phase: {} },
      integration_state: 'attention',
      attention: canonicalAttention,
      warnings: [],
    });
    expect(ok.success).toBe(true);
    expect(ok.data?.attention?.class).toBe('needs_action');

    const legacyArray = ServerRelationshipChildSchema.safeParse({
      id: 'abcd1234ef567890',
      name: 'Refactor pass',
      kind: 'refactor',
      display_token: 'R1',
      display_state: 'Active — ReviewPassed',
      pipeline: 'medium',
      status: 'ReviewPassed',
      started_at: '2026-07-14T00:00:00Z',
      cost: { total_usd: 0, by_phase: {} },
      integration_state: 'attention',
      attention: [{ code: 'conflict', message: 'Resolve conflict', repo: 'repo-a' }],
      warnings: [],
    });
    expect(legacyArray.success).toBe(false);
  });

  it('accepts a relationship child with no attention at all', () => {
    const parsed = ServerRelationshipChildSchema.safeParse({
      id: 'abcd1234ef567890',
      name: 'Refactor pass',
      kind: 'refactor',
      display_token: 'R1',
      display_state: 'Active — ReviewPassed',
      pipeline: 'medium',
      status: 'ReviewPassed',
      started_at: '2026-07-14T00:00:00Z',
      cost: { total_usd: 0, by_phase: {} },
      integration_state: 'pending',
      warnings: [],
    });
    expect(parsed.success).toBe(true);
    expect(parsed.data?.attention).toBeUndefined();
  });
});

describe('ServerFeatureDetailSchema failure', () => {
  const failureField = ServerFeatureDetailSchema.pick({ failure: true });

  it('accepts a canonical catalog-rendered failure', () => {
    const parsed = failureField.safeParse({
      failure: {
        code: 'worktree_setup_failed',
        class: 'blocking',
        title: 'Worktree setup failed',
        summary: 'Setting up the worktree for repository "repo-a" failed.',
        remediation: {
          hint: 'Resolve the reported problem in the repository, then retry setup.',
          actions: ['setup'],
        },
        context: { repositories: [{ name: 'repo-a', branch: 'feature/search-revamp' }] },
        diagnostics: 'git worktree add failed: no commits yet',
      },
    });
    expect(parsed.success).toBe(true);
    expect(parsed.data?.failure?.code).toBe('worktree_setup_failed');
    expect(parsed.data?.failure?.remediation?.actions).toEqual(['setup']);
  });

  it('rejects the pre-canonical {type,message} failure shape', () => {
    const parsed = failureField.safeParse({
      failure: { type: 'worktree_setup', message: 'git worktree add failed: no commits yet' },
    });
    expect(parsed.success).toBe(false);
  });

  it('accepts a canonical error with a setup_task block', () => {
    const parsed = CanonicalErrorSchema.safeParse({
      code: 'worktree_setup_failed',
      class: 'blocking',
      title: 'Worktree setup failed',
      summary: 'Setup task "Worktree: repo-a" failed.',
      remediation: {
        hint: 'Resolve the reported problem in the repository or branch, then retry setup.',
        actions: ['setup'],
      },
      context: {
        setup_task: { key: 'worktree:repo-a', kind: 'worktree', label: 'Worktree: repo-a' },
      },
    });
    expect(parsed.success).toBe(true);
    expect(parsed.data?.context?.setup_task?.key).toBe('worktree:repo-a');
  });

  it('accepts a setup task with a canonical error and rejects stale last_error keys', () => {
    const relationshipChildFixture = {
      id: 'abcd1234ef567890',
      name: 'Refactor pass',
      kind: 'refactor',
      display_token: 'R1',
      display_state: 'Completed',
      pipeline: 'medium',
      status: 'Done',
      started_at: '2026-07-14T00:00:00Z',
      cost: { total_usd: 0, by_phase: {} },
      integration_state: 'merged',
      warnings: [],
    };
    const canonicalError = {
      code: 'worktree_setup_failed',
      class: 'blocking',
      title: 'Worktree setup failed',
      summary: 'Setting up the worktree for repository "repo-a" failed.',
      remediation: { hint: 'Fix the repository, then retry setup.', actions: ['setup'] },
      context: { repositories: [{ name: 'repo-a', branch: 'feature/repo-a' }] },
      diagnostics: 'git worktree add failed: no commits yet',
    };
    const task = {
      key: 'worktree:repo-a',
      kind: 'worktree',
      label: 'Worktree: repo-a',
      repo: 'repo-a',
      status: 'failed',
      branch: 'feature/repo-a',
      attempt: 1,
      error: canonicalError,
    };
    expect(ServerSetupTaskSchema.safeParse(task).success).toBe(true);
    expect(
      ServerSetupSchema.safeParse({
        status: 'failed',
        attempt: 1,
        tasks: { 'worktree:repo-a': task },
        task_order: ['worktree:repo-a'],
      }).success,
    ).toBe(true);

    // The removed last_error strings fail parsing everywhere they could
    // reappear: on the task, on the aggregate, and on a relationship child.
    expect(ServerSetupTaskSchema.safeParse({ ...task, last_error: 'boom' }).success).toBe(false);
    expect(ServerSetupSchema.safeParse({ status: 'failed', last_error: 'boom' }).success).toBe(
      false,
    );
    expect(
      ServerRelationshipChildSchema.safeParse({ ...relationshipChildFixture, last_error: 'boom' })
        .success,
    ).toBe(false);
  });
});

describe('CanonicalErrorResponseSchema', () => {
  const canonicalError = {
    code: 'parent_worktrees_dirty',
    class: 'needs_action',
    title: 'Parent worktrees are dirty',
    summary: "The parent feature's worktrees have uncommitted changes.",
    remediation: { hint: 'Commit or stash the listed changes in each repository, then retry.' },
    context: {
      repositories: [{ name: 'repo-a', branch: 'main', dirty_files: ['src/query.ts'] }],
      phase: { name: 'implement', iteration: 2 },
      command: { exit_code: 1, log_paths: ['logs/repo-a.log'] },
    },
    diagnostics: 'git status reported uncommitted changes',
  };

  it('accepts exactly the canonical shape and returns its fields', () => {
    const parsed = CanonicalErrorResponseSchema.safeParse({
      api_version: 'v1',
      error: canonicalError,
    });
    expect(parsed.success).toBe(true);
    expect(parsed.data?.api_version).toBe('v1');
    expect(parsed.data?.error.code).toBe('parent_worktrees_dirty');
    expect(parsed.data?.error.class).toBe('needs_action');
    expect(parsed.data?.error.title).toBe('Parent worktrees are dirty');
    expect(parsed.data?.error.summary).toBe(
      "The parent feature's worktrees have uncommitted changes.",
    );
    expect(parsed.data?.error.remediation?.hint).toBe(
      'Commit or stash the listed changes in each repository, then retry.',
    );
    expect(parsed.data?.error.context?.repositories?.[0]).toEqual({
      name: 'repo-a',
      branch: 'main',
      dirty_files: ['src/query.ts'],
    });
    expect(parsed.data?.error.context?.phase).toEqual({ name: 'implement', iteration: 2 });
    expect(parsed.data?.error.context?.command).toEqual({
      exit_code: 1,
      log_paths: ['logs/repo-a.log'],
    });
    expect(parsed.data?.error.diagnostics).toBe('git status reported uncommitted changes');
  });

  it('rejects the pre-canonical {code,message,status} error body', () => {
    const parsed = CanonicalErrorResponseSchema.safeParse({
      api_version: 'v1',
      error: { code: 'conflict', message: 'review draft revision is stale', status: 409 },
    });
    expect(parsed.success).toBe(false);
  });

  it('rejects an unknown class value', () => {
    const parsed = CanonicalErrorResponseSchema.safeParse({
      api_version: 'v1',
      error: { ...canonicalError, class: 'critical' },
    });
    expect(parsed.success).toBe(false);
  });

  it('rejects unknown extra properties on the error object', () => {
    const parsed = CanonicalErrorResponseSchema.safeParse({
      api_version: 'v1',
      error: { ...canonicalError, target: 'feature-1' },
    });
    expect(parsed.success).toBe(false);
  });

  it('rejects a repositories entry missing name', () => {
    const parsed = CanonicalErrorResponseSchema.safeParse({
      api_version: 'v1',
      error: {
        ...canonicalError,
        context: { repositories: [{ branch: 'main', dirty_files: ['src/query.ts'] }] },
      },
    });
    expect(parsed.success).toBe(false);
    expect(parsed.error?.issues[0]?.path).toEqual(['error', 'context', 'repositories', 0, 'name']);
  });
});

describe('Repository publish-failure error contract', () => {
  const repoError = {
    code: 'publish_pull_request_failed',
    class: 'needs_action',
    title: 'Pull-request creation failed',
    summary: 'Creating the pull request for repository "repo-a" failed.',
    remediation: { hint: 'Check GitHub access, then retry.', actions: ['publish'] },
    context: {
      repositories: [
        { name: 'repo-a', branch: 'feature/f', rebase_target: 'main', remote_only_commits: 3 },
      ],
    },
    diagnostics: 'POST /repos/org/repo-a/pulls: 502 Bad Gateway',
  };
  const repoStatus = {
    name: 'repo-a',
    publishable: true,
    touched: true,
    error: repoError,
  };
  const preflightRepo = {
    repo: 'repo-a',
    publishable: true,
    touched: true,
    status: 'unpublished_changes',
    error: repoError,
  };

  it('accepts a repository status and a preflight repository carrying the canonical error', () => {
    expect(ServerRepoStatusSchema.safeParse(repoStatus).success).toBe(true);
    expect(CompletionPreflightRepoSchema.safeParse(preflightRepo).success).toBe(true);
    const parsed = ServerRepoStatusSchema.parse(repoStatus);
    expect(parsed.error?.context?.repositories?.[0]).toEqual({
      name: 'repo-a',
      branch: 'feature/f',
      rebase_target: 'main',
      remote_only_commits: 3,
    });
  });

  it('accepts the two new publish fields on a canonical error repository entry', () => {
    const parsed = CanonicalErrorSchema.safeParse(repoError);
    expect(parsed.success).toBe(true);
    expect(parsed.data?.context?.repositories?.[0]?.rebase_target).toBe('main');
    expect(parsed.data?.context?.repositories?.[0]?.remote_only_commits).toBe(3);
  });

  it('rejects stale last_error keys on the repository status and preflight repository', () => {
    expect(ServerRepoStatusSchema.safeParse({ ...repoStatus, last_error: 'boom' }).success).toBe(
      false,
    );
    expect(
      CompletionPreflightRepoSchema.safeParse({ ...preflightRepo, last_error: 'boom' }).success,
    ).toBe(false);
  });
});

describe('canonical warning wire shapes', () => {
  const canonicalWarning = {
    code: 'rewind_worktree_reset',
    class: 'warning',
    title: 'Worktree reset to anchor',
    summary: 'The worktree for repository "repo-a" was reset to its anchor commit.',
  };

  it('rejects a relationship child carrying the removed cleanup_warnings array', () => {
    const base = {
      id: 'abcd1234ef567890',
      name: 'Refactor pass',
      kind: 'refactor',
      display_token: 'R1',
      display_state: 'Completed',
      pipeline: 'medium',
      status: 'Done',
      started_at: '2026-07-14T00:00:00Z',
      cost: { total_usd: 0, by_phase: {} },
      integration_state: 'merged',
      warnings: [canonicalWarning],
    };
    expect(ServerRelationshipChildSchema.safeParse(base).success).toBe(true);
    expect(
      ServerRelationshipChildSchema.safeParse({
        ...base,
        cleanup_warnings: [{ repo: 'repo-a', message: 'worktree removal failed' }],
      }).success,
    ).toBe(false);
    const { warnings: _omitted, ...missingWarnings } = base;
    void _omitted;
    expect(ServerRelationshipChildSchema.safeParse(missingWarnings).success).toBe(false);
  });

  it('rejects a rewind response with string warnings or the removed warning_count', () => {
    const base = {
      api_version: 'v1',
      result: 'rewound',
      feature_id: 'abcd1234ef567890',
      warnings: [canonicalWarning],
    };
    expect(RewindActionResponseSchema.safeParse(base).success).toBe(true);
    const stringWarnings = RewindActionResponseSchema.safeParse({
      ...base,
      warnings: ['plain string warning'],
    });
    expect(stringWarnings.success).toBe(false);
    expect(RewindActionResponseSchema.safeParse({ ...base, warning_count: 1 }).success).toBe(false);
  });

  it('rejects a repository diff response carrying the removed partial_failure string', () => {
    const base = {
      api_version: 'v1',
      feature_id: 'abcd1234ef567890',
      repo: 'repo-a',
      files: [],
      error: canonicalWarning,
    };
    expect(RepositoryDiffResponseSchema.safeParse(base).success).toBe(true);
    const stalePartialFailure = RepositoryDiffResponseSchema.safeParse({
      ...base,
      partial_failure: 'repo unreachable',
    });
    expect(stalePartialFailure.success).toBe(false);
    const { error: _omitted, ...withoutError } = base;
    void _omitted;
    expect(RepositoryDiffResponseSchema.safeParse(withoutError).success).toBe(true);
  });

  it('rejects a recovery item without its required canonical orphan error', () => {
    const base = {
      key: 'feature-alpha:repo-a',
      feature_id: 'alpha1234ef567890',
      process_alive: true,
      allowed_actions: ['resume', 'kill'],
      default_action: 'resume',
      error: {
        code: 'orphan_session_live',
        class: 'needs_action',
        title: 'Orphan session still running',
        summary: 'The session process is still alive after its run was interrupted.',
      },
    };
    expect(ServerRecoveryItemSchema.safeParse(base).success).toBe(true);
    const { error: _omitted, ...withoutError } = base;
    void _omitted;
    expect(ServerRecoveryItemSchema.safeParse(withoutError).success).toBe(false);
  });

  it('accepts canonical list-level and per-feature warnings on the feature list response', () => {
    const parsed = FeatureListResponseSchema.safeParse({
      api_version: 'v1',
      features: [],
      warnings: [canonicalWarning],
    });
    expect(parsed.success).toBe(true);
    expect(parsed.data?.warnings?.[0]?.code).toBe('rewind_worktree_reset');
  });
});
