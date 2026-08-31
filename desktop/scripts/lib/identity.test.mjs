// Unit tests for the build-identity schema helpers shared by
// prepare-server.mjs (generation) and verify-package.mjs (validation).
// The rejection paths here are the cheap stand-in for rebuilding tampered
// packages: verify-package.mjs routes every identity decision through these
// functions, so a tampered/missing/mismatched identity fails exactly where
// these tests prove it fails.
import { describe, expect, it } from 'vitest';

import {
  IDENTITY_FIELDS,
  createBuildIdentity,
  validateBuildIdentity,
  crossCheckServerBinary,
  parseOpenApiInfoVersion,
  parseCompatibilitySchemaVersion,
  parseAgenticoVersionOutput,
  parseGoBuildInfo,
} from './identity.mjs';

const complete = {
  desktop_version: '0.1.0',
  api_version: 'v1',
  schema_version: 1,
  server_version: 'v0.148.0-30-g981fa32',
  server_revision: '981fa32aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
  os: 'darwin',
  arch: 'universal',
  built_at: '2026-07-14T21:59:50-07:00',
};

describe('createBuildIdentity', () => {
  it('returns a frozen identity when every field is present', () => {
    const identity = createBuildIdentity(complete);
    expect(identity).toEqual(complete);
    expect(Object.isFrozen(identity)).toBe(true);
  });

  it.each(IDENTITY_FIELDS)('throws when %s is missing', (field) => {
    const partial = { ...complete };
    delete partial[field];
    expect(() => createBuildIdentity(partial)).toThrow(field);
  });

  it('throws on empty-string fields instead of emitting hollow identity', () => {
    expect(() => createBuildIdentity({ ...complete, server_revision: '' })).toThrow(
      'server_revision',
    );
  });
});

describe('validateBuildIdentity', () => {
  it('accepts a complete darwin universal identity', () => {
    expect(validateBuildIdentity(complete)).toEqual({ ok: true, errors: [] });
  });

  it('accepts linux x64 and arm64 identities', () => {
    for (const arch of ['x64', 'arm64']) {
      const result = validateBuildIdentity({ ...complete, os: 'linux', arch });
      expect(result.ok).toBe(true);
    }
  });

  it('rejects non-object payloads (missing/corrupt build-identity.json)', () => {
    for (const value of [null, undefined, 'x', 42, []]) {
      const result = validateBuildIdentity(value);
      expect(result.ok).toBe(false);
      expect(result.errors.length).toBeGreaterThan(0);
    }
  });

  it.each(IDENTITY_FIELDS)('rejects an identity missing %s', (field) => {
    const tampered = { ...complete };
    delete tampered[field];
    const result = validateBuildIdentity(tampered);
    expect(result.ok).toBe(false);
    expect(result.errors.join('\n')).toContain(field);
  });

  it('rejects empty and whitespace-only string fields', () => {
    for (const value of ['', '   ']) {
      const result = validateBuildIdentity({ ...complete, server_version: value });
      expect(result.ok).toBe(false);
      expect(result.errors.join('\n')).toContain('server_version');
    }
  });

  it('rejects a non-positive-integer schema_version', () => {
    for (const value of [0, -1, 1.5, '1']) {
      const result = validateBuildIdentity({ ...complete, schema_version: value });
      expect(result.ok).toBe(false);
      expect(result.errors.join('\n')).toContain('schema_version');
    }
  });

  it('rejects unsupported os/arch values', () => {
    expect(validateBuildIdentity({ ...complete, os: 'win32' }).ok).toBe(false);
    expect(validateBuildIdentity({ ...complete, arch: 'mips' }).ok).toBe(false);
    // darwin packages are universal-only; linux never is.
    expect(validateBuildIdentity({ ...complete, os: 'darwin', arch: 'x64' }).ok).toBe(false);
    expect(validateBuildIdentity({ ...complete, os: 'linux', arch: 'universal' }).ok).toBe(false);
  });

  it('rejects an unparseable built_at timestamp', () => {
    const result = validateBuildIdentity({ ...complete, built_at: 'yesterday-ish' });
    expect(result.ok).toBe(false);
    expect(result.errors.join('\n')).toContain('built_at');
  });

  it('rejects unexpected extra fields so the schema stays closed', () => {
    const result = validateBuildIdentity({ ...complete, signed: true });
    expect(result.ok).toBe(false);
    expect(result.errors.join('\n')).toContain('signed');
  });
});

describe('parseAgenticoVersionOutput', () => {
  it('extracts version and revision from `agentico --version` output', () => {
    expect(
      parseAgenticoVersionOutput(
        'agentico vv0.148.0-30-g981fa32 (revision 981fa3290a2f2991f13ebd1d6c6f374f2a30bffe)\n',
      ),
    ).toEqual({
      version: 'v0.148.0-30-g981fa32',
      revision: '981fa3290a2f2991f13ebd1d6c6f374f2a30bffe',
    });
  });

  it('reports a null revision for unstamped builds', () => {
    expect(parseAgenticoVersionOutput('agentico v1.2.3')).toEqual({
      version: '1.2.3',
      revision: null,
    });
  });

  it('returns null for unrecognized output', () => {
    expect(parseAgenticoVersionOutput('')).toBeNull();
    expect(parseAgenticoVersionOutput('some other tool v1')).toBeNull();
  });
});

