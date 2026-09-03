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
 * Refactor child wizard, at parity with feature creation minus the Where
 * step: repositories are inherited from the parent (read-only), and every
 * Review axis — models, effort, checkpoints, risk, inquireness, exit
 * criteria — is seeded from the parent's current configuration. The
 * pipeline stays the child's independent
 * choice and never clobbers the seeded axes.
 */
import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import type {
  EffortLevel,
  FeatureConfigSnapshot,
  FeatureSnapshot,
  RepositoryFileRef,
} from '../../../../shared/ipc';
import { ErrorSurface } from '../../components/ErrorSurface';
import { FieldError, fieldAriaDescribedBy, fieldAriaInvalid } from '../../components/FieldError';
import { retryAction, useConnectionState, useIpcLoad } from '../../hooks';
import { parseIpcError } from '../../wizard/ipcError';
import type { CanonicalError } from '../../../../shared/ipc';
import {
  isBlockingStagedItem,
  STAGED_ITEMS_BLOCK_SUBMIT,
  submittableReferences,
  type ComposerUploadItem,
} from '../stagedItems';
import {
  GATE_FIELDS,
  ModelEffortRow,
  applicableGates,
  applicablePhaseFields,
  useModelCatalogue,
  type PhaseKey,
} from '../ConfigEditor';
import { DescriptionComposer } from '../DescriptionComposer';
import {
  PIPELINES,
  checkpointSummary,
  isPipeline,
  modelConfigKey,
  type CheckpointState,
  type Pipeline,
} from '../runContract';
const STEPS = ['What', 'Pipeline', 'Review'] as const;

export interface RefactorLauncherProps {
  featureId: string;
  snapshot: FeatureSnapshot;
  onCancel(): void;
  onDispatched(launch: { childId: string; autoStart: boolean }): void;
}

