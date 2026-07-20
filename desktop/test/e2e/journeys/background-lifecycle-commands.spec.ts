import { expect, test } from '@playwright/test';
import { spawn, type ChildProcess } from 'node:child_process';
import {
  assertNoLeakedProcesses,
  closeApp,
  createFeatureViaForm,
  launchApp,
  persistAppLogs,
  type AppHandle,
} from '../helpers/app';
import { bundledServerBinary, packagedExecutable } from '../helpers/packaged';
import {
  createRepo,
  createWorld,
  destroyWorld,
  minimalEnv,
  processAlive,
  readDiscovery,
  waitFor,
} from '../helpers/world';

test('packaged command palette, native menu routes, and active close policy stay coordinated', async ({}, testInfo) => {
  const world = createWorld('background-lifecycle-commands', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    workflowProvider: true,
  });
  createRepo(world, 'command-lab', { commit: true });
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, { traceName: 'background-lifecycle-commands' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const connection = await handle.page.evaluate(() => window.agentico.getConnectionStatus());
    expect(connection).toMatchObject({ status: 'ready', ownership: 'app-owned' });
    const initialDiscovery = readDiscovery(world);
    expect(initialDiscovery).not.toBeNull();
    expect(processAlive(initialDiscovery!.pid)).toBe(true);
    await assertNativeCommandsInstalled(handle);
    await assertTrayState(handle);

    await clickNativeMenu(handle, 'global.ama');
    await expect(handle.page.getByRole('complementary', { name: 'Ask Agentico' })).toHaveAttribute(
      'data-mode',
      'expanded',
    );
    await assertEditorShortcutSuppression(handle);

    await openPalette(handle);
    const palette = handle.page.getByRole('dialog', { name: 'Command palette' });
    await palette.getByLabel('Search commands').fill('settings');
    await handle.page.keyboard.press('Enter');
    await expect(handle.page.getByRole('tab', { name: 'Settings' })).toHaveAttribute(
      'aria-selected',
      'true',
    );

    await openPalette(handle);
    await palette.getByLabel('Search commands').fill('home');
    await handle.page.keyboard.press('Enter');
    await expect(handle.page.getByRole('tab', { name: 'Home' })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    await clickNativeMenu(handle, 'global.show');
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible();

    await createFeatureViaForm(handle, {
      name: 'Palette Non Target',
      description: 'This feature must remain idle when the palette targets the active tab.',
      repoPatterns: [/command-lab/],
      waitForReady: true,
    });
    const nonTargetFeatureId = await currentFeatureId(handle, 'Palette Non Target');
    await handle.page.getByRole('tab', { name: 'Home' }).click();

    const cockpit = await createFeatureViaForm(handle, {
      name: 'Background Command Lifecycle',
      description: 'Fixture-backed active work for close coordinator coverage.',
      repoPatterns: [/command-lab/],
      waitForReady: true,
    });
    const featureId = await currentFeatureId(handle, 'Background Command Lifecycle');
    await assertPaletteTargetsCurrentFeature(handle, featureId, nonTargetFeatureId);
    await assertZoomMenu(handle);
    await assertSecondInstanceFocusesExistingWindow(handle, initialDiscovery!.pid);

    await expect(cockpit.getByRole('button', { name: 'Stop' })).toBeEnabled({ timeout: 60_000 });

    const closeResult = await triggerQuitDecision(handle, [0], 'window-close');
    expect(closeResult.visible).toBe(false);
    expect(JSON.stringify(closeResult.captured[0])).toContain('Keep Running');
    expect(JSON.stringify(closeResult.captured[0])).toContain('Stop Work and Quit');
    expect(JSON.stringify(closeResult.captured[0])).toContain('Cancel');

    await handle.app.evaluate(({ BrowserWindow }) => {
      const window = BrowserWindow.getAllWindows()[0];
      window?.show();
      window?.focus();
    });
    const cancelResult = await triggerQuitDecision(handle, [2], 'native-quit');
    expect(cancelResult.visible).toBe(true);
    expect(JSON.stringify(cancelResult.captured[0])).toContain('Cancel');

    const stopResult = await triggerQuitDecision(handle, [1], 'window-close');
    expect(JSON.stringify(stopResult.captured[0])).toContain('Stop Work and Quit');
    await waitForAppExit(handle);
    handle.closed = true;
    await waitFor(
      async () => {
        const discovery = readDiscovery(world);
        return discovery === null || !processAlive(discovery.pid);
      },
      'app-owned server exits after Stop Work and Quit',
      60_000,
    );

    persistAppLogs(handle, 'background-lifecycle-commands-app-server');
  } finally {
    if (handle !== null) await closeApp(handle).catch(() => {});
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

test('packaged idle close quits without prompting for active-work decisions', async ({}, testInfo) => {
  const world = createWorld('background-lifecycle-idle-close', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    workflowProvider: true,
  });
  createRepo(world, 'idle-lab', { commit: true });
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, { traceName: 'background-lifecycle-idle-close' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const result = await handle.app.evaluate(async ({ BrowserWindow, dialog }) => {
      const window = BrowserWindow.getAllWindows()[0];
      if (window === undefined) throw new Error('main window missing');
      let promptCount = 0;
      const original = dialog.showMessageBox;
      dialog.showMessageBox = (async () => {
        promptCount += 1;
        return { response: 0, checkboxChecked: false };
      }) as typeof dialog.showMessageBox;
      window.close();
      dialog.showMessageBox = original;
      return { promptCount };
    });
    expect(result.promptCount).toBe(0);
    await waitForAppExit(handle);
    handle.closed = true;
    persistAppLogs(handle, 'background-lifecycle-idle-close-app-server');
  } finally {
    if (handle !== null) await closeApp(handle).catch(() => {});
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

test('packaged close policy exposes partial-stop Retry controls', async ({}, testInfo) => {
  const world = createWorld('background-lifecycle-partial-retry', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    workflowProvider: true,
  });
  createRepo(world, 'retry-lab', { commit: true });
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, { traceName: 'background-lifecycle-partial-retry' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const cockpit = await createFeatureViaForm(handle, {
      name: 'Partial Stop Retry',
      description: 'Fixture-backed active work for retrying a failed close stop.',
      repoPatterns: [/retry-lab/],
      waitForReady: true,
    });
    await cockpit.getByRole('button', { name: 'Start', exact: true }).click();
    await expect(cockpit.getByRole('button', { name: 'Stop' })).toBeEnabled({ timeout: 60_000 });
    await forceNextStopFailure(handle, 1);

    const result = await triggerQuitDecision(handle, [1, 1, 0], 'window-close');
    expect(JSON.stringify(result.captured[1])).toContain('Retry');
    expect(JSON.stringify(result.captured[1])).toContain('Quit Anyway');
    expect(JSON.stringify(result.captured[1])).toContain('Partial Stop Retry');
    await waitForAppExit(handle);
    handle.closed = true;
    persistAppLogs(handle, 'background-lifecycle-partial-retry-app-server');
  } finally {
    if (handle !== null) await closeApp(handle).catch(() => {});
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

test('packaged close policy exposes highest-risk Quit Anyway controls', async ({}, testInfo) => {
  const world = createWorld('background-lifecycle-quit-anyway', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
    workflowProvider: true,
  });
  createRepo(world, 'quit-anyway-lab', { commit: true });
  let handle: AppHandle | null = null;

  try {
    handle = await launchApp(world, testInfo, { traceName: 'background-lifecycle-quit-anyway' });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const cockpit = await createFeatureViaForm(handle, {
      name: 'Partial Stop Quit Anyway',
      description: 'Fixture-backed active work for the highest-risk quit path.',
      repoPatterns: [/quit-anyway-lab/],
      waitForReady: true,
    });
    await cockpit.getByRole('button', { name: 'Start', exact: true }).click();
    await expect(cockpit.getByRole('button', { name: 'Stop' })).toBeEnabled({ timeout: 60_000 });
    await forceNextStopFailure(handle, 1);

    const result = await triggerQuitDecision(handle, [1, 1, 0], 'native-quit');
    expect(JSON.stringify(result.captured[1])).toContain('Quit Anyway');
    expect(JSON.stringify(result.captured[2])).toContain('forces the app-owned runtime');
    await waitForAppExit(handle);
    handle.closed = true;
    persistAppLogs(handle, 'background-lifecycle-quit-anyway-app-server');
  } finally {
    if (handle !== null) await closeApp(handle).catch(() => {});
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

test('packaged external ownership remains detachable from native quit', async ({}, testInfo) => {
  const world = createWorld('background-lifecycle-external-ownership', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'external-lab', { commit: true });
  let external: ChildProcess | null = null;
  let handle: AppHandle | null = null;

  try {
    external = spawn(
      bundledServerBinary(packagedExecutable()),
      ['server', '--config', world.configPath, '--state-dir', world.stateDir],
      { env: minimalEnv(world), stdio: 'ignore' },
    );
    await waitFor(() => readDiscovery(world) !== null, 'external server discovery record', 30_000);
    const externalPid = external.pid;
    expect(externalPid).toBeDefined();

    handle = await launchApp(world, testInfo, {
      traceName: 'background-lifecycle-external-ownership',
    });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    const connection = await handle.page.evaluate(() => window.agentico.getConnectionStatus());
    expect(connection).toMatchObject({ status: 'ready', ownership: 'external' });
    expect(readDiscovery(world)?.pid).toBe(externalPid);
    await triggerQuitDecision(handle, [], 'native-quit');
    await waitForAppExit(handle);
    handle.closed = true;
    expect(processAlive(externalPid!)).toBe(true);
    persistAppLogs(handle, 'background-lifecycle-external-ownership-app-server');
  } finally {
    if (handle !== null) await closeApp(handle).catch(() => {});
    if (external !== null && external.exitCode === null && external.signalCode === null) {
      external.kill('SIGTERM');
      await waitFor(
        () => external!.exitCode !== null || external!.signalCode !== null,
        'external server exit',
        15_000,
      ).catch(() => external!.kill('SIGKILL'));
    }
    await assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});

async function openPalette(handle: AppHandle): Promise<void> {
  await handle.page.evaluate(() => {
    if (document.activeElement instanceof HTMLElement) {
      document.activeElement.blur();
    }
  });
  await handle.page.keyboard.press(process.platform === 'darwin' ? 'Meta+K' : 'Control+K');
  await expect(handle.page.getByRole('dialog', { name: 'Command palette' })).toBeVisible();
}

async function clickNativeMenu(handle: AppHandle, id: string): Promise<void> {
  await handle.app.evaluate(({ BrowserWindow, Menu }, commandId) => {
    const item = Menu.getApplicationMenu()?.getMenuItemById(commandId);
    if (item == null) throw new Error(`menu item ${commandId} missing`);
    item.click(undefined, BrowserWindow.getAllWindows()[0], undefined);
  }, id);
}

async function assertNativeCommandsInstalled(handle: AppHandle): Promise<void> {
  const commandState = await handle.app.evaluate(({ Menu }) => {
    const menu = Menu.getApplicationMenu();
    return {
      show: menu?.getMenuItemById('global.show') !== null,
      quit: menu?.getMenuItemById('global.quit') !== null,
      palette: menu?.getMenuItemById('global.palette') !== null,
      ama: menu?.getMenuItemById('global.ama') !== null,
      bulk: menu?.getMenuItemById('global.bulk') !== null,
    };
  });
  expect(commandState).toEqual({
    show: true,
    quit: true,
    palette: true,
    ama: true,
    bulk: true,
  });
}

async function assertTrayState(handle: AppHandle): Promise<void> {
  await waitFor(
    async () => (await nativeCommandState(handle)) !== null,
    'native command E2E state',
    10_000,
  );
  const state = await nativeCommandState(handle);
  expect(state).not.toBeNull();
  expect(state!.trayInstalled || state!.trayFallbackActive).toBe(true);
  expect(state!.attentionCount).toBeGreaterThanOrEqual(0);
  expect(typeof state!.amaActive).toBe('boolean');
}

async function nativeCommandState(handle: AppHandle): Promise<{
  attentionCount: number;
  amaActive: boolean;
  trayInstalled: boolean;
  trayFallbackActive: boolean;
  platform: NodeJS.Platform;
} | null> {
  return handle.app.evaluate(() => {
    const global = globalThis as typeof globalThis & {
      __agenticoNativeCommandState?: {
        attentionCount: number;
        amaActive: boolean;
        trayInstalled: boolean;
        trayFallbackActive: boolean;
        platform: NodeJS.Platform;
      };
    };
    return global.__agenticoNativeCommandState ?? null;
  });
}

async function currentFeatureId(handle: AppHandle, name: string): Promise<string> {
  const feature = await handle.page.evaluate(
    async (featureName) =>
      (await window.agentico.listFeatures()).find((entry) => entry.name === featureName),
    name,
  );
  expect(feature).toBeDefined();
  return feature!.id;
}

async function assertPaletteTargetsCurrentFeature(
  handle: AppHandle,
  featureId: string,
  nonTargetFeatureId: string,
): Promise<void> {
  await openPalette(handle);
  const palette = handle.page.getByRole('dialog', { name: 'Command palette' });
  await palette.getByLabel('Search commands').fill('start feature');
  await handle.page.keyboard.press('Enter');
  await waitFor(
    async () => {
      const feature = await handle.page.evaluate((id) => window.agentico.getFeature(id), featureId);
      return feature.actions.some((action) => action.id === 'pause-stop' && action.enabled);
    },
    'current feature palette Start action to target active tab',
    60_000,
  );
  const nonTarget = await handle.page.evaluate(
    (id) => window.agentico.getFeature(id),
    nonTargetFeatureId,
  );
  expect(nonTarget.actions.some((action) => action.id === 'pause-stop' && action.enabled)).toBe(
    false,
  );
  expect(nonTarget.actions.some((action) => action.id === 'start' && action.enabled)).toBe(true);
  await expect(handle.page.getByLabel('Feature Background Command Lifecycle')).toContainText(
    /running|implement|active|stop/i,
  );
}

async function assertEditorShortcutSuppression(handle: AppHandle): Promise<void> {
  const textbox = handle.page
    .getByRole('complementary', { name: 'Ask Agentico' })
    .getByRole('textbox', { name: 'Ask Agentico' });
  await textbox.fill('Shortcut focus stays in the composer');
  await textbox.click();
  await handle.page.keyboard.press(process.platform === 'darwin' ? 'Meta+K' : 'Control+K');
  await expect(handle.page.getByRole('dialog', { name: 'Command palette' })).not.toBeVisible();
  await expect(textbox).toBeFocused();
}

async function assertZoomMenu(handle: AppHandle): Promise<void> {
  const zoom = await handle.app.evaluate(async ({ BrowserWindow, Menu }) => {
    const window = BrowserWindow.getAllWindows()[0];
    if (window === undefined) throw new Error('main window missing');
    const before = window.webContents.getZoomFactor();
    const items = flattenMenu(Menu.getApplicationMenu());
    const zoomIn = items.find((item) => item.role === 'zoomIn' || item.label === 'Zoom In');
    const reset = items.find((item) => item.role === 'resetZoom' || item.label === 'Actual Size');
    zoomIn?.click(undefined, window, undefined);
    await new Promise((resolve) => setTimeout(resolve, 100));
    const afterZoomIn = window.webContents.getZoomFactor();
    reset?.click(undefined, window, undefined);
    await new Promise((resolve) => setTimeout(resolve, 100));
    const afterReset = window.webContents.getZoomFactor();
    return { before, afterZoomIn, afterReset };

    function flattenMenu(menu: Electron.Menu | null | undefined): Electron.MenuItem[] {
      if (menu === null || menu === undefined) return [];
      return menu.items.flatMap((item) => [item, ...flattenMenu(item.submenu)]);
    }
  });
  expect(zoom.afterZoomIn).toBeGreaterThan(zoom.before);
  expect(zoom.afterReset).toBeCloseTo(1, 2);
}

async function assertSecondInstanceFocusesExistingWindow(
  handle: AppHandle,
  expectedPid: number,
): Promise<void> {
  const result = await handle.app.evaluate(async ({ BrowserWindow, app }, pid) => {
    const global = globalThis as typeof globalThis & {
      __agenticoMainWindowFocusState?: { focused: boolean };
    };
    const window = BrowserWindow.getAllWindows()[0];
    if (window === undefined) throw new Error('main window missing');
    window.hide();
    app.emit('second-instance', {} as never, [], '');
    const deadline = Date.now() + 2_000;
    while (
      !window.isFocused() &&
      global.__agenticoMainWindowFocusState?.focused !== true &&
      Date.now() < deadline
    ) {
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    return {
      visible: window.isVisible(),
      focused: window.isFocused() || global.__agenticoMainWindowFocusState?.focused === true,
      pid,
    };
  }, expectedPid);
  expect(result.visible).toBe(true);
  expect(result.focused).toBe(true);
  expect(readDiscovery(handle.world)?.pid).toBe(expectedPid);
}

async function forceNextStopFailure(handle: AppHandle, count: number): Promise<void> {
  await handle.app.evaluate((_electron, failureCount) => {
    const global = globalThis as typeof globalThis & {
      __agenticoForceStopFailureCount?: number;
    };
    global.__agenticoForceStopFailureCount = failureCount;
  }, count);
}

async function triggerQuitDecision(
  handle: AppHandle,
  responses: Array<0 | 1 | 2>,
  trigger: 'window-close' | 'native-quit',
): Promise<{ visible: boolean; captured: unknown[] }> {
  return handle.app.evaluate(
    async ({ BrowserWindow, Menu, dialog }, options) => {
      const window = BrowserWindow.getAllWindows()[0];
      if (window === undefined) throw new Error('main window missing');
      const original = dialog.showMessageBox;
      const captured: unknown[] = [];
      let resolveDone!: () => void;
      const done = new Promise<void>((resolve) => {
        resolveDone = resolve;
      });
      dialog.showMessageBox = (async (...args: unknown[]) => {
        captured.push(args[args.length - 1]);
        if (captured.length >= options.responses.length) {
          queueMicrotask(resolveDone);
        }
        return {
          response:
            options.responses[Math.min(captured.length - 1, options.responses.length - 1)] ?? 0,
          checkboxChecked: false,
        };
      }) as typeof dialog.showMessageBox;
      if (options.responses.length === 0) {
        queueMicrotask(resolveDone);
      }
      if (options.trigger === 'native-quit') {
        const item = Menu.getApplicationMenu()?.getMenuItemById('global.quit');
        if (item === null || item === undefined) throw new Error('global.quit missing');
        item.click(undefined, window, undefined);
      } else {
        window.close();
      }
      await Promise.race([
        done,
        new Promise((resolve) => setTimeout(resolve, options.responses.length === 0 ? 100 : 5000)),
      ]);
      if (options.responses[0] === 0) {
        const deadline = Date.now() + 2_000;
        while (!window.isDestroyed() && window.isVisible() && Date.now() < deadline) {
          await new Promise((resolve) => setTimeout(resolve, 50));
        }
      }
      const visible = !window.isDestroyed() && window.isVisible();
      dialog.showMessageBox = original;
      return { visible, captured };
    },
    { responses, trigger },
  );
}

async function waitForAppExit(handle: AppHandle): Promise<void> {
  const appProcess = handle.app.process();
  await waitFor(
    () => appProcess.exitCode !== null || appProcess.signalCode !== null,
    'packaged app process to exit',
    20_000,
  );
}
