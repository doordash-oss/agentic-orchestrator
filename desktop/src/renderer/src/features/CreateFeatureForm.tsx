/**
 * Four-step creation contract. Initial defaults prefill the draft once;
 * later repository discovery must preserve every user-owned choice.
 */
import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import {
  CREATION_ATTACHMENT_LIMIT,
  CREATION_FILE_SEARCH_RESULT_LIMIT,
  CREATION_IMAGE_LIMIT,
  CREATION_REPOSITORY_FILE_LIMIT,
  type CreationDefaults,
  type CreationFileKind,
  type RepositoryFileRef,
} from '../../../shared/ipc';
import { parseIpcError, type WizardError } from '../wizard/ipcError';
import { fieldForCreationError } from './featureView';

type DefaultsState =
  | { phase: 'loading' }
  | { phase: 'error'; error: WizardError }
  | { phase: 'loaded'; defaults: CreationDefaults };

const STEPS = ['What', 'Where', 'Pipeline', 'Review'] as const;
type Step = (typeof STEPS)[number];
type Pipeline = 'medium' | 'large' | 'moonshot';

const PIPELINE_PROFILES: Record<
  Pipeline,
  {
    title: string;
    note: string;
    checkpoints: {
      inquiryReview: boolean;
      researchReview: boolean;
      designReview: boolean;
      roadmapReview: boolean;
      phasePlanReview: boolean;
      manualPublish: boolean;
      draftPublish: boolean;
    };
  }
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

function checkpointsForPipeline(pipeline: Pipeline) {
  return PIPELINE_PROFILES[pipeline].checkpoints;
}

const CHECKPOINT_LABELS: ReadonlyArray<
  readonly [keyof ReturnType<typeof checkpointsForPipeline>, string]
> = [
  ['inquiryReview', 'Inquiry review'],
  ['researchReview', 'Research review'],
  ['designReview', 'Design review'],
  ['roadmapReview', 'Roadmap review'],
  ['phasePlanReview', 'Phase plan review'],
  ['manualPublish', 'Manual publication'],
  ['draftPublish', 'Draft publication'],
];

