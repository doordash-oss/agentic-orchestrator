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
 * Main-process fault handling.
 *
 * Electron's default `uncaughtException` handler shows the error in a
 * modal (`dialog.showErrorBox`). On Linux that modal is `gtk_dialog_run`, a
 * nested loop on the main thread that returns only when a person dismisses
 * it: the Node loop stops being pumped, so every timer, child-exit callback,
 * and inspector request freezes and the app looks hung. Headless, nobody
 * ever dismisses it. A fault is a bug to record and keep running past, not a
 * reason to stop the process's event loop, so this module owns both fault
 * events: it logs the stack, records a main-process crash in diagnostics,
 * and never opens a dialog. Electron skips its own handler when another
 * listener is installed.
 */

/** The narrow surface of `process` this module needs; injectable for tests. */
interface FaultProcess {
  on(event: 'uncaughtException', listener: (error: Error) => void): unknown;
  on(event: 'unhandledRejection', listener: (reason: unknown) => void): unknown;
}

interface FaultRecorder {
  recordCrash(input: { processRole: 'main'; category: string; context?: string }): unknown;
}

interface FaultHandlerDeps {
  process: FaultProcess;
  log(line: string): void;
}

interface FaultHandlers {
  /** Diagnostics is constructed after startup; faults before that are only logged. */
  setRecorder(recorder: FaultRecorder): void;
}

export const FAULT_LOG_PREFIX = '[agentico-main]';

/**
 * Prefix for the one uncaught exception that is tolerated rather than
 * recorded (see `isSocketTeardownRace`). Deliberately not a continuation of
 * FAULT_LOG_PREFIX: the E2E harness fails a journey on any line that starts
 * with the fault prefix.
 */
export const TOLERATED_LOG_PREFIX = '[agentico-main-tolerated]';

/**
 * Node's TCP read callback throws when bytes land on a socket that
 * JavaScript destroyed earlier in the same libuv poll pass: the read reports
 * a positive byte count on a destroyed stream, and `onStreamRead` hands that
 * count to `ErrnoException`, which only accepts a negative errno. The error
 * is thrown from inside libuv, so no application catch can see it, and it has
 * no consequence: the socket is already gone. The main process hits it while
 * the app-owned runtime is being stopped with event streams still connected.
 *
 * The match is deliberately narrow: the exact code and message, and a stack
 * made only of Node-internal frames with `getSystemErrorName` on top. A real
 * ERR_OUT_OF_RANGE thrown by a data handler also has `onStreamRead` beneath
 * it, but carries an application frame, and still counts as a fault.
 */
export function isSocketTeardownRace(fault: unknown): boolean {
  if (!(fault instanceof RangeError)) {
    return false;
  }
  if ((fault as { code?: unknown }).code !== 'ERR_OUT_OF_RANGE') {
    return false;
  }
  if (!fault.message.startsWith('The value of "err" is out of range.')) {
    return false;
  }
  const frames = (fault.stack ?? '').split('\n').filter((line) => /^\s+at /.test(line));
  const top = frames[0];
  if (top === undefined || !top.includes('getSystemErrorName')) {
    return false;
  }
  return (
    frames.some((frame) => frame.includes('ErrnoException')) &&
    frames.some((frame) => frame.includes('onStreamRead')) &&
    frames.every((frame) => frame.includes('(node:'))
  );
}

export function installMainProcessFaultHandlers(deps: FaultHandlerDeps): FaultHandlers {
  let recorder: FaultRecorder | null = null;
  const handle = (category: 'uncaught exception' | 'unhandled rejection', fault: unknown): void => {
    const detail = describeFault(fault);
    if (category === 'uncaught exception' && isSocketTeardownRace(fault)) {
      deps.log(`${TOLERATED_LOG_PREFIX} socket teardown race: ${detail}`);
      return;
    }
    deps.log(`${FAULT_LOG_PREFIX} ${category}: ${detail}`);
    try {
      recorder?.recordCrash({ processRole: 'main', category, context: detail });
    } catch (error: unknown) {
      deps.log(`${FAULT_LOG_PREFIX} recording the fault failed: ${describeFault(error)}`);
    }
  };
  deps.process.on('uncaughtException', (error) => handle('uncaught exception', error));
  deps.process.on('unhandledRejection', (reason) => handle('unhandled rejection', reason));
  return {
    setRecorder: (next) => {
      recorder = next;
    },
  };
}

export function describeFault(fault: unknown): string {
  if (fault instanceof Error) {
    return fault.stack ?? `${fault.name}: ${fault.message}`;
  }
  try {
    return typeof fault === 'string' ? fault : JSON.stringify(fault);
  } catch {
    return String(fault);
  }
}
