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
 * Menu-bar journey against the PACKAGED app: the real application menu is the
 * surface under test, driven through main-process `MenuItem.click()` exactly
 * the way the Settings item and the Close Window role already are.
 *
 *  - the menu order reads as a Mac app's: File, Navigate, Edit, View, Feature,
 *    Window, with Feature in the app-specific slot between View and Window;
 *  - File ▸ New Feature opens the creation sheet from any pane;
 *  - the View toggles drive the real sidebar and inspector state, and their
 *    Show/Hide labels flip with it;
 *  - the Feature menu carries all fifteen verbs always — disabled and visible,
 *    never hidden — matching the selected feature's action catalogue, and the
 *    whole group is disabled on Overview;
 *  - Feature ▸ Stop surfaces the cockpit's own confirmation before anything is
 *    dispatched, and the confirmed stop lands;
 *  - the Navigate items and the tray still route exactly as before.
 */
import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  createFeatureViaForm,
  evidenceShot,
  launchApp,
  persistAppLogs,
  type AppHandle,
} from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import {
  createRepo,
  createWorld,
  destroyWorld,
  providerInvocationCount,
  waitFor,
} from '../helpers/world';
import { FEATURE_COMMAND_IDS, commandById } from '../../../src/shared/commands';

// Every locator action in this journey is bounded. The suite leaves
// `actionTimeout` unset, so an action that never becomes possible (a click on a
// control a stalled push left disabled, say) otherwise waits out the whole test
// timeout and reports nothing but "test timeout exceeded". Bounded, it fails on
// the action with the locator and the element's state.
test.use({ actionTimeout: 30_000 });

const EXPECTED_FEATURE_LABELS = FEATURE_COMMAND_IDS.map((id) => commandById(id).label);

/**
 * Bounds one round trip into the app. `page.evaluate`/`app.evaluate` have no
 * timeout of their own: a wedged main process or a busy renderer leaves the
 * promise pending forever, and inside a `waitFor` condition that swallows the
 * poll's own deadline too — one call can absorb the entire test budget. Racing
 * a deadline turns that into a fast, named failure.
 */
async function probe<T>(what: string, attempt: () => Promise<T>, budgetMs = 20_000): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      attempt(),
      new Promise<never>((_resolve, reject) => {
        timer = setTimeout(
          () => reject(new Error(`${what} did not answer within ${budgetMs}ms`)),
          budgetMs,
        );
      }),
    ]);
  } finally {
    if (timer !== undefined) clearTimeout(timer);
  }
}

interface MenuState {
  topLevel: string[];
  file: Array<{ id: string | undefined; label: string; enabled: boolean }>;
  view: Array<{ id: string | undefined; label: string; enabled: boolean }>;
  feature: Array<{ id: string | undefined; label: string; enabled: boolean; visible: boolean }>;
}

/** Reads the live application menu out of the running main process. */
async function menuState(handle: AppHandle): Promise<MenuState> {
  return probe('a read of the live application menu', () => readMenuState(handle));
}

function readMenuState(handle: AppHandle): Promise<MenuState> {
  return handle.app.evaluate(({ Menu }) => {
    const menu = Menu.getApplicationMenu();
    if (menu === null) throw new Error('no application menu is installed');
    const submenu = (label: string) => {
      const parent = menu.items.find((item) => item.label === label);
      if (parent === undefined) throw new Error(`no ${label} menu`);
      return (parent.submenu?.items ?? []).filter((item) => item.type !== 'separator');
    };
    return {
      topLevel: menu.items.map((item) => item.label),
      file: submenu('File').map((item) => ({
        id: item.id,
        label: item.label,
        enabled: item.enabled,
      })),
      view: submenu('View').map((item) => ({
        id: item.id,
        label: item.label,
        enabled: item.enabled,
      })),
      feature: submenu('Feature').map((item) => ({
        id: item.id,
        label: item.label,
        enabled: item.enabled,
        visible: item.visible,
      })),
    };
  });
}

