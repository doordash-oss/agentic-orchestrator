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
  ReadinessResponseSchema,
  RepositorySourcesResponseSchema,
  RepositoryOriginStatusResponseSchema,
  RepositoryUpdateSourceResponseSchema,
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
import { CanonicalErrorException, type CanonicalError } from '../errors';
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

describe('repository source response contract', () => {
  const identity = {
    path: '/work/repo-a',
    common_dir: '/work/repo-a/.git',
    device: '1',
    inode: '2',
  };

  it('accepts independent branch and detached sources with full local names and SHAs', () => {
    const parsed = RepositorySourcesResponseSchema.parse({
      api_version: 'v1',
      repositories: [
        {
          repo_key: 'repo-a',
          identity,
          mode: 'default',
          kind: 'branch',
          branch: 'release/2026/q3',
          observed_sha: 'a'.repeat(40),
        },
        {
          repo_key: 'repo-b',
          identity: {
            path: '/work/repo-b',
            common_dir: '/work/repo-b/.git',
            device: '3',
            inode: '4',
          },
          mode: 'current',
          kind: 'detached',
          observed_sha: 'b'.repeat(64),
        },
      ],
    });

    expect(
      parsed.repositories.map(({ repo_key, kind, branch, observed_sha }) => ({
        repo_key,
        kind,
        branch,
        observed_sha,
      })),
    ).toStrictEqual([
      {
        repo_key: 'repo-a',
        kind: 'branch',
        branch: 'release/2026/q3',
        observed_sha: 'a'.repeat(40),
      },
      {
        repo_key: 'repo-b',
        kind: 'detached',
        branch: undefined,
        observed_sha: 'b'.repeat(64),
      },
    ]);
  });

  it('rejects empty batches, invalid SHAs, and renderer-authority fields', () => {
    expect(
      RepositorySourcesResponseSchema.safeParse({ api_version: 'v1', repositories: [] }).success,
    ).toBe(false);
    expect(
      RepositorySourcesResponseSchema.safeParse({
        api_version: 'v1',
        repositories: [
          {
            repo_key: 'repo-a',
            identity,
            mode: 'current',
            kind: 'detached',
            observed_sha: 'not-a-sha',
          },
        ],
      }).success,
    ).toBe(false);
    expect(
      RepositorySourcesResponseSchema.safeParse({
        api_version: 'v1',
        repositories: [
          {
            repo_key: 'repo-a',
            identity,
            mode: 'default',
            kind: 'branch',
            branch: 'main',
            observed_sha: 'a'.repeat(40),
            path: '/renderer/chosen/path',
            revision: 'refs/tags/renderer-chosen',
          },
        ],
      }).success,
    ).toBe(false);
  });
});

