/**
 * Journey 4 — incompatible external runtime: a live loopback process
 * presents a health response whose compatibility declaration this app does
 * not support (foreign schema series + runtime policy), with a matching
 * owner-only discovery record. The app must hard-block with guidance,
 * offer no way to stop the foreign process, never present credentials to
 * it, and leave it running.
 */
import { spawn, type ChildProcess } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  evidenceShot,
  launchApp,
  persistAppLogs,
  type AppHandle,
} from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import {
  createWorld,
  destroyWorld,
  discoveryPath,
  minimalEnv,
  processAlive,
  waitFor,
} from '../helpers/world';

const fixturesDir = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'fixtures');

test('incompatible external runtime: blocked with guidance, never stopped', async ({}, testInfo) => {
  const transcript = new Transcript(
    'ownership-compatibility',
    'Journey 4 — incompatible external runtime is blocked and never stopped',
    { append: true },
  );
  const world = createWorld('incompatible', { auth: { loggedIn: true } });

  let stub: ChildProcess | null = null;
  let handle: AppHandle | null = null;
  const requestLog = path.join(world.root, 'stub-requests.log');
  try {
    transcript.section('A foreign runtime occupies the selected runtime dir');
    // The state dir must exist so its canonical (symlink-free) identity can
    // be computed the same way the app computes it.
    fs.mkdirSync(world.stateDir, { recursive: true });
    const canonicalStateDir = fs.realpathSync(world.stateDir);
    stub = spawn(
      process.execPath,
      [
        path.join(fixturesDir, 'incompatible-server.mjs'),
        '--state-dir',
        canonicalStateDir,
        '--log',
        requestLog,
      ],
      { env: minimalEnv(world), stdio: ['ignore', 'pipe', 'pipe'] },
    );
    const port = await readPort(stub);
    const baseUrl = `http://127.0.0.1:${port}`;
    const discovery = {
      schema_version: 1,
      api_version: 'v1',
      base_url: baseUrl,
      auth_token: 'e2e-foreign-token-the-app-must-never-send',
      runtime: {
        runtime_dir: path.dirname(canonicalStateDir),
        state_dir: canonicalStateDir,
        config_path: world.configPath,
      },
      pid: stub.pid,
      started_at: new Date().toISOString(),
    };
    fs.writeFileSync(discoveryPath(world), `${JSON.stringify(discovery, null, 2)}\n`, {
      mode: 0o600,
    });
    transcript.step(`stub runtime pid ${stub.pid} listening at ${baseUrl}`);
    transcript.json('health compatibility declaration the stub presents', {
      api_version: 'v1',
      schema_version: 99,
      min_client_schema: 99,
      runtime_policy: 'quantum-entangled-v99',
    });

    transcript.section('The app blocks: incompatible state with guidance, no stop affordance');
    handle = await launchApp(world, testInfo, { traceName: 'incompatible-block' });
    const shell = handle.page.getByLabel('Agentico connection');
    await expect(shell).toBeVisible();
    await expect(
      handle.page.locator('.shell-card__status-label[data-status="incompatible"]'),
    ).toBeVisible({ timeout: 30_000 });
    await expect(shell).toContainText('Incompatible');
    await expect(shell.locator('[data-ownership="external"]')).toBeVisible();
    await expect(shell).toContainText('E_INCOMPATIBLE_SERVER');
    await expect(shell).toContainText('This app never shuts down a runtime it does not own');
    // The only affordance is Retry; there is no way to stop the foreign process.
    const buttons = await shell.getByRole('button').allTextContents();
    expect(buttons).toEqual(['Retry']);
    await evidenceShot(handle, 'incompatible-blocked');
    const connection = await handle.page.evaluate(() => window.agentico.getConnectionStatus());
    transcript.json('connection state (via IPC): blocked, external ownership', connection);
    expect(connection.status).toBe('incompatible');
    expect(connection.ownership).toBe('external');

    transcript.section('The app only ever probed health — no credential was presented');
    persistAppLogs(handle, 'incompatible-app');
    await closeApp(handle);
    handle = null;
    const requests = fs
      .readFileSync(requestLog, 'utf8')
      .trim()
      .split('\n')
      .map(
        (line) => JSON.parse(line) as { method: string; path: string; has_authorization: boolean },
      );
    expect(requests.length).toBeGreaterThan(0);
    for (const request of requests) {
      expect(request.method).toBe('GET');
      expect(request.path).toBe('/api/v1/health');
      expect(request.has_authorization).toBe(false);
    }
    transcript.json('every request the stub received', requests);

    transcript.section('The foreign process survives the app');
    expect(processAlive(stub.pid!)).toBe(true);
    expect(stub.exitCode).toBeNull();
    transcript.step(
      `stub pid ${stub.pid} still alive after the app quit; test tears it down itself`,
    );
    stub.kill('SIGTERM');
    await waitFor(
      () => stub!.exitCode !== null || stub!.signalCode !== null,
      'stub exit after test-sent SIGTERM',
      15_000,
    );
    assertNoLeakedProcesses(world);
    transcript.write(testInfo);
  } finally {
    if (handle !== null) {
      await closeApp(handle).catch(() => {});
    }
    if (stub !== null && stub.exitCode === null && stub.signalCode === null) {
      stub.kill('SIGKILL');
    }
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

async function readPort(child: ChildProcess): Promise<number> {
  return new Promise<number>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('stub server did not report a port')), 15_000);
    let buffer = '';
    child.stdout?.on('data', (chunk: Buffer) => {
      buffer += chunk.toString();
      const match = /PORT (\d+)/.exec(buffer);
      if (match !== null) {
        clearTimeout(timer);
        resolve(Number(match[1]));
      }
    });
    child.on('exit', () => {
      clearTimeout(timer);
      reject(new Error('stub server exited before reporting a port'));
    });
  });
}
