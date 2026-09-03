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
import { CanonicalErrorException } from '../../shared/errors';
import { PromptSnapshotResponseSchema } from '../../shared/api/parse';
import type { HttpResult } from '../gateway/runtimeGateway';
import { mapServerError, serverRequest, type ServerTransport } from '../serverClient';

function transportReturning(result: HttpResult): ServerTransport {
  return { apiRequest: () => Promise.resolve(result) };
}

const CANONICAL_BODY = {
  api_version: 'v1',
  error: {
    code: 'parent_worktrees_dirty',
    class: 'needs_action',
    title: 'Parent worktrees are dirty',
    summary: "The parent feature's worktrees have uncommitted changes.",
    remediation: { hint: 'Commit or stash the listed changes in each repository, then retry.' },
    context: {
      repositories: [{ name: 'repo-a', branch: 'main', dirty_files: ['src/one.ts', 'src/two.ts'] }],
    },
    diagnostics: 'rejected repos: repo-a',
  },
};

describe('serverRequest', () => {
  it('returns the raw body for any 2xx status', async () => {
    for (const status of [200, 201, 204, 299]) {
      const body = { api_version: 'v1', ok: status };
      await expect(
        serverRequest(transportReturning({ status, body }), '/api/v1/x', undefined),
      ).resolves.toEqual(body);
    }
  });

  it('forwards the path and init untouched to the transport', async () => {
    const seen: Array<{ path: string; init: unknown }> = [];
    const transport: ServerTransport = {
      apiRequest: (path, init) => {
        seen.push({ path, init });
        return Promise.resolve({ status: 200, body: {} });
      },
    };
    await serverRequest(transport, '/api/v1/x', undefined);
    await serverRequest(transport, '/api/v1/y', { method: 'POST', body: {} });
    expect(seen).toEqual([
      { path: '/api/v1/x', init: undefined },
      { path: '/api/v1/y', init: { method: 'POST', body: {} } },
    ]);
  });

  it('accepts a parsed multi-gate snapshot within the response-wide gate budget', async () => {
    const text = 'x'.repeat(64 * 1024);
    const questionText = 'x'.repeat(32 * 1024);
    const verification = {
      blockers: Array.from({ length: 3 }, (_, index) => ({
        item_id: `item-${index}`,
        name: text,
        command: text,
        reason: text,
        capabilities: [],
        remediation: text,
      })),
      allowed_actions: ['WAIVE', 'RETRY_AFTER_AUTH'],
    };
    const body = {
      api_version: 'v1',
      ask_user_questions: [],
      help_queue: [],
      need_user_inputs: Array.from({ length: 6 }, (_, index) => ({
        feature_id: `feature-${index}`,
        open: true,
        questions: [{ index: 1, prompt: questionText, answer: questionText }],
        ...(index < 3 ? { verification } : {}),
      })),
    };
    const bytes = new TextEncoder().encode(JSON.stringify(body)).byteLength;
    expect(bytes).toBeLessThanOrEqual(3 * 1024 * 1024);
    expect(PromptSnapshotResponseSchema.parse(body).need_user_inputs).toHaveLength(6);

    await expect(
      serverRequest(transportReturning({ status: 200, body }), '/api/v1/prompts', undefined),
    ).resolves.toBe(body);
  });

  it('carries a canonical server body through as a CanonicalErrorException, intact', async () => {
    const failure = await serverRequest(
      transportReturning({ status: 409, body: CANONICAL_BODY }),
      '/api/v1/features/feat-1/actions/refactor',
      { method: 'POST', body: {} },
    ).catch((err: unknown) => err);

    expect(failure).toBeInstanceOf(CanonicalErrorException);
    const canonical = (failure as CanonicalErrorException).canonical;
    expect(canonical).toEqual(CANONICAL_BODY.error);
    expect(canonical.class).toBe('needs_action');
    expect(canonical.title).toBe('Parent worktrees are dirty');
    expect(canonical.summary).toBe("The parent feature's worktrees have uncommitted changes.");
    expect(canonical.remediation?.hint).toBe(
      'Commit or stash the listed changes in each repository, then retry.',
    );
    expect(canonical.context?.repositories?.[0]).toEqual({
      name: 'repo-a',
      branch: 'main',
      dirty_files: ['src/one.ts', 'src/two.ts'],
    });
    expect(canonical.diagnostics).toBe('rejected repos: repo-a');
    expect((failure as Error).message).toContain('parent_worktrees_dirty');
  });
});

describe('mapServerError (canonical bodies)', () => {
  it('redacts token and home-path material in the raw diagnostics text only', () => {
    const err = mapServerError({
      status: 400,
      body: {
        api_version: 'v1',
        error: {
          code: 'bad_request',
          class: 'blocking',
          title: 'Bad request',
          summary: 'The request was not valid.',
          diagnostics: 'saw Bearer tok-9 at /Users/someone/repo',
        },
      },
    });
    expect(err).toBeInstanceOf(CanonicalErrorException);
    const canonical = (err as CanonicalErrorException).canonical;
    expect(canonical.diagnostics).not.toContain('tok-9');
    expect(canonical.diagnostics).not.toContain('/Users/someone');
    expect(canonical.diagnostics).toContain('[redacted]');
    expect(canonical.diagnostics).toContain('[path]');
  });
});

describe('mapServerError (fail-closed fallback)', () => {
  it.each([
    ['pre-canonical body', { api_version: 'v1', error: { code: 'x', message: 'm', status: 409 } }],
    [
      'unknown class',
      { api_version: 'v1', error: { code: 'x', class: 'fatal', title: 't', summary: 's' } },
    ],
    [
      'unknown extra property',
      {
        api_version: 'v1',
        error: { code: 'x', class: 'blocking', title: 't', summary: 's', extra: 1 },
      },
    ],
    ['missing error key', { api_version: 'v1' }],
    ['malformed error object', { error: { code: 42 } }],
    ['string body', 'Bearer tok-leak exploded'],
    ['null body', null],
  ])('degrades to the fixed HTTP rejection code on %s', (_label, body) => {
    const err = mapServerError({ status: 502, body });
    expect(err).toBeInstanceOf(CanonicalErrorException);
    expect((err as CanonicalErrorException).canonical).toEqual({
      code: 'E_HTTP_REJECTED',
      class: 'blocking',
      title: 'The request was rejected',
      summary: 'The runtime rejected the request (HTTP 502).',
      remediation: { hint: 'Retry; if this persists, restart the runtime and check its log.' },
    });
    expect(JSON.stringify((err as CanonicalErrorException).canonical)).not.toContain('tok-leak');
  });
});
