import { describe, expect, it } from 'vitest';
import {
  ConnectionStateSchema,
  IPC_CHANNELS,
  IPC_EVENTS,
  IpcEnvelopeSchema,
  SettingsPatchSchema,
  SettingsSchema,
  defaultSettings,
  ipcContracts,
} from './ipc';

describe('IPC channel registry', () => {
  it('defines a zod request/response contract for every invokable channel', () => {
    for (const channel of Object.values(IPC_CHANNELS)) {
      const contract = ipcContracts[channel];
      expect(contract, `missing contract for ${channel}`).toBeDefined();
      expect(contract.request).toBeDefined();
      expect(contract.response).toBeDefined();
    }
  });

  it('namespaces all channels to avoid collisions with generic channels', () => {
    for (const channel of [...Object.values(IPC_CHANNELS), ...Object.values(IPC_EVENTS)]) {
      expect(channel.startsWith('agentico:')).toBe(true);
    }
  });
});

describe('ConnectionStateSchema', () => {
  it('accepts gateway lifecycle states with ownership and server build', () => {
    const state = {
      status: 'ready',
      stage: 'ready',
      detail: 'Connected to an externally managed Agentico runtime.',
      ownership: 'external',
      serverBuild: { version: 'v1.2.3', revision: 'abc' },
    };
    expect(ConnectionStateSchema.parse(state)).toEqual(state);
  });

  it('accepts terminal error states with redacted diagnostics', () => {
    const state = {
      status: 'incompatible',
      stage: 'connect',
      detail: 'A running Agentico runtime is not compatible with this app.',
      ownership: 'external',
      error: { code: 'E_INCOMPATIBLE_SERVER', message: 'nope', remediation: 'update' },
    };
    expect(ConnectionStateSchema.parse(state)).toEqual(state);
  });

  it('rejects unknown statuses and extra fields fail-closed', () => {
    expect(
      ConnectionStateSchema.safeParse({
        status: 'nope',
        stage: 'discover',
        detail: '',
        ownership: 'none',
      }).success,
    ).toBe(false);
    expect(
      ConnectionStateSchema.safeParse({ status: 'idle', stage: 'discover', detail: '' }).success,
    ).toBe(false); // ownership is required
  });

  it('rejects token-shaped fields anywhere in the state', () => {
    const base = { status: 'ready', stage: 'ready', detail: '', ownership: 'app-owned' };
    for (const extra of [
      { bearerToken: 'x' },
      { authToken: 'x' },
      { token: 'x' },
      { auth_token: 'x' },
      { baseUrl: 'http://127.0.0.1:1' },
      { serverBuild: { version: 'v1', token: 'x' } },
    ]) {
      expect(ConnectionStateSchema.safeParse({ ...base, ...extra }).success).toBe(false);
    }
  });
});

describe('SettingsSchema', () => {
  it('accepts the default settings', () => {
    expect(SettingsSchema.parse(defaultSettings())).toEqual(defaultSettings());
  });

  it('rejects credential- or server-domain-shaped fields fail-closed', () => {
    for (const extra of [
      { token: 'abc' },
      { serverUrl: 'http://x' },
      { features: [] },
      { runs: [] },
    ]) {
      const candidate = { ...defaultSettings(), ...extra };
      expect(SettingsSchema.safeParse(candidate).success).toBe(false);
    }
  });

  it('rejects unsupported schema versions', () => {
    expect(SettingsSchema.safeParse({ ...defaultSettings(), schemaVersion: 2 }).success).toBe(
      false,
    );
  });

  it('accepts a full settings document with window bounds and theme', () => {
    const doc = {
      schemaVersion: 1,
      runtime: { selection: 'claude' },
      window: { bounds: { x: 10, y: 20, width: 800, height: 600 } },
      theme: 'dark',
    };
    expect(SettingsSchema.parse(doc)).toEqual(doc);
  });
});

describe('SettingsPatchSchema', () => {
  it('accepts partial updates', () => {
    expect(SettingsPatchSchema.parse({ theme: 'light' })).toEqual({ theme: 'light' });
    expect(SettingsPatchSchema.parse({ runtime: { selection: null } })).toEqual({
      runtime: { selection: null },
    });
  });

  it('rejects schemaVersion tampering and unknown keys', () => {
    expect(SettingsPatchSchema.safeParse({ schemaVersion: 9 }).success).toBe(false);
    expect(SettingsPatchSchema.safeParse({ apiToken: 'x' }).success).toBe(false);
  });
});

describe('IpcEnvelopeSchema', () => {
  it('accepts ok and error envelopes', () => {
    expect(IpcEnvelopeSchema.safeParse({ ok: true, value: { any: 1 } }).success).toBe(true);
    expect(
      IpcEnvelopeSchema.safeParse({ ok: false, error: { code: 'E_X', message: 'm' } }).success,
    ).toBe(true);
  });

  it('rejects malformed envelopes', () => {
    expect(IpcEnvelopeSchema.safeParse({ ok: false }).success).toBe(false);
    expect(IpcEnvelopeSchema.safeParse({ value: 1 }).success).toBe(false);
  });
});