describe('repository origin status response contract', () => {
  const identity = {
    path: '/work/repo-a',
    common_dir: '/work/repo-a/.git',
    device: '1',
    inode: '2',
  };

  it('accepts typed snapshots with comparisons, stale history, and advisory update state', () => {
    const parsed = RepositoryOriginStatusResponseSchema.parse({
      api_version: 'v1',
      repositories: [
        {
          repo_key: 'repo-a',
          identity,
          mode: 'default',
          kind: 'branch',
          branch: 'release/2026/q3',
          local_sha: 'a'.repeat(40),
          origin_branch: 'upstream-main',
          fetched_sha: 'c'.repeat(40),
          checked_at: '2026-09-09T10:00:00Z',
          status: 'behind',
          ahead_count: 0,
          behind_count: 2,
          update_eligible: false,
          update_blockers: ['dirty_target_checkout'],
        },
        {
          repo_key: 'repo-b',
          identity: {
            path: '/work/repo-b',
            common_dir: '/work/repo-b/.git',
            device: '3',
            inode: '4',
          },
          mode: 'current',
          kind: 'detached',
          commit: 'b'.repeat(40),
          status: 'unknown',
          issue: {
            code: 'origin_check_unavailable',
            class: 'warning',
            title: 'Origin check unavailable',
            summary: 'The local source could not be compared with its origin.',
          },
          stale_comparison: {
            status: 'behind',
            local_sha: 'b'.repeat(40),
            fetched_sha: 'd'.repeat(40),
            origin_branch: 'main',
            ahead_count: 0,
            behind_count: 1,
            checked_at: '2026-09-09T09:00:00Z',
          },
        },
        {
          repo_key: 'repo-c',
          identity: {
            path: '/work/repo-c',
            common_dir: '/work/repo-c/.git',
            device: '5',
            inode: '6',
          },
          mode: 'default',
          kind: 'branch',
          branch: 'main',
          local_sha: 'e'.repeat(40),
          origin_branch: 'main',
          status: 'checking',
        },
      ],
    });
    expect(parsed.repositories[0]?.status).toBe('behind');
    expect(parsed.repositories[0]?.update_blockers).toEqual(['dirty_target_checkout']);
    expect(parsed.repositories[1]?.stale_comparison?.behind_count).toBe(1);
    expect(parsed.repositories[1]?.issue?.code).toBe('origin_check_unavailable');
    expect(parsed.repositories[2]?.checked_at).toBeUndefined();
  });

  it('accepts observed checkout HEAD identity on rows', () => {
    const parsed = RepositoryOriginStatusResponseSchema.parse({
      api_version: 'v1',
      repositories: [
        {
          repo_key: 'repo-a',
          identity,
          mode: 'default',
          kind: 'branch',
          branch: 'main',
          local_sha: 'a'.repeat(40),
          origin_branch: 'main',
          status: 'behind',
          update_eligible: true,
          checkout_head_ref: 'refs/heads/main',
          checkout_head_sha: 'a'.repeat(40),
        },
      ],
    });
    expect(parsed.repositories[0]?.checkout_head_ref).toBe('refs/heads/main');
    expect(parsed.repositories[0]?.checkout_head_sha).toBe('a'.repeat(40));
    expect(
      RepositoryOriginStatusResponseSchema.safeParse({
        api_version: 'v1',
        repositories: [
          {
            repo_key: 'repo-a',
            identity,
            mode: 'default',
            kind: 'branch',
            branch: 'main',
            status: 'behind',
            checkout_head_sha: 'not-a-sha',
          },
        ],
      }).success,
    ).toBe(false);
  });

  it('rejects invented SHAs, unknown statuses, and renderer-authority fields', () => {
    const base = {
      repo_key: 'repo-a',
      identity,
      mode: 'default' as const,
      kind: 'branch' as const,
      branch: 'main',
    };
    expect(
      RepositoryOriginStatusResponseSchema.safeParse({
        api_version: 'v1',
        repositories: [{ ...base, status: 'sideways' }],
      }).success,
    ).toBe(false);
    expect(
      RepositoryOriginStatusResponseSchema.safeParse({
        api_version: 'v1',
        repositories: [{ ...base, status: 'behind', fetched_sha: 'not-a-sha' }],
      }).success,
    ).toBe(false);
    expect(
      RepositoryOriginStatusResponseSchema.safeParse({
        api_version: 'v1',
        repositories: [
          {
            ...base,
            status: 'checking',
            checked_at: '2026-09-09T10:00:00Z',
            path: '/renderer/path',
          },
        ],
      }).success,
    ).toBe(false);
    expect(
      RepositoryOriginStatusResponseSchema.safeParse({
        api_version: 'v1',
        repositories: Array.from({ length: 33 }, (_, index) => ({
          ...base,
          repo_key: `repo-${index}`,
          status: 'checking',
        })),
      }).success,
    ).toBe(false);
  });
});

