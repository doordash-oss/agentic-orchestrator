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

import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { expect, test } from '@playwright/test';
import { closeApp, evidenceShot, launchApp, setTheme, type AppHandle } from '../helpers/app';
import { parseFeatureRepoField, parseFeatureRepos } from '../helpers/completionFixture';
import { Transcript } from '../helpers/transcript';
import { createRepo, createWorld, destroyWorld, minimalEnv, waitFor } from '../helpers/world';

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

function gitText(dir: string, ...args: string[]): string {
  return execFileSync('git', ['-C', dir, ...args], {
    encoding: 'utf8',
    env: { ...minimalEnv(), GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_SYSTEM: '/dev/null' },
  }).trim();
}

function durableFeatureIds(stateDir: string): string[] {
  return fs
    .readdirSync(stateDir, { withFileTypes: true })
    .filter(
      (entry) =>
        entry.isDirectory() && fs.existsSync(path.join(stateDir, entry.name, 'feature.yaml')),
    )
    .map((entry) => entry.name);
}

test('the creation sheet covers scoped files, initialization, the contract, setup, and retry-safe identity', async ({}, testInfo) => {
  const world = createWorld('zero-gap-creation', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const repo = createRepo(world, 'creation-lab', { commit: true });
  gitText(repo, 'remote', 'add', 'origin', path.join(world.root, 'offline-origin.git'));
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
    await expect(
      app.page.getByRole('checkbox', { name: /creation-lab.*Source: main/ }),
    ).toBeChecked();
    await expect(app.page.getByRole('radio', { name: 'Default branches' })).toBeChecked();
    await expect(app.page.getByRole('radio', { name: 'Current branches' })).not.toBeChecked();
    await app.page.getByRole('button', { name: 'Browse for folder' }).click();
    await app.page.getByRole('button', { name: 'Use this folder' }).click();
    await expect(app.page.getByText(/holds no git repository yet/i)).toBeVisible();
    await setTheme(app, 'dark');
    await evidenceShot(app, SHOTS.repositories);
    await app.page.getByRole('button', { name: /Initialize it as a repository/ }).click();
    const consent = app.page.getByRole('dialog', { name: 'Initialize a new repository?' });
    await expect(consent).toContainText(emptyRepository);
    await consent.getByRole('button', { name: 'Initialize repository' }).click();
    // The click starts asynchronous initialization and rediscovery. Wait for adoption
    // before inspecting Git so slower hosts cannot race the repository creation.
    await expect(app.page.getByRole('checkbox', { name: /initialized-lab/ })).toBeChecked();
    // Initialization honors the host's default branch; discovery must show that branch.
    const initializedBranch = gitText(emptyRepository, 'branch', '--show-current');
    await expect(
      app.page.getByRole('checkbox', {
        name: new RegExp(`initialized-lab.*Source: ${initializedBranch}`),
      }),
    ).toBeChecked();
    await expect(app.page.getByText(`Source: ${initializedBranch}`, { exact: true })).toHaveCount(
      initializedBranch === 'main' ? 2 : 1,
    );
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
    const acceptedCommits = {
      'creation-lab': gitText(repo, 'rev-parse', 'HEAD'),
      'initialized-lab': gitText(emptyRepository, 'rev-parse', 'HEAD'),
    };
    await app.page.getByRole('button', { name: 'Create', exact: true }).click();

    const cockpit = app.page.getByLabel('Feature Zero gap creation');
    await expect(cockpit).toBeVisible({ timeout: 30_000 });
    await expect(cockpit.getByText('Remote branch check unavailable')).toBeVisible();
    await expect(cockpit.getByText('Ready to start').first()).toBeVisible({ timeout: 60_000 });

    await waitFor(
      () => durableFeatureIds(world.stateDir).length === 1,
      'the single durable feature id',
    );
    const featureIds = durableFeatureIds(world.stateDir);
    const featureId = featureIds[0]!;
    const featureYaml = fs.readFileSync(
      path.join(world.stateDir, featureId, 'feature.yaml'),
      'utf8',
    );
    const runYaml = fs.readFileSync(
      path.join(world.stateDir, featureId, 'runs', 'run-001', 'run.yaml'),
      'utf8',
    );
    const storedCommits = parseFeatureRepoField(featureYaml, /^\s+commit:\s*(.+?)\s*$/);
    const worktrees = parseFeatureRepos(featureYaml);
    for (const [repoName, acceptedCommit] of Object.entries(acceptedCommits)) {
      expect(storedCommits[repoName]).toBe(acceptedCommit);
      expect(runYaml).toContain(`exact_sha: ${acceptedCommit}`);
      expect(gitText(worktrees[repoName]!, 'rev-parse', 'HEAD')).toBe(acceptedCommit);
    }
    transcript.step(
      'one idempotent authoritative creation persisted both accepted commits and created each worktree at its queued exact SHA',
    );
  } finally {
    if (handle !== undefined) await closeApp(handle);
    transcript.write(testInfo);
    destroyWorld(world);
  }
});