/** Clicks a real application-menu item by id, against the main window. */
async function clickMenuItem(handle: AppHandle, id: string): Promise<void> {
  await probe(`the click on menu item ${id}`, () =>
    handle.app.evaluate(({ BrowserWindow, Menu }, commandId) => {
      const item = Menu.getApplicationMenu()?.getMenuItemById(commandId);
      if (item == null) throw new Error(`menu item ${commandId} missing`);
      if (!item.enabled) throw new Error(`menu item ${commandId} is disabled`);
      const main = BrowserWindow.getAllWindows()[0];
      item.click(undefined, main, undefined);
    }, id),
  );
}

async function featureItem(handle: AppHandle, id: string): Promise<boolean> {
  const state = await menuState(handle);
  const item = state.feature.find((entry) => entry.id === id);
  if (item === undefined) throw new Error(`${id} is missing from the Feature menu`);
  return item.enabled;
}

test(
  'the application menu bar drives New Feature, the View toggles, and the Feature catalogue',
  { tag: '@smoke' },
  async ({}, testInfo) => {
    // A real run is started and stopped through the menu, so this needs more than
    // the suite's default per-test budget. The number is a backstop, not the
    // enforcement mechanism: every action, wait, and round trip below carries its
    // own bound, generous against its real duration, so a stalled step fails by
    // name in seconds instead of quietly soaking this budget.
    test.setTimeout(420_000);
    const transcript = new Transcript('menu-bar', 'Application menu bar — File, View, and Feature');
    const world = createWorld('menu-bar', {
      auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
      presetWorkspaceRoot: true,
      // Deterministic provider output, so the run this journey starts through the
      // menu is genuinely live — and therefore genuinely stoppable — rather than
      // racing its own completion.
      workflowProvider: true,
    });
    createRepo(world, 'menu-lab', { commit: true });
    let handle: AppHandle | null = null;
    let failed = true;

    try {
      handle = await launchApp(world, testInfo, { traceName: 'menu-bar' });
      await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
        timeout: 60_000,
      });
      transcript.step('app launched and reached the ready workspace');

      transcript.section('The menu bar reads as a Mac app: Feature sits between View and Window');
      const initial = await menuState(handle);
      expect(initial.topLevel.slice(1)).toEqual([
        'File',
        'Navigate',
        'Edit',
        'View',
        'Feature',
        'Window',
      ]);
      expect(initial.file.map((item) => item.label)).toEqual(['New Feature', 'Close Window']);
      expect(initial.view.map((item) => item.label)).toEqual([
        'Hide Sidebar',
        'Show Inspector',
        'Reload',
        'Force Reload',
        'Zoom In',
        'Zoom Out',
        'Actual Size',
        'Toggle Full Screen',
      ]);
      transcript.step('File, View, Feature, and Window are in the standard order with their items');

      transcript.section('On Overview the whole Feature menu is disabled — visible, never hidden');
      expect(initial.feature.map((item) => item.label)).toEqual(EXPECTED_FEATURE_LABELS);
      expect(initial.feature.every((item) => item.visible)).toBe(true);
      expect(initial.feature.every((item) => !item.enabled)).toBe(true);
      // Overview has no inspector to show or hide; New Feature works regardless.
      expect(initial.view.find((item) => item.id === 'global.toggle-inspector')?.enabled).toBe(
        false,
      );
      expect(initial.file.find((item) => item.id === 'global.new-feature')?.enabled).toBe(true);
      transcript.step(
        'all fifteen verbs present and dimmed on Overview, with Show Inspector dimmed too',
      );

      transcript.section('File ▸ New Feature opens the creation sheet');
      await clickMenuItem(handle, 'global.new-feature');
      await expect(handle.page.getByRole('form', { name: 'Create a feature' })).toBeVisible({
        timeout: 30_000,
      });
      await handle.page.getByRole('button', { name: 'Cancel' }).click();
      await expect(handle.page.getByRole('form', { name: 'Create a feature' })).toHaveCount(0);
      transcript.step('the File item opened the same creation sheet the toolbar button does');

      transcript.section('A selected feature lights the menu up from its action catalogue');
      const cockpit = await createFeatureViaForm(handle, {
        name: 'Menu Bar Feature',
        description: 'Exercises the native Feature menu against a real action catalogue.',
        repoPatterns: [/menu-lab/],
        waitForReady: true,
      });
      await waitFor(
        async () => await featureItem(handle!, 'feature.start'),
        'Feature ▸ Start to enable for the selected feature',
        60_000,
      );
      const selected = await menuState(handle);
      expect(selected.feature.map((item) => item.label)).toEqual(EXPECTED_FEATURE_LABELS);
      expect(selected.feature.every((item) => item.visible)).toBe(true);
      const enabledById = new Map(selected.feature.map((item) => [item.id, item.enabled]));
      // Configuration is a local editor: available whenever a feature is selected.
      expect(enabledById.get('feature.configuration')).toBe(true);
      // A pre-start feature has nothing to stop or merge — dimmed, still listed.
      expect(enabledById.get('feature.pause-stop')).toBe(false);
      expect(enabledById.get('feature.merge')).toBe(false);
      // Enablement matches the authoritative catalogue verb for verb.
      const catalogue = await probe('the authoritative action catalogue', () =>
        handle!.page.evaluate(async () => {
          const settings = await window.agentico.getSettings();
          const state = await window.agentico.getConnectionStatus();
          const id =
            state.serverKey == null
              ? null
              : (settings.shell.featureByServer[state.serverKey] ?? null);
          if (id === null) throw new Error('no feature is selected');
          return (await window.agentico.getFeature(id)).actions;
        }),
      );
      for (const action of catalogue) {
        const id = `feature.${action.id}`;
        if (!enabledById.has(id)) continue;
        expect(enabledById.get(id), `${id} drifted from the action catalogue`).toBe(action.enabled);
      }
      await evidenceShot(handle, 'menu-bar-feature-selected');
      transcript.step(
        'the Feature menu mirrored the selected feature’s action catalogue verb by verb',
      );

      transcript.section('The View toggles drive the real sidebar and inspector, labels and all');
      const sidebar = handle.page.locator('nav.sidebar');
      await expect(sidebar).toHaveAttribute('data-collapsed', 'false');
      await clickMenuItem(handle, 'global.toggle-sidebar');
      await expect(sidebar).toHaveAttribute('data-collapsed', 'true');
      await waitFor(
        async () =>
          (await menuState(handle!)).view.find((item) => item.id === 'global.toggle-sidebar')
            ?.label === 'Show Sidebar',
        'the sidebar item label to flip to Show Sidebar',
        15_000,
      );
      const persisted = await probe('the persisted shell settings', () =>
        handle!.page.evaluate(() => window.agentico.getSettings()),
      );
      expect(persisted.shell.sidebarCollapsed).toBe(true);
      await clickMenuItem(handle, 'global.toggle-sidebar');
      await expect(sidebar).toHaveAttribute('data-collapsed', 'false');
      transcript.step('View ▸ Show/Hide Sidebar collapsed and restored the same persisted state');

      const inspectorToggle = handle.page.getByRole('button', { name: 'Toggle inspector' });
      await expect(inspectorToggle).toHaveAttribute('aria-pressed', 'false');
      await clickMenuItem(handle, 'global.toggle-inspector');
      await expect(inspectorToggle).toHaveAttribute('aria-pressed', 'true');
      await waitFor(
        async () =>
          (await menuState(handle!)).view.find((item) => item.id === 'global.toggle-inspector')
            ?.label === 'Hide Inspector',
        'the inspector item label to flip to Hide Inspector',
        15_000,
      );
      await clickMenuItem(handle, 'global.toggle-inspector');
      await expect(inspectorToggle).toHaveAttribute('aria-pressed', 'false');
      transcript.step('View ▸ Show/Hide Inspector flipped the cockpit inspector and its own label');

      transcript.section('Feature ▸ Start then Stop runs the cockpit’s own confirmation');
      await clickMenuItem(handle, 'feature.start');
      await expect(cockpit.getByText(/Start accepted|Starting from/)).toBeVisible({
        timeout: 30_000,
      });
      // Three separate facts, waited for separately so a failure says which one
      // broke: the provider process really started, the cockpit's own Stop control
      // agrees the run is live, and only then the Feature menu's enabled map
      // caught up through the renderer→main push.
      await waitFor(
        () => providerInvocationCount(world.providerInvocationLog) >= 1,
        'the real provider process the menu-invoked Start produced',
        60_000,
      );
      // The cockpit's own Stop control lives in the feature header, outside the
      // cockpit region — the same locator start-watch-stop uses.
      await expect(handle.page.getByRole('button', { name: 'Stop', exact: true })).toBeEnabled({
        timeout: 60_000,
      });
      await waitFor(
        async () => await featureItem(handle!, 'feature.pause-stop'),
        'Feature ▸ Stop to follow the live run through the UI-state push',
        30_000,
      );
      await clickMenuItem(handle, 'feature.pause-stop');
      const stopDialog = handle.page.getByRole('dialog', { name: /^Stop Menu Bar Feature\?/ });
      await expect(stopDialog).toBeVisible({ timeout: 30_000 });
      await evidenceShot(handle, 'menu-bar-stop-confirmation');
      transcript.step(
        'the menu-invoked Stop surfaced the cockpit’s confirmation, not a raw dispatch',
      );

      await stopDialog.getByRole('button', { name: 'Confirm stop' }).click();
      await expect(stopDialog).toHaveCount(0, { timeout: 60_000 });
      await waitFor(
        async () => {
          const snapshot = await probe('the authoritative feature snapshot', () =>
            handle!.page.evaluate(async () => {
              const settings = await window.agentico.getSettings();
              const state = await window.agentico.getConnectionStatus();
              const id =
                state.serverKey == null
                  ? null
                  : (settings.shell.featureByServer[state.serverKey] ?? null);
              return id === null ? null : await window.agentico.getFeature(id);
            }),
          );
          return (
            snapshot !== null &&
            !['running', 'starting', 'stopping'].includes(snapshot.status.toLowerCase())
          );
        },
        'the authoritative terminal snapshot after the menu-invoked stop',
        90_000,
      );
      transcript.step('the confirmed stop dispatched and the feature reached a terminal state');

      transcript.section('Navigate and the tray still route exactly as before');
      await clickMenuItem(handle, 'global.home');
      await expect(handle.page.getByRole('option', { name: 'Overview' })).toHaveAttribute(
        'aria-selected',
        'true',
        { timeout: 30_000 },
      );
      await expect(cockpit).toHaveCount(0);
      // Back on Overview the Feature menu goes dark again, still listing fifteen.
      await waitFor(
        async () => (await menuState(handle!)).feature.every((item) => !item.enabled),
        'the Feature menu to go dark on Overview',
        15_000,
      );
      const trayState = await probe('the native-command controller state', () =>
        handle!.app.evaluate(() => {
          const global = globalThis as typeof globalThis & {
            __agenticoNativeCommandState?: { trayInstalled: boolean; trayFallbackActive: boolean };
          };
          return global.__agenticoNativeCommandState ?? null;
        }),
      );
      expect(trayState).not.toBeNull();
      expect(trayState!.trayInstalled || trayState!.trayFallbackActive).toBe(true);
      transcript.step('Navigate ▸ Overview routed as before and the tray is still installed');

      transcript.step('journey complete');
      failed = false;
    } finally {
      // Diagnostics before teardown, on both outcomes: a failed run is exactly
      // when the transcript and the app's own log are worth having, and the last
      // failure left nothing behind to post-mortem.
      if (handle !== null) {
        persistAppLogs(handle, 'menu-bar');
        if (failed) {
          await probe('the failure screenshot', () =>
            evidenceShot(handle!, 'menu-bar-failure'),
          ).catch(() => undefined);
          const state = await probe(
            'the failure menu read',
            () => readMenuState(handle!),
            10_000,
          ).catch(() => null);
          if (state !== null) transcript.json('live menu state at failure', state);
        }
      }
      transcript.write(testInfo);
      if (handle !== null) await closeApp(handle);
      await assertNoLeakedProcesses(world);
      destroyWorld(world);
    }
  },
);
