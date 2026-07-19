import { describe, expect, it, vi } from 'vitest';
import { registerIpcHandlers, type IpcServices } from '../../src/main/ipcHandlers';
import type { TrustedSender } from '../../src/main/security';
import {
  IPC_CHANNELS,
  IPC_EVENTS,
  defaultSettings,
  type SessionOutputEvent,
} from '../../src/shared/ipc';

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

function emptyReadinessSnapshot() {
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

function makeServices(): IpcServices {
  return {
    getConnectionStatus: vi.fn(() => ({
      status: 'discovering' as const,
      stage: 'discover' as const,
      detail: 'Looking for a running Agentico runtime.',
      ownership: 'none' as const,
    })),
    retryConnection: vi.fn(() => ({
      status: 'resolving-runtime' as const,
      stage: 'resolve-runtime' as const,
      detail: 'Resolving the selected runtime.',
      ownership: 'none' as const,
    })),
    restartConnection: vi.fn(() => ({
      status: 'resolving-runtime' as const,
      stage: 'resolve-runtime' as const,
      detail: 'Restarting to apply the pending runtime change.',
      ownership: 'none' as const,
    })),
    getSettings: vi.fn(() => defaultSettings()),
    updateSettings: vi.fn((patch) => ({ ...defaultSettings(), ...patch })),
    getTheme: vi.fn(() => ({ preference: 'system' as const, resolved: 'dark' as const })),
    setTheme: vi.fn((preference) => ({ preference, resolved: 'light' as const })),
    getReadiness: vi.fn(() => Promise.resolve(emptyReadinessSnapshot())),
    refreshReadiness: vi.fn(() => Promise.resolve(emptyReadinessSnapshot())),
    pickWorkspaceDirectory: vi.fn(() => Promise.resolve({ path: null })),
    addWorkspaceRoot: vi.fn(() => Promise.resolve(emptyReadinessSnapshot())),
    removeWorkspaceRoot: vi.fn(() => Promise.resolve(emptyReadinessSnapshot())),
    reorderWorkspaceRoots: vi.fn(() => Promise.resolve(emptyReadinessSnapshot())),
    initRepository: vi.fn(() => Promise.resolve(emptyReadinessSnapshot())),
    listRepositories: vi.fn(() => Promise.resolve([])),
    listFeatures: vi.fn(() => Promise.resolve([])),
    getFeature: vi.fn(() =>
      Promise.resolve({
        id: 'abcd1234ef567890',
        name: 'Feature',
        slug: 'feature',
        status: 'Created',
        currentPhase: 'Plan',
        repos: [],
        createdAt: '2026-07-14T10:00:00Z',
        activeRun: 1,
        actions: [],
      }),
    ),
    createFeature: vi.fn(() => Promise.resolve({ featureId: 'abcd1234ef567890' })),
    dispatchFeatureSetup: vi.fn(() => Promise.resolve({ result: 'setup_started' })),
    dispatchFeatureAction: vi.fn(() => Promise.reject(new Error('unused'))),
    getAttention: vi.fn(() => Promise.resolve({ items: [] })),
    answerPermission: vi.fn(() => Promise.resolve({ result: 'submitted' })),
    answerQuestions: vi.fn(() => Promise.resolve({ result: 'submitted' })),
    sendHelp: vi.fn(() => Promise.resolve({ result: 'submitted' })),
    saveGateDraft: vi.fn(() => Promise.resolve({ result: 'drafted' })),
    resolveGate: vi.fn(() => Promise.resolve({ result: 'resolved' })),
    listSessions: vi.fn(() => Promise.resolve([])),
    getSession: vi.fn(() => Promise.reject(new Error('unused'))),
    getSessionTranscript: vi.fn(() => Promise.reject(new Error('unused'))),
    openSessionOutput: vi.fn(() => 'sub-unused'),
    cancelSessionOutput: vi.fn(() => false),
    getCreationDefaults: vi.fn(() =>
      Promise.resolve({ repositories: [], defaults: { models: [], useCurrentBranch: false } }),
    ),
    loadLocalReviewDraft: vi.fn(() => null),
    saveLocalReviewDraft: vi.fn((request) => ({ ...request, savedAt: '2026-07-16T00:00:00.000Z' })),
    discardLocalReviewDraft: vi.fn(() => false),
    readReview: vi.fn(() => Promise.reject(new Error('unused'))),
    openReview: vi.fn(() => Promise.reject(new Error('unused'))),
    saveReview: vi.fn(() => Promise.reject(new Error('unused'))),
    validateReview: vi.fn(() => Promise.reject(new Error('unused'))),
    decideReview: vi.fn(() => Promise.reject(new Error('unused'))),
    listResources: vi.fn(() => Promise.resolve({ resources: [] })),
    readResource: vi.fn(() => Promise.reject(new Error('unused'))),
    validateResource: vi.fn(() => Promise.reject(new Error('unused'))),
    writeResource: vi.fn(() => Promise.reject(new Error('unused'))),
    loadLocalResourceDraft: vi.fn(() => null),
    saveLocalResourceDraft: vi.fn((request) => ({
      ...request,
      savedAt: '2026-07-16T00:00:00.000Z',
    })),
    discardLocalResourceDraft: vi.fn(() => false),
    listRuns: vi.fn(() => Promise.reject(new Error('unused'))),
    getRun: vi.fn(() => Promise.reject(new Error('unused'))),
    listRunSessions: vi.fn(() => Promise.reject(new Error('unused'))),
    listRunArtifacts: vi.fn(() => Promise.reject(new Error('unused'))),
    getRunArtifactContent: vi.fn(() => Promise.reject(new Error('unused'))),
    getRunLogContent: vi.fn(() => Promise.reject(new Error('unused'))),
    getRewindPreview: vi.fn(() => Promise.reject(new Error('unused'))),
    executeRewind: vi.fn(() => Promise.reject(new Error('unused'))),
    startRebase: vi.fn(() => Promise.reject(new Error('unused'))),
    preflightRebase: vi.fn(() => Promise.reject(new Error('unused'))),
    fetchReviewComments: vi.fn(() => Promise.reject(new Error('unused'))),
    startReviewComments: vi.fn(() => Promise.reject(new Error('unused'))),
    startRefactor: vi.fn(() => Promise.reject(new Error('unused'))),
    preflightRefactor: vi.fn(() => Promise.reject(new Error('unused'))),
    scanRecovery: vi.fn(() => Promise.reject(new Error('unused'))),
    executeRecovery: vi.fn(() => Promise.reject(new Error('unused'))),
    readRecoveryLog: vi.fn(() => Promise.reject(new Error('unused'))),
    bulkPreview: vi.fn(() => Promise.reject(new Error('unused'))),
    preflightCompletion: vi.fn(() => Promise.reject(new Error('unused'))),
    getRepositoryDiff: vi.fn(() => Promise.reject(new Error('unused'))),
    generatePublishDescription: vi.fn(() => Promise.reject(new Error('unused'))),
    openExternal: vi.fn(() => Promise.reject(new Error('unused'))),
    revealPath: vi.fn(() => Promise.reject(new Error('unused'))),
  };
}

function register(services = makeServices()) {
  const handlers = new Map<string, (event: unknown, ...args: unknown[]) => Promise<unknown>>();
  const ipcMain = {
    handle: vi.fn(
      (channel: string, listener: typeof handlers extends Map<string, infer H> ? H : never) => {
        handlers.set(channel, listener);
      },
    ),
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
    expect(result.value.status).toBe('discovering');
  });

  it('invokes the required attention service instead of returning a fallback success', async () => {
    const { handlers, services } = register();
    const result = (await handlers.get(IPC_CHANNELS.attentionAnswerPermission)!(goodEvent, {
      requestId: 'perm-1',
      decision: 'deny',
    })) as { ok: boolean; value: { result: string } };

    expect(result.ok).toBe(true);
    expect(result.value.result).toBe('submitted');
    expect(services.answerPermission).toHaveBeenCalledWith({
      requestId: 'perm-1',
      decision: 'deny',
    });
  });

  it('rejects a connection state carrying token-shaped fields fail-closed', async () => {
    const services = makeServices();
    services.getConnectionStatus = vi.fn(
      () =>
        ({
          status: 'ready',
          stage: 'ready',
          detail: 'ok',
          ownership: 'app-owned',
          authToken: 'tok-leak-123',
        }) as never,
    );
    const { handlers } = register(services);
    const result = (await handlers.get(IPC_CHANNELS.connectionGetStatus)!(goodEvent)) as {
      ok: boolean;
      error?: { code: string };
    };
    expect(result.ok).toBe(false);
    expect(result.error?.code).toBe('E_SCHEMA_MISMATCH');
    expect(JSON.stringify(result)).not.toContain('tok-leak-123');
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

  it('validates local draft requests before calling the owner-only store', async () => {
    const { handlers, services } = register();
    const result = (await handlers.get(IPC_CHANNELS.reviewDraftsSave)!(goodEvent, {
      runtimeId: 'runtime-a',
      featureId: 'feature-a',
      reviewId: 'review-a',
      baseDraftRevision: 'base-a',
      text: 'draft',
      extra: 'rejected',
    })) as { ok: boolean; error?: { code: string } };

    expect(result.ok).toBe(false);
    expect(result.error?.code).toBe('E_SCHEMA_MISMATCH');
    expect(services.saveLocalReviewDraft).not.toHaveBeenCalled();
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

  it('does not send session output after the renderer is destroyed', async () => {
    let emit!: (event: SessionOutputEvent) => void;
    const services = makeServices();
    services.openSessionOutput = vi.fn((_request, listener) => {
      emit = listener;
      return 'sub-fixed';
    });
    const send = vi.fn();
    const sender = {
      id: 1,
      send,
      isDestroyed: vi.fn(() => false),
    };
    const event = {
      sender,
      senderFrame: { url: 'file:///app/out/renderer/index.html' },
    };
    const { handlers } = register(services);

    await expect(
      handlers.get(IPC_CHANNELS.sessionsOutputOpen)!(event, {
        sessionId: 'session-1',
      }),
    ).resolves.toMatchObject({ ok: true, value: { subscriptionId: 'sub-fixed' } });
    sender.isDestroyed.mockReturnValue(true);
    emit({
      subscriptionId: 'sub-fixed',
      type: 'done',
      sessionId: 'session-1',
      nextIndex: 1,
    });

    expect(send).not.toHaveBeenCalledWith(IPC_EVENTS.sessionOutput, expect.anything());
  });
});
