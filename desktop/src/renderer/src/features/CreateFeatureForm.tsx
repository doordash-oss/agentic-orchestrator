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
 * The creation sheet: a window-modal, title-bar-attached sheet carrying the
 * four-step creation contract — Repositories, Describe, Depth, Contract.
 * Cancel and Escape are the only exits (with today's discard confirmation for
 * a dirty draft); the workspace stays mounted and navigable beneath the
 * scrim. Initial defaults prefill the draft once; later repository discovery
 * must preserve every user-owned choice.
 */
import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react';
import {
  type CloneOperation,
  type CreationDefaults,
  type EffortLevel,
  type ReadinessSnapshot,
  type RepositoryFileRef,
  type RepositoryIdentity,
  type RepositorySourcesResult,
  type RepositoryState,
  type WorkspaceRootState,
} from '../../../shared/ipc';
import { ConsentDialog } from '../components/wizard/ConsentDialog';
import { ErrorSurface } from '../components/ErrorSurface';
import { FieldError, fieldAriaDescribedBy, fieldAriaInvalid } from '../components/FieldError';
import { useModalDismiss } from '../components/useModalDismiss';
import { retryAction, useConnectionState, type LoadState } from '../hooks';
import {
  isBlockingStagedItem,
  STAGED_ITEMS_BLOCK_SUBMIT,
  submittableReferences,
  type ComposerUploadItem,
} from './stagedItems';
import { parseIpcError } from '../wizard/ipcError';
import { buildCanonicalError } from '../../../shared/errors';
import type { CanonicalError } from '../../../shared/ipc';
import {
  GATE_FIELDS,
  ModelEffortRow,
  applicableGates,
  applicablePhaseFields,
  useModelCatalogue,
  type PhaseKey,
} from './ConfigEditor';
import {
  retainableDraft,
  type CloneAssociation,
  type CreationDraftState,
  type PendingCreate,
  type PendingInitialize,
} from './creationDrafts';
import { InitializeOffer, type InitializeOfferController } from './cloneViews';
import { PickerCloneDialog } from './PickerCloneDialog';
import { PickerCreateDialog } from './PickerCreateDialog';
import type { CreateRepositoryStartInput } from './createViews';
import { DescriptionComposer } from './DescriptionComposer';
import { fieldForCreationError } from './featureView';
import {
  hasUnresolvedSelection,
  isSelectableRepository,
  reconcileRepoSelections,
  reconcileRepositoryFiles,
  resolvedSelectionKeys,
  sameRepoIdentity,
  type RepoSelection,
} from './repoSelections';
import {
  PIPELINES,
  checkpointSummary,
  checkpointsForPipeline,
  isPipeline,
  modelConfigKey,
  type CheckpointState,
  type Pipeline,
} from './runContract';

type DefaultsState = LoadState<{ phase: 'loaded'; defaults: CreationDefaults }>;
type SourceState =
  | { phase: 'idle' | 'loading' }
  | { phase: 'loaded'; value: RepositorySourcesResult }
  | { phase: 'error'; error: CanonicalError };
const EMPTY_REPOSITORIES: readonly RepositoryState[] = [];

const STEPS = ['Repositories', 'Describe', 'Depth', 'Contract'] as const;
type Step = (typeof STEPS)[number];

/** CreationDefaults phase labels → catalogue phase keys. */
const DEFAULT_MODEL_LABELS: ReadonlyArray<readonly [string, PhaseKey]> = [
  ['Inquiry', 'inquiry'],
  ['Research', 'research'],
  ['Planning', 'planning'],
  ['Implementation', 'implementation'],
  ['Review', 'review'],
  ['Utilities', 'utilities'],
  ['Knowledge base', 'kbBuild'],
];

function defaultModelsByKey(defaults: CreationDefaults): Partial<Record<PhaseKey, string>> {
  const byLabel = new Map(defaults.defaults.models.map(({ phase, model }) => [phase, model]));
  const result: Partial<Record<PhaseKey, string>> = {};
  for (const [label, key] of DEFAULT_MODEL_LABELS) {
    const model = byLabel.get(label);
    if (model !== undefined && model !== '') result[key] = model;
  }
  return result;
}

function defaultEffortByKey(defaults: CreationDefaults): Partial<Record<PhaseKey, EffortLevel>> {
  const byLabel = new Map(defaults.defaults.effort.map(({ phase, effort }) => [phase, effort]));
  const result: Partial<Record<PhaseKey, EffortLevel>> = {};
  for (const [label, key] of DEFAULT_MODEL_LABELS) {
    const effort = byLabel.get(label);
    if (effort !== undefined) result[key] = effort;
  }
  return result;
}

function withoutTrailingSeparators(folder: string): string {
  return folder.replace(/[\\/]+$/, '');
}

/** Null when the folder has no usable parent to configure as a root. */
function parentDirectory(folder: string): string | null {
  const trimmed = withoutTrailingSeparators(folder);
  const cut = Math.max(trimmed.lastIndexOf('/'), trimmed.lastIndexOf('\\'));
  return cut <= 0 ? null : trimmed.slice(0, cut);
}

/** Usable repositories at or below a folder, per the authoritative snapshot. */
function repositoriesWithin(
  repositories: readonly RepositoryState[],
  folder: string,
): readonly RepositoryState[] {
  const prefix = withoutTrailingSeparators(folder);
  return repositories.filter((repository) => {
    const path = withoutTrailingSeparators(repository.path);
    return (
      repository.valid &&
      (path === prefix || path.startsWith(`${prefix}/`) || path.startsWith(`${prefix}\\`))
    );
  });
}

function plural(count: number, one: string, many: string): string {
  return `${count} ${count === 1 ? one : many}`;
}

export interface CreateFeatureFormProps {
  onCreated(created: { featureId: string; name: string }): void;
  /** Cancel/Escape after any confirmation: the sheet closes, draft discarded. */
  onClose(): void;
  /**
   * The server's retained draft, when the sheet remounts after a connection
   * flip or server switch. Every user-owned value rehydrates from it; only
   * the catalog is refreshed. Absent for a fresh draft.
   */
  retainedDraft?: CreationDraftState;
  /**
   * Captures the live draft when the sheet unmounts without being retired
   * (a disconnect or server switch). Retirement paths (explicit discard,
   * successful creation) never call it. Absent in standalone renders.
   */
  onDraftDetach?(state: CreationDraftState): void;
}

