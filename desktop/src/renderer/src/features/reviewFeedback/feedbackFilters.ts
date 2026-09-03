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
 * Pure filtering, facet, and batching rules for the review-feedback inbox.
 * Author and type facets combine selected values with OR inside the facet and
 * AND across facets; the path query is a case-insensitive substring match
 * against the displayed repository-relative path. Everything here is
 * deterministic and shared by the rail, drawer, bulk actions, and tests so
 * there is exactly one filtering implementation.
 */
import { COMMENT_TYPE_LABEL } from '../refactor/refactorPassModel';
import type {
  ReviewFeedbackDraftCommentView,
  ReviewFeedbackDraftRepoGroup,
  ReviewFeedbackSelectionUpdate,
} from './reviewFeedbackDraftApi';

export interface ReviewFeedbackFilters {
  authors: string[];
  types: ReviewFeedbackDraftCommentView['type'][];
  path: string;
}

export const EMPTY_FILTERS: ReviewFeedbackFilters = { authors: [], types: [], path: '' };

/**
 * Server-side bound for one reference-only selection mutation. Bulk actions
 * split their targets into batches no larger than this so no request can
 * approach the shared mutation limit through comment content.
 */
export const SELECTION_BATCH_BOUND = 512;

export function filtersActive(filters: ReviewFeedbackFilters): boolean {
  return filters.authors.length > 0 || filters.types.length > 0 || filters.path.trim() !== '';
}

/** Repository groups in the active scope, preserving the server's grouping. */
export function scopeGroups(
  repos: ReviewFeedbackDraftRepoGroup[],
  scope: string,
): ReviewFeedbackDraftRepoGroup[] {
  return scope === 'all' ? repos : repos.filter((group) => group.repo === scope);
}

/** Type facet values in their stable labelled order. */
const TYPE_ORDER = Object.keys(COMMENT_TYPE_LABEL) as ReviewFeedbackDraftCommentView['type'][];

export interface FacetOptions {
  authors: string[];
  types: ReviewFeedbackDraftCommentView['type'][];
}

/** Facet choices derived from the comments in the active repository scope. */
export function facetOptions(groups: ReviewFeedbackDraftRepoGroup[]): FacetOptions {
  const authors = new Set<string>();
  const types = new Set<ReviewFeedbackDraftCommentView['type']>();
  for (const group of groups) {
    for (const comment of group.comments) {
      if (comment.author !== undefined && comment.author !== '') authors.add(comment.author);
      types.add(comment.type);
    }
  }
  return {
    // Case-insensitive alphabetical order.
    authors: [...authors].sort((a, b) => a.localeCompare(b, undefined, { sensitivity: 'base' })),
    types: TYPE_ORDER.filter((type) => types.has(type)),
  };
}

/**
 * Scope change keeps the path query exactly as typed but drops author/type
 * values the new scope cannot offer, so a stale facet choice never silently
 * hides everything in the new scope.
 */
export function pruneFiltersForScope(
  groups: ReviewFeedbackDraftRepoGroup[],
  filters: ReviewFeedbackFilters,
): ReviewFeedbackFilters {
  const options = facetOptions(groups);
  return {
    authors: filters.authors.filter((author) => options.authors.includes(author)),
    types: filters.types.filter((type) => options.types.includes(type)),
    path: filters.path,
  };
}

/** AND across facets, OR within a facet; pathless comments never match a path query. */
export function matchesFilters(
  comment: ReviewFeedbackDraftCommentView,
  filters: ReviewFeedbackFilters,
): boolean {
  if (
    filters.authors.length > 0 &&
    (comment.author === undefined || !filters.authors.includes(comment.author))
  ) {
    return false;
  }
  if (filters.types.length > 0 && !filters.types.includes(comment.type)) return false;
  const query = filters.path.trim().toLowerCase();
  if (query !== '' && !(comment.path?.toLowerCase().includes(query) ?? false)) return false;
  return true;
}

/**
 * Splits reference-only updates into deterministic batches bounded by the
 * server's per-mutation update limit and deduplicated by stable reference.
 */
export function chunkSelectionUpdates(
  updates: ReviewFeedbackSelectionUpdate[],
): ReviewFeedbackSelectionUpdate[][] {
  const seen = new Set<string>();
  const unique: ReviewFeedbackSelectionUpdate[] = [];
  for (const update of updates) {
    if (seen.has(update.stableRef)) continue;
    seen.add(update.stableRef);
    unique.push(update);
  }
  const batches: ReviewFeedbackSelectionUpdate[][] = [];
  for (let index = 0; index < unique.length; index += SELECTION_BATCH_BOUND) {
    batches.push(unique.slice(index, index + SELECTION_BATCH_BOUND));
  }
  return batches;
}
