import { describe, expect, it } from 'vitest';
import {
  DESKTOP_SCHEMA_VERSION,
  SUPPORTED_RUNTIME_POLICIES,
  SUPPORTED_SERVER_SCHEMA_VERSIONS,
  evaluateCompatibility,
} from '../gateway/compatibility';

function declaration(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    api_version: 'v1',
    schema_version: 1,
    min_client_schema: 1,
    runtime_policy: 'loopback-bearer-v1',
    server_build: { version: 'v9.9.9-other', revision: 'abc123' },
    ...overrides,
  };
}

describe('evaluateCompatibility', () => {
  it('accepts an explicitly compatible declaration even when builds differ', () => {
    const verdict = evaluateCompatibility(declaration());
    expect(verdict.compatible).toBe(true);
    if (verdict.compatible) {
      // Both build identities stay visible: the server's here, the desktop's
      // via the app's own build metadata.
      expect(verdict.serverBuild).toEqual({ version: 'v9.9.9-other', revision: 'abc123' });
    }
  });

  it('fails closed when the server declares no compatibility contract', () => {
    for (const missing of [undefined, null]) {
      const verdict = evaluateCompatibility(missing);
      expect(verdict.compatible).toBe(false);
      if (!verdict.compatible) {
        expect(verdict.reason).toMatch(/declare/i);
      }
    }
  });

  it('fails closed on an unparseable declaration', () => {
    const verdict = evaluateCompatibility({ api_version: 'v1' });
    expect(verdict.compatible).toBe(false);
  });

  it('a shared API major alone is insufficient: schema series must be supported', () => {
    const verdict = evaluateCompatibility(declaration({ schema_version: 999 }));
    expect(verdict.compatible).toBe(false);
    if (!verdict.compatible) {
      expect(verdict.reason).toMatch(/schema/i);
    }
  });

  it('rejects an unsupported API version', () => {
    const verdict = evaluateCompatibility(declaration({ api_version: 'v2' }));
    expect(verdict.compatible).toBe(false);
  });

  it('rejects when the server demands a newer client schema than this app implements', () => {
    const verdict = evaluateCompatibility(
      declaration({ min_client_schema: DESKTOP_SCHEMA_VERSION + 1 }),
    );
    expect(verdict.compatible).toBe(false);
    if (!verdict.compatible) {
      expect(verdict.reason).toMatch(/desktop/i);
    }
  });

  it('rejects an undeclared or foreign runtime policy', () => {
    for (const policy of ['', 'multi-tenant-v1', 'loopback-bearer-v2']) {
      const verdict = evaluateCompatibility(declaration({ runtime_policy: policy }));
      expect(verdict.compatible).toBe(false);
      if (!verdict.compatible) {
        expect(verdict.reason).toMatch(/polic/i);
      }
    }
  });

  it('never gates on informational fields: a server display name changes no outcome', () => {
    // The name lives at the health-payload top level and is threaded by the
    // gateway; any copy stray into the declaration itself is dropped, never
    // judged — compatible stays compatible, incompatible stays incompatible.
    const named = evaluateCompatibility(declaration({ name: 'frothy-macchiato' }));
    expect(named.compatible).toBe(true);
    expect(evaluateCompatibility(declaration({ name: 0xdeadbeef })).compatible).toBe(true);
    expect(
      evaluateCompatibility(declaration({ schema_version: 999, name: 'frothy-macchiato' }))
        .compatible,
    ).toBe(false);
  });

  it('pins the desktop support tables so widening is a conscious change', () => {
    expect(DESKTOP_SCHEMA_VERSION).toBe(1);
    expect(SUPPORTED_SERVER_SCHEMA_VERSIONS).toEqual([1]);
    expect(SUPPORTED_RUNTIME_POLICIES).toEqual(['loopback-bearer-v1']);
  });
});
