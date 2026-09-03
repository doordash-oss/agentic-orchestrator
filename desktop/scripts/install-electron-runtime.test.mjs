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

import { readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

import { installElectronRuntime } from './install-electron-runtime.mjs';

const desktopDir = resolve(dirname(fileURLToPath(import.meta.url)), '..');

describe('Electron runtime installer', () => {
  it('serializes installation and retries transient download failures', async () => {
    const calls = [];
    const waits = [];
    const warnings = [];

    await installElectronRuntime({
      installerPath: '/repo/node_modules/electron/install.js',
      attempts: 3,
      retryDelayMs: 5,
      execute: (command, args, options) => {
        calls.push({ command, args, options });
        if (calls.length < 3) throw new Error('fetch failed');
      },
      wait: async (delayMs) => waits.push(delayMs),
      output: { warn: (message) => warnings.push(message) },
    });

    expect(calls).toHaveLength(3);
    expect(calls[0]).toMatchObject({
      command: process.execPath,
      args: ['/repo/node_modules/electron/install.js'],
    });
    expect(waits).toEqual([5, 10]);
    expect(warnings).toHaveLength(2);
  });

  it('installs the runtime before parallel desktop tests', () => {
    const packageJson = JSON.parse(readFileSync(join(desktopDir, 'package.json'), 'utf8'));

    expect(packageJson.scripts.test).toBe('npm run install:electron-runtime && vitest run');
  });
});
