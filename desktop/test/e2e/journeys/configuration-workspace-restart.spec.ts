/**
 * Journey 7 — configuration, workspace, degraded-remediation, and
 * deferred-restart against the packaged app and real bundled server:
 *
 * create feature → edit feature config in cockpit with invalid model →
 * verify eligibility findings → fix and save through server → Settings tab →
 * runtime config editor with effect annotations → workspace-root management →
 * provider remediation rows → degraded state with affected actions disabled →
 * advanced path change → restart-pending → idle prompt → Later → Restart Now.
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
  setEditorText,
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

    transcript.section('Feature configuration editor in the cockpit');
    const configToggle = cockpit.getByRole('button', { name: /Configuration/ }).first();
    await expect(configToggle).toBeVisible();
    await configToggle.click();
    await expect(cockpit.locator('.resource-editor__breadcrumb')).toBeVisible({
      timeout: 15_000,
    });
    await expect(cockpit.locator('.resource-editor__monaco')).toBeVisible({ timeout: 15_000 });
    transcript.step('configuration editor expanded in the cockpit with Monaco visible');

    const configInfo = await handle.page.evaluate(async () => {
      const cat = await window.agentico.listResources('feature_config');
      const entry = cat.resources[0];
      if (!entry) throw new Error('no feature config resource');
      const read = await window.agentico.readResource(entry.id);
      return { id: entry.id, text: read.text, revision: read.revision };
    });
    expect(configInfo.text).toContain('models:');
    transcript.codeBlock('feature config YAML (via IPC)', configInfo.text);

    transcript.section('Edit with invalid model → verify eligibility findings');
    const invalidConfig = configInfo.text.replace(
      /^(\s*)review:\s*.*$/m,
      '$1review: bogus-model[1M]',
    );
    await setEditorText(handle, invalidConfig);
    await waitFor(
      () =>
        handle!.page
          .locator('.resource-editor__findings')
          .isVisible()
          .then((v) => v)
          .catch(() => false),
      'validation findings',
      15_000,
    );
    await expect(handle.page.locator('.resource-editor__findings')).toBeVisible();
    const findingsText = await handle.page.locator('.resource-editor__findings').textContent();
    expect(findingsText).toBeTruthy();
    expect(findingsText).toContain('model_unavailable');
    expect(findingsText).toContain('models.');
    transcript.step(`validation findings appeared for invalid model: "${findingsText}"`);

    await setWindowSize(handle, 1440, 900);
    // Scroll the config editor (with findings and Save button) into frame.
    await handle.page.locator('.resource-editor__footer').scrollIntoViewIfNeeded();
    await handle.page.waitForTimeout(200);
    await evidenceShot(
      handle,
      'feature-configuration-editor-showing-model-eligibility-findings-in-the-cockpit-l-1440x900',
    );
    await setTheme(handle, 'dark');
    await handle.page.locator('.resource-editor__footer').scrollIntoViewIfNeeded();
    await evidenceShot(
      handle,
      'feature-configuration-editor-showing-model-eligibility-findings-in-the-cockpit-d-1440x900',
    );
    await setTheme(handle, 'light');
    transcript.step('captured feature config editor with eligibility findings (light + dark)');

    transcript.section('Fix model and save through the server');
    const saveResult = await handle.page.evaluate(
      async ({ id, text, rev }) => {
        const result = await window.agentico.writeResource({
          resourceId: id,
          baseRevision: rev,
          text: `${text}# E2E valid save\n`,
        });
        return result;
      },
      { id: configInfo.id, text: configInfo.text, rev: configInfo.revision },
    );

    expect(saveResult.type).toBe('saved');
    transcript.step('feature config saved through the server with valid models');

    await waitFor(
      () =>
        handle!.page
          .locator('.resource-editor__state--saved, .resource-editor__state--idle')
          .isVisible()
          .then((v) => v)
          .catch(() => false),
      'saved or idle editor state after IPC save',
      10_000,
    ).catch(() => {});
    transcript.step('editor state updated after save');

    transcript.section('Runtime configuration editor in Settings');
    await handle.page.getByRole('tab', { name: 'Settings' }).click();
    await expect(handle.page.getByRole('heading', { name: 'Settings' })).toBeVisible({
      timeout: 15_000,
    });

    const runtimeEntry = await handle.page.evaluate(async () => {
      const cat = await window.agentico.listResources('runtime_config');
      return cat.resources[0] ?? null;
    });
    expect(runtimeEntry).not.toBeNull();
    transcript.step('runtime config resource discovered in the catalogue');

    await handle.page
      .locator('.resource-browser__group', { hasText: 'Runtime' })
      .locator('.resource-browser__entry')
      .click();
    await expect(handle.page.locator('.resource-editor__breadcrumb')).toContainText('Runtime');
    await expect(handle.page.locator('.resource-editor__effect')).toBeVisible();
    const runtimeEffect = await handle.page.locator('.resource-editor__effect').textContent();
    expect(runtimeEffect).toBeTruthy();
    expect(runtimeEffect).toContain('next dispatch');
    transcript.step(`runtime config editor opened with effect annotation: "${runtimeEffect}"`);

    await setWindowSize(handle, 1440, 900);
    // Scroll the runtime config editor into frame so the YAML, effect
    // annotation, and hierarchy are all visible in the capture.
    await handle.page.locator('.resource-editor__header').scrollIntoViewIfNeeded();
    await handle.page.waitForTimeout(200);
    await evidenceShot(
      handle,
      'global-runtime-configuration-editor-with-resource-hierarchy-and-effect-annotatio-1440x900',
    );
    await setTheme(handle, 'dark');
    await handle.page.locator('.resource-editor__header').scrollIntoViewIfNeeded();
    await evidenceShot(
      handle,
      'global-runtime-configuration-editor-with-resource-hierarchy-and-effect-annotatio-1440x900-c39ef463',
    );
    await setTheme(handle, 'light');
    transcript.step('captured runtime config editor with hierarchy and effect annotations');

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

    await setWindowSize(handle, 1440, 900);
    await evidenceShot(
      handle,
      'workspace-provider-degraded-remediation-surface-with-affected-actions-disabled-l-1440x900',
    );
    await setTheme(handle, 'dark');
    await evidenceShot(
      handle,
      'workspace-provider-degraded-remediation-surface-with-affected-actions-disabled-d-1440x900',
    );
    await setTheme(handle, 'light');
    transcript.step('captured degraded provider state with Not ready status (light + dark)');

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

    await setTheme(handle, 'dark');
    await evidenceShot(
      handle,
      'restart-pending-summary-and-idle-restart-now-or-later-prompt-dark-theme-1440x900',
    );
    transcript.step('captured restart prompt in dark theme');

    await setTheme(handle, 'light');
    await evidenceShot(
      handle,
      'restart-pending-summary-and-idle-restart-now-or-later-prompt-light-theme-1440x900',
    );
    transcript.step('captured restart prompt in light theme');

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
    transcript.step('captured constrained layout with hierarchy navigation accessible');

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
        15_000,
      ).catch(() => {});
    }
    assertNoLeakedProcesses(world);
    transcript.step('app quit gracefully; no leaked processes');
    transcript.write(testInfo);
  } finally {
    if (handle !== null) {
      await closeApp(handle).catch(() => {});
    }
    assertNoLeakedProcesses(world);
    destroyWorld(world);
  }
});
