import { describe, expect, it, vi } from 'vitest';
import { isTrustedSender, type TrustedSender } from '../../src/main/security';
import { WindowRegistry } from '../../src/main/windowRegistry';

const ALLOWED_ORIGINS = new Set(['http://localhost:5173', 'file://', 'agentico-app://bundle']);

const trusted: TrustedSender = {
  webContentsIds: new Set([7]),
  allowedOrigins: ALLOWED_ORIGINS,
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
    expect(isTrustedSender(event(7, 'agentico-app://bundle/index.html'), trusted)).toBe(true);
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

describe('isTrustedSender across multiple registered windows', () => {
  it('accepts senders from every registered window and rejects ids outside the set', () => {
    const multiWindow: TrustedSender = {
      webContentsIds: new Set([7, 12]),
      allowedOrigins: ALLOWED_ORIGINS,
    };
    expect(isTrustedSender(event(7, 'file:///app/out/renderer/index.html'), multiWindow)).toBe(
      true,
    );
    expect(isTrustedSender(event(12, 'file:///app/out/renderer/index.html'), multiWindow)).toBe(
      true,
    );
    expect(isTrustedSender(event(13, 'file:///app/out/renderer/index.html'), multiWindow)).toBe(
      false,
    );
  });
});

/**
 * The trust set is owned by the window registry and shared by reference with
 * the `TrustedSender`, so these cases drive the real registry rather than
 * hand-mutating a Set — the join-on-open and evict-on-close paths are what
 * decide whether a sender is still trusted.
 */
describe('isTrustedSender against a live WindowRegistry trust set', () => {
  interface FakeWindow {
    readonly id: number;
  }

  function makeRegistry() {
    const ids = new Set<number>();
    let nextId = 100;
    const registry = new WindowRegistry<FakeWindow>(
      {
        create: () => ({ id: (nextId += 1) }),
        focus: vi.fn(),
        webContentsId: (window) => window.id,
      },
      ids,
    );
    const sender: TrustedSender = { webContentsIds: ids, allowedOrigins: ALLOWED_ORIGINS };
    return { registry, sender };
  }

  const frame = 'file:///app/out/renderer/index.html';

  it('trusts senders from both the main and settings windows once they register', () => {
    const { registry, sender } = makeRegistry();
    const main = registry.openOrFocus('main');
    const settings = registry.openOrFocus('settings');

    expect(isTrustedSender(event(main.id, frame), sender)).toBe(true);
    expect(isTrustedSender(event(settings.id, frame), sender)).toBe(true);
  });

  it('rejects a sender whose id was never registered', () => {
    const { registry, sender } = makeRegistry();
    const main = registry.openOrFocus('main');

    expect(isTrustedSender(event(main.id + 1, frame), sender)).toBe(false);
  });

  it('stops trusting a closed window while the surviving window stays trusted', () => {
    const { registry, sender } = makeRegistry();
    const main = registry.openOrFocus('main');
    const settings = registry.openOrFocus('settings');

    registry.evict(settings);

    expect(isTrustedSender(event(settings.id, frame), sender)).toBe(false);
    expect(isTrustedSender(event(main.id, frame), sender)).toBe(true);
  });

  it('still rejects a registered window on a foreign origin or with no sender frame', () => {
    const { registry, sender } = makeRegistry();
    const settings = registry.openOrFocus('settings');

    expect(isTrustedSender(event(settings.id, 'https://evil.example.com/'), sender)).toBe(false);
    expect(isTrustedSender(event(settings.id, null), sender)).toBe(false);
  });
});
