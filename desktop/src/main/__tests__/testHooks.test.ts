import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import {
  resolveTestOutputFile,
  resolveTestPackagedResourcesDir,
  resolveTestUserDataDir,
  type TestPackagedResourcesDeps,
  type TestUserDataDeps,
} from '../testHooks';

const realDeps: TestUserDataDeps = {
  realpath: (candidate) => fs.realpathSync(candidate),
  tmpdir: () => os.tmpdir(),
  isAbsolute: (candidate) => path.isAbsolute(candidate),
  sep: path.sep,
};

const created: string[] = [];

function makeTempDir(): string {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'agentico-testhook-'));
  created.push(dir);
  return dir;
}

afterEach(() => {
  for (const dir of created.splice(0)) {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

describe('resolveTestUserDataDir', () => {
  it('accepts an existing directory inside the OS temp directory', () => {
    const dir = makeTempDir();
    expect(resolveTestUserDataDir(dir, realDeps)).toBe(fs.realpathSync(dir));
  });

  it('is inert when the override is absent or empty', () => {
    expect(resolveTestUserDataDir(undefined, realDeps)).toBeNull();
    expect(resolveTestUserDataDir('', realDeps)).toBeNull();
  });

  it('rejects relative paths', () => {
    expect(resolveTestUserDataDir('relative/dir', realDeps)).toBeNull();
  });

  it('rejects directories that do not exist yet (never creates them)', () => {
    expect(resolveTestUserDataDir(path.join(os.tmpdir(), 'agentico-nope-x9'), realDeps)).toBeNull();
  });

  it('rejects real user locations outside the temp directory', () => {
    expect(resolveTestUserDataDir(os.homedir(), realDeps)).toBeNull();
    expect(resolveTestUserDataDir('/', realDeps)).toBeNull();
  });

  it('rejects a symlink inside tmp that escapes to a foreign directory', () => {
    const dir = makeTempDir();
    const escape = path.join(dir, 'escape');
    fs.symlinkSync(os.homedir(), escape);
    expect(resolveTestUserDataDir(escape, realDeps)).toBeNull();
  });

  it('rejects prefix cousins of the temp directory (tmp2 is not inside tmp)', () => {
    const deps: TestUserDataDeps = {
      realpath: (candidate) => candidate,
      tmpdir: () => '/tmp',
      isAbsolute: (candidate) => candidate.startsWith('/'),
      sep: '/',
    };
    expect(resolveTestUserDataDir('/tmp2/evil', deps)).toBeNull();
    expect(resolveTestUserDataDir('/tmp/fine', deps)).toBe('/tmp/fine');
  });
});

describe('resolveTestPackagedResourcesDir', () => {
  const deps: TestPackagedResourcesDeps = {
    realpath: (candidate) => fs.realpathSync(candidate),
    isAbsolute: (candidate) => path.isAbsolute(candidate),
    exists: (candidate) => fs.existsSync(candidate),
    join: (...parts) => path.join(...parts),
  };

  it('accepts a package resources directory only during a temp-backed E2E launch', () => {
    const userData = makeTempDir();
    const resources = makeTempDir();
    fs.mkdirSync(path.join(resources, 'bin'));
    fs.writeFileSync(path.join(resources, 'app.asar'), '');
    fs.writeFileSync(path.join(resources, 'build-identity.json'), '{}');
    fs.writeFileSync(path.join(resources, 'bin', 'agentico'), '');

    expect(resolveTestPackagedResourcesDir(resources, userData, deps)).toBe(
      fs.realpathSync(resources),
    );
  });

  it('rejects missing resources and non-E2E launches', () => {
    const userData = makeTempDir();
    const resources = makeTempDir();
    expect(resolveTestPackagedResourcesDir(resources, userData, deps)).toBeNull();
    expect(resolveTestPackagedResourcesDir(resources, null, deps)).toBeNull();
    expect(resolveTestPackagedResourcesDir('relative', userData, deps)).toBeNull();
  });
});

describe('resolveTestOutputFile', () => {
  it('accepts files below the temp directory only during a temp-backed E2E launch', () => {
    const userData = makeTempDir();
    const outputDir = makeTempDir();
    const output = path.join(outputDir, 'ready.json');
    expect(
      resolveTestOutputFile(output, userData, {
        realpath: (candidate) => fs.realpathSync(candidate),
        dirname: (candidate) => path.dirname(candidate),
        tmpdir: () => os.tmpdir(),
        isAbsolute: (candidate) => path.isAbsolute(candidate),
        sep: path.sep,
      }),
    ).toBe(output);
  });

  it('rejects files outside temp, relative files, and non-E2E launches', () => {
    const userData = makeTempDir();
    const deps = {
      realpath: (candidate: string) => candidate,
      dirname: (candidate: string) => path.dirname(candidate),
      tmpdir: () => '/tmp',
      isAbsolute: (candidate: string) => candidate.startsWith('/'),
      sep: '/',
    };
    expect(resolveTestOutputFile('/Users/example/ready.json', userData, deps)).toBeNull();
    expect(resolveTestOutputFile('ready.json', userData, deps)).toBeNull();
    expect(resolveTestOutputFile('/tmp/ready.json', null, deps)).toBeNull();
  });
});
