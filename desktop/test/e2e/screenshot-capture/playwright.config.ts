import { defineConfig } from '@playwright/test';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  testDir: __dirname,
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 120_000,
  expect: { timeout: 15_000 },
  reporter: [['list']],
  outputDir: path.resolve(__dirname, '../../../test-results/screenshots'),
  use: {
    baseURL: 'http://localhost:9871',
  },
  webServer: {
    command: 'npx vite --port 9871 --strictPort',
    cwd: __dirname,
    port: 9871,
    reuseExistingServer: true,
    timeout: 30_000,
  },
});
