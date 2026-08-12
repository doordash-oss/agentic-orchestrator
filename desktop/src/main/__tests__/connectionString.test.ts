import { describe, expect, it } from 'vitest';
import { createHash } from 'node:crypto';

import { SafeErrorException } from '../../shared/errors';
import {
  E_CONNECTION_STRING_HOST,
  E_CONNECTION_STRING_HOST_INVALID,
  E_CONNECTION_STRING_PORT,
  E_CONNECTION_STRING_PORT_RANGE,
  E_CONNECTION_STRING_SCHEME,
  E_CONNECTION_STRING_TOKEN,
  E_CONNECTION_STRING_WILDCARD,
  parseConnectionString,
  serverKeyForBaseUrl,
} from '../connectionString';

function parseError(raw: string): SafeErrorException {
  try {
    parseConnectionString(raw);
  } catch (err) {
    expect(err).toBeInstanceOf(SafeErrorException);
    return err as SafeErrorException;
  }
  throw new Error(`expected parseConnectionString(${raw}) to throw`);
}

describe('parseConnectionString round-trips (mirrors Go test cases)', () => {
  // Equivalent of Go's TestConnectionStringGenerateParseRoundTrip: the raw
  // strings below are byte-identical to what Go's GenerateConnectionString
  // emits for the same inputs (url.Values query-escapes spaces as "+").
  const cases = [
    {
      name: 'ipv4',
      raw: 'agentico://dG9rZW4td2l0aF9iNjQtdXJs@10.1.2.3:8080',
      token: 'dG9rZW4td2l0aF9iNjQtdXJs',
      baseUrl: 'http://10.1.2.3:8080',
      serverName: undefined,
    },
    {
      name: 'ipv4 named',
      raw: 'agentico://abc_DEF-1234@192.168.1.10:9090?name=frothy-macchiato',
      token: 'abc_DEF-1234',
      baseUrl: 'http://192.168.1.10:9090',
      serverName: 'frothy-macchiato',
    },
    {
      name: 'hostname',
      raw: 'agentico://tok@nas.local:443?name=home+server',
      token: 'tok',
      baseUrl: 'http://nas.local:443',
      serverName: 'home server',
    },
    {
      name: 'special name',
      raw: 'agentico://tok@10.0.0.5:7000?name=rig+%26+gamma%3A+100%25',
      token: 'tok',
      baseUrl: 'http://10.0.0.5:7000',
      serverName: 'rig & gamma: 100%',
    },
    {
      name: 'ipv6',
      raw: 'agentico://tok@[fe80::1]:8080',
      token: 'tok',
      baseUrl: 'http://[fe80::1]:8080',
      serverName: undefined,
    },
  ];

  for (const tc of cases) {
    it(`parses ${tc.name}`, () => {
      const parsed = parseConnectionString(tc.raw);
      expect(parsed.baseUrl).toBe(tc.baseUrl);
      expect(parsed.token).toBe(tc.token);
      if (tc.serverName === undefined) {
        expect(parsed.name).toBeUndefined();
      } else {
        expect(parsed.name).toBe(tc.serverName);
      }
    });
  }

  it('decodes percent-encoded spaces in names like Go', () => {
    expect(parseConnectionString('agentico://tok@10.1.2.3:8080?name=home%20server').name).toBe(
      'home server',
    );
  });

  it('omits name entirely when the parameter is absent', () => {
    const parsed = parseConnectionString('agentico://tok@10.1.2.3:8080');
    expect('name' in parsed).toBe(false);
  });

  it('ignores unknown query parameters like the Go parser', () => {
    const parsed = parseConnectionString('agentico://tok@10.1.2.3:8080?foo=bar&name=x');
    expect(parsed.name).toBe('x');
    expect(parsed.baseUrl).toBe('http://10.1.2.3:8080');
  });

  it.each([
    ['loopback ipv4', 'agentico://tok@127.0.0.1:8080', 'http://127.0.0.1:8080'],
    ['localhost', 'agentico://tok@localhost:8080', 'http://localhost:8080'],
    ['ipv6 loopback', 'agentico://tok@[::1]:8080', 'http://[::1]:8080'],
  ])('accepts %s hosts', (_label, raw, baseUrl) => {
    expect(parseConnectionString(raw).baseUrl).toBe(baseUrl);
  });
});

