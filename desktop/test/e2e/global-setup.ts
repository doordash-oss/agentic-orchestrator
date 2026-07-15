/**
 * Ensures exactly one fresh packaged build is reused by every journey.
 *
 * Freshness contract: dist/package-verification.json must exist, its
 * unpacked_app must exist, and the identity's server_revision must match the
 * current git HEAD (a package built from other code would silently test the
 * wrong build). Anything else triggers one `npm run package:verify`, which
 * rebuilds and re-inspects the native package. CI runs package:verify right
 * before this suite, so the check passes without a second build there.
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
  const remaining = stalenessReason();
  if (remaining !== null) {
    throw new Error(`package:verify did not produce a fresh package: ${remaining}`);
  }
}

function stalenessReason(): string | null {
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
