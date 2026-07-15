/**
 * Security posture of the first-launch setup IPC surface: spoofed senders
 * are rejected, repository initialization is consent-gated at the schema
 * layer, paths are constrained to validated absolute strings, and no
 * envelope ever carries bearer material or raw server payloads.
 */
import { describe, expect, it, vi } from 'vitest';
import { registerIpcHandlers, type IpcServices } from '../../src/main/ipcHandlers';
import type { TrustedSender } from '../../src/main/security';
import { SetupService } from '../../src/main/setup';
import { IPC_CHANNELS, defaultSettings, type ReadinessSnapshot } from '../../src/shared/ipc';

const trusted: TrustedSender = {
  webContentsId: 1,
  allowedOrigins: new Set(['file://']),
};

const goodEvent = {
  sender: { id: 1 },
  senderFrame: { url: 'file:///app/out/renderer/index.html' },
};
const foreignEvent = {
  sender: { id: 66 },
  senderFrame: { url: 'https://evil.example.com/' },
};

function snapshot(): ReadinessSnapshot {
  return {
    ready: false,
    providers: [],
    models: { available: false },
    configuration: { valid: true },
    workspaceRoots: [],
    repositories: [],
    issues: [],
  };
}

function makeServices(overrides: Partial<IpcServices> = {}): IpcServices {
  return {
    getConnectionStatus: vi.fn(() => ({
      status: 'ready' as const,
      stage: 'ready' as const,
      detail: 'ok',
      ownership: 'external' as const,
    })),
    retryConnection: vi.fn(() => ({
      status: 'ready' as const,
      stage: 'ready' as const,
      detail: 'ok',
      ownership: 'external' as const,
    })),
    getSettings: vi.fn(() => defaultSettings()),
    updateSettings: vi.fn(() => defaultSettings()),
    getTheme: vi.fn(() => ({ preference: 'system' as const, resolved: 'dark' as const })),
    setTheme: vi.fn((preference) => ({ preference, resolved: 'dark' as const })),
    getReadiness: vi.fn(() => Promise.resolve(snapshot())),
    refreshReadiness: vi.fn(() => Promise.resolve(snapshot())),
    pickWorkspaceDirectory: vi.fn(() => Promise.resolve({ path: null })),
    addWorkspaceRoot: vi.fn(() => Promise.resolve(snapshot())),
    initRepository: vi.fn(() => Promise.resolve(snapshot())),
    listRepositories: vi.fn(() => Promise.resolve([])),
    listFeatures: vi.fn(() => Promise.resolve([])),
    getFeature: vi.fn(() => Promise.reject(new Error('not_found: feature not found'))),
    createFeature: vi.fn(() => Promise.resolve({ featureId: 'abcd1234ef567890' })),
    dispatchFeatureSetup: vi.fn(() => Promise.resolve({ result: 'setup_started' })),
    getCreationDefaults: vi.fn(() =>
      Promise.resolve({
        repositories: [],
        defaults: { models: [], useCurrentBranch: false },
      }),
    ),
    ...overrides,
  };
}

function register(services = makeServices()) {
  const handlers = new Map<string, (event: unknown, ...args: unknown[]) => Promise<unknown>>();
  const ipcMain = {
    handle: vi.fn((channel: string, listener: never) => {
      handlers.set(channel, listener as (event: unknown, ...args: unknown[]) => Promise<unknown>);
    }),
  };
  registerIpcHandlers(ipcMain, trusted, services);
  return { handlers, services };
}

interface Envelope {
  ok: boolean;
  value?: unknown;
  error?: { code: string; message: string };
}

describe('setup IPC surface: sender validation', () => {
  const setupChannels = [
    IPC_CHANNELS.readinessGet,
    IPC_CHANNELS.readinessRefresh,
    IPC_CHANNELS.workspacePickDirectory,
    IPC_CHANNELS.workspaceAddRoot,
    IPC_CHANNELS.workspaceInitRepository,
    IPC_CHANNELS.repositoriesList,
  ];

  it('rejects spoofed senders on every setup channel without invoking services', async () => {
    const { handlers, services } = register();
    for (const channel of setupChannels) {
      const result = (await handlers.get(channel)!(foreignEvent)) as Envelope;
      expect(result.ok, channel).toBe(false);
      expect(result.error?.code, channel).toBe('E_UNTRUSTED_SENDER');
    }
    expect(services.getReadiness).not.toHaveBeenCalled();
    expect(services.pickWorkspaceDirectory).not.toHaveBeenCalled();
    expect(services.initRepository).not.toHaveBeenCalled();
    expect(services.addWorkspaceRoot).not.toHaveBeenCalled();
  });
});

