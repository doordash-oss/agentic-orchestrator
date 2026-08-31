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
  type CreationDefaults,
  type EffortLevel,
  type RepositoryFileRef,
  type RepositoryState,
} from '../../../shared/ipc';
import { ConsentDialog } from '../components/wizard/ConsentDialog';
import { useModalDismiss } from '../components/useModalDismiss';
import { useConnectionState } from '../hooks';
import {
  isBlockingStagedItem,
  STAGED_ITEMS_BLOCK_SUBMIT,
  submittableReferences,
  type ComposerUploadItem,
} from './stagedItems';
import { parseIpcError, type WizardError } from '../wizard/ipcError';
import {
  GATE_FIELDS,
  ModelEffortRow,
  applicableGates,
  applicablePhaseFields,
  useModelCatalogue,
  type PhaseKey,
} from './ConfigEditor';
import { DescriptionComposer } from './DescriptionComposer';
import { fieldForCreationError } from './featureView';
import {
  PIPELINES,
  checkpointSummary,
  checkpointsForPipeline,
  isPipeline,
  modelConfigKey,
  type CheckpointState,
  type Pipeline,
} from './runContract';

type DefaultsState =
  | { phase: 'loading' }
  | { phase: 'error'; error: WizardError }
  | { phase: 'loaded'; defaults: CreationDefaults };

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
}

