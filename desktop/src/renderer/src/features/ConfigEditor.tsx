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
 * Structured configuration editor: models per phase, behavior (inquireness,
 * input alerts), and gates. Two variants share one form:
 *
 *  - `FeatureConfigPanel` edits a single feature's config through
 *    getFeatureConfig/updateFeatureConfig.
 *  - `WorkspaceDefaultsPanel` edits workspace-wide defaults (plus the
 *    Utilities model) through getWorkspaceDefaults/updateWorkspaceDefaults.
 *
 * Each phase is one compact row of a grouped list — name and hint leading,
 * the model and effort pickers trailing — driven by the server's model
 * catalogue. Model options are grouped by provider and effort options stay
 * capability-aware, while untouched values name the effective defaults.
 */
import { useCallback, useEffect, useId, useMemo, useState, type ReactNode } from 'react';
import type {
  AutomaticReviewMode,
  Checkpoints,
  EffortLevel,
  FeatureConfig,
  Inquireness,
  InputNotificationsMode,
  ModelCatalogue,
  PhaseEffort,
  PhaseModels,
  WorkspaceDefaults,
} from '../../../shared/ipc';
import { parseIpcError } from '../wizard/ipcError';

export type PhaseKey = keyof PhaseModels;

type PhaseField =
  | {
      key: keyof PhaseEffort;
      label: string;
      role: string;
      hint: string;
      workspaceOnly?: boolean;
      supportsEffort?: true;
    }
  | {
      key: 'automaticReview';
      label: string;
      role: string;
      hint: string;
      workspaceOnly: true;
      supportsEffort: false;
    };

/** Display order and catalogue-role mapping for the per-phase model rows. */
export const PHASE_FIELDS: ReadonlyArray<PhaseField> = [
  { key: 'inquiry', label: 'Clarify', role: 'inquiry', hint: 'Planning questions and intake' },
  { key: 'research', label: 'Research', role: 'research', hint: 'Codebase and context research' },
  { key: 'planning', label: 'Planning', role: 'planning', hint: 'Roadmaps and phase plans' },
  {
    key: 'implementation',
    label: 'Implementation',
    role: 'implementation',
    hint: 'Writing and revising code',
  },
  { key: 'review', label: 'Review', role: 'review', hint: 'Reviewing implementation output' },
  { key: 'utilities', label: 'Utilities', role: 'chat', hint: 'Chat and workspace utilities' },
  { key: 'kbBuild', label: 'KB Build', role: 'kb_build', hint: 'Knowledge base construction' },
  {
    key: 'automaticReview',
    label: 'Auto mode reviewer',
    role: 'automatic_review',
    hint: 'Model that reviews shell commands when auto mode is on',
    workspaceOnly: true,
    supportsEffort: false,
  },
];

const INQUIRENESS_OPTIONS: ReadonlyArray<{
  value: Inquireness;
  label: string;
  hint: string;
}> = [
  { value: 'none', label: 'None', hint: 'Ask only when input is required' },
  { value: 'medium', label: 'Medium', hint: 'Surface key planning questions' },
  { value: 'high', label: 'High', hint: 'Surface more planning questions' },
];

interface GateField {
  key: keyof Checkpoints;
  label: string;
  hint: string;
}

export const GATE_FIELDS: ReadonlyArray<GateField> = [
  { key: 'inquiryReview', label: 'Inquiry review', hint: 'Pause after inquiry, before research' },
  { key: 'researchReview', label: 'Research review', hint: 'Pause after research, before design' },
  { key: 'designReview', label: 'Design review', hint: 'Pause after design, before planning' },
  {
    key: 'roadmapReview',
    label: 'Roadmap review',
    hint: 'Pause after the roadmap, before phase planning',
  },
  {
    key: 'phasePlanReview',
    label: 'Phase plan review',
    hint: 'Pause after each phase plan, before implementation',
  },
  {
    key: 'manualPublish',
    label: 'Manual publish',
    hint: 'Review the diff and PR before publishing',
  },
];

/**
 * Phase rows that apply to a pipeline. Workspace scope adds Utilities and the
 * workspace-only rows; feature scope (config editor, creation wizard) shows
 * only the phases the chosen pipeline runs.
 */
