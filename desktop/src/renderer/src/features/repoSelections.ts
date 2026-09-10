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

/**
 * Repository selection reconciliation for the creation sheet. Selections are
 * bound to the server-resolved repository identity captured at selection
 * time; every catalog refresh reconciles by that identity so a rename moves
 * the selection to its current key while a removed or replaced repository
 * surfaces as needing reselection instead of silently adopting whatever now
 * carries the old key or path.
 */
import {
  sameRepositoryIdentity,
  type RepositoryIdentity,
  type RepositoryState,
} from '../../../shared/ipc';
import type { RepositoryFileRef } from '../../../shared/ipc';

/** A repository chosen in the picker, bound to its identity at selection time. */
export interface RepoSelection {
  key: string;
  identity: RepositoryIdentity;
}

/**
 * A selection after reconciliation against a fresh catalog. `unresolved`
 * selections keep their last known key for display and must be reselected or
 * removed before the draft can continue.
 */
export type ReconciledSelection =
  | { status: 'selected'; key: string; identity: RepositoryIdentity }
  | { status: 'unresolved'; key: string };

/** Identity equality: the whole server-resolved tuple, never a key or path alone. */
export function sameRepoIdentity(a: RepositoryIdentity, b: RepositoryIdentity): boolean {
  return sameRepositoryIdentity(a, b);
}

/**
 * Reconciles repository-file references against a fresh catalog by identity:
 * a rename moves the reference's transmitted key to its current value while
 * the relative path (and the description text that mentions it) is retained
 * verbatim; a missing or replaced repository keeps the reference with its
 * last known key so it stays visibly unresolved until the user reselects or
 * removes it.
 */
export function reconcileRepositoryFiles(
  files: readonly RepositoryFileRef[],
  catalog: readonly RepositoryState[],
): readonly RepositoryFileRef[] {
  return files.map((file) => {
    if (file.identity === undefined) return file;
    const expected = file.identity;
    const match = catalog.find(
      (repo) => repo.identity !== undefined && sameRepoIdentity(repo.identity, expected),
    );
    return match === undefined || match.identity === undefined
      ? file
      : { ...file, repoKey: match.name, identity: match.identity };
  });
}

/**
 * A repository can be selected only when the server resolved its identity;
 * without one there is nothing to reconcile a selection against across
 * discovery changes, so the row stays visible but unselectable.
 */
export function isSelectableRepository(repo: RepositoryState): boolean {
  return repo.valid && repo.featureReady && repo.identity !== undefined;
}

/**
 * Reconciles ordered selections against a fresh catalog. Order is preserved
 * and no selection is duplicated; a selection whose identity is gone (removed,
 * replaced, or unresolvable) becomes `unresolved` — an old key or a matching
 * path alone never selects a replacement. When several catalog entries share
 * one identity (an explicit registration aliasing a discovered repository),
 * the entry still carrying the selection's current key wins.
 */
export function reconcileRepoSelections(
  selections: readonly RepoSelection[],
  catalog: readonly RepositoryState[],
): ReconciledSelection[] {
  return selections.map((selection) => {
    const candidates = catalog.filter(
      (repo) => repo.identity !== undefined && sameRepoIdentity(repo.identity, selection.identity),
    );
    const match = candidates.find((repo) => repo.name === selection.key) ?? candidates[0];
    return match === undefined || match.identity === undefined
      ? { status: 'unresolved', key: selection.key }
      : { status: 'selected', key: match.name, identity: match.identity };
  });
}

/** Keys of the still-resolved selections, in selection order. */
export function resolvedSelectionKeys(
  selections: readonly ReconciledSelection[],
): readonly string[] {
  return selections.flatMap((selection) =>
    selection.status === 'selected' ? [selection.key] : [],
  );
}

/** Any selection the user must resolve or remove before continuing. */
export function hasUnresolvedSelection(selections: readonly ReconciledSelection[]): boolean {
  return selections.some((selection) => selection.status === 'unresolved');
}
