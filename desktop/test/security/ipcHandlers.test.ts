import { describe, expect, it, vi } from 'vitest';
import { registerIpcHandlers, type IpcServices } from '../../src/main/ipcHandlers';
import type { TrustedSender } from '../../src/main/security';
import { IPC_CHANNELS, defaultSettings } from '../../src/shared/ipc';

const trusted: TrustedSender = {
  webContentsId: 1,
  allowedOrigins: new Set(['file://']),
};

const goodEvent = {
  sender: { id: 1 },
  senderFrame: { url: 'file:///app/out/renderer/index.html' },
};
const foreignEvent = {
  sender: { id: 99 },
  senderFrame: { url: 'https://evil.example.com/' },
};

function makeServices(): IpcServices {
  return {
    getConnectionStatus: vi.fn(() => ({
      status: 'awaiting-gateway' as const,
      stage: 'resolve-runtime' as const,
      detail: 'Runtime gateway not yet available.',
    })),
    getSettings: vi.fn(() => defaultSettings()),
    updateSettings: vi.fn((patch) => ({ ...defaultSettings(), ...patch })),
    getTheme: vi.fn(() => ({ preference: 'system' as const, resolved: 'dark' as const })),
    setTheme: vi.fn((preference) => ({ preference, resolved: 'light' as const })),
  };
}

function register(services = makeServices()) {
  const handlers = new Map<string, (event: unknown, ...args: unknown[]) => Promise<unknown>>();
  const ipcMain = {
    handle: vi.fn((channel: string, listener: (typeof handlers extends Map<string, infer H> ? H : never)) => {
      handlers.set(channel, listener);
    }),
  };
  registerIpcHandlers(ipcMain, trusted, services);
  return { handlers, services };
}

describe('registerIpcHandlers', () => {
  it('registers exactly the channels in the registry — no generic passthrough', () => {
    const { handlers } = register();
    expect([...handlers.keys()].sort()).toEqual(Object.values(IPC_CHANNELS).sort());
  });

  it('rejects untrusted senders without invoking the service', async () => {
    const { handlers, services } = register();
    const result = (await handlers.get(IPC_CHANNELS.settingsGet)!(foreignEvent)) as {
      ok: boolean;
      error?: { code: string };
    };
    expect(result.ok).toBe(false);
    expect(result.error?.code).toBe('E_UNTRUSTED_SENDER');
    expect(services.getSettings).not.toHaveBeenCalled();
  });

  it('returns a validated ok envelope for a trusted, valid request', async () => {
    const { handlers } = register();
    const result = (await handlers.get(IPC_CHANNELS.connectionGetStatus)!(goodEvent)) as {
      ok: boolean;
      value: { status: string };
    };
    expect(result.ok).toBe(true);
    expect(result.value.status).toBe('awaiting-gateway');
  });

  it('fails closed on schema-invalid payloads without invoking the service', async () => {
    const { handlers, services } = register();
    const result = (await handlers.get(IPC_CHANNELS.settingsUpdate)!(goodEvent, {
      theme: 'neon',
    })) as { ok: boolean; error?: { code: string } };
    expect(result.ok).toBe(false);
    expect(result.error?.code).toBe('E_SCHEMA_MISMATCH');
    expect(services.updateSettings).not.toHaveBeenCalled();
  });

  it('fails closed on prototype-polluting payloads', async () => {
    const { handlers, services } = register();
    const payload = JSON.parse('{"theme": "dark", "__proto__": {"polluted": true}}');
    const result = (await handlers.get(IPC_CHANNELS.settingsUpdate)!(goodEvent, payload)) as {
      ok: boolean;
      error?: { code: string };
    };
    expect(result.ok).toBe(false);
    expect(result.error?.code).toBe('E_UNSAFE_PAYLOAD');
    expect(services.updateSettings).not.toHaveBeenCalled();
  });

  it('fails closed on oversized payloads', async () => {
    const { handlers, services } = register();
    const result = (await handlers.get(IPC_CHANNELS.settingsUpdate)!(goodEvent, {
      runtime: { selection: 'x'.repeat(6 * 1024 * 1024) },
    })) as { ok: boolean; error?: { code: string } };
    expect(result.ok).toBe(false);
    expect(result.error?.code).toBe('E_PAYLOAD_TOO_LARGE');
    expect(services.updateSettings).not.toHaveBeenCalled();
  });

  it('fails closed when the service returns a response violating its schema', async () => {
    const services = makeServices();
    services.getTheme = vi.fn(() => ({ preference: 'system', resolved: 'sepia' }) as never);
    const { handlers } = register(services);
    const result = (await handlers.get(IPC_CHANNELS.themeGet)!(goodEvent)) as {
      ok: boolean;
      error?: { code: string };
    };
    expect(result.ok).toBe(false);
    expect(result.error?.code).toBe('E_SCHEMA_MISMATCH');
  });

  it('converts service exceptions into redacted safe errors', async () => {
    const services = makeServices();
    services.getSettings = vi.fn(() => {
      throw new Error('exploded at /Users/somebody/secret with Bearer tok123');
    });
    const { handlers } = register(services);
    const result = (await handlers.get(IPC_CHANNELS.settingsGet)!(goodEvent)) as {
      ok: boolean;
      error?: { code: string; message: string };
    };
    expect(result.ok).toBe(false);
    expect(result.error?.code).toBe('E_INTERNAL');
    expect(result.error?.message).not.toContain('/Users/somebody');
    expect(result.error?.message).not.toContain('tok123');
  });
});
