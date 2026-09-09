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
import path from 'node:path';
import { expect, test } from '@playwright/test';
import { closeApp, evidenceShot, launchApp, type AppHandle } from '../helpers/app';
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
