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
 * Security posture of the feature-creation IPC surface: spoofed senders are
 * rejected on every new op, creation input and feature ids are validated at
 * the schema layer before any service runs, responses that violate their
 * strict schemas fail closed, and the local shell settings section can never
 * store server-domain state beyond identity/presentation.
 */
import { describe, expect, it, vi } from 'vitest';
import { registerIpcHandlers, type IpcServices } from '../../src/main/ipcHandlers';
import type { TrustedSender } from '../../src/main/security';
import {
  IPC_CHANNELS,
  defaultSettings,
  type DiagnosticsSnapshot,
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

function snapshot() {
  return {
    id: 'abcd1234ef567890',
    name: 'Search revamp',
    slug: 'search-revamp',
    status: 'Created',
    currentPhase: 'Plan',
    repos: ['repo-a'],
    createdAt: '2026-07-14T10:00:00Z',
    activeRun: 1,
    automaticReview: {
      mode: 'default' as const,
      enabled: true,
      source: 'global' as const,
    },
    reviewGate: {
      reviewingGate: false,
      reviewFixing: false,
      validatingPlan: false,
      validatorStatuses: {},
    },
    actions: [{ id: 'start', enabled: true, disabledReasons: [] }],
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
    getReadiness: vi.fn(() => Promise.reject(new Error('unused'))),
    refreshReadiness: vi.fn(() => Promise.reject(new Error('unused'))),
    pickWorkspaceDirectory: vi.fn(() => Promise.resolve({ path: null })),
    addWorkspaceRoot: vi.fn(() => Promise.reject(new Error('unused'))),
    removeWorkspaceRoot: vi.fn(() => Promise.reject(new Error('unused'))),
    reorderWorkspaceRoots: vi.fn(() => Promise.reject(new Error('unused'))),
    initRepository: vi.fn(() => Promise.reject(new Error('unused'))),
    listRepositories: vi.fn(() => Promise.resolve([])),
    listFeatures: vi.fn(() => Promise.resolve([])),
    getFeature: vi.fn(() => Promise.resolve(snapshot())),
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
        defaults: { models: [], effort: [], useCurrentBranch: false },
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
  error?: { code: string; message: string };
}

const validInput = {
  name: 'Search revamp',
  description: '',
  repoKeys: ['repo-a'],
  useCurrentBranch: false,
};

describe('feature IPC security', () => {
  it('rejects untrusted senders on every feature/creation channel', async () => {
    const { handlers, services } = register();
    for (const channel of [
      IPC_CHANNELS.featuresList,
      IPC_CHANNELS.featuresGet,
      IPC_CHANNELS.featuresCreate,
      IPC_CHANNELS.featuresSetup,
      IPC_CHANNELS.featuresDispatchAction,
      IPC_CHANNELS.sessionsList,
      IPC_CHANNELS.sessionsGet,
      IPC_CHANNELS.sessionsTranscript,
      IPC_CHANNELS.sessionsOutputOpen,
      IPC_CHANNELS.sessionsOutputCancel,
      IPC_CHANNELS.creationDefaults,
    ]) {
      const result = (await handlers.get(channel)!(foreignEvent, validInput)) as Envelope;
      expect(result.ok).toBe(false);
      expect(result.error?.code).toBe('E_UNTRUSTED_SENDER');
    }
    expect(services.createFeature).not.toHaveBeenCalled();
    expect(services.getFeature).not.toHaveBeenCalled();
    expect(services.dispatchFeatureSetup).not.toHaveBeenCalled();
  });

  it('rejects broad feature actions and global cursors on session operations', async () => {
    const { handlers, services } = register();
    for (const action of ['delete', 'publish', 'merge', '../start']) {
      const result = (await handlers.get(IPC_CHANNELS.featuresDispatchAction)!(goodEvent, {
        featureId: 'abcd1234ef567890',
        action,
      })) as Envelope;
      expect(result.error?.code).toBe('E_SCHEMA_MISMATCH');
    }
    const cursorResult = (await handlers.get(IPC_CHANNELS.sessionsOutputOpen)!(goodEvent, {
      sessionId: 'session-1',
      epoch: 'global',
      seq: 9,
    })) as Envelope;
    expect(cursorResult.error?.code).toBe('E_SCHEMA_MISMATCH');
    expect(services.dispatchFeatureAction).not.toHaveBeenCalled();
    expect(services.openSessionOutput).not.toHaveBeenCalled();
  });

  it('rejects invalid creation input at the schema layer before any service runs', async () => {
    const { handlers, services } = register();
    const create = handlers.get(IPC_CHANNELS.featuresCreate)!;
    for (const bad of [
      { ...validInput, name: '' },
      { ...validInput, name: '   ' },
      { ...validInput, repoKeys: [] },
      { ...validInput, extra: 'field' },
      { ...validInput, useCurrentBranch: 'yes' },
      'not-an-object',
    ]) {
      const result = (await create(goodEvent, bad)) as Envelope;
      expect(result.ok).toBe(false);
      expect(result.error?.code).toBe('E_SCHEMA_MISMATCH');
    }
    expect(services.createFeature).not.toHaveBeenCalled();
  });

  it('rejects feature ids that could smuggle path segments', async () => {
    const { handlers, services } = register();
    for (const channel of [IPC_CHANNELS.featuresGet, IPC_CHANNELS.featuresSetup]) {
      for (const bad of ['../other', 'id/with/slash', 'id?a=1', 'id#f', '', 'id name']) {
        const result = (await handlers.get(channel)!(goodEvent, bad)) as Envelope;
        expect(result.ok).toBe(false);
        expect(result.error?.code).toBe('E_SCHEMA_MISMATCH');
      }
    }
    expect(services.getFeature).not.toHaveBeenCalled();
    expect(services.dispatchFeatureSetup).not.toHaveBeenCalled();
  });

  it('rejects review-feedback launches carrying comment payloads — dispatch is constant-size', async () => {
    const { handlers, services } = register();
    const huge = {
      parentId: 'abcd1234ef567890',
      expectedRevision: 7,
      comments: [{ repo: 'repo-a', id: 1, type: 'review', body: 'x'.repeat(128 * 1024) }],
    };
    const result = (await handlers.get(IPC_CHANNELS.featuresReviewFeedbackLaunch)!(
      goodEvent,
      huge,
    )) as Envelope;
    expect(result.ok).toBe(false);
    expect(result.error?.code).toBe('E_SCHEMA_MISMATCH');
    expect(services.launchReviewFeedbackChild).not.toHaveBeenCalled();

    // The schema-valid launch carries only identity, revision, and the gate.
    const valid = { parentId: 'abcd1234ef567890', expectedRevision: 7, gate: true };
    expect(JSON.stringify(valid).length).toBeLessThan(256);
    await handlers.get(IPC_CHANNELS.featuresReviewFeedbackLaunch)!(goodEvent, valid);
    expect(services.launchReviewFeedbackChild).toHaveBeenCalledWith(valid);
  });

  it('rejects review-feedback selection updates carrying comment content', async () => {
    const { handlers, services } = register();
    for (const bad of [
      {
        featureId: 'abcd1234ef567890',
        expectedRevision: 7,
        updates: [{ stableRef: 'repo-a:review:41', selected: true, body: 'smuggled' }],
      },
      {
        featureId: 'abcd1234ef567890',
        expectedRevision: 7,
        updates: [{ stableRef: 'repo-a:review:41' }],
      },
      { featureId: 'abcd1234ef567890', expectedRevision: 7, updates: [] },
    ]) {
      const result = (await handlers.get(IPC_CHANNELS.featuresReviewFeedbackSelection)!(
        goodEvent,
        bad,
      )) as Envelope;
      expect(result.ok).toBe(false);
      expect(result.error?.code).toBe('E_SCHEMA_MISMATCH');
    }
    expect(services.updateReviewFeedbackSelection).not.toHaveBeenCalled();
  });

  it('fails closed when a feature snapshot carries token-shaped fields', async () => {
    const services = makeServices({
      getFeature: vi.fn(() => Promise.resolve({ ...snapshot(), authToken: 'tok-leak-9' } as never)),
    });
    const { handlers } = register(services);
    const result = (await handlers.get(IPC_CHANNELS.featuresGet)!(
      goodEvent,
      'abcd1234ef567890',
    )) as Envelope;
    expect(result.ok).toBe(false);
    expect(result.error?.code).toBe('E_SCHEMA_MISMATCH');
    expect(JSON.stringify(result)).not.toContain('tok-leak-9');
  });

  it('fails closed on prototype-polluting creation payloads', async () => {
    const { handlers, services } = register();
    const payload = JSON.parse(
      '{"name":"x","description":"","repoKeys":["r"],"useCurrentBranch":false,"__proto__":{"polluted":true}}',
    );
    const result = (await handlers.get(IPC_CHANNELS.featuresCreate)!(
      goodEvent,
      payload,
    )) as Envelope;
    expect(result.ok).toBe(false);
    expect(result.error?.code).toBe('E_UNSAFE_PAYLOAD');
    expect(services.createFeature).not.toHaveBeenCalled();
  });

  it('rejects a shell settings patch carrying feature/domain state', async () => {
    const { handlers, services } = register();
    const update = handlers.get(IPC_CHANNELS.settingsUpdate)!;
    for (const shell of [
      // Domain fields beyond identity/presentation.
      { activeFeatureId: null, sidebarCollapsed: false, status: 'Created' },
      // Identity that is not a confined feature id.
      { activeFeatureId: '../etc', sidebarCollapsed: false },
    ]) {
      const result = (await update(goodEvent, { shell })) as Envelope;
      expect(result.ok).toBe(false);
      expect(result.error?.code).toBe('E_SCHEMA_MISMATCH');
    }
    expect(services.updateSettings).not.toHaveBeenCalled();
  });

  it('accepts a shell patch limited to identity and presentation', async () => {
    const { handlers } = register();
    const result = (await handlers.get(IPC_CHANNELS.settingsUpdate)!(goodEvent, {
      shell: {
        setActiveFeature: { serverKey: 'a'.repeat(64), featureId: 'abcd1234ef567890' },
        sidebarCollapsed: false,
      },
    })) as Envelope;
    expect(result.ok).toBe(true);
  });
});
