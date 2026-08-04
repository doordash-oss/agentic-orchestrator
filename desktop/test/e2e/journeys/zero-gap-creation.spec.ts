import fs from 'node:fs';
import path from 'node:path';
import { expect, test } from '@playwright/test';
import { closeApp, evidenceShot, launchApp, setTheme, type AppHandle } from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import { createRepo, createWorld, destroyWorld } from '../helpers/world';

const SHOTS = {
  home: 'ready-runtime-home-with-branded-welcome-visible-global-commands-and-no-terminal-1440x900',
  help: 'ready-runtime-home-with-shortcut-help-overlay-and-visible-keyboard-focus-dark-th-760x900',
  what: 'creation-what-step-with-image-and-file-previews-plus-repository-scoped-fuzzy-pic-1440x900',
  where:
    'creation-where-step-with-repository-browser-eligibility-detail-and-initializatio-1440x900',
  pipeline:
    'creation-pipeline-step-with-profile-cards-and-effective-gate-summary-light-theme-1440x900',
  review: 'creation-review-step-with-models-checkpoints-exit-criteria-and-complete-s-1440x900',
} as const;

test('four-step creation covers scoped files, initialization, review, setup, and retry-safe identity', async ({}, testInfo) => {
  const world = createWorld('zero-gap-creation', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const repo = createRepo(world, 'creation-lab', { commit: true });
  fs.mkdirSync(path.join(repo, 'src'), { recursive: true });
  fs.writeFileSync(path.join(repo, 'src', 'creation-context.md'), '# Creation context\n');
  const image = path.join(world.root, 'brief.png');
  const attachment = path.join(world.root, 'acceptance.md');
  fs.writeFileSync(image, 'bounded-image-fixture');
  fs.writeFileSync(attachment, '# Acceptance\nCreate exactly once.\n');
  const additionalRoot = path.join(world.root, 'additional-workspace');
  const emptyRepository = path.join(additionalRoot, 'initialized-lab');
  fs.mkdirSync(emptyRepository, { recursive: true });
  const transcript = new Transcript('zero-gap-creation', 'Four-step authoritative creation');
  let handle: AppHandle | undefined;
  try {
    handle = await launchApp(world, testInfo, { traceName: 'zero-gap-creation' });
    const app = handle;
    await app.app.evaluate(
      ({ dialog }, answers) => {
        const directories = [answers.emptyRepository];
        dialog.showOpenDialog = (async (...args: unknown[]) => {
          const options = args.at(-1) as { title?: string };
          const selected = options.title?.includes('images')
            ? answers.image
            : options.title?.includes('attachments')
              ? answers.attachment
              : directories.shift();
          return {
            canceled: selected === undefined,
            filePaths: selected ? [selected] : [],
            bookmarks: [],
          };
        }) as typeof dialog.showOpenDialog;
      },
      { image, attachment, emptyRepository },
    );

    await expect(app.page.getByRole('button', { name: 'New feature' })).toBeVisible();
    await app.page.setViewportSize({ width: 1440, height: 900 });
    await setTheme(app, 'light');
    await evidenceShot(app, SHOTS.home);

    await app.page.keyboard.press('ControlOrMeta+K');
    await app.page.getByLabel('Search commands').fill('keyboard shortcuts');
    await app.page.keyboard.press('Enter');
    await app.page.setViewportSize({ width: 760, height: 900 });
    await setTheme(app, 'dark');
    await expect(app.page.getByRole('dialog', { name: 'Keyboard shortcuts' })).toBeVisible();
    await evidenceShot(app, SHOTS.help);
    await app.page.keyboard.press('Escape');

    await app.page.setViewportSize({ width: 1440, height: 900 });
    await app.page.getByRole('button', { name: 'New feature' }).click();
    await app.page.getByRole('checkbox', { name: /creation-lab/ }).check();
    await app.page.getByRole('button', { name: 'Browse for folder' }).click();
    await app.page.getByRole('button', { name: 'Use this folder' }).click();
    await expect(app.page.getByText(/holds no git repository yet/i)).toBeVisible();
    await setTheme(app, 'dark');
    await evidenceShot(app, SHOTS.where);
    await app.page.getByRole('button', { name: /Initialize it as a repository/ }).click();
    const consent = app.page.getByRole('dialog', { name: 'Initialize a new repository?' });
    await expect(consent).toContainText(emptyRepository);
    await consent.getByRole('button', { name: 'Initialize repository' }).click();
    // A single unambiguous discovery selects itself.
    await expect(app.page.getByRole('checkbox', { name: /initialized-lab/ })).toBeChecked();
    transcript.step(
      'Where adopted a folder as a root, consented to server-owned initialization, and observed the rediscovered repository select itself',
    );

    await app.page.getByRole('button', { name: 'Next: What' }).click();
    await app.page.locator('#feature-name').fill('Zero gap creation');
    await app.page
      .locator('#feature-description')
      .fill('Prove the complete desktop creation contract. See @creation-context');
    const repoFile = app.page.getByRole('option', { name: /creation-lab.*creation-context/ });
    await expect(repoFile).toBeVisible();
    await repoFile.click();
    await expect(
      app.page.getByRole('button', { name: /Remove reference creation-lab/ }),
    ).toBeVisible();
    await app.page.getByRole('button', { name: 'Attach files or photos' }).click();
    await app.page.getByRole('menuitem', { name: 'Add photos' }).click();
    await app.page.getByRole('button', { name: 'Attach files or photos' }).click();
    await app.page.getByRole('menuitem', { name: 'Add files' }).click();
    await setTheme(app, 'light');
    await evidenceShot(app, SHOTS.what);
    transcript.step(
      'What preserved ordered native-picked inputs and an @-mentioned repository file',
    );

    await app.page.getByRole('button', { name: 'Next: Pipeline' }).click();
    await app.page.getByRole('radio', { name: /Large/ }).check();
    await setTheme(app, 'light');
    await evidenceShot(app, SHOTS.pipeline);
    await app.page.getByRole('button', { name: 'Next: Review' }).click();
    await app.page.getByLabel('Risk').selectOption('high');
    await app.page.getByLabel('Inquireness').selectOption('high');
    await app.page
      .getByText('Exit criteria')
      .locator('..')
      .getByRole('textbox')
      .fill('All focused checks pass.');
    await setTheme(app, 'dark');
    await app.page.evaluate(() => {
      const wizard = document.querySelector('.creation-wizard');
      if (wizard instanceof HTMLElement) wizard.style.zoom = '0.72';
      const panel = document.querySelector('.tab-panel');
      if (panel instanceof HTMLElement) panel.scrollTop = 0;
    });
    await evidenceShot(app, SHOTS.review);
    await app.page.evaluate(() => {
      const wizard = document.querySelector('.creation-wizard');
      if (wizard instanceof HTMLElement) wizard.style.zoom = '1';
    });
    await app.page.getByRole('checkbox', { name: /Start immediately/ }).uncheck();
    await app.page.getByRole('button', { name: 'Create feature' }).click();

    const cockpit = app.page.getByLabel('Feature Zero gap creation');
    await expect(cockpit).toBeVisible({ timeout: 30_000 });
    await expect(cockpit.getByText(/Setting up|Ready to start|Setup failed/).first()).toBeVisible({
      timeout: 60_000,
    });
    transcript.step('one idempotent authoritative creation opened its setup-backed cockpit');
  } finally {
    if (handle !== undefined) await closeApp(handle);
    transcript.write(testInfo);
    destroyWorld(world);
  }
});
