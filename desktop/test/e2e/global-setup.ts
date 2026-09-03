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
 * Ensures exactly one fresh packaged build is reused by every journey.
 *
 * Freshness contract: dist/package-verification.json must exist, its
 * unpacked_app must exist, and the identity's server_revision must match the
 * current git HEAD and newer than any local source edit (a package built from
 * other code would silently test the wrong build). Anything else triggers one
 * `npm run package:verify:e2e`, which rebuilds and re-inspects the runnable
 * unpacked app. CI's distribution-package gate can still run independently;
 * journeys do not require hdiutil/FUSE just to launch Electron.
 */
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { desktopDir, readVerification } from './helpers/packaged';
import { collectProcessTree } from './helpers/processes';

export default function globalSetup(): void {
  sweepOrphanedE2eProcesses();
  // Electron 43 installs its runtime lazily on first import; workers launch
  // in parallel and must never race that extraction (idempotent when done).
  execFileSync('npm', ['run', 'install:electron-runtime'], {
    cwd: desktopDir,
    stdio: 'inherit',
    timeout: 5 * 60_000,
  });
  const reason = stalenessReason();
  if (reason === null) {
    console.log('e2e global-setup: reusing the verified package in dist/');
    return;
  }
  console.log(`e2e global-setup: (re)building the package — ${reason}`);
  execFileSync('npm', ['run', 'package:verify:e2e'], {
    cwd: desktopDir,
    stdio: 'inherit',
    timeout: 15 * 60_000,
  });
  const remaining = stalenessReason({ includeLocalChanges: false });
  if (remaining !== null) {
    throw new Error(`package:verify:e2e did not produce a fresh package: ${remaining}`);
  }
}

function sweepOrphanedE2eProcesses(): void {
  const currentPid = process.pid;
  const rows = processRows();
  const stalePids = rows
    .filter(({ pid, command }) => pid !== currentPid && isE2eOrphanCommand(command))
    .map(({ pid }) => pid);
  if (stalePids.length === 0) {
    return;
  }
  const allPids = new Set<number>();
  for (const pid of stalePids) {
    for (const treePid of collectProcessTree(pid)) {
      if (treePid !== currentPid) {
        allPids.add(treePid);
      }
    }
  }
  for (const pid of [...allPids].sort((a, b) => b - a)) {
    try {
      process.kill(pid, 'SIGKILL');
    } catch {
      // Best-effort preflight cleanup. Live failures are reported by tests.
    }
  }
  console.log(`e2e global-setup: killed ${allPids.size} stale packaged-e2e process(es)`);
}

function isE2eOrphanCommand(command: string): boolean {
  const distDir = path.join(desktopDir, 'dist');
  const tempE2eRoot = path.join(os.tmpdir(), 'agentico-e2e-');
  if (command.includes(tempE2eRoot) && command.includes('/stubs/claude-stub')) {
    return true;
  }
  if (!command.includes(distDir)) {
    return false;
  }
  return (
    command.includes('/Agentico.app/Contents/MacOS/Agentico') ||
    command.includes('/Contents/Resources/bin/agentico') ||
    command.includes('/resources/bin/agentico')
  );
}

function processRows(): Array<{ pid: number; command: string }> {
  let out = '';
  try {
    out = execFileSync('ps', ['-axo', 'pid=,command='], { encoding: 'utf8' });
  } catch {
    return [];
  }
  return out
    .split('\n')
    .map((line) => {
      const match = line.match(/^\s*(\d+)\s+(.*)$/);
      if (match === null) {
        return null;
      }
      return { pid: Number(match[1]), command: match[2] };
    })
    .filter((row): row is { pid: number; command: string } => row !== null);
}

function stalenessReason({ includeLocalChanges = true } = {}): string | null {
  const verification = readVerification();
  if (verification === null) {
    return 'dist/package-verification.json is missing or unreadable';
  }
  if (!fs.existsSync(verification.unpacked_app)) {
    return `unpacked app is missing at ${verification.unpacked_app}`;
  }
  const head = gitHead();
  if (head !== null && verification.identity.server_revision !== head) {
    return (
      `package was built from ${verification.identity.server_revision.slice(0, 12)} ` +
      `but HEAD is ${head.slice(0, 12)}`
    );
  }
  if (includeLocalChanges && hasChangesNewerThan(verification.verified_at)) {
    return 'the worktree has changes newer than the verified package';
  }
  return null;
}

function gitHead(): string | null {
  try {
    return execFileSync('git', ['rev-parse', 'HEAD'], {
      cwd: path.dirname(desktopDir),
      encoding: 'utf8',
    }).trim();
  } catch {
    return null; // not a git checkout — trust the existing verified package
  }
}

function hasChangesNewerThan(verifiedAt: string): boolean {
  const verifiedAtMs = Date.parse(verifiedAt);
  if (Number.isNaN(verifiedAtMs)) return true;

  try {
    const repository = path.dirname(desktopDir);
    const changed = [
      ...gitPaths(repository, ['diff', '--name-only', '-z', 'HEAD', '--']),
      ...gitPaths(repository, ['ls-files', '--others', '--exclude-standard', '-z']),
    ];
    return changed.some((file) => {
      // Packaged tests are not included by electron-builder (`files: out/**,
      // package.json`), so changing only their orchestration must not trigger
      // an identical application rebuild before every retry.
      if (file.startsWith('desktop/test/')) return false;
      try {
        return fs.statSync(path.join(repository, file)).mtimeMs > verifiedAtMs;
      } catch {
        // Deleted paths are already captured by a package rebuilt from this checkout.
        return false;
      }
    });
  } catch {
    return true;
  }
}

function gitPaths(repository: string, args: string[]): string[] {
  return execFileSync('git', args, { cwd: repository, encoding: 'utf8' })
    .split('\0')
    .filter((file) => file.length > 0);
}
