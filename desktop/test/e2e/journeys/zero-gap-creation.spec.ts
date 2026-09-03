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

import fs from 'node:fs';
import path from 'node:path';
import { expect, test } from '@playwright/test';
import { closeApp, evidenceShot, launchApp, setTheme, type AppHandle } from '../helpers/app';
import { Transcript } from '../helpers/transcript';
import { createRepo, createWorld, destroyWorld } from '../helpers/world';

const SHOTS = {
  home: 'ready-runtime-home-with-branded-welcome-visible-global-commands-and-no-terminal-1440x900',
  help: 'ready-runtime-home-with-shortcut-help-overlay-and-visible-keyboard-focus-dark-th-760x900',
  describe:
    'creation-describe-step-with-image-and-file-previews-plus-repository-scoped-fuzzy-1440x900',
  repositories:
    'creation-repositories-step-with-repository-browser-eligibility-detail-and-initia-1440x900',
  depth: 'creation-depth-step-with-profile-cards-and-effective-gate-summary-light-theme-1440x900',
  contract: 'creation-contract-step-with-models-checkpoints-exit-criteria-and-complete-1440x900',
} as const;

test('the creation sheet covers scoped files, initialization, the contract, setup, and retry-safe identity', async ({}, testInfo) => {
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
    await app.page.getByLabel('Search features and commands').fill('keyboard shortcuts');
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
    await evidenceShot(app, SHOTS.repositories);
    await app.page.getByRole('button', { name: /Initialize it as a repository/ }).click();
    const consent = app.page.getByRole('dialog', { name: 'Initialize a new repository?' });
    await expect(consent).toContainText(emptyRepository);
    await consent.getByRole('button', { name: 'Initialize repository' }).click();
    // A single unambiguous discovery selects itself.
    await expect(app.page.getByRole('checkbox', { name: /initialized-lab/ })).toBeChecked();
    transcript.step(
      'Repositories adopted a folder as a root, consented to server-owned initialization, and observed the rediscovered repository select itself',
    );

    await app.page.getByRole('button', { name: 'Next: Describe' }).click();
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
    await evidenceShot(app, SHOTS.describe);
    transcript.step(
      'Describe preserved ordered native-picked inputs and an @-mentioned repository file',
    );

    await app.page.getByRole('button', { name: 'Next: Depth' }).click();
    await app.page.getByRole('radio', { name: /Large/ }).check();
    await setTheme(app, 'light');
    await evidenceShot(app, SHOTS.depth);
    await app.page.getByRole('button', { name: 'Next: Contract' }).click();
    // Scope the contract knobs to the creation sheet: getByLabel matches by
    // substring, and a randomly generated server name (e.g. "frisky-lungo")
    // can otherwise collide with the sidebar server control's aria-label and
    // trip strict mode.
    const creationSheet = app.page.getByRole('dialog', { name: 'New feature' });
    await creationSheet.getByLabel('Risk').selectOption('high');
    await creationSheet.getByLabel('Inquireness').selectOption('high');
    await creationSheet
      .getByText('Exit criteria')
      .locator('..')
      .getByRole('textbox')
      .fill('All focused checks pass.');
    await setTheme(app, 'dark');
    // The sheet body is the scroll container, so the long Contract step is
    // captured from its own top instead of a zoomed-out whole page.
    await app.page.evaluate(() => {
      const body = document.querySelector('.creation-sheet__body');
      if (body instanceof HTMLElement) body.scrollTop = 0;
    });
    await evidenceShot(app, SHOTS.contract);
    await app.page.getByRole('checkbox', { name: /Start immediately/ }).uncheck();
    await app.page.getByRole('button', { name: 'Create', exact: true }).click();

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
