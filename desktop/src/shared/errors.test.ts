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
import {
  ERROR_CATALOG,
  buildCanonicalError,
  CanonicalErrorException,
  toCanonicalError,
  redactText,
  redactedCanonicalError,
  isRequestTimeout,
  isAbortError,
  requestTimeoutError,
  requiresLocalServerError,
  E_REQUEST_TIMEOUT,
  type CatalogCode,
  type CatalogSpec,
} from './errors';

const VALID_CLASSES = new Set(['blocking', 'needs_action', 'warning']);

/** Builds an entry's summary with placeholder values for its typed parameters. */
function summaryOf(spec: (typeof ERROR_CATALOG)[CatalogCode]): string {
  const params = new Proxy({} as Record<string, string>, {
    get: (_target, key) => (typeof key === 'string' ? 'param' : undefined),
  });
  return (spec as CatalogSpec<Record<string, string>>).summary(params);
}

describe('ERROR_CATALOG', () => {
  it('keys every code with the E_UPPER_SNAKE desktop-local marker', () => {
    for (const code of Object.keys(ERROR_CATALOG)) {
      expect(code).toMatch(/^E_[A-Z0-9_]+$/);
      // Server catalog codes are lowercase snake_case, so the families stay
      // disjoint by construction.
      expect(code).not.toMatch(/[a-z]/);
    }
  });

  it('gives every entry a valid class and nonempty title and summary', () => {
    for (const spec of Object.values(ERROR_CATALOG)) {
      expect(VALID_CLASSES.has(spec.class)).toBe(true);
      expect(spec.title.length).toBeGreaterThan(0);
      expect(summaryOf(spec).length).toBeGreaterThan(0);
    }
  });

  it('keeps the connection-string codes pairwise distinct in title and summary', () => {
    const codes = [
      'E_CONNECTION_STRING_SCHEME',
      'E_CONNECTION_STRING_TOKEN',
      'E_CONNECTION_STRING_HOST',
      'E_CONNECTION_STRING_HOST_INVALID',
      'E_CONNECTION_STRING_WILDCARD',
      'E_CONNECTION_STRING_PORT',
      'E_CONNECTION_STRING_PORT_RANGE',
    ] as const;
    const titles = new Set<string>();
    const summaries = new Set<string>();
    for (const code of codes) {
      const spec = ERROR_CATALOG[code];
      titles.add(spec.title);
      summaries.add(summaryOf(spec));
    }
    expect(titles.size).toBe(codes.length);
    expect(summaries.size).toBe(codes.length);
  });
});

describe('buildCanonicalError', () => {
  it('builds the catalog-authored canonical shape', () => {
    const err = buildCanonicalError('E_BAD_API_PATH');
    expect(err).toEqual({
      code: 'E_BAD_API_PATH',
      class: 'blocking',
      title: 'Disallowed API path',
      summary: 'The requested API path is not allowed.',
    });
  });

  it('interpolates typed parameters into the authored summary', () => {
    const err = buildCanonicalError('E_CONNECTION_STRING_SCHEME', {
      params: { got: 'https' },
    });
    expect(err.code).toBe('E_CONNECTION_STRING_SCHEME');
    expect(err.class).toBe('needs_action');
    expect(err.title).toBe('The connection string could not be parsed');
    expect(err.summary).toBe('Connection string must use the agentico:// scheme, got https.');
    expect(err.remediation?.hint).toBeTruthy();
  });

  it('keeps the internal fallback summary catalog-authored', () => {
    const err = buildCanonicalError('E_INTERNAL', {
      params: { reason: 'boom at /Users/somebody/app with Bearer tok123' },
    });
    expect(err.summary).toBe('The request failed unexpectedly.');
  });

  it('redacts free-form diagnostics', () => {
    const err = buildCanonicalError('E_LAUNCH_FAILED', {
      params: { reason: 'spawn failed' },
      diagnostics: 'read /Users/somebody/secret.txt then Bearer tok123',
    });
    expect(err.diagnostics).toContain('[path]');
    expect(err.diagnostics).toContain('[redacted]');
    expect(err.diagnostics).not.toContain('/Users/somebody');
    expect(err.diagnostics).not.toContain('tok123');
  });

  it('redacts a remediation-hint override', () => {
    const err = buildCanonicalError('E_INTERNAL', {
      params: { reason: 'the request failed' },
      remediationHint: 're-paste from /Users/somebody/notes with Bearer tok123',
    });
    expect(err.remediation?.hint).not.toContain('/Users/somebody');
    expect(err.remediation?.hint).not.toContain('tok123');
  });
});

