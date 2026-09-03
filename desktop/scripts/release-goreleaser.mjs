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
