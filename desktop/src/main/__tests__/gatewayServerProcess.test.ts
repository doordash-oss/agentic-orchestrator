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

import { EventEmitter } from 'node:events';
import { describe, expect, it, vi } from 'vitest';
import { RedactedLogBuffer } from '../gateway/logBuffer';
import {
  DEFAULT_STOP_TIMEOUT_MS,
  ManagedServerProcess,
  type ChildProcessLike,
} from '../gateway/serverProcess';

class FakeChild extends EventEmitter implements ChildProcessLike {
  pid: number | undefined = 1234;
  stdout = new EventEmitter();
  stderr = new EventEmitter();
  killed: NodeJS.Signals[] = [];
  /** When true, SIGTERM is ignored so stop() must escalate to SIGKILL. */
  ignoreSigterm = false;

  kill(signal?: NodeJS.Signals): boolean {
    const sig = signal ?? 'SIGTERM';
    this.killed.push(sig);
    if (sig === 'SIGKILL') {
      setImmediate(() => this.emit('exit', null, 'SIGKILL'));
      return true;
    }
    if (!this.ignoreSigterm) {
      setImmediate(() => this.emit('exit', 0, null));
    }
    return true;
  }
}

function launch(child: FakeChild = new FakeChild()) {
  const spawn = vi.fn(() => child);
  const log = new RedactedLogBuffer(50);
  const managed = ManagedServerProcess.launch({
    binaryPath: '/res dir/bín/agentico',
    args: ['server', '--config', '/c.yaml', '--state-dir', '/state dir/features'],
    spawn,
    log,
  });
  return { managed, child, spawn, log };
}

describe('ManagedServerProcess', () => {
  it('spawns with an argv array, no shell, not detached, piped stdio', () => {
    const { spawn } = launch();
    expect(spawn).toHaveBeenCalledTimes(1);
    const [file, args, options] = spawn.mock.calls[0]! as unknown as [
      string,
      readonly string[],
      Record<string, unknown>,
    ];
    expect(file).toBe('/res dir/bín/agentico');
    expect(args).toEqual(['server', '--config', '/c.yaml', '--state-dir', '/state dir/features']);
    expect(options['shell']).toBe(false);
    expect(options['detached']).toBe(false);
    expect(options['stdio']).toEqual(['ignore', 'pipe', 'pipe']);
  });

  it('captures stdout and stderr into the redacted log buffer', () => {
    const { child, log } = launch();
    child.stdout.emit('data', Buffer.from('booting with Bearer abc123token\n'));
    child.stderr.emit('data', 'warn: something\n');
    const lines = log.snapshot().join('\n');
    expect(lines).toContain('booting');
    expect(lines).toContain('warn: something');
    expect(lines).not.toContain('abc123token');
  });

  it('notifies exit listeners with expectedness and supports unsubscribe', async () => {
    const { managed, child } = launch();
    const seen: Array<{ expected: boolean }> = [];
    const unsubscribe = managed.onExit((info) => seen.push({ expected: info.expected }));
    child.emit('exit', 1, null);
    expect(managed.exited).toBe(true);
    expect(seen).toEqual([{ expected: false }]);
    unsubscribe();
    child.emit('exit', 1, null);
    expect(seen).toHaveLength(1);
  });

  it('stop() sends SIGTERM and resolves once the child exits', async () => {
    const { managed, child } = launch();
    await managed.stop({ timeoutMs: 200 });
    expect(child.killed).toEqual(['SIGTERM']);
    expect(managed.exited).toBe(true);
  });

  it('leaves room for the bundled server to reap provider process groups', () => {
    // Session.Stop permits two five-second cleanup stages before the Go
    // server exits; Electron must not preempt that owner-side reaping.
    expect(DEFAULT_STOP_TIMEOUT_MS).toBeGreaterThan(10_000);
  });

  it('stop() escalates to SIGKILL after the grace period', async () => {
    const child = new FakeChild();
    child.ignoreSigterm = true;
    const { managed } = launch(child);
    await managed.stop({ timeoutMs: 20 });
    expect(child.killed).toEqual(['SIGTERM', 'SIGKILL']);
    expect(managed.exited).toBe(true);
  });

  it('an exit during stop() is reported as expected (not a crash)', async () => {
    const { managed, child } = launch();
    const seen: boolean[] = [];
    managed.onExit((info) => seen.push(info.expected));
    await managed.stop({ timeoutMs: 200 });
    expect(seen).toEqual([true]);
    expect(child.killed).toEqual(['SIGTERM']);
  });

  it('stop() after exit is a no-op and never signals again', async () => {
    const { managed, child } = launch();
    child.emit('exit', 0, null);
    await managed.stop({ timeoutMs: 20 });
    expect(child.killed).toEqual([]);
  });
});
