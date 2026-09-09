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
import type {
  CanonicalError,
  RepositoryIdentity,
  RepositoryOriginStatusSnapshot,
  RepositoryUpdateSourceResult,
} from '../../../shared/ipc';
import { mockRepoIdentity } from '../test/agenticoMock';
import {
  noticeFor,
  serverLabelFor,
  updateBlockedExplanation,
  updateErrorText,
  updateImpactText,
  updateRefusalText,
  updateSuccessText,
  updateTargetFor,
  withoutNoticesFor,
  type SourceUpdateNotice,
} from './sourceUpdates';

const IDENTITY: RepositoryIdentity = mockRepoIdentity('/work/space/repo-a');

function behindRow(
  overrides: Partial<RepositoryOriginStatusSnapshot> = {},
): RepositoryOriginStatusSnapshot {
  return {
    repoKey: 'repo-a',
    identity: IDENTITY,
    mode: 'default',
    kind: 'branch',
    branch: 'main',
    localSha: 'a'.repeat(40),
    originBranch: 'main',
    fetchedSha: 'c'.repeat(40),
    status: 'behind',
    behindCount: 2,
    updateEligible: true,
    checkoutHeadRef: 'refs/heads/work',
    checkoutHeadSha: 'b'.repeat(40),
    ...overrides,
  };
}

describe('updateTargetFor', () => {
  it('binds the displayed expectations unchanged for an eligible behind row', () => {
    const target = updateTargetFor(
      behindRow({
        branch: 'feature/slashy',
        originBranch: 'other-name',
        checkoutHeadRef: 'refs/heads/main',
      }),
    );
    expect(target).not.toBeNull();
    expect(target?.request).toStrictEqual({
      repoKey: 'repo-a',
      identity: IDENTITY,
      mode: 'default',
      branch: 'feature/slashy',
      originBranch: 'other-name',
      expectedLocalSha: 'a'.repeat(40),
      expectedOriginSha: 'c'.repeat(40),
      checkoutHeadRef: 'refs/heads/main',
      checkoutHeadSha: 'b'.repeat(40),
    });
    expect(target?.branch).toBe('feature/slashy');
    expect(target?.originBranch).toBe('other-name');
  });

  it('never treats advisory eligibility as permission for a clean original-checkout target', () => {
    expect(updateTargetFor(behindRow({ checkoutHeadRef: 'refs/heads/main' }))).toBeNull();
  });

  it('requires an observed checkout identity, a behind comparison, and eligibility', () => {
    expect(
      updateTargetFor(behindRow({ checkoutHeadRef: undefined, checkoutHeadSha: undefined })),
    ).toBeNull();
    expect(updateTargetFor(behindRow({ status: 'up_to_date', updateEligible: false }))).toBeNull();
    expect(updateTargetFor(behindRow({ updateEligible: false }))).toBeNull();
    expect(
      updateTargetFor(
        behindRow({
          kind: 'detached',
          branch: undefined,
          updateEligible: undefined,
        }),
      ),
    ).toBeNull();
    expect(updateTargetFor(behindRow({ localSha: undefined, updateEligible: false }))).toBeNull();
    expect(updateTargetFor(behindRow({ fetchedSha: undefined, updateEligible: false }))).toBeNull();
  });
});

describe('updateBlockedExplanation', () => {
  it('explains a clean original-checkout target as unavailable in this phase', () => {
    expect(updateBlockedExplanation(behindRow({ checkoutHeadRef: 'refs/heads/main' }), 'lab')).toBe(
      'main is checked out in the original repository — updating it from here is unavailable in this phase; update that checkout yourself.',
    );
  });

  it('identifies external action on the correct server for a linked worktree holder', () => {
    expect(
      updateBlockedExplanation(
        behindRow({ updateEligible: false, updateBlockers: ['branch_checked_out_in_worktree'] }),
        'lab-server',
      ),
    ).toBe(
      "main is checked out in a linked worktree — update that worktree's checkout on lab-server yourself.",
    );
  });

  it('explains an uninspectable checkout, a dirty holder, and an in-progress git operation', () => {
    expect(
      updateBlockedExplanation(
        behindRow({
          updateEligible: false,
          checkoutHeadRef: undefined,
          checkoutHeadSha: undefined,
        }),
        'lab',
      ),
    ).toBe(
      "main cannot be updated from here — the repository's checkout could not be inspected, so it cannot be proven unoccupied.",
    );
    expect(
      updateBlockedExplanation(
        behindRow({ updateEligible: false, updateBlockers: ['dirty_target_checkout'] }),
        'lab',
      ),
    ).toContain('uncommitted changes');
    expect(
      updateBlockedExplanation(
        behindRow({ updateEligible: false, updateBlockers: ['git_operation_in_progress'] }),
        'lab',
      ),
    ).toContain('another Agentico git operation is running');
  });

  it('stays quiet for rows that are not behind branches', () => {
    expect(updateBlockedExplanation(behindRow({ status: 'ahead' }), 'lab')).toBeNull();
    expect(
      updateBlockedExplanation(behindRow({ kind: 'detached', branch: undefined }), 'lab'),
    ).toBeNull();
    // A local-not-behind row has nothing to add beyond the comparison line.
    expect(
      updateBlockedExplanation(
        behindRow({ updateEligible: false, updateBlockers: ['local_not_behind'] }),
        'lab',
      ),
    ).toBeNull();
  });
});