describe('parseConnectionString malformed matrix (distinct messages)', () => {
  const cases = [
    {
      label: 'wrong scheme',
      raw: 'http://tok@10.1.2.3:8080',
      code: E_CONNECTION_STRING_SCHEME,
      want: 'agentico://',
    },
    {
      label: 'no scheme at all',
      raw: 'tok@10.1.2.3:8080',
      code: E_CONNECTION_STRING_SCHEME,
      want: 'agentico://',
    },
    {
      label: 'missing token',
      raw: 'agentico://10.1.2.3:8080',
      code: E_CONNECTION_STRING_TOKEN,
      want: 'bearer token',
    },
    {
      label: 'empty token',
      raw: 'agentico://@10.1.2.3:8080',
      code: E_CONNECTION_STRING_TOKEN,
      want: 'bearer token',
    },
    {
      label: 'missing host',
      raw: 'agentico://tok@',
      code: E_CONNECTION_STRING_HOST,
      want: 'missing a host',
    },
    {
      label: 'empty host with port',
      raw: 'agentico://tok@:8080',
      code: E_CONNECTION_STRING_HOST,
      want: 'missing a host',
    },
    {
      label: 'wildcard v4 host',
      raw: 'agentico://tok@0.0.0.0:8080',
      code: E_CONNECTION_STRING_WILDCARD,
      want: 'wildcard',
    },
    {
      label: 'wildcard v6 host',
      raw: 'agentico://tok@[::]:8080',
      code: E_CONNECTION_STRING_WILDCARD,
      want: 'wildcard',
    },
    {
      label: 'non-dialable host chars',
      raw: 'agentico://tok@my host:8080',
      code: E_CONNECTION_STRING_HOST_INVALID,
      want: 'http://',
    },
    {
      label: 'missing port',
      raw: 'agentico://tok@10.1.2.3',
      code: E_CONNECTION_STRING_PORT,
      want: 'explicit port',
    },
    {
      label: 'empty port',
      raw: 'agentico://tok@10.1.2.3:',
      code: E_CONNECTION_STRING_PORT,
      want: 'explicit port',
    },
    {
      label: 'out-of-range port',
      raw: 'agentico://tok@10.1.2.3:99999',
      code: E_CONNECTION_STRING_PORT_RANGE,
      want: '1-65535',
    },
    {
      label: 'zero port',
      raw: 'agentico://tok@10.1.2.3:0',
      code: E_CONNECTION_STRING_PORT_RANGE,
      want: '1-65535',
    },
    {
      label: 'non-numeric port',
      raw: 'agentico://tok@10.1.2.3:abc',
      code: E_CONNECTION_STRING_PORT_RANGE,
      want: 'not a number',
    },
  ];

  for (const tc of cases) {
    it(`rejects ${tc.label} with ${tc.code}`, () => {
      const err = parseError(tc.raw);
      expect(err.safe.code).toBe(tc.code);
      expect(err.safe.message).toContain(tc.want);
      expect(err.safe.remediation).toBeTruthy();
    });
  }

  it('gives each failure mode a distinct message', () => {
    const raws = [
      'http://tok@10.1.2.3:8080',
      'agentico://10.1.2.3:8080',
      'agentico://tok@',
      'agentico://tok@0.0.0.0:8080',
      'agentico://tok@my host:8080',
      'agentico://tok@10.1.2.3',
      'agentico://tok@10.1.2.3:99999',
    ];
    const messages = new Set(raws.map((raw) => parseError(raw).safe.message));
    expect(messages.size).toBe(raws.length);
  });

  it('never echoes the raw string (which may carry the token) in the error', () => {
    const raw = 'agentico://s3cr3t-tok@0.0.0.0:8080';
    const err = parseError(raw);
    expect(err.safe.message).not.toContain('s3cr3t-tok');
    expect(err.safe.remediation ?? '').not.toContain('s3cr3t-tok');
  });
});

describe('serverKeyForBaseUrl', () => {
  it('returns the first 32 hex chars of the sha256 over the canonical base URL', () => {
    const expected = createHash('sha256').update('http://10.1.2.3:8080').digest('hex').slice(0, 32);
    expect(serverKeyForBaseUrl('http://10.1.2.3:8080')).toBe(expected);
    expect(serverKeyForBaseUrl('http://10.1.2.3:8080')).toMatch(/^[0-9a-f]{32}$/);
  });

  it('canonicalizes host casing like the Go BaseURL consumer path', () => {
    expect(serverKeyForBaseUrl('http://LOCALHOST:8080')).toBe(
      serverKeyForBaseUrl('http://localhost:8080'),
    );
  });

  it('keeps IPv6 bracket canonicalization stable', () => {
    const expected = createHash('sha256')
      .update('http://[fe80::1]:8080')
      .digest('hex')
      .slice(0, 32);
    expect(serverKeyForBaseUrl('http://[fe80::1]:8080')).toBe(expected);
  });

  it('matches the parse result round-trip: key(parse.baseUrl) hashes the baseUrl', () => {
    const parsed = parseConnectionString('agentico://tok@[fe80::1]:8080');
    expect(serverKeyForBaseUrl(parsed.baseUrl)).toBe(
      createHash('sha256').update(parsed.baseUrl).digest('hex').slice(0, 32),
    );
  });
});
