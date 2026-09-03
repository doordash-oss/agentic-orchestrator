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

import { execFileSync } from 'node:child_process';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptPath = fileURLToPath(import.meta.url);
const desktopDir = resolve(dirname(scriptPath), '..');
const repoRoot = resolve(desktopDir, '..');

function delay(delayMs) {
  return new Promise((resolveDelay) => setTimeout(resolveDelay, delayMs));
}

/**
 * Electron 43 installs its runtime lazily on first import. Desktop tests run
 * in parallel, so CI installs it once up front and retries transient artifact
 * fetch failures instead of letting workers race independent downloads.
 */
export async function installElectronRuntime({
  installerPath = join(repoRoot, 'node_modules', 'electron', 'install.js'),
  attempts = 3,
  retryDelayMs = 5_000,
  execute = execFileSync,
  wait = delay,
  output = console,
} = {}) {
  let lastError;
  for (let attempt = 1; attempt <= attempts; attempt += 1) {
    try {
      execute(process.execPath, [installerPath], { cwd: repoRoot, stdio: 'inherit' });
      return;
    } catch (error) {
      lastError = error;
      if (attempt === attempts) break;
      const backoffMs = retryDelayMs * attempt;
      output.warn(
        `Electron runtime install attempt ${attempt} failed; retrying in ${backoffMs}ms.`,
      );
      await wait(backoffMs);
    }
  }
  throw new Error(`Electron runtime install failed after ${attempts} attempts.`, {
    cause: lastError,
  });
}

if (process.argv[1] === scriptPath) {
  installElectronRuntime().catch((error) => {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = 1;
  });
}
