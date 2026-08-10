import { describe, expect, it } from 'vitest';
import { parseIpcError } from './ipcError';

describe('parseIpcError', () => {
  it('preserves preload metadata apart from the display message', () => {
    const remediation = 'Review and reconcile the branch on GitHub, then refresh and retry.';
    const error = Object.assign(
      new Error(`publish_remote_diverged: safe display message ${remediation}`),
      { code: 'publish_remote_diverged', remediation },
    );

    expect(parseIpcError(error)).toEqual({
      code: 'publish_remote_diverged',
      message: 'safe display message',
      remediation,
    });
  });
});
