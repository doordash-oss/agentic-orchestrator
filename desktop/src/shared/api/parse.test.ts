import { describe, expect, it } from 'vitest';
import { z } from 'zod';
import { HealthResponseSchema, parseServerJson, PromptSnapshotResponseSchema } from './parse';
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
});