describe('toCanonicalError', () => {
  it('passes an existing CanonicalErrorException through untouched', () => {
    const canonical = buildCanonicalError('E_INTERNAL', {
      params: { reason: 'original' },
    });
    const exc = new CanonicalErrorException(canonical);
    expect(toCanonicalError(exc, 'E_GATEWAY')).toBe(canonical);
  });

  it('keeps an unknown Error message out of the fallback summary and redacts its diagnostics', () => {
    const err = toCanonicalError(
      new Error('boom at /Users/somebody/app with Bearer tok123'),
      'E_INTERNAL',
    );
    expect(err.code).toBe('E_INTERNAL');
    expect(err.class).toBe('blocking');
    expect(err.title).toBe('Request failed');
    expect(err.summary).toBe('The request failed unexpectedly.');
    expect(err.diagnostics).toContain('[path]');
    expect(err.diagnostics).toContain('[redacted]');
    expect(err.diagnostics).not.toContain('/Users/somebody');
    expect(err.diagnostics).not.toContain('tok123');
  });

  it('never embeds non-Error payloads (which could hold raw data) in the summary', () => {
    const err = toCanonicalError({ secret: 'hunter2' }, 'E_INTERNAL');
    expect(err.code).toBe('E_INTERNAL');
    expect(JSON.stringify(err)).not.toContain('hunter2');
    expect(err.summary).toBe('The request failed unexpectedly.');
    expect(err.diagnostics).toBeUndefined();
  });

  it('maps a fetch abort to the typed timeout code instead of its raw DOM message', () => {
    const abort = new Error('This operation was aborted');
    abort.name = 'AbortError';
    const err = toCanonicalError(abort, 'E_INTERNAL');
    expect(err.code).toBe(E_REQUEST_TIMEOUT);
    expect(err.class).toBe('warning');
    expect(err.summary).not.toContain('aborted');
    expect(err.remediation?.hint).toBeTruthy();
  });
});

describe('isRequestTimeout', () => {
  it('recognizes only the typed timeout exception', () => {
    expect(isRequestTimeout(new CanonicalErrorException(requestTimeoutError()))).toBe(true);
    expect(isRequestTimeout(new CanonicalErrorException(requiresLocalServerError()))).toBe(false);
    expect(isRequestTimeout(new Error('boom'))).toBe(false);
  });
});

describe('isAbortError', () => {
  it('recognizes abort and timeout DOM error names', () => {
    const abort = new Error('This operation was aborted');
    abort.name = 'AbortError';
    expect(isAbortError(abort)).toBe(true);
    const timeout = new Error('Timed out');
    timeout.name = 'TimeoutError';
    expect(isAbortError(timeout)).toBe(true);
    expect(isAbortError(new Error('boom'))).toBe(false);
    expect(isAbortError('nope')).toBe(false);
  });
});

describe('redactText', () => {
  it('redacts bearer tokens', () => {
    const out = redactText('failed: Authorization: Bearer abc.def-123 rejected');
    expect(out).not.toContain('abc.def-123');
    expect(out).toContain('[redacted]');
  });

  it('redacts absolute user paths on macOS and Linux', () => {
    const out = redactText('read /Users/somebody/secret.txt and /home/somebody/.config/x');
    expect(out).not.toContain('/Users/somebody');
    expect(out).not.toContain('/home/somebody');
    expect(out).toContain('[path]');
  });

  it('redacts token-like query parameters', () => {
    const out = redactText('GET http://127.0.0.1:9999/api?token=supersecretvalue1234 failed');
    expect(out).not.toContain('supersecretvalue1234');
  });
});

describe('redactedCanonicalError', () => {
  it('redacts diagnostics while leaving the catalog-authored fields intact', () => {
    const err = redactedCanonicalError({
      ...buildCanonicalError('E_INTERNAL', { params: { reason: 'boom' } }),
      diagnostics: 'path /Users/somebody/secret.txt and Bearer tok123',
    });
    expect(err.diagnostics).toContain('[path]');
    expect(err.diagnostics).toContain('[redacted]');
    expect(err.summary).toBe('The request failed unexpectedly.');
  });

  it('leaves an error without diagnostics untouched', () => {
    const err = buildCanonicalError('E_NOT_CONNECTED');
    expect(redactedCanonicalError(err)).toEqual(err);
  });
});
