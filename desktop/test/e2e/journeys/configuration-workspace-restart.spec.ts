/**
 * Journey 7 — configuration, workspace, degraded-remediation, and
 * deferred-restart against the packaged app and real bundled server:
 *
 * create feature → edit feature config in the cockpit through the structured
 * form (model per phase, inquireness, gates) → save through the server →
 * verify persistence → Settings tab → workspace defaults form with the
 * Utilities model → workspace-root management → provider remediation rows →
 * degraded state → advanced path change → restart-pending → idle prompt →
 * Later → Restart Now.
 */
import { execFileSync } from 'node:child_process';
import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  createFeatureViaForm,
  evidenceShot,
  launchApp,
  mockDirectoryPicker,
  persistAppLogs,
  setTheme,
  setWindowSize,
  type AppHandle,
} from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import {
  createPlainFolder,
  createRepo,
  createWorld,
  destroyWorld,
  readDiscovery,
  waitFor,
} from '../helpers/world';

const RUN_NAME = `config-workspace-restart-${
  process.env['AGENTICO_E2E_VARIANT'] ?? (process.platform === 'darwin' ? 'macos' : 'linux')
}`;

test('configuration, workspace, and restart journey against the packaged app', async ({}, testInfo) => {
  const transcript = new Transcript(
    RUN_NAME,
    'Journey 7 — configuration/workspace/restart (packaged app, real bundled server)',
  );
  const world = createWorld('config-restart', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  createRepo(world, 'alpha', { commit: true });

  let handle: AppHandle | null = null;
  try {
    transcript.section('Launch and reach the ready workspace');
    handle = await launchApp(world, testInfo, { traceName: RUN_NAME });
    await expect(handle.page.getByRole('button', { name: 'New feature' })).toBeVisible({
      timeout: 60_000,
    });
    transcript.step('app launched and reached the ready workspace');

    transcript.section('Create a feature');
    const cockpit = await createFeatureViaForm(handle, {
      name: 'Config Journey',
      description: 'Exercise configuration editing in the cockpit.',
      repoPatterns: [/alpha/],
      waitForReady: true,
    });
    transcript.step('feature created and setup complete');

    transcript.section('Structured feature configuration in the cockpit');
    const configToggle = cockpit.getByRole('button', { name: /Configuration/ }).first();
    await expect(configToggle).toBeVisible();
    await configToggle.click();

    const editor = handle.page.locator('[aria-label="Feature configuration editor"]');
    await expect(editor).toBeVisible({ timeout: 15_000 });
    await expect(editor.getByRole('group', { name: 'Models' })).toBeVisible();
    await expect(editor.getByRole('group', { name: 'Behavior' })).toBeVisible();
    await expect(editor.getByRole('group', { name: 'Gates' })).toBeVisible();
    transcript.step('structured config form visible with Models / Behavior / Gates groups');

    // The feature form has no Utilities row — that model is workspace-scoped.
    await expect(editor.getByLabel('Utilities model')).toHaveCount(0);

    const implementationPicker = editor.getByLabel('Implementation model');
    await expect(implementationPicker).toBeVisible();
    const defaultOptionLabel = await implementationPicker
      .locator('option')
      .first()
      .textContent();
    expect(defaultOptionLabel).toContain('Default —');
    transcript.step(`implementation picker names the effective default: "${defaultOptionLabel}"`);

    // Pick the first concrete model, flip inquireness and a gate, then save.
    const optionValues = await implementationPicker
      .locator('option')
      .evaluateAll((options) =>
        options
          .map((option) => (option as HTMLOptionElement).value)
          .filter((value) => value !== ''),
      );
    expect(optionValues.length).toBeGreaterThan(0);
    const chosenModel = optionValues[0] as string;
    await implementationPicker.selectOption(chosenModel);
    await editor.locator('.config-editor__segment', { hasText: 'High' }).click();
    const researchGate = editor.getByRole('checkbox', { name: /Research review/ });
    const researchGateWasChecked = await researchGate.isChecked();
    await researchGate.click();

    const saveButton = editor.getByRole('button', { name: 'Save changes' });
    await expect(editor.getByRole('status')).toContainText('Unsaved changes');
    await expect(saveButton).toBeEnabled();

    await setWindowSize(handle, 1440, 900);
    await editor.scrollIntoViewIfNeeded();
    await evidenceShot(handle, 'feature-configuration-structured-form-dirty-l-1440x900');
    await setTheme(handle, 'dark');
    await evidenceShot(handle, 'feature-configuration-structured-form-dirty-d-1440x900');
    await setTheme(handle, 'light');

    await saveButton.click();
    await expect(editor.getByRole('status')).toContainText('Saved', { timeout: 15_000 });
    transcript.step('feature config saved through the server');

    const persisted = await handle.page.evaluate(async () => {
      const list = await window.agentico.listFeatures();
      const feature = list.find((f) => f.name === 'Config Journey');
      if (!feature) throw new Error('feature not found');
      const snapshot = await window.agentico.getFeatureConfig(feature.id);
      return snapshot.current;
    });
    expect(persisted.models.implementation).toBe(chosenModel);
    expect(persisted.inquireness).toBe('high');
    expect(persisted.checkpoints.researchReview).toBe(!researchGateWasChecked);
    transcript.step(
      `server persisted implementation=${persisted.models.implementation}, inquireness=high, researchReview=${String(!researchGateWasChecked)}`,
    );

    transcript.section('Workspace defaults in Settings');
    await handle.page.getByRole('tab', { name: 'Settings' }).click();
    await expect(handle.page.getByRole('heading', { name: 'Settings' })).toBeVisible({
      timeout: 15_000,
    });

    const defaultsEditor = handle.page.locator('[aria-label="Workspace defaults editor"]');
    await defaultsEditor.scrollIntoViewIfNeeded();
    await expect(defaultsEditor).toBeVisible({ timeout: 15_000 });
    await expect(defaultsEditor.getByLabel('Utilities model')).toBeVisible();
    transcript.step('workspace defaults form visible including the Utilities model row');

    await defaultsEditor.locator('.config-editor__segment', { hasText: 'None' }).click();
    const defaultsSave = defaultsEditor.getByRole('button', { name: 'Save changes' });
    await expect(defaultsSave).toBeEnabled();
    await defaultsSave.click();
    await expect(defaultsEditor.getByRole('status')).toContainText('Saved', { timeout: 15_000 });

    const workspaceDefaults = await handle.page.evaluate(() =>
      window.agentico.getWorkspaceDefaults(),
    );
    expect(workspaceDefaults.inquireness).toBe('none');
    transcript.step('workspace default inquireness saved and re-read as none');

    await evidenceShot(handle, 'workspace-defaults-structured-form-saved-l-1440x900');
    await setTheme(handle, 'dark');
    await evidenceShot(handle, 'workspace-defaults-structured-form-saved-d-1440x900');
    await setTheme(handle, 'light');

    transcript.section('Workspace-root management');
    await expect(handle.page.getByRole('heading', { name: 'Workspace roots' })).toBeVisible();
    const rootList = handle.page.locator('.settings-panel__roots');
    await expect(rootList).toContainText(world.workspaceRoot);
    transcript.step('existing workspace root visible in the settings panel');

    createPlainFolder(world, 'extra-dir');
    await mockDirectoryPicker(handle, `${world.workspaceRoot}/extra-dir`);
    await handle.page.getByRole('button', { name: 'Add workspace root' }).click();
    await expect(rootList).toContainText('extra-dir', { timeout: 15_000 });
    transcript.step('workspace root added through the native picker');

    transcript.section('Provider remediation rows');
    await expect(handle.page.getByRole('heading', { name: 'Providers' })).toBeVisible();
    const providerList = handle.page.locator('.settings-panel__providers');
    await expect(providerList).toContainText('claude');
    const claudeRow = handle.page.locator('.settings-panel__provider', { hasText: 'claude' });
    await expect(claudeRow.locator('.settings-panel__provider-status')).toContainText('Ready');
    transcript.step('provider rows visible with readiness status');

    transcript.section('Degraded remediation surface');
    const degradedProviders = handle.page.locator(
      '.settings-panel__provider .settings-panel__provider-status.is-degraded',
    );
    const degradedCount = await degradedProviders.count();
    transcript.step(`${degradedCount} provider(s) show degraded (Not ready) status`);
    expect(degradedCount).toBeGreaterThan(0);
    const firstDegraded = degradedProviders.first();
    await expect(firstDegraded).toContainText('Not ready');
    const degradedCause = await handle.page
      .locator('.settings-panel__provider.is-degraded .settings-panel__provider-cause')
      .first()
      .textContent();
    expect(degradedCause).toBeTruthy();
    transcript.step('degraded provider shows Not ready with cause and remedy');

    transcript.section('Appearance settings');
    const themeGroup = handle.page.locator('.settings-panel__theme');
    await expect(themeGroup).toBeVisible();
    await expect(themeGroup.getByRole('radio', { name: 'Dark' })).toBeVisible();
    await expect(themeGroup.getByRole('radio', { name: 'Light' })).toBeVisible();
    await expect(themeGroup.getByRole('radio', { name: 'System' })).toBeVisible();
    transcript.step('appearance theme radiogroup visible with System/Light/Dark options');

    transcript.section('Restart-pending flow: advanced runtime path change');
    await setWindowSize(handle, 1440, 900);
    await expect(handle.page.getByRole('heading', { name: 'Advanced' })).toBeVisible({
      timeout: 10_000,
    });
    const advancedSection = handle.page.locator('section[aria-label="Advanced runtime path"]');
    await expect(advancedSection).toBeVisible();

    createPlainFolder(world, 'alt-runtime');
    await mockDirectoryPicker(handle, `${world.workspaceRoot}/alt-runtime`);
    await advancedSection.getByRole('button', { name: 'Change runtime path' }).click();
    await expect(handle.page.locator('.restart-pending__banner')).toBeVisible({
      timeout: 10_000,
    });
    transcript.step('runtime path changed; restart-pending banner visible');

    await expect(handle.page.locator('.restart-prompt__backdrop')).toBeVisible({
      timeout: 15_000,
    });
    transcript.step('idle detected; restart prompt dialog appeared');

    await handle.page
      .locator('.restart-prompt__actions')
      .getByRole('button', { name: 'Later' })
      .click();
    await expect(handle.page.locator('.restart-prompt__backdrop')).not.toBeVisible();
    transcript.step('chose Later; prompt dismissed, pending reminder preserved');

    transcript.section('Restart Now: deliberate activation');
    await expect(handle.page.locator('.restart-pending__banner')).toBeVisible();
    const restartNowBtn = handle.page
      .locator('.restart-pending__banner')
      .getByRole('button', { name: 'Restart Now' });
    await expect(restartNowBtn).toBeVisible({ timeout: 5_000 });
    await restartNowBtn.click();
    transcript.step('clicked Restart Now on the pending banner — graceful restart initiated');

    await waitFor(
      () =>
        handle!.page
          .locator('.restart-pending__banner')
          .isHidden()
          .then((v) => v)
          .catch(() => false),
      'restart-pending banner to clear after restart',
      60_000,
    );
    await expect(handle.page.locator('.restart-pending__banner')).not.toBeVisible();
    transcript.step('restart-pending banner cleared after deliberate activation');

    // Verify the connected runtime path actually changed to the selected path.
    const postRestartConnection = await handle.page.evaluate(() =>
      window.agentico.getConnectionStatus(),
    );
    const postRestartDir = postRestartConnection.connectedRuntimeDir ?? null;
    expect(postRestartDir).toContain('alt-runtime');
    transcript.step(`connected runtime path changed to ${postRestartDir} after restart`);

    transcript.section('Constrained layout');
    await setWindowSize(handle, 760, 900);
    await evidenceShot(handle, 'config-constrained-light');
    await setWindowSize(handle, 1440, 900);
    transcript.step('captured constrained layout');

    transcript.section('No filesystem lifecycle controls');
    const pageText = await handle.page.locator('body').innerText();
    expect(pageText).not.toMatch(/create\s+new\s+(file|resource)/i);
    expect(pageText).not.toMatch(/delete\s+(file|resource)/i);
    expect(pageText).not.toMatch(/rename\s+(file|resource)/i);
    transcript.step('verified no create/rename/delete controls exist in the UI');

    const logText = persistAppLogs(handle, 'config-workspace-app');
    const discovery = readDiscovery(world);
    if (discovery?.auth_token) {
      expect(logText).not.toContain(discovery.auth_token);
    }
    transcript.step('app logs contain no bearer material');

    await closeApp(handle);
    handle = null;
    if (discovery?.pid) {
      await waitFor(
        () => {
          try {
            execFileSync('kill', ['-0', String(discovery.pid)], { stdio: 'ignore' });
            return false;
          } catch {
            return true;
          }
        },
        `server pid ${discovery.pid} to exit`,
        30_000,
      );
    }
    assertNoLeakedProcesses(world);
    transcript.step('app closed; no orphaned server processes remain');

    transcript.write(testInfo);
  } finally {
    if (handle !== null) {
      await closeApp(handle).catch(() => {});
    }
    destroyWorld(world);
  }
});