export function applicablePhaseFields(
  pipeline: string,
  workspaceScope: boolean,
): ReadonlyArray<PhaseField> {
  return PHASE_FIELDS.filter((field) => {
    if (field.workspaceOnly === true) return workspaceScope;
    if (pipeline === 'medium') {
      return field.key === 'planning' || field.key === 'implementation' || field.key === 'review';
    }
    return workspaceScope || field.key !== 'utilities';
  });
}

/** Gates that apply per pipeline profile (mirrors the server's rules). */
export function applicableGates(pipeline: string): ReadonlySet<keyof Checkpoints> {
  if (pipeline === 'medium') {
    return new Set(['roadmapReview', 'phasePlanReview', 'manualPublish']);
  }
  return new Set([
    'inquiryReview',
    'researchReview',
    'designReview',
    'roadmapReview',
    'phasePlanReview',
    'manualPublish',
  ]);
}

function modelDisplayName(catalogue: ModelCatalogue | null, provider: string, id: string): string {
  const info = catalogue?.providerModels[provider]?.find((m) => m.id === id);
  return info?.displayName !== undefined && info.displayName !== '' ? info.displayName : id;
}

/**
 * The stored selection is `provider:model` when more than one provider is
 * detected (matching dispatch resolution), else the bare model id.
 */
function selectionValue(catalogue: ModelCatalogue | null, provider: string, id: string): string {
  if (catalogue !== null && catalogue.providerOrder.length > 1) return `${provider}:${id}`;
  return id;
}

function catalogueModelForSelection(catalogue: ModelCatalogue | null, value: string) {
  if (catalogue === null || value === '') return undefined;
  for (const provider of catalogue.providerOrder) {
    const model = (catalogue.providerModels[provider] ?? []).find((candidate) =>
      [candidate.id, ...(candidate.aliases ?? [])].some(
        (candidateValue) =>
          value === candidateValue || value === selectionValue(catalogue, provider, candidateValue),
      ),
    );
    if (model !== undefined) return { provider, model };
  }
  return undefined;
}

interface ModelPickerProps {
  field: (typeof PHASE_FIELDS)[number];
  value: string;
  defaultModel: string;
  catalogue: ModelCatalogue | null;
  onChange(value: string): void;
}

export function ModelPicker({ field, value, defaultModel, catalogue, onChange }: ModelPickerProps) {
  const groups = useMemo(() => {
    if (catalogue === null) return [];
    const eligibleByProvider = catalogue.phaseProviderModels[field.role] ?? {};
    return catalogue.providerOrder.flatMap((provider) => {
      const eligible = eligibleByProvider[provider] ?? [];
      const all = catalogue.providerModels[provider] ?? [];
      // Eligible ids first (server-ordered), then any remaining provider
      // models so a persisted off-role value stays representable.
      const ordered = [...eligible, ...all.map((m) => m.id).filter((id) => !eligible.includes(id))];
      if (ordered.length === 0) return [];
      return [{ provider, ids: ordered }];
    });
  }, [catalogue, field.role]);

  const recommended = catalogue?.phaseDefaults[field.key];
  const canonicalValues = new Set(
    groups.flatMap((g) => g.ids.map((id) => selectionValue(catalogue, g.provider, id))),
  );
  const automaticReviewer = field.key === 'automaticReview';
  const effectiveDefault = defaultModel === '' ? 'server default' : defaultModel;
  const defaultUnavailable =
    catalogue !== null &&
    defaultModel !== '' &&
    catalogueModelForSelection(catalogue, defaultModel) === undefined;
  const selectedAlias =
    value !== '' && !canonicalValues.has(value)
      ? catalogueModelForSelection(catalogue, value)
      : undefined;
  const selectedCanonical =
    value !== '' && canonicalValues.has(value)
      ? catalogueModelForSelection(catalogue, value)
      : undefined;
  // The select clamps long labels with an ellipsis; the title carries the
  // full label of the current choice.
  const selectedLabel =
    value === ''
      ? automaticReviewer
        ? 'Automatic — Claude → OpenCode → Codex'
        : `Default — ${effectiveDefault}${defaultUnavailable ? ' (unavailable)' : ''}`
      : canonicalValues.has(value)
        ? selectedCanonical === undefined
          ? value
          : modelDisplayName(catalogue, selectedCanonical.provider, selectedCanonical.model.id)
        : selectedAlias === undefined
          ? `${value} (unavailable)`
          : `${modelDisplayName(catalogue, selectedAlias.provider, selectedAlias.model.id)} — ${value} (alias)`;

  return (
    <select
      className="config-editor__pick config-editor__pick--model"
      aria-label={`${field.label} model`}
      title={selectedLabel}
      value={value}
      onChange={(event) => onChange(event.target.value)}
    >
      <option value="">
        {automaticReviewer ? (
          <>Automatic — Claude → OpenCode → Codex</>
        ) : (
          <>
            Default — {effectiveDefault}
            {defaultUnavailable ? ' (unavailable)' : ''}
          </>
        )}
      </option>
      {value !== '' && !canonicalValues.has(value) ? (
        <option value={value}>
          {selectedAlias === undefined
            ? `${value} (unavailable)`
            : `${modelDisplayName(
                catalogue,
                selectedAlias.provider,
                selectedAlias.model.id,
              )} — ${value} (alias)`}
        </option>
      ) : null}
      {groups.map((group) => (
        <optgroup key={group.provider} label={group.provider}>
          {group.ids.map((id) => {
            const optionValue = selectionValue(catalogue, group.provider, id);
            const star = recommended === id || recommended === optionValue ? ' ★' : '';
            return (
              <option key={optionValue} value={optionValue}>
                {modelDisplayName(catalogue, group.provider, id)}
                {star}
              </option>
            );
          })}
        </optgroup>
      ))}
    </select>
  );
}

