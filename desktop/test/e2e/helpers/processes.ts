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

import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

export function collectProcessTree(rootPid: number): number[] {
  const collected = new Set<number>([rootPid]);
  const queue = [rootPid];
  for (const pid of queue) {
    let out = '';
    try {
      out = execFileSync('pgrep', ['-P', String(pid)], { encoding: 'utf8' });
    } catch {
      continue;
    }
    for (const line of out.split('\n')) {
      const childPid = Number(line.trim());
      if (Number.isInteger(childPid) && childPid > 0 && !collected.has(childPid)) {
        collected.add(childPid);
        queue.push(childPid);
      }
    }
  }
  return [...collected];
}

export function killProcessTree(rootPid: number | undefined): void {
  if (rootPid === undefined) {
    return;
  }
  const pids = collectProcessTree(rootPid);
  for (const pid of pids.reverse()) {
    try {
      process.kill(pid, 'SIGKILL');
    } catch {
      // Best-effort cleanup. Leak assertions classify live failures.
    }
  }
}

const PROBE_TIMEOUT_MS = 10_000;

/** Runs a diagnostic tool and returns whatever it produced; a failure is text, never a throw. */
function probe(command: string, args: string[], timeoutMs = PROBE_TIMEOUT_MS): string {
  try {
    return execFileSync(command, args, {
      encoding: 'utf8',
      timeout: timeoutMs,
      stdio: ['ignore', 'pipe', 'pipe'],
    });
  } catch (error) {
    const failure = error as { code?: string; message?: string; stdout?: string; stderr?: string };
    if (failure.code === 'ENOENT') {
      return `(${command} not available)`;
    }
    return [
      failure.stdout ?? '',
      failure.stderr ?? '',
      `(${command} failed: ${failure.message ?? String(error)})`,
    ]
      .filter((part) => part !== '')
      .join('\n');
  }
}

function linuxThreadStates(pid: number): string {
  const taskDir = `/proc/${String(pid)}/task`;
  let tids: string[];
  try {
    tids = fs.readdirSync(taskDir);
  } catch (error) {
    return `(cannot read ${taskDir}: ${error instanceof Error ? error.message : String(error)})`;
  }
  return tids
    .map((tid) => {
      const read = (name: string): string => {
        try {
          return fs.readFileSync(path.join(taskDir, tid, name), 'utf8').trim();
        } catch {
          return '?';
        }
      };
      const stat = read('stat');
      const state = stat.slice(stat.lastIndexOf(')') + 2).split(' ')[0] ?? '?';
      return `${tid.padStart(8)} ${state} ${read('comm').padEnd(16)} wchan=${read('wchan')}`;
    })
    .join('\n');
}

/**
 * Best-effort snapshot of a process that stopped answering: its live process
 * tree with states and wait channels, per-thread kernel states (Linux), and a
 * native stack of the main thread (gdb on Linux, `sample` on macOS) when the
 * tool is present. Every probe is bounded and reports its own failure, so
 * teardown can always finish.
 */
export function captureProcessDiagnostics(rootPid: number): string {
  const tree = collectProcessTree(rootPid);
  const sections: Array<[string, string]> = [
    [
      'process tree',
      probe('ps', ['-o', 'pid,ppid,stat,etime,wchan,comm,args', '-p', tree.join(',')]),
    ],
  ];
  if (process.platform === 'linux') {
    sections.push(['threads', linuxThreadStates(rootPid)]);
    sections.push([
      'main thread native stack (gdb)',
      probe(
        'gdb',
        ['--batch', '-p', String(rootPid), '-ex', 'info threads', '-ex', 'thread apply 1 bt 40'],
        20_000,
      ),
    ]);
  } else if (process.platform === 'darwin') {
    sections.push([
      'native stack sample',
      probe('sample', [String(rootPid), '1', '-mayDie'], 20_000),
    ]);
  }
  return sections.map(([title, body]) => `## ${title}\n${body.trimEnd()}\n`).join('\n');
}

/** Returns live process IDs whose command lines reference an isolated journey root. */
export function worldProcessPIDs(worldRoot: string): number[] {
  try {
    return execFileSync('pgrep', ['-f', worldRoot], { encoding: 'utf8' })
      .split('\n')
      .map((value) => Number(value.trim()))
      .filter((pid) => Number.isInteger(pid) && pid > 0 && pid !== process.pid);
  } catch {
    return [];
  }
}
