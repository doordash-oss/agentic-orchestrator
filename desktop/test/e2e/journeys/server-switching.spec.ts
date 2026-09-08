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
 * In-app server switching (packaged app): two real `agentico server`
 * processes share one HOME; the footer server control switches the workspace
 * between them with no restart.
 *   switch A→B→A     → each server's own feature truth and selection restore
 *   killed target    → the switch fails with Retry and "Back to <name>"
 *   app-owned child  → survives the switch-away, still stopped on quit
 * The journey also pins that no token ever crosses an IPC-visible surface.
 */
import { spawn, type ChildProcess } from 'node:child_process';
import fs from 'node:fs';
import net from 'node:net';
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
  createRepo,
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
  /** Fixed listen address when the server must return on the same one. */
  listen?: string;
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

/** Reserves and releases an ephemeral port for a fixed-address server. */
async function pickFreePort(): Promise<number> {
  const server = net.createServer();
  const listening = new Promise<void>((resolve) => server.once('listening', () => resolve()));
  server.listen(0, '127.0.0.1');
  await listening;
  const address = server.address();
  const port = typeof address === 'object' && address !== null ? address.port : 0;
  server.close();
  if (port === 0) {
    throw new Error('could not reserve a fixed listen port');
  }
  return port;
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
  listen?: string,
): TestServer {
  const runtimePath = path.join(world.root, runtimeDir);
  const stateDir = path.join(runtimePath, 'features');
  const configPath = path.join(runtimePath, 'config.yaml');
  fs.mkdirSync(stateDir, { recursive: true });
  fs.copyFileSync(world.configPath, configPath);
  const server: TestServer = {
    name,
    runtimeDir: runtimePath,
    stateDir,
    configPath,
    listen,
    proc: null as unknown as ChildProcess,
    logs: [],
  };
  server.proc = spawnServer(world, server);
  return server;
}