/**
 * One row of the grouped phase list: the phase name and its hint lead, the
 * compact pickers trail as one unit — beside the copy when they fit, or
 * wrapped together onto their own trailing line, never split apart.
 */
export function PhaseRow({
  field,
  hint,
  children,
}: {
  field: (typeof PHASE_FIELDS)[number];
  hint: string;
  children: ReactNode;
}) {
  return (
    <div className="config-editor__phase-row">
      <span className="config-editor__phase-row-copy">
        <b className="config-editor__phase-row-name">{field.label}</b>
        <span className="config-editor__phase-row-hint">{hint}</span>
      </span>
      <span className="config-editor__phase-row-picks">{children}</span>
    </div>
  );
}

const EFFORT_LABELS: Record<EffortLevel, string> = {
  auto: 'Auto',
  low: 'Low',
  medium: 'Medium',
  high: 'High',
  xhigh: 'XHigh',
  max: 'Max',
};

function automaticEffort(field: (typeof PHASE_FIELDS)[number], pipeline: string): EffortLevel {
  if (field.key === 'utilities') return 'low';
  return pipeline === 'medium' ? 'medium' : 'high';
}

function modelEffortCapabilities(
  catalogue: ModelCatalogue | null,
  modelValue: string,
): readonly EffortLevel[] {
  return catalogueModelForSelection(catalogue, modelValue)?.model.effortCapabilities ?? [];
}

interface ModelEffortRowProps {
  field: (typeof PHASE_FIELDS)[number];
  modelValue: string;
  defaultModel: string;
  effortValue?: EffortLevel;
  defaultEffort?: EffortLevel;
  catalogue: ModelCatalogue | null;
  pipeline: string;
  onModelChange(value: string, resetEffort?: EffortLevel): void;
  onEffortChange(value: EffortLevel | undefined): void;
}

