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

import { describe, expect, it } from 'vitest';

import { reapTrackedProcesses, trackProcess, untrackProcess } from './appTermination.mjs';

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
