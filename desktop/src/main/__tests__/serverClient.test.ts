import { describe, expect, it } from 'vitest';
import { SafeErrorException } from '../../shared/errors';
import { PromptSnapshotResponseSchema } from '../../shared/api/parse';
import type { HttpResult } from '../gateway/runtimeGateway';
import { mapServerError, serverRequest, type ServerTransport } from '../serverClient';

const REMEDIES = { not_ready: 'Finish setup first.' } as const;

function transportReturning(result: HttpResult): ServerTransport {
  return { apiRequest: () => Promise.resolve(result) };
}

describe('serverRequest', () => {
  it('returns the raw body for any 2xx status', async () => {
    for (const status of [200, 201, 204, 299]) {
      const body = { api_version: 'v1', ok: status };
      await expect(
        serverRequest(transportReturning({ status, body }), '/api/v1/x', undefined, {
          remedyByCode: REMEDIES,
        }),
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
    await serverRequest(transport, '/api/v1/x', undefined, { remedyByCode: REMEDIES });
    await serverRequest(
      transport,
      '/api/v1/y',
      { method: 'POST', body: {} },
      {
        remedyByCode: REMEDIES,
      },
    );
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
      serverRequest(transportReturning({ status: 200, body }), '/api/v1/prompts', undefined, {
        remedyByCode: REMEDIES,
      }),
    ).resolves.toBe(body);
  });

  it('maps a bounded irreducible prompt snapshot error without exposing an oversized success', async () => {
    const body = {
      api_version: 'v1',
      error: {
        code: 'prompt_snapshot_too_large',
        message: 'pending prompt snapshot exceeds the safe response limit',
        status: 500,
      },
    };
    expect(new TextEncoder().encode(JSON.stringify(body)).byteLength).toBeLessThan(1024);

    const failure = await serverRequest(
      transportReturning({ status: 500, body }),
      '/api/v1/prompts',
      undefined,
      {
        remedyByCode: {
          prompt_snapshot_too_large:
            'Stop obsolete runs or resolve pending input gates from another client, then retry.',
        },
      },
    ).catch((err: unknown) => err);

    expect(failure).toBeInstanceOf(SafeErrorException);
    expect((failure as SafeErrorException).safe).toEqual({
      code: 'prompt_snapshot_too_large',
      message: 'pending prompt snapshot exceeds the safe response limit',
      remediation:
        'Stop obsolete runs or resolve pending input gates from another client, then retry.',
    });
  });

  it('throws the mapped SafeErrorException on non-2xx statuses', async () => {
    const failure = await serverRequest(
      transportReturning({
        status: 409,
        body: { api_version: 'v1', error: { code: 'not_ready', message: 'not ready' } },
      }),
      '/api/v1/x',
      undefined,
      { remedyByCode: REMEDIES },
    ).catch((err: unknown) => err);
    expect(failure).toBeInstanceOf(SafeErrorException);
    expect((failure as SafeErrorException).safe).toEqual({
      code: 'not_ready',
      message: 'not ready',
      remediation: 'Finish setup first.',
    });
  });
});

describe('mapServerError (structured bodies)', () => {
  it('omits remediation for codes without a configured remedy', () => {
    const err = mapServerError(
      { status: 400, body: { error: { code: 'unmapped_code', message: 'nope' } } },
      { remedyByCode: REMEDIES },
    );
    expect(err.safe).toEqual({ code: 'unmapped_code', message: 'nope' });
  });

  it('redacts token and home-path material in the server message', () => {
    const err = mapServerError(
      {
        status: 400,
        body: {
          error: { code: 'bad_request', message: 'saw Bearer tok-9 at /Users/someone/repo' },
        },
      },
      { remedyByCode: REMEDIES },
    );
    expect(err.safe.message).not.toContain('tok-9');
    expect(err.safe.message).not.toContain('/Users/someone');
  });
});

describe('mapServerError (fail-closed fallback)', () => {
  it.each([
    ['string body', 'Bearer tok-leak exploded'],
    ['missing error key', { api_version: 'v1' }],
    ['malformed error object', { error: { code: 42 } }],
    ['null body', null],
  ])('degrades to the generic E_HTTP error on %s', (_label, body) => {
    const err = mapServerError({ status: 502, body }, { remedyByCode: REMEDIES });
    expect(err.safe).toEqual({
      code: 'E_HTTP_502',
      message: 'The runtime rejected the request.',
      remediation: 'Retry; if this persists, restart the runtime and check its log.',
    });
    expect(JSON.stringify(err.safe)).not.toContain('tok-leak');
  });
});

describe('mapServerError (foldTargetIssues)', () => {
  const body = (issues: unknown) => ({
    api_version: 'v1',
    error: { code: 'not_ready', message: 'runtime not ready', status: 409, target: { issues } },
  });

  it('folds redacted issue messages ahead of the configured remedy', () => {
    const err = mapServerError(
      {
        status: 409,
        body: body([
          { code: 'unauthenticated', message: 'claude signed out at /Users/x/secret' },
          { code: 'models_unavailable', message: 'No models.' },
        ]),
      },
      { remedyByCode: REMEDIES, foldTargetIssues: true },
    );
    expect(err.safe.code).toBe('not_ready');
    expect(err.safe.remediation).toBe('claude signed out at [path] No models. Finish setup first.');
  });

  it('keeps the trimmed issue text when the code has no configured remedy', () => {
    const err = mapServerError(
      {
        status: 409,
        body: {
          error: {
            code: 'unmapped',
            message: 'm',
            target: { issues: [{ code: 'c', message: 'Sign in.' }] },
          },
        },
      },
      { remedyByCode: REMEDIES, foldTargetIssues: true },
    );
    expect(err.safe.remediation).toBe('Sign in.');
  });

  it('falls back to the plain remedy when the issue list is empty or absent', () => {
    for (const target of [{ issues: [] }, {}]) {
      const err = mapServerError(
        { status: 409, body: { error: { code: 'not_ready', message: 'm', target } } },
        { remedyByCode: REMEDIES, foldTargetIssues: true },
      );
      expect(err.safe.remediation).toBe('Finish setup first.');
    }
  });

  it('fails closed on a malformed target only when folding is enabled', () => {
    // Preserved per-call-site behavior: the issues-aware schema rejects a
    // malformed target (fallback), while the plain schema ignores it and
    // keeps the structured code.
    const result: HttpResult = {
      status: 409,
      body: { error: { code: 'not_ready', message: 'm', target: 'junk' } },
    };
    const folded = mapServerError(result, { remedyByCode: REMEDIES, foldTargetIssues: true });
    expect(folded.safe.code).toBe('E_HTTP_409');
    const plain = mapServerError(result, { remedyByCode: REMEDIES });
    expect(plain.safe).toEqual({
      code: 'not_ready',
      message: 'm',
      remediation: 'Finish setup first.',
    });
  });
});
