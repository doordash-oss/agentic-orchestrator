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