/** (Re)spawns a test server; a fixed listen address returns on the same one. */
function spawnServer(world: JourneyWorld, server: TestServer): ChildProcess {
  const args = [
    'server',
    '--config',
    server.configPath,
    '--state-dir',
    server.stateDir,
    '--name',
    server.name,
  ];
  if (server.listen !== undefined) args.push('--listen', server.listen);
  const proc = spawn(bundledServerBinary(packagedExecutable()), args, {
    env: minimalEnv(world),
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  proc.stdout?.on('data', (chunk: Buffer) => server.logs.push(chunk.toString()));
  proc.stderr?.on('data', (chunk: Buffer) => server.logs.push(chunk.toString()));
  return proc;
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

function pidAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

/** The switcher popover's listbox. */
function switcherListbox(handle: AppHandle) {
  return handle.page.getByRole('listbox', { name: 'Servers' });
}

/** Opens the footer switcher and waits for its rows. */
async function openSwitcher(handle: AppHandle, currentName: string) {
  await handle.page.getByRole('button', { name: `${currentName} — switch server` }).click();
  await expect(switcherListbox(handle)).toBeVisible({ timeout: 30_000 });
}

async function connectionState(handle: AppHandle) {
  return handle.page.evaluate(() => window.agentico.getConnectionStatus());
}

const FEATURE_NAME = 'Switching Anchor Fixture';

test("two-server switching: A→B→A restores each server's truth and selection", async ({}, testInfo) => {
  const transcript = new Transcript('server-switching', 'In-app two-server switching (packaged)');
  const world = createWorld('server-switching', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'switch-lab', { commit: true });
  const servers: TestServer[] = [];
  let handle: AppHandle | null = null;
  try {
    transcript.section('Start two named servers sharing one HOME');
    // alpha listens on a fixed address so it can die and return on the
    // same one, which is what a retained creation draft keys on.
    const alphaListen = `127.0.0.1:${await pickFreePort()}`;
    const alpha = startTestServer(world, 'alpha', 'runtime-alpha', alphaListen);
    const beta = startTestServer(world, 'beta', 'runtime-beta');
    servers.push(alpha, beta);
    await waitFor(() => discoveryAt(alpha.runtimeDir) !== null, 'alpha discovery record', 30_000);
    await waitFor(() => discoveryAt(beta.runtimeDir) !== null, 'beta discovery record', 30_000);
    await waitFor(() => readRegistry(world).length === 2, 'two registry entries', 30_000);
    const registryTokens = readRegistry(world).flatMap((entry) =>
      entry.auth_token === undefined || entry.auth_token === '' ? [] : [entry.auth_token],
    );
    expect(registryTokens).toHaveLength(2);

    transcript.section('Attach to alpha from the startup picker and create a feature');
    handle = await launchApp(world, testInfo, { traceName: 'server-switching-picker' });
    const picker = handle.page.getByRole('listbox', { name: /running agentico servers/i });
    await expect(picker).toBeVisible({ timeout: 60_000 });
    await picker.getByRole('option').filter({ hasText: 'alpha' }).click();
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    expect((await connectionState(handle)).serverName).toBe('alpha');

    const featureId = await handle.page.evaluate(
      async (name) =>
        (
          await window.agentico.createFeature({
            name,
            description: 'Selection anchor for the switching journey.',
            repoKeys: ['switch-lab'],
            useCurrentBranch: false,
          })
        ).featureId,
      FEATURE_NAME,
    );
    await handle.page.getByRole('option', { name: new RegExp(FEATURE_NAME) }).click();
    const alphaSettings = await handle.page.evaluate(() => window.agentico.getSettings());
    const alphaKey = alphaSettings.servers.lastUsed;
    expect(alphaKey).not.toBeNull();
    expect(alphaSettings.shell.featureByServer[alphaKey!]).toBe(featureId);
    for (const token of registryTokens) {
      expect(JSON.stringify(alphaSettings)).not.toContain(token);
    }
    await evidenceShot(handle, 'server-switching-alpha-selected');

    transcript.section('Open the footer switcher: rows carry live health');
    await openSwitcher(handle, 'alpha');
    const alphaRow = handle.page.getByRole('option', { name: /alpha .* — Connected/ });
    await expect(alphaRow).toBeVisible();
    await expect(alphaRow).toHaveAttribute('aria-disabled', 'true');
    const betaRow = handle.page.getByRole('option', {
      name: /beta at .+ — Available/,
    });
    await expect(betaRow).toBeVisible({ timeout: 30_000 });
    // The runtime dir disambiguates same-named servers (canonicalized
    // server-side: /var → /private/var on macOS).
    await expect(betaRow).toContainText('runtime-beta');
    transcript.step('rows show name, runtime dir, a Connected marker, and live health');
    await evidenceShot(handle, 'server-switching-popover');

    transcript.section('Switch to beta: the workspace remounts with beta truth');
    await betaRow.click();
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const betaState = await connectionState(handle);
    expect(betaState.status).toBe('ready');
    expect(betaState.serverName).toBe('beta');
    // beta's own truth: alpha's feature is nowhere on this server.
    await expect(handle.page.getByRole('option', { name: new RegExp(FEATURE_NAME) })).toHaveCount(
      0,
    );
    // beta had no recorded selection: the shell lands on Overview.
    await expect(handle.page.getByRole('option', { name: 'Overview' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    const betaSettings = await handle.page.evaluate(() => window.agentico.getSettings());
    expect(betaSettings.servers.lastUsed).toBe(betaState.serverKey);
    expect(betaState.serverKey).not.toBe(alphaKey);
    for (const token of registryTokens) {
      expect(JSON.stringify(betaSettings)).not.toContain(token);
      expect(JSON.stringify(betaState)).not.toContain(token);
    }
    await evidenceShot(handle, 'server-switching-beta-workspace');

    transcript.section('Switch back to alpha: selection restores');
    await openSwitcher(handle, 'beta');
    await handle.page.getByRole('option', { name: /alpha at .+ — Available/ }).click();
    // The restored workspace lands on alpha's recorded selection (the
    // feature, not Overview — so no New feature button this time).
    await waitFor(
      async () => (await connectionState(handle!)).serverName === 'alpha',
      'alpha re-attach',
      60_000,
    );
    await expect(
      handle.page.getByRole('option', { name: new RegExp(FEATURE_NAME) }),
    ).toHaveAttribute('aria-selected', 'true', { timeout: 60_000 });
    const restoredSettings = await handle.page.evaluate(() => window.agentico.getSettings());
    expect(restoredSettings.shell.featureByServer[alphaKey!]).toBe(featureId);
    transcript.step('A→B→A: alpha workspace, feature list, and selection all restored');

    transcript.section('Creation drafts survive a real connection flip');
    // The creation sheet is window-modal, so the in-app switcher is not the
    // boundary a draft crosses: a server-level disconnect is. Alpha opens a
    // draft and gives it user-owned values first.
    await handle.page.getByRole('option', { name: 'Overview' }).click();
    await handle.page.getByRole('button', { name: 'New feature' }).click();
    const alphaSheet = handle.page.getByRole('dialog', { name: 'New feature' });
    await expect(alphaSheet).toBeVisible({ timeout: 30_000 });
    await alphaSheet.getByRole('checkbox', { name: /switch-lab/ }).click();
    await alphaSheet.getByRole('radio', { name: 'Current branches' }).click();
    await expect(
      alphaSheet.getByRole('checkbox', { name: /switch-lab.*Source: main/ }),
    ).toBeChecked();
    await alphaSheet.getByRole('button', { name: 'Next: Describe' }).click();
    await alphaSheet.getByLabel('Name').fill('Alpha retained draft');

    // Alpha dies: the ready tree — and with it the sheet — unmounts, and
    // the connection shell takes over while the draft stays retained.
    await stopServer(alpha);
    await expect(handle.page.getByRole('dialog', { name: 'New feature' })).toHaveCount(0, {
      timeout: 60_000,
    });
    await expect(handle.page.getByRole('region', { name: 'Agentico connection' })).toBeVisible({
      timeout: 60_000,
    });
    transcript.step('dead alpha unmounted the ready tree with the draft retained');

    // A retry while alpha is still down rescans and falls back to the live
    // beta: the ready tree remounts on beta's workspace, alpha's creation
    // sheet stays unmounted, and the retained draft never leaks into it.
    await handle.page.getByRole('button', { name: 'Retry' }).click();
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    await expect(handle.page.getByRole('dialog', { name: 'New feature' })).toHaveCount(0);
    const fallbackState = await connectionState(handle);
    expect(fallbackState.status).toBe('ready');
    expect(fallbackState.serverName).toBe('beta');
    transcript.step('failed alpha retry fell back to beta with the draft retained');

    // Alpha returns on the same address: switching back re-attaches and the
    // draft restores verbatim — values, selection, branch mode, position.
    alpha.proc = spawnServer(world, alpha);
    await waitFor(() => discoveryAt(alpha.runtimeDir) !== null, 'alpha rediscovery', 30_000);
    await openSwitcher(handle, 'beta');
    await handle.page.getByRole('option', { name: /alpha at .+ — Available/ }).click();
    await waitFor(
      async () => (await connectionState(handle!)).serverName === 'alpha',
      'alpha re-attach for draft restore',
      60_000,
    );
    const restoredSheet = handle.page.getByRole('dialog', { name: 'New feature' });
    await expect(restoredSheet).toBeVisible({ timeout: 60_000 });
    await expect(restoredSheet.getByRole('heading', { name: 'Define the work' })).toBeVisible();
    await expect(restoredSheet.getByLabel('Name')).toHaveValue('Alpha retained draft');
    await restoredSheet.getByRole('button', { name: 'Back' }).click();
    await expect(restoredSheet.getByRole('checkbox', { name: /switch-lab/ })).toBeChecked();
    await expect(restoredSheet.getByRole('radio', { name: 'Current branches' })).toBeChecked();
    await expect(
      restoredSheet.getByRole('checkbox', { name: /switch-lab.*Source: main/ }),
    ).toBeChecked();
    transcript.step('alpha creation draft restored verbatim across the connection flip');

    // An explicit discard retires the restored draft.
    await restoredSheet.getByRole('button', { name: 'Cancel' }).click();
    await restoredSheet.getByRole('button', { name: 'Discard draft' }).click();
    await expect(handle.page.getByRole('dialog', { name: 'New feature' })).toHaveCount(0);
    transcript.step('explicit discard retired the restored draft');

    persistAppLogs(handle, 'server-switching-app');
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

test('a killed target fails with retry and back-to-previous; back restores the session', async ({}, testInfo) => {
  const transcript = new Transcript(
    'server-switching-failure',
    'Failed switch recovery (packaged)',
  );
  const world = createWorld('server-switching-failure', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const servers: TestServer[] = [];
  let handle: AppHandle | null = null;
  try {
    const alpha = startTestServer(world, 'alpha', 'runtime-alpha');
    const beta = startTestServer(world, 'beta', 'runtime-beta');
    servers.push(alpha, beta);
    await waitFor(() => discoveryAt(alpha.runtimeDir) !== null, 'alpha discovery record', 30_000);
    await waitFor(() => discoveryAt(beta.runtimeDir) !== null, 'beta discovery record', 30_000);
    await waitFor(() => readRegistry(world).length === 2, 'two registry entries', 30_000);

    handle = await launchApp(world, testInfo, { traceName: 'server-switching-failure-picker' });
    const picker = handle.page.getByRole('listbox', { name: /running agentico servers/i });
    await expect(picker).toBeVisible({ timeout: 60_000 });
    await picker.getByRole('option').filter({ hasText: 'alpha' }).click();
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });

    transcript.section('Kill beta between the healthy probe and the click');
    await openSwitcher(handle, 'alpha');
    const betaRow = handle.page.getByRole('option', {
      name: /beta at .+ — Available/,
    });
    await expect(betaRow).toBeVisible({ timeout: 30_000 });
    await stopServer(beta);
    transcript.step('beta killed after the popover probed it healthy');

    transcript.section('The switch fails onto the error surface with both recovery actions');
    await betaRow.click();
    const errorCode = handle.page.locator('.error-surface__code');
    await expect(errorCode).toBeVisible({ timeout: 60_000 });
    await expect(errorCode).toHaveText('E_SWITCH_UNAVAILABLE');
    const retryButton = handle.page.getByRole('button', { name: 'Retry', exact: true });
    const backButton = handle.page.getByRole('button', { name: 'Back to alpha' });
    await expect(retryButton).toBeVisible();
    await expect(backButton).toBeVisible();
    const failedState = await connectionState(handle);
    expect(failedState.status).toBe('error');
    expect(JSON.stringify(failedState)).not.toContain('auth_token');
    await evidenceShot(handle, 'server-switching-failed');

    transcript.section('Retry re-attempts the (still dead) target and fails the same way');
    await retryButton.click();
    await expect(errorCode).toHaveText('E_SWITCH_UNAVAILABLE', { timeout: 60_000 });

    transcript.section('Back to alpha re-attaches and restores the session');
    await backButton.click();
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const restored = await connectionState(handle);
    expect(restored.status).toBe('ready');
    expect(restored.serverName).toBe('alpha');
    transcript.step('back-to-previous re-attached through the standard attach path');

    persistAppLogs(handle, 'server-switching-failure-app');
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

test('the app-owned child survives a switch-away and is still stopped on quit', async ({}, testInfo) => {
  const transcript = new Transcript(
    'server-switching-supervision',
    'Decoupled app-owned supervision (packaged)',
  );
  const world = createWorld('server-switching-supervision', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const servers: TestServer[] = [];
  let handle: AppHandle | null = null;
  try {
    // No external server: the app spawns and supervises its own child.
    handle = await launchApp(world, testInfo, { traceName: 'server-switching-supervision-spawn' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 90_000,
    });
    const spawnedState = await connectionState(handle);
    expect(spawnedState.status).toBe('ready');
    expect(spawnedState.ownership).toBe('app-owned');
    const ownedDiscovery = readDiscovery(world);
    expect(ownedDiscovery).not.toBeNull();
    const ownedPid = ownedDiscovery!.pid;
    expect(pidAlive(ownedPid)).toBe(true);

    transcript.section('Start an external server mid-session and switch to it');
    const beta = startTestServer(world, 'beta', 'runtime-beta');
    servers.push(beta);
    await waitFor(() => discoveryAt(beta.runtimeDir) !== null, 'beta discovery record', 30_000);
    await waitFor(() => readRegistry(world).length === 2, 'two registry entries', 30_000);

    // The spawned child names itself (generated server name), so the
    // footer label is read back from the state rather than assumed.
    const ownedLabel: string = spawnedState.serverName ?? 'Runtime ready';
    await handle.page.getByRole('button', { name: `${ownedLabel} — switch server` }).click();
    await expect(switcherListbox(handle)).toBeVisible({ timeout: 30_000 });
    await handle.page.getByRole('option', { name: /beta at .+ — Available/ }).click();
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    expect((await connectionState(handle)).serverName).toBe('beta');

    transcript.section('The left-behind child keeps running and never hijacks the surface');
    expect(pidAlive(ownedPid)).toBe(true);
    // No crashed/error takeover while attached to beta.
    await expect(handle.page.locator('.error-surface__code')).toHaveCount(0);
    expect((await connectionState(handle)).status).toBe('ready');
    transcript.step('app-owned child still alive while attached to the other server');

    transcript.section('Quit still stops the supervised child');
    persistAppLogs(handle, 'server-switching-supervision-app');
    await closeApp(handle);
    handle = null;
    await waitFor(() => !pidAlive(ownedPid), 'app-owned child to exit on quit', 30_000);
    transcript.step('shutdown() stopped the left-behind child unconditionally');

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
