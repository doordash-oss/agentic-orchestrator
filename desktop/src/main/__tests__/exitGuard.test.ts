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

import { spawn } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { HARD_EXIT_MARKER, HARD_EXIT_WORKER_SOURCE, armHardExitGuard } from '../exitGuard';

const posix = process.platform === 'linux' || process.platform === 'darwin';

function alive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

async function waitUntil(condition: () => boolean, timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (!condition()) {
    if (Date.now() > deadline) {
      throw new Error(`condition not met within ${String(timeoutMs)}ms`);
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
}

/**
 * A host process that spawns an owned child, arms the guard against itself,
 * then spins its main thread forever: no timer, exit handler, or console call
 * on that thread can ever run again, which is the state the guard exists for.
 */
function hostSource(timeoutMs: number, childGraceMs: number): string {
  return `
const { Worker } = require('node:worker_threads');
const { spawn } = require('node:child_process');
const fs = require('node:fs');
const child = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { stdio: 'ignore' });
fs.writeSync(1, String(child.pid) + '\\n');
new Worker(${JSON.stringify(HARD_EXIT_WORKER_SOURCE)}, {
  eval: true,
  workerData: {
    pid: process.pid,
    timeoutMs: ${String(timeoutMs)},
    childPids: [child.pid],
    childGraceMs: ${String(childGraceMs)},
    marker: ${JSON.stringify(HARD_EXIT_MARKER)},
  },
});
const end = Date.now() + 30_000;
while (Date.now() < end) {}
`;
}

describe('hard exit guard', () => {
  let tempDir: string | null = null;

  afterEach(() => {
    if (tempDir !== null) {
      fs.rmSync(tempDir, { recursive: true, force: true });
      tempDir = null;
    }
  });

  it.runIf(posix)(
    'ends a host whose main thread never runs another callback, stopping its owned child first',
    async () => {
      tempDir = fs.mkdtempSync(path.join(os.tmpdir(), 'agentico-exit-guard-'));
      const hostPath = path.join(tempDir, 'host.cjs');
      // The child grace is far longer than the test budget: the guard must
      // recognise the SIGTERMed child as finished (a zombie, since its wedged
      // parent never reaps it) instead of waiting the grace out.
      fs.writeFileSync(hostPath, hostSource(300, 20_000));
      const host = spawn(process.execPath, [hostPath], { stdio: ['ignore', 'pipe', 'pipe'] });
      let stdout = '';
      let stderr = '';
      host.stdout.on('data', (chunk: Buffer) => {
        stdout += chunk.toString();
      });
      host.stderr.on('data', (chunk: Buffer) => {
        stderr += chunk.toString();
      });
      const started = Date.now();
      const exit = await new Promise<{ code: number | null; signal: NodeJS.Signals | null }>(
        (resolve) => {
          host.on('exit', (code, signal) => resolve({ code, signal }));
        },
      );
      const elapsed = Date.now() - started;
      const childPid = Number(stdout.trim());

      expect(exit.signal).toBe('SIGKILL');
      expect(elapsed).toBeLessThan(10_000);
      expect(stderr).toContain(HARD_EXIT_MARKER);
      expect(stderr).toContain(`owned children: ${String(childPid)}`);
      // Reparented and reaped once the host is gone.
      await waitUntil(() => !alive(childPid), 5_000);
    },
    20_000,
  );

  it('never fires once disarmed', async () => {
    const guard = armHardExitGuard({ timeoutMs: 500, childPids: [] });
    guard.disarm();
    // Had the guard fired, this process would not be here to finish the wait.
    await new Promise((resolve) => setTimeout(resolve, 700));
    expect(alive(process.pid)).toBe(true);
  });
});
