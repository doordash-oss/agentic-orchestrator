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

// Publish the desktop DMG's Homebrew cask to the tap after a goreleaser
// release. goreleaser's OSS cask pipe cannot attach a checksum to artifacts
// it did not build, so this script renders Casks/agentico-desktop.rb from the
// locally built DMG and pushes it with the operator's ambient git
// credentials — the same trust model as running goreleaser locally.
//
//   node desktop/scripts/publish-desktop-cask.mjs [--dry-run] [--tap-dir <path>]
//
// The version comes from the release tag HEAD sits on (matching what
// package-build.mjs stamped into the DMG). --dry-run prints the cask without
// touching the tap. --tap-dir reuses an existing clone instead of a fresh
// shallow clone.
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

import { renderDesktopCask } from './lib/desktop-cask.mjs';
import { desktopVersionFromExactTag } from './lib/release-version.mjs';

const desktopDir = dirname(dirname(fileURLToPath(import.meta.url)));
const DMG_PATH = join(
  process.env.AGENTICO_PUBLICATION_DIR ?? join(desktopDir, 'dist'),
  'Agentico-mac-universal.dmg',
);
const TAP_REMOTE =
  process.env.AGENTICO_TAP_REMOTE ??
  'https://github.com/doordash-oss/homebrew-agentic-orchestrator.git';

const args = process.argv.slice(2);
const dryRun = args.includes('--dry-run');
const tapDirFlag = args.indexOf('--tap-dir');
const providedTapDir = tapDirFlag === -1 ? null : args[tapDirFlag + 1];

function fail(message) {
  console.error(`publish-desktop-cask: ${message}`);
  process.exit(1);
}

function git(cwd, ...gitArgs) {
  return execFileSync('git', gitArgs, { cwd, encoding: 'utf8' }).trim();
}

function releaseVersion() {
  let tag;
  try {
    tag = git(desktopDir, 'describe', '--tags', '--exact-match');
  } catch {
    fail('HEAD is not on a release tag; cut the tag before publishing the cask');
  }
  const version = desktopVersionFromExactTag(tag);
  if (version === null) fail(`tag ${tag} is not a clean vMAJOR.MINOR.PATCH release tag`);
  return version;
}

if (!existsSync(DMG_PATH)) {
  fail(`missing ${DMG_PATH}; run \`npm run package:build --workspace desktop\` first`);
}
const version = releaseVersion();
const sha256 = createHash('sha256').update(readFileSync(DMG_PATH)).digest('hex');
const cask = renderDesktopCask({ version, sha256 });

if (dryRun) {
  console.log(cask);
  process.exit(0);
}

const tapDir = providedTapDir ?? mkdtempSync(join(tmpdir(), 'agentico-tap-'));
try {
  if (providedTapDir === null) {
    execFileSync('git', ['clone', '--depth', '1', TAP_REMOTE, tapDir], { stdio: 'inherit' });
  } else if (!existsSync(join(tapDir, '.git'))) {
    fail(`--tap-dir ${tapDir} is not a git checkout`);
  }
  const caskPath = join(tapDir, 'Casks', 'agentico-desktop.rb');
  mkdirSync(dirname(caskPath), { recursive: true });
  writeFileSync(caskPath, cask);
  if (git(tapDir, 'status', '--porcelain') === '') {
    console.log(`publish-desktop-cask: tap already carries agentico-desktop ${version}`);
    process.exit(0);
  }
  git(tapDir, 'add', 'Casks/agentico-desktop.rb');
  git(tapDir, 'commit', '-m', `agentico-desktop ${version}`);
  execFileSync('git', ['push'], { cwd: tapDir, stdio: 'inherit' });
  console.log(`publish-desktop-cask: pushed agentico-desktop ${version}`);
} finally {
  if (providedTapDir === null) rmSync(tapDir, { recursive: true, force: true });
}
