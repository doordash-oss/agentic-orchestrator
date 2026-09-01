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
import { CANONICAL_ERROR_MESSAGE_PREFIX } from '../../../shared/errors';
import { canonicalFromWizardError, parseIpcError } from './ipcError';
import type { CanonicalError } from '../../../shared/api/parse';

const CANONICAL: CanonicalError = {
  code: 'parent_worktrees_dirty',
  class: 'needs_action',
  title: 'Parent worktrees are dirty',
  summary: "The parent feature's worktrees have uncommitted changes.",
  remediation: { hint: 'Commit or stash the listed changes in each repository, then retry.' },
  context: { repositories: [{ name: 'repo-a', dirty_files: ['src/one.ts'] }] },
};

describe('parseIpcError', () => {
  it('preserves preload metadata apart from the display message', () => {
    const remediation = 'Review and reconcile the branch on GitHub, then refresh and retry.';
    const error = Object.assign(
      new Error(`publish_remote_diverged: safe display message ${remediation}`),
      { code: 'publish_remote_diverged', remediation },
    );

    expect(parseIpcError(error)).toEqual({
      code: 'publish_remote_diverged',
      message: 'safe display message',
      remediation,
    });
  });

  it('returns the canonical object when preload attached it', () => {
    const error = Object.assign(new Error('unused'), { canonical: CANONICAL });
    expect(parseIpcError(error)).toEqual({
      code: 'parent_worktrees_dirty',
      message: CANONICAL.summary,
      remediation: CANONICAL.remediation?.hint,
      canonical: CANONICAL,
    });
  });

  it('recovers the canonical object from the sentinel message the bridge keeps', () => {
    // Custom Error properties do not survive the context bridge, so preload
    // carries the canonical object inside the message.
    const error = new Error(CANONICAL_ERROR_MESSAGE_PREFIX + JSON.stringify(CANONICAL));
    expect(parseIpcError(error)).toEqual({
      code: 'parent_worktrees_dirty',
      message: CANONICAL.summary,
      remediation: CANONICAL.remediation?.hint,
      canonical: CANONICAL,
    });
  });

  it('falls back to the legacy shape when a sentinel message is malformed', () => {
    const error = new Error(CANONICAL_ERROR_MESSAGE_PREFIX + 'not json');
    const parsed = parseIpcError(error);
    expect(parsed.canonical).toBeUndefined();
    expect(parsed.code).toBe('E_IPC');
  });

  it('adapts legacy transport errors to a blocking canonical for ErrorSurface', () => {
    const adapted = canonicalFromWizardError({
      code: 'E_REQUEST_TIMEOUT',
      message: 'The runtime did not answer within the request bound.',
      remediation: 'Wait for the feature to refresh.',
    });
    expect(adapted).toEqual({
      code: 'E_REQUEST_TIMEOUT',
      class: 'blocking',
      title: 'Request failed',
      summary: 'The runtime did not answer within the request bound.',
      remediation: { hint: 'Wait for the feature to refresh.' },
    });
    expect(canonicalFromWizardError({ code: 'x', message: 'm', canonical: CANONICAL })).toBe(
      CANONICAL,
    );
  });
});
