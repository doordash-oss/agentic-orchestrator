/**
 * Four-step creation contract, repository-first like the TUI: Where, What,
 * Pipeline, Review. Initial defaults prefill the draft once; later repository
 * discovery must preserve every user-owned choice.
 */
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ClipboardEvent,
  type DragEvent,
  type FormEvent,
  type KeyboardEvent,
} from 'react';
import {
  CREATION_ATTACHMENT_LIMIT,
  CREATION_IMAGE_LIMIT,
  CREATION_REPOSITORY_FILE_LIMIT,
  type Checkpoints,
  type CreationDefaults,
  type RepositoryFileRef,
} from '../../../shared/ipc';
import { parseIpcError, type WizardError } from '../wizard/ipcError';
import {
  GATE_FIELDS,
  ModelPicker,
  PHASE_FIELDS,
  applicableGates,
  useModelCatalogue,
  type PhaseKey,
} from './ConfigEditor';
import { fieldForCreationError } from './featureView';

type DefaultsState =
  | { phase: 'loading' }
  | { phase: 'error'; error: WizardError }
  | { phase: 'loaded'; defaults: CreationDefaults };

const STEPS = ['Where', 'What', 'Pipeline', 'Review'] as const;
type Step = (typeof STEPS)[number];
type Pipeline = 'medium' | 'large' | 'moonshot';

type CheckpointState = Checkpoints & { draftPublish: boolean };

const PIPELINE_PROFILES: Record<
  Pipeline,
  { title: string; note: string; checkpoints: CheckpointState }
> = {
  medium: {
    title: 'Medium',
    note: 'Plan, implement, review',
    checkpoints: {
      inquiryReview: false,
      researchReview: false,
      designReview: false,
      roadmapReview: false,
      phasePlanReview: true,
      manualPublish: false,
      draftPublish: false,
    },
  },
  large: {
    title: 'Large',
    note: 'Full discovery and delivery',
    checkpoints: {
      inquiryReview: true,
      researchReview: false,
      designReview: false,
      roadmapReview: true,
      phasePlanReview: true,
      manualPublish: false,
      draftPublish: false,
    },
  },
  moonshot: {
    title: 'Moonshot',
    note: 'Full depth, maximum scrutiny',
    checkpoints: {
      inquiryReview: true,
      researchReview: true,
      designReview: true,
      roadmapReview: true,
      phasePlanReview: true,
      manualPublish: true,
      draftPublish: false,
    },
  },
};
const PIPELINES = (
  Object.entries(PIPELINE_PROFILES) as Array<[Pipeline, (typeof PIPELINE_PROFILES)[Pipeline]]>
).map(([id, profile]) => ({ id, ...profile }));

function checkpointsForPipeline(pipeline: Pipeline): CheckpointState {
  return { ...PIPELINE_PROFILES[pipeline].checkpoints };
}

function checkpointSummary(pipeline: Pipeline, checkpoints: CheckpointState): string {
  const gates = applicableGates(pipeline);
  const active = GATE_FIELDS.filter((gate) => gates.has(gate.key) && checkpoints[gate.key]).map(
    (gate) => gate.label,
  );
  return active.length === 0 ? 'No review checkpoints' : active.join(', ');
}

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

/** The authoritative server contract's ModelConfig JSON keys. */
function modelConfigKey(key: PhaseKey): string {
  return key === 'kbBuild' ? 'kb_build' : key;
}

function defaultModelsByKey(defaults: CreationDefaults): Partial<Record<PhaseKey, string>> {
  const byLabel = new Map(defaults.defaults.models.map(({ phase, model }) => [phase, model]));
  const result: Partial<Record<PhaseKey, string>> = {};
  for (const [label, key] of DEFAULT_MODEL_LABELS) {
    const model = byLabel.get(label);
    if (model !== undefined && model !== '') result[key] = model;
  }
  return result;
}

