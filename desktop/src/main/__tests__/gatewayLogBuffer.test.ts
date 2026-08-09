import { describe, expect, it } from 'vitest';
import { RedactedLogBuffer } from '../gateway/logBuffer';

describe('RedactedLogBuffer', () => {
  it('keeps only the newest maxLines lines', () => {
    const buffer = new RedactedLogBuffer(3);
    for (let i = 1; i <= 5; i += 1) {
      buffer.append(`line ${i}\n`);
    }
    expect(buffer.snapshot()).toEqual(['line 3', 'line 4', 'line 5']);
  });

  it('joins partial chunks into whole lines', () => {
    const buffer = new RedactedLogBuffer(10);
    buffer.append('hel');
    buffer.append('lo\nwor');
    buffer.append('ld\n');
    expect(buffer.snapshot()).toEqual(['hello', 'world']);
  });

  it('redacts bearer-like strings', () => {
    const buffer = new RedactedLogBuffer(10);
    buffer.append('Authorization: Bearer abc.def-123\n');
    const lines = buffer.snapshot();
    expect(lines.join('\n')).not.toContain('abc.def-123');
  });

  it('scrubs registered exact secrets even outside bearer syntax', () => {
    const buffer = new RedactedLogBuffer(10, ['s3cr3t-token-value']);
    buffer.append('server says token=s3cr3t-token-value ok\n');
    expect(buffer.snapshot().join('\n')).not.toContain('s3cr3t-token-value');
  });

  it('redacts secrets registered after construction', () => {
    const buffer = new RedactedLogBuffer(10);
    buffer.addSecret('late-secret');
    buffer.append('value late-secret here\n');
    expect(buffer.snapshot().join('\n')).not.toContain('late-secret');
  });

  it('retroactively scrubs retained and partial output when discovery reveals a secret', () => {
    const buffer = new RedactedLogBuffer(10);
    buffer.append('printed-before-discovery\npartial-before-discovery');
    buffer.addSecret('before-discovery');
    const snapshot = buffer.snapshot().join('\n');
    expect(snapshot).not.toContain('before-discovery');
    expect(snapshot).toContain('[redacted]');
  });

  it('includes a bounded final partial line in diagnostic snapshots', () => {
    const buffer = new RedactedLogBuffer(2);
    buffer.append('complete one\ncomplete two\nlast partial');
    expect(buffer.snapshot()).toEqual(['complete two', 'last partial']);
  });
});
