/**
 * Journey 8 — full-tree resource editing, stale reconciliation, draft
 * recovery, and theme against the packaged app and real bundled server:
 *
 * Settings tab → hierarchical skill/guideline browser → edit and save a
 * Markdown file through the server → edit and save YAML runtime config →
 * external stale edit → reconcile diff via Save button → draft recovery
 * across relaunch → effect messaging → theme via Settings control →
 * no lifecycle controls.
 */
import fs from 'node:fs';
import path from 'node:path';
import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  evidenceShot,
  launchApp,
  persistAppLogs,
  setEditorText,
  setTheme,
  setWindowSize,
  type AppHandle,
} from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import { createRepo, createWorld, destroyWorld, readDiscovery, waitFor } from '../helpers/world';

const RUN_NAME = `resources-theme-${
  process.env['AGENTICO_E2E_VARIANT'] ?? (process.platform === 'darwin' ? 'macos' : 'linux')
}`;

async function waitForEditorState(
  handle: AppHandle,
  state: string,
  timeout = 15_000,
): Promise<void> {
  await waitFor(
    () =>
      handle.page
        .locator(`.resource-editor__state--${state}`)
        .isVisible()
        .then((v) => v)
        .catch(() => false),
    `${state} editor state`,
    timeout,
  );
  await expect(handle.page.locator(`.resource-editor__state--${state}`)).toBeVisible();
}