function checkpointSummaryForPipeline(pipeline: Pipeline): string {
  const checkpoints = checkpointsForPipeline(pipeline);
  return CHECKPOINT_LABELS.filter(([key]) => checkpoints[key])
    .map(([, label]) => label)
    .join(', ');
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
  const [useCurrentBranch, setUseCurrentBranch] = useState(false);
  const [pipeline, setPipeline] = useState<Pipeline>('medium');
  const [riskLevel, setRiskLevel] = useState<'low' | 'medium' | 'high'>('medium');
  const [inquireness, setInquireness] = useState<'none' | 'medium' | 'high'>('medium');
  const [exitCriteria, setExitCriteria] = useState('');
  const [images, setImages] = useState<readonly string[]>([]);
  const [attachments, setAttachments] = useState<readonly string[]>([]);
  const [repositoryFiles, setRepositoryFiles] = useState<readonly RepositoryFileRef[]>([]);
  const [fileQuery, setFileQuery] = useState('');
  const [fileResults, setFileResults] = useState<readonly RepositoryFileRef[]>([]);
  const [fileSearchStatus, setFileSearchStatus] = useState<'idle' | 'searching' | 'truncated'>(
    'idle',
  );
  const [directoryCandidate, setDirectoryCandidate] = useState<string | null>(null);
  const [initializeConsent, setInitializeConsent] = useState(false);
  const [workspacePending, setWorkspacePending] = useState(false);
  const [skills, setSkills] = useState<readonly { id: string; label: string }[]>([]);
  const [selectedSkills, setSelectedSkills] = useState<readonly string[]>([]);
  const [skillQuery, setSkillQuery] = useState('');
  const [pending, setPending] = useState(false);
  const [nameError, setNameError] = useState<string | null>(null);
  const [repoError, setRepoError] = useState<string | null>(null);
  const [formError, setFormError] = useState<WizardError | null>(null);
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
    Promise.all([window.agentico.getCreationDefaults(), window.agentico.listResources('skill')])
      .then(([defaults, catalogue]) => {
        setState({ phase: 'loaded', defaults });
        setUseCurrentBranch(defaults.defaults.useCurrentBranch);
        if (isPipeline(defaults.defaults.pipeline)) setPipeline(defaults.defaults.pipeline);
        setInquireness(normalizeInquireness(defaults.defaults.inquireness));
        setSkills(
          catalogue.resources
            .filter((entry) => entry.kind === 'skill')
            .map((entry) => ({ id: entry.id, label: entry.label })),
        );
      })
      .catch((err: unknown) => setState({ phase: 'error', error: parseIpcError(err) }));
  }, []);

  useEffect(loadInitialDefaults, [loadInitialDefaults]);
  useEffect(() => {
    if (formError !== null) formErrorRef.current?.focus();
  }, [formError]);

  useEffect(() => {
    if (repoKeys.length === 0 || fileQuery.trim() === '') {
      setFileResults([]);
      setFileSearchStatus('idle');
      return;
    }
    const requestId = crypto.randomUUID();
    let dispatched = false;
    const timer = window.setTimeout(() => {
      dispatched = true;
      setFileSearchStatus('searching');
      window.agentico
        .searchCreationFiles({ requestId, repoKeys: [...repoKeys], query: fileQuery })
        .then((result) => {
          if (!result.cancelled) {
            setFileResults(result.files);
            setFileSearchStatus(result.truncated ? 'truncated' : 'idle');
          }
        })
        .catch((err: unknown) => setFormError(parseIpcError(err)));
    }, 120);
    return () => {
      window.clearTimeout(timer);
      if (dispatched) void window.agentico.cancelCreationFileSearch(requestId);
    };
  }, [fileQuery, repoKeys]);

  const pickFiles = async (kind: CreationFileKind): Promise<void> => {
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

  const validateStep = (index: number): boolean => {
    setNameError(null);
    setRepoError(null);
    if (index === 0 && name.trim() === '') {
      setNameError('Enter a feature name.');
      nameRef.current?.focus();
      return false;
    }
    if (index === 1 && repoKeys.length === 0) {
      setRepoError('Select at least one repository.');
      repoGroupRef.current?.focus();
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
    const models = Object.fromEntries(
      state.defaults.defaults.models.map(({ phase, model }) => [modelConfigKey(phase), model]),
    );
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
          checkpoints: checkpointsForPipeline(pipeline),
          skills: [...selectedSkills],
          idempotencyKey: creationKey.current,
        });
        try {
          await window.agentico.dispatchFeatureSetup(created.featureId);
        } catch {
          /* cockpit owns retry */
        }
        onCreated({ featureId: created.featureId, name: name.trim() });
      } catch (err) {
        const parsed = parseIpcError(err);
        const field = fieldForCreationError(parsed);
        if (field === 'name') {
          setStepIndex(0);
          setNameError(parsed.message);
          nameRef.current?.focus();
        } else if (field === 'repos') {
          setStepIndex(1);
          setRepoError(parsed.message);
          repoGroupRef.current?.focus();
        } else setFormError(parsed);
      } finally {
        setPending(false);
      }
    })();
  };

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

  const repositories = state.defaults.repositories;
  const visibleSkills = skills
    .filter((skill) =>
      `${skill.label} ${skill.id}`.toLowerCase().includes(skillQuery.trim().toLowerCase()),
    )
    .slice(0, 50);
  const currentStep = STEPS[stepIndex] as Step;

  return (
    <form
      className="create-form creation-wizard"
      aria-label="Create a feature"
      noValidate
      onSubmit={submit}
    >
      <nav className="creation-wizard__spine" aria-label="Creation steps">
        {STEPS.map((step, index) => (
          <button
            key={step}
            type="button"
            aria-current={index === stepIndex ? 'step' : undefined}
            disabled={index > stepIndex}
            onClick={() => setStepIndex(index)}
          >
            <span>{index + 1}</span>
            {step}
          </button>
        ))}
      </nav>
      {formError !== null ? (
        <div ref={formErrorRef} tabIndex={-1} role="alert" className="create-form__error">
          <b>{formError.code}</b>
          <p>{formError.message}</p>
        </div>
      ) : null}

      {currentStep === 'What' ? (
        <section className="creation-wizard__panel" aria-labelledby="creation-what">
          <p className="home-surface__eyebrow">01 / What</p>
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
          <label className="form-field">
            <span className="form-field__label">Description</span>
            <textarea
              id="feature-description"
              className="form-field__input form-field__input--multiline"
              value={description}
              maxLength={10000}
              rows={6}
              onChange={(event) => setDescription(event.target.value)}
            />
          </label>
          <FileShelf
            label="Images"
            kind="image"
            paths={images}
            onChoose={() => void pickFiles('image')}
            onImport={(paths) =>
              setImages((items) => unique([...items, ...paths]).slice(0, CREATION_IMAGE_LIMIT))
            }
            onRemove={(path) => setImages((items) => items.filter((item) => item !== path))}
          />
          <FileShelf
            label="Attachments"
            kind="attachment"
            paths={attachments}
            onChoose={() => void pickFiles('attachment')}
            onImport={(paths) =>
              setAttachments((items) =>
                unique([...items, ...paths]).slice(0, CREATION_ATTACHMENT_LIMIT),
              )
            }
            onRemove={(path) => setAttachments((items) => items.filter((item) => item !== path))}
          />
          <RepositoryFilePicker
            repoKeys={repoKeys}
            query={fileQuery}
            onQuery={setFileQuery}
            status={fileSearchStatus}
            results={fileResults}
            selected={repositoryFiles}
            onSelected={setRepositoryFiles}
          />
        </section>
      ) : null}

      {currentStep === 'Where' ? (
        <section className="creation-wizard__panel" aria-labelledby="creation-where">
          <p className="home-surface__eyebrow">02 / Where</p>
          <h2 id="creation-where">Choose repositories</h2>
          <fieldset
            ref={repoGroupRef}
            tabIndex={-1}
            className="form-field form-field--group"
            aria-invalid={repoError !== null}
          >
            <legend className="form-field__label">Fresh workspace discovery</legend>
            <ul className="repo-options">
              {repositories.map((repo) => (
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
                  onChange={() => setPipeline(profile.id)}
                />
                <b>{profile.title}</b>
                <span>{profile.note}</span>
                <small>{checkpointSummaryForPipeline(profile.id)}</small>
              </label>
            ))}
          </div>
        </section>
      ) : null}

      {currentStep === 'Review' ? (
        <section className="creation-wizard__panel" aria-labelledby="creation-review">
          <p className="home-surface__eyebrow">04 / Review</p>
          <h2 id="creation-review">Review the run contract</h2>
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
              onChange={(event) => setExitCriteria(event.target.value)}
            />
          </label>
          <section className="review-contract" aria-label="Models and checkpoints">
            <div>
              <h3>Eligible model defaults</h3>
              <ul>
                {state.defaults.defaults.models.map((entry) => (
                  <li key={entry.phase}>
                    <span>{entry.phase}</span>
                    <code>{entry.model}</code>
                  </li>
                ))}
              </ul>
            </div>
            <div>
              <h3>Effective checkpoints</h3>
              <p>{checkpointSummaryForPipeline(pipeline)}</p>
            </div>
          </section>
          <section aria-label="Skills" className="skill-picker">
            <label>
              Search skills
              <input value={skillQuery} onChange={(event) => setSkillQuery(event.target.value)} />
            </label>
            <ul>
              {visibleSkills.map((skill) => (
                <li key={skill.id}>
                  <label>
                    <input
                      type="checkbox"
                      checked={selectedSkills.includes(skill.id)}
                      onChange={() =>
                        setSelectedSkills((items) =>
                          items.includes(skill.id)
                            ? items.filter((item) => item !== skill.id)
                            : [...items, skill.id],
                        )
                      }
                    />
                    {skill.label}
                  </label>
                </li>
              ))}
            </ul>
          </section>
          <dl className="creation-summary">
            <div>
              <dt>What</dt>
              <dd>{name}</dd>
            </div>
            <div>
              <dt>Where</dt>
              <dd>{repoKeys.join(', ')}</dd>
            </div>
            <div>
              <dt>Pipeline</dt>
              <dd>{pipeline}</dd>
            </div>
            <div>
              <dt>Review</dt>
              <dd>
                {riskLevel} risk · {selectedSkills.length} skills
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
            {pending ? 'Creating…' : 'Create feature'}
          </button>
        )}
      </footer>
    </form>
  );
}

