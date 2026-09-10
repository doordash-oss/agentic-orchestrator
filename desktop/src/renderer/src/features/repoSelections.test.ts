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
import type { RepositoryIdentity, RepositoryState } from '../../../shared/ipc';
import {
  hasUnresolvedSelection,
  isSelectableRepository,
  reconcileRepoSelections,
  reconcileRepositoryFiles,
  resolvedSelectionKeys,
  sameRepoIdentity,
  type RepoSelection,
} from './repoSelections';

function identity(path: string, device = '1', inode = '2'): RepositoryIdentity {
  return { path, commonDir: `${path}/.git`, device, inode };
}

function repo(
  name: string,
  id: RepositoryIdentity,
  overrides: Partial<RepositoryState> = {},
): RepositoryState {
  return { name, path: id.path, valid: true, featureReady: true, identity: id, ...overrides };
}

function selection(key: string, id: RepositoryIdentity): RepoSelection {
  return { key, identity: id };
}

describe('sameRepoIdentity', () => {
  it('compares the whole identity tuple', () => {
    const base = identity('/work/service');
    expect(sameRepoIdentity(base, identity('/work/service'))).toBe(true);
    expect(sameRepoIdentity(base, identity('/work/service', '9', '2'))).toBe(false);
    expect(sameRepoIdentity(base, identity('/work/service', '1', '9'))).toBe(false);
    // A worktree shares the common dir and filesystem identity but differs in path.
    const worktree = { ...identity('/main/.git'), path: '/work/service-wt' };
    expect(sameRepoIdentity(base, { ...base, ...worktree })).toBe(false);
  });
});

describe('isSelectableRepository', () => {
  it('requires validity, feature readiness, and a resolved identity', () => {
    const id = identity('/work/service');
    expect(isSelectableRepository(repo('service', id))).toBe(true);
    expect(isSelectableRepository(repo('service', id, { valid: false, issue: undefined }))).toBe(
      false,
    );
    expect(isSelectableRepository(repo('service', id, { featureReady: false }))).toBe(false);
    expect(isSelectableRepository(repo('service', id, { identity: undefined }))).toBe(false);
  });
});

describe('reconcileRepoSelections', () => {
  it('moves a selection to its current key when a collision rename changes it', () => {
    const id = identity('/root-a/service');
    const before = [selection('service', id), selection('other', identity('/root-b/other'))];
    const catalog = [repo('root-a/service', id), repo('other', identity('/root-b/other'))];
    const reconciled = reconcileRepoSelections(before, catalog);
    expect(reconciled).toEqual([
      { status: 'selected', key: 'root-a/service', identity: id },
      { status: 'selected', key: 'other', identity: identity('/root-b/other') },
    ]);
    expect(resolvedSelectionKeys(reconciled)).toEqual(['root-a/service', 'other']);
    expect(hasUnresolvedSelection(reconciled)).toBe(false);
  });

  it('moves a selection back to the bare key when the colliding repository is removed', () => {
    const id = identity('/root-a/service');
    const before = [selection('root-a/service', id)];
    const catalog = [repo('service', id)];
    expect(reconcileRepoSelections(before, catalog)).toEqual([
      { status: 'selected', key: 'service', identity: id },
    ]);
  });

  it('preserves order and never duplicates selections', () => {
    const serviceA = identity('/root-a/service');
    const serviceB = identity('/root-b/service');
    const other = identity('/root-b/other');
    const before = [
      selection('service', serviceA),
      selection('other', other),
      selection('root-b/service', serviceB),
    ];
    const catalog = [
      repo('root-a/service', serviceA),
      repo('root-b/service', serviceB),
      repo('other', other),
    ];
    expect(resolvedSelectionKeys(reconcileRepoSelections(before, catalog))).toEqual([
      'root-a/service',
      'other',
      'root-b/service',
    ]);
  });

  it('marks a removed repository as unresolved without selecting anything else', () => {
    const id = identity('/root-a/service');
    const replacement = identity('/root-a/service', '1', '77'); // new .git at the same path
    const before = [selection('service', id)];
    const catalog = [repo('service', replacement)];
    expect(reconcileRepoSelections(before, catalog)).toEqual([
      { status: 'unresolved', key: 'service' },
    ]);
  });

  it('marks a replaced checkout at the same path and key as unresolved', () => {
    const id = identity('/root-a/service');
    const replacement = identity('/root-a/service', '1', '99');
    const before = [selection('service', id)];
    const catalog = [repo('service', replacement)];
    const reconciled = reconcileRepoSelections(before, catalog);
    expect(reconciled).toEqual([{ status: 'unresolved', key: 'service' }]);
    expect(hasUnresolvedSelection(reconciled)).toBe(true);
    expect(resolvedSelectionKeys(reconciled)).toEqual([]);
  });

  it('keeps an unresolvable selection unresolved when the catalog entry loses its identity', () => {
    const id = identity('/root-a/service');
    const before = [selection('service', id)];
    const catalog: RepositoryState[] = [
      { name: 'service', path: id.path, valid: true, featureReady: true },
    ];
    expect(reconcileRepoSelections(before, catalog)).toEqual([
      { status: 'unresolved', key: 'service' },
    ]);
  });

  it('distinguishes linked worktrees that share a common directory', () => {
    const main = identity('/work/main');
    const worktree: RepositoryIdentity = {
      path: '/work/main-wt',
      commonDir: main.commonDir,
      device: main.device,
      inode: main.inode,
    };
    const before = [selection('main', main)];
    const catalog = [repo('main', main), repo('main-wt', worktree)];
    expect(reconcileRepoSelections(before, catalog)).toEqual([
      { status: 'selected', key: 'main', identity: main },
    ]);
  });

  it('prefers the catalog entry still carrying the selection key among shared identities', () => {
    const id = identity('/root-a/service');
    const before = [selection('alias', id)];
    const catalog = [repo('alias', id), repo('service', id)];
    expect(reconcileRepoSelections(before, catalog)).toEqual([
      { status: 'selected', key: 'alias', identity: id },
    ]);
  });

  it('falls back to the first shared-identity entry when the old key is gone', () => {
    const id = identity('/root-a/service');
    const before = [selection('service', id)];
    const catalog = [repo('alias', id), repo('service-2', id)];
    expect(reconcileRepoSelections(before, catalog)).toEqual([
      { status: 'selected', key: 'alias', identity: id },
    ]);
  });
});

describe('reconcileRepositoryFiles', () => {
  it('moves a reference key with its repository across a rename', () => {
    const id = identity('/root-a/service');
    const files = [{ repoKey: 'service', path: 'src/query.ts', identity: id }];
    const catalog = [repo('root-a/service', id)];
    expect(reconcileRepositoryFiles(files, catalog)).toEqual([
      { repoKey: 'root-a/service', path: 'src/query.ts', identity: id },
    ]);
  });

  it('keeps a reference with its last known key when its repository is gone', () => {
    const id = identity('/root-a/service');
    const replacement = identity('/root-a/service', '1', '77');
    const files = [{ repoKey: 'service', path: 'src/query.ts', identity: id }];
    expect(reconcileRepositoryFiles(files, [repo('service', replacement)])).toEqual(files);
    expect(reconcileRepositoryFiles(files, [])).toEqual(files);
  });

  it('passes identity-less references through untouched', () => {
    const files = [{ repoKey: 'repo-a', path: 'src/a.ts' }];
    expect(reconcileRepositoryFiles(files, [repo('renamed', identity('/x'))])).toEqual(files);
  });
});
