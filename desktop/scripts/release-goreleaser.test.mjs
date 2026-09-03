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
import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

import { goreleaserArguments, runGoreleaserRelease } from './release-goreleaser.mjs';

describe('GoReleaser release wrapper', () => {
  it('uses only the fixed release command when no notes file is supplied', () => {
    expect(goreleaserArguments()).toEqual(['release', '--clean']);
  });

  it('passes a notes file as one argument rather than accepting arbitrary flags', () => {
    expect(goreleaserArguments('/tmp/release notes.md')).toEqual([
      'release',
      '--clean',
      '--release-notes',
      '/tmp/release notes.md',
    ]);
  });

  it('rejects an absent notes file before it invokes GoReleaser', () => {
    expect(() =>
      runGoreleaserRelease({
        notesFile: '/does/not/exist.md',
        isFile: () => false,
        execute: () => {
          throw new Error('must not execute');
        },
      }),
    ).toThrow(/release notes file does not exist/);
  });

  it('invokes GoReleaser with no ambient arbitrary flag channel', () => {
    const calls = [];
    runGoreleaserRelease({
      notesFile: '/tmp/notes.md',
      isFile: () => true,
      evidence: { workspace_root: '/release/workspace' },
      verifyEvidence: (evidence) => evidence,
      verifySnapshot: () => {},
      execute: (command, args, options) => calls.push({ command, args, options }),
    });
    expect(calls).toEqual([
      {
        command: 'goreleaser',
        args: ['release', '--clean', '--release-notes', '/tmp/notes.md'],
        options: expect.objectContaining({
          cwd: '/release/workspace',
          env: expect.objectContaining({ GOWORK: 'off', GOFLAGS: '-mod=readonly' }),
        }),
      },
    ]);
  });

  it('rehashes the immutable publication snapshot before and after GoReleaser', () => {
    const phases = [];
    runGoreleaserRelease({
      evidence: { workspace_root: '/release/workspace' },
      verifyEvidence: (evidence) => evidence,
      verifySnapshot: () => phases.push('hash'),
      execute: () => phases.push('publish'),
    });
    expect(phases).toEqual(['hash', 'publish', 'hash']);
  });

  it('scrubs ambient GoReleaser identity/config overrides and pins only the evidence tag', () => {
    let invoked;
    runGoreleaserRelease({
      evidence: { workspace_root: '/release/workspace', tag: 'v0.150.0' },
      verifyEvidence: (value) => value,
      verifySnapshot: () => {},
      env: {
        GITHUB_TOKEN: 'publish-token',
        GORELEASER_CURRENT_TAG: 'v9.9.9',
        GORELEASER_PREVIOUS_TAG: 'v9.9.8',
        GORELEASER_CONFIG: '/tmp/evil.yaml',
        GORELEASER_GIT_COMMIT: 'b'.repeat(40),
      },
      execute: (_command, _args, options) => {
        invoked = options;
      },
    });
    expect(invoked.env).toMatchObject({
      GITHUB_TOKEN: 'publish-token',
      GORELEASER_CURRENT_TAG: 'v0.150.0',
      GOWORK: 'off',
      GOFLAGS: '-mod=readonly',
    });
    expect(Object.keys(invoked.env).filter((name) => name.startsWith('GORELEASER_'))).toEqual([
      'GORELEASER_CURRENT_TAG',
    ]);
  });

  it('passes the evidence tag to a real probe subprocess instead of ambient identity', () => {
    const root = mkdtempSync(join(tmpdir(), 'agentico-goreleaser-probe-'));
    const probe = join(root, 'probe.mjs');
    const output = join(root, 'environment.json');
    writeFileSync(
      probe,
      "import { writeFileSync } from 'node:fs'; writeFileSync(process.argv[2], JSON.stringify(process.env));\n",
    );
    try {
      runGoreleaserRelease({
        evidence: { workspace_root: root, tag: 'v0.150.0' },
        verifyEvidence: (value) => value,
        verifySnapshot: () => {},
        env: {
          GORELEASER_CURRENT_TAG: 'v9.9.9',
          GORELEASER_CONFIG: '/tmp/evil.yaml',
          GORELEASER_GIT_COMMIT: 'b'.repeat(40),
        },
        execute: (_command, _args, options) =>
          execFileSync(process.execPath, [probe, output], options),
      });
      const observed = JSON.parse(readFileSync(output, 'utf8'));
      expect(observed.GORELEASER_CURRENT_TAG).toBe('v0.150.0');
      expect(observed).not.toHaveProperty('GORELEASER_CONFIG');
      expect(observed).not.toHaveProperty('GORELEASER_GIT_COMMIT');
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it('does not infer release notes from ambient process state in a real subprocess', () => {
    const root = mkdtempSync(join(tmpdir(), 'agentico-goreleaser-notes-probe-'));
    const probe = join(root, 'probe.mjs');
    const output = join(root, 'arguments.json');
    const moduleUrl = new URL('./release-goreleaser.mjs', import.meta.url).href;
    writeFileSync(
      probe,
      `import { writeFileSync } from 'node:fs';
import { runGoreleaserRelease } from ${JSON.stringify(moduleUrl)};
const args = runGoreleaserRelease({
  evidence: { workspace_root: process.cwd(), tag: 'v0.150.0' },
  verifyEvidence: (value) => value,
  verifySnapshot: () => {},
  execute: () => {},
});
writeFileSync(process.argv[2], JSON.stringify(args));
`,
    );
    try {
      execFileSync(process.execPath, [probe, output], {
        cwd: root,
        env: { ...process.env, AGENTICO_RELEASE_NOTES_FILE: '/ambient/poison.md' },
      });
      expect(JSON.parse(readFileSync(output, 'utf8'))).toEqual(['release', '--clean']);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });
});