export function CreateFeatureForm({
  onCreated,
  onClose,
  retainedDraft,
  onDraftDetach,
}: CreateFeatureFormProps) {
  // A retained draft rehydrates every user-owned value; only in-flight
  // work (a submission, a folder probe) resets, and the mount effect
  // refreshes the catalog instead of re-applying server defaults.
  const retained = retainedDraft;
  const [state, setState] = useState<DefaultsState>(() =>
    retained ? retained.defaultsState : { phase: 'loading' },
  );
  const [stepIndex, setStepIndex] = useState(() => retained?.stepIndex ?? 0);
  const [name, setName] = useState(() => retained?.name ?? '');
  const [description, setDescription] = useState(() => retained?.description ?? '');
  // Selections are bound to the server-resolved repository identity captured
  // at selection time; the catalog refresh reconciles them by identity so a
  // rename moves a selection to its current key while a removed or replaced
  // repository surfaces as needing reselection.
  const [repoSelections, setRepoSelections] = useState<readonly RepoSelection[]>(
    () => retained?.repoSelections ?? [],
  );
  const [repoQuery, setRepoQuery] = useState(() => retained?.repoQuery ?? '');
  const [useCurrentBranch, setUseCurrentBranch] = useState(
    () => retained?.useCurrentBranch ?? false,
  );
  const [pipeline, setPipeline] = useState<Pipeline>(() => retained?.pipeline ?? 'medium');
  const [checkpoints, setCheckpoints] = useState<CheckpointState>(
    () => retained?.checkpoints ?? checkpointsForPipeline('medium'),
  );
  const [modelChoices, setModelChoices] = useState<Partial<Record<PhaseKey, string>>>(
    () => retained?.modelChoices ?? {},
  );
  const [effortChoices, setEffortChoices] = useState<Partial<Record<PhaseKey, EffortLevel>>>(
    () => retained?.effortChoices ?? {},
  );
  const [riskLevel, setRiskLevel] = useState<'low' | 'medium' | 'high'>(
    () => retained?.riskLevel ?? 'medium',
  );
  const [inquireness, setInquireness] = useState<'none' | 'medium' | 'high'>(
    () => retained?.inquireness ?? 'medium',
  );
  const [exitCriteria, setExitCriteria] = useState(() => retained?.exitCriteria ?? '');
  const [images, setImages] = useState<readonly string[]>(() => retained?.images ?? []);
  const [attachments, setAttachments] = useState<readonly string[]>(
    () => retained?.attachments ?? [],
  );
  const [imageUploads, setImageUploads] = useState<readonly ComposerUploadItem[]>(
    () => retained?.imageUploads ?? [],
  );
  const [attachmentUploads, setAttachmentUploads] = useState<readonly ComposerUploadItem[]>(
    () => retained?.attachmentUploads ?? [],
  );
  const [repositoryFiles, setRepositoryFiles] = useState<readonly RepositoryFileRef[]>(
    () => retained?.repositoryFiles ?? [],
  );
  const [autoStart, setAutoStart] = useState(() => retained?.autoStart ?? true);
  const [folderCandidate, setFolderCandidate] = useState<string | null>(
    () => retained?.folderCandidate ?? null,
  );
  /** Set once a candidate is a configured root that holds no repository. */
  const [folderHoldsNoRepository, setFolderHoldsNoRepository] = useState(
    () => retained?.folderHoldsNoRepository ?? false,
  );
  const [folderNotice, setFolderNotice] = useState(() => retained?.folderNotice ?? '');
  const [workspaceRoots, setWorkspaceRoots] = useState<readonly string[]>(
    () => retained?.workspaceRoots ?? [],
  );
  const [consentOpen, setConsentOpen] = useState(() => retained?.consentOpen ?? false);
  const [discardOpen, setDiscardOpen] = useState(() => retained?.discardOpen ?? false);
  const [folderPending, setFolderPending] = useState(false);
  /** Typed path + its inline rejection, only ever used on remote servers. */
  const [folderDraft, setFolderDraft] = useState(() => retained?.folderDraft ?? '');
  const [folderError, setFolderError] = useState<string | null>(
    () => retained?.folderError ?? null,
  );
  const [pending, setPending] = useState(false);
  const [nameError, setNameError] = useState<string | null>(() => retained?.nameError ?? null);
  const [repoError, setRepoError] = useState<string | null>(() => retained?.repoError ?? null);
  const [formError, setFormError] = useState<CanonicalError | null>(
    () => retained?.formError ?? null,
  );
  /** A failed catalog refresh never becomes an authoritative empty catalog. */
  const [catalogRefreshError, setCatalogRefreshError] = useState<CanonicalError | null>(
    () => retained?.catalogRefreshError ?? null,
  );
  const [sourceState, setSourceState] = useState<SourceState>({ phase: 'idle' });
  // The nested clone view and its association with this draft, plus the
  // nested create view and its pending adoption.
  const [cloneOpen, setCloneOpen] = useState(() => retained?.cloneOpen ?? false);
  const [cloneAssociation, setCloneAssociation] = useState<CloneAssociation | null>(
    () => retained?.cloneAssociation ?? null,
  );
  const [createOpen, setCreateOpen] = useState(() => retained?.createOpen ?? false);
  const [pendingCreate, setPendingCreate] = useState<PendingCreate | null>(
    () => retained?.pendingCreate ?? null,
  );
  // The picker-initiated explicit initialization this draft is waiting to
  // adopt, plus the offer affordances scoped to the repository action.
  const [pendingInitialize, setPendingInitialize] = useState<PendingInitialize | null>(
    () => retained?.pendingInitialize ?? null,
  );
  const [initializeError, setInitializeError] = useState<CanonicalError | null>(null);
  /** Clone-success offers declined with Not now (hidden per operation). */
  const [declinedInitializeOperations, setDeclinedInitializeOperations] = useState<
    ReadonlySet<string>
  >(new Set());
  /** The unborn catalog row whose inline later-opt-in offer is expanded. */
  const [initializeExpandedKey, setInitializeExpandedKey] = useState<string | null>(null);
  // All configured workspace roots (the destination controls filter for
  // clone eligibility themselves).
  const [cloneableRoots, setCloneableRoots] = useState<readonly WorkspaceRootState[]>(
    () => retained?.cloneableRoots ?? [],
  );
  /** Authoritative snapshot of the associated clone operation. */
  const [cloneOperation, setCloneOperation] = useState<CloneOperation | null>(null);
  /** The repository row awaiting focus once the picker step is visible. */
  const [focusRepoKey, setFocusRepoKey] = useState<string | null>(null);
  const catalogue = useModelCatalogue();
  const creationKey = useRef(retained?.creationKey ?? crypto.randomUUID());
  const sheetRef = useRef<HTMLDivElement | null>(null);
  const nameRef = useRef<HTMLInputElement | null>(null);
  const repoGroupRef = useRef<HTMLFieldSetElement | null>(null);
  const formErrorRef = useRef<HTMLDivElement | null>(null);
  const catalogRefreshSeq = useRef(0);
  const cloneResolveSeq = useRef(0);
  const initializeRequestSeq = useRef(0);
  const sourceRequestSeq = useRef(0);

  // Locality decides how a folder reaches the form: the native directory
  // dialog on a local server (the picker resolves real paths on this
  // machine), typed entry on a remote one (the folder lives on the server
  // host, so only the server can validate it). Follows the live connection
  // state so a server switch swaps the affordance without remounting.
  const connection = useConnectionState();
  const remoteServer = connection.status === 'ready' && connection.kind === 'remote';
  const serverKey = connection.status === 'ready' ? (connection.serverKey ?? null) : null;
  // In-progress, failed, or foreign-server uploads block creation until
  // they are removed (or the user switches back to the server that holds them).
  const uploadsBlocking = [...imageUploads, ...attachmentUploads].some((item) =>
    isBlockingStagedItem(item, serverKey),
  );

  // Retirement marks the two deliberate exits (explicit discard, successful
  // creation); any other unmount is a detach that captures the draft.
  const retiredRef = useRef(false);
  const retire = useCallback(() => {
    retiredRef.current = true;
  }, []);
  const handleCreated = useCallback(
    (created: { featureId: string; name: string }) => {
      retire();
      onCreated(created);
    },
    [onCreated, retire],
  );
  const handleClose = useCallback(() => {
    retire();
    onClose();
  }, [onClose, retire]);

  /** Unsaved work worth confirming before it is thrown away. */
  const dirty =
    name.trim() !== '' ||
    description !== '' ||
    repoSelections.length > 0 ||
    images.length > 0 ||
    attachments.length > 0 ||
    imageUploads.length > 0 ||
    attachmentUploads.length > 0;

  // Catalog + reconciled selections. `repositories` is the current catalog;
  // reconciliation by identity is derived so every catalog change (initial
  // load, folder adoption, SSE refresh) re-reconciles selections without
  // touching any other draft value.
  const loadedDefaultsEarly = state.phase === 'loaded' ? state.defaults : null;
  const repositories = loadedDefaultsEarly?.repositories ?? EMPTY_REPOSITORIES;
  const reconciledSelections = useMemo(
    () => reconcileRepoSelections(repoSelections, repositories),
    [repoSelections, repositories],
  );
  const selectedKeys = useMemo(
    () => resolvedSelectionKeys(reconciledSelections),
    [reconciledSelections],
  );
  /** Resolved selections as the identity-bound @-mention search scope. */
  const searchRepositories = useMemo(
    () =>
      reconciledSelections.flatMap((selection) =>
        selection.status === 'selected'
          ? [{ key: selection.key, identity: selection.identity }]
          : [],
      ),
    [reconciledSelections],
  );
  const unresolvedSelections = useMemo(
    () => repoSelections.filter((_, index) => reconciledSelections[index]?.status === 'unresolved'),
    [repoSelections, reconciledSelections],
  );

  const requestCancel = useCallback(() => {
    if (dirty) {
      setDiscardOpen(true);
      return;
    }
    handleClose();
  }, [dirty, handleClose]);

  // Escape routes to the same Cancel path; the hook's nested-dialog bail
  // leaves Escape to the consent and discard dialogs while either is open.
  useModalDismiss(sheetRef, requestCancel);

  const loadInitialDefaults = useCallback(() => {
    setState({ phase: 'loading' });
    window.agentico
      .getCreationDefaults()
      .then((defaults) => {
        setState({ phase: 'loaded', defaults });
        setCloneableRoots(defaults.workspaceRoots);
        setUseCurrentBranch(defaults.defaults.useCurrentBranch);
        if (isPipeline(defaults.defaults.pipeline)) {
          setPipeline(defaults.defaults.pipeline);
          setCheckpoints(checkpointsForPipeline(defaults.defaults.pipeline));
        }
        setInquireness(normalizeInquireness(defaults.defaults.inquireness));
      })
      .catch((err: unknown) => setState({ phase: 'error', error: parseIpcError(err) }));
  }, []);

  /**
   * Applies one authoritative readiness snapshot to the live draft: the
   * catalog (and with it every selection) reconciles by identity, attachment
   * references follow their repositories, and the destination controls see
   * the fresh root list — all without resetting any draft value.
   */
  const applyCatalogSnapshot = useCallback((snapshot: ReadinessSnapshot) => {
    setState((current) =>
      current.phase === 'loaded'
        ? {
            phase: 'loaded',
            defaults: { ...current.defaults, repositories: [...snapshot.repositories] },
          }
        : current,
    );
    // Attachment references follow their repository's current key by
    // identity; their relative paths and the description text are kept.
    setRepositoryFiles((files) => [...reconcileRepositoryFiles(files, snapshot.repositories)]);
    setCloneableRoots(snapshot.workspaceRoots);
    setWorkspaceRoots(snapshot.workspaceRoots.map((root) => root.path));
  }, []);

  /**
   * Discovery refreshes reconcile the catalog (and with it every selection)
   * by identity, without resetting any draft value. Requests are fenced by
   * sequence so a stale reply cannot masquerade as a newer catalog, and a
   * failed refresh keeps the last authoritative catalog instead of adopting
   * an empty one.
   */
  const refreshCatalog = useCallback(() => {
    const seq = ++catalogRefreshSeq.current;
    void window.agentico
      .getReadiness()
      .then((snapshot) => {
        if (seq !== catalogRefreshSeq.current) return;
        setCatalogRefreshError(null);
        applyCatalogSnapshot(snapshot);
      })
      .catch((err: unknown) => {
        if (seq !== catalogRefreshSeq.current) return;
        setCatalogRefreshError(parseIpcError(err));
      });
  }, [applyCatalogSnapshot]);

  useEffect(() => {
    const unsub = window.agentico.onAppEvent((event) => {
      if (event.type !== 'invalidated') return;
      // Any runtime configuration change can reshape discovery (roots,
      // explicit registrations, clone publication); a resync replays the
      // whole stream, so the catalog is re-read then too.
      if (event.kind === 'resync' || event.kind.startsWith('config')) refreshCatalog();
    });
    return unsub;
  }, [refreshCatalog]);

  /**
   * Resolves the draft's associated clone operation from the authoritative
   * server state: by id when the start was observed, otherwise by
   * idempotency key (a lost acceptance). Late replies cannot replace a
   * newer snapshot, and a failed refresh never drops the association.
   */
  const resolveCloneOperation = useCallback(() => {
    const association = cloneAssociationRef.current;
    if (association === null) {
      setCloneOperation(null);
      return;
    }
    // While the start request itself is in flight, a keyed lookup must not
    // conclude anything: the server may simply not have accepted yet.
    if (association.operationId === null && association.startInFlight) return;
    const seq = ++cloneResolveSeq.current;
    const found: Promise<CloneOperation | null | 'unavailable'> =
      association.operationId !== null
        ? window.agentico
            .getCloneOperation(association.operationId)
            .catch(() => 'unavailable' as const)
        : window.agentico
            .listCloneOperations()
            .then(
              (list) =>
                list.operations.find(
                  (candidate) => candidate.idempotencyKey === association.idempotencyKey,
                ) ?? null,
            )
            .catch(() => 'unavailable' as const);
    void found.then((snapshot) => {
      if (seq !== cloneResolveSeq.current) return;
      if (snapshot === 'unavailable') return;
      if (snapshot === null) {
        if (association.operationId === null) {
          // A keyed lookup that found nothing proves no operation was ever
          // accepted: the association is stale, not in flight.
          setCloneAssociation(null);
          setCloneOperation(null);
          return;
        }
        // A known-id lookup that misses this instant keeps the last known
        // snapshot: the operation is merely unavailable right now.
        return;
      }
      if (association.operationId === null) {
        // A lost acceptance recovered by idempotency key: persist the
        // operation id so later refreshes resolve directly.
        setCloneAssociation({
          idempotencyKey: association.idempotencyKey,
          operationId: snapshot.id,
          adopted: association.adopted,
          startInFlight: false,
        });
      }
      setCloneOperation(snapshot);
    });
  }, []);

  // The association is tracked whether or not the clone view is open:
  // background completion adopts into this draft from any wizard step,
  // across Close, and after a restore — but only while the draft lives.
  const cloneAssociationRef = useRef(cloneAssociation);
  useEffect(() => {
    cloneAssociationRef.current = cloneAssociation;
  }, [cloneAssociation]);
  useEffect(() => {
    if (cloneAssociation === null) {
      setCloneOperation(null);
      return;
    }
    resolveCloneOperation();
    const unsub = window.agentico.onAppEvent((event) => {
      if (event.type !== 'invalidated') return;
      if (
        event.kind === 'resync' ||
        event.kind.startsWith('clone.') ||
        event.kind.startsWith('config')
      ) {
        resolveCloneOperation();
      }
    });
    return unsub;
  }, [cloneAssociation, resolveCloneOperation]);

  // A restored draft re-reads its catalog before applying anything
  // asynchronous; a fresh draft loads its creation defaults once. Server
  // defaults are never re-applied over a restored draft's user-owned values.
  useEffect(() => {
    if (retained?.restored === true && retained.defaultsState.phase === 'loaded') {
      refreshCatalog();
      return;
    }
    // The load identity is the mount itself: a restored draft never
    // re-applies defaults, and no later render re-triggers the load.
    loadInitialDefaults();
  }, []);

  // The live draft is captured on every render so an unmount that is not a
  // retirement (a disconnect or a server switch) retains the whole draft.
  const snapshotRef = useRef<CreationDraftState | null>(null);
  useEffect(() => {
    snapshotRef.current = {
      restored: true,
      creationKey: creationKey.current,
      defaultsState: state,
      stepIndex,
      name,
      description,
      repoSelections: [...repoSelections],
      repoQuery,
      useCurrentBranch,
      pipeline,
      checkpoints,
      modelChoices,
      effortChoices,
      riskLevel,
      inquireness,
      exitCriteria,
      images: [...images],
      attachments: [...attachments],
      imageUploads: [...imageUploads],
      attachmentUploads: [...attachmentUploads],
      repositoryFiles: [...repositoryFiles],
      autoStart,
      folderCandidate,
      folderHoldsNoRepository,
      folderNotice,
      workspaceRoots: [...workspaceRoots],
      consentOpen,
      discardOpen,
      folderDraft,
      folderError,
      nameError,
      repoError,
      formError,
      catalogRefreshError,
      cloneOpen,
      cloneAssociation: cloneAssociation === null ? null : { ...cloneAssociation },
      cloneableRoots: [...cloneableRoots],
      createOpen,
      pendingCreate: pendingCreate === null ? null : { ...pendingCreate },
      pendingInitialize: pendingInitialize === null ? null : { ...pendingInitialize },
    };
  });
  const detachRef = useRef(onDraftDetach);
  useEffect(() => {
    detachRef.current = onDraftDetach;
  }, [onDraftDetach]);
  useEffect(
    () => () => {
      if (retiredRef.current || snapshotRef.current === null) return;
      // A detach only retains the draft when a store is listening.
      detachRef.current?.(retainableDraft(snapshotRef.current));
    },
    [],
  );

  /**
   * A usable clone success adopts into exactly this draft, once: the
   * published identity is selected under its current catalog key after the
   * catalog has refreshed to include it. An unborn result or a publication
   * without provable identity stays visible without selection, and nothing
   * is adopted by a key or path fallback.
   */
  useEffect(() => {
    if (cloneOperation === null || cloneOperation.state !== 'succeeded') return;
    if (cloneAssociation === null || cloneAssociation.adopted) return;
    // Only the draft's own associated operation adopts: a snapshot that
    // belongs to another draft or to Settings stays visible without ever
    // selecting anything here.
    if (cloneOperation.idempotencyKey !== cloneAssociation.idempotencyKey) return;
    const published = cloneOperation.published;
    if (published === undefined || !published.hasHead || published.identity === undefined) return;
    const identity = published.identity;
    // Adoption requires the succeeded snapshot plus the repository still
    // being the published one and feature-ready: stream completion alone,
    // a replacement, or an unborn result never selects anything.
    const match = repositories.find(
      (repo) =>
        repo.valid &&
        repo.featureReady &&
        repo.identity !== undefined &&
        sameRepoIdentity(repo.identity, identity),
    );
    if (match === undefined || match.identity === undefined) return;
    const matchIdentity = match.identity;
    // The association has served its purpose: retiring it with the adoption
    // makes the exactly-once guarantee structural — no later invalidation
    // can reselect, and the clone view is free to start a fresh clone.
    setCloneAssociation(null);
    setRepoSelections((current) =>
      current.some((selection) => sameRepoIdentity(selection.identity, matchIdentity))
        ? current
        : [...current, { key: match.name, identity: matchIdentity }],
    );
    // A search filter cannot hide the focus target.
    setRepoQuery('');
    setFocusRepoKey(match.name);
    setCloneOpen(false);
    setFolderNotice(`Cloned ${match.name} and selected it.`);
    setRepoError(null);
  }, [cloneOperation, cloneAssociation, repositories]);

  /**
   * A picker-initiated creation adopts into exactly this draft, once: the
   * pending marker is recorded before the request flies, and once the
   * server-resolved identity returns, the repository is selected under its
   * current catalog key after the catalog has refreshed to include it. A
   * result without provable identity never selects anything, and a
   * discarded draft (or a server switch that remounts the sheet) can never
   * adopt a late completion.
   */
  const serverKeyRef = useRef<string | null>(serverKey);
  const serverGenerationRef = useRef(0);
  useEffect(() => {
    if (serverKeyRef.current !== serverKey) {
      serverGenerationRef.current += 1;
      initializeRequestSeq.current += 1;
      setPendingInitialize(null);
      setInitializeError(null);
    }
    serverKeyRef.current = serverKey;
  }, [serverKey]);

  useEffect(() => {
    const repositories = reconciledSelections.flatMap((selection) =>
      selection.status === 'selected'
        ? [{ repoKey: selection.key, identity: selection.identity }]
        : [],
    );
    const request = ++sourceRequestSeq.current;
    if (repositories.length === 0 || unresolvedSelections.length > 0) {
      setSourceState({ phase: 'idle' });
      return;
    }
    const requestedServer = serverKey;
    setSourceState({ phase: 'loading' });
    void window.agentico
      .inspectRepositorySources({
        mode: useCurrentBranch ? 'current' : 'default',
        repositories,
      })
      .then(
        (value) => {
          if (sourceRequestSeq.current === request && serverKeyRef.current === requestedServer) {
            setSourceState({ phase: 'loaded', value });
          }
        },
        (error: unknown) => {
          if (sourceRequestSeq.current === request && serverKeyRef.current === requestedServer) {
            setSourceState({ phase: 'error', error: parseIpcError(error) });
          }
        },
      );
  }, [reconciledSelections, serverKey, unresolvedSelections.length, useCurrentBranch]);
  const handleCreate = useCallback(
    (input: CreateRepositoryStartInput) => {
      const startServerKey = serverKeyRef.current;
      // The marker precedes the flight: a lost response still adopts from
      // the refreshed catalog, and closing the view never discards it.
      setPendingCreate({ idempotencyKey: input.idempotencyKey, identity: null });
      return window.agentico.createRepository(input).then(
        (result) => {
          // A completion from another server (or after a remount) is not
          // this draft's result: the new server's UI stays untouched.
          if (serverKeyRef.current !== startServerKey) {
            return result;
          }
          if (result.identity !== undefined) {
            setPendingCreate({ idempotencyKey: input.idempotencyKey, identity: result.identity });
          } else {
            // No provable identity (a replayed success whose destination
            // was replaced): visible success, never adopted by key or path.
            setPendingCreate(null);
            setFolderNotice(`Created ${result.repoKey}; select it from the list once it appears.`);
          }
          // The catalog refresh reconciles the new repository (and every
          // selection) by identity; adoption follows from it.
          refreshCatalog();
          return result;
        },
        (err: unknown) => {
          if (serverKeyRef.current !== null && serverKeyRef.current === startServerKey) {
            // The attempt is over for this draft; the form keeps its
            // inputs and shows the canonical rejection.
            setPendingCreate(null);
          }
          throw err;
        },
      );
    },
    [refreshCatalog],
  );

  useEffect(() => {
    if (pendingCreate === null || pendingCreate.identity === null) return;
    const identity = pendingCreate.identity;
    // Adoption requires the published repository to still be the published
    // one and feature-ready in the authoritative catalog.
    const match = repositories.find(
      (repo) =>
        repo.valid &&
        repo.featureReady &&
        repo.identity !== undefined &&
        sameRepoIdentity(repo.identity, identity),
    );
    if (match === undefined || match.identity === undefined) return;
    const matchIdentity = match.identity;
    // Exactly-once is structural: the marker retires with the adoption.
    setPendingCreate(null);
    setRepoSelections((current) =>
      current.some((selection) => sameRepoIdentity(selection.identity, matchIdentity))
        ? current
        : [...current, { key: match.name, identity: matchIdentity }],
    );
    // A search filter cannot hide the focus target.
    setRepoQuery('');
    setFocusRepoKey(match.name);
    setCreateOpen(false);
    setFolderNotice(`Created ${match.name} and selected it.`);
    setRepoError(null);
  }, [pendingCreate, repositories]);

  /**
   * The shared explicit-initialization action, used by both picker entry
   * points: the unborn clone-success offer and the later opt-in beside an
   * unborn catalog row. The pending marker is recorded before the request
   * flies (it also suppresses duplicate actions for that repository); a
   * completion from another server is discarded silently, and a rejection
   * is reconciled by a fresh authoritative read — never by repeating the
   * mutation. Both result values are successes; adoption follows from the
   * refreshed catalog.
   */
  const handleInitialize = useCallback(
    (target: { repoKey: string; identity: RepositoryIdentity; path: string }) => {
      const startServerKey = serverKeyRef.current;
      const startServerGeneration = serverGenerationRef.current;
      const requestSeq = ++initializeRequestSeq.current;
      const isCurrentRequest = (): boolean =>
        requestSeq === initializeRequestSeq.current &&
        startServerGeneration === serverGenerationRef.current &&
        startServerKey === serverKeyRef.current;
      const reconcile = (
        identity: RepositoryIdentity,
        resultRepoKey: string,
        failure: CanonicalError | null,
      ): void => {
        void window.agentico.getReadiness().then(
          (snapshot) => {
            if (!isCurrentRequest()) return;
            applyCatalogSnapshot(snapshot);
            const ready = snapshot.repositories.some(
              (repo) =>
                repo.valid &&
                repo.featureReady &&
                repo.identity !== undefined &&
                sameRepoIdentity(repo.identity, identity),
            );
            if (ready) {
              setInitializeError(null);
              setPendingInitialize({ repoKey: resultRepoKey, identity });
              return;
            }
            setPendingInitialize(null);
            if (failure !== null) {
              setInitializeError(failure);
            } else {
              setFolderNotice(
                `Initialized ${resultRepoKey}, but the repository changed or is no longer available. Reselect it from the current list.`,
              );
            }
          },
          (readError: unknown) => {
            if (!isCurrentRequest()) return;
            // The outcome is still uncertain. Keep the identity-bound
            // marker so another mutation cannot be offered until a later
            // authoritative catalog refresh can prove adoption.
            setPendingInitialize({ repoKey: resultRepoKey, identity });
            setInitializeError(failure ?? parseIpcError(readError));
          },
        );
      };
      setPendingInitialize({ repoKey: target.repoKey, identity: null });
      setInitializeError(null);
      return window.agentico
        .initializeRepository({
          repoKey: target.repoKey,
          identity: target.identity,
          path: target.path,
          consent: true,
        })
        .then(
          (result) => {
            // A completion from another server (or after a remount) is not
            // this draft's result: the new server's UI stays untouched.
            if (!isCurrentRequest()) {
              return result;
            }
            if (result.identity !== undefined) {
              reconcile(result.identity, result.repoKey, null);
            } else {
              // No provable identity: visible success, never adopted by
              // key or path.
              setPendingInitialize(null);
              setFolderNotice(
                `Initialized ${result.repoKey}; select it from the list once it appears.`,
              );
            }
            return result;
          },
          (err: unknown) => {
            if (serverKeyRef.current !== null && isCurrentRequest()) {
              // The attempt is over for this draft; the canonical
              // rejection stays scoped to the repository action.
              const parsed = parseIpcError(err);
              // A lost response is reconciled through a fresh
              // authoritative read; the mutation is never repeated
              // automatically.
              if (parsed.code !== 'E_SERVER_SWITCHED') {
                reconcile(target.identity, target.repoKey, parsed);
              } else {
                setPendingInitialize(null);
              }
            }
            throw err;
          },
        );
    },
    [applyCatalogSnapshot],
  );

  /** Duplicate initialize actions for one repository are suppressed while pending. */
  const initializeSuppressed = useCallback(
    (repo: RepositoryState): boolean => {
      if (pendingInitialize === null) return false;
      if (pendingInitialize.identity === null) return pendingInitialize.repoKey === repo.name;
      return (
        repo.identity !== undefined && sameRepoIdentity(pendingInitialize.identity, repo.identity)
      );
    },
    [pendingInitialize],
  );

  /**
   * A picker-initiated initialization adopts into exactly this draft,
   * once: the pending marker carries the server-resolved identity of the
   * refreshed repository, and the repository is selected under its current
   * catalog key only after current readiness proves the same repository
   * feature-ready. The historical clone record may still report its
   * publication as unborn — that flag never blocks adoption.
   */
  useEffect(() => {
    if (pendingInitialize === null || pendingInitialize.identity === null) return;
    const identity = pendingInitialize.identity;
    const match = repositories.find(
      (repo) =>
        repo.valid &&
        repo.featureReady &&
        repo.identity !== undefined &&
        sameRepoIdentity(repo.identity, identity),
    );
    if (match === undefined || match.identity === undefined) return;
    const matchIdentity = match.identity;
    // Exactly-once is structural: the marker retires with the adoption.
    setPendingInitialize(null);
    // The terminal clone association is historical once its repository is
    // initialized and adopted. Retire it so a later Clone action opens a
    // fresh form instead of reviving the unborn success card.
    setCloneAssociation(null);
    setRepoSelections((current) =>
      current.some((selection) => sameRepoIdentity(selection.identity, matchIdentity))
        ? current
        : [...current, { key: match.name, identity: matchIdentity }],
    );
    // A search filter cannot hide the focus target.
    setRepoQuery('');
    setFocusRepoKey(match.name);
    setCloneOpen(false);
    setInitializeExpandedKey(null);
    setFolderNotice(`Initialized ${match.name} and selected it.`);
    setRepoError(null);
  }, [pendingInitialize, repositories]);

  /**
   * The initialize offer for the associated unborn clone success. The
   * historical publication flag drives visibility; the pending state and
   * the draft adoption stay with this sheet.
   */
  const cloneInitializeOffer = useMemo<InitializeOfferController | null>(() => {
    if (cloneOperation === null || cloneOperation.state !== 'succeeded') return null;
    const published = cloneOperation.published;
    if (published === undefined || published.hasHead) return null;
    const identity = published.identity;
    if (identity === undefined) return null;
    if (declinedInitializeOperations.has(cloneOperation.id)) return null;
    const pending =
      pendingInitialize !== null &&
      (pendingInitialize.identity === null
        ? pendingInitialize.repoKey === published.repoKey
        : sameRepoIdentity(pendingInitialize.identity, identity));
    return {
      pending,
      error: initializeError,
      onInitialize: () => {
        // The rejection is already surfaced as the scoped offer error;
        // this catch only keeps the discarded promise quiet.
        void handleInitialize({ repoKey: published.repoKey, identity, path: published.path }).catch(
          () => undefined,
        );
      },
      onDecline: () =>
        setDeclinedInitializeOperations((current) => new Set([...current, cloneOperation.id])),
    };
  }, [
    cloneOperation,
    declinedInitializeOperations,
    pendingInitialize,
    initializeError,
    handleInitialize,
  ]);

  /**
   * Pending row focus lands only while the picker step is actually visible,
   * so an adoption that completes elsewhere never steals focus.
   */
  useEffect(() => {
    if (focusRepoKey === null || cloneOpen || createOpen || stepIndex !== 0) return;
    const group = repoGroupRef.current;
    if (group === null) return;
    const row = Array.from(group.querySelectorAll<HTMLElement>('[data-repo-key]'))
      .find((candidate) => candidate.dataset.repoKey === focusRepoKey)
      ?.querySelector('.creation-sheet__row-control');
    if (!(row instanceof HTMLInputElement)) return;
    (row as HTMLInputElement).focus();
    setFocusRepoKey(null);
  }, [focusRepoKey, cloneOpen, createOpen, stepIndex, repositories, repoQuery]);

  // Field errors are announced by moving focus to the control that must
  // change — from an effect, so a submit-time error that first has to jump
  // back to an earlier step focuses the field once that step has rendered.
  useEffect(() => {
    if (formError !== null) formErrorRef.current?.focus();
  }, [formError]);
  useEffect(() => {
    if (nameError !== null) nameRef.current?.focus();
  }, [nameError]);
  useEffect(() => {
    if (repoError !== null) repoGroupRef.current?.focus();
  }, [repoError]);

  const browseDirectory = async (): Promise<void> => {
    try {
      const picked = await window.agentico.pickWorkspaceDirectory();
      if (picked.path !== null) {
        setFolderCandidate(picked.path);
        setFolderHoldsNoRepository(false);
        setFolderNotice('');
      }
    } catch (err) {
      setFormError(parseIpcError(err));
    }
  };

  /**
   * Remote-only candidate entry: the folder lives on the server host, so the
   * typed path is adopted as-is and the server's own filesystem check (the
   * tightened PATCH on workspace roots) is the validation gate.
   */
  const checkTypedFolder = (): void => {
    const folder = folderDraft.trim();
    if (folder === '') return;
    if (!folder.startsWith('/')) {
      setFolderError('Enter the path exactly as the server sees it, starting with /.');
      return;
    }
    setFolderCandidate(folder);
    setFolderHoldsNoRepository(false);
    setFolderError(null);
    setFolderNotice('');
  };

  /** Adopts a snapshot's workspace view and selects whatever it discovered. */
  const adoptSnapshot = (snapshot: {
    repositories: readonly RepositoryState[];
    workspaceRoots: readonly WorkspaceRootState[];
  }): readonly RepositoryState[] => {
    setState((current) =>
      current.phase === 'loaded'
        ? {
            phase: 'loaded',
            defaults: { ...current.defaults, repositories: [...snapshot.repositories] },
          }
        : current,
    );
    setRepositoryFiles((files) => [...reconcileRepositoryFiles(files, snapshot.repositories)]);
    setCloneableRoots(snapshot.workspaceRoots);
    setWorkspaceRoots(snapshot.workspaceRoots.map((root) => root.path));
    return snapshot.repositories;
  };

  /** An unambiguous discovery selects itself; several stay for the user. */
  const selectDiscovered = (discovered: readonly RepositoryState[]): void => {
    const only = discovered.length === 1 ? discovered[0] : undefined;
    if (only === undefined || !isSelectableRepository(only)) return;
    const identity = only.identity;
    if (identity === undefined) return;
    setRepoSelections((current) =>
      current.some((selection) => sameRepoIdentity(selection.identity, identity))
        ? current
        : [...current, { key: only.name, identity }],
    );
    setRepoError(null);
  };

  /**
   * One verb for any folder: adding it as a workspace root covers both an
   * existing repository (discovery registers a root that is one) and a folder
   * that holds several. Only when it yields none does initialization become
   * the remaining option.
   */
  const useFolder = async (): Promise<void> => {
    const folder = folderCandidate;
    if (folder === null) return;
    setFolderPending(true);
    setFormError(null);
    try {
      const discovered = repositoriesWithin(
        adoptSnapshot(await window.agentico.addWorkspaceRoot(folder)),
        folder,
      );
      if (discovered.length === 0) {
        setFolderHoldsNoRepository(true);
        setFolderNotice(
          'Added as a workspace root, but it holds no git repository yet. Initialize it, ' +
            (remoteServer ? 'or type a different folder.' : 'or browse for a different folder.'),
        );
        return;
      }
      selectDiscovered(discovered);
      setFolderCandidate(null);
      setFolderDraft('');
      setFolderNotice(
        discovered.length === 1
          ? `Added ${discovered[0]?.name} and selected it.`
          : `Added a workspace root holding ${discovered.length} repositories.`,
      );
    } catch (err) {
      setFormError(parseIpcError(err));
    } finally {
      setFolderPending(false);
    }
  };

  /**
   * Server-owned `git init`. The server only initializes a folder strictly
   * inside a configured root, so the parent is configured for the call and
   * dropped again afterwards — the folder itself stays the root, and once it
   * is a repository discovery registers it without the parent's siblings.
   */
  const initializeFolder = async (): Promise<void> => {
    const folder = folderCandidate;
    if (folder === null) return;
    const parent = parentDirectory(folder);
    if (parent === null) {
      setConsentOpen(false);
      setFormError(
        buildCanonicalError('E_INVALID_PATH', {
          remediationHint:
            'Choose a folder inside another directory to initialize it as a repository.',
        }),
      );
      return;
    }
    const parentAlreadyRoot = workspaceRoots.includes(parent);
    setFolderPending(true);
    setFormError(null);
    try {
      if (!parentAlreadyRoot) await window.agentico.addWorkspaceRoot(parent);
      let snapshot = await window.agentico.initRepository({ path: folder, consent: true });
      if (!parentAlreadyRoot) {
        snapshot = await window.agentico.removeWorkspaceRoot(parent).catch(() => snapshot);
      }
      const initialized = repositoriesWithin(adoptSnapshot(snapshot), folder);
      selectDiscovered(initialized);
      setFolderCandidate(null);
      setFolderHoldsNoRepository(false);
      setFolderNotice(
        initialized.length === 0
          ? 'Initialized the folder; the runtime has not discovered it yet.'
          : `Initialized ${initialized[0]?.name} and selected it.`,
      );
    } catch (err) {
      setFormError(parseIpcError(err));
      if (!parentAlreadyRoot) {
        await window.agentico
          .removeWorkspaceRoot(parent)
          .then(adoptSnapshot)
          .catch(() => undefined);
      }
    } finally {
      setConsentOpen(false);
      setFolderPending(false);
    }
  };

  const validateStep = (index: number): boolean => {
    setNameError(null);
    setRepoError(null);
    if (index === 0) {
      if (hasUnresolvedSelection(reconciledSelections)) {
        setRepoError(
          'Resolve or remove the repositories marked as needing reselection before continuing.',
        );
        return false;
      }
      if (selectedKeys.length === 0) {
        setRepoError('Select at least one repository.');
        return false;
      }
      if (sourceState.phase !== 'loaded') {
        setRepoError(
          sourceState.phase === 'loading'
            ? 'Wait for the selected repository sources to finish loading.'
            : 'Refresh or reselect repositories whose local source could not be resolved.',
        );
        return false;
      }
    }
    if (index === 1 && name.trim() === '') {
      setNameError('Enter a feature name.');
      return false;
    }
    return true;
  };

  const next = (): void => {
    if (validateStep(stepIndex)) setStepIndex((current) => Math.min(current + 1, 3));
  };

  const submit = (event: FormEvent): void => {
    event.preventDefault();
    if (pending || uploadsBlocking || state.phase !== 'loaded') return;
    if (!validateStep(0)) {
      setStepIndex(0);
      return;
    }
    if (!validateStep(1)) {
      setStepIndex(1);
      return;
    }
    setFormError(null);
    setPending(true);
    const models: Record<string, string> = {};
    const effort: Record<string, EffortLevel> = {};
    for (const field of applicablePhaseFields(pipeline, false)) {
      const chosen = modelChoices[field.key] ?? '';
      if (chosen !== '') models[modelConfigKey(field.key)] = chosen;
      const chosenEffort = effortChoices[field.key];
      if (chosenEffort !== undefined) effort[modelConfigKey(field.key)] = chosenEffort;
    }
    const submittedGates = applicableGates(pipeline);
    void (async () => {
      try {
        const createdImageRefs = submittableReferences(imageUploads, 'image', serverKey);
        const createdAttachmentRefs = submittableReferences(
          attachmentUploads,
          'attachment',
          serverKey,
        );
        const created = await window.agentico.createFeature({
          name: name.trim(),
          description,
          repoKeys: [...selectedKeys],
          useCurrentBranch,
          repositorySources:
            sourceState.phase === 'loaded' ? [...sourceState.value.repositories] : [],
          images: [...images],
          attachments: [...attachments],
          ...(createdImageRefs.length === 0 ? {} : { imageUploads: createdImageRefs }),
          ...(createdAttachmentRefs.length === 0
            ? {}
            : { attachmentUploads: createdAttachmentRefs }),
          repositoryFiles: [...repositoryFiles],
          pipeline,
          riskLevel,
          inquireness,
          exitCriteria,
          models,
          effort,
          checkpoints: {
            inquiryReview: submittedGates.has('inquiryReview') && checkpoints.inquiryReview,
            researchReview: submittedGates.has('researchReview') && checkpoints.researchReview,
            designReview: submittedGates.has('designReview') && checkpoints.designReview,
            roadmapReview: submittedGates.has('roadmapReview') && checkpoints.roadmapReview,
            phasePlanReview: submittedGates.has('phasePlanReview') && checkpoints.phasePlanReview,
            manualPublish: submittedGates.has('manualPublish') && checkpoints.manualPublish,
            draftPublish: checkpoints.draftPublish,
          },
          idempotencyKey: creationKey.current,
        });
        if (autoStart) {
          try {
            // Auto-start: creation flows straight into setup + orchestration.
            await window.agentico.dispatchFeatureAction({
              featureId: created.featureId,
              action: 'start',
            });
          } catch {
            /* cockpit owns retry */
          }
        } else {
          try {
            await window.agentico.dispatchFeatureSetup(created.featureId);
          } catch {
            /* cockpit owns retry */
          }
        }
        handleCreated({ featureId: created.featureId, name: name.trim() });
      } catch (err) {
        const parsed = parseIpcError(err);
        // An unresolved repository-file reference keeps the editable draft
        // and surfaces where the reference chips are visible.
        if (parsed.code === 'E_REPOSITORY_FILE_UNRESOLVED') {
          setStepIndex(1);
          setFormError(parsed);
          return;
        }
        const field = fieldForCreationError(parsed);
        if (field === 'name') {
          setStepIndex(1);
          setNameError(parsed.summary);
        } else if (field === 'repos') {
          setStepIndex(0);
          setRepoError(parsed.summary);
        } else setFormError(parsed);
      } finally {
        setPending(false);
      }
    })();
  };

  const loadedDefaults = loadedDefaultsEarly;
  const filteredRepositories = useMemo(() => {
    const query = repoQuery.trim().toLowerCase();
    if (query === '') return repositories;
    return repositories.filter(
      (repo) => repo.name.toLowerCase().includes(query) || repo.path.toLowerCase().includes(query),
    );
  }, [repoQuery, repositories]);

  const currentStep = STEPS[stepIndex] as Step;
  const gates = applicableGates(pipeline);
  const visibleGates = GATE_FIELDS.filter((gate) => gates.has(gate.key));
  const modelDefaults = loadedDefaults === null ? {} : defaultModelsByKey(loadedDefaults);
  const effortDefaults = loadedDefaults === null ? {} : defaultEffortByKey(loadedDefaults);
  const checkedCheckpoints = visibleGates.filter((gate) => checkpoints[gate.key]).length;

  /** The three depth profiles; compact on Contract, where depth is confirmed. */
  const depthProfiles = (variant: 'full' | 'compact') => (
    <div className="creation-sheet__profiles" data-variant={variant}>
      {PIPELINES.map((profile) => (
        <label
          key={profile.id}
          className="creation-sheet__profile"
          data-selected={pipeline === profile.id}
        >
          <input
            type="radio"
            name="pipeline"
            checked={pipeline === profile.id}
            onChange={() => {
              setPipeline(profile.id);
              setCheckpoints(checkpointsForPipeline(profile.id));
            }}
          />
          <span className="creation-sheet__profile-body">
            <b className="creation-sheet__profile-title">{profile.title}</b>
            <span className="creation-sheet__profile-note">{profile.note}</span>
            {variant === 'full' ? (
              <small className="creation-sheet__profile-gates">
                {checkpointSummary(profile.id, profile.checkpoints)}
              </small>
            ) : null}
          </span>
        </label>
      ))}
    </div>
  );

  return (
    <div className="sheet-scrim creation-sheet__scrim">
      <div
        ref={sheetRef}
        role="dialog"
        aria-modal="true"
        aria-label="New feature"
        className="sheet creation-sheet"
        data-width={currentStep === 'Contract' ? 'wide' : 'default'}
        tabIndex={-1}
      >
        {loadedDefaults === null ? null : (
          <nav className="creation-sheet__rail" aria-label="Creation steps">
            {STEPS.map((step, index) => {
              const railState =
                index < stepIndex ? 'done' : index === stepIndex ? 'current' : 'upcoming';
              return (
                <button
                  key={step}
                  type="button"
                  className="creation-sheet__rail-step"
                  data-state={railState}
                  aria-current={index === stepIndex ? 'step' : undefined}
                  disabled={index > stepIndex}
                  onClick={() => setStepIndex(index)}
                >
                  {railState === 'done' ? (
                    <span className="creation-sheet__rail-check" aria-hidden="true">
                      ✓
                    </span>
                  ) : null}
                  <span className="creation-sheet__rail-label">{step}</span>
                </button>
              );
            })}
          </nav>
        )}

        <form
          className="creation-sheet__form"
          aria-label="Create a feature"
          noValidate
          onSubmit={submit}
        >
          <div className="sheet__body creation-sheet__body">
            {state.phase === 'loading' ? (
              <p role="status" className="creation-sheet__status">
                Loading creation defaults from the runtime…
              </p>
            ) : state.phase === 'error' ? (
              <ErrorSurface
                error={state.error}
                variant="compact"
                localAction={retryAction(loadInitialDefaults)}
              />
            ) : (
              <>
                {formError !== null ? (
                  <ErrorSurface
                    error={formError}
                    variant="compact"
                    rootRef={formErrorRef}
                    rootTabIndex={-1}
                  />
                ) : null}

                {currentStep === 'Repositories' ? (
                  <section className="creation-sheet__step" aria-labelledby="creation-repositories">
                    <h2 id="creation-repositories" className="creation-sheet__heading">
                      Choose repositories
                    </h2>
                    {catalogRefreshError !== null ? (
                      <ErrorSurface
                        error={catalogRefreshError}
                        variant="compact"
                        localAction={retryAction(refreshCatalog)}
                      />
                    ) : null}
                    {repositories.length > 0 ? (
                      <label className="creation-sheet__field">
                        <span className="creation-sheet__field-label">Search repositories</span>
                        <input
                          className="creation-sheet__input"
                          type="search"
                          value={repoQuery}
                          placeholder="Filter by name or path"
                          onChange={(event) => setRepoQuery(event.target.value)}
                        />
                      </label>
                    ) : null}
                    <fieldset
                      ref={repoGroupRef}
                      tabIndex={-1}
                      className="creation-sheet__group"
                      aria-invalid={fieldAriaInvalid(repoError !== null)}
                      aria-describedby={fieldAriaDescribedBy(
                        'creation-repositories-error',
                        repoError !== null,
                      )}
                    >
                      <legend className="creation-sheet__group-label">
                        {repositories.length === 0
                          ? 'No repositories yet'
                          : 'Fresh workspace discovery'}
                      </legend>
                      {repositories.length === 0 && unresolvedSelections.length === 0 ? (
                        <p className="creation-sheet__group-desc">
                          Point Agentico at a folder below: an existing repository, a folder that
                          holds several, or an empty folder to start something new.
                        </p>
                      ) : (
                        <ul className="creation-sheet__rows">
                          {unresolvedSelections.map((selection) => (
                            <li
                              key={`unresolved:${selection.key}`}
                              className="creation-sheet__row-item"
                            >
                              <div
                                className="creation-sheet__row"
                                data-valid={false}
                                data-unresolved="true"
                              >
                                <span className="creation-sheet__row-body">
                                  <b className="creation-sheet__row-name">{selection.key}</b>
                                  <span className="creation-sheet__row-issue">
                                    Needs reselection — this repository is no longer available on
                                    the server. Reselect it below, or remove it.
                                  </span>
                                </span>
                                <button
                                  type="button"
                                  className="creation-sheet__row-control creation-sheet__button"
                                  disabled={pending}
                                  onClick={() => {
                                    setRepoSelections((current) =>
                                      current.filter(
                                        (item) =>
                                          !sameRepoIdentity(item.identity, selection.identity),
                                      ),
                                    );
                                    setRepositoryFiles((files) =>
                                      files.filter((file) => file.repoKey !== selection.key),
                                    );
                                    setRepoError(null);
                                  }}
                                >
                                  Remove
                                </button>
                              </div>
                            </li>
                          ))}
                          {filteredRepositories.map((repo) => (
                            <li
                              key={repo.name}
                              className="creation-sheet__row-item"
                              data-repo-key={repo.name}
                            >
                              <label className="creation-sheet__row" data-valid={repo.valid}>
                                <span className="creation-sheet__row-body">
                                  <b className="creation-sheet__row-name">{repo.name}</b>
                                  <code className="creation-sheet__row-path">{repo.path}</code>
                                  {!repo.valid ? (
                                    <span className="creation-sheet__row-issue">
                                      {repo.issue?.summary ?? 'Unavailable'}
                                    </span>
                                  ) : !repo.featureReady ? (
                                    <span className="creation-sheet__row-issue">
                                      No commits yet — an initial commit is required before feature
                                      work can start.
                                    </span>
                                  ) : repo.identity === undefined ? (
                                    <span className="creation-sheet__row-issue">
                                      The server could not resolve this repository's identity, so it
                                      cannot be selected.
                                    </span>
                                  ) : null}
                                  {sourceState.phase === 'loaded'
                                    ? sourceState.value.repositories
                                        .filter((source) => source.repoKey === repo.name)
                                        .map((source) => (
                                          <span
                                            key={source.observedSha}
                                            className="creation-sheet__row-hint"
                                          >
                                            Source:{' '}
                                            {source.kind === 'detached'
                                              ? `detached ${source.observedSha}`
                                              : source.branch}
                                          </span>
                                        ))
                                    : null}
                                </span>
                                <input
                                  className="creation-sheet__row-control"
                                  type="checkbox"
                                  checked={
                                    repo.identity !== undefined &&
                                    reconciledSelections.some(
                                      (selection) =>
                                        selection.status === 'selected' &&
                                        sameRepoIdentity(
                                          selection.identity,
                                          repo.identity as NonNullable<RepositoryState['identity']>,
                                        ),
                                    )
                                  }
                                  disabled={!isSelectableRepository(repo) || pending}
                                  onChange={() => {
                                    if (repo.identity === undefined) return;
                                    const identity = repo.identity;
                                    const isSelected = repoSelections.some((selection) =>
                                      sameRepoIdentity(selection.identity, identity),
                                    );
                                    const nextSelections = isSelected
                                      ? repoSelections.filter(
                                          (selection) =>
                                            !sameRepoIdentity(selection.identity, identity),
                                        )
                                      : [...repoSelections, { key: repo.name, identity }];
                                    setRepoSelections(nextSelections);
                                    const nextKeys = resolvedSelectionKeys(
                                      reconcileRepoSelections(nextSelections, repositories),
                                    );
                                    setRepositoryFiles((files) =>
                                      files.filter((file) => nextKeys.includes(file.repoKey)),
                                    );
                                    setRepoError(null);
                                  }}
                                />
                              </label>
                              {repo.valid && !repo.featureReady && repo.identity !== undefined ? (
                                <div className="creation-sheet__row-initialize">
                                  <button
                                    type="button"
                                    className="creation-sheet__button"
                                    aria-expanded={initializeExpandedKey === repo.name}
                                    aria-controls={`repo-${encodeURIComponent(repo.name)}-initialize-copy`}
                                    disabled={initializeSuppressed(repo) || pending}
                                    onClick={() =>
                                      setInitializeExpandedKey((current) =>
                                        current === repo.name ? null : repo.name,
                                      )
                                    }
                                  >
                                    Create initial commit…
                                  </button>
                                  {initializeExpandedKey === repo.name ? (
                                    <InitializeOffer
                                      idPrefix={`repo-${encodeURIComponent(repo.name)}`}
                                      controller={{
                                        pending: initializeSuppressed(repo),
                                        error: initializeError,
                                        onInitialize: () => {
                                          const identity = repo.identity;
                                          if (identity === undefined) return;
                                          // The rejection is already surfaced as the scoped
                                          // offer error; this catch only keeps the discarded
                                          // promise quiet.
                                          void handleInitialize({
                                            repoKey: repo.name,
                                            identity,
                                            path: repo.path,
                                          }).catch(() => undefined);
                                        },
                                        onDecline: () => setInitializeExpandedKey(null),
                                      }}
                                    />
                                  ) : null}
                                </div>
                              ) : null}
                            </li>
                          ))}
                          {filteredRepositories.length === 0 &&
                          unresolvedSelections.length === 0 ? (
                            <li className="creation-sheet__row-item creation-sheet__row-empty">
                              No repositories match “{repoQuery.trim()}”.
                            </li>
                          ) : null}
                        </ul>
                      )}
                      <FieldError id="creation-repositories-error" message={repoError} />
                    </fieldset>
                    <section
                      className="creation-sheet__browser"
                      aria-label="Add a repository to the workspace"
                      {...(repositories.length === 0 ? { 'data-primary': 'true' } : {})}
                    >
                      {remoteServer ? null : (
                        <div className="creation-sheet__browser-head">
                          <h3 className="creation-sheet__browser-title">
                            {repositories.length === 0
                              ? 'Add your first repository'
                              : 'Bring in another folder'}
                          </h3>
                          <button
                            type="button"
                            className="creation-sheet__button"
                            disabled={folderPending}
                            onClick={() => void browseDirectory()}
                          >
                            Browse for folder
                          </button>
                        </div>
                      )}
                      {remoteServer ? (
                        <div className="creation-sheet__path-entry">
                          <label className="creation-sheet__field">
                            <span className="creation-sheet__field-label">
                              Folder path on the server
                            </span>
                            <input
                              className="creation-sheet__input"
                              type="text"
                              value={folderDraft}
                              placeholder="/srv/work/my-repo"
                              spellCheck={false}
                              autoComplete="off"
                              disabled={folderPending}
                              aria-invalid={fieldAriaInvalid(folderError !== null)}
                              aria-describedby={fieldAriaDescribedBy(
                                'creation-folder-path-error',
                                folderError !== null,
                              )}
                              onChange={(event) => {
                                setFolderDraft(event.target.value);
                                setFolderError(null);
                              }}
                              onKeyDown={(event) => {
                                if (event.key === 'Enter') {
                                  event.preventDefault();
                                  checkTypedFolder();
                                }
                              }}
                            />
                          </label>
                          <div className="creation-sheet__browser-actions">
                            <button
                              type="button"
                              className="creation-sheet__button"
                              disabled={folderPending || folderDraft.trim() === ''}
                              onClick={checkTypedFolder}
                            >
                              Use this path
                            </button>
                          </div>
                          <FieldError id="creation-folder-path-error" message={folderError} />
                        </div>
                      ) : null}
                      <p
                        className="creation-sheet__browser-notice"
                        role="status"
                        aria-live="polite"
                      >
                        {folderNotice}
                      </p>
                      {folderCandidate !== null ? (
                        <>
                          <code className="creation-sheet__browser-path">{folderCandidate}</code>
                          <div className="creation-sheet__browser-actions">
                            {folderHoldsNoRepository ? (
                              <button
                                type="button"
                                className="creation-sheet__button"
                                disabled={folderPending}
                                onClick={() => setConsentOpen(true)}
                              >
                                Initialize it as a repository…
                              </button>
                            ) : (
                              <button
                                type="button"
                                className="creation-sheet__button"
                                disabled={folderPending}
                                onClick={() => void useFolder()}
                              >
                                {folderPending ? 'Adding…' : 'Use this folder'}
                              </button>
                            )}
                          </div>
                        </>
                      ) : (
                        <p className="creation-sheet__browser-hint">
                          {'Choose deliberately; no folder is changed until you confirm an action.'}
                        </p>
                      )}
                      <div className="creation-sheet__browser-actions">
                        <button
                          type="button"
                          className="creation-sheet__button"
                          onClick={() => {
                            setCreateOpen(true);
                            setFolderNotice('');
                          }}
                        >
                          Create a repository…
                        </button>
                        <button
                          type="button"
                          className="creation-sheet__button"
                          onClick={() => {
                            setCloneOpen(true);
                            setFolderNotice('');
                          }}
                        >
                          Clone a repository…
                        </button>
                      </div>
                    </section>
                    <fieldset className="creation-sheet__group">
                      <legend className="creation-sheet__group-label">Local source</legend>
                      <div className="creation-sheet__rows">
                        <label className="creation-sheet__row creation-sheet__row--choice">
                          <input
                            type="radio"
                            name="branch"
                            checked={!useCurrentBranch}
                            onChange={() => setUseCurrentBranch(false)}
                          />
                          <span className="creation-sheet__row-name">Default branches</span>
                        </label>
                        <label className="creation-sheet__row creation-sheet__row--choice">
                          <input
                            type="radio"
                            name="branch"
                            checked={useCurrentBranch}
                            onChange={() => setUseCurrentBranch(true)}
                          />
                          <span className="creation-sheet__row-name">Current branches</span>
                        </label>
                      </div>
                      {sourceState.phase === 'loading' ? (
                        <p className="creation-sheet__row-hint" role="status">
                          Resolving local sources…
                        </p>
                      ) : sourceState.phase === 'error' ? (
                        <ErrorSurface error={sourceState.error} />
                      ) : null}
                    </fieldset>
                  </section>
                ) : null}

                {currentStep === 'Describe' ? (
                  <section className="creation-sheet__step" aria-labelledby="creation-describe">
                    <h2 id="creation-describe" className="creation-sheet__heading">
                      Define the work
                    </h2>
                    <DescriptionComposer
                      id="feature-description"
                      label="Description"
                      placeholder="Describe the work. Type @ to reference files in the selected repositories; paste or drop images and files to attach them."
                      value={description}
                      searchRepositories={searchRepositories}
                      images={images}
                      attachments={attachments}
                      imageUploads={imageUploads}
                      attachmentUploads={attachmentUploads}
                      repositoryFiles={repositoryFiles}
                      onValueChange={setDescription}
                      onImagesChange={setImages}
                      onAttachmentsChange={setAttachments}
                      onImageUploadsChange={setImageUploads}
                      onAttachmentUploadsChange={setAttachmentUploads}
                      onRepositoryFilesChange={setRepositoryFiles}
                      onError={setFormError}
                    />
                    <label className="creation-sheet__field">
                      <span className="creation-sheet__field-label">Name</span>
                      <input
                        ref={nameRef}
                        id="feature-name"
                        className="creation-sheet__input"
                        value={name}
                        maxLength={200}
                        aria-invalid={fieldAriaInvalid(nameError !== null)}
                        aria-describedby={fieldAriaDescribedBy(
                          'feature-name-error',
                          nameError !== null,
                        )}
                        onChange={(event) => {
                          setName(event.target.value);
                          setNameError(null);
                        }}
                      />
                      <FieldError id="feature-name-error" message={nameError} />
                    </label>
                  </section>
                ) : null}

                {currentStep === 'Depth' ? (
                  <section className="creation-sheet__step" aria-labelledby="creation-depth">
                    <h2 id="creation-depth" className="creation-sheet__heading">
                      Set the depth
                    </h2>
                    {depthProfiles('full')}
                  </section>
                ) : null}

                {currentStep === 'Contract' ? (
                  <section className="creation-sheet__step" aria-labelledby="creation-contract">
                    <h2 id="creation-contract" className="creation-sheet__heading">
                      Review the run contract
                    </h2>
                    {/* The chosen depth stays adjustable while the contract it
                        shapes is confirmed, as in the mock's Contract screen. */}
                    {depthProfiles('compact')}
                    <fieldset className="creation-sheet__group">
                      <legend className="creation-sheet__group-label">
                        Where the run stops for you
                      </legend>
                      <p className="creation-sheet__group-desc">
                        Checkpoints pause the pipeline for your review before continuing. The{' '}
                        {pipeline} pipeline supports the checkpoints below.
                      </p>
                      <div className="creation-sheet__rows">
                        {visibleGates.map((gate) => (
                          <label key={gate.key} className="creation-sheet__row">
                            <span className="creation-sheet__row-body">
                              <b className="creation-sheet__row-name">{gate.label}</b>
                              <span className="creation-sheet__row-hint">{gate.hint}</span>
                            </span>
                            <input
                              className="creation-sheet__row-control"
                              type="checkbox"
                              checked={checkpoints[gate.key]}
                              onChange={(event) =>
                                setCheckpoints((current) => {
                                  const nextState = {
                                    ...current,
                                    [gate.key]: event.target.checked,
                                  };
                                  // Roadmap review implies phase plan review.
                                  if (gate.key === 'roadmapReview')
                                    nextState.phasePlanReview = event.target.checked;
                                  return nextState;
                                })
                              }
                            />
                          </label>
                        ))}
                      </div>
                    </fieldset>
                    <fieldset className="creation-sheet__group">
                      <legend className="creation-sheet__group-label">Models</legend>
                      <p className="creation-sheet__group-desc">
                        Only models available from provider discovery can be selected. Default uses
                        the workspace model for that phase.
                      </p>
                      <div className="config-editor__phase-rows">
                        {applicablePhaseFields(pipeline, false).map((field) => (
                          <ModelEffortRow
                            key={field.key}
                            field={field}
                            modelValue={modelChoices[field.key] ?? ''}
                            defaultModel={modelDefaults[field.key] ?? ''}
                            effortValue={effortChoices[field.key]}
                            defaultEffort={effortDefaults[field.key]}
                            catalogue={catalogue}
                            pipeline={pipeline}
                            onModelChange={(model, resetEffort) => {
                              setModelChoices((choices) => ({ ...choices, [field.key]: model }));
                              if (resetEffort !== undefined) {
                                setEffortChoices((choices) => ({
                                  ...choices,
                                  [field.key]: resetEffort,
                                }));
                              }
                            }}
                            onEffortChange={(effort) =>
                              setEffortChoices((choices) => ({ ...choices, [field.key]: effort }))
                            }
                          />
                        ))}
                      </div>
                    </fieldset>
                    <div className="creation-sheet__knobs">
                      <div className="creation-sheet__knob-pair">
                        <label className="creation-sheet__field">
                          <span className="creation-sheet__field-label">Risk</span>
                          <select
                            className="creation-sheet__select"
                            value={riskLevel}
                            onChange={(e) => setRiskLevel(e.target.value as typeof riskLevel)}
                          >
                            <option value="low">Low</option>
                            <option value="medium">Medium</option>
                            <option value="high">High</option>
                          </select>
                        </label>
                        <label className="creation-sheet__field">
                          <span className="creation-sheet__field-label">Inquireness</span>
                          <select
                            className="creation-sheet__select"
                            value={inquireness}
                            onChange={(e) => setInquireness(e.target.value as typeof inquireness)}
                          >
                            <option value="none">None</option>
                            <option value="medium">Medium</option>
                            <option value="high">High</option>
                          </select>
                        </label>
                      </div>
                      <label className="creation-sheet__field">
                        <span className="creation-sheet__field-label">Exit criteria</span>
                        <textarea
                          className="creation-sheet__input creation-sheet__input--multiline"
                          value={exitCriteria}
                          maxLength={4000}
                          rows={3}
                          placeholder="What must be true for this run to be considered done?"
                          onChange={(event) => setExitCriteria(event.target.value)}
                        />
                      </label>
                    </div>
                    <div className="creation-sheet__rows creation-sheet__rows--standalone">
                      <label className="creation-sheet__row">
                        <span className="creation-sheet__row-body">
                          <b className="creation-sheet__row-name">Start immediately</b>
                          <span className="creation-sheet__row-hint">
                            Run setup and begin the first phase as soon as the feature is created.
                          </span>
                        </span>
                        <input
                          className="creation-sheet__row-control"
                          type="checkbox"
                          checked={autoStart}
                          onChange={(event) => setAutoStart(event.target.checked)}
                        />
                      </label>
                    </div>
                    <dl className="creation-sheet__summary">
                      <div>
                        <dt>Repositories</dt>
                        <dd>{selectedKeys.join(', ')}</dd>
                      </div>
                      <div>
                        <dt>Local sources</dt>
                        <dd>
                          {sourceState.phase === 'loaded'
                            ? sourceState.value.repositories
                                .map((source) =>
                                  source.kind === 'detached'
                                    ? `${source.repoKey}: detached ${source.observedSha}`
                                    : `${source.repoKey}: ${source.branch ?? ''}`,
                                )
                                .join(', ')
                            : 'Not resolved'}
                        </dd>
                      </div>
                      <div>
                        <dt>Describe</dt>
                        <dd>{name}</dd>
                      </div>
                      <div>
                        <dt>Depth</dt>
                        <dd>{pipeline}</dd>
                      </div>
                      <div>
                        <dt>Contract</dt>
                        <dd>
                          {riskLevel} risk · {checkpointSummary(pipeline, checkpoints)}
                        </dd>
                      </div>
                    </dl>
                  </section>
                ) : null}
              </>
            )}
          </div>

          <footer className="sheet__footer creation-sheet__footer">
            <button type="button" className="sheet__footer-secondary" onClick={requestCancel}>
              Cancel
            </button>
            {currentStep === 'Contract' && loadedDefaults !== null ? (
              <span className="sheet__footer-note">
                {plural(checkedCheckpoints, 'checkpoint', 'checkpoints')} ·{' '}
                {plural(selectedKeys.length, 'repository', 'repositories')}
              </span>
            ) : null}
            {loadedDefaults === null ? null : (
              <div className="creation-sheet__footer-trailing">
                {stepIndex > 0 ? (
                  <button
                    type="button"
                    className="sheet__footer-secondary"
                    onClick={() => setStepIndex((current) => current - 1)}
                  >
                    Back
                  </button>
                ) : null}
                {stepIndex < 3 ? (
                  <button
                    key="next-step"
                    type="button"
                    className="sheet__footer-primary"
                    disabled={
                      stepIndex === 0 && selectedKeys.length > 0 && sourceState.phase === 'loading'
                    }
                    onClick={(event) => {
                      // React can reuse this DOM node as the submit button when
                      // the click advances to Contract. Cancel the original
                      // button's browser default before that type transition.
                      event.preventDefault();
                      next();
                    }}
                  >
                    Next: {STEPS[stepIndex + 1]}
                  </button>
                ) : (
                  <>
                    {uploadsBlocking ? (
                      <span className="sheet__footer-note" role="status">
                        {STAGED_ITEMS_BLOCK_SUBMIT}
                      </span>
                    ) : null}
                    <button
                      key="create-feature"
                      type="submit"
                      className="sheet__footer-primary"
                      disabled={pending || uploadsBlocking}
                    >
                      {pending ? 'Creating…' : autoStart ? 'Create and start' : 'Create'}
                    </button>
                  </>
                )}
              </div>
            )}
          </footer>
        </form>

        {cloneOpen ? (
          <PickerCloneDialog
            connection={connection}
            workspaceRoots={cloneableRoots}
            association={cloneAssociation}
            operation={cloneOperation}
            onAssociate={(association) => setCloneAssociation(association)}
            onRefresh={resolveCloneOperation}
            onReadinessChanged={applyCatalogSnapshot}
            initializeOffer={cloneInitializeOffer}
            onClose={() => setCloneOpen(false)}
          />
        ) : null}

        {createOpen ? (
          <PickerCreateDialog
            connection={connection}
            workspaceRoots={cloneableRoots}
            onCreate={handleCreate}
            onReadinessChanged={applyCatalogSnapshot}
            onClose={() => setCreateOpen(false)}
          />
        ) : null}

        {discardOpen ? (
          <DiscardDialog onKeepEditing={() => setDiscardOpen(false)} onDiscard={handleClose} />
        ) : null}

        {consentOpen && folderCandidate !== null ? (
          <ConsentDialog
            path={folderCandidate}
            busy={folderPending}
            onConfirm={() => void initializeFolder()}
            onCancel={() => setConsentOpen(false)}
          />
        ) : null}
      </div>
    </div>
  );
}

