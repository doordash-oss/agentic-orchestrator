/**
 * Narrow-viewport "Switch Server…" routing (packaged app): below the 700px
 * sidebar auto-collapse the sidebar footer — the switcher's wide-layout home —
 * is display:none. The menu item and the palette command must still land on a
 * visible, usable Servers listbox, served by the narrow dock under the
 * toolbar. Both routes are driven end to end, and each hands over a real
 * server choice:
 *   menu route    → click the other server's row, the workspace switches
 *   palette route → click the other server's row, the workspace switches back
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
  setWindowSize,
  type AppHandle,
} from '../helpers/app';
import { bundledServerBinary, packagedExecutable } from '../helpers/packaged';
import { Transcript } from '../helpers/transcript';
import { tailText } from '../helpers/runtime';
import {
  createWorld,
  destroyWorld,
  minimalEnv,
  waitFor,
  type JourneyWorld,
} from '../helpers/world';

// Same bounded-action rationale as menu-bar.spec.
test.use({ actionTimeout: 30_000 });

interface TestServer {
  name: string;
  runtimeDir: string;
  proc: ChildProcess;
  logs: string[];
}

function registryDir(world: JourneyWorld): string {
  return path.join(world.home, '.agentic-orchestrator', 'servers');
}

function registryCount(world: JourneyWorld): number {
  try {
    return fs.readdirSync(registryDir(world)).filter((name) => name.endsWith('.json')).length;
  } catch {
    return 0;
  }
}

/** Starts a test-owned server with its own runtime dir, name, and port. */
function startTestServer(world: JourneyWorld, name: string, runtimeDir: string): TestServer {
  const runtimePath = path.join(world.root, runtimeDir);
  const stateDir = path.join(runtimePath, 'features');
  const configPath = path.join(runtimePath, 'config.yaml');
  fs.mkdirSync(stateDir, { recursive: true });
  fs.copyFileSync(world.configPath, configPath);
  const logs: string[] = [];
  const proc = spawn(
    bundledServerBinary(packagedExecutable()),
    ['server', '--config', configPath, '--state-dir', stateDir, '--name', name],
    { env: minimalEnv(world), stdio: ['ignore', 'pipe', 'pipe'] },
  );
  proc.stdout?.on('data', (chunk: Buffer) => logs.push(chunk.toString()));
  proc.stderr?.on('data', (chunk: Buffer) => logs.push(chunk.toString()));
  return { name, runtimeDir: runtimePath, proc, logs };
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
  if (server.proc.exitCode !== null || server.proc.signalCode !== null) return;
  server.proc.kill('SIGKILL');
  await waitFor(
    () => server.proc.exitCode !== null || server.proc.signalCode !== null,
    `${server.name} to exit`,
    15_000,
  ).catch(() => {});
}

/** Clicks a real application-menu item by id, against the main window. */
async function clickMenuItem(handle: AppHandle, id: string): Promise<void> {
  await handle.app.evaluate(({ BrowserWindow, Menu }, commandId) => {
    const item = Menu.getApplicationMenu()?.getMenuItemById(commandId);
    if (item == null) throw new Error(`menu item ${commandId} missing`);
    if (!item.enabled) throw new Error(`menu item ${commandId} is disabled`);
    const main = BrowserWindow.getAllWindows()[0];
    item.click(undefined, main, undefined);
  }, id);
}

/** The switcher popover's listbox. */
function switcherListbox(handle: AppHandle) {
  return handle.page.getByRole('listbox', { name: 'Servers' });
}

async function connectionState(handle: AppHandle) {
  return handle.page.evaluate(() => window.agentico.getConnectionStatus());
}

test('narrow window: the menu and palette "Switch Server…" routes open a usable, visible switcher', async ({}, testInfo) => {
  const transcript = new Transcript(
    'switch-server-narrow',
    'Narrow-viewport Switch Server routes (packaged)',
  );
  const world = createWorld('switch-server-narrow', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const servers: TestServer[] = [];
  let handle: AppHandle | null = null;
  try {
    transcript.section('Start two named servers sharing one HOME and attach to alpha');
    const alpha = startTestServer(world, 'alpha', 'runtime-alpha');
    const beta = startTestServer(world, 'beta', 'runtime-beta');
    servers.push(alpha, beta);
    await waitFor(() => discoveryAt(alpha.runtimeDir) !== null, 'alpha discovery record', 30_000);
    await waitFor(() => discoveryAt(beta.runtimeDir) !== null, 'beta discovery record', 30_000);
    await waitFor(() => registryCount(world) === 2, 'two registry entries', 30_000);

    handle = await launchApp(world, testInfo, { traceName: 'switch-server-narrow-picker' });
    const picker = handle.page.getByRole('listbox', { name: /running agentico servers/i });
    await expect(picker).toBeVisible({ timeout: 60_000 });
    await picker.getByRole('option').filter({ hasText: 'alpha' }).click();
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    expect((await connectionState(handle)).serverName).toBe('alpha');

    transcript.section('Shrink to 600×720: the sidebar auto-collapses away');
    await setWindowSize(handle, 600, 720);
    await expect(handle.page.locator('.sidebar')).toBeHidden();
    // The narrow dock puts the same switcher control where it stays visible.
    const dockControl = handle.page.getByRole('button', { name: 'alpha — switch server' });
    await expect(dockControl).toBeVisible();
    transcript.step('sidebar hidden; the docked switcher control is the visible home');

    transcript.section('Menu route: Navigate ▸ Switch Server… opens the listbox in view');
    await clickMenuItem(handle, 'global.switch-server');
    await expect(switcherListbox(handle)).toBeVisible({ timeout: 30_000 });
    await expect(dockControl).toBeFocused();
    await evidenceShot(handle, 'switch-server-narrow-menu-route');
    const betaRow = handle.page.getByRole('option', { name: /beta at .+ — Available/ });
    await expect(betaRow).toBeVisible({ timeout: 30_000 });
    await betaRow.click();
    await waitFor(
      async () => (await connectionState(handle!)).serverName === 'beta',
      'beta attach',
      60_000,
    );
    transcript.step('the menu-routed popover handed over a real server choice');

    transcript.section('Palette route: ⌘K → Switch Server… opens the listbox in view');
    await handle.page.keyboard.press(process.platform === 'darwin' ? 'Meta+K' : 'Control+K');
    const palette = handle.page.getByRole('dialog', { name: 'Command palette' });
    await expect(palette).toBeVisible();
    await palette.getByLabel('Search features and commands').fill('Switch Server');
    await palette.getByRole('option', { name: /Switch Server/ }).click();
    await expect(switcherListbox(handle)).toBeVisible({ timeout: 30_000 });
    await expect(handle.page.getByRole('button', { name: 'beta — switch server' })).toBeFocused();
    await evidenceShot(handle, 'switch-server-narrow-palette-route');
    const alphaRow = handle.page.getByRole('option', { name: /alpha at .+ — Available/ });
    await expect(alphaRow).toBeVisible({ timeout: 30_000 });
    await alphaRow.click();
    await waitFor(
      async () => (await connectionState(handle!)).serverName === 'alpha',
      'alpha re-attach',
      60_000,
    );
    transcript.step('the palette-routed popover switched back to alpha');

    persistAppLogs(handle, 'switch-server-narrow-app');
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