describe('repository update source response contract', () => {
  const identity = {
    path: '/work/repo-a',
    common_dir: '/work/repo-a/.git',
    device: '1',
    inode: '2',
  };

  it('accepts an updated result with previous, local, and fetched SHAs', () => {
    const parsed = RepositoryUpdateSourceResponseSchema.parse({
      api_version: 'v1',
      result: 'updated',
      repo_key: 'repo-a',
      identity,
      mode: 'default',
      branch: 'release/2026/q3',
      origin_branch: 'upstream-main',
      previous_sha: 'a'.repeat(40),
      local_sha: 'c'.repeat(40),
      fetched_sha: 'c'.repeat(40),
    });
    expect(parsed.result).toBe('updated');
    expect(parsed.reason).toBeUndefined();
    expect(parsed.status).toBeUndefined();
    expect(parsed.previous_sha).toBe('a'.repeat(40));
    expect(parsed.local_sha).toBe('c'.repeat(40));
    expect(parsed.fetched_sha).toBe('c'.repeat(40));
  });

  it('accepts a stale refusal carrying a freshly resolved status snapshot', () => {
    const parsed = RepositoryUpdateSourceResponseSchema.parse({
      api_version: 'v1',
      result: 'stale',
      reason: 'branch_checked_out',
      repo_key: 'repo-a',
      identity,
      mode: 'default',
      branch: 'release/2026/q3',
      origin_branch: 'upstream-main',
      status: {
        repo_key: 'repo-a',
        identity,
        mode: 'default',
        kind: 'branch',
        branch: 'release/2026/q3',
        local_sha: 'a'.repeat(40),
        origin_branch: 'upstream-main',
        status: 'behind',
        ahead_count: 0,
        behind_count: 3,
        update_eligible: false,
        update_blockers: ['branch_checked_out_in_original_checkout'],
        checkout_head_ref: 'refs/heads/main',
        checkout_head_sha: 'e'.repeat(40),
      },
    });
    expect(parsed.result).toBe('stale');
    expect(parsed.reason).toBe('branch_checked_out');
    expect(parsed.status?.status).toBe('behind');
    expect(parsed.status?.update_blockers).toEqual(['branch_checked_out_in_original_checkout']);
    expect(parsed.status?.checkout_head_ref).toBe('refs/heads/main');
    expect(parsed.status?.checkout_head_sha).toBe('e'.repeat(40));
  });

  it('rejects invented SHAs, unknown results and reasons, and renderer-authority fields', () => {
    const base = {
      repo_key: 'repo-a',
      identity,
      mode: 'default' as const,
      branch: 'main',
      origin_branch: 'main',
    };
    expect(
      RepositoryUpdateSourceResponseSchema.safeParse({
        api_version: 'v1',
        result: 'sideways',
        ...base,
      }).success,
    ).toBe(false);
    expect(
      RepositoryUpdateSourceResponseSchema.safeParse({
        api_version: 'v1',
        result: 'stale',
        reason: 'sideways',
        ...base,
      }).success,
    ).toBe(false);
    expect(
      RepositoryUpdateSourceResponseSchema.safeParse({
        api_version: 'v1',
        result: 'updated',
        ...base,
        local_sha: 'not-a-sha',
      }).success,
    ).toBe(false);
    const withRendererPath = RepositoryUpdateSourceResponseSchema.safeParse({
      api_version: 'v1',
      result: 'updated',
      ...base,
      path: '/renderer/chosen/path',
    });
    expect(withRendererPath.success).toBe(true);
    expect(withRendererPath.success && 'path' in withRendererPath.data).toBe(false);
  });
});

