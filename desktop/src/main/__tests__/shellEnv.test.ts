// Login-shell PATH resolution: GUI launches must gain the shell's PATH
// entries (append-only, so launch-env precedence survives), and every failure
// mode must leave the environment untouched rather than throw during startup.
import { describe, expect, it } from 'vitest';

import { applyLoginShellPath, mergePathLists, parsePathFromEnvOutput } from '../shellEnv';

describe('parsePathFromEnvOutput', () => {
  it('extracts a line-anchored PATH from noisy profile output', () => {
    const output = [
      'Welcome banner from .zshrc',
      'NOT_PATH=PATH=/decoy',
      'PATH=/opt/homebrew/bin:/usr/bin',
      'HOME=/Users/me',
    ].join('\n');
    expect(parsePathFromEnvOutput(output)).toBe('/opt/homebrew/bin:/usr/bin');
  });

  it('returns null when no PATH line exists or it is empty', () => {
    expect(parsePathFromEnvOutput('HOME=/Users/me')).toBeNull();
    expect(parsePathFromEnvOutput('PATH=\nHOME=/x')).toBeNull();
  });
});

describe('mergePathLists', () => {
  it('appends only missing entries, preserving launch-env precedence', () => {
    expect(mergePathLists('/stub/bin:/usr/bin', '/opt/homebrew/bin:/usr/bin')).toBe(
      '/stub/bin:/usr/bin:/opt/homebrew/bin',
    );
  });

  it('handles an unset current PATH and drops empty entries', () => {
    expect(mergePathLists(undefined, '/a::/b')).toBe('/a:/b');
  });
});

describe('applyLoginShellPath', () => {
  const shellOutput = (path: string) => Promise.resolve(`HOME=/Users/me\nPATH=${path}\n`);

  it('appends login-shell entries to env.PATH', async () => {
    const env: NodeJS.ProcessEnv = { PATH: '/usr/bin:/bin', SHELL: '/bin/zsh' };
    const calls: Array<{ shell: string; args: readonly string[] }> = [];
    const outcome = await applyLoginShellPath({
      env,
      platform: 'darwin',
      runShell: (shell, args) => {
        calls.push({ shell, args });
        return shellOutput('/opt/homebrew/bin:/usr/bin:/Users/me/.local/bin');
      },
    });
    expect(outcome).toEqual({
      applied: true,
      added: ['/opt/homebrew/bin', '/Users/me/.local/bin'],
    });
    expect(env['PATH']).toBe('/usr/bin:/bin:/opt/homebrew/bin:/Users/me/.local/bin');
    expect(calls).toEqual([{ shell: '/bin/zsh', args: ['-l', '-i', '-c', '/usr/bin/env'] }]);
  });

  it('falls back to a platform default shell when SHELL is unset', async () => {
    const env: NodeJS.ProcessEnv = { PATH: '/usr/bin' };
    const shells: string[] = [];
    await applyLoginShellPath({
      env,
      platform: 'linux',
      runShell: (shell) => {
        shells.push(shell);
        return shellOutput('/usr/bin');
      },
    });
    expect(shells).toEqual(['/bin/bash']);
  });

  it('reports and leaves env alone when the shell fails or hangs', async () => {
    const env: NodeJS.ProcessEnv = { PATH: '/usr/bin', SHELL: '/bin/zsh' };
    const outcome = await applyLoginShellPath({
      env,
      platform: 'darwin',
      runShell: () => Promise.reject(new Error('timed out')),
    });
    expect(outcome.applied).toBe(false);
    expect(outcome.reason).toContain('/bin/zsh failed: timed out');
    expect(env['PATH']).toBe('/usr/bin');
  });

  it('reports when the shell output carries no PATH', async () => {
    const env: NodeJS.ProcessEnv = { PATH: '/usr/bin', SHELL: '/bin/zsh' };
    const outcome = await applyLoginShellPath({
      env,
      platform: 'darwin',
      runShell: () => Promise.resolve('HOME=/Users/me\n'),
    });
    expect(outcome).toMatchObject({ applied: false, reason: '/bin/zsh produced no PATH' });
    expect(env['PATH']).toBe('/usr/bin');
  });

  it('is a no-op when the login shell adds nothing new', async () => {
    const env: NodeJS.ProcessEnv = { PATH: '/opt/homebrew/bin:/usr/bin', SHELL: '/bin/zsh' };
    const outcome = await applyLoginShellPath({
      env,
      platform: 'darwin',
      runShell: () => shellOutput('/usr/bin:/opt/homebrew/bin'),
    });
    expect(outcome).toMatchObject({ applied: false, reason: 'login shell added no new entries' });
    expect(env['PATH']).toBe('/opt/homebrew/bin:/usr/bin');
  });

  it('respects the AGENTICO_DISABLE_SHELL_ENV escape hatch', async () => {
    const env: NodeJS.ProcessEnv = {
      PATH: '/usr/bin',
      SHELL: '/bin/zsh',
      AGENTICO_DISABLE_SHELL_ENV: '1',
    };
    const outcome = await applyLoginShellPath({
      env,
      platform: 'darwin',
      runShell: () => {
        throw new Error('must not be invoked');
      },
    });
    expect(outcome).toMatchObject({ applied: false });
    expect(env['PATH']).toBe('/usr/bin');
  });

  it('skips unsupported platforms', async () => {
    const env: NodeJS.ProcessEnv = { PATH: 'C:\\Windows' };
    const outcome = await applyLoginShellPath({ env, platform: 'win32' });
    expect(outcome).toMatchObject({ applied: false });
    expect(env['PATH']).toBe('C:\\Windows');
  });
});