export function CreateFeatureForm({ onCreated, onClose }: CreateFeatureFormProps) {
  const [state, setState] = useState<DefaultsState>({ phase: 'loading' });
  const [stepIndex, setStepIndex] = useState(0);
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [repoKeys, setRepoKeys] = useState<readonly string[]>([]);
  const [repoQuery, setRepoQuery] = useState('');
  const [useCurrentBranch, setUseCurrentBranch] = useState(false);
  const [pipeline, setPipeline] = useState<Pipeline>('medium');
  const [checkpoints, setCheckpoints] = useState<CheckpointState>(checkpointsForPipeline('medium'));
  const [modelChoices, setModelChoices] = useState<Partial<Record<PhaseKey, string>>>({});
  const [effortChoices, setEffortChoices] = useState<Partial<Record<PhaseKey, EffortLevel>>>({});
  const [riskLevel, setRiskLevel] = useState<'low' | 'medium' | 'high'>('medium');
  const [inquireness, setInquireness] = useState<'none' | 'medium' | 'high'>('medium');
  const [exitCriteria, setExitCriteria] = useState('');
  const [images, setImages] = useState<readonly string[]>([]);
  const [attachments, setAttachments] = useState<readonly string[]>([]);
  const [imageUploads, setImageUploads] = useState<readonly ComposerUploadItem[]>([]);
  const [attachmentUploads, setAttachmentUploads] = useState<readonly ComposerUploadItem[]>([]);
  const [repositoryFiles, setRepositoryFiles] = useState<readonly RepositoryFileRef[]>([]);
  const [autoStart, setAutoStart] = useState(true);
  const [folderCandidate, setFolderCandidate] = useState<string | null>(null);
  /** Set once a candidate is a configured root that holds no repository. */
  const [folderHoldsNoRepository, setFolderHoldsNoRepository] = useState(false);
  const [folderNotice, setFolderNotice] = useState('');
  const [workspaceRoots, setWorkspaceRoots] = useState<readonly string[]>([]);
  const [consentOpen, setConsentOpen] = useState(false);
  const [discardOpen, setDiscardOpen] = useState(false);
  const [folderPending, setFolderPending] = useState(false);
  /** Typed path + its inline rejection, only ever used on remote servers. */
  const [folderDraft, setFolderDraft] = useState('');
  const [folderError, setFolderError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const [nameError, setNameError] = useState<string | null>(null);
  const [repoError, setRepoError] = useState<string | null>(null);
  const [formError, setFormError] = useState<WizardError | null>(null);
  const catalogue = useModelCatalogue();
  const creationKey = useRef(crypto.randomUUID());
  const sheetRef = useRef<HTMLDivElement | null>(null);
  const nameRef = useRef<HTMLInputElement | null>(null);
  const repoGroupRef = useRef<HTMLFieldSetElement | null>(null);
  const formErrorRef = useRef<HTMLDivElement | null>(null);

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

  /** Unsaved work worth confirming before it is thrown away. */
  const dirty =
    name.trim() !== '' ||
    description !== '' ||
    repoKeys.length > 0 ||
    images.length > 0 ||
    attachments.length > 0 ||
    imageUploads.length > 0 ||
    attachmentUploads.length > 0;

  const requestCancel = useCallback(() => {
    if (dirty) {
      setDiscardOpen(true);
      return;
    }
    onClose();
  }, [dirty, onClose]);

  // Escape routes to the same Cancel path; the hook's nested-dialog bail
  // leaves Escape to the consent and discard dialogs while either is open.
  useModalDismiss(sheetRef, requestCancel);

  const loadInitialDefaults = useCallback(() => {
    setState({ phase: 'loading' });
    window.agentico
      .getCreationDefaults()
      .then((defaults) => {
        setState({ phase: 'loaded', defaults });
        setUseCurrentBranch(defaults.defaults.useCurrentBranch);
        if (isPipeline(defaults.defaults.pipeline)) {
          setPipeline(defaults.defaults.pipeline);
          setCheckpoints(checkpointsForPipeline(defaults.defaults.pipeline));
        }
        setInquireness(normalizeInquireness(defaults.defaults.inquireness));
      })
      .catch((err: unknown) => setState({ phase: 'error', error: parseIpcError(err) }));
  }, []);

  useEffect(loadInitialDefaults, [loadInitialDefaults]);
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
    workspaceRoots: readonly { path: string }[];
  }): readonly RepositoryState[] => {
    setState((current) =>
      current.phase === 'loaded'
        ? {
            phase: 'loaded',
            defaults: { ...current.defaults, repositories: [...snapshot.repositories] },
          }
        : current,
    );
    setWorkspaceRoots(snapshot.workspaceRoots.map((root) => root.path));
    return snapshot.repositories;
  };

  /** An unambiguous discovery selects itself; several stay for the user. */
  const selectDiscovered = (discovered: readonly RepositoryState[]): void => {
    const only = discovered.length === 1 ? discovered[0] : undefined;
    if (only === undefined) return;
    setRepoKeys((current) => (current.includes(only.name) ? current : [...current, only.name]));
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
      // On a remote server the typed path's fate is the form's own affair:
      // the server's rejection stays next to the field, not in the sheet's
      // global alert.
      if (remoteServer) setFolderError(parseIpcError(err).message);
      else setFormError(parseIpcError(err));
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
      setFormError({
        code: 'E_INVALID_PATH',
        message: 'Choose a folder inside another directory to initialize it as a repository.',
      });
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
      setFolderDraft('');
      setFolderHoldsNoRepository(false);
      setFolderNotice(
        initialized.length === 0
          ? 'Initialized the folder; the runtime has not discovered it yet.'
          : `Initialized ${initialized[0]?.name} and selected it.`,
      );
    } catch (err) {
      if (remoteServer) setFolderError(parseIpcError(err).message);
      else setFormError(parseIpcError(err));
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
    if (index === 0 && repoKeys.length === 0) {
      setRepoError('Select at least one repository.');
      return false;
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
          repoKeys: [...repoKeys],
          useCurrentBranch,
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
        onCreated({ featureId: created.featureId, name: name.trim() });
      } catch (err) {
        const parsed = parseIpcError(err);
        const field = fieldForCreationError(parsed);
        if (field === 'name') {
          setStepIndex(1);
          setNameError(parsed.message);
        } else if (field === 'repos') {
          setStepIndex(0);
          setRepoError(parsed.message);
        } else setFormError(parsed);
      } finally {
        setPending(false);
      }
    })();
  };

  const loadedDefaults = state.phase === 'loaded' ? state.defaults : null;
  const repositories = loadedDefaults?.repositories ?? [];
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
              <div className="creation-sheet__retry">
                <div role="alert" className="creation-sheet__alert">
                  <b>{state.error.code}</b>
                  <p>{state.error.message}</p>
                </div>
                <button
                  type="button"
                  className="creation-sheet__button"
                  onClick={loadInitialDefaults}
                >
                  Try again
                </button>
              </div>
            ) : (
              <>
                {formError !== null ? (
                  <div
                    ref={formErrorRef}
                    tabIndex={-1}
                    role="alert"
                    className="creation-sheet__alert"
                  >
                    <b>{formError.code}</b>
                    <p>{formError.message}</p>
                  </div>
                ) : null}

                {currentStep === 'Repositories' ? (
                  <section className="creation-sheet__step" aria-labelledby="creation-repositories">
                    <h2 id="creation-repositories" className="creation-sheet__heading">
                      Choose repositories
                    </h2>
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
                      aria-invalid={repoError !== null}
                    >
                      <legend className="creation-sheet__group-label">
                        {repositories.length === 0
                          ? 'No repositories yet'
                          : 'Fresh workspace discovery'}
                      </legend>
                      {repositories.length === 0 ? (
                        <p className="creation-sheet__group-desc">
                          Point Agentico at a folder below: an existing repository, a folder that
                          holds several, or an empty folder to start something new.
                        </p>
                      ) : (
                        <ul className="creation-sheet__rows">
                          {filteredRepositories.map((repo) => (
                            <li key={repo.name} className="creation-sheet__row-item">
                              <label className="creation-sheet__row" data-valid={repo.valid}>
                                <span className="creation-sheet__row-body">
                                  <b className="creation-sheet__row-name">{repo.name}</b>
                                  <code className="creation-sheet__row-path">{repo.path}</code>
                                  {!repo.valid ? (
                                    <span className="creation-sheet__row-issue">
                                      {repo.issue?.message ?? 'Unavailable'}
                                    </span>
                                  ) : null}
                                </span>
                                <input
                                  className="creation-sheet__row-control"
                                  type="checkbox"
                                  checked={repoKeys.includes(repo.name)}
                                  disabled={!repo.valid || pending}
                                  onChange={() => {
                                    const nextRepoKeys = repoKeys.includes(repo.name)
                                      ? repoKeys.filter((item) => item !== repo.name)
                                      : [...repoKeys, repo.name];
                                    setRepoKeys(nextRepoKeys);
                                    setRepositoryFiles((files) =>
                                      files.filter((file) => nextRepoKeys.includes(file.repoKey)),
                                    );
                                    setRepoError(null);
                                  }}
                                />
                              </label>
                            </li>
                          ))}
                          {filteredRepositories.length === 0 ? (
                            <li className="creation-sheet__row-item creation-sheet__row-empty">
                              No repositories match “{repoQuery.trim()}”.
                            </li>
                          ) : null}
                        </ul>
                      )}
                      {repoError ? (
                        <p className="creation-sheet__field-error">{repoError}</p>
                      ) : null}
                    </fieldset>
                    <section
                      className="creation-sheet__browser"
                      aria-label="Add a repository to the workspace"
                      {...(repositories.length === 0 ? { 'data-primary': 'true' } : {})}
                    >
                      <div className="creation-sheet__browser-head">
                        <h3 className="creation-sheet__browser-title">
                          {repositories.length === 0
                            ? 'Add your first repository'
                            : 'Bring in another folder'}
                        </h3>
                        {!remoteServer ? (
                          <button
                            type="button"
                            className="creation-sheet__button"
                            disabled={folderPending}
                            onClick={() => void browseDirectory()}
                          >
                            Browse for folder
                          </button>
                        ) : null}
                      </div>
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
                              aria-invalid={folderError !== null}
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
                          {folderError !== null ? (
                            <p className="creation-sheet__field-error" role="alert">
                              {folderError}
                            </p>
                          ) : null}
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
                          {remoteServer
                            ? 'The path is validated on the server host; nothing is changed until you confirm an action.'
                            : 'Choose deliberately; no folder is changed until you confirm an action.'}
                        </p>
                      )}
                    </section>
                    <fieldset className="creation-sheet__group">
                      <legend className="creation-sheet__group-label">Branch</legend>
                      <div className="creation-sheet__rows">
                        <label className="creation-sheet__row creation-sheet__row--choice">
                          <input
                            type="radio"
                            name="branch"
                            checked={!useCurrentBranch}
                            onChange={() => setUseCurrentBranch(false)}
                          />
                          <span className="creation-sheet__row-name">New feature branch</span>
                        </label>
                        <label className="creation-sheet__row creation-sheet__row--choice">
                          <input
                            type="radio"
                            name="branch"
                            checked={useCurrentBranch}
                            onChange={() => setUseCurrentBranch(true)}
                          />
                          <span className="creation-sheet__row-name">Current branch</span>
                        </label>
                      </div>
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
                      repoKeys={repoKeys}
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
                        aria-invalid={nameError !== null}
                        aria-describedby={nameError ? 'feature-name-error' : undefined}
                        onChange={(event) => {
                          setName(event.target.value);
                          setNameError(null);
                        }}
                      />
                      {nameError ? (
                        <span id="feature-name-error" className="creation-sheet__field-error">
                          {nameError}
                        </span>
                      ) : null}
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
                        <dd>{repoKeys.join(', ')}</dd>
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
                {plural(repoKeys.length, 'repository', 'repositories')}
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

        {discardOpen ? (
          <DiscardDialog onKeepEditing={() => setDiscardOpen(false)} onDiscard={onClose} />
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