export function RefactorLauncher({
  featureId,
  snapshot,
  onCancel,
  onDispatched,
}: RefactorLauncherProps): React.ReactElement {
  const [stepIndex, setStepIndex] = useState(0);
  const [name, setName] = useState(`Refactor ${snapshot.name}`);
  const [description, setDescription] = useState('');
  const [images, setImages] = useState<readonly string[]>([]);
  const [attachments, setAttachments] = useState<readonly string[]>([]);
  const [imageUploads, setImageUploads] = useState<readonly ComposerUploadItem[]>([]);
  const [attachmentUploads, setAttachmentUploads] = useState<readonly ComposerUploadItem[]>([]);
  const [repositoryFiles, setRepositoryFiles] = useState<readonly RepositoryFileRef[]>([]);
  const [pipeline, setPipeline] = useState<Pipeline>(
    isPipeline(snapshot.pipeline) ? snapshot.pipeline : 'medium',
  );
  const [checkpoints, setCheckpoints] = useState<CheckpointState>({
    inquiryReview: false,
    researchReview: false,
    designReview: false,
    roadmapReview: true,
    phasePlanReview: true,
    manualPublish: false,
    draftPublish: false,
  });
  const [modelChoices, setModelChoices] = useState<Partial<Record<PhaseKey, string>>>({});
  const [effortChoices, setEffortChoices] = useState<Partial<Record<PhaseKey, EffortLevel>>>({});
  const [riskLevel, setRiskLevel] = useState<'low' | 'medium' | 'high'>(
    isRiskLevel(snapshot.riskLevel) ? snapshot.riskLevel : 'medium',
  );
  const [inquireness, setInquireness] = useState<'none' | 'medium' | 'high'>('medium');
  const [exitCriteria, setExitCriteria] = useState(snapshot.exitCriteria ?? '');
  const [pending, setPending] = useState(false);
  const [autoStart, setAutoStart] = useState(true);
  const [nameError, setNameError] = useState<string | null>(null);
  const [formError, setFormError] = useState<CanonicalError | null>(null);
  const catalogue = useModelCatalogue();
  const nameRef = useRef<HTMLInputElement | null>(null);
  const formErrorRef = useRef<HTMLDivElement | null>(null);
  const connection = useConnectionState();
  const serverKey = connection.status === 'ready' ? (connection.serverKey ?? null) : null;
  // In-progress, failed, or foreign-server uploads block launch until they
  // are removed (or the user switches back to the server that holds them).
  const uploadsBlocking = [...imageUploads, ...attachmentUploads].some((item) =>
    isBlockingStagedItem(item, serverKey),
  );

  const loadSeed = useCallback(
    (): Promise<FeatureConfigSnapshot> => window.agentico.getFeatureConfig(featureId),
    [featureId],
  );
  const { state: seed, reload: reloadSeed } = useIpcLoad(loadSeed, [featureId]);

  useEffect(() => {
    if (seed.phase !== 'loaded') return;
    const config = seed.data;
    // The parent's current configuration is the authoritative seed for
    // every paired-review axis; the effective workspace defaults label
    // the untouched rows, exactly as in the parent's config editor.
    const choices: Partial<Record<PhaseKey, string>> = {};
    const efforts: Partial<Record<PhaseKey, EffortLevel>> = {};
    for (const key of Object.keys(config.current.models) as PhaseKey[]) {
      const model = config.current.models[key];
      if (model !== undefined && model !== '') choices[key] = model;
    }
    for (const key of Object.keys(config.current.effort) as Array<
      keyof typeof config.current.effort
    >) {
      const effort = config.current.effort[key];
      if (effort !== undefined) efforts[key] = effort;
    }
    setModelChoices(choices);
    setEffortChoices(efforts);
    setInquireness(config.current.inquireness);
    setCheckpoints({ ...config.current.checkpoints });
    if (isPipeline(config.current.pipeline)) setPipeline(config.current.pipeline);
  }, [seed]);

  useEffect(() => {
    if (formError !== null) formErrorRef.current?.focus();
  }, [formError]);

  const validateName = (): boolean => {
    if (name.trim() !== '') return true;
    setNameError('Enter a child name.');
    nameRef.current?.focus();
    return false;
  };

  const next = (): void => {
    if (stepIndex === 0 && !validateName()) return;
    setStepIndex((current) => Math.min(current + 1, STEPS.length - 1));
  };

  const submit = (event: FormEvent): void => {
    event.preventDefault();
    if (pending || uploadsBlocking || seed.phase !== 'loaded') return;
    if (!validateName()) {
      setStepIndex(0);
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
    const imageRefs = submittableReferences(imageUploads, 'image', serverKey);
    const attachmentRefs = submittableReferences(attachmentUploads, 'attachment', serverKey);
    void (async () => {
      try {
        const launched = await window.agentico.launchRefactorChild({
          parentId: featureId,
          name: name.trim(),
          ...(description.trim() === '' ? {} : { description }),
          ...(images.length === 0 ? {} : { images: [...images] }),
          ...(attachments.length === 0 ? {} : { attachments: [...attachments] }),
          ...(imageRefs.length === 0 ? {} : { imageUploads: imageRefs }),
          ...(attachmentRefs.length === 0 ? {} : { attachmentUploads: attachmentRefs }),
          ...(repositoryFiles.length === 0 ? {} : { repositoryFiles: [...repositoryFiles] }),
          pipeline,
          riskLevel,
          inquireness,
          ...(exitCriteria.trim() === '' ? {} : { exitCriteria: exitCriteria.trim() }),
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
        });
        onDispatched({ childId: launched.childId, autoStart });
        onCancel();
      } catch (err) {
        // Keep every field intact so dirty-parent remediation is a retry, not a restart.
        setFormError(parseIpcError(err));
      } finally {
        setPending(false);
      }
    })();
  };

  if (seed.phase === 'loading')
    return (
      <section className="refactor-wizard" aria-label="Start refactor">
        <p role="status">Loading the parent’s run contract…</p>
      </section>
    );
  if (seed.phase === 'error')
    return (
      <section className="refactor-wizard" aria-label="Start refactor">
        <ErrorSurface error={seed.error} variant="compact" localAction={retryAction(reloadSeed)} />
      </section>
    );

  const currentStep = STEPS[stepIndex];
  const gates = applicableGates(pipeline);
  const visibleGates = GATE_FIELDS.filter((gate) => gates.has(gate.key));
  const modelDefaults = seed.data.defaults.models;
  const effortDefaults = seed.data.defaults.effort;

  return (
    <form
      className="create-form creation-wizard refactor-wizard"
      aria-label="Start refactor"
      noValidate
      onSubmit={submit}
    >
      <section className="refactor-wizard__inherited" aria-label="Inherited repositories">
        <p className="eyebrow-label">Where · Inherited from {snapshot.name}</p>
        <ul className="refactor-wizard__repos">
          {snapshot.repos.map((repo) => (
            <li key={repo}>
              <code>{repo}</code>
            </li>
          ))}
        </ul>
      </section>
      <nav className="creation-wizard__spine" aria-label="Refactor steps">
        {STEPS.map((step, index) => {
          const state = index < stepIndex ? 'done' : index === stepIndex ? 'current' : 'upcoming';
          return (
            <button
              key={step}
              type="button"
              data-state={state}
              aria-current={index === stepIndex ? 'step' : undefined}
              disabled={index > stepIndex || pending}
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
        <ErrorSurface
          error={formError}
          variant="compact"
          rootRef={formErrorRef}
          rootTabIndex={-1}
        />
      ) : null}

      {currentStep === 'What' ? (
        <section className="creation-wizard__panel" aria-labelledby="refactor-what">
          <p className="eyebrow-label">01 / What</p>
          <h2 id="refactor-what">Define the refactor</h2>
          <label className="form-field">
            <span className="form-field__label">Child name</span>
            <input
              ref={nameRef}
              autoFocus
              id="refactor-child-name"
              className="form-field__input"
              value={name}
              maxLength={200}
              aria-invalid={fieldAriaInvalid(nameError !== null)}
              aria-describedby={fieldAriaDescribedBy(
                'refactor-child-name-error',
                nameError !== null,
              )}
              onChange={(event) => {
                setName(event.target.value);
                setNameError(null);
              }}
            />
            <FieldError id="refactor-child-name-error" message={nameError} />
          </label>
          <DescriptionComposer
            id="refactor-brief"
            label="Brief"
            placeholder="Describe the refactor. Type @ to reference files in the inherited repositories; paste or drop files to attach them."
            value={description}
            repoKeys={snapshot.repos}
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
        </section>
      ) : null}

      {currentStep === 'Pipeline' ? (
        <section className="creation-wizard__panel" aria-labelledby="refactor-pipeline">
          <p className="eyebrow-label">02 / Pipeline</p>
          <h2 id="refactor-pipeline">Set the depth</h2>
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
                  // The pipeline is the child's independent choice; the
                  // parent-seeded checkpoints stay authoritative.
                  onChange={() => setPipeline(profile.id)}
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
        <section className="creation-wizard__panel" aria-labelledby="refactor-review">
          <p className="eyebrow-label">03 / Review</p>
          <h2 id="refactor-review">Review the run contract</h2>
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
                placeholder="What must be true for this refactor to be considered done?"
                onChange={(event) => setExitCriteria(event.target.value)}
              />
            </label>
          </div>
          <section className="review-contract" aria-label="Models and checkpoints">
            <fieldset className="config-editor__group">
              <legend className="config-editor__group-title">Models</legend>
              <p className="config-editor__group-desc">
                Seeded from {snapshot.name}’s configuration; the child keeps its own copy. Only
                models available from provider discovery can be selected.
              </p>
              <div className="config-editor__phase-rows">
                {applicablePhaseFields(pipeline, false).map((field) => (
                  <ModelEffortRow
                    key={field.key}
                    field={field}
                    modelValue={modelChoices[field.key] ?? ''}
                    defaultModel={modelDefaults[field.key] ?? ''}
                    effortValue={effortChoices[field.key]}
                    defaultEffort={
                      field.key === 'automaticReview' ? undefined : effortDefaults[field.key]
                    }
                    catalogue={catalogue}
                    pipeline={pipeline}
                    onModelChange={(model, resetEffort) => {
                      setModelChoices((choices) => ({ ...choices, [field.key]: model }));
                      if (resetEffort !== undefined) {
                        setEffortChoices((choices) => ({ ...choices, [field.key]: resetEffort }));
                      }
                    }}
                    onEffortChange={(effort) =>
                      setEffortChoices((choices) => ({ ...choices, [field.key]: effort }))
                    }
                  />
                ))}
              </div>
            </fieldset>
            <fieldset className="config-editor__group">
              <legend className="config-editor__group-title">Review checkpoints</legend>
              <p className="config-editor__group-desc">
                Checkpoints pause the child for your review before continuing. The {pipeline}{' '}
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
              <span>Begin the first phase as soon as the pass&apos;s worktrees are ready.</span>
            </span>
          </label>
          <dl className="creation-summary">
            <div>
              <dt>Where</dt>
              <dd>{snapshot.repos.join(', ')} (inherited)</dd>
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
          <button type="button" disabled={pending} onClick={() => setStepIndex((c) => c - 1)}>
            Back
          </button>
        ) : (
          <button type="button" disabled={pending} onClick={onCancel}>
            Cancel
          </button>
        )}
        {stepIndex < STEPS.length - 1 ? (
          <button
            key="next-step"
            type="button"
            className="create-form__submit"
            onClick={(event) => {
              // React can reuse this DOM node as the submit button when the
              // click advances to Review. Cancel the original
              // button's browser default before that type transition occurs.
              event.preventDefault();
              next();
            }}
          >
            Next: {STEPS[stepIndex + 1]}
          </button>
        ) : (
          <>
            {uploadsBlocking ? (
              <span className="creation-wizard__blocking-note" role="status">
                {STAGED_ITEMS_BLOCK_SUBMIT}
              </span>
            ) : null}
            <button
              key="launch-child"
              type="submit"
              className="create-form__submit"
              disabled={pending || uploadsBlocking}
            >
              {pending ? 'Launching…' : autoStart ? 'Launch and start' : 'Launch child'}
            </button>
          </>
        )}
      </footer>
    </form>
  );
}

function isRiskLevel(value: string | undefined): value is 'low' | 'medium' | 'high' {
  return value === 'low' || value === 'medium' || value === 'high';
}
