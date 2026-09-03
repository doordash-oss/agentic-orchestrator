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
 * Supervision of the app-owned bundled server child. The child is always
 * spawned with an argv array (never a shell string), not detached, with
 * stdio piped into the bounded redacted log buffer. stop() requests
 * graceful termination (SIGTERM) and escalates to SIGKILL after a bound so
 * shutdown never hangs and never leaks a child. This class only ever
 * signals the process it spawned — externally owned servers are never
 * touched.
 */
import type { RedactedLogBuffer } from './logBuffer';

export interface ChildStreamLike {
  on(event: 'data', listener: (chunk: Buffer | string) => void): unknown;
}

export interface ChildProcessLike {
  pid?: number | undefined;
  stdout: ChildStreamLike | null;
  stderr: ChildStreamLike | null;
  on(
    event: 'exit',
    listener: (code: number | null, signal: NodeJS.Signals | null) => void,
  ): unknown;
  kill(signal?: NodeJS.Signals): boolean;
}

export interface SpawnOptionsLike {
  detached: boolean;
  shell: false;
  stdio: ['ignore', 'pipe', 'pipe'];
  windowsHide: boolean;
}

export type SpawnLike = (
  file: string,
  args: readonly string[],
  options: SpawnOptionsLike,
) => ChildProcessLike;

export interface ChildExit {
  code: number | null;
  signal: NodeJS.Signals | null;
  /** True when the exit was requested by stop() — not a crash. */
  expected: boolean;
}

export interface LaunchOptions {
  binaryPath: string;
  args: readonly string[];
  spawn: SpawnLike;
  log: RedactedLogBuffer;
}

export interface StopOptions {
  /** Grace period before escalating to SIGKILL. */
  timeoutMs?: number;
}

// The bundled Go server gives each managed provider process up to two
// five-second stages (graceful EOF, then SIGTERM) before it escalates to
// SIGKILL. Leave enough room for that cleanup to reap the whole provider
// process group before Electron is allowed to kill the server itself.
export const DEFAULT_STOP_TIMEOUT_MS = 15_000;

export class ManagedServerProcess {
  private readonly listeners = new Set<(info: ChildExit) => void>();
  private exitedFlag = false;
  private stopping = false;
  private readonly exitPromise: Promise<void>;

  private constructor(private readonly child: ChildProcessLike) {
    this.exitPromise = new Promise<void>((resolve) => {
      child.on('exit', (code, signal) => {
        if (this.exitedFlag) {
          return;
        }
        this.exitedFlag = true;
        const info: ChildExit = { code, signal, expected: this.stopping };
        for (const listener of [...this.listeners]) {
          listener(info);
        }
        resolve();
      });
    });
  }

  static launch(options: LaunchOptions): ManagedServerProcess {
    // argv array + shell:false — arguments are never shell-interpolated,
    // so paths with spaces, quotes, or non-ASCII characters are safe.
    const child = options.spawn(options.binaryPath, [...options.args], {
      detached: false,
      shell: false,
      stdio: ['ignore', 'pipe', 'pipe'],
      windowsHide: true,
    });
    const managed = new ManagedServerProcess(child);
    child.stdout?.on('data', (chunk) => options.log.append(chunk.toString()));
    child.stderr?.on('data', (chunk) => options.log.append(chunk.toString()));
    return managed;
  }

  get pid(): number | undefined {
    return this.child.pid;
  }

  get exited(): boolean {
    return this.exitedFlag;
  }

  onExit(listener: (info: ChildExit) => void): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  /**
   * Graceful, bounded termination of the app-owned child: SIGTERM, wait up
   * to timeoutMs, then SIGKILL and reap. Resolves once the child exited.
   */
  async stop(options: StopOptions = {}): Promise<void> {
    if (this.exitedFlag) {
      return;
    }
    this.stopping = true;
    this.child.kill('SIGTERM');
    const graceful = await this.waitForExit(options.timeoutMs ?? DEFAULT_STOP_TIMEOUT_MS);
    if (graceful) {
      return;
    }
    this.child.kill('SIGKILL');
    await this.exitPromise;
  }

  private waitForExit(timeoutMs: number): Promise<boolean> {
    return new Promise<boolean>((resolve) => {
      const timer = setTimeout(() => resolve(false), timeoutMs);
      void this.exitPromise.then(() => {
        clearTimeout(timer);
        resolve(true);
      });
    });
  }
}
