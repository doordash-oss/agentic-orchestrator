import { describe, expect, it } from 'vitest';

import { RELEASE_SEQUENCE, runRelease, validateResumeEvidenceBoundary } from './release-run.mjs';

const evidence = {
  schema_version: 3,
  tag: 'v0.150.0',
  commit: 'a'.repeat(40),
  run_id: '11111111-1111-4111-8111-111111111111',
  workspace_root: '/release/workspace',
  operator_root: '/operator/checkout',
  evidence_path: '/operator/.git/agentico-release-preflight.json',
  workspace_token: {
    path: '/release/workspace',
    kind: 'directory',
    dev: 1,
    ino: 2,
  },
};

function fixture(overrides = {}) {
  const calls = [];
  return {
    calls,
    options: {
      operatorRoot: '/operator/checkout',
      preflight: () => evidence,
      command: (label, _command, _args, options) => calls.push({ label, ...options }),
      verifyProvenance: () => calls.push({ label: 'provenance' }),
      createSnapshot: () => ({ path: '/release/workspace/desktop/dist/publication' }),
      verifyPackages: () => {
        calls.push({ label: 'packages' });
        return { ok: true };
      },
      reserveTag: async () => calls.push({ label: 'reserve' }),
      publish: () => calls.push({ label: 'goreleaser' }),
      verifyManifest: () => {
        calls.push({ label: 'manifest' });
        return { ok: true };
      },
      verifySnapshot: () => {},
      verifyRemote: async () => calls.push({ label: 'remote' }),
      publishCask: () => calls.push({ label: 'cask' }),
      cleanup: () => calls.push({ label: 'cleanup' }),
      loadResume: () => null,
      saveResume: (_evidence, _snapshot, stage) => calls.push({ label: `save-${stage}` }),
      removeResume: () => calls.push({ label: 'remove-resume' }),
      ...overrides,
    },
  };
}

