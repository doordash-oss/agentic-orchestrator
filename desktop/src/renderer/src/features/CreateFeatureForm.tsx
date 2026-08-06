/**
 * Four-step creation contract, repository-first: Where, What,
 * Pipeline, Review. Initial defaults prefill the draft once; later repository
 * discovery must preserve every user-owned choice.
 */
import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react';
import {
  type CreationDefaults,
  type EffortLevel,
  type RepositoryFileRef,
  type RepositoryState,
} from '../../../shared/ipc';
import { ConsentDialog } from '../components/wizard/ConsentDialog';
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

const STEPS = ['Where', 'What', 'Pipeline', 'Review'] as const;
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

export interface CreateFeatureFormProps {
  onCreated(created: { featureId: string; name: string }): void;
  onDirtyChange?(dirty: boolean): void;
}

export function CreateFeatureForm({ onCreated, onDirtyChange }: CreateFeatureFormProps) {
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
  const [repositoryFiles, setRepositoryFiles] = useState<readonly RepositoryFileRef[]>([]);
  const [autoStart, setAutoStart] = useState(true);
  const [folderCandidate, setFolderCandidate] = useState<string | null>(null);
  /** Set once a candidate is a configured root that holds no repository. */
  const [folderHoldsNoRepository, setFolderHoldsNoRepository] = useState(false);
  const [folderNotice, setFolderNotice] = useState('');
  const [workspaceRoots, setWorkspaceRoots] = useState<readonly string[]>([]);
  const [consentOpen, setConsentOpen] = useState(false);
  const [folderPending, setFolderPending] = useState(false);
  const [pending, setPending] = useState(false);
  const [nameError, setNameError] = useState<string | null>(null);
  const [repoError, setRepoError] = useState<string | null>(null);
  const [formError, setFormError] = useState<WizardError | null>(null);
  const catalogue = useModelCatalogue();
  const creationKey = useRef(crypto.randomUUID());
  const nameRef = useRef<HTMLInputElement | null>(null);
  const repoGroupRef = useRef<HTMLFieldSetElement | null>(null);
  const formErrorRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    onDirtyChange?.(
      name.trim() !== '' ||
        description !== '' ||
        repoKeys.length > 0 ||
        images.length > 0 ||
        attachments.length > 0,
    );
  }, [attachments.length, description, images.length, name, onDirtyChange, repoKeys.length]);

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
  useEffect(() => {
    if (formError !== null) formErrorRef.current?.focus();
  }, [formError]);

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
          'Added as a workspace root, but it holds no git repository yet. ' +
            'Initialize it, or browse for a different folder.',
        );
        return;
      }
      selectDiscovered(discovered);
      setFolderCandidate(null);
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
    if (index === 0 && repoKeys.length === 0) {
      setRepoError('Select at least one repository.');
      repoGroupRef.current?.focus();
      return false;
    }
    if (index === 1 && name.trim() === '') {
      setNameError('Enter a feature name.');
      nameRef.current?.focus();
      return false;
    }
    return true;
  };

  const next = (): void => {
    if (validateStep(stepIndex)) setStepIndex((current) => Math.min(current + 1, 3));
  };

  const submit = (event: FormEvent): void => {
    event.preventDefault();
    if (pending || state.phase !== 'loaded') return;
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
    const gates = applicableGates(pipeline);
    void (async () => {
      try {
        const created = await window.agentico.createFeature({
          name: name.trim(),
          description,
          repoKeys: [...repoKeys],
          useCurrentBranch,
          images: [...images],
          attachments: [...attachments],
          repositoryFiles: [...repositoryFiles],
          pipeline,
          riskLevel,
          inquireness,
          exitCriteria,
          models,
          effort,
          checkpoints: {
            inquiryReview: gates.has('inquiryReview') && checkpoints.inquiryReview,
            researchReview: gates.has('researchReview') && checkpoints.researchReview,
            designReview: gates.has('designReview') && checkpoints.designReview,
            roadmapReview: gates.has('roadmapReview') && checkpoints.roadmapReview,
            phasePlanReview: gates.has('phasePlanReview') && checkpoints.phasePlanReview,
            manualPublish: gates.has('manualPublish') && checkpoints.manualPublish,
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
          nameRef.current?.focus();
        } else if (field === 'repos') {
          setStepIndex(0);
          setRepoError(parsed.message);
          repoGroupRef.current?.focus();
        } else setFormError(parsed);
      } finally {
        setPending(false);
      }
    })();
  };

  const repositories = state.phase === 'loaded' ? state.defaults.repositories : [];
  const filteredRepositories = useMemo(() => {
    const query = repoQuery.trim().toLowerCase();
    if (query === '') return repositories;
    return repositories.filter(
      (repo) => repo.name.toLowerCase().includes(query) || repo.path.toLowerCase().includes(query),
    );
  }, [repoQuery, repositories]);

  if (state.phase === 'loading')
    return (
      <section className="create-form" aria-label="Create a feature">
        <p role="status">Loading creation defaults from the runtime…</p>
      </section>
    );
  if (state.phase === 'error')
    return (
      <section className="create-form" aria-label="Create a feature">
        <div role="alert" className="create-form__error">
          <b>{state.error.code}</b>
          <p>{state.error.message}</p>
        </div>
        <button type="button" onClick={loadInitialDefaults}>
          Try again
        </button>
      </section>
    );

  const currentStep = STEPS[stepIndex] as Step;
  const gates = applicableGates(pipeline);
  const visibleGates = GATE_FIELDS.filter((gate) => gates.has(gate.key));
  const defaults = defaultModelsByKey(state.defaults);
  const effortDefaults = defaultEffortByKey(state.defaults);

  return (
    <form
      className="create-form creation-wizard"
      aria-label="Create a feature"
      noValidate
      onSubmit={submit}
    >
      <nav className="creation-wizard__spine" aria-label="Creation steps">
        {STEPS.map((step, index) => {
          const state = index < stepIndex ? 'done' : index === stepIndex ? 'current' : 'upcoming';
          return (
            <button
              key={step}
              type="button"
              data-state={state}
              aria-current={index === stepIndex ? 'step' : undefined}
              disabled={index > stepIndex}
              onClick={() => setStepIndex(index)}
            >
              <span className="creation-wizard__step-marker" aria-hidden="true">
                {state === 'done' ? '✓' : index + 1}
              </span>
              <span className="creation-wizard__step-label">{step}</span>
            </button>
          );
        })}
      </nav>
      {formError !== null ? (
        <div ref={formErrorRef} tabIndex={-1} role="alert" className="create-form__error">
          <b>{formError.code}</b>
          <p>{formError.message}</p>
        </div>
      ) : null}

      {currentStep === 'Where' ? (
        <section className="creation-wizard__panel" aria-labelledby="creation-where">
          <p className="eyebrow-label">01 / Where</p>
          <h2 id="creation-where">Choose repositories</h2>
          {repositories.length > 0 ? (
            <label className="form-field">
              <span className="form-field__label">Search repositories</span>
              <input
                className="form-field__input"
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
            className="form-field form-field--group"
            aria-invalid={repoError !== null}
          >
            <legend className="form-field__label">
              {repositories.length === 0 ? 'No repositories yet' : 'Fresh workspace discovery'}
            </legend>
            {repositories.length === 0 ? (
              <p className="repo-options__empty">
                Point Agentico at a folder below: an existing repository, a folder that holds
                several, or an empty folder to start something new.
              </p>
            ) : null}
            <ul className="repo-options">
              {filteredRepositories.map((repo) => (
                <li key={repo.name} className="repo-option" data-valid={repo.valid}>
                  <label className="repo-option__label">
                    <input
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
                    <b>{repo.name}</b>
                    <code>{repo.path}</code>
                  </label>
                  {!repo.valid ? (
                    <span className="repo-option__issue">
                      {repo.issue?.message ?? 'Unavailable'}
                    </span>
                  ) : null}
                </li>
              ))}
              {repositories.length > 0 && filteredRepositories.length === 0 ? (
                <li className="repo-option repo-option--empty">
                  No repositories match “{repoQuery.trim()}”.
                </li>
              ) : null}
            </ul>
            {repoError ? <p className="form-field__error">{repoError}</p> : null}
          </fieldset>
          <section
            className="directory-browser"
            aria-label="Add a repository to the workspace"
            {...(repositories.length === 0 ? { 'data-primary': 'true' } : {})}
          >
            <div>
              <h3>
                {repositories.length === 0
                  ? 'Add your first repository'
                  : 'Bring in another folder'}
              </h3>
              <button type="button" disabled={folderPending} onClick={() => void browseDirectory()}>
                Browse for folder
              </button>
            </div>
            <p className="directory-browser__notice" role="status" aria-live="polite">
              {folderNotice}
            </p>
            {folderCandidate !== null ? (
              <>
                <code>{folderCandidate}</code>
                <div className="directory-browser__actions">
                  {folderHoldsNoRepository ? (
                    <button
                      type="button"
                      disabled={folderPending}
                      onClick={() => setConsentOpen(true)}
                    >
                      Initialize it as a repository…
                    </button>
                  ) : (
                    <button type="button" disabled={folderPending} onClick={() => void useFolder()}>
                      {folderPending ? 'Adding…' : 'Use this folder'}
                    </button>
                  )}
                </div>
              </>
            ) : (
              <p>Choose deliberately; no folder is changed until you confirm an action.</p>
            )}
          </section>
          <fieldset className="form-field form-field--group">
            <legend className="form-field__label">Branch</legend>
            <label>
              <input
                type="radio"
                name="branch"
                checked={!useCurrentBranch}
                onChange={() => setUseCurrentBranch(false)}
              />{' '}
              New feature branch
            </label>
            <label>
              <input
                type="radio"
                name="branch"
                checked={useCurrentBranch}
                onChange={() => setUseCurrentBranch(true)}
              />{' '}
              Current branch
            </label>
          </fieldset>
        </section>
      ) : null}

      {currentStep === 'What' ? (
        <section className="creation-wizard__panel" aria-labelledby="creation-what">
          <p className="eyebrow-label">02 / What</p>
          <h2 id="creation-what">Define the work</h2>
          <label className="form-field">
            <span className="form-field__label">Name</span>
            <input
              ref={nameRef}
              id="feature-name"
              className="form-field__input"
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
              <span id="feature-name-error" className="form-field__error">
                {nameError}
              </span>
            ) : null}
          </label>
          <DescriptionComposer
            id="feature-description"
            label="Description"
            placeholder="Describe the work. Type @ to reference files in the selected repositories; paste or drop images and files to attach them."
            value={description}
            repoKeys={repoKeys}
            images={images}
            attachments={attachments}
            repositoryFiles={repositoryFiles}
            onValueChange={setDescription}
            onImagesChange={setImages}
            onAttachmentsChange={setAttachments}
            onRepositoryFilesChange={setRepositoryFiles}
            onError={setFormError}
          />
        </section>
      ) : null}

      {currentStep === 'Pipeline' ? (
        <section className="creation-wizard__panel" aria-labelledby="creation-pipeline">
          <p className="eyebrow-label">03 / Pipeline</p>
          <h2 id="creation-pipeline">Set the depth</h2>
          <div className="pipeline-grid">
            {PIPELINES.map((profile) => (
              <label
                key={profile.id}
                className="pipeline-card"
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
                <b>{profile.title}</b>
                <span>{profile.note}</span>
                <small>{checkpointSummary(profile.id, profile.checkpoints)}</small>
              </label>
            ))}
          </div>
        </section>
      ) : null}

      {currentStep === 'Review' ? (
        <section className="creation-wizard__panel" aria-labelledby="creation-review">
          <p className="eyebrow-label">04 / Review</p>
          <h2 id="creation-review">Review the run contract</h2>
          <div className="review-knobs">
            <div className="review-controls">
              <label>
                Risk
                <select
                  value={riskLevel}
                  onChange={(e) => setRiskLevel(e.target.value as typeof riskLevel)}
                >
                  <option value="low">Low</option>
                  <option value="medium">Medium</option>
                  <option value="high">High</option>
                </select>
              </label>
              <label>
                Inquireness
                <select
                  value={inquireness}
                  onChange={(e) => setInquireness(e.target.value as typeof inquireness)}
                >
                  <option value="none">None</option>
                  <option value="medium">Medium</option>
                  <option value="high">High</option>
                </select>
              </label>
            </div>
            <label className="form-field">
              <span className="form-field__label">Exit criteria</span>
              <textarea
                value={exitCriteria}
                maxLength={4000}
                rows={3}
                placeholder="What must be true for this run to be considered done?"
                onChange={(event) => setExitCriteria(event.target.value)}
              />
            </label>
          </div>
          <section className="review-contract" aria-label="Models and checkpoints">
            <fieldset className="config-editor__group">
              <legend className="config-editor__group-title">Models</legend>
              <p className="config-editor__group-desc">
                Only models available from provider discovery can be selected. Default uses the
                workspace model for that phase.
              </p>
              {applicablePhaseFields(pipeline, false).map((field) => (
                <ModelEffortRow
                  key={field.key}
                  field={field}
                  modelValue={modelChoices[field.key] ?? ''}
                  defaultModel={defaults[field.key] ?? ''}
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
            </fieldset>
            <fieldset className="config-editor__group">
              <legend className="config-editor__group-title">Review checkpoints</legend>
              <p className="config-editor__group-desc">
                Checkpoints pause the pipeline for your review before continuing. The {pipeline}{' '}
                pipeline supports the checkpoints below.
              </p>
              {visibleGates.map((gate) => (
                <label key={gate.key} className="config-editor__gate">
                  <input
                    type="checkbox"
                    checked={checkpoints[gate.key]}
                    onChange={(event) =>
                      setCheckpoints((current) => {
                        const nextState = { ...current, [gate.key]: event.target.checked };
                        // Roadmap review implies phase plan review.
                        if (gate.key === 'roadmapReview')
                          nextState.phasePlanReview = event.target.checked;
                        return nextState;
                      })
                    }
                  />
                  <span className="config-editor__gate-text">
                    <b>{gate.label}</b>
                    <span>{gate.hint}</span>
                  </span>
                </label>
              ))}
            </fieldset>
          </section>
          <label className="config-editor__gate creation-autostart">
            <input
              type="checkbox"
              checked={autoStart}
              onChange={(event) => setAutoStart(event.target.checked)}
            />
            <span className="config-editor__gate-text">
              <b>Start immediately</b>
              <span>Run setup and begin the first phase as soon as the feature is created.</span>
            </span>
          </label>
          <dl className="creation-summary">
            <div>
              <dt>Where</dt>
              <dd>{repoKeys.join(', ')}</dd>
            </div>
            <div>
              <dt>What</dt>
              <dd>{name}</dd>
            </div>
            <div>
              <dt>Pipeline</dt>
              <dd>{pipeline}</dd>
            </div>
            <div>
              <dt>Review</dt>
              <dd>
                {riskLevel} risk · {checkpointSummary(pipeline, checkpoints)}
              </dd>
            </div>
          </dl>
        </section>
      ) : null}

      <footer className="creation-wizard__actions">
        {stepIndex > 0 ? (
          <button type="button" onClick={() => setStepIndex((current) => current - 1)}>
            Back
          </button>
        ) : (
          <span />
        )}
        {stepIndex < 3 ? (
          <button
            key="next-step"
            type="button"
            className="create-form__submit"
            onClick={(event) => {
              // React can reuse this DOM node as the submit button when the
              // click advances to Review. Cancel the original button's
              // browser default before that type transition occurs.
              event.preventDefault();
              next();
            }}
          >
            Next: {STEPS[stepIndex + 1]}
          </button>
        ) : (
          <button
            key="create-feature"
            type="submit"
            className="create-form__submit"
            disabled={pending}
          >
            {pending ? 'Creating…' : autoStart ? 'Create and start' : 'Create feature'}
          </button>
        )}
      </footer>

      {consentOpen && folderCandidate !== null ? (
        <ConsentDialog
          path={folderCandidate}
          busy={folderPending}
          onConfirm={() => void initializeFolder()}
          onCancel={() => setConsentOpen(false)}
        />
      ) : null}
    </form>
  );
}

function normalizeInquireness(value: string | undefined): 'none' | 'medium' | 'high' {
  return value === 'none' || value === 'high' ? value : 'medium';
}
