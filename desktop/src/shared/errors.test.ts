import { describe, expect, it } from 'vitest';
import { SafeErrorException, safeError, toSafeError, redactText } from './errors';

describe('safeError', () => {
  it('builds a typed safe error with code, message, and remediation', () => {
    const err = safeError('E_TEST', 'something broke', 'try again');
    expect(err).toEqual({ code: 'E_TEST', message: 'something broke', remediation: 'try again' });
  });

  it('omits remediation when not provided', () => {
    expect(safeError('E_TEST', 'msg')).toEqual({ code: 'E_TEST', message: 'msg' });
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

describe('toSafeError', () => {
  it('passes through an existing SafeErrorException payload', () => {
    const exc = new SafeErrorException(safeError('E_ORIGINAL', 'original', 'fix'));
    expect(toSafeError(exc, 'E_FALLBACK')).toEqual({
      code: 'E_ORIGINAL',
      message: 'original',
      remediation: 'fix',
    });
  });

  it('redacts messages from unknown errors and applies the fallback code', () => {
    const err = toSafeError(
      new Error('boom at /Users/somebody/app with Bearer tok123'),
      'E_FALLBACK',
    );
    expect(err.code).toBe('E_FALLBACK');
    expect(err.message).not.toContain('/Users/somebody');
    expect(err.message).not.toContain('tok123');
  });

  it('never embeds non-Error payloads (which could hold raw data) in the message', () => {
    const err = toSafeError({ secret: 'hunter2' }, 'E_FALLBACK');
    expect(JSON.stringify(err)).not.toContain('hunter2');
  });
});
