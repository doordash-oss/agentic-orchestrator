import { execFileSync, spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  chmodSync,
  copyFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  renameSync,
  rmSync,
  statSync,
  symlinkSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { afterEach, describe, expect, it } from 'vitest';

import {
  cleanGoEnvironment,
  createDetachedReleaseWorkspace,
  createPublicationSnapshot,
  removeDetachedReleaseWorkspace,
  verifyPublicationSnapshot,
} from './release-workspace.mjs';

const roots = [];
const projectRoot = fileURLToPath(new URL('../..', import.meta.url));

afterEach(() => {
  for (const root of roots.splice(0)) rmSync(root, { recursive: true, force: true });
});

function git(cwd, ...args) {
  return execFileSync('git', args, { cwd, encoding: 'utf8' }).trim();
}

function repositoryFixture() {
  const root = mkdtempSync(join(tmpdir(), 'agentico-release-workspace-test-'));
  roots.push(root);
  git(root, 'init');
  git(root, 'config', 'user.email', 'test@example.com');
  git(root, 'config', 'user.name', 'Test');
  writeFileSync(join(root, '.gitignore'), 'go.work\nskills/.env\nnode_modules\n');
  writeFileSync(join(root, 'tracked.txt'), 'committed\n');
  mkdirSync(join(root, 'skills'));
  writeFileSync(join(root, 'skills', '.env'), 'SECRET=ambient\n');
  writeFileSync(join(root, 'go.work'), 'go 1.25\n');
  git(root, 'add', '.gitignore', 'tracked.txt');
  git(root, 'commit', '-m', 'fixture');
  return { root, commit: git(root, 'rev-parse', 'HEAD') };
}

describe('detached release workspace', () => {
  it('contains only the captured committed source and excludes ambient ignored inputs', () => {
    const fixture = repositoryFixture();
    const workspace = createDetachedReleaseWorkspace({
      operatorRoot: fixture.root,
      commit: fixture.commit,
      runId: '11111111-1111-4111-8111-111111111111',
    });
    try {
      expect(readFileSync(join(workspace.path, 'tracked.txt'), 'utf8')).toBe('committed\n');
      expect(existsSync(join(workspace.path, 'go.work'))).toBe(false);
      expect(existsSync(join(workspace.path, 'skills', '.env'))).toBe(false);
      expect(git(workspace.path, 'rev-parse', 'HEAD')).toBe(fixture.commit);
      expect(git(workspace.path, 'status', '--porcelain', '--untracked-files=all')).toBe('');
      const readonlySnapshot = join(workspace.path, 'desktop', 'dist', 'publication');
      mkdirSync(readonlySnapshot, { recursive: true });
      writeFileSync(join(readonlySnapshot, 'artifact'), 'bytes');
      chmodSync(readonlySnapshot, 0o500);
    } finally {
      removeDetachedReleaseWorkspace(workspace);
    }
    expect(existsSync(workspace.path)).toBe(false);
    expect(git(fixture.root, 'worktree', 'list', '--porcelain')).not.toContain(workspace.path);
  }, 20_000);

  it('clears ambient Go workspace and flags while forcing readonly modules', () => {
    expect(
      cleanGoEnvironment({
        GOWORK: '/tmp/poison/go.work',
        GOFLAGS: '-overlay=/tmp/poison.json',
        X: 'ok',
      }),
    ).toMatchObject({ GOWORK: 'off', GOFLAGS: '-mod=readonly', X: 'ok' });
  });

  it('refuses cleanup after workspace replacement without touching a symlink victim', () => {
    const fixture = repositoryFixture();
    const workspace = createDetachedReleaseWorkspace({
      operatorRoot: fixture.root,
      commit: fixture.commit,
      runId: '22222222-2222-4222-8222-222222222222',
    });
    git(fixture.root, 'worktree', 'remove', '--force', workspace.path);
    const victim = join(fixture.root, 'victim');
    mkdirSync(victim);
    writeFileSync(join(victim, 'keep'), 'unchanged');
    chmodSync(victim, 0o755);
    symlinkSync(victim, workspace.path);
    expect(() => removeDetachedReleaseWorkspace(workspace)).toThrow(/changed after validation/);
    expect(readFileSync(join(victim, 'keep'), 'utf8')).toBe('unchanged');
    expect(statSync(victim).mode & 0o777).toBe(0o755);
  });

  it('does not chmod a symlink substituted for the read-only publication directory', () => {
    const fixture = repositoryFixture();
    const workspace = createDetachedReleaseWorkspace({
      operatorRoot: fixture.root,
      commit: fixture.commit,
      runId: '33333333-3333-4333-8333-333333333333',
    });
    const victim = join(fixture.root, 'publication-victim');
    mkdirSync(victim);
    chmodSync(victim, 0o755);
    mkdirSync(join(workspace.path, 'desktop', 'dist'), { recursive: true });
    symlinkSync(victim, join(workspace.path, 'desktop', 'dist', 'publication'));
    expect(() => removeDetachedReleaseWorkspace(workspace)).not.toThrow();
    expect(statSync(victim).mode & 0o777).toBe(0o755);
    expect(existsSync(workspace.path)).toBe(false);
  });

  it.each(['desktop', 'dist'])(
    'does not follow a symlink at the %s component while preparing publication cleanup',
    (component) => {
      const fixture = repositoryFixture();
      const workspace = createDetachedReleaseWorkspace({
        operatorRoot: fixture.root,
        commit: fixture.commit,
        runId:
          component === 'desktop'
            ? '44444444-4444-4444-8444-444444444444'
            : '66666666-6666-4666-8666-666666666666',
      });
      const victim = join(fixture.root, `${component}-victim`);
      const victimPublication =
        component === 'desktop' ? join(victim, 'dist', 'publication') : join(victim, 'publication');
      mkdirSync(victimPublication, { recursive: true });
      chmodSync(victimPublication, 0o755);
      if (component === 'desktop') {
        symlinkSync(victim, join(workspace.path, 'desktop'));
      } else {
        mkdirSync(join(workspace.path, 'desktop'));
        symlinkSync(victim, join(workspace.path, 'desktop', 'dist'));
      }
      expect(() => removeDetachedReleaseWorkspace(workspace)).not.toThrow();
      expect(statSync(victimPublication).mode & 0o777).toBe(0o755);
      expect(existsSync(victim)).toBe(true);
      expect(existsSync(workspace.path)).toBe(false);
    },
  );

  it('uses Git only for non-destructive administrative pruning after inode-bound cleanup', () => {
    const fixture = repositoryFixture();
    const workspace = createDetachedReleaseWorkspace({
      operatorRoot: fixture.root,
      commit: fixture.commit,
      runId: '55555555-5555-4555-8555-555555555555',
    });
    const gitCalls = [];
    let cleaned;
    removeDetachedReleaseWorkspace(workspace, {
      cleanupCommand: (value) => {
        cleaned = value;
      },
      gitCommand: (_cwd, ...args) => {
        gitCalls.push(args);
        return '';
      },
    });
    expect(cleaned.token).toEqual(workspace.token);
    expect(gitCalls).toEqual([['worktree', 'prune', '--expire', 'now']]);
    expect(gitCalls.flat()).not.toContain(workspace.path);
  });

  it('never traverses a real-directory root replacement carrying the copied worktree pointer', async () => {
    const fixture = repositoryFixture();
    const workspace = createDetachedReleaseWorkspace({
      operatorRoot: fixture.root,
      commit: fixture.commit,
      runId: '77777777-7777-4777-8777-777777777777',
    });
    const child = spawn(
      'go',
      [
        'run',
        './desktop/scripts/release-cleanup',
        workspace.path,
        String(workspace.token.dev),
        String(workspace.token.ino),
      ],
      { cwd: projectRoot, stdio: ['pipe', 'pipe', 'pipe'] },
    );
    await new Promise((resolve, reject) => {
      let output = '';
      child.stdout.on('data', (chunk) => {
        output += chunk;
        if (output.includes('ready\n')) resolve();
      });
      child.once('error', reject);
      child.once('exit', (code) =>
        reject(new Error(`cleanup helper exited before ready: ${code}`)),
      );
    });
    const original = `${workspace.path}.validated-original`;
    renameSync(workspace.path, original);
    mkdirSync(workspace.path);
    const worktreePointer = readFileSync(join(original, '.git'), 'utf8');
    copyFileSync(join(original, '.git'), join(workspace.path, '.git'));
    writeFileSync(join(workspace.path, 'keep'), 'victim bytes');
    chmodSync(workspace.path, 0o755);
    const exit = new Promise((resolve) => {
      let stderr = '';
      child.stderr.on('data', (chunk) => {
        stderr += chunk;
      });
      child.once('exit', (code) => resolve({ code, stderr }));
    });
    child.stdin.end();
    const { code, stderr } = await exit;
    expect(code).not.toBe(0);
    expect(stderr).toMatch(/workspace root changed|not empty/);
    expect(readFileSync(join(workspace.path, 'keep'), 'utf8')).toBe('victim bytes');
    expect(readFileSync(join(workspace.path, '.git'), 'utf8')).toBe(worktreePointer);
    expect(statSync(workspace.path).mode & 0o777).toBe(0o755);
    expect(existsSync(original)).toBe(false);
  }, 30_000);
});

describe('publication snapshot', () => {
  it('copies receipt-bound artifacts, rewrites canonical paths, and detects later byte changes', () => {
    const root = mkdtempSync(join(tmpdir(), 'agentico-publication-test-'));
    roots.push(root);
    const source = join(root, 'source');
    const workspace = join(root, 'workspace');
    mkdirSync(source);
    mkdirSync(workspace);
    const names = ['Agentico-mac-universal.dmg'];
    writeFileSync(join(source, names[0]), 'dmg bytes');
    writeFileSync(
      join(source, 'package-verification-darwin-universal.json'),
      `${JSON.stringify({
        schema_version: 2,
        target: { os: 'darwin', arch: 'universal' },
        artifacts: [
          {
            target: { os: 'darwin', arch: 'universal' },
            format: 'dmg',
            path: join(source, names[0]),
            sha256: createHash('sha256').update('dmg bytes').digest('hex'),
            size: 9,
            identity: {},
          },
        ],
      })}\n`,
    );

    const snapshot = createPublicationSnapshot({
      workspaceRoot: workspace,
      sourceDir: source,
      artifactNames: names,
      receiptNames: ['package-verification-darwin-universal.json'],
    });
    expect(snapshot.artifacts[0].path).toBe(join(snapshot.path, names[0]));
    const receipt = JSON.parse(
      readFileSync(join(snapshot.path, 'package-verification-darwin-universal.json'), 'utf8'),
    );
    expect(receipt.artifacts[0].path).toBe(join(snapshot.path, names[0]));
    expect(receipt.artifacts[0].sha256).toBe(snapshot.artifacts[0].sha256);
    expect(statSync(join(snapshot.path, names[0])).mode & 0o222).toBe(0);
    expect(() => verifyPublicationSnapshot(snapshot)).not.toThrow();
    chmodSync(join(snapshot.path, names[0]), 0o600);
    writeFileSync(join(snapshot.path, names[0]), 'changed');
    expect(() => verifyPublicationSnapshot(snapshot)).toThrow(/changed after snapshot/);
    chmodSync(snapshot.path, 0o700);
  });

  it('refuses to bless artifact bytes that no longer match the verified receipt', () => {
    const root = mkdtempSync(join(tmpdir(), 'agentico-publication-stale-test-'));
    roots.push(root);
    const source = join(root, 'source');
    const workspace = join(root, 'workspace');
    mkdirSync(source);
    mkdirSync(workspace);
    writeFileSync(join(source, 'Agentico-mac-universal.dmg'), 'changed bytes');
    writeFileSync(
      join(source, 'package-verification-darwin-universal.json'),
      `${JSON.stringify({
        schema_version: 2,
        artifacts: [
          {
            format: 'dmg',
            path: join(source, 'Agentico-mac-universal.dmg'),
            sha256: createHash('sha256').update('original bytes').digest('hex'),
            size: 14,
          },
        ],
      })}\n`,
    );
    expect(() =>
      createPublicationSnapshot({
        workspaceRoot: workspace,
        sourceDir: source,
        artifactNames: ['Agentico-mac-universal.dmg'],
        receiptNames: ['package-verification-darwin-universal.json'],
      }),
    ).toThrow(/receipt digest does not match staged artifact/);
  });
});
