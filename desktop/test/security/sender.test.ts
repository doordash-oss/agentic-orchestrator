import { describe, expect, it } from 'vitest';
import { isTrustedSender, type TrustedSender } from '../../src/main/security';

const trusted: TrustedSender = {
  webContentsId: 7,
  allowedOrigins: new Set(['http://localhost:5173', 'file://']),
};

function event(senderId: number, frameUrl: string | null) {
  return {
    sender: { id: senderId },
    senderFrame: frameUrl === null ? null : { url: frameUrl },
  };
}

describe('isTrustedSender', () => {
  it('accepts the main window webContents with an app-origin frame', () => {
    expect(isTrustedSender(event(7, 'http://localhost:5173/index.html'), trusted)).toBe(true);
    expect(isTrustedSender(event(7, 'file:///app/out/renderer/index.html'), trusted)).toBe(true);
  });

  it('rejects a different webContents id', () => {
    expect(isTrustedSender(event(8, 'http://localhost:5173/index.html'), trusted)).toBe(false);
  });

  it('rejects frames on foreign origins', () => {
    expect(isTrustedSender(event(7, 'https://evil.example.com/'), trusted)).toBe(false);
    expect(isTrustedSender(event(7, 'http://localhost:9999/'), trusted)).toBe(false);
  });

  it('rejects a missing or destroyed sender frame', () => {
    expect(isTrustedSender(event(7, null), trusted)).toBe(false);
  });

  it('rejects unparsable frame URLs', () => {
    expect(isTrustedSender(event(7, 'not a url'), trusted)).toBe(false);
  });
});