function failure(fn: () => unknown): CanonicalError {
  try {
    fn();
  } catch (err) {
    if (err instanceof CanonicalErrorException) return err.canonical;
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
    expect(safe.summary).toContain('owner');
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

describe('CanonicalErrorSchema code format', () => {
  const base = { class: 'blocking' as const, title: 'Title', summary: 'Summary.' };

  it('accepts both code families: server snake_case and desktop E_ codes', () => {
    expect(
      CanonicalErrorSchema.safeParse({ ...base, code: 'publish_pull_request_failed' }).success,
    ).toBe(true);
    expect(CanonicalErrorSchema.safeParse({ ...base, code: 'E_SERVER_CRASHED' }).success).toBe(
      true,
    );
  });

  it('rejects codes outside both families', () => {
    for (const code of ['Server_Crashed', 'e_x', 'E_lower', 'HTTP-500', '', 'E_']) {
      expect(CanonicalErrorSchema.safeParse({ ...base, code }).success, code).toBe(false);
    }
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

describe('owned error wire shapes on the feature summary', () => {
  const summaryBase = {
    id: 'abcd1234ef567890',
    name: 'Search revamp',
    slug: 'search-revamp',
    status: 'Failed',
    current_phase: 'Implement',
    repos: ['repo-a'],
    created_at: '2026-07-14T10:00:00Z',
    active_run: 1,
    run_count: 1,
    progress: {},
  };
  const runEntry = {
    ref: { scope: 'run', code: 'iteration_budget_exhausted', feature_id: 'abcd1234ef567890' },
    error: {
      code: 'iteration_budget_exhausted',
      class: 'blocking',
      title: 'Iteration budget exhausted',
      summary: 'The Implement phase exhausted its iteration budget.',
    },
  };
  const repoEntry = {
    ref: {
      scope: 'repository',
      code: 'publish_rebase_conflict',
      feature_id: 'abcd1234ef567890',
      repository: 'repo-a',
    },
    error: {
      code: 'publish_rebase_conflict',
      class: 'needs_action',
      title: 'Pull-rebase conflict',
      summary: 'The pull rebase for repository "repo-a" conflicted with its target branch.',
    },
  };

  it('parses a summary carrying two owned-error entries', () => {
    const parsed = ServerFeatureSummarySchema.parse({
      ...summaryBase,
      errors: [runEntry, repoEntry],
    });
    expect(parsed.errors).toHaveLength(2);
    expect(parsed.errors?.[0]?.ref.feature_id).toBe('abcd1234ef567890');
    expect(parsed.errors?.[1]?.ref.repository).toBe('repo-a');
    const list = FeatureListResponseSchema.safeParse({
      api_version: 'v1',
      features: [{ ...summaryBase, errors: [runEntry, repoEntry] }],
    });
    expect(list.success).toBe(true);
  });

  it('defaults to no errors when the server omits the field', () => {
    const parsed = ServerFeatureSummarySchema.parse(summaryBase);
    expect(parsed.errors).toBeUndefined();
  });

  it('rejects an entry whose error carries the warning class', () => {
    const warningEntry = {
      ref: { scope: 'run', code: 'rewind_worktree_reset', feature_id: 'abcd1234ef567890' },
      error: {
        code: 'rewind_worktree_reset',
        class: 'warning',
        title: 'Worktree reset to anchor',
        summary: 'The worktree was reset.',
      },
    };
    expect(
      ServerFeatureSummarySchema.safeParse({ ...summaryBase, errors: [warningEntry] }).success,
    ).toBe(false);
  });

  it('rejects an entry whose setup reference lacks the task key', () => {
    const undisciplined = {
      ref: { scope: 'setup', code: 'worktree_setup_failed', feature_id: 'abcd1234ef567890' },
      error: { ...runEntry.error, code: 'worktree_setup_failed' },
    };
    expect(
      ServerFeatureSummarySchema.safeParse({ ...summaryBase, errors: [undisciplined] }).success,
    ).toBe(false);
  });

  it('rejects an entry carrying unknown keys', () => {
    expect(
      ServerFeatureSummarySchema.safeParse({
        ...summaryBase,
        errors: [{ ...runEntry, extra: 'x' }],
      }).success,
    ).toBe(false);
    expect(
      ServerFeatureSummarySchema.safeParse({
        ...summaryBase,
        errors: [{ ...runEntry, error: { ...runEntry.error, diagnostics: 'raw' } }],
      }).success,
    ).toBe(true);
  });
});

describe('Readiness repository identity contract', () => {
  const baseRepository = {
    name: 'service',
    path: '/work/service',
    valid: true,
    feature_ready: true,
  };
  const validIdentity = {
    path: '/work/service',
    common_dir: '/work/service/.git',
    device: '16777234',
    inode: '982394',
  };

  function readinessWith(identity: unknown) {
    return {
      api_version: 'v1',
      ready: true,
      providers: [],
      models: { available: true },
      configuration: { valid: true },
      workspace: {
        roots: [],
        repositories: [{ ...baseRepository, identity }],
      },
    };
  }

  it('accepts a well-formed identity and maps it through the readiness snapshot', () => {
    const parsed = ReadinessResponseSchema.parse(readinessWith(validIdentity));
    expect(parsed.workspace.repositories[0]?.identity).toEqual({
      path: '/work/service',
      common_dir: '/work/service/.git',
      device: '16777234',
      inode: '982394',
    });
  });

  it('accepts a repository without an identity (unselectable, not malformed)', () => {
    const parsed = ReadinessResponseSchema.parse(readinessWith(undefined));
    expect(parsed.workspace.repositories[0]?.identity).toBeUndefined();
  });

  it.each([
    ['non-decimal device', { ...validIdentity, device: '0x1f' }],
    ['negative inode', { ...validIdentity, inode: '-4' }],
    ['numeric device instead of text', { ...validIdentity, device: 16777234 }],
    ['empty path', { ...validIdentity, path: '' }],
    ['missing common_dir', { ...validIdentity, common_dir: undefined }],
    ['oversized path', { ...validIdentity, path: 'x'.repeat(4097) }],
    ['extra field', { ...validIdentity, extra: 'no' }],
  ])('rejects malformed identity data: %s', (_label, identity) => {
    expect(() => ReadinessResponseSchema.parse(readinessWith(identity))).toThrow();
  });
});