describe('parseGoBuildInfo', () => {
  const goVersionM = [
    'bin/agentico: go1.24.1',
    '\tpath\tgithub.com/doordash-oss/agentic-orchestrator/cmd/agentico',
    '\tmod\tgithub.com/doordash-oss/agentic-orchestrator\t(devel)',
    '\tbuild\tGOOS=darwin',
    '\tbuild\tGOARCH=arm64',
    '\tbuild\tvcs.revision=981fa32aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
    '\tbuild\tvcs.modified=false',
  ].join('\n');

  it('extracts revision, GOOS and GOARCH from `go version -m` output', () => {
    expect(parseGoBuildInfo(goVersionM)).toEqual({
      revision: '981fa32aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',
      goos: 'darwin',
      goarch: 'arm64',
    });
  });

  it('returns nulls for output without VCS stamping', () => {
    expect(parseGoBuildInfo('bin/agentico: go1.24.1')).toEqual({
      revision: null,
      goos: null,
      goarch: null,
    });
  });
});

describe('crossCheckServerBinary', () => {
  const identity = createBuildIdentity(complete);
  const probe = {
    reportedVersion: complete.server_version,
    reportedRevision: complete.server_revision,
    reportedGoos: 'darwin',
    reportedGoarch: 'arm64',
  };

  it('passes when the binary matches the identity file', () => {
    expect(crossCheckServerBinary(identity, probe)).toEqual([]);
  });

  it('fails on a server version mismatch', () => {
    const errors = crossCheckServerBinary(identity, {
      ...probe,
      reportedVersion: 'v0.0.1',
    });
    expect(errors.join('\n')).toContain('server_version');
  });

  it('fails on a revision mismatch or an unstamped binary', () => {
    expect(
      crossCheckServerBinary(identity, { ...probe, reportedRevision: 'deadbeef' }).join('\n'),
    ).toContain('server_revision');
    expect(
      crossCheckServerBinary(identity, { ...probe, reportedRevision: null }).join('\n'),
    ).toContain('server_revision');
  });

  it('fails when the binary was built for a different OS', () => {
    const errors = crossCheckServerBinary(identity, { ...probe, reportedGoos: 'linux' });
    expect(errors.join('\n')).toContain('GOOS');
  });

  it('fails when GOARCH is absent or does not match the package target', () => {
    const linuxIdentity = createBuildIdentity({ ...complete, os: 'linux', arch: 'arm64' });
    const linuxProbe = { ...probe, reportedGoos: 'linux', reportedGoarch: 'amd64' };
    expect(crossCheckServerBinary(linuxIdentity, linuxProbe).join('\n')).toContain(
      'GOARCH mismatch: package target linux/arm64 requires arm64, binary built for amd64',
    );
    expect(
      crossCheckServerBinary(linuxIdentity, { ...linuxProbe, reportedGoarch: null }).join('\n'),
    ).toContain(
      'GOARCH mismatch: package target linux/arm64 requires arm64, binary built for (unknown)',
    );
  });
});

describe('parseOpenApiInfoVersion', () => {
  it('extracts info.version from the OpenAPI spec', () => {
    const yaml = [
      'openapi: 3.1.0',
      'info:',
      '  title: Agentico Server API',
      '  version: v1',
      '  description: Loopback REST and SSE API.',
      'paths: {}',
    ].join('\n');
    expect(parseOpenApiInfoVersion(yaml)).toBe('v1');
  });

  it('ignores version keys outside the info block and quotes the value', () => {
    const yaml = [
      'openapi: 3.1.0',
      'info:',
      '  title: X',
      "  version: 'v2'",
      'components:',
      '  schemas:',
      '    Thing:',
      '      properties:',
      '        version:',
      '          type: string',
    ].join('\n');
    expect(parseOpenApiInfoVersion(yaml)).toBe('v2');
  });

  it('throws when info.version cannot be found', () => {
    expect(() => parseOpenApiInfoVersion('openapi: 3.1.0\npaths: {}\n')).toThrow('info.version');
  });
});

describe('parseCompatibilitySchemaVersion', () => {
  it('extracts CompatibilitySchemaVersion from the Go source', () => {
    const src = [
      'const (',
      '\t// CompatibilitySchemaVersion is the series number.',
      '\tCompatibilitySchemaVersion = 7',
      ')',
    ].join('\n');
    expect(parseCompatibilitySchemaVersion(src)).toBe(7);
  });

  it('throws when the constant is absent', () => {
    expect(() => parseCompatibilitySchemaVersion('package server')).toThrow(
      'CompatibilitySchemaVersion',
    );
  });
});
