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
import { useCallback, useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react';
import type {
  AutomaticReviewMode,
  CanonicalError,
  Checkpoints,
  EffortLevel,
  FeatureConfig,
  Inquireness,
  InputNotificationsMode,
  ModelCatalogue,
  PhaseEffort,
  PhaseModels,
  SlackRecipient,
  WorkspaceDefaults,
} from '../../../shared/ipc';
import { ErrorSurface } from '../components/ErrorSurface';
import { FieldError, fieldAriaDescribedBy, fieldAriaInvalid } from '../components/FieldError';
import { retryAction, useIpcLoad } from '../hooks';
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
  ultra: 'Ultra',
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
  notifications?: ReactNode;
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
  notifications,
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
      {notifications}
    </div>
  );
}

interface SaveBarProps {
  dirty: boolean;
  blocked?: boolean;
  saving: boolean;
  saved: boolean;
  error: CanonicalError | null;
  effectNote: string;
  onSave(): void;
  onReset(): void;
}

function SaveBar({
  dirty,
  blocked,
  saving,
  saved,
  error,
  effectNote,
  onSave,
  onReset,
}: SaveBarProps) {
  return (
    <footer className="config-editor__footer">
      {/* The failure branch is the canonical card; the bar's own Save button
       * is the retry, so the card carries no action of its own. Success,
       * progress, and the clean note stay a status line. */}
      {error !== null ? (
        <ErrorSurface error={error} variant="compact" />
      ) : (
        <span className="config-editor__status" role="status">
          {saving
            ? 'Saving…'
            : blocked
              ? 'Resolve or remove every recipient before saving.'
              : dirty
                ? 'Unsaved changes'
                : saved
                  ? `Saved. ${effectNote}`
                  : effectNote}
        </span>
      )}
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
          disabled={!dirty || saving || blocked}
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

type SlackOverride = '' | 'on' | 'off';
type FeatureSlack = {
  mode?: '' | 'muted';
  progress?: SlackOverride;
  needsInput?: SlackOverride;
  problems?: SlackOverride;
  recipients?: SlackRecipient[];
};
type ConfigWithSlack = FeatureConfig & {
  slackNotifications?: FeatureSlack;
  slackConfigured?: boolean;
  slackDefaults?: {
    categories: { progress: boolean; needsInput: boolean; problems: boolean };
    recipientNames: string[];
  };
};
type RecipientRow = {
  key: number;
  input: string;
  resolved: SlackRecipient | null;
  resolving: boolean;
  error: string | null;
};
const CATEGORY_FIELDS = [
  { key: 'progress', label: 'Progress' },
  { key: 'needsInput', label: 'Needs input' },
  { key: 'problems', label: 'Problems' },
] as const;

function NotificationGroup({
  configured,
  defaults,
  defaultRecipients,
  slack,
  rows,
  onSlackChange,
  onRowChange,
  onRowResolve,
  onRowRemove,
  onRowAdd,
}: {
  configured: boolean;
  defaults?: NonNullable<ConfigWithSlack['slackDefaults']>['categories'];
  defaultRecipients?: string[];
  slack?: FeatureSlack;
  rows: RecipientRow[];
  onSlackChange(next: FeatureSlack): void;
  onRowChange(key: number, input: string): void;
  onRowResolve(key: number): void;
  onRowRemove(key: number): void;
  onRowAdd(): void;
}) {
  return (
    <fieldset className="config-editor__group">
      <legend className="config-editor__group-title">Notifications</legend>
      {!configured ? (
        <p className="config-editor__group-desc">Set up Slack in Settings</p>
      ) : (
        <>
          <label className="config-editor__gate">
            <input
              type="checkbox"
              checked={slack?.mode === 'muted'}
              onChange={(event) =>
                onSlackChange({ ...slack, mode: event.currentTarget.checked ? 'muted' : '' })
              }
            />
            <span className="config-editor__gate-text">
              <b>Mute this feature</b>
              <span>Stop new Slack updates. Items already posted can still be answered.</span>
            </span>
          </label>
          {slack?.mode === 'muted' ? (
            <p className="config-editor__group-desc">
              Category choices have no effect while muted.
            </p>
          ) : null}
          {CATEGORY_FIELDS.map(({ key, label }) => (
            <label key={key} className="config-editor__row">
              <span className="config-editor__row-label">{label}</span>
              <span className="config-editor__row-hint">Slack updates for this feature</span>
              <select
                className="config-editor__select"
                aria-label={label}
                value={slack?.[key] ?? ''}
                onChange={(event) =>
                  onSlackChange({
                    ...slack,
                    [key]: event.currentTarget.value as SlackOverride,
                  })
                }
              >
                <option value="">Inherit ({defaults?.[key] === false ? 'off' : 'on'})</option>
                <option value="on">On</option>
                <option value="off">Off</option>
              </select>
            </label>
          ))}
          <div className="config-editor__recipients">
            <div className="config-editor__recipients-head">
              <div>
                <b>Also notify</b>
                <p className="config-editor__group-desc">
                  Workspace defaults:{' '}
                  {defaultRecipients?.length ? defaultRecipients.join(', ') : 'None'}
                </p>
              </div>
              <button type="button" className="config-editor__btn" onClick={onRowAdd}>
                Add recipient
              </button>
            </div>
            {rows.map((row, index) => {
              const errorId = `config-slack-recipient-${row.key}-error`;
              return (
                <div className="config-editor__recipient" key={row.key}>
                  <div className="config-editor__recipient-field">
                    <label className="sr-only" htmlFor={`config-slack-recipient-${row.key}`}>
                      Recipient {index + 1}
                    </label>
                    <input
                      id={`config-slack-recipient-${row.key}`}
                      value={row.input}
                      placeholder="Email, @handle, #channel, or Slack ID"
                      aria-invalid={fieldAriaInvalid(row.error !== null)}
                      aria-describedby={fieldAriaDescribedBy(errorId, row.error !== null)}
                      onChange={(event) => onRowChange(row.key, event.currentTarget.value)}
                      onBlur={() => onRowResolve(row.key)}
                      onKeyDown={(event) => {
                        if (event.key === 'Enter') {
                          event.preventDefault();
                          onRowResolve(row.key);
                        }
                      }}
                    />
                    {row.resolving ? <span role="status">Resolving...</span> : null}
                    {row.resolved !== null ? (
                      <span role="status">{row.resolved.displayName}</span>
                    ) : null}
                    <FieldError id={errorId} message={row.error} />
                  </div>
                  <button
                    type="button"
                    className="config-editor__btn"
                    aria-label={`Remove recipient ${index + 1}`}
                    onClick={() => onRowRemove(row.key)}
                  >
                    Remove
                  </button>
                </div>
              );
            })}
          </div>
        </>
      )}
    </fieldset>
  );
}

export function FeatureConfigPanel({ featureId }: { featureId: string }) {
  const catalogue = useModelCatalogue();
  const draftRevision = useRef(0);
  const nextRowKey = useRef(0);
  const rowRevisions = useRef(new Map<number, number>());
  const [rows, setRows] = useState<RecipientRow[]>([]);
  const makeRow = useCallback(
    (recipient?: SlackRecipient): RecipientRow => ({
      key: ++nextRowKey.current,
      input: recipient?.typedText ?? '',
      resolved: recipient ?? null,
      resolving: false,
      error: null,
    }),
    [],
  );
  const resetRows = useCallback(
    (slack?: FeatureSlack) => {
      rowRevisions.current.clear();
      setRows([...(slack?.recipients ?? []).map((recipient) => makeRow(recipient)), makeRow()]);
    },
    [makeRow],
  );
  const loadConfig = useCallback(async () => {
    const snapshot = await window.agentico.getFeatureConfig(featureId);
    return {
      baseline: snapshot.current,
      draft: snapshot.current,
      defaults: snapshot.defaults,
      manualPublishAvailable: snapshot.manualPublishAvailable,
    };
  }, [featureId]);
  const { state, reload, replace } = useIpcLoad(loadConfig, [featureId]);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [savedNotifications, setSavedNotifications] = useState(false);
  const [saveError, setSaveError] = useState<CanonicalError | null>(null);
  const baselineSlack =
    state.phase === 'loaded'
      ? (state.data.baseline as ConfigWithSlack).slackNotifications
      : undefined;
  const draftSlack =
    state.phase === 'loaded' ? (state.data.draft as ConfigWithSlack).slackNotifications : undefined;
  const slackSettings =
    state.phase === 'loaded' ? (state.data.draft as ConfigWithSlack) : undefined;
  useEffect(() => {
    if (state.phase === 'loaded') resetRows(baselineSlack);
  }, [baselineSlack, resetRows, state.phase]);
  const resolvedRecipients = rows.flatMap((row) =>
    row.input.trim() !== '' && row.resolved !== null ? [row.resolved] : [],
  );
  const invalidRecipients = rows.some((row) => row.input.trim() !== '' && row.resolved === null);
  const sectionDirty =
    slackSettings?.slackConfigured === true &&
    (JSON.stringify(baselineSlack?.mode ?? '') !== JSON.stringify(draftSlack?.mode ?? '') ||
      CATEGORY_FIELDS.some(
        ({ key }) => (baselineSlack?.[key] ?? '') !== (draftSlack?.[key] ?? ''),
      ) ||
      JSON.stringify(baselineSlack?.recipients ?? []) !== JSON.stringify(resolvedRecipients) ||
      invalidRecipients);
  const dirty =
    state.phase === 'loaded' &&
    (JSON.stringify(state.data.baseline) !== JSON.stringify(state.data.draft) || sectionDirty);

  useEffect(() => {
    let active = true;
    const unsubscribe = window.agentico.onAppEvent((event) => {
      if (
        !active ||
        dirty ||
        saving ||
        event.type !== 'invalidated' ||
        (event.kind !== 'resync' &&
          !event.kind.startsWith('config') &&
          !event.kind.startsWith('feature.config')) ||
        (event.kind !== 'resync' &&
          event.resourceType !== 'runtime' &&
          event.featureId !== featureId &&
          event.resourceId !== featureId)
      )
        return;
      const revision = draftRevision.current;
      void loadConfig()
        .then((next) => {
          if (active && draftRevision.current === revision) replace(next);
        })
        .catch(() => {
          // A background refresh is best-effort; the current draft remains editable.
        });
    });
    return () => {
      active = false;
      unsubscribe();
    };
  }, [dirty, featureId, loadConfig, replace, saving]);

  const save = useCallback(() => {
    if (state.phase !== 'loaded' || invalidRecipients) return;
    draftRevision.current += 1;
    setSaving(true);
    setSaveError(null);
    const {
      slackNotifications: _storedSlack,
      slackConfigured: _configured,
      slackDefaults: _defaults,
      ...other
    } = state.data.draft as ConfigWithSlack;
    const slackPatch: FeatureSlack = {};
    for (const { key } of CATEGORY_FIELDS) {
      if ((baselineSlack?.[key] ?? '') !== (draftSlack?.[key] ?? '')) {
        slackPatch[key] = draftSlack?.[key] ?? '';
      }
    }
    if ((baselineSlack?.mode ?? '') !== (draftSlack?.mode ?? '')) {
      slackPatch.mode = draftSlack?.mode ?? '';
    }
    if (JSON.stringify(baselineSlack?.recipients ?? []) !== JSON.stringify(resolvedRecipients)) {
      slackPatch.recipients = resolvedRecipients;
    }
    const config: ConfigWithSlack = {
      ...other,
      ...(sectionDirty ? { slackNotifications: slackPatch } : {}),
    };
    void window.agentico
      .updateFeatureConfig({ featureId, config })
      .then((snapshot) => {
        replace({
          baseline: snapshot.current,
          draft: snapshot.current,
          defaults: snapshot.defaults,
          manualPublishAvailable: snapshot.manualPublishAvailable,
        });
        resetRows((snapshot.current as ConfigWithSlack).slackNotifications);
        setSavedNotifications(sectionDirty);
        setSaved(true);
      })
      .catch((e: unknown) => setSaveError(parseIpcError(e)))
      .finally(() => setSaving(false));
  }, [
    baselineSlack,
    draftSlack,
    featureId,
    invalidRecipients,
    replace,
    resetRows,
    resolvedRecipients,
    sectionDirty,
    state,
  ]);

  if (state.phase === 'loading') {
    return <p className="config-editor__notice">Loading configuration…</p>;
  }
  if (state.phase === 'error') {
    return <ErrorSurface error={state.error} variant="compact" localAction={retryAction(reload)} />;
  }

  const { baseline, draft, defaults, manualPublishAvailable } = state.data;
  const setDraft = (next: ConfigWithSlack) => {
    draftRevision.current += 1;
    setSaved(false);
    setSavedNotifications(false);
    replace({ ...state.data, draft: next });
  };
  const setSlack = (slackNotifications: FeatureSlack) => setDraft({ ...draft, slackNotifications });
  const updateRow = (key: number, input: string) => {
    draftRevision.current += 1;
    rowRevisions.current.set(key, (rowRevisions.current.get(key) ?? 0) + 1);
    setSaved(false);
    setSavedNotifications(false);
    setRows((current) =>
      current.map((row) =>
        row.key === key ? { ...row, input, resolved: null, resolving: false, error: null } : row,
      ),
    );
  };
  const resolveRow = (key: number) => {
    const row = rows.find((candidate) => candidate.key === key);
    const input = row?.input.trim() ?? '';
    if (!row || !input || row.resolving || row.resolved?.typedText === input) return;
    const revision = (rowRevisions.current.get(key) ?? 0) + 1;
    rowRevisions.current.set(key, revision);
    setRows((current) =>
      current.map((candidate) =>
        candidate.key === key ? { ...candidate, resolving: true, error: null } : candidate,
      ),
    );
    void window.agentico
      .resolveSlackRecipient({ input })
      .then((recipient) => {
        if (rowRevisions.current.get(key) !== revision) return;
        setRows((current) => {
          const duplicate = current.some(
            (candidate) =>
              candidate.key !== key &&
              candidate.resolved?.kind === recipient.kind &&
              candidate.resolved.id === recipient.id,
          );
          return current.map((candidate) =>
            candidate.key === key
              ? {
                  ...candidate,
                  resolving: false,
                  resolved: duplicate ? null : recipient,
                  error: duplicate ? 'Already in the list' : null,
                }
              : candidate,
          );
        });
      })
      .catch((error: unknown) => {
        if (rowRevisions.current.get(key) !== revision) return;
        const canonical = parseIpcError(error);
        setRows((current) =>
          current.map((candidate) =>
            candidate.key === key
              ? {
                  ...candidate,
                  resolving: false,
                  error:
                    canonical.remediation?.hint === undefined
                      ? canonical.summary
                      : `${canonical.summary} ${canonical.remediation.hint}`,
                }
              : candidate,
          ),
        );
      });
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
        notifications={
          <NotificationGroup
            configured={slackSettings?.slackConfigured === true}
            defaults={slackSettings?.slackDefaults?.categories}
            defaultRecipients={slackSettings?.slackDefaults?.recipientNames}
            slack={draftSlack}
            rows={rows}
            onSlackChange={setSlack}
            onRowChange={updateRow}
            onRowResolve={resolveRow}
            onRowRemove={(key) => {
              draftRevision.current += 1;
              rowRevisions.current.delete(key);
              setSaved(false);
              setSavedNotifications(false);
              setRows((current) => {
                const remaining = current.filter((row) => row.key !== key);
                return remaining.length ? remaining : [makeRow()];
              });
            }}
            onRowAdd={() => setRows((current) => [...current, makeRow()])}
          />
        }
      />
      <SaveBar
        dirty={dirty}
        blocked={invalidRecipients}
        saving={saving}
        saved={saved}
        error={saveError}
        effectNote={
          sectionDirty || savedNotifications
            ? 'Notification changes apply immediately.'
            : 'Changes apply to the next dispatch.'
        }
        onSave={save}
        onReset={() => {
          setDraft(baseline);
          resetRows((baseline as ConfigWithSlack).slackNotifications);
        }}
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
  const loadDefaults = useCallback(async () => {
    const defaults = await window.agentico.getWorkspaceDefaults();
    return { baseline: defaults, draft: defaults };
  }, []);
  const { state, reload, replace } = useIpcLoad(loadDefaults, []);
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [saveError, setSaveError] = useState<CanonicalError | null>(null);

  const save = useCallback(() => {
    if (state.phase !== 'loaded') return;
    setSaving(true);
    setSaveError(null);
    void window.agentico
      .updateWorkspaceDefaults(state.data.draft)
      .then((defaults) => {
        replace({ baseline: defaults, draft: defaults });
        setSaved(true);
      })
      .catch((e: unknown) => setSaveError(parseIpcError(e)))
      .finally(() => setSaving(false));
  }, [replace, state]);

  if (state.phase === 'loading') {
    return <p className="config-editor__notice">Loading workspace defaults…</p>;
  }
  if (state.phase === 'error') {
    return <ErrorSurface error={state.error} variant="compact" localAction={retryAction(reload)} />;
  }

  const { baseline, draft } = state.data;
  const dirty = JSON.stringify(baseline) !== JSON.stringify(draft);
  const setDraft = (next: WorkspaceDefaults) => {
    setSaved(false);
    replace({ ...state.data, draft: next });
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
