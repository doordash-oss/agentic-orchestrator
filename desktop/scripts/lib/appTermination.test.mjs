import { describe, expect, it } from 'vitest';

import {
  cleanShutdownFailure,
  reapTrackedProcesses,
  terminateElectronApp,
  trackProcess,
  untrackProcess,
} from './appTermination.mjs';

/**
 * Minimal stand-in for a Playwright ElectronApplication process handle
 * (a Node ChildProcess). `dieOn` lists the signals the fake honors, so a
 * quit-guard hang is modeled by omitting SIGTERM.
 */
function fakeHandle({ dieOn = ['SIGTERM', 'SIGKILL'] } = {}) {
  const exitListeners = [];
  return {
    exitCode: null,
    signalCode: null,
    killed: [],
    kill(signal) {
      this.killed.push(signal);
      if (dieOn.includes(signal) && this.signalCode === null && this.exitCode === null) {
        this.signalCode = signal;
        for (const listener of exitListeners.splice(0)) listener();
      }
    },
    once(event, listener) {
      if (event === 'exit') exitListeners.push(listener);
    },
    exitCleanly() {
      if (this.signalCode === null && this.exitCode === null) {
        this.exitCode = 0;
        for (const listener of exitListeners.splice(0)) listener();
      }
    },
  };
}

const fastTimeouts = { closeTimeoutMs: 20, sigtermTimeoutMs: 20, sigkillTimeoutMs: 20 };

describe('terminateElectronApp', () => {
  it('reports a graceful shutdown when close() exits the process without signals', async () => {
    const handle = fakeHandle();
    const app = {
      close: async () => {
        handle.exitCleanly();
      },
    };

    const result = await terminateElectronApp(app, handle, fastTimeouts);

    expect(result.method).toBe('graceful');
    expect(handle.killed).toEqual([]);
  });

  it('escalates to SIGTERM when close() hangs (quit guard blocking)', async () => {
    const handle = fakeHandle();
    const app = { close: () => new Promise(() => {}) };

    const result = await terminateElectronApp(app, handle, fastTimeouts);

    expect(result.method).toBe('sigterm');
    expect(handle.killed).toEqual(['SIGTERM']);
    expect(handle.signalCode).toBe('SIGTERM');
  });

  it('escalates to SIGKILL when SIGTERM is also ignored', async () => {
    const handle = fakeHandle({ dieOn: ['SIGKILL'] });
    const app = { close: () => new Promise(() => {}) };

    const result = await terminateElectronApp(app, handle, fastTimeouts);

    expect(result.method).toBe('sigkill');
    expect(handle.killed).toEqual(['SIGTERM', 'SIGKILL']);
    expect(handle.signalCode).toBe('SIGKILL');
  });

  it('treats a close() rejection like a hang and still kills the process', async () => {
    const handle = fakeHandle();
    const app = {
      close: async () => {
        throw new Error('target closed');
      },
    };

    const result = await terminateElectronApp(app, handle, fastTimeouts);

    expect(result.method).toBe('sigterm');
    expect(handle.signalCode).toBe('SIGTERM');
  });
});

describe('process reaper', () => {
  it('SIGKILLs every still-live tracked process and clears the registry', () => {
    const live = fakeHandle({ dieOn: ['SIGKILL'] });
    const exited = fakeHandle();
    exited.exitCleanly();
    const untracked = fakeHandle();
    trackProcess(live);
    trackProcess(exited);
    trackProcess(untracked);
    untrackProcess(untracked);

    reapTrackedProcesses();

    expect(live.killed).toEqual(['SIGKILL']);
    expect(exited.killed).toEqual([]);
    expect(untracked.killed).toEqual([]);

    reapTrackedProcesses();
    expect(live.killed).toEqual(['SIGKILL']);
  });
});

describe('cleanShutdownFailure', () => {
  it('is null for a graceful shutdown', () => {
    expect(cleanShutdownFailure('graceful')).toBeNull();
  });

  it('explains the quit-guard leak for forced shutdowns', () => {
    const message = cleanShutdownFailure('sigkill');
    expect(message).toContain('did not quit cleanly');
    expect(message).toContain('sigkill');
  });
});
