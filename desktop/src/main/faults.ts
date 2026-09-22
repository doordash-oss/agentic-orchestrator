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
export interface FaultProcess {
  on(event: 'uncaughtException', listener: (error: Error) => void): unknown;
  on(event: 'unhandledRejection', listener: (reason: unknown) => void): unknown;
}

export interface FaultRecorder {
  recordCrash(input: { processRole: 'main'; category: string; context?: string }): unknown;
}

export interface FaultHandlerDeps {
  process: FaultProcess;
  log(line: string): void;
}

export interface FaultHandlers {
  /** Diagnostics is constructed after startup; faults before that are only logged. */
  setRecorder(recorder: FaultRecorder): void;
}

export const FAULT_LOG_PREFIX = '[agentico-main]';

export function installMainProcessFaultHandlers(deps: FaultHandlerDeps): FaultHandlers {
  let recorder: FaultRecorder | null = null;
  const handle = (category: 'uncaught exception' | 'unhandled rejection', fault: unknown): void => {
    const detail = describeFault(fault);
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
