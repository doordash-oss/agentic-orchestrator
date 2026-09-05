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
 * Per-server creation draft sessions. The creation sheet unmounts whenever
 * the app leaves the ready state (a temporary disconnect or a deliberate
 * server switch), so the draft's live state is captured into this store on
 * unmount and rehydrated when its server is ready again. One live draft
 * exists per server; nothing is copied between servers. The store lives in
 * memory only — drafts are never written to disk, browser persistent
 * storage, or the server, so closing or relaunching the application never
 * restores them.
 *
 * Retirement: an explicit discard or a successful creation removes the
 * server's entry; nested dialogs, Settings navigation, and
 * connection/readiness transitions never do.
 */
import { createContext, useContext, useRef, useSyncExternalStore } from 'react';
import type {
  CanonicalError,
  CreationDefaults,
  RepositoryFileRef,
  WorkspaceRootState,
} from '../../../shared/ipc';
import type { ComposerUploadItem } from './stagedItems';
import { checkpointsForPipeline, type CheckpointState, type Pipeline } from './runContract';
import type { RepoSelection } from './repoSelections';
import type { EffortLevel } from '../../../shared/ipc';
import type { PhaseKey } from './ConfigEditor';

/** A creation sheet's whole user-owned state, captureable and rehydratable. */
export interface CreationDraftState {
  /**
   * True once the draft has been live in a sheet: on restore the sheet keeps
   * every user-owned choice and only refreshes the catalog, instead of
   * re-applying server defaults over the user's values.
   */
  restored: boolean;
  creationKey: string;
  defaultsState:
    | { phase: 'loading' }
    | { phase: 'error'; error: CanonicalError }
    | { phase: 'loaded'; defaults: CreationDefaults };
  stepIndex: number;
  name: string;
  description: string;
  repoSelections: readonly RepoSelection[];
  repoQuery: string;
  useCurrentBranch: boolean;
  pipeline: Pipeline;
  checkpoints: CheckpointState;
  modelChoices: Partial<Record<PhaseKey, string>>;
  effortChoices: Partial<Record<PhaseKey, EffortLevel>>;
  riskLevel: 'low' | 'medium' | 'high';
  inquireness: 'none' | 'medium' | 'high';
  exitCriteria: string;
  images: readonly string[];
  attachments: readonly string[];
  imageUploads: readonly ComposerUploadItem[];
  attachmentUploads: readonly ComposerUploadItem[];
  repositoryFiles: readonly RepositoryFileRef[];
  autoStart: boolean;
  folderCandidate: string | null;
  folderHoldsNoRepository: boolean;
  folderNotice: string;
  workspaceRoots: readonly string[];
  consentOpen: boolean;
  discardOpen: boolean;
  folderDraft: string;
  folderError: string | null;
  nameError: string | null;
  repoError: string | null;
  formError: CanonicalError | null;
  catalogRefreshError: CanonicalError | null;
  /** Whether the nested clone view is open (restored with the draft). */
  cloneOpen: boolean;
  /** Roots eligible as clone destinations, from the last catalog snapshot. */
  cloneableRoots: readonly WorkspaceRootState[];
  /** The picker-initiated clone operation associated with this draft. */
  cloneAssociation: CloneAssociation | null;
}

/**
 * The picker-initiated clone operation this draft is waiting on. The
 * idempotency key is recorded before the start request flies, so a lost
 * acceptance is recovered by key; `adopted` makes selection-on-success
 * exactly-once. The association lives and retires with its draft.
 */
export interface CloneAssociation {
  idempotencyKey: string;
  operationId: string | null;
  adopted: boolean;
  /**
   * True while the start request itself is in flight: a keyed lookup must
   * not conclude anything yet, because the server may not have accepted.
   * Always false in a retained draft (a detached start is dead, and the
   * key is exactly what recovers its acceptance).
   */
  startInFlight: boolean;
}

/** One server's draft entry: whether the sheet is open and its live state. */
export interface CreationDraftEntry {
  open: boolean;
  state: CreationDraftState;
}

const UPLOAD_INTERRUPTED = 'Upload was interrupted while the server was away. Retry.';