function modelConfigKey(phase: string): string {
  const normalized = phase.toLowerCase().replaceAll(' ', '_');
  // The UI uses a human-facing label while the authoritative server contract
  // retains the established ModelConfig JSON key.
  return normalized === 'knowledge_base' ? 'kb_build' : normalized;
}

function RepositoryFilePicker({
  repoKeys,
  query,
  onQuery,
  status,
  results,
  selected,
  onSelected,
}: {
  repoKeys: readonly string[];
  query: string;
  onQuery(value: string): void;
  status: 'idle' | 'searching' | 'truncated';
  results: readonly RepositoryFileRef[];
  selected: readonly RepositoryFileRef[];
  onSelected(value: readonly RepositoryFileRef[]): void;
}) {
  return (
    <section className="repository-file-picker" aria-label="Repository files">
      <label>
        Add repository files
        <input
          value={query}
          disabled={repoKeys.length === 0}
          placeholder="Fuzzy search selected repositories"
          onChange={(event) => onQuery(event.target.value)}
        />
      </label>
      {repoKeys.length === 0 ? (
        <p>Select repositories in Where, then return here to add scoped files.</p>
      ) : null}
      {status === 'searching' ? <p role="status">Searching bounded repository indexes…</p> : null}
      {status === 'truncated' ? (
        <p>Showing the best {CREATION_FILE_SEARCH_RESULT_LIMIT} bounded matches.</p>
      ) : null}
      <ul>
        {results.map((file) => {
          const checked = selected.some(
            (item) => item.repoKey === file.repoKey && item.path === file.path,
          );
          return (
            <li key={`${file.repoKey}:${file.path}`}>
              <label>
                <input
                  type="checkbox"
                  checked={checked}
                  disabled={!checked && selected.length >= CREATION_REPOSITORY_FILE_LIMIT}
                  onChange={() =>
                    onSelected(
                      checked
                        ? selected.filter(
                            (item) => item.repoKey !== file.repoKey || item.path !== file.path,
                          )
                        : [...selected, file].slice(0, CREATION_REPOSITORY_FILE_LIMIT),
                    )
                  }
                />
                <b>{file.repoKey}</b>
                <span>{file.path}</span>
              </label>
            </li>
          );
        })}
      </ul>
    </section>
  );
}