export function ModelEffortRow({
  field,
  modelValue,
  defaultModel,
  effortValue,
  defaultEffort,
  catalogue,
  pipeline,
  onModelChange,
  onEffortChange,
}: ModelEffortRowProps) {
  const [resetNotice, setResetNotice] = useState(false);
  const effectiveModel = modelValue === '' ? defaultModel : modelValue;
  const capabilities = modelEffortCapabilities(catalogue, effectiveModel);
  const controlledEffort = effortValue ?? (defaultEffort === undefined ? 'auto' : '');
  const unavailableEffort =
    controlledEffort !== '' &&
    controlledEffort !== 'auto' &&
    !capabilities.includes(controlledEffort)
      ? controlledEffort
      : undefined;
  const pipelineEffort = automaticEffort(field, pipeline);
  const pipelineLabel =
    pipeline === 'medium' ? 'Medium' : pipeline === 'moonshot' ? 'Moonshot' : 'Large';

  const changeModel = (nextModel: string) => {
    const nextEffectiveModel = nextModel === '' ? defaultModel : nextModel;
    const nextCapabilities = modelEffortCapabilities(catalogue, nextEffectiveModel);
    const shouldReset =
      controlledEffort !== '' &&
      controlledEffort !== 'auto' &&
      !nextCapabilities.includes(controlledEffort);
    onModelChange(nextModel, shouldReset ? 'auto' : undefined);
    if (shouldReset) {
      setResetNotice(true);
    } else {
      setResetNotice(false);
    }
  };

  return (
    <PhaseRow
      field={field}
      hint={resetNotice ? 'Effort reset to Auto for the selected model.' : field.hint}
    >
      <ModelPicker
        field={field}
        value={modelValue}
        defaultModel={defaultModel}
        catalogue={catalogue}
        onChange={changeModel}
      />
      <select
        className="config-editor__pick config-editor__pick--effort"
        aria-label={`${field.label} effort`}
        value={controlledEffort}
        onChange={(event) => {
          const next = event.target.value;
          setResetNotice(false);
          onEffortChange(next === '' ? undefined : (next as EffortLevel));
        }}
      >
        {defaultEffort === undefined ? null : (
          <option key="default" value="">
            Default — {EFFORT_LABELS[defaultEffort]}
          </option>
        )}
        <option key="auto" value="auto">
          Auto — {field.key === 'utilities' ? 'Utilities' : pipelineLabel} default ({pipelineEffort}
          )
        </option>
        {unavailableEffort !== undefined ? (
          <option key={`unavailable:${unavailableEffort}`} value={unavailableEffort}>
            {EFFORT_LABELS[unavailableEffort]} (unavailable)
          </option>
        ) : null}
        {capabilities.map((level) => (
          <option key={level} value={level}>
            {EFFORT_LABELS[level]}
          </option>
        ))}
      </select>
    </PhaseRow>
  );
}

interface ConfigFormValue {
  models: PhaseModels;
  effort: PhaseEffort;
  inquireness: Inquireness;
  checkpoints: Checkpoints;
}

interface ConfigFormProps {
  value: ConfigFormValue;
  defaults: PhaseModels;
  catalogue: ModelCatalogue | null;
  pipeline: string;
  /** Utilities is workspace-scoped; feature configs hide it. */
  showUtilities: boolean;
  manualPublishAvailable: boolean;
  inputAlerts: { value: string; options: ReadonlyArray<{ value: string; label: string }> };
  automaticReview: {
    value: string;
    hint: string;
    options: ReadonlyArray<{ value: string; label: string }>;
  };
  onChange(next: ConfigFormValue): void;
  onInputAlertsChange(value: string): void;
  onAutomaticReviewChange(value: string): void;
}

