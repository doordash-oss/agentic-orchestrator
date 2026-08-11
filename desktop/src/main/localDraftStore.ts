/**
 * Owner-only, recoverable storage for unsaved review-editor text.
 *
 * Delegates to the generic {@link KeyedDraftStore}, configuring it with the
 * review-draft key shape, file name, draft cap, error codes, and Zod schemas.
 * A corrupt or incompatible file is moved aside; recovery always starts empty
 * rather than implying that draft text survived.
 */
import {
  LocalReviewDraftSaveRequestSchema,
  LocalReviewDraftStoreSchema,
  MAX_LOCAL_REVIEW_DRAFT_BYTES,
  type LocalReviewDraft,
  type LocalReviewDraftSaveRequest,
  type LocalReviewDraftLookupRequest,
  type ReviewDraftKey,
} from '../shared/ipc';
import { KeyedDraftStore } from './keyedDraftStore';

const FILE_NAME = 'review-local-drafts.json';

export interface LocalDraftStoreOptions {
  warn?: (message: string) => void;
  now?: () => Date;
}

export class LocalDraftStore {
  private readonly store: KeyedDraftStore<
    LocalReviewDraft,
    ReviewDraftKey,
    LocalReviewDraftLookupRequest,
    LocalReviewDraftSaveRequest
  >;

  constructor(
    private readonly dir: string,
    options: LocalDraftStoreOptions = {},
  ) {
    this.store = new KeyedDraftStore(
      dir,
      {
        fileName: FILE_NAME,
        maxDrafts: 20,
        errorCodePrefix: 'LOCAL_DRAFT',
        maxBytes: MAX_LOCAL_REVIEW_DRAFT_BYTES,
        saveRequestSchema: LocalReviewDraftSaveRequestSchema,
        storeSchema: LocalReviewDraftStoreSchema,
        keysMatch,
        partialMatch,
        hasRevision: (lookup) => lookup.baseDraftRevision !== undefined,
        recoveryLabel: 'local review drafts',
      },
      options,
    );
  }

  load(key: LocalReviewDraftLookupRequest): LocalReviewDraft | null {
    return this.store.load(key);
  }

  save(request: LocalReviewDraftSaveRequest): LocalReviewDraft {
    return this.store.save(request);
  }

  discard(key: ReviewDraftKey): boolean {
    return this.store.discard(key);
  }

  /**
   * One-time identity migration: drafts stored under a legacy runtime id
   * (the pre-serverKey `runtime.selection` value or the default) are re-keyed
   * to the connecting server's identity key. Drafts belonging to any other
   * identity are never touched; repeated calls are no-ops.
   */
  rekeyRuntimeIds(serverKey: string, legacyRuntimeIds: readonly string[]): number {
    const legacy = new Set(legacyRuntimeIds);
    return this.store.rekey(
      (draft) => legacy.has(draft.runtimeId),
      (draft) => ({ ...draft, runtimeId: serverKey }),
    );
  }
}

function keysMatch(draft: ReviewDraftKey, key: ReviewDraftKey): boolean {
  return (
    draft.runtimeId === key.runtimeId &&
    draft.featureId === key.featureId &&
    draft.reviewId === key.reviewId &&
    draft.baseDraftRevision === key.baseDraftRevision
  );
}

function partialMatch(draft: ReviewDraftKey, key: LocalReviewDraftLookupRequest): boolean {
  return (
    draft.runtimeId === key.runtimeId &&
    draft.featureId === key.featureId &&
    draft.reviewId === key.reviewId
  );
}
