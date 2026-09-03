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
import { MAX_PAYLOAD_BYTES, assertNoPrototypePollution, assertWithinByteSize } from './sanitize';
import { SafeErrorException } from './errors';

function codeOf(fn: () => void): string {
  try {
    fn();
  } catch (err) {
    if (err instanceof SafeErrorException) return err.safe.code;
    throw err;
  }
  throw new Error('expected function to throw');
}

describe('assertNoPrototypePollution', () => {
  it('accepts plain nested data', () => {
    expect(() =>
      assertNoPrototypePollution({ a: 1, b: { c: [1, 2, { d: 'x' }] }, e: null }),
    ).not.toThrow();
  });

  it('rejects an own __proto__ key produced by JSON.parse', () => {
    const payload = JSON.parse('{"__proto__": {"polluted": true}}');
    expect(codeOf(() => assertNoPrototypePollution(payload))).toBe('E_UNSAFE_PAYLOAD');
  });

  it('rejects constructor and prototype keys nested deep in the tree', () => {
    const a = JSON.parse('{"outer": [{"inner": {"constructor": {}}}]}');
    expect(codeOf(() => assertNoPrototypePollution(a))).toBe('E_UNSAFE_PAYLOAD');
    const b = JSON.parse('{"outer": {"prototype": 1}}');
    expect(codeOf(() => assertNoPrototypePollution(b))).toBe('E_UNSAFE_PAYLOAD');
  });

  it('does not flag inherited constructor properties on normal objects', () => {
    expect(() => assertNoPrototypePollution({ safe: true })).not.toThrow();
  });

  it('does not leak offending values in the error message', () => {
    try {
      assertNoPrototypePollution(JSON.parse('{"__proto__": {"secret": "hunter2"}}'));
      throw new Error('expected throw');
    } catch (err) {
      expect(err).toBeInstanceOf(SafeErrorException);
      expect(JSON.stringify((err as SafeErrorException).safe)).not.toContain('hunter2');
    }
  });
});

describe('assertWithinByteSize', () => {
  it('accepts payloads at or below the limit', () => {
    expect(() => assertWithinByteSize('x'.repeat(1024), 1024)).not.toThrow();
  });

  it('rejects oversized payloads fail-closed with a typed error', () => {
    expect(codeOf(() => assertWithinByteSize('x'.repeat(1025), 1024))).toBe('E_PAYLOAD_TOO_LARGE');
  });

  it('counts multibyte characters by encoded byte length', () => {
    // '€' is 3 bytes in UTF-8.
    expect(codeOf(() => assertWithinByteSize('€€', 5))).toBe('E_PAYLOAD_TOO_LARGE');
    expect(() => assertWithinByteSize('€', 5)).not.toThrow();
  });

  it('exports a 5 MiB default budget', () => {
    expect(MAX_PAYLOAD_BYTES).toBe(5 * 1024 * 1024);
  });
});
