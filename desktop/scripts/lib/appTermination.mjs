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
 * Bounded termination for Playwright-launched Electron apps plus a
 * last-resort reaper, so automation can never leak live app windows: a
 * graceful close is raced against a timeout, then escalated SIGTERM →
 * SIGKILL, and anything still tracked when the harness itself dies is
 * SIGKILLed from the process exit path.
 */

const trackedProcesses = new Set();
let reaperInstalled = false;

function hasExited(handle) {
  return handle.exitCode !== null || handle.signalCode !== null;
}

/** Registers a child process to be SIGKILLed if the harness exits first. */
export function trackProcess(handle) {
  trackedProcesses.add(handle);
}

/** Removes a process from the reaper registry after confirmed shutdown. */
export function untrackProcess(handle) {
  trackedProcesses.delete(handle);
}

/** SIGKILLs every tracked process that is still alive, then clears the set. */
export function reapTrackedProcesses() {
  for (const handle of trackedProcesses) {
    if (!hasExited(handle)) {
      try {
        handle.kill('SIGKILL');
      } catch {
        // already gone
      }
    }
  }
  trackedProcesses.clear();
}

/**
 * Hooks the reaper into harness shutdown: normal exit, Ctrl-C, and the
 * SIGTERM a CI timeout sends. Without this, killing the harness mid-run
 * orphans every launched app.
 */
export function installProcessReaper(proc = process) {
  if (reaperInstalled) {
    return;
  }
  reaperInstalled = true;
  proc.once('exit', reapTrackedProcesses);
  for (const signal of ['SIGINT', 'SIGTERM']) {
    proc.once(signal, () => {
      reapTrackedProcesses();
      proc.exit(1);
    });
  }
}
