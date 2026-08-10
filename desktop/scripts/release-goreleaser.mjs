// Run the fixed GoReleaser publication command with an optional release-notes file.
import { execFileSync } from 'node:child_process';
import { statSync } from 'node:fs';
import { isMainModule } from './lib/main-entry.mjs';

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
  notesFile = process.env.AGENTICO_RELEASE_NOTES_FILE,
  isFile = regularFile,
  execute = (command, args) => execFileSync(command, args, { stdio: 'inherit' }),
} = {}) {
  if (notesFile !== undefined && notesFile !== '' && !isFile(notesFile)) {
    throw new Error(`release notes file does not exist or is not a regular file: ${notesFile}`);
  }
  const args = goreleaserArguments(notesFile);
  execute('goreleaser', args);
  return args;
}

function main() {
  try {
    runGoreleaserRelease();
  } catch (error) {
    console.error(`release-goreleaser: ${error instanceof Error ? error.message : String(error)}`);
    process.exitCode = 1;
  }
}

if (isMainModule(import.meta.url)) main();
