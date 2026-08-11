// Execute the complete local release from a detached committed-source workspace.
import { execFileSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import { isAbsolute, join, resolve } from 'node:path';

import { expectedDesktopArtifacts, readArtifactEvidence } from './lib/release-artifacts.mjs';
import { isMainModule } from './lib/main-entry.mjs';
import {
  cleanupReleaseWorkspace,
  runReleasePreflight,
  verifyReleaseProvenance,
} from './release-preflight.mjs';
import { runGoreleaserRelease } from './release-goreleaser.mjs';
import {
  cleanGoEnvironment,
  createPublicationSnapshot,
  verifyPublicationSnapshot,
} from './release-workspace.mjs';
import { verifyReleaseArtifacts } from './verify-release-artifacts.mjs';
import {
  githubRequest,
  reserveRemoteTag,
  verifyReleasePublication,
} from './verify-release-publication.mjs';

export const RELEASE_SEQUENCE = Object.freeze([
  'preflight',
  'npm-ci',
  'mac-package',
  'linux-packages',
  'package-gate',
  'publication-snapshot',
  'snapshot-gate',
  'provenance-recheck',
  'remote-tag-reservation',
  'goreleaser',
  'manifest-gate',
  'remote-byte-gate',
  'desktop-cask',
  'cleanup',
]);

const RECEIPTS = Object.freeze([
  'package-verification-darwin-universal.json',
  'package-verification-linux-x64.json',
  'package-verification-linux-arm64.json',
]);

function execute(_label, command, args, options) {
  return execFileSync(command, args, { stdio: 'inherit', ...options });
}

function requireCleanIgnoredInputs(workspaceRoot) {
  for (const relativePath of ['go.work', 'go.work.sum', 'skills/.env']) {
    if (existsSync(join(workspaceRoot, relativePath))) {
      throw new Error(
        `isolated release workspace contains forbidden ignored input: ${relativePath}`,
      );
    }
  }
}

function assertGate(evidence, label) {
  if (!evidence?.ok) throw new Error(`${label} failed:\n${(evidence?.errors ?? []).join('\n')}`);
}

/** Run every build and publication step from evidence-bound source, then clean it up. */
export async function runRelease({
  operatorRoot = process.cwd(),
  preflight = () => runReleasePreflight({ cwd: operatorRoot }),
  command = execute,
  verifyProvenance = (evidence) => verifyReleaseProvenance({ evidence }),
  createSnapshot = createPublicationSnapshot,
  verifySnapshot = verifyPublicationSnapshot,
  verifyPackages = ({ evidence, desktopDist }) =>
    verifyReleaseArtifacts({
      mode: 'packages',
      tag: evidence.tag,
      revision: evidence.commit,
      desktopDist,
      evidenceDir: join(evidence.workspace_root, 'desktop', 'dist'),
    }),
  reserveTag = ({ evidence }) =>
    reserveRemoteTag({ tag: evidence.tag, commit: evidence.commit, request: githubRequest }),
  publish = ({ evidence }) => runGoreleaserRelease({ evidence }),
  verifyManifest = ({ evidence }) =>
    verifyReleaseArtifacts({
      mode: 'manifest',
      tag: evidence.tag,
      revision: evidence.commit,
      desktopDist: join(evidence.workspace_root, 'desktop', 'dist', 'publication'),
      checksumsPath: join(evidence.workspace_root, 'dist', 'checksums.txt'),
      evidenceDir: join(evidence.workspace_root, 'desktop', 'dist'),
    }),
  verifyRemote = ({ evidence, snapshot }) =>
    verifyReleasePublication({
      tag: evidence.tag,
      commit: evidence.commit,
      request: githubRequest,
      expectedDigests: Object.fromEntries(
        [
          ...snapshot.artifacts,
          readArtifactEvidence(join(evidence.workspace_root, 'dist', 'checksums.txt')),
          readArtifactEvidence(join(evidence.workspace_root, 'dist', 'checksums.txt.sig')),
        ].map(({ path, sha256 }) => [path.split('/').at(-1), sha256]),
      ),
    }),
  publishCask = ({ evidence, snapshot }) =>
    command('desktop-cask', process.execPath, ['desktop/scripts/publish-desktop-cask.mjs'], {
      cwd: evidence.workspace_root,
      env: {
        ...process.env,
        AGENTICO_PUBLICATION_DIR: snapshot.path,
      },
    }),
  cleanup = (evidence) => cleanupReleaseWorkspace({ evidence }),
} = {}) {
  let evidence;
  let snapshot;
  try {
    evidence = preflight();
    const cwd = evidence.workspace_root;
    const env = cleanGoEnvironment(process.env);
    requireCleanIgnoredInputs(cwd);
    const notes = process.env.AGENTICO_RELEASE_NOTES_FILE;
    if (notes)
      process.env.AGENTICO_RELEASE_NOTES_FILE = isAbsolute(notes)
        ? notes
        : resolve(operatorRoot, notes);

    command('npm-ci', 'npm', ['ci'], { cwd, env });
    requireCleanIgnoredInputs(cwd);
    command('mac-package', 'npm', ['run', 'package:verify', '--workspace', 'desktop'], {
      cwd,
      env,
    });
    command('linux-packages', 'npm', ['run', 'package:linux:release', '--workspace', 'desktop'], {
      cwd,
      env,
    });
    assertGate(
      verifyPackages({ evidence, desktopDist: join(cwd, 'desktop', 'dist') }),
      'package gate',
    );

    snapshot = createSnapshot({
      workspaceRoot: cwd,
      sourceDir: join(cwd, 'desktop', 'dist'),
      artifactNames: expectedDesktopArtifacts(evidence.tag).map(({ name }) => name),
      receiptNames: RECEIPTS,
    });
    verifySnapshot(snapshot);
    assertGate(verifyPackages({ evidence, desktopDist: snapshot.path }), 'snapshot gate');
    verifyProvenance(evidence);
    await reserveTag({ evidence });
    publish({ evidence, snapshot });
    assertGate(verifyManifest({ evidence, snapshot }), 'manifest gate');
    await verifyRemote({ evidence, snapshot });
    publishCask({ evidence, snapshot });
    return { tag: evidence.tag, commit: evidence.commit };
  } finally {
    if (evidence !== undefined) cleanup(evidence);
  }
}

async function main() {
  try {
    const result = await runRelease();
    console.log(`release complete for ${result.tag} (${result.commit})`);
  } catch (error) {
    console.error(`release failed: ${error instanceof Error ? error.message : String(error)}`);
    process.exitCode = 1;
  }
}

if (isMainModule(import.meta.url)) void main();
