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
import net from 'node:net';
import path from 'node:path';
import { expect, test } from '@playwright/test';
import {
  assertNoLeakedProcesses,
  closeApp,
  evidenceShot,
  launchApp,
  type AppHandle,
} from '../helpers/app';
import { parseFeatureRepoField, parseFeatureRepos } from '../helpers/completionFixture';
import { Transcript } from '../helpers/transcript';
import { createRepo, createWorld, destroyWorld, minimalEnv, waitFor } from '../helpers/world';
import type { JourneyWorld } from '../helpers/world';

const HERMETIC_GIT_ENV = {
  ...minimalEnv(),
  GIT_CONFIG_GLOBAL: '/dev/null',
  GIT_CONFIG_SYSTEM: '/dev/null',
} as const;

function gitText(dir: string, ...args: string[]): string {
  return execFileSync('git', ['-C', dir, ...args], {
    encoding: 'utf8',
    env: HERMETIC_GIT_ENV,
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

/**
 * Pairs a repository with a real bare origin and leaves the local default
 * branch UNOCCUPIED: the original checkout moves to a `work` branch (with
 * unrelated dirty state) while a writer clone advances origin/main by two
 * commits. The default-mode source still resolves `main` through
 * origin/HEAD, mirroring the Go update-fixture contract.
 */
function pairUnoccupiedBehindOrigin(
  world: JourneyWorld,
  name: string,
): { repo: string; bare: string } {
  const repo = createRepo(world, name, { commit: true });
  const bare = path.join(world.root, `${name}-origin.git`);
  fs.mkdirSync(bare, { recursive: true });
  gitText(bare, 'init', '--bare');
  gitText(repo, 'remote', 'add', 'origin', bare);
  gitText(repo, 'push', 'origin', 'main');
  // The default-mode source resolves through origin/HEAD, not the checkout
  // HEAD: pin it to main while the checkout sits on `work`.
  gitText(repo, 'symbolic-ref', 'refs/remotes/origin/HEAD', 'refs/remotes/origin/main');
  gitText(repo, 'checkout', '-b', 'work');
  const writer = path.join(world.root, `${name}-writer`);
  execFileSync('git', ['clone', bare, writer], { stdio: 'pipe', env: HERMETIC_GIT_ENV });
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
  return { repo, bare };
}

/** Reserves an ephemeral port and frees it so nothing can answer there. */
async function pickDeadPort(): Promise<number> {
  const server = net.createServer();
  const listening = new Promise<void>((resolve) => server.once('listening', () => resolve()));
  server.listen(0, '127.0.0.1');
  await listening;
  const address = server.address();
  const port = typeof address === 'object' && address !== null ? address.port : 0;
  server.close();
  if (port === 0) throw new Error('could not reserve a dead port');
  return port;
}

/**
 * Pairs a repository with a real bare origin and leaves the default branch
 * HELD BY THE ORIGINAL CHECKOUT: the checkout stays ON `main`, clean, while
 * a writer clone advances origin/main by two commits with real file changes
 * (a rewritten tracked README and a new file extended by the second commit).
 * Current-mode selection resolves the checked-out branch; default mode still
 * resolves `main` through origin/HEAD.
 */
function pairOccupiedBehindOrigin(
  world: JourneyWorld,
  name: string,
): { repo: string; bare: string } {
  const repo = createRepo(world, name, { commit: true });
  const bare = path.join(world.root, `${name}-origin.git`);
  fs.mkdirSync(bare, { recursive: true });
  gitText(bare, 'init', '--bare');
  gitText(repo, 'remote', 'add', 'origin', bare);
  gitText(repo, 'push', 'origin', 'main');
  gitText(repo, 'symbolic-ref', 'refs/remotes/origin/HEAD', 'refs/remotes/origin/main');
  const writer = path.join(world.root, `${name}-writer`);
  execFileSync('git', ['clone', bare, writer], { stdio: 'pipe', env: HERMETIC_GIT_ENV });
  // Real file changes, not empty commits: the fast-forward must advance the
  // checkout's index and working files, and the assertions compare bytes.
  fs.writeFileSync(path.join(writer, 'README.md'), `# ${name} (advanced by origin)\n`);
  fs.writeFileSync(path.join(writer, 'remote-notes.txt'), 'remote note one\n');
  gitText(writer, 'add', '.');
  gitText(
    writer,
    '-c',
    'user.name=e2e',
    '-c',
    'user.email=e2e@example.invalid',
    'commit',
    '-m',
    'remote one',
  );
  fs.writeFileSync(path.join(writer, 'remote-notes.txt'), 'remote note one\nremote note two\n');
  gitText(writer, 'add', '.');
  gitText(
    writer,
    '-c',
    'user.name=e2e',
    '-c',
    'user.email=e2e@example.invalid',
    'commit',
    '-m',
    'remote two',
  );
  gitText(writer, 'push', 'origin', 'main');
  return { repo, bare };
}

test('the per-row Update from origin advances an unoccupied source through the real server and preserves the dirty checkout', async ({}, testInfo) => {
  const world = createWorld('repository-source-update', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const { repo, bare } = pairUnoccupiedBehindOrigin(world, 'update-lab');

  // Unrelated dirty state on the occupied `work` checkout: an unstaged
  // tracked edit, a staged new file, and an untracked file.
  fs.writeFileSync(path.join(repo, 'README.md'), '# update-lab (locally edited)\n');
  fs.writeFileSync(path.join(repo, 'staged.txt'), 'staged bytes\n');
  gitText(repo, 'add', 'staged.txt');
  fs.writeFileSync(path.join(repo, 'untracked.txt'), 'untracked bytes\n');
  const statusBefore = gitText(repo, 'status', '--porcelain');
  const indexBefore = gitText(repo, 'ls-files', '--stage');
  const workSha = gitText(repo, 'rev-parse', 'HEAD');
  const staleSha = gitText(repo, 'rev-parse', 'refs/heads/main');
  const originSha = gitText(bare, 'rev-parse', 'refs/heads/main');
  if (originSha === staleSha) {
    throw new Error('fixture did not place the unoccupied source behind origin');
  }

  const transcript = new Transcript(
    'repository-source-update',
    'Per-row Update from origin (packaged app, real bundled server)',
  );
  let handle: AppHandle | undefined;
  try {
    handle = await launchApp(world, testInfo, { traceName: 'repository-source-update' });
    const app = handle;
    await app.page.getByRole('button', { name: 'New feature' }).click();
    const sheet = app.page.getByRole('dialog', { name: 'New feature' });
    await sheet.getByRole('checkbox', { name: /update-lab/ }).check();

    // Selection triggers the automatic origin check: the unoccupied default
    // branch reports the real behind comparison from the bare origin.
    await expect(
      sheet.getByRole('checkbox', { name: /update-lab.*Origin: 2 commits behind origin\/main/ }),
    ).toBeVisible({ timeout: 30_000 });
    const row = sheet.locator('.creation-sheet__row-item', { hasText: 'update-lab' });
    await expect(
      row.getByText(/Advances main to origin\/main in the original repository on /),
    ).toBeVisible();
    transcript.step('the behind row offered Update from origin with its impact copy');

    // The action sends the displayed expectations through the real server.
    await row.getByRole('button', { name: 'Update from origin' }).click();
    await expect(
      row.getByText(
        new RegExp(`Updated main to origin\\/main on .+ \\(now at ${originSha.slice(0, 7)}\\)\\.`),
      ),
    ).toBeVisible({ timeout: 60_000 });
    await evidenceShot(app, 'repository-source-update-row-updated-from-origin');
    transcript.step('the update advanced the branch and announced the resulting tip');

    // Real-git evidence: only refs/heads/main advanced, to the freshly
    // verified origin tip; every checkout stayed exactly as it was.
    expect(gitText(repo, 'rev-parse', 'refs/heads/main')).toBe(originSha);
    expect(gitText(repo, 'rev-parse', 'refs/heads/work')).toBe(workSha);
    expect(gitText(repo, 'symbolic-ref', '--quiet', 'HEAD')).toBe('refs/heads/work');
    expect(gitText(repo, 'status', '--porcelain')).toBe(statusBefore);
    expect(gitText(repo, 'ls-files', '--stage')).toBe(indexBefore);
    expect(fs.readFileSync(path.join(repo, 'README.md'), 'utf8')).toBe(
      '# update-lab (locally edited)\n',
    );
    expect(fs.readFileSync(path.join(repo, 'staged.txt'), 'utf8')).toBe('staged bytes\n');
    expect(fs.readFileSync(path.join(repo, 'untracked.txt'), 'utf8')).toBe('untracked bytes\n');
    transcript.step('the ref-only update preserved HEAD, the index, and all working files');

    // The refreshed comparison confirms the source is now up to date.
    await expect(
      sheet.getByRole('checkbox', { name: /update-lab.*Origin: up to date with origin\/main/ }),
    ).toBeVisible({ timeout: 30_000 });

    // Creation accepts and persists the updated local SHA through the
    // exact-start setup contract.
    await sheet.getByRole('button', { name: 'Next: Describe' }).click();
    await app.page.locator('#feature-name').fill('Repository source update');
    await sheet.getByRole('button', { name: 'Next: Depth' }).click();
    await sheet.getByRole('button', { name: 'Next: Contract' }).click();
    await expect(
      sheet.getByText(/update-lab: main is up to date with origin\/main\./),
    ).toBeVisible();
    await expect(sheet.getByText(/Updated main to origin\/main on /)).toBeVisible();
    await sheet.getByRole('checkbox', { name: /Start immediately/ }).uncheck();
    await sheet.getByRole('button', { name: 'Create', exact: true }).click();

    const cockpit = app.page.getByLabel('Feature Repository source update');
    await expect(cockpit).toBeVisible({ timeout: 30_000 });
    await expect(cockpit.getByText('Ready to start').first()).toBeVisible({ timeout: 60_000 });
    await waitFor(() => durableFeatureIds(world.stateDir).length === 1, 'the update feature id');
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
    const worktree = parseFeatureRepos(featureYaml)['update-lab']!;
    expect(storedCommits['update-lab']).toBe(originSha);
    expect(runYaml).toContain(`exact_sha: ${originSha}`);
    expect(gitText(worktree, 'rev-parse', 'HEAD')).toBe(originSha);
    // The original repository is untouched by feature setup: still on the
    // dirty `work` checkout.
    expect(gitText(repo, 'branch', '--show-current')).toBe('work');
    expect(gitText(repo, 'status', '--porcelain')).toBe(statusBefore);
    transcript.step('the feature started from the updated accepted SHA with the source untouched');
  } finally {
    if (handle !== undefined) await closeApp(handle);
    transcript.write(testInfo);
    destroyWorld(world);
  }
});

test('the per-row Update from origin fast-forwards the original checkout in current mode and creation pins the advanced tip', async ({}, testInfo) => {
  const world = createWorld('repository-source-update-original', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const { repo, bare } = pairOccupiedBehindOrigin(world, 'update-current-lab');
  const staleSha = gitText(repo, 'rev-parse', 'refs/heads/main');
  const originSha = gitText(bare, 'rev-parse', 'refs/heads/main');
  if (originSha === staleSha) {
    throw new Error('fixture did not place the original checkout behind origin');
  }
  if (gitText(repo, 'status', '--porcelain') !== '') {
    throw new Error('fixture did not leave the original checkout clean');
  }

  const transcript = new Transcript(
    'repository-source-update-original',
    'Original-checkout Update from origin (packaged app, real bundled server)',
  );
  let handle: AppHandle | undefined;
  try {
    handle = await launchApp(world, testInfo, { traceName: 'repository-source-update-original' });
    const app = handle;
    await app.page.getByRole('button', { name: 'New feature' }).click();
    const sheet = app.page.getByRole('dialog', { name: 'New feature' });
    await sheet.getByRole('checkbox', { name: /update-current-lab/ }).check();
    // Current-branch mode selects the checked-out branch of the original
    // repository: main, held only by that checkout.
    await sheet.getByRole('radio', { name: 'Current branches' }).click();
    await expect(sheet.getByRole('radio', { name: 'Current branches' })).toBeChecked();
    await expect(
      sheet.getByRole('checkbox', {
        name: /update-current-lab.*Source: main.*Origin: 2 commits behind origin\/main/,
      }),
    ).toBeVisible({ timeout: 30_000 });
    const row = sheet.locator('.creation-sheet__row-item', { hasText: 'update-current-lab' });
    await expect(
      row.getByText(
        /Advances main and its checked-out files in the original repository on .+ to origin\/main\./,
      ),
    ).toBeVisible();
    transcript.step(
      'the behind original-checkout row offered Update from origin with its impact copy',
    );

    // The action fast-forwards the branch AND the checkout that holds it.
    await row.getByRole('button', { name: 'Update from origin' }).click();
    await expect(
      row.getByText(
        new RegExp(
          `Updated main and its checked-out files to origin\\/main on .+ \\(now at ${originSha.slice(0, 7)}\\)\\.`,
        ),
      ),
    ).toBeVisible({ timeout: 60_000 });
    await evidenceShot(app, 'repository-source-update-original-checkout-advanced');
    transcript.step('the update advanced the branch and its checked-out files');

    // Real-git evidence: branch, symbolic HEAD, index, and working files all
    // advanced to the fetched origin tip, and the checkout stayed clean.
    expect(gitText(repo, 'rev-parse', 'refs/heads/main')).toBe(originSha);
    expect(gitText(repo, 'rev-parse', 'refs/remotes/origin/main')).toBe(originSha);
    expect(gitText(repo, 'symbolic-ref', '--quiet', 'HEAD')).toBe('refs/heads/main');
    expect(gitText(repo, 'rev-parse', 'HEAD')).toBe(originSha);
    expect(gitText(repo, 'status', '--porcelain')).toBe('');
    expect(fs.readFileSync(path.join(repo, 'README.md'), 'utf8')).toBe(
      '# update-current-lab (advanced by origin)\n',
    );
    expect(fs.readFileSync(path.join(repo, 'remote-notes.txt'), 'utf8')).toBe(
      'remote note one\nremote note two\n',
    );
    expect(gitText(repo, 'ls-files', '--stage', '--', 'README.md')).toContain(
      gitText(repo, 'rev-parse', `${originSha}:README.md`),
    );
    expect(gitText(repo, 'diff', '--stat', originSha)).toBe('');
    transcript.step(
      'branch, symbolic HEAD, index, and working files all advanced to the origin tip',
    );

    // The refreshed comparison confirms the source is now up to date.
    await expect(
      sheet.getByRole('checkbox', {
        name: /update-current-lab.*Source: main.*Origin: up to date with origin\/main/,
      }),
    ).toBeVisible({ timeout: 30_000 });

    // Creation accepts and persists the updated local SHA through the
    // exact-start setup contract, leaving the original checkout on main.
    await sheet.getByRole('button', { name: 'Next: Describe' }).click();
    await app.page.locator('#feature-name').fill('Original checkout update');
    await sheet.getByRole('button', { name: 'Next: Depth' }).click();
    await sheet.getByRole('button', { name: 'Next: Contract' }).click();
    await expect(
      sheet.getByText('update-current-lab: main is up to date with origin/main.'),
    ).toBeVisible();
    await expect(
      sheet.getByText(/Updated main and its checked-out files to origin\/main on /),
    ).toBeVisible();
    await sheet.getByRole('checkbox', { name: /Start immediately/ }).uncheck();
    await sheet.getByRole('button', { name: 'Create', exact: true }).click();

    const cockpit = app.page.getByLabel('Feature Original checkout update');
    await expect(cockpit).toBeVisible({ timeout: 30_000 });
    await expect(cockpit.getByText('Ready to start').first()).toBeVisible({ timeout: 60_000 });
    await waitFor(
      () => durableFeatureIds(world.stateDir).length === 1,
      'the original-checkout update feature id',
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
    const worktree = parseFeatureRepos(featureYaml)['update-current-lab']!;
    expect(storedCommits['update-current-lab']).toBe(originSha);
    expect(runYaml).toContain(`exact_sha: ${originSha}`);
    expect(gitText(worktree, 'rev-parse', 'HEAD')).toBe(originSha);
    // The original repository stays on main at the advanced tip, clean.
    expect(gitText(repo, 'branch', '--show-current')).toBe('main');
    expect(gitText(repo, 'rev-parse', 'HEAD')).toBe(originSha);
    expect(gitText(repo, 'status', '--porcelain')).toBe('');
    transcript.step('the feature started from the advanced tip with the original checkout intact');
  } finally {
    if (handle !== undefined) await closeApp(handle);
    assertNoLeakedProcesses(world);
    transcript.write(testInfo);
    destroyWorld(world);
  }
});

test('a dirty original checkout refuses the attempted update, preserves local bytes, and creation continues from the still-valid local commit', async ({}, testInfo) => {
  const world = createWorld('repository-source-update-dirty', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const { repo, bare } = pairOccupiedBehindOrigin(world, 'update-dirty-lab');
  const staleSha = gitText(repo, 'rev-parse', 'refs/heads/main');
  const originSha = gitText(bare, 'rev-parse', 'refs/heads/main');
  if (originSha === staleSha) {
    throw new Error('fixture did not place the original checkout behind origin');
  }

  const transcript = new Transcript(
    'repository-source-update-dirty',
    'Dirty original-checkout refusal and warning-based continuation (packaged app, real bundled server)',
  );
  let handle: AppHandle | undefined;
  try {
    handle = await launchApp(world, testInfo, { traceName: 'repository-source-update-dirty' });
    const app = handle;
    await app.page.getByRole('button', { name: 'New feature' }).click();
    const sheet = app.page.getByRole('dialog', { name: 'New feature' });
    await sheet.getByRole('checkbox', { name: /update-dirty-lab/ }).check();
    await sheet.getByRole('radio', { name: 'Current branches' }).click();
    await expect(sheet.getByRole('radio', { name: 'Current branches' })).toBeChecked();
    await expect(
      sheet.getByRole('checkbox', {
        name: /update-dirty-lab.*Source: main.*Origin: 2 commits behind origin\/main/,
      }),
    ).toBeVisible({ timeout: 30_000 });
    const row = sheet.locator('.creation-sheet__row-item', { hasText: 'update-dirty-lab' });
    await expect(
      row.getByText(
        /Advances main and its checked-out files in the original repository on .+ to origin\/main\./,
      ),
    ).toBeVisible();
    transcript.step('the clean comparison offered the original-checkout update');

    // The comparison was observed while the checkout was clean; the checkout
    // is dirtied externally (an unstaged tracked edit plus an untracked file)
    // before the action, so the server's execution-time safety revalidation
    // is what refuses it.
    fs.writeFileSync(path.join(repo, 'README.md'), '# update-dirty-lab (locally edited)\n');
    fs.writeFileSync(path.join(repo, 'untracked-dirty.txt'), 'untracked bytes\n');
    const dirtyStatus = gitText(repo, 'status', '--porcelain');

    await row.getByRole('button', { name: 'Update from origin' }).click();
    await expect(
      row.getByText(
        "main was not updated — the original repository's checkout has uncommitted or untracked files. Commit or stash them outside Agentico, then check again.",
      ),
    ).toBeVisible({ timeout: 60_000 });
    await evidenceShot(app, 'repository-source-update-dirty-refused');
    transcript.step('the attempt was refused for the dirty checkout');

    // Nothing mutated: the branch, HEAD, and both dirty files are preserved
    // byte-for-byte.
    expect(gitText(repo, 'rev-parse', 'refs/heads/main')).toBe(staleSha);
    expect(gitText(repo, 'rev-parse', 'HEAD')).toBe(staleSha);
    expect(gitText(repo, 'symbolic-ref', '--quiet', 'HEAD')).toBe('refs/heads/main');
    expect(gitText(repo, 'status', '--porcelain')).toBe(dirtyStatus);
    expect(fs.readFileSync(path.join(repo, 'README.md'), 'utf8')).toBe(
      '# update-dirty-lab (locally edited)\n',
    );
    expect(fs.readFileSync(path.join(repo, 'untracked-dirty.txt'), 'utf8')).toBe(
      'untracked bytes\n',
    );
    transcript.step('the refused attempt preserved the branch and every local byte');

    // The refusal's fresh comparison re-marks the row unsafe: the blocked
    // explanation replaces the action.
    await expect(
      row.getByText(
        'main is checked out in the original repository, whose checkout has uncommitted or untracked files — commit or stash them outside Agentico before updating.',
      ),
    ).toBeVisible();
    await expect(row.getByRole('button', { name: 'Update from origin' })).toHaveCount(0);

    // A fresh check keeps the unsafe state visible; nothing is retried.
    await row.getByRole('button', { name: 'Check again' }).click();
    await expect(
      sheet.getByRole('checkbox', {
        name: /update-dirty-lab.*Source: main.*Origin: 2 commits behind origin\/main/,
      }),
    ).toBeVisible({ timeout: 30_000 });
    await expect(
      row.getByText(
        'main is checked out in the original repository, whose checkout has uncommitted or untracked files — commit or stash them outside Agentico before updating.',
      ),
    ).toBeVisible();
    await expect(row.getByRole('button', { name: 'Update from origin' })).toHaveCount(0);
    transcript.step('the fresh check kept the blocked explanation and offered no update');

    // Warning-based continuation: the still-valid local source is accepted
    // without an acknowledgement, and the warning persists through review.
    await sheet.getByRole('button', { name: 'Next: Describe' }).click();
    await app.page.locator('#feature-name').fill('Dirty checkout continuation');
    await sheet.getByRole('button', { name: 'Next: Depth' }).click();
    await sheet.getByRole('button', { name: 'Next: Contract' }).click();
    await expect(
      sheet.getByText(
        'update-dirty-lab: main is 2 commits behind origin/main; the feature will start from the local source.',
      ),
    ).toBeVisible();
    await expect(
      sheet.getByText(
        "main was not updated — the original repository's checkout has uncommitted or untracked files. Commit or stash them outside Agentico, then check again.",
      ),
    ).toBeVisible();
    await sheet.getByRole('checkbox', { name: /Start immediately/ }).uncheck();
    await sheet.getByRole('button', { name: 'Create', exact: true }).click();

    const cockpit = app.page.getByLabel('Feature Dirty checkout continuation');
    await expect(cockpit).toBeVisible({ timeout: 30_000 });
    await expect(cockpit.getByText('Ready to start').first()).toBeVisible({ timeout: 60_000 });
    await waitFor(
      () => durableFeatureIds(world.stateDir).length === 1,
      'the dirty-checkout continuation feature id',
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
    const worktree = parseFeatureRepos(featureYaml)['update-dirty-lab']!;
    expect(storedCommits['update-dirty-lab']).toBe(staleSha);
    expect(runYaml).toContain(`exact_sha: ${staleSha}`);
    expect(gitText(worktree, 'rev-parse', 'HEAD')).toBe(staleSha);
    // The original repository is untouched by the refusal and the setup: still
    // on main at the stale tip with its dirty bytes preserved byte-for-byte.
    expect(gitText(repo, 'branch', '--show-current')).toBe('main');
    expect(gitText(repo, 'rev-parse', 'HEAD')).toBe(staleSha);
    expect(gitText(repo, 'status', '--porcelain')).toBe(dirtyStatus);
    expect(fs.readFileSync(path.join(repo, 'README.md'), 'utf8')).toBe(
      '# update-dirty-lab (locally edited)\n',
    );
    expect(fs.readFileSync(path.join(repo, 'untracked-dirty.txt'), 'utf8')).toBe(
      'untracked bytes\n',
    );
    transcript.step(
      'creation continued at the still-valid local commit with the dirty bytes preserved',
    );
  } finally {
    if (handle !== undefined) await closeApp(handle);
    assertNoLeakedProcesses(world);
    transcript.write(testInfo);
    destroyWorld(world);
  }
});

test('a failed origin check on the original checkout keeps warning-based continuation from the still-valid local commit', async ({}, testInfo) => {
  const world = createWorld('repository-source-update-dead-origin', {
    auth: { loggedIn: true, authMethod: 'oauth', email: 'e2e@example.invalid' },
    presetWorkspaceRoot: true,
  });
  const { repo, bare } = pairOccupiedBehindOrigin(world, 'update-unreachable-lab');
  const staleSha = gitText(repo, 'rev-parse', 'HEAD');
  const originSha = gitText(bare, 'rev-parse', 'refs/heads/main');
  if (originSha === staleSha) {
    throw new Error('fixture did not place the original checkout behind origin');
  }
  const deadPort = await pickDeadPort();

  const transcript = new Transcript(
    'repository-source-update-dead-origin',
    'Failed origin check on the original checkout (packaged app, real bundled server)',
  );
  let handle: AppHandle | undefined;
  try {
    handle = await launchApp(world, testInfo, {
      traceName: 'repository-source-update-dead-origin',
    });
    const app = handle;
    await app.page.getByRole('button', { name: 'New feature' }).click();
    const sheet = app.page.getByRole('dialog', { name: 'New feature' });
    await sheet.getByRole('checkbox', { name: /update-unreachable-lab/ }).check();
    await sheet.getByRole('radio', { name: 'Current branches' }).click();
    await expect(sheet.getByRole('radio', { name: 'Current branches' })).toBeChecked();
    await expect(
      sheet.getByRole('checkbox', {
        name: /update-unreachable-lab.*Source: main.*Origin: 2 commits behind origin\/main/,
      }),
    ).toBeVisible({ timeout: 30_000 });
    const row = sheet.locator('.creation-sheet__row-item', { hasText: 'update-unreachable-lab' });
    transcript.step('the original-checkout row reported the real behind comparison');

    // The origin becomes unreachable after the comparison: a fresh check
    // fails and keeps the earlier comparison as explicitly stale evidence.
    gitText(repo, 'remote', 'set-url', 'origin', `http://127.0.0.1:${deadPort}/dead.git`);
    await row.getByRole('button', { name: 'Check again' }).click();
    await expect(
      sheet.getByRole('checkbox', {
        name: /update-unreachable-lab.*Origin check unavailable — creation continues from the local source/,
      }),
    ).toBeVisible({ timeout: 30_000 });
    await expect(sheet.getByText(/Earlier comparison: 2 commits behind \(stale\)\./)).toBeVisible();
    await evidenceShot(app, 'repository-source-update-dead-origin-unavailable');
    transcript.step('the failed recheck kept the earlier comparison as stale evidence');

    // The warning persists through the review and creation continues from the
    // still-valid local commit without an acknowledgement.
    await sheet.getByRole('button', { name: 'Next: Describe' }).click();
    await app.page.locator('#feature-name').fill('Unreachable origin continuation');
    await sheet.getByRole('button', { name: 'Next: Depth' }).click();
    await sheet.getByRole('button', { name: 'Next: Contract' }).click();
    await expect(
      sheet.getByText(
        'update-unreachable-lab: the origin check could not complete; the feature will start from main. An earlier comparison (2 commits behind) is preserved but stale.',
      ),
    ).toBeVisible();
    await sheet.getByRole('checkbox', { name: /Start immediately/ }).uncheck();
    await sheet.getByRole('button', { name: 'Create', exact: true }).click();

    const cockpit = app.page.getByLabel('Feature Unreachable origin continuation');
    await expect(cockpit).toBeVisible({ timeout: 30_000 });
    await expect(cockpit.getByText('Ready to start').first()).toBeVisible({ timeout: 60_000 });
    await waitFor(
      () => durableFeatureIds(world.stateDir).length === 1,
      'the unreachable-origin continuation feature id',
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
    const worktree = parseFeatureRepos(featureYaml)['update-unreachable-lab']!;
    expect(storedCommits['update-unreachable-lab']).toBe(staleSha);
    expect(runYaml).toContain(`exact_sha: ${staleSha}`);
    expect(gitText(worktree, 'rev-parse', 'HEAD')).toBe(staleSha);
    // The original repository is untouched: still on main at the local tip,
    // clean, and the dead origin never mutated anything.
    expect(gitText(repo, 'branch', '--show-current')).toBe('main');
    expect(gitText(repo, 'rev-parse', 'HEAD')).toBe(staleSha);
    expect(gitText(repo, 'status', '--porcelain')).toBe('');
    transcript.step('creation continued at the still-valid local commit despite the dead origin');
  } finally {
    if (handle !== undefined) await closeApp(handle);
    assertNoLeakedProcesses(world);
    transcript.write(testInfo);
    destroyWorld(world);
  }
});
