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

import { describe, expect, it } from 'vitest';

import { RELEASE_SEQUENCE, runRelease, validateResumeEvidenceBoundary } from './release-run.mjs';

const evidence = {
  schema_version: 4,
  tag: 'v0.150.0',
  commit: 'a'.repeat(40),
  run_id: '11111111-1111-4111-8111-111111111111',
  workspace_root: '/release/workspace',
  operator_root: '/operator/checkout',
  evidence_path: '/operator/.git/agentico-release-preflight.json',
  evidence_sha256: 'd'.repeat(64),
  workspace_token: {
    path: '/release/workspace',
    kind: 'directory',
    dev: 1,
    ino: 2,
  },
  cleanup_helper: { path: '/git/cleanup-helper', sha256: 'c'.repeat(64), size: 123 },
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
      prepareDesktopManifest: () => calls.push({ label: 'desktop-manifest-create' }),
      verifyDesktopManifest: () => calls.push({ label: 'desktop-manifest-verify' }),
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
      revalidateWorkspace: () => {},
      prepareBuildHome: () => {},
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
      'desktop-manifest-sign',
      'desktop-manifest-gate',
      'publication-snapshot',
      'snapshot-gate',
      'provenance-recheck',
      'tag-reservation-start-record',
      'remote-tag-reservation',
      'goreleaser-start-record',
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
      'desktop-manifest-create',
      'desktop-manifest-verify',
      'packages',
      'desktop-manifest-verify',
      'provenance',
      'save-tag-reservation-started',
      'reserve',
      'save-goreleaser-started',
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
          observed.push({ label, env: commandOptions.env });
        }
      },
    });
    await runRelease(options);
    for (const { label, env } of observed) {
      if (label !== 'linux-packages') {
        expect(env).toMatchObject({ NPM_TOKEN: 'registry-required', GOWORK: 'off' });
      }
      expect(env).not.toHaveProperty('GITHUB_TOKEN');
      expect(env).not.toHaveProperty('GH_TOKEN');
      expect(env).not.toHaveProperty('AGENTICO_RELEASE_SIGNING_KEY');
      expect(env).not.toHaveProperty('AGENTICO_RELEASE_SIGNING_KEY_FILE');
      expect(env.AGENTICO_RELEASE_EVIDENCE_FILE).toMatch(/^\//);
      expect(env.AGENTICO_RELEASE_EVIDENCE_SHA256).toBe(evidence.evidence_sha256);
    }
  });

  it('resolves release notes from the operator root and passes the path explicitly to publication', async () => {
    let publication;
    const { options } = fixture({
      ambientEnv: { AGENTICO_RELEASE_NOTES_FILE: 'notes/release.md' },
      publish: (input) => {
        publication = input;
      },
    });
    await runRelease(options);
    expect(publication.notesFile).toBe('/operator/checkout/notes/release.md');
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

  it('resumes a transient tag-reservation failure without rebuilding and invokes GoReleaser once', async () => {
    let resumeState;
    let reservationAttempts = 0;
    let publications = 0;
    const first = fixture({
      reserveTag: async () => {
        reservationAttempts += 1;
        throw new Error('temporary reservation network failure');
      },
      saveResume: (savedEvidence, savedSnapshot, stage) => {
        resumeState = { evidence: savedEvidence, snapshot: savedSnapshot, stage };
      },
    });
    await expect(runRelease(first.options)).rejects.toThrow(/reservation network failure/);
    expect(resumeState.stage).toBe('tag-reservation-started');
    expect(first.calls.some(({ label }) => label === 'cleanup')).toBe(false);

    const resumedEvents = [];
    const second = fixture({
      loadResume: () => resumeState,
      validateResume: (state) => state,
      preflight: () => {
        throw new Error('must not preflight or rebuild');
      },
      command: () => {
        throw new Error('must not rebuild');
      },
      reserveTag: async () => {
        reservationAttempts += 1;
        resumedEvents.push('reserve');
      },
      publish: () => {
        publications += 1;
        resumedEvents.push('goreleaser');
      },
      saveResume: (savedEvidence, savedSnapshot, stage) => {
        resumeState = { evidence: savedEvidence, snapshot: savedSnapshot, stage };
        resumedEvents.push(`save-${stage}`);
      },
      verifyManifest: () => {
        resumedEvents.push('manifest');
        return { ok: true };
      },
      verifyRemote: async () => resumedEvents.push('remote'),
      publishCask: () => resumedEvents.push('cask'),
      cleanup: () => resumedEvents.push('cleanup'),
      removeResume: () => resumedEvents.push('remove-resume'),
    });
    await runRelease(second.options);
    expect(reservationAttempts).toBe(2);
    expect(publications).toBe(1);
    expect(resumedEvents).toEqual([
      'reserve',
      'save-goreleaser-started',
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

  it('fails a resumed tag reservation when the remote tag belongs to another commit', async () => {
    const resumeState = {
      evidence,
      snapshot: { path: '/snapshot' },
      stage: 'tag-reservation-started',
    };
    let published = false;
    const { options } = fixture({
      loadResume: () => resumeState,
      validateResume: (state) => state,
      reserveTag: async () => {
        throw new Error(`remote tag belongs to ${'b'.repeat(40)}`);
      },
      publish: () => {
        published = true;
      },
    });
    await expect(runRelease(options)).rejects.toThrow(/remote tag belongs/);
    expect(published).toBe(false);
    expect(resumeState.stage).toBe('tag-reservation-started');
  });

  it('checkpoints each remote boundary and preserves evidence when publication throws', async () => {
    let resumeState;
    const { calls, options } = fixture({
      publish: () => {
        calls.push({ label: 'goreleaser' });
        throw new Error('publication outcome unknown');
      },
      saveResume: (savedEvidence, savedSnapshot, stage) => {
        resumeState = { evidence: savedEvidence, snapshot: savedSnapshot, stage };
        calls.push({ label: `save-${stage}` });
      },
    });
    await expect(runRelease(options)).rejects.toThrow(/outcome unknown/);
    expect(resumeState.stage).toBe('goreleaser-started');
    expect(calls.map(({ label }) => label)).toContain('save-tag-reservation-started');
    expect(calls.map(({ label }) => label)).toContain('save-goreleaser-started');
    expect(calls.findIndex(({ label }) => label === 'save-tag-reservation-started')).toBeLessThan(
      calls.findIndex(({ label }) => label === 'reserve'),
    );
    expect(calls.findIndex(({ label }) => label === 'reserve')).toBeLessThan(
      calls.findIndex(({ label }) => label === 'save-goreleaser-started'),
    );
    expect(calls.findIndex(({ label }) => label === 'save-goreleaser-started')).toBeLessThan(
      calls.findIndex(({ label }) => label === 'goreleaser'),
    );
    expect(calls.some(({ label }) => label === 'cleanup')).toBe(false);
  });

  it('revalidates the workspace inode immediately before every build and remote boundary', async () => {
    const events = [];
    const { options } = fixture({
      revalidateWorkspace: () => events.push('revalidate'),
      command: (label) => events.push(label),
      createSnapshot: () => {
        events.push('snapshot');
        return { path: '/snapshot' };
      },
      reserveTag: async () => events.push('reserve'),
      publish: () => events.push('publish'),
      publishCask: () => events.push('cask'),
    });
    await runRelease(options);
    for (const boundary of [
      'npm-ci',
      'mac-package',
      'linux-packages',
      'snapshot',
      'reserve',
      'publish',
    ]) {
      const index = events.indexOf(boundary);
      expect(events[index - 1], boundary).toBe('revalidate');
    }
  });

  it('fails closed instead of blindly republishing when a started publication is not complete', async () => {
    const { options } = fixture({
      loadResume: () => ({
        evidence,
        snapshot: { path: '/snapshot' },
        stage: 'goreleaser-started',
      }),
      validateResume: (state) => state,
      verifyRemote: async () => {
        throw new Error('remote release is absent');
      },
      publish: () => {
        throw new Error('must not republish');
      },
    });
    await expect(runRelease(options)).rejects.toThrow(/publication outcome is uncertain.*absent/);
    expect(options.loadResume().stage).toBe('goreleaser-started');
  });

  it('resumes a failed final cask without rebuilding or republishing and rechecks remote bytes', async () => {
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
      verifyRemote: async () => calls.push('remote'),
      publishCask: () => calls.push('cask'),
      removeResume: () => {
        resumeState = null;
        calls.push('remove-resume');
      },
      cleanup: () => calls.push('cleanup'),
    });
    await runRelease(options);
    expect(calls).toEqual(['remote', 'cask', 'cleanup', 'remove-resume']);
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
