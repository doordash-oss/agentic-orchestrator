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
 * Multi-server startup selection (packaged app): several real `agentico
 * server` processes share one HOME, each publishing its registry entry. The
 * app's startup matrix runs entirely through the real packaged surfaces:
 *   two live servers            → picker; the choice attaches
 *   relaunch                    → silent reconnect to the last-used server
 *   last-used killed, other up  → picker again (dead entry pruned)
 *   every server killed         → unchanged spawn fallback
 * The journey also pins that the picker never exposes tokens and that
 * registry entries live under the world's isolated HOME with owner-only
 * permissions.
 */
import { spawn, type ChildProcess } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  evidenceShot,
  launchApp,
  persistAppLogs,
  type AppHandle,
} from '../helpers/app';
import { bundledServerBinary, packagedExecutable } from '../helpers/packaged';
import { Transcript } from '../helpers/transcript';
import { tailText } from '../helpers/runtime';
import {
  createWorld,
  destroyWorld,
  minimalEnv,
  readDiscovery,
  waitFor,
  type JourneyWorld,
} from '../helpers/world';

interface TestServer {
  name: string;
  runtimeDir: string;
  stateDir: string;
  configPath: string;
  proc: ChildProcess;
  logs: string[];
}

interface RegistryEntry {
  name?: string;
  auth_token?: string;
  runtime: { runtime_dir: string };
  pid: number;
  base_url: string;
}

function registryDir(world: JourneyWorld): string {
  return path.join(world.home, '.agentic-orchestrator', 'servers');
}

function readRegistry(world: JourneyWorld): RegistryEntry[] {
  let names: string[];
  try {
    names = fs.readdirSync(registryDir(world)).filter((name) => name.endsWith('.json'));
  } catch {
    return [];
  }
  return names.map(
    (name) =>
      JSON.parse(fs.readFileSync(path.join(registryDir(world), name), 'utf8')) as RegistryEntry,
  );
}

