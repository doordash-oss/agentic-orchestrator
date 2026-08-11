// Execute the complete local release from a detached committed-source workspace.
import { execFileSync } from 'node:child_process';
import { existsSync, lstatSync, readFileSync, rmSync } from 'node:fs';
import { isAbsolute, join, resolve } from 'node:path';

import { expectedDesktopArtifacts, readArtifactEvidence } from './lib/release-artifacts.mjs';
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
  goreleaserEnvironment,
  readPublicationSnapshot,
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
  'publication-snapshot',
  'snapshot-gate',
  'provenance-recheck',
  'remote-tag-reservation',
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
  if (!['goreleaser-published', 'remote-verified'].includes(stage)) {
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
    !['goreleaser-published', 'remote-verified'].includes(state.stage)
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
  reserveTag = ({ evidence }) =>
    reserveRemoteTag({ tag: evidence.tag, commit: evidence.commit, request: githubRequest }),
  publish = ({ evidence, notesFile }) =>
    runGoreleaserRelease({ evidence, env: ambientEnv, notesFile }),
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
        ...goreleaserEnvironment(ambientEnv, evidence.tag),
        AGENTICO_PUBLICATION_DIR: snapshot.path,
      },
    }),
  cleanup = (evidence) => cleanupReleaseWorkspace({ evidence }),
  loadResume = (root) => loadReleaseResumeState(root),
  saveResume = (evidence, snapshot, stage) => saveReleaseResumeState(evidence, snapshot, stage),
  removeResume = (root) => removeReleaseResumeState(root),
  validateResume = validateReleaseResumeState,
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
      assertGate(verifyManifest({ evidence, snapshot }), 'resumed manifest gate');
      await verifyRemote({ evidence, snapshot });
      if (resumed.stage === 'goreleaser-published') {
        saveResume(evidence, snapshot, 'remote-verified');
      }
      publishCask({ evidence, snapshot });
      preserveWorkspace = false;
      clearResumeAfterCleanup = true;
      return { tag: evidence.tag, commit: evidence.commit, resumed: true };
    }

    evidence = preflight();
    const cwd = evidence.workspace_root;
    const env = secretFreeBuildEnvironment(ambientEnv, evidence.evidence_path);
    requireCleanIgnoredInputs(cwd);
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
    publish({ evidence, snapshot, notesFile });
    preserveWorkspace = true;
    saveResume(evidence, snapshot, 'goreleaser-published');
    assertGate(verifyManifest({ evidence, snapshot }), 'manifest gate');
    await verifyRemote({ evidence, snapshot });
    saveResume(evidence, snapshot, 'remote-verified');
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