interface MentionToken {
  /** Index of the "@" character in the description. */
  start: number;
  query: string;
}

/** An "@token" immediately before the caret, at start-of-text or after whitespace. */
function detectMention(text: string, caret: number): MentionToken | null {
  const match = /(^|[\s([{])@([^\s@]*)$/.exec(text.slice(0, caret));
  if (match === null) return null;
  const query = match[2] ?? '';
  return { start: caret - query.length - 1, query };
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
  const [riskLevel, setRiskLevel] = useState<'low' | 'medium' | 'high'>('medium');
  const [inquireness, setInquireness] = useState<'none' | 'medium' | 'high'>('medium');
  const [exitCriteria, setExitCriteria] = useState('');
  const [images, setImages] = useState<readonly string[]>([]);
  const [attachments, setAttachments] = useState<readonly string[]>([]);
  const [repositoryFiles, setRepositoryFiles] = useState<readonly RepositoryFileRef[]>([]);
  const [mention, setMention] = useState<MentionToken | null>(null);
  const [mentionResults, setMentionResults] = useState<readonly RepositoryFileRef[]>([]);
  const [mentionIndex, setMentionIndex] = useState(0);
  const [mentionStatus, setMentionStatus] = useState<'idle' | 'searching'>('idle');
  const [attachMenuOpen, setAttachMenuOpen] = useState(false);
  const [autoStart, setAutoStart] = useState(true);
  const [directoryCandidate, setDirectoryCandidate] = useState<string | null>(null);
  const [initializeConsent, setInitializeConsent] = useState(false);
  const [workspacePending, setWorkspacePending] = useState(false);
  const [pending, setPending] = useState(false);
  const [nameError, setNameError] = useState<string | null>(null);
  const [repoError, setRepoError] = useState<string | null>(null);
  const [formError, setFormError] = useState<WizardError | null>(null);
  const catalogue = useModelCatalogue();
  const creationKey = useRef(crypto.randomUUID());
  const nameRef = useRef<HTMLInputElement | null>(null);
  const descriptionRef = useRef<HTMLTextAreaElement | null>(null);
  const repoGroupRef = useRef<HTMLFieldSetElement | null>(null);
  const formErrorRef = useRef<HTMLDivElement | null>(null);
  const attachMenuRef = useRef<HTMLDivElement | null>(null);

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

  useEffect(() => {
    if (!attachMenuOpen) return;
    const onPointerDown = (event: PointerEvent) => {
      if (!attachMenuRef.current?.contains(event.target as Node)) setAttachMenuOpen(false);
    };
    window.addEventListener('pointerdown', onPointerDown);
    return () => window.removeEventListener('pointerdown', onPointerDown);
  }, [attachMenuOpen]);

  useEffect(() => {
    if (mention === null || mention.query === '' || repoKeys.length === 0) {
      setMentionResults([]);
      setMentionStatus('idle');
      return;
    }
    const requestId = crypto.randomUUID();
    let dispatched = false;
    const timer = window.setTimeout(() => {
      dispatched = true;
      setMentionStatus('searching');
      window.agentico
        .searchCreationFiles({ requestId, repoKeys: [...repoKeys], query: mention.query })
        .then((result) => {
          if (!result.cancelled) {
            setMentionResults(result.files);
            setMentionIndex(0);
            setMentionStatus('idle');
          }
        })
        .catch((err: unknown) => setFormError(parseIpcError(err)));
    }, 120);
    return () => {
      window.clearTimeout(timer);
      if (dispatched) void window.agentico.cancelCreationFileSearch(requestId);
    };
  }, [mention, repoKeys]);

  const importFiles = (files: FileList | readonly File[]): void => {
    const list = Array.from(files);
    const photos = list.filter((file) => file.type.startsWith('image/'));
    const documents = list.filter((file) => !file.type.startsWith('image/'));
    if (photos.length > 0) {
      const imported = window.agentico.importDroppedCreationFiles('image', photos);
      setImages((items) => unique([...items, ...imported.paths]).slice(0, CREATION_IMAGE_LIMIT));
    }
    if (documents.length > 0) {
      const imported = window.agentico.importDroppedCreationFiles('attachment', documents);
      setAttachments((items) =>
        unique([...items, ...imported.paths]).slice(0, CREATION_ATTACHMENT_LIMIT),
      );
    }
  };

  const pickFiles = async (kind: 'image' | 'attachment'): Promise<void> => {
    setAttachMenuOpen(false);
    try {
      const result = await window.agentico.pickCreationFiles(kind);
      if (kind === 'image')
        setImages((current) =>
          unique([...current, ...result.paths]).slice(0, CREATION_IMAGE_LIMIT),
        );
      else
        setAttachments((current) =>
          unique([...current, ...result.paths]).slice(0, CREATION_ATTACHMENT_LIMIT),
        );
    } catch (err) {
      setFormError(parseIpcError(err));
    }
  };

  const browseDirectory = async (): Promise<void> => {
    try {
      const picked = await window.agentico.pickWorkspaceDirectory();
      if (picked.path !== null) {
        setDirectoryCandidate(picked.path);
        setInitializeConsent(false);
      }
    } catch (err) {
      setFormError(parseIpcError(err));
    }
  };

  const applyDirectory = async (action: 'add-root' | 'initialize'): Promise<void> => {
    if (directoryCandidate === null || (action === 'initialize' && !initializeConsent)) return;
    setWorkspacePending(true);
    try {
      const snapshot =
        action === 'add-root'
          ? await window.agentico.addWorkspaceRoot(directoryCandidate)
          : await window.agentico.initRepository({ path: directoryCandidate, consent: true });
      setState((current) =>
        current.phase === 'loaded'
          ? {
              phase: 'loaded',
              defaults: { ...current.defaults, repositories: snapshot.repositories },
            }
          : current,
      );
    } catch (err) {
      setFormError(parseIpcError(err));
    } finally {
      setWorkspacePending(false);
    }
  };

  const syncMention = (element: HTMLTextAreaElement): void => {
    setMention(detectMention(element.value, element.selectionStart));
  };

  const applyMention = (file: RepositoryFileRef): void => {
    if (mention === null) return;
    const reference = `@${file.repoKey}/${file.path}`;
    const tokenEnd = mention.start + mention.query.length + 1;
    setDescription((text) => `${text.slice(0, mention.start)}${reference} ${text.slice(tokenEnd)}`);
    setRepositoryFiles((files) =>
      files.some((item) => item.repoKey === file.repoKey && item.path === file.path)
        ? files
        : [...files, file].slice(0, CREATION_REPOSITORY_FILE_LIMIT),
    );
    const caret = mention.start + reference.length + 1;
    setMention(null);
    setMentionResults([]);
    requestAnimationFrame(() => {
      const element = descriptionRef.current;
      if (element !== null) {
        element.focus();
        element.setSelectionRange(caret, caret);
      }
    });
  };

  const onDescriptionKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>): void => {
    if (mention === null || mentionResults.length === 0) return;
    if (event.key === 'ArrowDown') {
      event.preventDefault();
      setMentionIndex((index) => (index + 1) % mentionResults.length);
    } else if (event.key === 'ArrowUp') {
      event.preventDefault();
      setMentionIndex((index) => (index - 1 + mentionResults.length) % mentionResults.length);
    } else if (event.key === 'Enter' || event.key === 'Tab') {
      event.preventDefault();
      const file = mentionResults[mentionIndex];
      if (file !== undefined) applyMention(file);
    } else if (event.key === 'Escape') {
      setMention(null);
      setMentionResults([]);
    }
  };

  const onComposerPaste = (event: ClipboardEvent): void => {
    if (event.clipboardData.files.length === 0) return;
    event.preventDefault();
    importFiles(event.clipboardData.files);
  };

  const onComposerDrop = (event: DragEvent): void => {
    event.preventDefault();
    importFiles(event.dataTransfer.files);
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
    for (const field of PHASE_FIELDS) {
      const chosen = modelChoices[field.key] ?? '';
      if (chosen !== '') models[modelConfigKey(field.key)] = chosen;
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
            // TUI parity: creation flows straight into setup + orchestration.
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
          <p className="home-surface__eyebrow">01 / Where</p>
          <h2 id="creation-where">Choose repositories</h2>
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
          <fieldset
            ref={repoGroupRef}
            tabIndex={-1}
            className="form-field form-field--group"
            aria-invalid={repoError !== null}
          >
            <legend className="form-field__label">Fresh workspace discovery</legend>
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
              {filteredRepositories.length === 0 ? (
                <li className="repo-option repo-option--empty">
                  No repositories match “{repoQuery.trim()}”.
                </li>
              ) : null}
            </ul>
            {repoError ? <p className="form-field__error">{repoError}</p> : null}
          </fieldset>
          <section className="directory-browser" aria-label="Add or initialize a repository">
            <div>
              <h3>Bring in another folder</h3>
              <button type="button" onClick={() => void browseDirectory()}>
                Browse for folder
              </button>
            </div>
            {directoryCandidate !== null ? (
              <>
                <code>{directoryCandidate}</code>
                <div className="directory-browser__actions">
                  <button
                    type="button"
                    disabled={workspacePending}
                    onClick={() => void applyDirectory('add-root')}
                  >
                    Add workspace root
                  </button>
                  <label>
                    <input
                      type="checkbox"
                      checked={initializeConsent}
                      onChange={(event) => setInitializeConsent(event.target.checked)}
                    />
                    Initialize this empty folder as a Git repository
                  </label>
                  <button
                    type="button"
                    disabled={!initializeConsent || workspacePending}
                    onClick={() => void applyDirectory('initialize')}
                  >
                    Initialize and rediscover
                  </button>
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
          <p className="home-surface__eyebrow">02 / What</p>
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
          <div
            className="composer"
            onDragOver={(event) => event.preventDefault()}
            onDrop={onComposerDrop}
          >
            <label className="form-field">
              <span className="form-field__label">Description</span>
              <textarea
                ref={descriptionRef}
                id="feature-description"
                className="form-field__input form-field__input--multiline"
                value={description}
                maxLength={10000}
                rows={6}
                placeholder="Describe the work. Type @ to reference files in the selected repositories; paste or drop images and files to attach them."
                onChange={(event) => {
                  setDescription(event.target.value);
                  syncMention(event.target);
                }}
                onSelect={(event) => syncMention(event.currentTarget)}
                onKeyDown={onDescriptionKeyDown}
                onPaste={onComposerPaste}
              />
            </label>
            {mention !== null && repoKeys.length > 0 ? (
              <div className="composer__mentions" role="listbox" aria-label="Repository files">
                {mention.query === '' ? (
                  <p className="composer__mentions-hint">Keep typing to search repository files…</p>
                ) : mentionStatus === 'searching' && mentionResults.length === 0 ? (
                  <p className="composer__mentions-hint" role="status">
                    Searching {repoKeys.join(', ')}…
                  </p>
                ) : mentionResults.length === 0 ? (
                  <p className="composer__mentions-hint">No files match “{mention.query}”.</p>
                ) : (
                  mentionResults.map((file, index) => (
                    <button
                      key={`${file.repoKey}:${file.path}`}
                      type="button"
                      role="option"
                      aria-selected={index === mentionIndex}
                      data-active={index === mentionIndex}
                      className="composer__mention-option"
                      onMouseEnter={() => setMentionIndex(index)}
                      onClick={() => applyMention(file)}
                    >
                      <b>{file.repoKey}</b>
                      <span>{file.path}</span>
                    </button>
                  ))
                )}
              </div>
            ) : null}
            <div className="composer__toolbar">
              <div className="composer__attach" ref={attachMenuRef}>
                <button
                  type="button"
                  className="composer__attach-button"
                  aria-label="Attach files or photos"
                  aria-haspopup="menu"
                  aria-expanded={attachMenuOpen}
                  onClick={() => setAttachMenuOpen((open) => !open)}
                >
                  +
                </button>
                {attachMenuOpen ? (
                  <div className="composer__attach-menu" role="menu">
                    <button type="button" role="menuitem" onClick={() => void pickFiles('image')}>
                      Add photos
                    </button>
                    <button
                      type="button"
                      role="menuitem"
                      onClick={() => void pickFiles('attachment')}
                    >
                      Add files
                    </button>
                  </div>
                ) : null}
              </div>
              <span className="composer__hint">
                Paste or drop images and documents anywhere in the description.
              </span>
            </div>
            {images.length > 0 || attachments.length > 0 ? (
              <ol className="composer__chips" aria-label="Attached files">
                {images.map((path) => (
                  <li key={path} className="composer__chip" data-kind="image">
                    <span>🖼 {basename(path)}</span>
                    <button
                      type="button"
                      aria-label={`Remove ${basename(path)}`}
                      onClick={() => setImages((items) => items.filter((item) => item !== path))}
                    >
                      ×
                    </button>
                  </li>
                ))}
                {attachments.map((path) => (
                  <li key={path} className="composer__chip" data-kind="attachment">
                    <span>📎 {basename(path)}</span>
                    <button
                      type="button"
                      aria-label={`Remove ${basename(path)}`}
                      onClick={() =>
                        setAttachments((items) => items.filter((item) => item !== path))
                      }
                    >
                      ×
                    </button>
                  </li>
                ))}
              </ol>
            ) : null}
            {repositoryFiles.length > 0 ? (
              <ol className="composer__chips" aria-label="Referenced repository files">
                {repositoryFiles.map((file) => (
                  <li
                    key={`${file.repoKey}:${file.path}`}
                    className="composer__chip"
                    data-kind="reference"
                  >
                    <span>
                      @{file.repoKey}/{file.path}
                    </span>
                    <button
                      type="button"
                      aria-label={`Remove reference ${file.repoKey}/${file.path}`}
                      onClick={() =>
                        setRepositoryFiles((files) =>
                          files.filter(
                            (item) => item.repoKey !== file.repoKey || item.path !== file.path,
                          ),
                        )
                      }
                    >
                      ×
                    </button>
                  </li>
                ))}
              </ol>
            ) : null}
          </div>
        </section>
      ) : null}

      {currentStep === 'Pipeline' ? (
        <section className="creation-wizard__panel" aria-labelledby="creation-pipeline">
          <p className="home-surface__eyebrow">03 / Pipeline</p>
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
          <p className="home-surface__eyebrow">04 / Review</p>
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
              {PHASE_FIELDS.map((field) => (
                <ModelPicker
                  key={field.key}
                  field={field}
                  value={modelChoices[field.key] ?? ''}
                  defaultModel={defaults[field.key] ?? ''}
                  catalogue={catalogue}
                  onChange={(model) =>
                    setModelChoices((choices) => ({ ...choices, [field.key]: model }))
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
                        // Roadmap review implies phase plan review (TUI linkage).
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
    </form>
  );
}

function basename(path: string): string {
  return path.split(/[\\/]/).pop() ?? 'Selected file';
}
function unique(items: readonly string[]): string[] {
  return [...new Set(items)];
}
function isPipeline(value: string | undefined): value is Pipeline {
  return value === 'medium' || value === 'large' || value === 'moonshot';
}
function normalizeInquireness(value: string | undefined): 'none' | 'medium' | 'high' {
  return value === 'none' || value === 'high' ? value : 'medium';
}
