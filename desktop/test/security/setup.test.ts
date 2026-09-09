/*
Copyright 2026 DoorDash, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

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
import {
  IPC_CHANNELS,
  defaultSettings,
  type DiagnosticsSnapshot,
  type ReadinessSnapshot,
  type UpdateState,
} from '../../src/shared/ipc';

const trusted: TrustedSender = {
  webContentsIds: new Set([1]),
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

function updateState(): UpdateState {
  return {
    status: 'current' as const,
    currentVersion: '0.1.0',
    packageFormat: 'macos' as const,
    signatureStatus: 'unknown' as const,
    message: 'Agentico is up to date.',
  };
}

function diagnosticsSnapshot(): DiagnosticsSnapshot {
  return {
    retention: {
      maxBytes: 25 * 1024 * 1024,
      maxAgeDays: 7,
      maxCrashRecords: 10,
      currentBytes: 0,
      entryCount: 0,
      crashCount: 0,
    },
    entries: [],
    crashes: [],
  };
}

function makeServices(overrides: Partial<IpcServices> = {}): IpcServices {
  return {
    getConnectionStatus: vi.fn(() => ({
      status: 'ready' as const,
      stage: 'ready' as const,
      detail: 'ok',
      ownership: 'external' as const,
      kind: 'remote' as const,
    })),
    retryConnection: vi.fn(() => ({
      status: 'ready' as const,
      stage: 'ready' as const,
      detail: 'ok',
      ownership: 'external' as const,
      kind: 'remote' as const,
    })),
    restartConnection: vi.fn(() => ({
      status: 'ready' as const,
      stage: 'ready' as const,
      detail: 'ok',
      ownership: 'external' as const,
      kind: 'remote' as const,
    })),
    chooseConnectionServer: vi.fn(() => ({
      status: 'ready' as const,
      stage: 'ready' as const,
      detail: 'ok',
      ownership: 'external' as const,
      kind: 'remote' as const,
    })),
    switchConnectionServer: vi.fn(() => ({
      status: 'ready' as const,
      stage: 'ready' as const,
      detail: 'ok',
      ownership: 'external' as const,
      kind: 'remote' as const,
    })),
    startLocalRuntime: vi.fn(() => ({
      status: 'ready' as const,
      stage: 'ready' as const,
      detail: 'ok',
      ownership: 'external' as const,
      kind: 'remote' as const,
    })),
    listServers: vi.fn(() => ({ rows: [] })),
    probeServers: vi.fn(() => ({ rows: [] })),
    addRemoteServer: vi.fn(() => Promise.reject(new Error('unused'))),
    removeServer: vi.fn(() => Promise.reject(new Error('unused'))),
    getServerTokenStatus: vi.fn(() => Promise.reject(new Error('unused'))),
    getSettings: vi.fn(() => defaultSettings()),
    updateSettings: vi.fn(() => defaultSettings()),
    openSettingsWindow: vi.fn(() => ({ opened: true })),
    getTheme: vi.fn(() => ({ preference: 'system' as const, resolved: 'dark' as const })),
    setTheme: vi.fn((preference) => ({ preference, resolved: 'dark' as const })),
    getReadiness: vi.fn(() => Promise.resolve(snapshot())),
    refreshReadiness: vi.fn(() => Promise.resolve(snapshot())),
    pickWorkspaceDirectory: vi.fn(() => Promise.resolve({ path: null })),
    addWorkspaceRoot: vi.fn(() => Promise.resolve(snapshot())),
    removeWorkspaceRoot: vi.fn(() => Promise.resolve(snapshot())),
    reorderWorkspaceRoots: vi.fn(() => Promise.resolve(snapshot())),
    initRepository: vi.fn(() => Promise.resolve(snapshot())),
    startClone: vi.fn(() => Promise.reject(new Error('unused'))),
    getCloneOperation: vi.fn(() => Promise.reject(new Error('unused'))),
    listCloneOperations: vi.fn(() => Promise.reject(new Error('unused'))),
    cancelCloneOperation: vi.fn(() => Promise.reject(new Error('unused'))),
    retryCloneCleanup: vi.fn(() => Promise.reject(new Error('unused'))),
    retryCloneOperation: vi.fn(() => Promise.reject(new Error('unused'))),
    createRepository: vi.fn(() => Promise.reject(new Error('unused'))),
    initializeRepository: vi.fn(() => Promise.reject(new Error('unused'))),
    listRepositories: vi.fn(() => Promise.resolve([])),
    listFeatures: vi.fn(() => Promise.resolve({ features: [], warnings: [] })),
    getFeature: vi.fn(() => Promise.reject(new Error('not_found: feature not found'))),
    createFeature: vi.fn(() => Promise.resolve({ featureId: 'abcd1234ef567890' })),
    dispatchFeatureSetup: vi.fn(() => Promise.resolve({ result: 'setup_started' })),
    dispatchFeatureAction: vi.fn(() => Promise.reject(new Error('unused'))),
    getAttention: vi.fn(() => Promise.resolve({ items: [] })),
    answerPermission: vi.fn(() => Promise.resolve({ result: 'submitted' })),
    answerQuestions: vi.fn(() => Promise.resolve({ result: 'submitted' })),
    sendHelp: vi.fn(() => Promise.resolve({ result: 'submitted' })),
    saveGateDraft: vi.fn(() => Promise.resolve({ result: 'drafted' })),
    resolveGate: vi.fn(() => Promise.resolve({ result: 'resolved' })),
    startChat: vi.fn(() => Promise.resolve({ sessionId: '__chat__', result: 'started' })),
    endChat: vi.fn(() => Promise.resolve({ sessionId: '__chat__', result: 'ended' })),
    listSessions: vi.fn(() => Promise.resolve([])),
    getSession: vi.fn(() => Promise.reject(new Error('unused'))),
    getSessionTranscript: vi.fn(() => Promise.reject(new Error('unused'))),
    openSessionOutput: vi.fn(() => 'sub-unused'),
    cancelSessionOutput: vi.fn(() => false),
    getCreationDefaults: vi.fn(() =>
      Promise.resolve({
        repositories: [],
        workspaceRoots: [],
        defaults: { models: [], effort: [], useCurrentBranch: false },
      }),
    ),
    inspectRepositorySources: vi.fn(
      async (request: Parameters<IpcServices['inspectRepositorySources']>[0]) => ({
        repositories: request.repositories.map((repository) => ({
          ...repository,
          mode: request.mode,
          kind: 'branch' as const,
          branch: 'main',
          observedSha: 'a'.repeat(40),
        })),
      }),
    ),
    checkRepositoryOriginStatus: vi.fn(
      async (request: Parameters<IpcServices['checkRepositoryOriginStatus']>[0]) => ({
        repositories: request.repositories.map((repository) => ({
          ...repository,
          mode: request.mode,
          kind: 'branch' as const,
          branch: 'main',
          localSha: 'a'.repeat(40),
          status: 'no_origin' as const,
        })),
      }),
    ),
    updateRepositorySource: vi.fn(
      async (request: Parameters<IpcServices['updateRepositorySource']>[0]) => ({
        result: 'already_up_to_date' as const,
        repoKey: request.repoKey,
        identity: request.identity,
        mode: request.mode,
        branch: request.branch,
        originBranch: request.originBranch,
        localSha: request.expectedLocalSha,
      }),
    ),
    reconcileSourceUpdate: vi.fn(
      async (request: Parameters<IpcServices['reconcileSourceUpdate']>[0]) => ({
        outcome: 'original_tip_remains' as const,
        repoKey: request.repoKey,
        identity: request.identity,
        mode: request.mode,
        branch: request.branch,
        originBranch: request.originBranch,
        localSha: request.expectedLocalSha,
      }),
    ),
    loadLocalReviewDraft: vi.fn(() => null),
    saveLocalReviewDraft: vi.fn((request) => ({ ...request, savedAt: '2026-07-16T00:00:00.000Z' })),
    discardLocalReviewDraft: vi.fn(() => false),
    readReview: vi.fn(() => Promise.reject(new Error('unused'))),
    openReview: vi.fn(() => Promise.reject(new Error('unused'))),
    saveReview: vi.fn(() => Promise.reject(new Error('unused'))),
    validateReview: vi.fn(() => Promise.reject(new Error('unused'))),
    decideReview: vi.fn(() => Promise.reject(new Error('unused'))),
    getFeatureConfig: vi.fn(() => Promise.reject(new Error('unused'))),
    updateFeatureConfig: vi.fn(() => Promise.reject(new Error('unused'))),
    getWorkspaceDefaults: vi.fn(() => Promise.reject(new Error('unused'))),
    updateWorkspaceDefaults: vi.fn(() => Promise.reject(new Error('unused'))),
    getModelCatalogue: vi.fn(() => Promise.reject(new Error('unused'))),
    refreshProviderModels: vi.fn(() => Promise.reject(new Error('unused'))),
    listRuns: vi.fn(() => Promise.reject(new Error('unused'))),
    getRun: vi.fn(() => Promise.reject(new Error('unused'))),
    listRunSessions: vi.fn(() => Promise.reject(new Error('unused'))),
    getLivePreview: vi.fn(() => Promise.reject(new Error('unused'))),
    listRunArtifacts: vi.fn(() => Promise.reject(new Error('unused'))),
    listRunLogs: vi.fn(() => Promise.reject(new Error('unused'))),
    getRunArtifactContent: vi.fn(() => Promise.reject(new Error('unused'))),
    getRunLogContent: vi.fn(() => Promise.reject(new Error('unused'))),
    getRewindPreview: vi.fn(() => Promise.reject(new Error('unused'))),
    executeRewind: vi.fn(() => Promise.reject(new Error('unused'))),
    launchRebaseChild: vi.fn(() => Promise.reject(new Error('unused'))),
    launchRefactorChild: vi.fn(() => Promise.reject(new Error('unused'))),
    discardRefactorChild: vi.fn(() => Promise.reject(new Error('unused'))),
    deleteFeatureCascade: vi.fn(() => Promise.reject(new Error('unused'))),
    fetchReviewFeedback: vi.fn(() => Promise.reject(new Error('unused'))),
    updateReviewFeedbackSelection: vi.fn(() => Promise.reject(new Error('unused'))),
    launchReviewFeedbackChild: vi.fn(() => Promise.reject(new Error('unused'))),
    scanRecovery: vi.fn(() => Promise.reject(new Error('unused'))),
    executeRecovery: vi.fn(() => Promise.reject(new Error('unused'))),
    readRecoveryLog: vi.fn(() => Promise.reject(new Error('unused'))),
    bulkPreview: vi.fn(() => Promise.reject(new Error('unused'))),
    getUpdates: vi.fn(() => updateState()),
    checkForUpdates: vi.fn(() => Promise.resolve(updateState())),
    installUpdateWhenIdle: vi.fn(() => Promise.resolve(updateState())),
    installUpdateNow: vi.fn(() => Promise.resolve(updateState())),
    restartToUpdate: vi.fn(() => updateState()),
    getDiagnostics: vi.fn(() => diagnosticsSnapshot()),
    revealDiagnostics: vi.fn(() => Promise.resolve({ ok: true })),
    clearDiagnostics: vi.fn(() => diagnosticsSnapshot()),
    preflightCompletion: vi.fn(() => Promise.reject(new Error('unused'))),
    getRepositoryDiff: vi.fn(() => Promise.reject(new Error('unused'))),
    generatePublishDescription: vi.fn(() => Promise.reject(new Error('unused'))),
    openExternal: vi.fn(() => Promise.reject(new Error('unused'))),
    revealPath: vi.fn(() => Promise.reject(new Error('unused'))),
    writeClipboardText: vi.fn(() => Promise.reject(new Error('unused'))),
    publishUiState: vi.fn(() => ({ accepted: true })),
    ...overrides,
    pickCreationFiles: overrides.pickCreationFiles ?? vi.fn(() => Promise.resolve({ paths: [] })),
    readClipboardImage: overrides.readClipboardImage ?? vi.fn(() => Promise.resolve({ paths: [] })),
    uploadCreationFiles:
      overrides.uploadCreationFiles ?? vi.fn(() => Promise.resolve({ results: [] })),
    searchCreationFiles:
      overrides.searchCreationFiles ??
      vi.fn((request) =>
        Promise.resolve({
          requestId: request.requestId,
          files: [],
          truncated: false,
          cancelled: false,
        }),
      ),
    cancelCreationFileSearch: overrides.cancelCreationFileSearch ?? vi.fn(() => false),
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
  error?: { code: string };
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

  it('rejects repository creation without consent at the schema layer', async () => {
    const { handlers, services } = register();
    for (const request of [
      { rootPath: '/work', destination: 'repo', idempotencyKey: 'key-12345678', consent: false },
      { rootPath: '/work', destination: 'repo', idempotencyKey: 'key-12345678' },
      { rootPath: '/work', destination: 'repo', idempotencyKey: 'key-12345678', consent: 'yes' },
      {
        rootPath: '/work',
        destination: 'repo',
        idempotencyKey: 'key-12345678',
        consent: true,
        extra: 1,
      },
    ]) {
      const result = (await handlers.get(IPC_CHANNELS.createRepository)!(
        goodEvent,
        request,
      )) as Envelope;
      expect(result.ok).toBe(false);
      expect(result.error?.code).toBe('E_SCHEMA_MISMATCH');
    }
    expect(services.createRepository).not.toHaveBeenCalled();
  });

  it('passes a well-formed consenting create request through to the service', async () => {
    const { handlers, services } = register(
      makeServices({
        createRepository: vi.fn(() =>
          Promise.resolve({
            repoKey: 'repo',
            path: '/work/repo',
            hasHead: true,
            root: '/work',
            identity: {
              path: '/work/repo',
              commonDir: '/work/repo/.git',
              device: '16777234',
              inode: '4242',
            },
          }),
        ),
      }),
    );
    const result = (await handlers.get(IPC_CHANNELS.createRepository)!(goodEvent, {
      rootPath: '/work',
      destination: 'repo',
      idempotencyKey: 'key-12345678',
      consent: true,
    })) as Envelope;
    expect(result.ok).toBe(true);
    expect(services.createRepository).toHaveBeenCalledWith({
      rootPath: '/work',
      destination: 'repo',
      idempotencyKey: 'key-12345678',
      consent: true,
    });
  });

  it('rejects repository initialization without consent at the schema layer', async () => {
    const { handlers, services } = register();
    const identity = {
      path: '/work/unborn',
      commonDir: '/work/unborn/.git',
      device: '16777234',
      inode: '4242',
    };
    for (const request of [
      { repoKey: 'unborn', identity, consent: false },
      { repoKey: 'unborn', identity },
      { repoKey: 'unborn', identity, consent: 'true' },
      { repoKey: 'unborn', identity, consent: true, extra: 'field' },
    ]) {
      const result = (await handlers.get(IPC_CHANNELS.initializeRepository)!(
        goodEvent,
        request,
      )) as Envelope;
      expect(result.ok).toBe(false);
      expect(result.error?.code).toBe('E_SCHEMA_MISMATCH');
    }
    expect(services.initializeRepository).not.toHaveBeenCalled();
  });

  it('passes a well-formed consenting initialize request through to the service', async () => {
    const { handlers, services } = register(
      makeServices({
        initializeRepository: vi.fn(() =>
          Promise.resolve({
            result: 'initialized' as const,
            repoKey: 'unborn',
            path: '/work/unborn',
            hasHead: true,
            root: '/work',
            identity: {
              path: '/work/unborn',
              commonDir: '/work/unborn/.git',
              device: '16777234',
              inode: '4242',
            },
          }),
        ),
      }),
    );
    const result = (await handlers.get(IPC_CHANNELS.initializeRepository)!(goodEvent, {
      repoKey: 'unborn',
      identity: {
        path: '/work/unborn',
        commonDir: '/work/unborn/.git',
        device: '16777234',
        inode: '4242',
      },
      consent: true,
    })) as Envelope;
    expect(result.ok).toBe(true);
    expect(services.initializeRepository).toHaveBeenCalledWith({
      repoKey: 'unborn',
      identity: {
        path: '/work/unborn',
        commonDir: '/work/unborn/.git',
        device: '16777234',
        inode: '4242',
      },
      consent: true,
    });
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
      dialogs: {
        pickDirectory: () => Promise.resolve(null),
      },
    });
    await service.getReadiness();
    // Transport is invoked with fixed /api/v1 paths only — never renderer data.
    for (const call of apiRequest.mock.calls as unknown as [string, unknown][]) {
      expect(call[0]).toMatch(/^\/api\/v1\//);
    }
  });
});

describe('SetupService locality enforcement on a remote connection', () => {
  const remoteLocality = () => 'remote' as const;

  function makeRemoteSetupService() {
    const apiRequest = vi.fn(() =>
      Promise.resolve({
        status: 200,
        body: {
          api_version: 'v1',
          ready: false,
          providers: [],
          models: { available: false },
          configuration: { valid: true },
          workspace: {
            roots: [{ path: '/work', valid: true, clone_eligible: true }],
            repositories: [],
          },
        },
      }),
    );
    const pickDirectory = vi.fn(() => Promise.resolve(null));
    const service = new SetupService({
      transport: { apiRequest },
      dialogs: { pickDirectory },
      locality: remoteLocality,
    });
    return { service, apiRequest, pickDirectory };
  }

  it('addWorkspaceRoot refuses with E_REQUIRES_LOCAL_SERVER before any request or dialog', async () => {
    const { service, apiRequest } = makeRemoteSetupService();
    await expect(service.addWorkspaceRoot('/work/new')).rejects.toMatchObject({
      canonical: { code: 'E_REQUIRES_LOCAL_SERVER' },
    });
    // A remote server's roots are administrator-owned: the refusal fires
    // before any remote request, exactly like the native picker.
    expect(apiRequest).not.toHaveBeenCalled();
  });

  it('removeWorkspaceRoot refuses with E_REQUIRES_LOCAL_SERVER and never calls the transport', async () => {
    const { service, apiRequest } = makeRemoteSetupService();
    await expect(service.removeWorkspaceRoot('/work/old')).rejects.toMatchObject({
      canonical: { code: 'E_REQUIRES_LOCAL_SERVER' },
    });
    expect(apiRequest).not.toHaveBeenCalled();
  });

  it('reorderWorkspaceRoots refuses with E_REQUIRES_LOCAL_SERVER and never calls the transport', async () => {
    const { service, apiRequest } = makeRemoteSetupService();
    await expect(service.reorderWorkspaceRoots(['/a', '/b'])).rejects.toMatchObject({
      canonical: { code: 'E_REQUIRES_LOCAL_SERVER' },
    });
    expect(apiRequest).not.toHaveBeenCalled();
  });

  it('pickWorkspaceDirectory refuses with E_REQUIRES_LOCAL_SERVER and never calls the dialog', async () => {
    const { service, pickDirectory } = makeRemoteSetupService();
    await expect(service.pickWorkspaceDirectory()).rejects.toMatchObject({
      canonical: { code: 'E_REQUIRES_LOCAL_SERVER' },
    });
    expect(pickDirectory).not.toHaveBeenCalled();
  });
});
