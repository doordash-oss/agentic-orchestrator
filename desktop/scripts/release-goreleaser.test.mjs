import { describe, expect, it } from 'vitest';

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
});