/**
 * The unsaved-work confirmation, rendered as the innermost dialog inside the
 * sheet so the shared modal-dismiss hook hands Escape to it while it is open.
 * Focus lands on the safe action first.
 */
function DiscardDialog({ onKeepEditing, onDiscard }: { onKeepEditing(): void; onDiscard(): void }) {
  const keepRef = useRef<HTMLButtonElement | null>(null);
  useEffect(() => {
    keepRef.current?.focus();
  }, []);
  return (
    <div className="impact-dialog__backdrop">
      <div
        className="impact-dialog"
        role="dialog"
        aria-modal="true"
        aria-label="Discard feature draft"
        onKeyDown={(event) => {
          if (event.key === 'Escape') {
            event.stopPropagation();
            onKeepEditing();
          }
        }}
      >
        <h2>Discard this feature draft?</h2>
        <p>Your entered feature details have not been created.</p>
        <div className="impact-dialog__actions">
          <button type="button" ref={keepRef} onClick={onKeepEditing}>
            Keep editing
          </button>
          <button type="button" className="cockpit__stop" onClick={onDiscard}>
            Discard draft
          </button>
        </div>
      </div>
    </div>
  );
}

function normalizeInquireness(value: string | undefined): 'none' | 'medium' | 'high' {
  return value === 'none' || value === 'high' ? value : 'medium';
}
