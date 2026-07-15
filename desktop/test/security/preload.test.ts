import { beforeEach, describe, expect, it, vi } from 'vitest';

const exposeInMainWorld = vi.fn();
const invoke = vi.fn();
const on = vi.fn();
const removeListener = vi.fn();

vi.mock('electron', () => ({
  contextBridge: { exposeInMainWorld },
  ipcRenderer: { invoke, on, removeListener },
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
    expect(Object.keys(api as object).sort()).toEqual([
      'addWorkspaceRoot',
      'getConnectionStatus',
      'getReadiness',
      'getSettings',
      'getThemePreference',
      'initRepository',
      'listRepositories',
      'onConnectionChanged',
      'pickWorkspaceDirectory',
      'refreshReadiness',
      'retryConnection',
      'setThemePreference',
      'updateSettings',
    ]);
  });

  it('exposes no generic invoke, send, require, or process handles', () => {
    const api = exposeInMainWorld.mock.calls[0]![1] as Record<string, unknown>;
    for (const forbidden of ['invoke', 'send', 'sendSync', 'require', 'process', 'ipcRenderer']) {
      expect(api[forbidden]).toBeUndefined();
    }
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
});
