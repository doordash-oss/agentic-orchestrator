import fs from 'node:fs';
import path from 'node:path';
import { FuseV1Options, getCurrentFuseWire } from '@electron/fuses';
import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  launchApp,
  openSettings,
  persistAppLogs,
  type AppHandle,
} from '../helpers/app';
import {
  bundledServerBinary,
  packagedExecutable,
  packagedResourcesDir,
  readVerification,
} from '../helpers/packaged';
import { Transcript } from '../helpers/transcript';
import { createRepo, createWorld, destroyWorld } from '../helpers/world';

test('packaged diagnostics stay local, bounded, redacted, and clearable without touching runtime state', async ({}, testInfo) => {
  const transcript = new Transcript(
    'diagnostics-production-posture',
    'Packaged diagnostics and production posture',
  );
  const world = createWorld('diagnostics', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'alpha', { commit: true });
  seedDiagnosticsForPruning(world.userData);
  let handle: AppHandle | null = null;

  try {
    const executable = packagedExecutable();
    await expectHardenedFuses(executable);
    transcript.step('verified hardened Electron fuses on the packaged executable');

    handle = await launchApp(world, testInfo, { traceName: 'diagnostics-production-posture' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });

    const apiSurface = await handle.page.evaluate(() => Object.keys(window.agentico).sort());
    expect(apiSurface).toContain('getDiagnostics');
    expect(apiSurface).toContain('clearDiagnostics');
    expect(apiSurface).not.toContain('exportDiagnostics');
    expect(apiSurface).not.toContain('uploadDiagnostics');

    const before = await handle.page.evaluate(() => window.agentico.getDiagnostics());
    expect(before.retention.maxAgeDays).toBe(7);
    expect(before.retention.maxBytes).toBe(25 * 1024 * 1024);
    expect(before.retention.maxCrashRecords).toBe(10);
    expect(before.retention.currentBytes).toBeLessThanOrEqual(before.retention.maxBytes);
    expect(before.entries.length).toBeLessThanOrEqual(200);
    expect(before.crashes).toHaveLength(10);
    expect(before.entries.some((entry) => entry.message === 'stale event')).toBe(false);
    expect(before.entries.some((entry) => entry.id === 'evt-oversized')).toBe(false);
    expect(before.crashes.some((crash) => crash.id === 'crash-oversized')).toBe(false);
    expect(
      before.crashes.every((crash) =>
        Object.keys(crash).every((key) =>
          [
            'id',
            'time',
            'version',
            'revision',
            'platform',
            'architecture',
            'processRole',
            'category',
            'context',
          ].includes(key),
        ),
      ),
    ).toBe(true);
    expect(JSON.stringify(before)).not.toContain(world.home);
    expect(JSON.stringify(before)).not.toContain('diagnosticsRoot');
    transcript.json('diagnostics before clear', before);

    await openSettings(handle);
    await expect(handle.page.getByRole('heading', { name: 'Diagnostics' })).toBeVisible();
    await expect(handle.page.getByRole('button', { name: 'Reveal Folder' })).toBeVisible();
    await handle.page.getByRole('button', { name: 'Clear Diagnostics' }).click();
    await expect(
      handle.page.getByRole('dialog', { name: 'Clear diagnostics confirmation' }),
    ).toBeVisible();
    await handle.page
      .getByRole('dialog', { name: 'Clear diagnostics confirmation' })
      .getByRole('button', { name: 'Clear Diagnostics' })
      .click();

    const after = await handle.page.evaluate(() => window.agentico.getDiagnostics());
    expect(after.crashes).toEqual([]);
    expect(after.entries.length).toBeLessThanOrEqual(20);
    expect(JSON.stringify(after)).not.toContain(world.home);
    expect(fs.existsSync(world.stateDir)).toBe(true);
    transcript.step('Clear Diagnostics removed local diagnostics only; runtime state survived');

    const stress = await measureBoundedStress(handle);
    expect(stress.tabSwitchMedianMs).toBeLessThanOrEqual(120);
    expect(stress.updateRefreshMedianMs).toBeLessThanOrEqual(120);
    expect(stress.heapDeltaBytes).toBeLessThan(12 * 1024 * 1024);
    transcript.json('bounded packaged stress measurements', stress);

    const resources = packagedResourcesDir(executable);
    const server = bundledServerBinary(executable);
    expect(fs.existsSync(resources)).toBe(true);
    expect(fs.existsSync(path.join(resources, 'app.asar'))).toBe(true);
    expect(fs.existsSync(server)).toBe(true);
    expect(fs.statSync(server).mode & 0o111).not.toBe(0);
    const verification = readVerification();
    expect(verification?.unpacked_app).toBe(executable);
    transcript.step('verified packaged read-only/spaced/non-ASCII resource resolution inputs');
    persistAppLogs(handle, 'diagnostics-production-posture');
  } finally {
    if (handle !== null) await closeApp(handle);
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

const FUSE_ENABLE = 49;
const FUSE_DISABLE = 48;
const EXPECTED_FUSES = new Map([
  [FuseV1Options.RunAsNode, { name: 'RunAsNode', value: FUSE_DISABLE }],
  [
    FuseV1Options.EnableNodeOptionsEnvironmentVariable,
    { name: 'EnableNodeOptionsEnvironmentVariable', value: FUSE_DISABLE },
  ],
  [
    FuseV1Options.EnableNodeCliInspectArguments,
    { name: 'EnableNodeCliInspectArguments', value: FUSE_DISABLE },
  ],
  [
    FuseV1Options.EnableEmbeddedAsarIntegrityValidation,
    { name: 'EnableEmbeddedAsarIntegrityValidation', value: FUSE_ENABLE },
  ],
  [FuseV1Options.OnlyLoadAppFromAsar, { name: 'OnlyLoadAppFromAsar', value: FUSE_ENABLE }],
  [
    FuseV1Options.GrantFileProtocolExtraPrivileges,
    { name: 'GrantFileProtocolExtraPrivileges', value: FUSE_DISABLE },
  ],
]);

async function expectHardenedFuses(executable: string): Promise<void> {
  const fuses = await getCurrentFuseWire(executable);
  for (const [index, expected] of EXPECTED_FUSES.entries()) {
    expect(fuses[index], expected.name).toBe(expected.value);
  }
}

function seedDiagnosticsForPruning(userData: string): void {
  const diagnosticsRoot = path.join(userData, 'diagnostics');
  fs.mkdirSync(diagnosticsRoot, { recursive: true, mode: 0o700 });
  const now = Date.now();
  const recent = new Date(now - 60_000).toISOString();
  const stale = new Date(now - 10 * 24 * 60 * 60 * 1000).toISOString();
  const entries = [
    diagnosticEntry('evt-stale', stale, 'stale event'),
    ...Array.from({ length: 230 }, (_, index) =>
      diagnosticEntry(`evt-${index}`, recent, `bounded event ${index}`),
    ),
    {
      ...diagnosticEntry('evt-oversized', recent, 'oversized event'),
      detail: 'x'.repeat(1201),
    },
  ];
  fs.writeFileSync(
    path.join(diagnosticsRoot, 'events.jsonl'),
    `${entries.map((entry) => JSON.stringify(entry)).join('\n')}\n`,
    { mode: 0o600 },
  );

  const crashes = [
    ...Array.from({ length: 14 }, (_, index) => crashRecord(`crash-${index}`, recent, index)),
    crashRecord('crash-stale', stale, 99),
    {
      ...crashRecord('crash-oversized', recent, 100),
      category: 'x'.repeat(81),
    },
  ];
  fs.writeFileSync(path.join(diagnosticsRoot, 'crashes.json'), `${JSON.stringify(crashes)}\n`, {
    mode: 0o600,
  });
}

function diagnosticEntry(id: string, time: string, message: string) {
  return {
    id,
    time,
    source: 'update',
    level: 'info',
    message,
    detail: 'redacted bounded detail',
  };
}

function crashRecord(id: string, time: string, index: number) {
  return {
    id,
    time,
    version: '0.1.0',
    platform: process.platform,
    architecture: process.arch,
    processRole: 'main',
    category: `bounded crash ${index}`,
    context: 'metadata only',
  };
}

async function measureBoundedStress(handle: AppHandle): Promise<{
  tabSwitchMedianMs: number;
  updateRefreshMedianMs: number;
  heapDeltaBytes: number;
}> {
  const beforeHeap = await rendererHeap(handle);
  const tabSamples: number[] = [];
  const updateSamples: number[] = [];
  for (let i = 0; i < 8; i += 1) {
    tabSamples.push(
      await handle.page.evaluate(async () => {
        const start = performance.now();
        await window.agentico.listFeatures();
        await window.agentico.listSessions();
        return performance.now() - start;
      }),
    );
    updateSamples.push(
      await handle.page.evaluate(async () => {
        const start = performance.now();
        await window.agentico.getUpdates();
        await window.agentico.getDiagnostics();
        return performance.now() - start;
      }),
    );
  }
  const afterHeap = await rendererHeap(handle);
  return {
    tabSwitchMedianMs: median(tabSamples),
    updateRefreshMedianMs: median(updateSamples),
    heapDeltaBytes: afterHeap - beforeHeap,
  };
}

async function rendererHeap(handle: AppHandle): Promise<number> {
  return handle.page.evaluate(() => {
    const memory = (performance as Performance & { memory?: { usedJSHeapSize?: number } }).memory;
    return memory?.usedJSHeapSize ?? 0;
  });
}

function median(values: readonly number[]): number {
  const sorted = [...values].sort((a, b) => a - b);
  return sorted[Math.floor(sorted.length / 2)] ?? 0;
}
