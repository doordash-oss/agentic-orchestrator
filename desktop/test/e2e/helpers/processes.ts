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
