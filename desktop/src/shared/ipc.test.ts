import { describe, expect, it } from 'vitest';
import {
  ConnectionStateSchema,
  IPC_CHANNELS,
  IPC_EVENTS,
  InitRepositoryRequestSchema,
  IpcEnvelopeSchema,
  AbsolutePathSchema,
  ReadinessSnapshotSchema,
  SettingsPatchSchema,
  SettingsSchema,
  defaultSettings,
  defaultWizardPrefs,
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
      wizard: { collapsedHelp: true, lastRepositoryPathHint: '/work/repo' },
    };
    expect(SettingsSchema.parse(doc)).toEqual(doc);
  });

  it('fills wizard presentation prefs with defaults for pre-wizard documents', () => {
    const doc = {
      schemaVersion: 1,
      runtime: { selection: null },
      window: {},
      theme: 'system',
    };
    expect(SettingsSchema.parse(doc)).toEqual({ ...doc, wizard: defaultWizardPrefs() });
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

describe('ReadinessSnapshotSchema', () => {
  const snapshot = {
    ready: false,
    probedAt: '2026-07-14T10:00:00Z',
    providers: [
      {
        name: 'claude',
        installed: true,
        version: '2.1.0',
        ready: false,
        issue: { code: 'unauthenticated', message: 'not authenticated', remedy: 'claude login' },
      },
    ],
    models: { available: false, issue: { code: 'models_unavailable', message: 'no models' } },
    configuration: { valid: true },
    workspaceRoots: [{ path: '/w', valid: true }],
    repositories: [{ name: 'r', path: '/w/r', valid: true }],
    issues: [{ code: 'unauthenticated', message: 'not authenticated', remedy: 'claude login' }],
  };

  it('accepts a complete snapshot', () => {
    expect(ReadinessSnapshotSchema.parse(snapshot)).toEqual(snapshot);
  });

  it('rejects unknown issue codes and token-shaped extras fail-closed', () => {
    expect(
      ReadinessSnapshotSchema.safeParse({
        ...snapshot,
        issues: [{ code: 'mystery', message: 'x' }],
      }).success,
    ).toBe(false);
    for (const extra of [{ authToken: 'x' }, { token: 'x' }, { baseUrl: 'http://127.0.0.1:1' }]) {
      expect(ReadinessSnapshotSchema.safeParse({ ...snapshot, ...extra }).success).toBe(false);
    }
  });
});

describe('AbsolutePathSchema', () => {
  it('accepts absolute POSIX paths', () => {
    expect(AbsolutePathSchema.parse('/work/space')).toBe('/work/space');
  });

  it('rejects relative paths, NUL bytes, and empty strings', () => {
    for (const bad of ['relative/path', '', '/bad\0path', '/bad\npath', 'C:\\windows']) {
      expect(AbsolutePathSchema.safeParse(bad).success).toBe(false);
    }
  });
});

describe('InitRepositoryRequestSchema', () => {
  it('requires consent to be literally true at the schema layer', () => {
    expect(InitRepositoryRequestSchema.safeParse({ path: '/w/repo', consent: true }).success).toBe(
      true,
    );
    for (const consent of [false, 'true', 1, undefined]) {
      expect(InitRepositoryRequestSchema.safeParse({ path: '/w/repo', consent }).success).toBe(
        false,
      );
    }
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