describe('release runner', () => {
  it('requires resume evidence at the operator repository evidence path', () => {
    expect(() =>
      validateResumeEvidenceBoundary(
        { ...evidence, evidence_path: '/attacker/replacement.json' },
        {
          expectedPath: evidence.evidence_path,
          readEvidence: () => evidence,
        },
      ),
    ).toThrow(/path.*does not match/);
    expect(
      validateResumeEvidenceBoundary(evidence, {
        expectedPath: evidence.evidence_path,
        readEvidence: () => evidence,
      }),
    ).toBe(evidence);
  });

  it('defines the one audited committed-source publication sequence', () => {
    expect(RELEASE_SEQUENCE).toEqual([
      'preflight',
      'npm-ci',
      'mac-package',
      'linux-packages',
      'package-gate',
      'publication-snapshot',
      'snapshot-gate',
      'provenance-recheck',
      'remote-tag-reservation',
      'goreleaser',
      'goreleaser-resume-record',
      'manifest-gate',
      'remote-byte-gate',
      'remote-resume-record',
      'desktop-cask',
      'cleanup',
    ]);
  });

  it('runs native and Linux builds in the detached workspace and reserves immediately before publish', async () => {
    const { calls, options } = fixture();
    await runRelease(options);
    expect(
      calls
        .filter(({ cwd }) => cwd !== undefined)
        .every(({ cwd }) => cwd === evidence.workspace_root),
    ).toBe(true);
    expect(calls.map(({ label }) => label)).toEqual([
      'npm-ci',
      'mac-package',
      'linux-packages',
      'packages',
      'packages',
      'provenance',
      'reserve',
      'goreleaser',
      'save-goreleaser-published',
      'manifest',
      'remote',
      'save-remote-verified',
      'cask',
      'cleanup',
      'remove-resume',
    ]);
  });

  it('strips release credentials from all build subprocesses but leaves npm registry auth', async () => {
    const observed = [];
    const { options } = fixture({
      ambientEnv: {
        GITHUB_TOKEN: 'github',
        GH_TOKEN: 'gh',
        AGENTICO_RELEASE_SIGNING_KEY: 'private',
        AGENTICO_RELEASE_SIGNING_KEY_FILE: '/secret/key',
        NPM_TOKEN: 'registry-required',
      },
      command: (label, _command, _args, commandOptions) => {
        if (['npm-ci', 'mac-package', 'linux-packages'].includes(label)) {
          observed.push(commandOptions.env);
        }
      },
    });
    await runRelease(options);
    for (const env of observed) {
      expect(env).toMatchObject({ NPM_TOKEN: 'registry-required', GOWORK: 'off' });
      expect(env).not.toHaveProperty('GITHUB_TOKEN');
      expect(env).not.toHaveProperty('GH_TOKEN');
      expect(env).not.toHaveProperty('AGENTICO_RELEASE_SIGNING_KEY');
      expect(env).not.toHaveProperty('AGENTICO_RELEASE_SIGNING_KEY_FILE');
      expect(env.AGENTICO_RELEASE_EVIDENCE_FILE).toMatch(/^\//);
    }
  });

  it('preserves post-GoReleaser state and resumes remote verification without rebuilding', async () => {
    let resumeState = null;
    let remoteAttempts = 0;
    const first = fixture({
      verifyRemote: async () => {
        remoteAttempts += 1;
        throw new Error('temporary network failure');
      },
      saveResume: (savedEvidence, savedSnapshot, stage) => {
        resumeState = { evidence: savedEvidence, snapshot: savedSnapshot, stage };
      },
      removeResume: () => {
        resumeState = null;
      },
    });
    await expect(runRelease(first.options)).rejects.toThrow(/temporary network failure/);
    expect(resumeState.stage).toBe('goreleaser-published');
    expect(first.calls.some(({ label }) => label === 'cleanup')).toBe(false);

    const resumedCalls = [];
    const second = fixture({
      loadResume: () => resumeState,
      validateResume: (state) => state,
      preflight: () => {
        throw new Error('must not preflight');
      },
      command: (label) => resumedCalls.push(label),
      publish: () => {
        throw new Error('must not republish');
      },
      verifyRemote: async () => {
        remoteAttempts += 1;
        resumedCalls.push('remote');
      },
      publishCask: () => resumedCalls.push('cask'),
      saveResume: (savedEvidence, savedSnapshot, stage) => {
        resumeState = { evidence: savedEvidence, snapshot: savedSnapshot, stage };
      },
      removeResume: () => {
        resumeState = null;
        resumedCalls.push('remove-resume');
      },
      cleanup: () => resumedCalls.push('cleanup'),
    });
    await runRelease(second.options);
    expect(resumedCalls).toEqual(['remote', 'cask', 'cleanup', 'remove-resume']);
    expect(remoteAttempts).toBe(2);
  });

  it('resumes a failed final cask without rebuilding, republishing, or rechecking remote bytes', async () => {
    let resumeState = null;
    const first = fixture({
      publishCask: () => {
        throw new Error('tap unavailable');
      },
      saveResume: (savedEvidence, savedSnapshot, stage) => {
        resumeState = { evidence: savedEvidence, snapshot: savedSnapshot, stage };
      },
      removeResume: () => {
        resumeState = null;
      },
    });
    await expect(runRelease(first.options)).rejects.toThrow(/tap unavailable/);
    expect(resumeState.stage).toBe('remote-verified');
    expect(first.calls.some(({ label }) => label === 'cleanup')).toBe(false);

    const calls = [];
    const { options } = fixture({
      loadResume: () => resumeState,
      validateResume: (state) => state,
      preflight: () => {
        throw new Error('must not preflight');
      },
      publish: () => {
        throw new Error('must not republish');
      },
      verifyRemote: async () => {
        throw new Error('must not recheck remote');
      },
      publishCask: () => calls.push('cask'),
      removeResume: () => {
        resumeState = null;
        calls.push('remove-resume');
      },
      cleanup: () => calls.push('cleanup'),
    });
    await runRelease(options);
    expect(calls).toEqual(['cask', 'cleanup', 'remove-resume']);
  });

  it('fails closed on tampered resume state without touching the workspace', async () => {
    let cleaned = false;
    const { options } = fixture({
      loadResume: () => ({ schema_version: 1, stage: 'goreleaser-published' }),
      validateResume: () => {
        throw new Error('release resume snapshot was tampered; inspect it before manual cleanup');
      },
      preflight: () => {
        throw new Error('must not preflight');
      },
      cleanup: () => {
        cleaned = true;
      },
    });
    await expect(runRelease(options)).rejects.toThrow(/tampered.*manual cleanup/);
    expect(cleaned).toBe(false);
  });

  it('removes the exact detached workspace on a build failure', async () => {
    const { calls, options } = fixture({
      command: (label) => {
        calls.push({ label });
        if (label === 'mac-package') throw new Error('package failed');
      },
    });
    await expect(runRelease(options)).rejects.toThrow('package failed');
    expect(calls.at(-1)).toEqual({ label: 'cleanup' });
    expect(calls.some(({ label }) => label === 'reserve')).toBe(false);
  });
});
