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
 * Off-main-thread hard exit guard for quit.
 *
 * The QuitCoordinator's shutdown bound and exit watchdog are main-thread
 * timers, so they cannot fire while the main thread is blocked in native
 * teardown or a synchronous loop. That is the state a hung quit leaves
 * behind: the app log ends mid-shutdown and no later stage is ever recorded.
 * This guard runs on a worker thread with its own event loop, so its deadline
 * fires regardless of what the main thread is doing. When it fires it first
 * stops the app-owned runtime child (SIGTERM, bounded, then SIGKILL) so the
 * forced exit never orphans the server, then kills the process.
 */
import { Worker } from 'node:worker_threads';

export interface HardExitGuardOptions {
  /** Delay before the guard ends the process. */
  timeoutMs: number;
  /** App-owned child pids to stop before the process is killed. */
  childPids: readonly number[];
  /** Grace period between SIGTERM and SIGKILL for each child. */
  childGraceMs?: number;
  log?(line: string): void;
}

export interface HardExitGuard {
  disarm(): void;
}

// Matches the bundled server's own SIGTERM budget for reaping provider
// process groups (see serverProcess.ts DEFAULT_STOP_TIMEOUT_MS).
export const DEFAULT_CHILD_GRACE_MS = 15_000;

/** The line the worker writes to stderr when it fires; the E2E harness keys on it. */
export const HARD_EXIT_MARKER = 'forcing exit off the main thread';

/**
 * Plain CommonJS for `new Worker(source, { eval: true })`: no bundler chunk,
 * no asar path, nothing that depends on the main thread once started. Writes
 * go straight to fd 2 because a worker's console is relayed through the main
 * thread, which is the thread presumed dead here.
 */
export const HARD_EXIT_WORKER_SOURCE = String.raw`
const { workerData } = require('node:worker_threads');
const fs = require('node:fs');
const { execFileSync } = require('node:child_process');
const { pid, timeoutMs, childPids, childGraceMs, marker } = workerData;

function write(line) {
  try {
    fs.writeSync(2, '[agentico-quit] ' + line + '\n');
  } catch {
    // stderr gone: nothing left to tell.
  }
}

// [state, ppid] or null when the process cannot be inspected.
function inspect(target) {
  try {
    if (process.platform === 'linux') {
      const stat = fs.readFileSync('/proc/' + target + '/stat', 'utf8');
      const fields = stat.slice(stat.lastIndexOf(')') + 2).split(' ');
      return [fields[0], Number(fields[1])];
    }
    if (process.platform === 'darwin') {
      const out = execFileSync('ps', ['-o', 'stat=,ppid=', '-p', String(target)], {
        encoding: 'utf8',
      });
      const fields = out.trim().split(/\s+/);
      return [fields[0] || '', Number(fields[1])];
    }
  } catch {
    // Exited, or no inspection surface on this platform.
  }
  return null;
}

// A pid recorded at arm time may have been reaped and reused since; only a
// process whose parent is still this one is signalled.
function owned(target) {
  const info = inspect(target);
  return info !== null && info[1] === pid;
}

// A SIGTERMed child of a wedged parent lingers as a zombie: exited, but the
// parent never reaped it. kill(pid, 0) still succeeds on it, so the state
// decides.
function running(target) {
  const info = inspect(target);
  return info !== null && !info[0].startsWith('Z');
}

function signal(target, sig) {
  try {
    process.kill(target, sig);
  } catch {
    // Already gone.
  }
}

setTimeout(() => {
  const children = childPids.filter(owned);
  write(
    'process still alive ' + timeoutMs + 'ms after shutdown began; ' + marker +
      ' (owned children: ' + (children.length === 0 ? 'none' : children.join(', ')) + ')',
  );
  for (const target of children) signal(target, 'SIGTERM');
  const deadline = Date.now() + childGraceMs;
  const finish = () => {
    for (const target of children) signal(target, 'SIGKILL');
    signal(pid, 'SIGKILL');
  };
  const poll = () => {
    if (!children.some(running) || Date.now() >= deadline) {
      finish();
      return;
    }
    setTimeout(poll, 100);
  };
  poll();
}, timeoutMs);
`;

export function armHardExitGuard(options: HardExitGuardOptions): HardExitGuard {
  const worker = new Worker(HARD_EXIT_WORKER_SOURCE, {
    eval: true,
    workerData: {
      pid: process.pid,
      timeoutMs: options.timeoutMs,
      childPids: [...options.childPids],
      childGraceMs: options.childGraceMs ?? DEFAULT_CHILD_GRACE_MS,
      marker: HARD_EXIT_MARKER,
    },
  });
  worker.on('error', (error: unknown) => {
    options.log?.(
      `hard exit guard failed: ${error instanceof Error ? error.message : String(error)}`,
    );
  });
  // A normal quit must never wait on the guard.
  worker.unref();
  return {
    disarm: () => {
      void worker.terminate();
    },
  };
}