function FileShelf({
  label,
  kind,
  paths,
  onChoose,
  onImport,
  onRemove,
}: {
  label: string;
  kind: CreationFileKind;
  paths: readonly string[];
  onChoose(): void;
  onImport(paths: readonly string[]): void;
  onRemove(path: string): void;
}) {
  const importFiles = (files: FileList): void => {
    const imported = window.agentico.importDroppedCreationFiles(kind, Array.from(files));
    onImport(imported.paths);
  };
  return (
    <section
      className="file-shelf"
      aria-label={label}
      onDragOver={(event) => event.preventDefault()}
      onDrop={(event) => {
        event.preventDefault();
        importFiles(event.dataTransfer.files);
      }}
      onPaste={(event) => importFiles(event.clipboardData.files)}
      tabIndex={0}
    >
      <div>
        <h3>{label}</h3>
        <button type="button" onClick={onChoose}>
          Choose {label.toLowerCase()}
        </button>
      </div>
      {paths.length === 0 ? (
        <p>No {label.toLowerCase()} selected.</p>
      ) : (
        <ol>
          {paths.map((path) => (
            <li key={path}>
              <span>{basename(path)}</span>
              <button
                type="button"
                aria-label={`Remove ${basename(path)}`}
                onClick={() => onRemove(path)}
              >
                Remove
              </button>
            </li>
          ))}
        </ol>
      )}
    </section>
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
