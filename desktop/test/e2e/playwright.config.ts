/**
 * Packaged E2E journeys (Phase 1 Task 6b): Playwright drives the REAL
 * unsigned package produced by `npm run package:verify` — the unpacked app
 * recorded in dist/package-verification.json — with the real bundled Go
 * server. Provider CLIs are the only stubbed boundary (config
 * providers.<name>.cli fixtures), because real provider auth cannot exist in
 * CI.
 *
 * Determinism/bounds:
 *  - one worker, no parallelism: journeys share the host's process table and
 *    each launches a packaged Electron app + server;
 *  - one packaged build reused across all journeys (global-setup builds only
 *    when dist is missing or stale vs HEAD);
 *  - every journey runs in its own throwaway HOME/userData/runtime/workspace
 *    and must leave no processes behind (asserted in its teardown);
 *  - traces are always recorded (evidence runs consume them).
 */
import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './journeys',
  globalSetup: './global-setup.ts',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  forbidOnly: !!process.env['CI'],
  // Generous but bounded: a journey covers launch → server boot → full UI
  // flow; the whole suite must stay well under ten minutes.
  timeout: 240_000,
  expect: { timeout: 20_000 },
  reporter: [['list']],
  outputDir: '../../test-results/e2e',
});
