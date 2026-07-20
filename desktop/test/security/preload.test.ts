import { beforeEach, describe, expect, it, vi } from 'vitest';

const exposeInMainWorld = vi.fn();
const invoke = vi.fn();
const on = vi.fn();
const removeListener = vi.fn();
const getPathForFile = vi.fn((file: File) => `/chosen/${file.name}`);

vi.mock('electron', () => ({
  contextBridge: { exposeInMainWorld },
  ipcRenderer: { invoke, on, removeListener },
  webUtils: { getPathForFile },
}));

describe('preload surface', () => {
  beforeEach(async () => {
    vi.clearAllMocks();
    vi.resetModules();
    await import('../../src/preload/index');
  });

  it('exposes a single agentico API with only task-specific operations', () => {
    expect(exposeInMainWorld).toHaveBeenCalledTimes(1);
    const [name, api] = exposeInMainWorld.mock.calls[0]!;
    expect(name).toBe('agentico');
    expect(Object.keys(api as object).sort()).toEqual(
      [
        'addWorkspaceRoot',
        'answerPermission',
        'answerQuestions',
        'createFeature',
        'dispatchFeatureSetup',
        'dispatchFeatureAction',
        'getConnectionStatus',
        'getAttention',
        'getCreationDefaults',
        'pickCreationFiles',
        'importDroppedCreationFiles',
        'searchCreationFiles',
        'cancelCreationFileSearch',
        'loadLocalReviewDraft',
        'saveLocalReviewDraft',
        'discardLocalReviewDraft',
        'getFeature',
        'getReadiness',
        'getSession',
        'getSessionTranscript',
        'getSettings',
        'getThemePreference',
        'initRepository',
        'listFeatures',
        'listRepositories',
        'listSessions',
        'onAppEvent',
        'onConnectionChanged',
        'onRouteRequest',
        'onSessionOutput',
        'openSessionOutput',
        'cancelSessionOutput',
        'pickWorkspaceDirectory',
        'removeWorkspaceRoot',
        'reorderWorkspaceRoots',
        'readReview',
        'openReview',
        'saveReview',
        'validateReview',
        'decideReview',
        'listResources',
        'readResource',
        'validateResource',
        'writeResource',
        'loadLocalResourceDraft',
        'saveLocalResourceDraft',
        'discardLocalResourceDraft',
        'refreshReadiness',
        'resolveGate',
        'retryConnection',
        'restartConnection',
        'saveGateDraft',
        'sendHelp',
        'setThemePreference',
        'startChat',
        'endChat',
        'updateSettings',
        'listRuns',
        'getRun',
        'listRunSessions',
        'getLivePreview',
        'listRunArtifacts',
        'getRunArtifactContent',
        'getRunLogContent',
        'getRewindPreview',
        'executeRewind',
        'preflightCompletion',
        'getRepositoryDiff',
        'generatePublishDescription',
        'openExternal',
        'revealPath',
        'startRebase',
        'preflightRebase',
        'fetchReviewComments',
        'startReviewComments',
        'startRefactor',
        'preflightRefactor',
        'scanRecovery',
        'executeRecovery',
        'readRecoveryLog',
        'bulkPreview',
        'getUpdates',
        'checkForUpdates',
        'installUpdateWhenIdle',
        'installUpdateNow',
        'restartToUpdate',
        'getDiagnostics',
        'revealDiagnostics',
        'clearDiagnostics',
      ].sort(),
    );
  });

  it('exposes no generic invoke, send, require, or process handles', () => {
    const api = exposeInMainWorld.mock.calls[0]![1] as Record<string, unknown>;
    for (const forbidden of ['invoke', 'send', 'sendSync', 'require', 'process', 'ipcRenderer']) {
      expect(api[forbidden]).toBeUndefined();
    }
  });

  it('maps only explicit dropped image File objects through the narrow webUtils seam', () => {
    const api = exposeInMainWorld.mock.calls[0]![1] as {
      importDroppedCreationFiles(kind: 'image', files: File[]): { paths: string[] };
    };
    const result = api.importDroppedCreationFiles('image', [
      new File(['ok'], 'screen.png', { type: 'image/png' }),
      new File(['no'], 'notes.txt', { type: 'text/plain' }),
    ]);
    expect(result.paths).toEqual(['/chosen/screen.png']);
    expect(getPathForFile).toHaveBeenCalledTimes(1);
  });

  it('unwraps ok envelopes returned by main', async () => {
    const api = exposeInMainWorld.mock.calls[0]![1] as {
      getConnectionStatus(): Promise<unknown>;
    };
    invoke.mockResolvedValueOnce({
      ok: true,
      value: { status: 'idle', stage: 'resolve-runtime', detail: '' },
    });
    await expect(api.getConnectionStatus()).resolves.toEqual({
      status: 'idle',
      stage: 'resolve-runtime',
      detail: '',
    });
    expect(invoke).toHaveBeenCalledWith('agentico:connection:get-status');
  });

  it('rejects with the safe error message on error envelopes', async () => {
    const api = exposeInMainWorld.mock.calls[0]![1] as {
      getSettings(): Promise<unknown>;
    };
    invoke.mockResolvedValueOnce({
      ok: false,
      error: { code: 'E_INTERNAL', message: 'nope', remediation: 'retry' },
    });
    await expect(api.getSettings()).rejects.toThrow(/E_INTERNAL/);
  });

  it('fails closed when main returns a malformed envelope', async () => {
    const api = exposeInMainWorld.mock.calls[0]![1] as {
      getSettings(): Promise<unknown>;
    };
    invoke.mockResolvedValueOnce({ unexpected: true });
    await expect(api.getSettings()).rejects.toThrow(/E_IPC_PROTOCOL/);
  });

  it('validates pushed connection events and supports unsubscribe', () => {
    const api = exposeInMainWorld.mock.calls[0]![1] as {
      onConnectionChanged(cb: (s: unknown) => void): () => void;
    };
    const cb = vi.fn();
    const unsubscribe = api.onConnectionChanged(cb);
    expect(on).toHaveBeenCalledWith('agentico:connection:changed', expect.any(Function));
    const listener = on.mock.calls[0]![1] as (event: unknown, payload: unknown) => void;

    const validState = { status: 'ready', stage: 'ready', detail: 'ok', ownership: 'external' };
    listener({}, validState);
    expect(cb).toHaveBeenCalledWith(validState);

    cb.mockClear();
    listener({}, { ...validState, bearerToken: 'x' });
    listener({}, { ...validState, authToken: 'x' });
    listener({}, JSON.parse('{"__proto__": {}, "status": "ready"}'));
    expect(cb).not.toHaveBeenCalled();

    unsubscribe();
    expect(removeListener).toHaveBeenCalledWith(
      'agentico:connection:changed',
      expect.any(Function),
    );
  });

  it('validates pushed app events fail-closed and never forwards foreign fields', () => {
    const api = exposeInMainWorld.mock.calls[0]![1] as {
      onAppEvent(cb: (e: unknown) => void): () => void;
    };
    const cb = vi.fn();
    const unsubscribe = api.onAppEvent(cb);
    expect(on).toHaveBeenCalledWith('agentico:events:app', expect.any(Function));
    const listener = on.mock.calls[0]![1] as (event: unknown, payload: unknown) => void;

    const valid = { type: 'invalidated', kind: 'feature.updated', featureId: 'abcd1234' };
    listener({}, valid);
    expect(cb).toHaveBeenCalledWith(valid);

    cb.mockClear();
    // Domain payloads, token-shaped fields, and polluted objects are dropped.
    listener({}, { ...valid, summary: 'setup finished' });
    listener({}, { ...valid, token: 'tok-leak' });
    listener({}, { type: 'invalidated', kind: 'bad kind with spaces' });
    listener({}, JSON.parse('{"__proto__": {}, "type": "invalidated", "kind": "x"}'));
    listener({}, { type: 'status', stream: 'exploded' });
    expect(cb).not.toHaveBeenCalled();

    listener({}, { type: 'status', stream: 'stale' });
    expect(cb).toHaveBeenCalledWith({ type: 'status', stream: 'stale' });

    unsubscribe();
    expect(removeListener).toHaveBeenCalledWith('agentico:events:app', expect.any(Function));
  });

  it('validates route requests fail-closed', () => {
    const api = exposeInMainWorld.mock.calls[0]![1] as {
      onRouteRequest(cb: (e: unknown) => void): () => void;
    };
    const cb = vi.fn();
    const unsubscribe = api.onRouteRequest(cb);
    expect(on).toHaveBeenCalledWith('agentico:route:requested', expect.any(Function));
    const listener = on.mock.calls.find(
      ([channel]) => channel === 'agentico:route:requested',
    )?.[1] as (event: unknown, payload: unknown) => void;

    listener({}, { target: 'ama' });
    expect(cb).toHaveBeenCalledWith({ target: 'ama' });

    cb.mockClear();
    listener({}, { target: 'settings', settingsSection: 'updates' });
    expect(cb).toHaveBeenCalledWith({ target: 'settings', settingsSection: 'updates' });

    cb.mockClear();
    listener({}, { target: 'shell' });
    listener({}, { target: 'settings', settingsSection: 'secrets' });
    listener({}, { target: 'attention', token: 'tok-leak' });
    listener({}, JSON.parse('{"__proto__": {}, "target": "home"}'));
    expect(cb).not.toHaveBeenCalled();

    unsubscribe();
    expect(removeListener).toHaveBeenCalledWith('agentico:route:requested', expect.any(Function));
  });

  it('validates session output events and removes the exact listener on cleanup', () => {
    const api = exposeInMainWorld.mock.calls[0]![1] as {
      onSessionOutput(cb: (event: unknown) => void): () => void;
    };
    const cb = vi.fn();
    const unsubscribe = api.onSessionOutput(cb);
    expect(on).toHaveBeenCalledWith('agentico:sessions:output', expect.any(Function));
    const listener = on.mock.calls.find(
      ([channel]) => channel === 'agentico:sessions:output',
    )?.[1] as (event: unknown, payload: unknown) => void;
    const valid = {
      subscriptionId: 'sub-1',
      type: 'record',
      sessionId: 'session-1',
      index: 4,
      message: { index: 4, role: 'assistant', type: 'text', text: '<script>x</script>' },
    };
    listener({}, valid);
    expect(cb).toHaveBeenCalledWith(valid);
    cb.mockClear();
    listener({}, { ...valid, accessToken: 'leak' });
    listener(
      {},
      JSON.parse(
        '{"subscriptionId":"sub-1","type":"done","sessionId":"session-1","nextIndex":5,"__proto__":{}}',
      ),
    );
    expect(cb).not.toHaveBeenCalled();
    unsubscribe();
    expect(removeListener).toHaveBeenCalledWith('agentico:sessions:output', listener);
  });
});
