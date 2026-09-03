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

import { afterEach, expect, it, vi } from 'vitest';

afterEach(() => {
  vi.unstubAllEnvs();
  vi.resetModules();
});

it('compiles the release-stamped desktop version into the renderer', async () => {
  vi.stubEnv('AGENTICO_DESKTOP_VERSION', '0.149.1');
  vi.resetModules();

  const { default: config } = await import('../../../electron.vite.config');
  if (typeof config !== 'object' || config === null) {
    throw new Error('electron-vite config did not resolve to an object');
  }
  const renderer = config.renderer as { define?: Record<string, string> } | undefined;

  expect(renderer?.define?.__APP_VERSION__).toBe(JSON.stringify('0.149.1'));
});