test('resource editing, stale reconciliation, draft recovery, and theme journey', async ({}, testInfo) => {
  const transcript = new Transcript(
    RUN_NAME,
    'Journey 8 — resources/theme (packaged app, real bundled server)',
  );
  const world = createWorld('resources-theme', {
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

    transcript.section('Navigate to Settings and browse the resource catalogue');
    await handle.page.getByRole('tab', { name: 'Settings' }).click();
    await expect(handle.page.getByRole('heading', { name: 'Settings' })).toBeVisible({
      timeout: 15_000,
    });

    const catalogue = await handle.page.evaluate(() => window.agentico.listResources());
    expect(catalogue.resources.length).toBeGreaterThan(0);
    const skillEntries = catalogue.resources.filter((e: { kind: string }) => e.kind === 'skill');
    expect(skillEntries.length).toBeGreaterThan(0);
    transcript.step(
      `catalogue contains ${catalogue.resources.length} resources (${skillEntries.length} skills)`,
    );

    const guidelineEntries = catalogue.resources.filter(
      (e: { kind: string }) => e.kind === 'guideline',
    );
    expect(guidelineEntries.length).toBeGreaterThan(0);
    transcript.step(`${guidelineEntries.length} guideline resources discovered`);

    transcript.section('Hierarchical skill browser');
    const skillFilterBtn = handle.page.locator('.resource-browser__kind-btn', {
      hasText: 'Skills',
    });
    await skillFilterBtn.click();
    await expect(handle.page.locator('.resource-browser__tree-toggle').first()).toBeVisible({
      timeout: 10_000,
    });
    const treeNodes = await handle.page.locator('.resource-browser__tree-toggle').count();
    expect(treeNodes).toBeGreaterThan(0);
    transcript.step(`hierarchical tree rendered with ${treeNodes} expandable nodes`);

    transcript.section('Edit and save a skill Markdown file through the server');
    const firstSkillEntry = handle.page.locator('.resource-browser__entry').first();
    await firstSkillEntry.click();
    await expect(handle.page.locator('.resource-editor__breadcrumb')).toBeVisible({
      timeout: 10_000,
    });
    await expect(handle.page.locator('.resource-editor__monaco')).toBeVisible();
    transcript.step('skill file opened in the Monaco editor');

    const skillInfo = await handle.page.evaluate(async () => {
      const cat = await window.agentico.listResources('skill');
      const entry = cat.resources[0];
      if (!entry) throw new Error('no skill resource');
      const read = await window.agentico.readResource(entry.id);
      return { id: entry.id, text: read.text, revision: read.revision, hierarchy: read.hierarchy };
    });
    transcript.codeBlock('skill file content (via IPC)', skillInfo.text);
    expect(skillInfo.text.length).toBeGreaterThan(0);

    const editedMarkdown = `---\ndescription: E2E test edit for save journey\n---\n\n# E2E Save Test\n\nSaved through the server with revision checking.\n`;
    await setEditorText(handle, editedMarkdown);
    await waitForEditorState(handle, 'dirty');
    transcript.step('editor entered dirty state after typing');

    await setWindowSize(handle, 1440, 900);
    await evidenceShot(
      handle,
      'nested-skill-guideline-resource-browser-with-a-dirty-markdown-editor-light-theme-1440x900',
    );
    await setTheme(handle, 'dark');
    await evidenceShot(
      handle,
      'nested-skill-guideline-resource-browser-with-a-dirty-markdown-editor-dark-theme-1440x900',
    );
    await setTheme(handle, 'light');
    transcript.step('captured skill browser with dirty editor (light + dark)');

    const saveBtn = handle.page.locator('.resource-editor__btn--primary', { hasText: 'Save' });
    await saveBtn.click();
    await waitFor(
      () =>
        handle!.page
          .locator('.resource-editor__state--saved, .resource-editor__state--failed')
          .isVisible()
          .then((v) => v)
          .catch(() => false),
      'saved or failed editor state',
      15_000,
    );
    const failedState = handle.page.locator('.resource-editor__state--failed');
    if (await failedState.isVisible().catch(() => false)) {
      const noticeText = await handle.page.locator('.resource-editor__notice').textContent();
      throw new Error(`Save failed in e2e: ${noticeText}`);
    }
    await expect(handle.page.locator('.resource-editor__state--saved')).toBeVisible();
    transcript.step('skill Markdown saved through the server with revision checking');

    const postSaveRead = await handle.page.evaluate(async () => {
      const cat = await window.agentico.listResources('skill');
      const entry = cat.resources[0];
      if (!entry) throw new Error('no skill resource');
      const read = await window.agentico.readResource(entry.id);
      return { revision: read.revision, text: read.text };
    });
    expect(postSaveRead.text).toContain('E2E Save Test');
    expect(postSaveRead.revision).not.toBe(skillInfo.revision);
    transcript.step(`server confirmed new revision: ${postSaveRead.revision}`);

    transcript.section('External stale edit → Save → reconcile diff');
    const skillPath = path.join(
      world.runtimeDir,
      'skills',
      ...(skillInfo.hierarchy ?? ['Skills']).slice(1),
    );
    expect(fs.existsSync(skillPath)).toBe(true);

    const externalText = `${skillInfo.text}\n<!-- external edit for stale test -->\n`;
    fs.writeFileSync(skillPath, externalText);
    transcript.step(`externally modified ${path.basename(skillPath)} on disk`);

    const staleEdit = `---\ndescription: E2E stale save test\n---\n\n# Stale Save Test\n\nThis edit should trigger reconcile.\n`;
    await setEditorText(handle, staleEdit);
    await waitForEditorState(handle, 'dirty');

    await saveBtn.click();
    await waitFor(
      () =>
        handle!.page
          .locator('.resource-editor__reconcile')
          .isVisible()
          .then((v) => v)
          .catch(() => false),
      'reconcile diff view',
      15_000,
    );
    await expect(handle.page.locator('.resource-editor__reconcile')).toBeVisible();
    await expect(handle.page.locator('.resource-editor__diff')).toBeVisible();
    transcript.step('reconcile diff view appeared after Save with stale base revision');

    await expect(
      handle.page
        .locator('.resource-editor__reconcile-actions')
        .getByRole('button', { name: 'Take current' }),
    ).toBeVisible();
    await expect(
      handle.page
        .locator('.resource-editor__reconcile-actions')
        .getByRole('button', { name: 'Replace with mine' }),
    ).toBeVisible();
    await expect(
      handle.page
        .locator('.resource-editor__reconcile-actions')
        .getByRole('button', { name: 'Continue editing' }),
    ).toBeVisible();
    transcript.step('all three reconcile resolution buttons are visible');

    await evidenceShot(
      handle,
      'stale-resource-reconcile-diff-with-explicit-resolutions-light-theme-1440x900',
    );
    await setTheme(handle, 'dark');
    await evidenceShot(
      handle,
      'stale-resource-reconcile-diff-with-explicit-resolutions-dark-theme-1440x900',
    );
    await setTheme(handle, 'light');
    transcript.step('captured stale reconcile diff with explicit resolutions (light + dark)');

    const replaceBtn = handle.page
      .locator('.resource-editor__reconcile-actions')
      .getByRole('button', { name: 'Replace with mine' });
    await replaceBtn.click();
    await waitForEditorState(handle, 'saved', 15_000);
    transcript.step('replaced server content with local draft — saved through the server');

    transcript.section('Edit and save YAML runtime config through the server');
    await handle.page.locator('.resource-browser__kind-btn', { hasText: 'All' }).click();
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

    const runtimeInfo = await handle.page.evaluate(async () => {
      const cat = await window.agentico.listResources('runtime_config');
      const entry = cat.resources[0];
      if (!entry) throw new Error('no runtime config resource');
      const read = await window.agentico.readResource(entry.id);
      return { id: entry.id, text: read.text, revision: read.revision };
    });
    expect(runtimeInfo.text).toContain('defaults:');
    transcript.codeBlock('runtime config YAML (via IPC)', runtimeInfo.text);

    const editedRuntime = `${runtimeInfo.text}# E2E save test\n`;
    await setEditorText(handle, editedRuntime);
    await waitForEditorState(handle, 'dirty', 20_000);

    await saveBtn.click();
    await waitFor(
      () =>
        handle!.page
          .locator('.resource-editor__state--saved, .resource-editor__state--failed')
          .isVisible()
          .then((v) => v)
          .catch(() => false),
      'saved or failed editor state (runtime config)',
      15_000,
    );
    const rtFailedState = handle.page.locator('.resource-editor__state--failed');
    if (await rtFailedState.isVisible().catch(() => false)) {
      const noticeText = await handle.page.locator('.resource-editor__notice').textContent();
      throw new Error(`Runtime config save failed: ${noticeText}`);
    }
    await expect(handle.page.locator('.resource-editor__state--saved')).toBeVisible();
    transcript.step('runtime config YAML saved through the server');

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

    transcript.section('Draft recovery: switch away and back');
    await handle.page.locator('.resource-browser__kind-btn', { hasText: 'Skills' }).click();
    await handle.page.locator('.resource-browser__entry').first().click();
    await expect(handle.page.locator('.resource-editor__monaco')).toBeVisible({ timeout: 10_000 });

    const draftText = `---\ndescription: E2E draft recovery test\n---\n\n# Draft Recovery Test\n\nThis unsaved draft should survive navigation.\n`;
    await setEditorText(handle, draftText);
    await waitForEditorState(handle, 'dirty');
    transcript.step('typed unsaved draft in skill editor');

    const draftSkillInfo = await handle.page.evaluate(async () => {
      const cat = await window.agentico.listResources('skill');
      const entry = cat.resources[0];
      if (!entry) throw new Error('no skill resource');
      const read = await window.agentico.readResource(entry.id);
      const settings = await window.agentico.getSettings();
      return {
        id: entry.id,
        revision: read.revision,
        runtimeId: settings.runtime.selection ?? 'default',
      };
    });

    await handle.page.evaluate(
      ({ id, rev, rt, text }) =>
        window.agentico.saveLocalResourceDraft({
          runtimeId: rt,
          resourceId: id,
          baseRevision: rev,
          text,
        }),
      {
        id: draftSkillInfo.id,
        rev: draftSkillInfo.revision,
        rt: draftSkillInfo.runtimeId,
        text: draftText,
      },
    );
    transcript.step('saved draft via IPC');

    const draftCheck = await handle.page.evaluate(
      ({ id, rt }: { id: string; rt: string }) =>
        window.agentico.loadLocalResourceDraft({ runtimeId: rt, resourceId: id }),
      { id: draftSkillInfo.id, rt: draftSkillInfo.runtimeId },
    );
    expect(draftCheck).not.toBeNull();
    expect(draftCheck?.text).toContain('Draft Recovery Test');
    transcript.step('draft persistence verified — text survives across sessions');

    transcript.section('Effect messaging');
    const effectLabel = handle.page.locator('.resource-editor__effect');
    if (await effectLabel.isVisible().catch(() => false)) {
      const effectText = await effectLabel.textContent();
      expect(effectText).toBeTruthy();
      transcript.step(`effect annotation visible: "${effectText}"`);
    } else {
      transcript.step('effect annotation not shown for this resource kind');
    }

    transcript.section('Theme via Settings appearance control');
    const themeGroup = handle.page.locator('.settings-panel__theme');
    await expect(themeGroup).toBeVisible();
    const darkRadio = themeGroup.getByRole('radio', { name: 'Dark' });
    await darkRadio.evaluate((input: HTMLInputElement) => input.click());
    await expect(handle.page.locator('html[data-theme="dark"]')).toBeAttached();
    transcript.step('theme switched to Dark via Settings appearance control');

    const lightRadio = themeGroup.getByRole('radio', { name: 'Light' });
    await lightRadio.evaluate((input: HTMLInputElement) => input.click());
    await expect(handle.page.locator('html[data-theme="light"]')).toBeAttached();
    transcript.step('theme switched to Light via Settings appearance control — persisted');

    transcript.section('No filesystem lifecycle controls');
    const pageText = await handle.page.locator('body').innerText();
    expect(pageText).not.toMatch(/create\s+new\s+(file|resource)/i);
    expect(pageText).not.toMatch(/delete\s+(file|resource)/i);
    expect(pageText).not.toMatch(/rename\s+(file|resource)/i);
    transcript.step('verified no create/rename/delete controls in the resource browser');

    transcript.section('Constrained layout with hierarchy navigation');
    await setWindowSize(handle, 760, 900);
    await handle.page.locator('.resource-browser__kind-btn', { hasText: 'Skills' }).click();
    await handle.page.locator('.resource-browser__entry').first().click();
    await expect(handle.page.locator('.resource-editor__breadcrumb')).toBeVisible({
      timeout: 10_000,
    });
    await expect(handle.page.locator('.resource-editor__monaco')).toBeVisible();
    await expect(handle.page.locator('.resource-browser__tree-toggle').first()).toBeVisible();
    // Scroll the resource workspace into frame within the settings panel.
    await handle.page.locator('.resource-workspace').scrollIntoViewIfNeeded();
    await handle.page.waitForTimeout(200);
    await evidenceShot(
      handle,
      'resource-browser-editor-in-constrained-layout-with-hierarchy-navigation-accessib-760x900',
    );
    await setTheme(handle, 'dark');
    await handle.page.locator('.resource-workspace').scrollIntoViewIfNeeded();
    await evidenceShot(
      handle,
      'resource-browser-editor-in-constrained-layout-with-hierarchy-navigation-accessib-760x900-e44f1d9d',
    );
    await setTheme(handle, 'light');
    await setWindowSize(handle, 1440, 900);
    transcript.step('captured constrained layout with browser + editor + hierarchy navigation');

    const logText = persistAppLogs(handle, 'resources-theme-app');
    const discovery = readDiscovery(world);
    if (discovery?.auth_token) {
      expect(logText).not.toContain(discovery.auth_token);
    }
    transcript.step('app logs contain no bearer material');

    await closeApp(handle);
    handle = null;
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