function ConfigForm({
  value,
  defaults,
  catalogue,
  pipeline,
  showUtilities,
  manualPublishAvailable,
  inputAlerts,
  automaticReview,
  onChange,
  onInputAlertsChange,
  onAutomaticReviewChange,
}: ConfigFormProps) {
  const inquirenessName = useId();
  const phaseFields = applicablePhaseFields(pipeline, showUtilities);
  const gates = applicableGates(pipeline);
  const visibleGates = GATE_FIELDS.filter(
    (g) => gates.has(g.key) && (g.key !== 'manualPublish' || manualPublishAvailable),
  );

  const setCheckpoint = (key: keyof Checkpoints, on: boolean) => {
    const next = { ...value.checkpoints, [key]: on };
    // Roadmap review implies phase plan review.
    if (key === 'roadmapReview') next.phasePlanReview = on;
    onChange({ ...value, checkpoints: next });
  };

  return (
    <div className="config-editor__groups">
      <fieldset className="config-editor__group">
        <legend className="config-editor__group-title">Models</legend>
        <p className="config-editor__group-desc">
          Choose the model for each phase. Default uses the workspace model for that phase.
        </p>
        <div className="config-editor__phase-rows">
          {phaseFields.map((field) =>
            field.supportsEffort === false ? (
              <PhaseRow key={field.key} field={field} hint={field.hint}>
                <ModelPicker
                  field={field}
                  value={value.models[field.key] ?? ''}
                  defaultModel={defaults[field.key] ?? ''}
                  catalogue={catalogue}
                  onChange={(model) =>
                    onChange({
                      ...value,
                      models: { ...value.models, [field.key]: model === '' ? undefined : model },
                    })
                  }
                />
              </PhaseRow>
            ) : (
              <ModelEffortRow
                key={field.key}
                field={field}
                modelValue={value.models[field.key] ?? ''}
                defaultModel={defaults[field.key] ?? ''}
                effortValue={value.effort[field.key]}
                catalogue={catalogue}
                pipeline={pipeline}
                onModelChange={(model, resetEffort) =>
                  onChange({
                    ...value,
                    models: { ...value.models, [field.key]: model === '' ? undefined : model },
                    effort:
                      resetEffort === undefined
                        ? value.effort
                        : { ...value.effort, [field.key]: resetEffort },
                  })
                }
                onEffortChange={(effort) =>
                  onChange({
                    ...value,
                    effort: { ...value.effort, [field.key]: effort },
                  })
                }
              />
            ),
          )}
        </div>
      </fieldset>

      <fieldset className="config-editor__group">
        <legend className="config-editor__group-title">Behavior</legend>
        <div
          className="config-editor__row config-editor__row--stacked"
          role="radiogroup"
          aria-label="Inquireness"
        >
          <span className="config-editor__row-label">Inquireness</span>
          <span className="config-editor__row-hint">How many planning questions to surface</span>
          <div className="config-editor__segments">
            {INQUIRENESS_OPTIONS.map((option) => (
              <label
                key={option.value}
                className="config-editor__segment"
                data-selected={value.inquireness === option.value}
              >
                <input
                  type="radio"
                  name={inquirenessName}
                  checked={value.inquireness === option.value}
                  onChange={() => onChange({ ...value, inquireness: option.value })}
                />
                <b>{option.label}</b>
                <span>{option.hint}</span>
              </label>
            ))}
          </div>
        </div>
        <label className="config-editor__row">
          <span className="config-editor__row-label">Input alerts</span>
          <span className="config-editor__row-hint">Notifications when a feature needs input</span>
          <select
            className="config-editor__select"
            aria-label="Input alerts"
            value={inputAlerts.value}
            onChange={(event) => onInputAlertsChange(event.target.value)}
          >
            {inputAlerts.options.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
        </label>
        <label className="config-editor__row">
          <span className="config-editor__row-label">Auto mode</span>
          <span className="config-editor__row-hint">{automaticReview.hint}</span>
          <select
            className="config-editor__select"
            aria-label="Auto mode"
            value={automaticReview.value}
            onChange={(event) => onAutomaticReviewChange(event.target.value)}
          >
            {automaticReview.options.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
        </label>
      </fieldset>

      <fieldset className="config-editor__group">
        <legend className="config-editor__group-title">Gates</legend>
        <p className="config-editor__group-desc">
          Gates pause the pipeline for your review before continuing.
        </p>
        {visibleGates.map((gate) => (
          <label key={gate.key} className="config-editor__gate">
            <input
              type="checkbox"
              checked={value.checkpoints[gate.key]}
              onChange={(event) => setCheckpoint(gate.key, event.target.checked)}
            />
            <span className="config-editor__gate-text">
              <b>{gate.label}</b>
              <span>{gate.hint}</span>
            </span>
          </label>
        ))}
      </fieldset>
    </div>
  );
}

type LoadState<T> =
  { phase: 'loading' } | { phase: 'error'; message: string } | { phase: 'ready'; data: T };

interface SaveBarProps {
  dirty: boolean;
  saving: boolean;
  saved: boolean;
  error: string | null;
  effectNote: string;
  onSave(): void;
  onReset(): void;
}

function SaveBar({ dirty, saving, saved, error, effectNote, onSave, onReset }: SaveBarProps) {
  return (
    <footer className="config-editor__footer">
      <span className="config-editor__status" role="status">
        {error !== null
          ? `Save failed — ${error}`
          : saving
            ? 'Saving…'
            : dirty
              ? 'Unsaved changes'
              : saved
                ? `Saved. ${effectNote}`
                : effectNote}
      </span>
      <div className="config-editor__actions">
        <button
          type="button"
          className="config-editor__btn"
          onClick={onReset}
          disabled={!dirty || saving}
        >
          Reset
        </button>
        <button
          type="button"
          className="config-editor__btn config-editor__btn--primary"
          onClick={onSave}
          disabled={!dirty || saving}
        >
          Save changes
        </button>
      </div>
    </footer>
  );
}

export function useModelCatalogue(): ModelCatalogue | null {
  const [catalogue, setCatalogue] = useState<ModelCatalogue | null>(null);
  useEffect(() => {
    let alive = true;
    void window.agentico
      .getModelCatalogue()
      .then((cat) => {
        if (alive) setCatalogue(cat);
      })
      .catch(() => {
        // The pickers degrade to free defaults; a missing catalogue is not fatal.
      });
    return () => {
      alive = false;
    };
  }, []);
  return catalogue;
}

const FEATURE_ALERT_OPTIONS = [
  { value: 'default', label: 'Workspace default' },
  { value: 'enabled', label: 'Enabled' },
  { value: 'muted', label: 'Muted' },
] as const;

const FEATURE_AUTOMATIC_REVIEW_OPTIONS = [
  { value: 'default', label: 'Workspace default' },
  { value: 'enabled', label: 'Enabled' },
  { value: 'disabled', label: 'Disabled' },
] as const;

export function FeatureConfigPanel({ featureId }: { featureId: string }) {
  const catalogue = useModelCatalogue();
  const [state, setState] = useState<
    LoadState<{
      baseline: FeatureConfig;
      draft: FeatureConfig;
      manualPublishAvailable: boolean;
      defaults: FeatureConfig;
    }>
  >({ phase: 'loading' });
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [retryNonce, setRetryNonce] = useState(0);

  useEffect(() => {
    let alive = true;
    setState({ phase: 'loading' });
    setSaved(false);
    setSaveError(null);
    void window.agentico
      .getFeatureConfig(featureId)
      .then((snapshot) => {
        if (!alive) return;
        setState({
          phase: 'ready',
          data: {
            baseline: snapshot.current,
            draft: snapshot.current,
            defaults: snapshot.defaults,
            manualPublishAvailable: snapshot.manualPublishAvailable,
          },
        });
      })
      .catch((e: unknown) => {
        if (alive) setState({ phase: 'error', message: parseIpcError(e).message });
      });
    return () => {
      alive = false;
    };
  }, [featureId, retryNonce]);

  const save = useCallback(() => {
    if (state.phase !== 'ready') return;
    setSaving(true);
    setSaveError(null);
    void window.agentico
      .updateFeatureConfig({ featureId, config: state.data.draft })
      .then((snapshot) => {
        setState({
          phase: 'ready',
          data: {
            baseline: snapshot.current,
            draft: snapshot.current,
            defaults: snapshot.defaults,
            manualPublishAvailable: snapshot.manualPublishAvailable,
          },
        });
        setSaved(true);
      })
      .catch((e: unknown) => setSaveError(parseIpcError(e).message))
      .finally(() => setSaving(false));
  }, [featureId, state]);

  if (state.phase === 'loading') {
    return <p className="config-editor__notice">Loading configuration…</p>;
  }
  if (state.phase === 'error') {
    return (
      <div className="config-editor__notice config-editor__notice--error" role="alert">
        <p>Could not load configuration — {state.message}</p>
        <button
          type="button"
          className="config-editor__btn"
          onClick={() => setRetryNonce((n) => n + 1)}
        >
          Retry
        </button>
      </div>
    );
  }

  const { baseline, draft, defaults, manualPublishAvailable } = state.data;
  const dirty = JSON.stringify(baseline) !== JSON.stringify(draft);
  const setDraft = (next: FeatureConfig) => {
    setSaved(false);
    setState({ phase: 'ready', data: { ...state.data, draft: next } });
  };

  return (
    <div className="config-editor" aria-label="Feature configuration editor">
      <ConfigForm
        value={{
          models: draft.models,
          effort: draft.effort,
          inquireness: draft.inquireness,
          checkpoints: draft.checkpoints,
        }}
        defaults={defaults.models}
        catalogue={catalogue}
        pipeline={draft.pipeline}
        showUtilities={false}
        manualPublishAvailable={manualPublishAvailable}
        inputAlerts={{ value: draft.inputNotifications, options: FEATURE_ALERT_OPTIONS }}
        automaticReview={{
          value: draft.automaticReviewMode,
          hint: 'Override the workspace setting for this feature',
          options: FEATURE_AUTOMATIC_REVIEW_OPTIONS,
        }}
        onChange={(next) => setDraft({ ...draft, ...next })}
        onInputAlertsChange={(mode) =>
          setDraft({ ...draft, inputNotifications: mode as InputNotificationsMode })
        }
        onAutomaticReviewChange={(mode) =>
          setDraft({ ...draft, automaticReviewMode: mode as AutomaticReviewMode })
        }
      />
      <SaveBar
        dirty={dirty}
        saving={saving}
        saved={saved}
        error={saveError}
        effectNote="Changes apply to the next dispatch."
        onSave={save}
        onReset={() => setDraft(baseline)}
      />
    </div>
  );
}

const WORKSPACE_ALERT_OPTIONS = [
  { value: 'enabled', label: 'Enabled' },
  { value: 'muted', label: 'Muted' },
] as const;

const WORKSPACE_AUTOMATIC_REVIEW_OPTIONS = [
  { value: 'disabled', label: 'Disabled' },
  { value: 'enabled', label: 'Enabled' },
] as const;

export function WorkspaceDefaultsPanel({
  catalogue: catalogueOverride,
}: {
  catalogue?: ModelCatalogue | null;
} = {}) {
  const loadedCatalogue = useModelCatalogue();
  const catalogue = catalogueOverride ?? loadedCatalogue;
  const [state, setState] = useState<
    LoadState<{ baseline: WorkspaceDefaults; draft: WorkspaceDefaults }>
  >({ phase: 'loading' });
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [retryNonce, setRetryNonce] = useState(0);

  useEffect(() => {
    let alive = true;
    setState({ phase: 'loading' });
    setSaved(false);
    setSaveError(null);
    void window.agentico
      .getWorkspaceDefaults()
      .then((defaults) => {
        if (alive) setState({ phase: 'ready', data: { baseline: defaults, draft: defaults } });
      })
      .catch((e: unknown) => {
        if (alive) setState({ phase: 'error', message: parseIpcError(e).message });
      });
    return () => {
      alive = false;
    };
  }, [retryNonce]);

  const save = useCallback(() => {
    if (state.phase !== 'ready') return;
    setSaving(true);
    setSaveError(null);
    void window.agentico
      .updateWorkspaceDefaults(state.data.draft)
      .then((defaults) => {
        setState({ phase: 'ready', data: { baseline: defaults, draft: defaults } });
        setSaved(true);
      })
      .catch((e: unknown) => setSaveError(parseIpcError(e).message))
      .finally(() => setSaving(false));
  }, [state]);

  if (state.phase === 'loading') {
    return <p className="config-editor__notice">Loading workspace defaults…</p>;
  }
  if (state.phase === 'error') {
    return (
      <div className="config-editor__notice config-editor__notice--error" role="alert">
        <p>Could not load workspace defaults — {state.message}</p>
        <button
          type="button"
          className="config-editor__btn"
          onClick={() => setRetryNonce((n) => n + 1)}
        >
          Retry
        </button>
      </div>
    );
  }

  const { baseline, draft } = state.data;
  const dirty = JSON.stringify(baseline) !== JSON.stringify(draft);
  const setDraft = (next: WorkspaceDefaults) => {
    setSaved(false);
    setState({ phase: 'ready', data: { ...state.data, draft: next } });
  };

  return (
    <div className="config-editor" aria-label="Workspace defaults editor">
      <ConfigForm
        value={{
          models: draft.models,
          effort: draft.effort,
          inquireness: draft.inquireness,
          checkpoints: draft.checkpoints,
        }}
        defaults={catalogue?.phaseDefaults ?? {}}
        catalogue={catalogue}
        pipeline={draft.pipeline}
        showUtilities
        manualPublishAvailable
        inputAlerts={{
          value: draft.muteFeatureInput ? 'muted' : 'enabled',
          options: WORKSPACE_ALERT_OPTIONS,
        }}
        automaticReview={{
          value: draft.automaticReviewEnabled ? 'enabled' : 'disabled',
          hint: 'Approve shell commands automatically instead of asking you',
          options: WORKSPACE_AUTOMATIC_REVIEW_OPTIONS,
        }}
        onChange={(next) => setDraft({ ...draft, ...next })}
        onInputAlertsChange={(mode) => setDraft({ ...draft, muteFeatureInput: mode === 'muted' })}
        onAutomaticReviewChange={(mode) =>
          setDraft({ ...draft, automaticReviewEnabled: mode === 'enabled' })
        }
      />
      <SaveBar
        dirty={dirty}
        saving={saving}
        saved={saved}
        error={saveError}
        effectNote="Defaults apply to new dispatches and new features."
        onSave={save}
        onReset={() => setDraft(baseline)}
      />
    </div>
  );
}
