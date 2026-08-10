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
      execute: (command, args) => calls.push({ command, args }),
    });
    expect(calls).toEqual([
      {
        command: 'goreleaser',
        args: ['release', '--clean', '--release-notes', '/tmp/notes.md'],
      },
    ]);
  });
});
