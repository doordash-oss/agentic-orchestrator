/**
 * Owner-only, recoverable storage for unsaved resource-editor text.
 *
 * Delegates to the generic {@link KeyedDraftStore}, configuring it with the
 * resource-draft key shape, file name, draft cap, error codes, and Zod schemas.
 */
import {
  LocalResourceDraftSaveRequestSchema,
  LocalResourceDraftStoreSchema,
  MAX_LOCAL_RESOURCE_DRAFT_BYTES,
  type LocalResourceDraft,
  type LocalResourceDraftSaveRequest,
  type LocalResourceDraftLookupRequest,
  type ResourceDraftKey,
} from '../shared/ipc';
import { KeyedDraftStore } from './keyedDraftStore';

const FILE_NAME = 'resource-local-drafts.json';

export interface ResourceDraftStoreOptions {
  warn?: (message: string) => void;
  now?: () => Date;
}

export class ResourceDraftStore {
  private readonly store: KeyedDraftStore<
    LocalResourceDraft,
    ResourceDraftKey,
    LocalResourceDraftLookupRequest,
    LocalResourceDraftSaveRequest
  >;

  constructor(
    private readonly dir: string,
    options: ResourceDraftStoreOptions = {},
  ) {
    this.store = new KeyedDraftStore(
      dir,
      {
        fileName: FILE_NAME,
        maxDrafts: 50,
        errorCodePrefix: 'RESOURCE_DRAFT',
        maxBytes: MAX_LOCAL_RESOURCE_DRAFT_BYTES,
        saveRequestSchema: LocalResourceDraftSaveRequestSchema,
        storeSchema: LocalResourceDraftStoreSchema,
        keysMatch,
        partialMatch,
        hasRevision: (lookup) => lookup.baseRevision !== undefined,
        recoveryLabel: 'resource drafts',
      },
      options,
    );
  }

  load(key: LocalResourceDraftLookupRequest): LocalResourceDraft | null {
    return this.store.load(key);
  }

  save(request: LocalResourceDraftSaveRequest): LocalResourceDraft {
    return this.store.save(request);
  }

  discard(key: ResourceDraftKey): boolean {
    return this.store.discard(key);
  }
}

function keysMatch(draft: ResourceDraftKey, key: ResourceDraftKey): boolean {
  return (
    draft.runtimeId === key.runtimeId &&
    draft.resourceId === key.resourceId &&
    draft.baseRevision === key.baseRevision
  );
}

function partialMatch(draft: ResourceDraftKey, key: LocalResourceDraftLookupRequest): boolean {
  return draft.runtimeId === key.runtimeId && draft.resourceId === key.resourceId;
}
