import { describe, expect, it } from 'vitest';
import { z } from 'zod';
import { HealthResponseSchema, parseServerJson } from './parse';
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
});