/**
 * Normalizes a live sheet state into its retainable form: in-flight
 * submissions end (the creation idempotency key makes a retry safe), and an
 * upload that was in flight when its sheet unmounted surfaces as failed and
 * retryable instead of blocking the draft forever.
 */
export function retainableDraft(state: CreationDraftState): CreationDraftState {
  const interrupt = (items: readonly ComposerUploadItem[]) =>
    items.map((item) =>
      item.state === 'uploading'
        ? { ...item, state: 'failed' as const, message: UPLOAD_INTERRUPTED }
        : item,
    );
  return {
    ...state,
    restored: true,
    imageUploads: interrupt(state.imageUploads),
    attachmentUploads: interrupt(state.attachmentUploads),
    cloneAssociation:
      state.cloneAssociation === null ? null : { ...state.cloneAssociation, startInFlight: false },
  };
}

export function freshCreationDraft(): CreationDraftState {
  return {
    restored: false,
    creationKey: crypto.randomUUID(),
    defaultsState: { phase: 'loading' },
    stepIndex: 0,
    name: '',
    description: '',
    repoSelections: [],
    repoQuery: '',
    useCurrentBranch: false,
    pipeline: 'medium',
    checkpoints: checkpointsForPipeline('medium'),
    modelChoices: {},
    effortChoices: {},
    riskLevel: 'medium',
    inquireness: 'medium',
    exitCriteria: '',
    images: [],
    attachments: [],
    imageUploads: [],
    attachmentUploads: [],
    repositoryFiles: [],
    autoStart: true,
    folderCandidate: null,
    folderHoldsNoRepository: false,
    folderNotice: '',
    workspaceRoots: [],
    consentOpen: false,
    discardOpen: false,
    folderDraft: '',
    folderError: null,
    nameError: null,
    repoError: null,
    formError: null,
    catalogRefreshError: null,
    cloneOpen: false,
    cloneableRoots: [],
    cloneAssociation: null,
  };
}

/**
 * In-memory, per-server draft sessions. Notifies subscribers on every
 * change so mounted shells can follow their server's entry.
 */
export class CreationDraftsStore {
  private readonly entries = new Map<string, CreationDraftEntry>();
  private readonly listeners = new Set<() => void>();

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  private notify(): void {
    for (const listener of this.listeners) listener();
  }

  entry(serverKey: string): CreationDraftEntry | undefined {
    return this.entries.get(serverKey);
  }

  /** Opens the server's sheet, creating a fresh draft when none is retained. */
  openDraft(serverKey: string): void {
    const current = this.entries.get(serverKey);
    if (current === undefined) {
      this.entries.set(serverKey, { open: true, state: freshCreationDraft() });
      this.notify();
      return;
    }
    if (!current.open) {
      this.entries.set(serverKey, { ...current, open: true });
      this.notify();
    }
  }

  /** Captures the live state of an unmounted-but-retained sheet. */
  persist(serverKey: string, state: CreationDraftState): void {
    const current = this.entries.get(serverKey);
    if (current === undefined || !current.open) return;
    this.entries.set(serverKey, { open: true, state });
    this.notify();
  }

  /** Retires the server's draft: explicit discard or successful creation. */
  retire(serverKey: string): void {
    if (this.entries.delete(serverKey)) this.notify();
  }
}

export const CreationDraftsContext = createContext<CreationDraftsStore | null>(null);

/**
 * The app-session draft store. The provider (App) supplies the store that
 * survives connection flips; without one, a private per-mount store keeps
 * standalone renders (tests, story surfaces) self-contained.
 */
export function useCreationDrafts(): CreationDraftsStore {
  const provided = useContext(CreationDraftsContext);
  const fallback = useRef<CreationDraftsStore | null>(null);
  if (provided !== null) return provided;
  if (fallback.current === null) fallback.current = new CreationDraftsStore();
  return fallback.current;
}

/** Reactively reads one server's draft entry. */
export function useCreationDraftEntry(
  store: CreationDraftsStore,
  serverKey: string,
): CreationDraftEntry | undefined {
  return (
    useSyncExternalStore(
      store.subscribe,
      () => store.entry(serverKey) ?? null,
      () => null,
    ) ?? undefined
  );
}
