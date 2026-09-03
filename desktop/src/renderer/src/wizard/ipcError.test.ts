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
import { CANONICAL_ERROR_MESSAGE_PREFIX, buildCanonicalError } from '../../../shared/errors';
import { parseIpcError } from './ipcError';
import type { CanonicalError } from '../../../shared/api/parse';

const CANONICAL: CanonicalError = {
  code: 'parent_worktrees_dirty',
  class: 'needs_action',
  title: 'Parent worktrees are dirty',
  summary: "The parent feature's worktrees have uncommitted changes.",
  remediation: { hint: 'Commit or stash the listed changes in each repository, then retry.' },
  context: { repositories: [{ name: 'repo-a', dirty_files: ['src/one.ts'] }] },
};

const IPC_UNREACHABLE = buildCanonicalError('E_IPC_UNREACHABLE');

describe('parseIpcError', () => {
  it('recovers the canonical object from the sentinel message the bridge keeps', () => {
    // Custom Error properties do not survive the context bridge, so preload
    // carries the canonical object inside the message.
    const error = new Error(CANONICAL_ERROR_MESSAGE_PREFIX + JSON.stringify(CANONICAL));
    expect(parseIpcError(error)).toEqual(CANONICAL);
  });

  it('honors an attached canonical prop (same-world mocks and tests)', () => {
    const error = Object.assign(new Error('unused'), { canonical: CANONICAL });
    expect(parseIpcError(error)).toEqual(CANONICAL);
  });

  it('prefers the attached canonical over the sentinel message', () => {
    const error = Object.assign(
      new Error(CANONICAL_ERROR_MESSAGE_PREFIX + JSON.stringify(IPC_UNREACHABLE)),
      { canonical: CANONICAL },
    );
    expect(parseIpcError(error)).toEqual(CANONICAL);
  });

  it('degrades an unparseable sentinel message to the catalog IPC-unreachable canonical', () => {
    const error = new Error(CANONICAL_ERROR_MESSAGE_PREFIX + 'not json');
    expect(parseIpcError(error)).toEqual(IPC_UNREACHABLE);
  });

  it('degrades any other rejection to the catalog IPC-unreachable canonical', () => {
    expect(parseIpcError(new Error('connect ECONNREFUSED'))).toEqual(IPC_UNREACHABLE);
    expect(parseIpcError('not even an error')).toEqual(IPC_UNREACHABLE);
    expect(parseIpcError(undefined)).toEqual(IPC_UNREACHABLE);
  });

  it('preserves the canonical code, class, title, and summary verbatim', () => {
    const error = new Error(CANONICAL_ERROR_MESSAGE_PREFIX + JSON.stringify(CANONICAL));
    const parsed = parseIpcError(error);
    expect(parsed.code).toBe('parent_worktrees_dirty');
    expect(parsed.class).toBe('needs_action');
    expect(parsed.title).toBe('Parent worktrees are dirty');
    expect(parsed.summary).toBe("The parent feature's worktrees have uncommitted changes.");
    expect(parsed.remediation?.hint).toBe(
      'Commit or stash the listed changes in each repository, then retry.',
    );
  });
});
