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

/**
 * Packaged E2E journeys: Playwright drives the real
 * unsigned package produced by `npm run package:verify` — the unpacked app
 * recorded in dist/package-verification.json — with the real bundled Go
 * server. Provider CLIs are the only stubbed boundary (config
 * providers.<name>.cli fixtures), because real provider auth cannot exist in
 * CI.
 *
 * Determinism/bounds:
 *  - parallel workers are safe: every journey runs in its own throwaway
 *    HOME/userData/runtime/workspace with its own server port (per-world
 *    discovery file), leak assertions are scoped to that world's root, and
 *    the only global step — the orphan sweep — runs once in global-setup
 *    before any test;
 *  - one packaged build reused across all journeys (global-setup builds only
 *    when dist is missing or stale vs HEAD);
 *  - every journey must leave no processes behind (asserted in its teardown).
 */
import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './journeys',
  globalSetup: './global-setup.ts',
  fullyParallel: true,
  workers: 3,
  retries: 0,
  forbidOnly: !!process.env['CI'],
  // Generous but bounded: a journey covers launch → server boot → full UI
  // flow; the whole suite must stay well under ten minutes.
  timeout: 240_000,
  expect: { timeout: 20_000 },
  reporter: [['list']],
  outputDir: '../../test-results/e2e',
});
