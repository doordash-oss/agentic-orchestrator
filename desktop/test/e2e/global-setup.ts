/**
 * Ensures exactly one fresh packaged build is reused by every journey.
 *
 * Freshness contract: dist/package-verification.json must exist, its
 * unpacked_app must exist, and the identity's server_revision must match the
 * current git HEAD and newer than any local source edit (a package built from
 * other code would silently test the wrong build). Anything else triggers one
 * `npm run package:verify`, which rebuilds and re-inspects the native package.
 * CI runs package:verify right before this suite, so the check passes without
 * a second build there.
 */
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { desktopDir, readVerification } from './helpers/packaged';

export default function globalSetup(): void {
  const reason = stalenessReason();
  if (reason === null) {
    console.log('e2e global-setup: reusing the verified package in dist/');
    return;
  }
  console.log(`e2e global-setup: (re)building the package — ${reason}`);
  execFileSync('npm', ['run', 'package:verify'], {
    cwd: desktopDir,
    stdio: 'inherit',
    timeout: 15 * 60_000,
  });
  const remaining = stalenessReason({ includeLocalChanges: false });
  if (remaining !== null) {
    throw new Error(`package:verify did not produce a fresh package: ${remaining}`);
  }
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