/** Starts a test-owned server with its own runtime dir, name, and port. */
function startTestServer(
  world: JourneyWorld,
  name: string,
  runtimeDir: string,
  listenPort?: number,
): TestServer {
  const runtimePath = path.join(world.root, runtimeDir);
  const stateDir = path.join(runtimePath, 'features');
  const configPath = path.join(runtimePath, 'config.yaml');
  fs.mkdirSync(stateDir, { recursive: true });
  // The stub-provider config is location-independent (absolute stub paths).
  fs.copyFileSync(world.configPath, configPath);
  const args = [
    'server',
    '--config',
    configPath,
    '--state-dir',
    stateDir,
    '--name',
    name,
    ...(listenPort === undefined ? [] : ['--listen', String(listenPort)]),
  ];
  const logs: string[] = [];
  const proc = spawn(bundledServerBinary(packagedExecutable()), args, {
    env: minimalEnv(world),
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  proc.stdout?.on('data', (chunk: Buffer) => logs.push(chunk.toString()));
  proc.stderr?.on('data', (chunk: Buffer) => logs.push(chunk.toString()));
  return { name, runtimeDir: runtimePath, stateDir, configPath, proc, logs };
}

function discoveryAt(runtimeDir: string): { pid: number; base_url: string } | null {
  try {
    return JSON.parse(fs.readFileSync(path.join(runtimeDir, '.agentico-server.json'), 'utf8')) as {
      pid: number;
      base_url: string;
    };
  } catch {
    return null;
  }
}

async function stopServer(server: TestServer): Promise<void> {
  if (server.proc.exitCode !== null || server.proc.signalCode !== null) {
    return;
  }
  server.proc.kill('SIGKILL');
  await waitFor(
    () => server.proc.exitCode !== null || server.proc.signalCode !== null,
    `${server.name} to exit`,
    15_000,
  ).catch(() => {});
}

/** The picker's listbox, scoped to the current app window. */
function pickerListbox(handle: AppHandle) {
  return handle.page.getByRole('listbox', { name: /running agentico servers/i });
}

test('multi-server startup: picker, silent reconnect, re-pick, spawn fallback', async ({}, testInfo) => {
  const transcript = new Transcript(
    'server-selection',
    'Multi-server startup selection (packaged app)',
  );
  const world = createWorld('server-selection', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const servers: TestServer[] = [];
  let handle: AppHandle | null = null;
  try {
    transcript.section('Start two named servers sharing one HOME');
    const alpha = startTestServer(world, 'alpha', 'runtime-alpha');
    const beta = startTestServer(world, 'beta', 'runtime-beta');
    const gamma = startTestServer(world, 'gamma', 'runtime-gamma');
    servers.push(alpha, beta, gamma);
    transcript.command('agentico server --name alpha (own runtime) &', '(test-owned process)');
    transcript.command('agentico server --name beta (own runtime) &', '(test-owned process)');
    transcript.command('agentico server --name gamma (own runtime) &', '(test-owned process)');

    await waitFor(() => discoveryAt(alpha.runtimeDir) !== null, 'alpha discovery record', 30_000);
    await waitFor(() => discoveryAt(beta.runtimeDir) !== null, 'beta discovery record', 30_000);
    await waitFor(() => discoveryAt(gamma.runtimeDir) !== null, 'gamma discovery record', 30_000);

    // All servers published their owner-only registry entries under the
    // world's isolated HOME — one file each, no accumulation.
    await waitFor(() => readRegistry(world).length === 3, 'three registry entries', 30_000);
    const dirMode = fs.statSync(registryDir(world)).mode & 0o777;
    expect(dirMode).toBe(0o700);
    for (const file of fs.readdirSync(registryDir(world))) {
      expect(fs.statSync(path.join(registryDir(world), file)).mode & 0o777).toBe(0o600);
    }
    const registryNames = readRegistry(world)
      .map((entry) => entry.name)
      .sort();
    expect(registryNames).toEqual(['alpha', 'beta', 'gamma']);
    const registryTokens = readRegistry(world).flatMap((entry) =>
      entry.auth_token === undefined || entry.auth_token === '' ? [] : [entry.auth_token],
    );
    expect(registryTokens).toHaveLength(3);
    transcript.json('registry entries (tokens redacted)', {
      names: registryNames,
      dirMode: dirMode.toString(8),
      fileMode: '600',
    });

    transcript.section('First launch: the picker offers both servers (attach-only, snapshot)');
    handle = await launchApp(world, testInfo, { traceName: 'server-selection-picker' });
    await expect(pickerListbox(handle)).toBeVisible({ timeout: 60_000 });
    const options = handle.page.getByRole('option');
    await expect(options).toHaveCount(3);
    await expect(options.filter({ hasText: 'alpha' })).toHaveCount(1);
    await expect(options.filter({ hasText: 'beta' })).toHaveCount(1);
    await expect(options.filter({ hasText: 'gamma' })).toHaveCount(1);
    await expect(options.filter({ hasText: alpha.runtimeDir })).toHaveCount(1);
    await expect(options.filter({ hasText: beta.runtimeDir })).toHaveCount(1);

    // The picker's IPC-visible snapshot must never expose tokens.
    const pickerState = await handle.page.evaluate(() => window.agentico.getConnectionStatus());
    expect(pickerState.status).toBe('awaiting-server-choice');
    for (const token of registryTokens) {
      expect(JSON.stringify(pickerState)).not.toContain(token);
    }
    transcript.step('the awaiting-server-choice snapshot carries no token material');

    transcript.section('Pick beta: it attaches with external ownership');
    await options.filter({ hasText: 'beta' }).click();
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const attached = await handle.page.evaluate(() => window.agentico.getConnectionStatus());
    expect(attached.status).toBe('ready');
    expect(attached.ownership).toBe('external');
    expect(attached.serverName).toBe('beta');
    transcript.json('connection state after choosing beta (via IPC)', attached);
    await evidenceShot(handle, 'server-selection-attached');

    // The choice was persisted: last-used pointer set in the world settings.
    const settings = await handle.page.evaluate(() => window.agentico.getSettings());
    expect(settings.servers.lastUsed).not.toBeNull();
    expect(settings.servers.known.some((entry) => entry.name === 'beta')).toBe(true);
    for (const token of registryTokens) {
      expect(JSON.stringify(settings)).not.toContain(token);
    }

    transcript.section('Relaunch: silent reconnect to beta, no render of the picker');
    persistAppLogs(handle, 'server-selection-first-app');
    await closeApp(handle);
    handle = null;
    handle = await launchApp(world, testInfo, { traceName: 'server-selection-relaunch' });
    // No interaction: a picker outcome here would stall on this expectation.
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const reconnected = await handle.page.evaluate(() => window.agentico.getConnectionStatus());
    expect(reconnected.status).toBe('ready');
    expect(reconnected.ownership).toBe('external');
    expect(reconnected.serverName).toBe('beta');
    await expect(pickerListbox(handle)).toHaveCount(0);
    transcript.step('relaunch silently reconnected to beta (last-used pointer won)');

    transcript.section('Kill the last-used server: picker returns, pruned to the survivors');
    await stopServer(beta);
    persistAppLogs(handle, 'server-selection-relaunch-app');
    await closeApp(handle);
    handle = null;
    handle = await launchApp(world, testInfo, { traceName: 'server-selection-repick' });
    await expect(pickerListbox(handle)).toBeVisible({ timeout: 60_000 });
    const repickOptions = handle.page.getByRole('option');
    await expect(repickOptions).toHaveCount(2); // beta's dead entry was pruned
    await expect(repickOptions.filter({ hasText: 'alpha' })).toHaveCount(1);
    await expect(repickOptions.filter({ hasText: 'gamma' })).toHaveCount(1);
    transcript.step('beta (crashed, last-used) was pruned; the picker offers alpha and gamma');

    await repickOptions.filter({ hasText: 'alpha' }).click();
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const reAttached = await handle.page.evaluate(() => window.agentico.getConnectionStatus());
    expect(reAttached.status).toBe('ready');
    expect(reAttached.ownership).toBe('external');
    expect(reAttached.serverName).toBe('alpha');

    transcript.section('Kill every server: the spawn fallback is unchanged');
    await stopServer(alpha);
    await stopServer(gamma);
    persistAppLogs(handle, 'server-selection-repick-app');
    await closeApp(handle);
    handle = null;
    handle = await launchApp(world, testInfo, { traceName: 'server-selection-spawn-fallback' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const spawned = await handle.page.evaluate(() => window.agentico.getConnectionStatus());
    expect(spawned.status).toBe('ready');
    expect(spawned.ownership).toBe('app-owned');
    await expect(pickerListbox(handle)).toHaveCount(0);
    // The dead entries were pruned and the app-owned child published its own.
    const finalRegistry = readRegistry(world);
    expect(finalRegistry).toHaveLength(1);
    const spawnedDiscovery = readDiscovery(world);
    expect(spawnedDiscovery).not.toBeNull();
    expect(finalRegistry[0]?.pid).toBe(spawnedDiscovery?.pid);
    transcript.step('empty registry fell back to spawning; the new server registered itself');

    persistAppLogs(handle, 'server-selection-final-app');
    transcript.write(testInfo);
  } finally {
    if (handle !== null) {
      await closeApp(handle).catch(() => {});
    }
    for (const server of servers) {
      await stopServer(server);
    }
    for (const server of servers) {
      if (server.logs.length > 0) {
        transcript.codeBlock(`${server.name} stderr tail`, tailText(server.logs.join(''), 15));
      }
    }
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

test('graceful shutdown retracts the registry entry (crash-kill leaves it for pruning)', async () => {
  const world = createWorld('server-selection-retract', {
    auth: { loggedIn: true },
    presetWorkspaceRoot: true,
  });
  let server: TestServer | null = null;
  try {
    server = startTestServer(world, 'retractable', 'runtime-retractable');
    await waitFor(() => readRegistry(world).length === 1, 'registry entry', 30_000);
    const gracefulRuntime = server.runtimeDir;

    server.proc.kill('SIGTERM');
    await waitFor(
      () => server !== null && (server.proc.exitCode !== null || server.proc.signalCode !== null),
      'graceful exit',
      15_000,
    );
    // Registry entry removed; the per-runtime discovery file stays (unchanged
    // legacy behavior).
    await waitFor(() => readRegistry(world).length === 0, 'registry retraction', 15_000);
    expect(discoveryAt(gracefulRuntime)).not.toBeNull();

    // A crash-killed server leaves its entry behind for client-side pruning.
    server = startTestServer(world, 'crashy', 'runtime-crashy');
    await waitFor(() => readRegistry(world).length === 1, 'crashy registry entry', 30_000);
    await stopServer(server);
    await new Promise((resolve) => setTimeout(resolve, 500));
    expect(readRegistry(world)).toHaveLength(1);
  } finally {
    if (server !== null) {
      await stopServer(server);
    }
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});
