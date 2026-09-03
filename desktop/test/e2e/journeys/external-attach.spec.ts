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
 * Journey 3 — compatible external attach: the test starts the BUNDLED server
 * binary itself (external ownership), waits for its discovery record, then
 * launches the packaged app. The app must attach (ownership `external`),
 * and quitting the app must leave the external server running — the app
 * never signals a process it does not own.
 */
import { spawn, type ChildProcess } from 'node:child_process';
import fs from 'node:fs';
import net from 'node:net';
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
  processAlive,
  readDiscovery,
  waitFor,
} from '../helpers/world';

test('external compatible server: attach, never own, never stop', async ({}, testInfo) => {
  const transcript = new Transcript(
    'ownership-compatibility',
    'Journeys 3 + 4 — server ownership and compatibility (packaged app)',
  );
  const world = createWorld('external-attach', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'alpha', { commit: true });

  let external: ChildProcess | null = null;
  let handle: AppHandle | null = null;
  const externalLogs: string[] = [];
  try {
    transcript.section('Journey 3 — start the bundled server externally');
    const serverBinary = bundledServerBinary(packagedExecutable());
    const args = ['server', '--config', world.configPath, '--state-dir', world.stateDir];
    external = spawn(serverBinary, args, {
      env: minimalEnv(world),
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    external.stdout?.on('data', (chunk: Buffer) => externalLogs.push(chunk.toString()));
    external.stderr?.on('data', (chunk: Buffer) => externalLogs.push(chunk.toString()));
    transcript.command(`${serverBinary} ${args.join(' ')} &`, '(started as a test-owned process)');

    await waitFor(() => readDiscovery(world) !== null, 'external server discovery record', 30_000);
    const discovery = readDiscovery(world)!;
    expect(discovery.pid).toBe(external.pid);
    await waitFor(
      async () => (await healthStatus(discovery.base_url)) === 200,
      'external server health',
      30_000,
    );
    transcript.json('discovery record published by the external server (token redacted)', {
      ...discovery,
      auth_token: '[redacted]',
    });

    transcript.section('Launch the app: it attaches instead of launching its own');
    handle = await launchApp(world, testInfo, { traceName: 'external-attach' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const connection = await handle.page.evaluate(() => window.agentico.getConnectionStatus());
    expect(connection.status).toBe('ready');
    expect(connection.ownership).toBe('external');
    expect(connection.serverBuild?.version).toBeTruthy();
    transcript.json('connection state (via IPC): attached with external ownership', connection);
    await evidenceShot(handle, 'external-attach');

    // Same pid, same discovery record: the app did not spawn a second server.
    expect(readDiscovery(world)!.pid).toBe(external.pid);

    transcript.section('Quit the app: the external server survives');
    persistAppLogs(handle, 'external-attach-app');
    await closeApp(handle);
    handle = null;
    // The external server must still be alive and healthy after app exit.
    await new Promise((resolve) => setTimeout(resolve, 1_500));
    expect(processAlive(external.pid!)).toBe(true);
    expect(external.exitCode).toBeNull();
    expect(await healthStatus(discovery.base_url)).toBe(200);
    transcript.step(
      `external server pid ${external.pid} is still alive and healthy after the app quit — ` +
        'the app never signalled it',
    );

    transcript.section('Test-owned teardown of the external server');
    external.kill('SIGTERM');
    await waitFor(
      () => external!.exitCode !== null || external!.signalCode !== null,
      'external server to exit after test-sent SIGTERM',
      15_000,
    );
    transcript.step(
      `test sent SIGTERM; external server exited (code ${String(external.exitCode)}, ` +
        `signal ${String(external.signalCode)})`,
    );
    transcript.codeBlock('external server stderr tail', tailText(externalLogs.join(''), 15));
    assertNoLeakedProcesses(world);
    transcript.write(testInfo);
  } finally {
    if (handle !== null) {
      await closeApp(handle).catch(() => {});
    }
    if (external !== null && external.exitCode === null && external.signalCode === null) {
      external.kill('SIGKILL');
    }
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

async function healthStatus(baseUrl: string): Promise<number> {
  try {
    const response = await fetch(`${baseUrl.replace(/\/+$/, '')}/api/v1/health`, {
      signal: AbortSignal.timeout(2_000),
    });
    await response.arrayBuffer();
    return response.status;
  } catch {
    return 0;
  }
}

/** Reserves a free loopback port for a test-owned --listen server. */
async function freeLoopbackPort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const probe = net.createServer();
    probe.once('error', reject);
    probe.listen(0, '127.0.0.1', () => {
      const address = probe.address();
      probe.close(() => {
        if (address === null || typeof address === 'string') {
          reject(new Error('could not reserve a loopback port'));
          return;
        }
        resolve(address.port);
      });
    });
  });
}

test('external named server: the name surfaces in the app', async ({}, testInfo) => {
  const SERVER_NAME = 'frothy-macchiato';
  const transcript = new Transcript(
    'external-named-attach',
    'External attach with an operator-named server (packaged app)',
  );
  const world = createWorld('external-named-attach', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'alpha', { commit: true });

  let external: ChildProcess | null = null;
  let handle: AppHandle | null = null;
  const externalLogs: string[] = [];
  try {
    transcript.section('Start a named external server on an explicit port');
    const serverBinary = bundledServerBinary(packagedExecutable());
    const listenPort = await freeLoopbackPort();
    const args = [
      'server',
      '--config',
      world.configPath,
      '--state-dir',
      world.stateDir,
      '--listen',
      String(listenPort),
      '--name',
      SERVER_NAME,
    ];
    external = spawn(serverBinary, args, {
      env: minimalEnv(world),
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    external.stdout?.on('data', (chunk: Buffer) => externalLogs.push(chunk.toString()));
    external.stderr?.on('data', (chunk: Buffer) => externalLogs.push(chunk.toString()));
    transcript.command(`${serverBinary} ${args.join(' ')} &`, '(started as a test-owned process)');

    await waitFor(() => readDiscovery(world) !== null, 'named server discovery record', 30_000);
    const discovery = readDiscovery(world)!;
    expect(discovery.pid).toBe(external.pid);
    await waitFor(
      async () => (await healthStatus(discovery.base_url)) === 200,
      'named server health',
      30_000,
    );

    transcript.section('Launch the app: the name rides the connection state');
    handle = await launchApp(world, testInfo, { traceName: 'external-named-attach' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const connection = await handle.page.evaluate(() => window.agentico.getConnectionStatus());
    expect(connection.status).toBe('ready');
    expect(connection.ownership).toBe('external');
    expect(connection.serverName).toBe(SERVER_NAME);
    transcript.json('connection state (via IPC): named external runtime attached', connection);
    // The sidebar footer greets the user with the server name.
    await expect(handle.page.locator('.sidebar__footer').getByText(SERVER_NAME)).toBeVisible();
    await evidenceShot(handle, 'external-named-attach');

    // The app attached, it did not launch its own server.
    expect(readDiscovery(world)!.pid).toBe(external.pid);

    transcript.section('Test-owned teardown of the external server');
    persistAppLogs(handle, 'external-named-attach-app');
    await closeApp(handle);
    handle = null;
    external.kill('SIGTERM');
    await waitFor(
      () => external!.exitCode !== null || external!.signalCode !== null,
      'external server to exit after test-sent SIGTERM',
      15_000,
    );
    transcript.codeBlock('named server stderr tail', tailText(externalLogs.join(''), 15));
    assertNoLeakedProcesses(world);
    transcript.write(testInfo);
  } finally {
    if (handle !== null) {
      await closeApp(handle).catch(() => {});
    }
    if (external !== null && external.exitCode === null && external.signalCode === null) {
      external.kill('SIGKILL');
    }
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

// Sanity: the discovery file itself must be owner-only on disk.
test('external server publishes owner-only discovery material', async () => {
  // Covered implicitly above; kept as an explicit cheap assertion so a
  // regression in file modes fails with a precise message.
  const world = createWorld('external-perms', {
    auth: { loggedIn: true },
    presetWorkspaceRoot: true,
  });
  let external: ChildProcess | null = null;
  try {
    const serverBinary = bundledServerBinary(packagedExecutable());
    external = spawn(
      serverBinary,
      ['server', '--config', world.configPath, '--state-dir', world.stateDir],
      {
        env: minimalEnv(world),
        stdio: 'ignore',
      },
    );
    await waitFor(() => readDiscovery(world) !== null, 'discovery record', 30_000);
    const mode = fs.statSync(`${world.runtimeDir}/.agentico-server.json`).mode & 0o777;
    expect(mode).toBe(0o600);
    const tokenMode = fs.statSync(`${world.runtimeDir}/.agentico-server-token`).mode & 0o777;
    expect(tokenMode).toBe(0o600);
  } finally {
    if (external !== null) {
      external.kill('SIGTERM');
      await waitFor(
        () => external!.exitCode !== null || external!.signalCode !== null,
        'server exit',
        15_000,
      ).catch(() => external!.kill('SIGKILL'));
    }
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});