describe('update copy', () => {
  const target = updateTargetFor(behindRow());
  if (target === null) throw new Error('fixture must be eligible');

  it('names the exact branches, the original repository, and the server in the impact line', () => {
    expect(updateImpactText(target, 'lab-server')).toBe(
      'Advances main to origin/main in the original repository on lab-server.',
    );
  });

  it('announces a definitive advance with the resulting tip and a no-op as already at origin', () => {
    expect(
      updateSuccessText(
        {
          result: 'updated',
          repoKey: 'repo-a',
          identity: IDENTITY,
          mode: 'default',
          branch: 'main',
          originBranch: 'main',
          previousSha: 'a'.repeat(40),
          localSha: 'ccccccc'.padEnd(40, 'c'),
        } as RepositoryUpdateSourceResult,
        target,
        'lab-server',
      ),
    ).toBe('Updated main to origin/main on lab-server (now at ccccccc).');
    expect(
      updateSuccessText(
        {
          result: 'already_up_to_date',
          repoKey: 'repo-a',
          identity: IDENTITY,
          mode: 'default',
          branch: 'main',
          originBranch: 'main',
          localSha: 'c'.repeat(40),
        } as RepositoryUpdateSourceResult,
        target,
        'lab-server',
      ),
    ).toBe('main is already at origin/main on lab-server.');
  });

  it('states typed refusals without ever claiming a rollback', () => {
    const refusal = (result: Partial<RepositoryUpdateSourceResult>): string =>
      updateRefusalText(
        {
          reason: 'origin_tip_changed',
          ...result,
        } as Pick<RepositoryUpdateSourceResult, 'reason' | 'status'>,
        target,
        'lab-server',
      );
    expect(refusal({ reason: 'origin_tip_changed' })).toBe(
      'main was not updated on lab-server — origin/main moved since the comparison. Check again, then update from the fresh comparison.',
    );
    expect(refusal({ reason: 'local_tip_changed' })).toContain('it moved since the comparison');
    expect(refusal({ reason: 'not_fast_forward' })).toContain(
      'only fast-forward updates are supported. Nothing was changed.',
    );
    expect(refusal({ reason: 'origin_branch_missing' })).toContain(
      'origin/main no longer exists on the remote. Nothing was changed.',
    );
    expect(
      refusal({
        reason: 'branch_checked_out',
        status: behindRow({
          status: 'up_to_date',
          updateEligible: false,
          updateBlockers: ['branch_checked_out_in_original_checkout'],
        }),
      }),
    ).toContain('now checked out in the original repository');
    expect(
      refusal({
        reason: 'branch_checked_out',
        status: behindRow({
          status: 'up_to_date',
          updateEligible: false,
          updateBlockers: ['branch_checked_out_in_worktree'],
        }),
      }),
    ).toContain(
      "now checked out in a linked worktree. Update that worktree's checkout on lab-server yourself.",
    );
  });

  it('preserves the canonical rejection summary verbatim', () => {
    const error: CanonicalError = {
      code: 'source_update_unavailable',
      class: 'blocking',
      title: 'Source update unavailable',
      summary: 'The update could not be completed; do not assume it was rolled back.',
    };
    expect(updateErrorText(target, error, 'lab-server')).toBe(
      'main was not updated on lab-server — The update could not be completed; do not assume it was rolled back.',
    );
  });
});

describe('serverLabelFor', () => {
  it('uses the connected server name and a truthful fallback otherwise', () => {
    expect(serverLabelFor({ status: 'ready', serverName: 'lab-server' })).toBe('lab-server');
    expect(serverLabelFor({ status: 'ready', serverName: null })).toBe('the connected server');
    expect(serverLabelFor({ status: 'ready' })).toBe('the connected server');
    expect(serverLabelFor({ status: 'resolving-runtime' })).toBe('the connected server');
  });
});

describe('notice bookkeeping', () => {
  const other = mockRepoIdentity('/work/space/repo-b', { inode: '99' });
  const notices: readonly SourceUpdateNotice[] = [
    { repoKey: 'repo-a', identity: IDENTITY, tone: 'warning', text: 'a' },
    { repoKey: 'repo-b', identity: other, tone: 'success', text: 'b' },
  ];
  it('finds by identity and removes exactly one identity', () => {
    expect(noticeFor(notices, IDENTITY)?.text).toBe('a');
    expect(noticeFor(notices, mockRepoIdentity('/elsewhere', { inode: '1' }))).toBeUndefined();
    expect(withoutNoticesFor(notices, IDENTITY).map((notice) => notice.text)).toStrictEqual(['b']);
  });
});