describe('setup IPC surface: consent gating', () => {
  it('rejects repository init without consent at the schema layer', async () => {
    const { handlers, services } = register();
    for (const request of [
      { path: '/work/repo', consent: false },
      { path: '/work/repo', consent: 'true' },
      { path: '/work/repo' },
      { path: '/work/repo', consent: true, extra: 'field' },
    ]) {
      const result = (await handlers.get(IPC_CHANNELS.workspaceInitRepository)!(
        goodEvent,
        request,
      )) as Envelope;
      expect(result.ok).toBe(false);
      expect(result.error?.code).toBe('E_SCHEMA_MISMATCH');
    }
    expect(services.initRepository).not.toHaveBeenCalled();
  });

  it('passes a well-formed consenting request through to the service', async () => {
    const { handlers, services } = register();
    const result = (await handlers.get(IPC_CHANNELS.workspaceInitRepository)!(goodEvent, {
      path: '/work/repo',
      consent: true,
    })) as Envelope;
    expect(result.ok).toBe(true);
    expect(services.initRepository).toHaveBeenCalledWith({ path: '/work/repo', consent: true });
  });
});

describe('setup IPC surface: path validation', () => {
  it('rejects relative, traversal-decoy, and NUL paths before the service runs', async () => {
    const { handlers, services } = register();
    for (const bad of ['relative/path', '', '/nul\0path', 42, null, { path: '/x' }]) {
      const result = (await handlers.get(IPC_CHANNELS.workspaceAddRoot)!(
        goodEvent,
        bad,
      )) as Envelope;
      expect(result.ok).toBe(false);
    }
    expect(services.addWorkspaceRoot).not.toHaveBeenCalled();
  });

  it('returns only a validated absolute path string (or null) from the picker', async () => {
    const { handlers } = register(
      makeServices({
        pickWorkspaceDirectory: vi.fn(() => Promise.resolve({ path: '/work/space' })),
      }),
    );
    const result = (await handlers.get(IPC_CHANNELS.workspacePickDirectory)!(
      goodEvent,
    )) as Envelope;
    expect(result.ok).toBe(true);
    expect(result.value).toEqual({ path: '/work/space' });
  });

  it('fails closed when a service returns a non-absolute picker path', async () => {
    const { handlers } = register(
      makeServices({
        pickWorkspaceDirectory: vi.fn(() => Promise.resolve({ path: 'relative/dir' })),
      }),
    );
    const result = (await handlers.get(IPC_CHANNELS.workspacePickDirectory)!(
      goodEvent,
    )) as Envelope;
    expect(result.ok).toBe(false);
    expect(result.error?.code).toBe('E_SCHEMA_MISMATCH');
  });
});

describe('setup IPC surface: no token or raw payload leakage', () => {
  it('rejects readiness snapshots carrying token-shaped fields fail-closed', async () => {
    const { handlers } = register(
      makeServices({
        getReadiness: vi.fn(() =>
          Promise.resolve({ ...snapshot(), authToken: 'tok-leak-42' } as never),
        ),
      }),
    );
    const result = (await handlers.get(IPC_CHANNELS.readinessGet)!(goodEvent)) as Envelope;
    expect(result.ok).toBe(false);
    expect(result.error?.code).toBe('E_SCHEMA_MISMATCH');
    expect(JSON.stringify(result)).not.toContain('tok-leak-42');
  });

  it('redacts service exceptions that mention credentials or home paths', async () => {
    const { handlers } = register(
      makeServices({
        refreshReadiness: vi.fn(() =>
          Promise.reject(new Error('refresh failed for /Users/someone with Bearer tok-99')),
        ),
      }),
    );
    const result = (await handlers.get(IPC_CHANNELS.readinessRefresh)!(goodEvent)) as Envelope;
    expect(result.ok).toBe(false);
    const raw = JSON.stringify(result);
    expect(raw).not.toContain('/Users/someone');
    expect(raw).not.toContain('tok-99');
  });
});

describe('SetupService transport confinement', () => {
  it('never lets a renderer-shaped path reach the transport as a URL', async () => {
    const apiRequest = vi.fn(() =>
      Promise.resolve({
        status: 200,
        body: {
          api_version: 'v1',
          ready: false,
          providers: [],
          models: { available: false },
          configuration: { valid: true },
          workspace: { roots: [], repositories: [] },
        },
      }),
    );
    const service = new SetupService({
      transport: { apiRequest },
      dialogs: { pickDirectory: () => Promise.resolve(null) },
    });
    await service.getReadiness();
    // Transport is invoked with fixed /api/v1 paths only — never renderer data.
    for (const call of apiRequest.mock.calls as unknown as [string, unknown][]) {
      expect(call[0]).toMatch(/^\/api\/v1\//);
    }
  });
});
