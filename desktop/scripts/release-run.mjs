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

// Execute the complete local release from a detached committed-source workspace.
import { execFileSync } from 'node:child_process';
import { existsSync, lstatSync, mkdirSync, readFileSync, rmSync } from 'node:fs';
import { dirname, isAbsolute, join, resolve } from 'node:path';

import { expectedDesktopArtifacts, readArtifactEvidence } from './lib/release-artifacts.mjs';
import {
  createDesktopReleaseManifest,
  DESKTOP_RELEASE_MANIFEST,
  DESKTOP_RELEASE_SIGNATURE,
  verifyDesktopReleaseManifest,
} from './lib/desktop-release-manifest.mjs';
import { extractEmbeddedReleasePublicKey } from './lib/release-signing.mjs';
import { isMainModule } from './lib/main-entry.mjs';
import {
  cleanupReleaseWorkspace,
  evidencePathFor,
  readReleaseEvidence,
  validateReleaseEvidence,
  runReleasePreflight,
  verifyOperatorReleaseSubject,
  verifyReleaseProvenance,
  writeReleaseEvidence,
} from './release-preflight.mjs';
import { runGoreleaserRelease } from './release-goreleaser.mjs';
import {
  createPublicationSnapshot,
  dockerReleaseEnvironment,
  goreleaserEnvironment,
  readPublicationSnapshot,
  releaseSigningEnvironment,
  revalidateWorkspaceToken,
  secretFreeBuildEnvironment,
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
  'desktop-manifest-sign',
  'desktop-manifest-gate',
  'publication-snapshot',
  'snapshot-gate',
  'provenance-recheck',
  'tag-reservation-start-record',
  'remote-tag-reservation',
  'goreleaser-start-record',
  'goreleaser',
  'goreleaser-resume-record',
  'manifest-gate',
  'remote-byte-gate',
  'remote-resume-record',
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

function git(cwd, ...args) {
  return execFileSync('git', args, { cwd, encoding: 'utf8' }).trim();
}

function resumePath(operatorRoot, gitCommand = git) {
  return resolve(
    operatorRoot,
    gitCommand(operatorRoot, 'rev-parse', '--git-path', 'agentico-release-resume.json'),
  );
}

export function loadReleaseResumeState(operatorRoot, { gitCommand = git } = {}) {
  const path = resumePath(operatorRoot, gitCommand);
  if (!existsSync(path)) return null;
  const stat = lstatSync(path);
  if (!stat.isFile() || stat.isSymbolicLink()) throw new Error('release resume state was replaced');
  return JSON.parse(readFileSync(path, 'utf8'));
}

export function saveReleaseResumeState(evidence, snapshot, stage, { gitCommand = git } = {}) {
  if (
    ![
      'tag-reservation-started',
      'goreleaser-started',
      'goreleaser-published',
      'remote-verified',
    ].includes(stage)
  ) {
    throw new Error(`invalid release resume stage: ${stage}`);
  }
  const path = resumePath(evidence.operator_root, gitCommand);
  if (existsSync(path)) {
    const stat = lstatSync(path);
    if (!stat.isFile() || stat.isSymbolicLink())
      throw new Error('release resume state was replaced');
  }
  writeReleaseEvidence(path, { schema_version: 1, stage, evidence, snapshot });
}

export function removeReleaseResumeState(operatorRoot, { gitCommand = git } = {}) {
  const path = resumePath(operatorRoot, gitCommand);
  if (!existsSync(path)) return;
  const stat = lstatSync(path);
  if (!stat.isFile() || stat.isSymbolicLink()) throw new Error('release resume state was replaced');
  rmSync(path);
}

export function validateResumeEvidenceBoundary(evidence, { expectedPath, readEvidence } = {}) {
  const path = resolve(expectedPath ?? evidencePathFor(evidence.operator_root));
  if (resolve(evidence.evidence_path) !== path) {
    throw new Error('release resume evidence path does not match the operator repository');
  }
  const onDisk =
    readEvidence?.(path) ??
    readReleaseEvidence({ cwd: evidence.operator_root, evidencePath: path });
  if (JSON.stringify(onDisk) !== JSON.stringify(evidence)) {
    throw new Error('release resume evidence was tampered; inspect it before manual cleanup');
  }
  return evidence;
}

export function validateReleaseResumeState(state) {
  if (
    state?.schema_version !== 1 ||
    ![
      'tag-reservation-started',
      'goreleaser-started',
      'goreleaser-published',
      'remote-verified',
    ].includes(state.stage)
  ) {
    throw new Error('release resume state is invalid; inspect it before manual cleanup');
  }
  const evidence = verifyOperatorReleaseSubject({
    evidence: validateReleaseEvidence(state.evidence),
  });
  revalidateWorkspaceToken(evidence.workspace_token);
  validateResumeEvidenceBoundary(evidence);
  const snapshot = readPublicationSnapshot(evidence.workspace_root);
  if (JSON.stringify(snapshot) !== JSON.stringify(state.snapshot)) {
    throw new Error('release resume snapshot was tampered; inspect it before manual cleanup');
  }
  return { stage: state.stage, evidence, snapshot };
}

/** Run every build and publication step from evidence-bound source, then clean it up. */
export async function runRelease({
  operatorRoot = process.cwd(),
  ambientEnv = process.env,
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
  prepareDesktopManifest = ({ evidence, desktopDist }) => {
    const manifestPath = join(desktopDist, DESKTOP_RELEASE_MANIFEST);
    createDesktopReleaseManifest({
      tag: evidence.tag,
      commit: evidence.commit,
      artifactDir: desktopDist,
      manifestPath,
    });
    command(
      'desktop-manifest-sign',
      process.execPath,
      ['desktop/scripts/release-sign.mjs', 'sign', manifestPath],
      { cwd: evidence.workspace_root, env: releaseSigningEnvironment(ambientEnv) },
    );
  },
  verifyDesktopManifest = ({ evidence, desktopDist }) =>
    verifyDesktopReleaseManifest({
      tag: evidence.tag,
      commit: evidence.commit,
      artifactDir: desktopDist,
      manifestPath: join(desktopDist, DESKTOP_RELEASE_MANIFEST),
      publicKey: extractEmbeddedReleasePublicKey(
        readFileSync(join(evidence.workspace_root, 'desktop', 'src', 'main', 'updates.ts'), 'utf8'),
      ),
    }),
  reserveTag = ({ evidence }) =>
    reserveRemoteTag({ tag: evidence.tag, commit: evidence.commit, request: githubRequest }),
  publish = ({ evidence, notesFile }) =>
    runGoreleaserRelease({ evidence, env: ambientEnv, notesFile }),
  verifyManifest = ({ evidence, snapshot }) =>
    verifyReleaseArtifacts({
      mode: 'manifest',
      tag: evidence.tag,
      revision: evidence.commit,
      desktopDist: join(evidence.workspace_root, 'desktop', 'dist', 'publication'),
      checksumsPath: join(evidence.workspace_root, 'dist', 'checksums.txt'),
      evidenceDir: join(evidence.workspace_root, 'desktop', 'dist'),
      additionalExpectedDigests: Object.fromEntries(
        snapshot.artifacts
          .filter(({ path }) =>
            [DESKTOP_RELEASE_MANIFEST, DESKTOP_RELEASE_SIGNATURE].includes(path.split('/').at(-1)),
          )
          .map(({ path, sha256 }) => [path.split('/').at(-1), sha256]),
      ),
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
        ...goreleaserEnvironment(ambientEnv, evidence.tag),
        AGENTICO_PUBLICATION_DIR: snapshot.path,
      },
    }),
  cleanup = (evidence) => cleanupReleaseWorkspace({ evidence }),
  loadResume = (root) => loadReleaseResumeState(root),
  saveResume = (evidence, snapshot, stage) => saveReleaseResumeState(evidence, snapshot, stage),
  removeResume = (root) => removeReleaseResumeState(root),
  validateResume = validateReleaseResumeState,
  revalidateWorkspace = revalidateWorkspaceToken,
  prepareBuildHome = (path) => mkdirSync(path, { recursive: true, mode: 0o700 }),
} = {}) {
  let evidence;
  let snapshot;
  let preserveWorkspace = false;
  let clearResumeAfterCleanup = false;
  const configuredNotes = ambientEnv.AGENTICO_RELEASE_NOTES_FILE;
  const notesFile = configuredNotes
    ? isAbsolute(configuredNotes)
      ? configuredNotes
      : resolve(operatorRoot, configuredNotes)
    : undefined;
  try {
    const pending = loadResume(operatorRoot);
    if (pending !== null) {
      const resumed = validateResume(pending);
      evidence = resumed.evidence;
      snapshot = resumed.snapshot;
      preserveWorkspace = true;
      revalidateWorkspace(evidence.workspace_token);
      verifyDesktopManifest({ evidence, desktopDist: snapshot.path });
      if (resumed.stage === 'tag-reservation-started') {
        await reserveTag({ evidence });
        revalidateWorkspace(evidence.workspace_token);
        saveResume(evidence, snapshot, 'goreleaser-started');
        publish({ evidence, snapshot, notesFile });
        saveResume(evidence, snapshot, 'goreleaser-published');
        assertGate(verifyManifest({ evidence, snapshot }), 'resumed manifest gate');
        await verifyRemote({ evidence, snapshot });
        saveResume(evidence, snapshot, 'remote-verified');
        revalidateWorkspace(evidence.workspace_token);
        publishCask({ evidence, snapshot });
        preserveWorkspace = false;
        clearResumeAfterCleanup = true;
        return { tag: evidence.tag, commit: evidence.commit, resumed: true };
      }
      assertGate(verifyManifest({ evidence, snapshot }), 'resumed manifest gate');
      try {
        await verifyRemote({ evidence, snapshot });
      } catch (error) {
        if (resumed.stage === 'goreleaser-started') {
          throw new Error(
            `release publication outcome is uncertain; do not republish. Inspect and clean up the partial remote publication before an explicit recovery: ${error instanceof Error ? error.message : String(error)}`,
            { cause: error },
          );
        }
        throw error;
      }
      if (resumed.stage === 'goreleaser-started') {
        saveResume(evidence, snapshot, 'goreleaser-published');
      }
      if (resumed.stage !== 'remote-verified') {
        saveResume(evidence, snapshot, 'remote-verified');
      }
      revalidateWorkspace(evidence.workspace_token);
      publishCask({ evidence, snapshot });
      preserveWorkspace = false;
      clearResumeAfterCleanup = true;
      return { tag: evidence.tag, commit: evidence.commit, resumed: true };
    }

    evidence = preflight();
    const cwd = evidence.workspace_root;
    const isolatedHome = join(dirname(evidence.cleanup_helper.path), 'build-home');
    prepareBuildHome(isolatedHome);
    const env = secretFreeBuildEnvironment(
      ambientEnv,
      evidence.evidence_path,
      evidence.evidence_sha256,
      isolatedHome,
    );
    const dockerEnv = dockerReleaseEnvironment(
      ambientEnv,
      evidence.evidence_path,
      evidence.evidence_sha256,
    );
    requireCleanIgnoredInputs(cwd);
    revalidateWorkspace(evidence.workspace_token);
    command('npm-ci', 'npm', ['ci'], { cwd, env });
    requireCleanIgnoredInputs(cwd);
    revalidateWorkspace(evidence.workspace_token);
    command('mac-package', 'npm', ['run', 'package:verify', '--workspace', 'desktop'], {
      cwd,
      env,
    });
    revalidateWorkspace(evidence.workspace_token);
    command('linux-packages', 'npm', ['run', 'package:linux:release', '--workspace', 'desktop'], {
      cwd,
      env: dockerEnv,
    });
    assertGate(
      verifyPackages({ evidence, desktopDist: join(cwd, 'desktop', 'dist') }),
      'package gate',
    );
    const desktopDist = join(cwd, 'desktop', 'dist');
    revalidateWorkspace(evidence.workspace_token);
    prepareDesktopManifest({ evidence, desktopDist });
    verifyDesktopManifest({ evidence, desktopDist });

    revalidateWorkspace(evidence.workspace_token);
    snapshot = createSnapshot({
      workspaceRoot: cwd,
      sourceDir: join(cwd, 'desktop', 'dist'),
      artifactNames: expectedDesktopArtifacts(evidence.tag).map(({ name }) => name),
      extraNames: [DESKTOP_RELEASE_MANIFEST, DESKTOP_RELEASE_SIGNATURE],
      receiptNames: RECEIPTS,
    });
    verifySnapshot(snapshot);
    assertGate(verifyPackages({ evidence, desktopDist: snapshot.path }), 'snapshot gate');
    verifyDesktopManifest({ evidence, desktopDist: snapshot.path });
    verifyProvenance(evidence);
    preserveWorkspace = true;
    saveResume(evidence, snapshot, 'tag-reservation-started');
    revalidateWorkspace(evidence.workspace_token);
    await reserveTag({ evidence });
    revalidateWorkspace(evidence.workspace_token);
    saveResume(evidence, snapshot, 'goreleaser-started');
    publish({ evidence, snapshot, notesFile });
    saveResume(evidence, snapshot, 'goreleaser-published');
    assertGate(verifyManifest({ evidence, snapshot }), 'manifest gate');
    await verifyRemote({ evidence, snapshot });
    saveResume(evidence, snapshot, 'remote-verified');
    revalidateWorkspace(evidence.workspace_token);
    publishCask({ evidence, snapshot });
    preserveWorkspace = false;
    clearResumeAfterCleanup = true;
    return { tag: evidence.tag, commit: evidence.commit };
  } finally {
    if (evidence !== undefined && !preserveWorkspace) {
      cleanup(evidence);
      if (clearResumeAfterCleanup) removeResume(operatorRoot);
    }
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