test('current-branch creation continues offline at the accepted slash branch commit', async ({}, testInfo) => {
  const world = createWorld('offline-current-creation', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const repo = createRepo(world, 'offline-current-lab', { commit: true });
  gitText(repo, 'checkout', '-b', 'release/2026/q3');
  fs.writeFileSync(path.join(repo, 'current.txt'), 'accepted current branch\n');
  gitText(repo, 'add', '.');
  gitText(
    repo,
    '-c',
    'user.name=Test',
    '-c',
    'user.email=test@example.invalid',
    'commit',
    '-m',
    'Advance current branch',
  );
  gitText(repo, 'remote', 'add', 'origin', path.join(world.root, 'offline-origin.git'));
  const acceptedCommit = gitText(repo, 'rev-parse', 'HEAD');
  const transcript = new Transcript('offline-current-creation', 'Offline current source pin');
  let handle: AppHandle | undefined;
  try {
    handle = await launchApp(world, testInfo, { traceName: 'offline-current-creation' });
    const app = handle;
    await app.page.getByRole('button', { name: 'New feature' }).click();
    await app.page.getByRole('checkbox', { name: /offline-current-lab/ }).check();
    await app.page.getByRole('radio', { name: 'Current branches' }).click();
    await expect(
      app.page.getByRole('checkbox', {
        name: /offline-current-lab.*Source: release\/2026\/q3/,
      }),
    ).toBeChecked();
    await app.page.getByRole('button', { name: 'Next: Describe' }).click();
    await app.page.locator('#feature-name').fill('Offline current creation');
    await app.page.getByRole('button', { name: 'Next: Depth' }).click();
    await app.page.getByRole('button', { name: 'Next: Contract' }).click();
    const creationSheet = app.page.getByRole('dialog', { name: 'New feature' });
    await creationSheet.getByRole('checkbox', { name: /Start immediately/ }).uncheck();
    await creationSheet.getByRole('button', { name: 'Create', exact: true }).click();

    const cockpit = app.page.getByLabel('Feature Offline current creation');
    await expect(cockpit.getByText('Remote branch check unavailable')).toBeVisible({
      timeout: 30_000,
    });
    await expect(cockpit.getByText('Ready to start').first()).toBeVisible({ timeout: 60_000 });
    await waitFor(
      () => durableFeatureIds(world.stateDir).length === 1,
      'the offline current feature id',
    );
    const featureIds = durableFeatureIds(world.stateDir);
    const featureId = featureIds[0]!;
    const featureYaml = fs.readFileSync(
      path.join(world.stateDir, featureId, 'feature.yaml'),
      'utf8',
    );
    const runYaml = fs.readFileSync(
      path.join(world.stateDir, featureId, 'runs', 'run-001', 'run.yaml'),
      'utf8',
    );
    const storedCommits = parseFeatureRepoField(featureYaml, /^\s+commit:\s*(.+?)\s*$/);
    const worktree = parseFeatureRepos(featureYaml)['offline-current-lab']!;
    expect(storedCommits['offline-current-lab']).toBe(acceptedCommit);
    expect(runYaml).toContain(`exact_sha: ${acceptedCommit}`);
    expect(gitText(worktree, 'rev-parse', 'HEAD')).toBe(acceptedCommit);
    expect(gitText(repo, 'branch', '--show-current')).toBe('release/2026/q3');
    expect(gitText(repo, 'status', '--porcelain')).toBe('');
    transcript.step(
      'the unavailable origin stayed nonblocking while current mode pinned the full slash branch commit and left its checkout untouched',
    );
  } finally {
    if (handle !== undefined) await closeApp(handle);
    transcript.write(testInfo);
    destroyWorld(world);
  }
});

/**
 * Pairs a repository with a real bare origin and advances the remote by two
 * commits, so the selected local source is provably behind its origin.
 */
function pairBehindOrigin(world: { root: string }, repo: string, name: string): string {
  const bare = path.join(world.root, `${name}-origin.git`);
  fs.mkdirSync(bare, { recursive: true });
  gitText(bare, 'init', '--bare', '--initial-branch=main');
  gitText(repo, 'remote', 'add', 'origin', bare);
  gitText(repo, 'push', '-u', 'origin', 'main');
  const writer = path.join(world.root, `${name}-writer`);
  execFileSync('git', ['clone', bare, writer], {
    stdio: 'pipe',
    env: { ...minimalEnv(), GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_SYSTEM: '/dev/null' },
  });
  for (const message of ['remote one', 'remote two']) {
    gitText(
      writer,
      '-c',
      'user.name=e2e',
      '-c',
      'user.email=e2e@example.invalid',
      'commit',
      '--allow-empty',
      '-m',
      message,
    );
  }
  gitText(writer, 'push', 'origin', 'main');
  return bare;
}

test('origin checks report behind, retry fresh, keep stale evidence, and continue from the local source', async ({}, testInfo) => {
  const world = createWorld('origin-behind-creation', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const repo = createRepo(world, 'origin-lab', { commit: true });
  const bare = pairBehindOrigin(world, repo, 'origin-lab');
  const originSha = gitText(bare, 'rev-parse', 'refs/heads/main');
  const acceptedCommit = gitText(repo, 'rev-parse', 'HEAD');
  if (originSha === acceptedCommit) {
    throw new Error('fixture did not place the local source behind origin');
  }
  const transcript = new Transcript('origin-behind-creation', 'Origin-check warning continuation');
  let handle: AppHandle | undefined;
  try {
    handle = await launchApp(world, testInfo, { traceName: 'origin-behind-creation' });
    const app = handle;
    await app.page.getByRole('button', { name: 'New feature' }).click();
    const sheet = app.page.getByRole('dialog', { name: 'New feature' });
    await sheet.getByRole('checkbox', { name: /origin-lab/ }).check();
    // Selection triggers the automatic check; the row reports the real
    // behind comparison against the freshly fetched origin.
    await expect(
      sheet.getByRole('checkbox', { name: /origin-lab.*Origin: 2 commits behind origin\/main/ }),
    ).toBeVisible({ timeout: 30_000 });

    // Check again is a fresh attempt: the row passes through checking
    // before the completed result returns.
    await sheet.getByRole('button', { name: 'Check again' }).click();
    await expect(sheet.getByText('Origin check still running…')).toBeVisible({ timeout: 10_000 });
    await expect(
      sheet.getByRole('checkbox', { name: /origin-lab.*Origin: 2 commits behind origin\/main/ }),
    ).toBeVisible({ timeout: 30_000 });

    // The review step names the local branch and the consequence.
    await sheet.getByRole('button', { name: 'Next: Describe' }).click();
    await app.page.locator('#feature-name').fill('Origin behind continuation');
    await sheet.getByRole('button', { name: 'Next: Depth' }).click();
    await sheet.getByRole('button', { name: 'Next: Contract' }).click();
    await expect(
      sheet.getByText(
        'origin-lab: main is 2 commits behind origin/main; the feature will start from the local source.',
      ),
    ).toBeVisible();
    transcript.step('the behind warning explained the local continuation in review');

    // A failed recheck preserves the earlier comparison as explicitly stale.
    await sheet.getByRole('button', { name: 'Back' }).click();
    await sheet.getByRole('button', { name: 'Back' }).click();
    await sheet.getByRole('button', { name: 'Back' }).click();
    gitText(repo, 'remote', 'set-url', 'origin', path.join(world.root, 'missing-origin.git'));
    await sheet.getByRole('button', { name: 'Check again' }).click();
    await expect(
      sheet.getByRole('checkbox', {
        name: /origin-lab.*Origin check unavailable — creation continues from the local source/,
      }),
    ).toBeVisible({ timeout: 30_000 });
    await expect(sheet.getByText(/Earlier comparison: 2 commits behind \(stale\)\./)).toBeVisible();
    transcript.step('the failed recheck kept the earlier comparison as stale evidence');

    // Navigation through the warnings preserved the draft, and creation
    // continues from the accepted local commit without an acknowledgement.
    await sheet.getByRole('button', { name: 'Next: Describe' }).click();
    await expect(app.page.locator('#feature-name')).toHaveValue('Origin behind continuation');
    await sheet.getByRole('button', { name: 'Next: Depth' }).click();
    await sheet.getByRole('button', { name: 'Next: Contract' }).click();
    await expect(
      sheet.getByText(
        'origin-lab: the origin check could not complete; the feature will start from main. An earlier comparison (2 commits behind) is preserved but stale.',
      ),
    ).toBeVisible();
    await sheet.getByRole('checkbox', { name: /Start immediately/ }).uncheck();
    await sheet.getByRole('button', { name: 'Create', exact: true }).click();

    const cockpit = app.page.getByLabel('Feature Origin behind continuation');
    await expect(cockpit).toBeVisible({ timeout: 30_000 });
    await expect(cockpit.getByText('Ready to start').first()).toBeVisible({ timeout: 60_000 });
    await waitFor(
      () => durableFeatureIds(world.stateDir).length === 1,
      'the origin continuation feature id',
    );
    const featureId = durableFeatureIds(world.stateDir)[0]!;
    const featureYaml = fs.readFileSync(
      path.join(world.stateDir, featureId, 'feature.yaml'),
      'utf8',
    );
    const runYaml = fs.readFileSync(
      path.join(world.stateDir, featureId, 'runs', 'run-001', 'run.yaml'),
      'utf8',
    );
    const storedCommits = parseFeatureRepoField(featureYaml, /^\s+commit:\s*(.+?)\s*$/);
    const worktree = parseFeatureRepos(featureYaml)['origin-lab']!;
    expect(storedCommits['origin-lab']).toBe(acceptedCommit);
    expect(runYaml).toContain(`exact_sha: ${acceptedCommit}`);
    expect(gitText(worktree, 'rev-parse', 'HEAD')).toBe(acceptedCommit);
    expect(gitText(repo, 'branch', '--show-current')).toBe('main');
    expect(gitText(repo, 'status', '--porcelain')).toBe('');
    transcript.step(
      'creation continued at the accepted local commit despite the failed origin check',
    );
  } finally {
    if (handle !== undefined) await closeApp(handle);
    transcript.write(testInfo);
    destroyWorld(world);
  }
});

test('creation continues from the local source while an origin check is still running', async ({}, testInfo) => {
  const world = createWorld('origin-running-creation', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const repo = createRepo(world, 'running-lab', { commit: true });
  pairBehindOrigin(world, repo, 'running-lab');
  const acceptedCommit = gitText(repo, 'rev-parse', 'HEAD');
  // Origin answers never arrive: the transport accepts connections and then
  // stays silent, so the automatic check remains in flight well past submit.
  const hangingServer = http.createServer(() => {
    /* deliberately never responds */
  });
  const listening = new Promise<void>((resolve) =>
    hangingServer.once('listening', () => resolve()),
  );
  hangingServer.listen(0, '127.0.0.1');
  await listening;
  const address = hangingServer.address();
  const port = typeof address === 'object' && address !== null ? address.port : 0;
  gitText(
    repo,
    'remote',
    'set-url',
    'origin',
    `http://127.0.0.1:${String(port)}/running-origin.git`,
  );
  const transcript = new Transcript(
    'origin-running-creation',
    'Continuation during a running check',
  );
  let handle: AppHandle | undefined;
  try {
    handle = await launchApp(world, testInfo, { traceName: 'origin-running-creation' });
    const app = handle;
    await app.page.getByRole('button', { name: 'New feature' }).click();
    const sheet = app.page.getByRole('dialog', { name: 'New feature' });
    await sheet.getByRole('checkbox', { name: /running-lab/ }).check();
    await expect(sheet.getByText('Origin check still running…')).toBeVisible({ timeout: 30_000 });

    // Walk to review while the check is still in flight: the pending origin
    // check never gates any step.
    await sheet.getByRole('button', { name: 'Next: Describe' }).click();
    await app.page.locator('#feature-name').fill('Origin running continuation');
    await sheet.getByRole('button', { name: 'Next: Depth' }).click();
    await sheet.getByRole('button', { name: 'Next: Contract' }).click();
    await expect(
      sheet.getByText('running-lab: origin check still running; the feature will start from main.'),
    ).toBeVisible();
    await sheet.getByRole('checkbox', { name: /Start immediately/ }).uncheck();
    await sheet.getByRole('button', { name: 'Create', exact: true }).click();

    const cockpit = app.page.getByLabel('Feature Origin running continuation');
    // Acceptance serializes with the in-flight check on the repository's
    // mutation boundary, so submission waits out the server's 60-second
    // attempt deadline before it completes; the client allowance covers the
    // deadline plus response delivery.
    await expect(cockpit).toBeVisible({ timeout: 90_000 });
    await expect(cockpit.getByText('Ready to start').first()).toBeVisible({ timeout: 60_000 });
    await waitFor(
      () => durableFeatureIds(world.stateDir).length === 1,
      'the running-check feature id',
    );
    const featureId = durableFeatureIds(world.stateDir)[0]!;
    const featureYaml = fs.readFileSync(
      path.join(world.stateDir, featureId, 'feature.yaml'),
      'utf8',
    );
    const runYaml = fs.readFileSync(
      path.join(world.stateDir, featureId, 'runs', 'run-001', 'run.yaml'),
      'utf8',
    );
    const storedCommits = parseFeatureRepoField(featureYaml, /^\s+commit:\s*(.+?)\s*$/);
    const worktree = parseFeatureRepos(featureYaml)['running-lab']!;
    expect(storedCommits['running-lab']).toBe(acceptedCommit);
    expect(runYaml).toContain(`exact_sha: ${acceptedCommit}`);
    expect(gitText(worktree, 'rev-parse', 'HEAD')).toBe(acceptedCommit);
    transcript.step(
      'submission proceeded while the check was still running and pinned the accepted local commit',
    );
  } finally {
    if (handle !== undefined) await closeApp(handle);
    hangingServer.closeAllConnections();
    hangingServer.close();
    transcript.write(testInfo);
    destroyWorld(world);
  }
});
