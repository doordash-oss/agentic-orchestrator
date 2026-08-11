// Run the fixed GoReleaser publication command with an optional release-notes file.
import { execFileSync } from 'node:child_process';
import { statSync } from 'node:fs';
import { isAbsolute, resolve } from 'node:path';
import { isMainModule } from './lib/main-entry.mjs';
import { verifyReleaseProvenance } from './release-preflight.mjs';
import { goreleaserEnvironment, readPublicationSnapshot } from './release-workspace.mjs';

export function goreleaserArguments(notesFile) {
  const args = ['release', '--clean'];
  if (notesFile !== undefined && notesFile !== '') args.push('--release-notes', notesFile);
  return args;
}

function regularFile(path) {
  try {
    return statSync(path).isFile();
  } catch {
    return false;
  }
}

/** Execute only the fixed release command; the optional file is the sole supported input. */
export function runGoreleaserRelease({
  notesFile,
  isFile = regularFile,
  evidence,
  verifyEvidence = (value) => verifyReleaseProvenance({ evidence: value }),
  verifySnapshot = (workspaceRoot) => readPublicationSnapshot(workspaceRoot),
  execute = (command, args, options) => execFileSync(command, args, options),
  env = process.env,
} = {}) {
  if (notesFile !== undefined && notesFile !== '' && !isFile(notesFile)) {
    throw new Error(`release notes file does not exist or is not a regular file: ${notesFile}`);
  }
  const trusted = verifyEvidence(evidence);
  if (typeof trusted.workspace_root !== 'string' || trusted.workspace_root === '') {
    throw new Error('release provenance evidence has no isolated workspace');
  }
  verifySnapshot(trusted.workspace_root);
  const args = goreleaserArguments(notesFile);
  execute('goreleaser', args, {
    cwd: trusted.workspace_root,
    stdio: 'inherit',
    env: goreleaserEnvironment(env, trusted.tag),
  });
  verifySnapshot(trusted.workspace_root);
  return args;
}

function main() {
  try {
    const configuredNotes = process.env.AGENTICO_RELEASE_NOTES_FILE;
    const notesFile = configuredNotes
      ? isAbsolute(configuredNotes)
        ? configuredNotes
        : resolve(process.cwd(), configuredNotes)
      : undefined;
    runGoreleaserRelease({ notesFile });
  } catch (error) {
    console.error(`release-goreleaser: ${error instanceof Error ? error.message : String(error)}`);
    process.exitCode = 1;
  }
}

if (isMainModule(import.meta.url)) main();
