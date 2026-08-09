import { describe, expect, it } from 'vitest';
import { SUPPORTED_API_VERSION, assertCompatibleApiVersion } from './apiVersion';
import { SafeErrorException } from './errors';

describe('assertCompatibleApiVersion', () => {
  it('accepts the supported major version', () => {
    expect(() => assertCompatibleApiVersion('v1')).not.toThrow();
    expect(() => assertCompatibleApiVersion('v1.2')).not.toThrow();
  });

  it('fails closed on a different major version with an actionable error', () => {
    for (const bad of ['v2', 'v10', 'v0', '1', '', 'weird']) {
      try {
        assertCompatibleApiVersion(bad);
        throw new Error(`expected ${JSON.stringify(bad)} to be rejected`);
      } catch (err) {
        expect(err).toBeInstanceOf(SafeErrorException);
        const safe = (err as SafeErrorException).safe;
        expect(safe.code).toBe('E_API_VERSION_INCOMPATIBLE');
        expect(safe.remediation).toBeTruthy();
        expect(safe.message).toContain(SUPPORTED_API_VERSION);
      }
    }
  });
});
