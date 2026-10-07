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
 * The supervisor's harness-and-model chip: a footer button reading the
 * committed choice, opening a toolbar popover (the same transient surface the
 * bell and the update button use, here hanging upward from the composer).
 * Every pick commits immediately through the settings route; the chip and
 * the checked radios always reflect what the server committed, with the
 * in-flight pick shown only while its commit is pending.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type {
  CanonicalError,
  EffortLevel,
  ModelCatalogue,
  SupervisorSettings,
} from '../../../../shared/ipc';
import { ChevronDownIcon } from '../../components/icons';
import { ErrorSurface } from '../../components/ErrorSurface';
import { ToolbarPopover, ToolbarPopoverAnchor } from '../../components/ToolbarPopover';
import { parseIpcError } from '../../wizard/ipcError';
import {
  effortLabel,
  findCatalogueModel,
  modelLabel,
  settingsChosen,
  SUPERVISOR_COPY,
  supervisorChipLabel,
  supervisorHarnessGroups,
} from './supervisorModel';

export interface SupervisorModelChipProps {
  settings: SupervisorSettings;
  catalogue: ModelCatalogue | null;
  onCommit(request: SupervisorSettings): Promise<void>;
  openSection?: { section: 'model' | 'effort'; filter: string; token: number } | null;
}

export function SupervisorModelChip({
  settings,
  catalogue,
  onCommit,
  openSection,
}: SupervisorModelChipProps) {
  const trigger = useRef<HTMLButtonElement>(null);
  const [open, setOpen] = useState(false);
  const [pending, setPending] = useState<SupervisorSettings | null>(null);
  const [error, setError] = useState<CanonicalError | null>(null);
  const [modelFilter, setModelFilter] = useState('');
  const effortRef = useRef<HTMLFieldSetElement>(null);
  useEffect(() => {
    if (openSection === null || openSection === undefined) return;
    setOpen(true);
    setModelFilter(openSection.section === 'model' ? openSection.filter : '');
    if (openSection.section === 'effort')
      requestAnimationFrame(() => effortRef.current?.scrollIntoView?.({ block: 'nearest' }));
  }, [openSection]);
  const dismiss = useCallback(() => setOpen(false), []);
  const groups = useMemo(
    () => (catalogue === null ? null : supervisorHarnessGroups(catalogue)),
    [catalogue],
  );
  // The radios show the pick being committed; everything else reads the
  // committed settings.
  const shown = pending ?? settings;
  const chosen = settingsChosen(shown);
  const capabilities: readonly EffortLevel[] = chosen
    ? (findCatalogueModel(catalogue, shown.harness, shown.model)?.effortCapabilities ?? [])
    : [];
  const label = supervisorChipLabel(settings, catalogue);

  const commit = async (next: SupervisorSettings): Promise<void> => {
    if (pending !== null) return;
    setPending(next);
    setError(null);
    try {
      await onCommit(next);
    } catch (err) {
      setError(parseIpcError(err));
    } finally {
      setPending(null);
    }
  };

  const chooseModel = (harness: string, model: string): void => {
    const nextCapabilities =
      findCatalogueModel(catalogue, harness, model)?.effortCapabilities ?? [];
    // Keep the chosen effort when the new model offers it; otherwise fall
    // back to the harness default rather than commit an effort it refuses.
    const effort =
      shown.effort !== '' && nextCapabilities.includes(shown.effort as EffortLevel)
        ? shown.effort
        : '';
    void commit({ harness, model, effort });
  };

  return (
    <ToolbarPopoverAnchor className="supervisor-chip">
      <button
        ref={trigger}
        type="button"
        className="supervisor-chip__trigger"
        data-testid="supervisor-model-chip"
        data-unset={!settingsChosen(settings)}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls="supervisor-model-popover"
        onClick={() => {
          setOpen((current) => !current);
        }}
      >
        <span className="supervisor-chip__label">{label}</span>
        <ChevronDownIcon className="supervisor-chip__chevron" />
      </button>
      <ToolbarPopover
        open={open}
        id="supervisor-model-popover"
        className="supervisor-chip__popover"
        label={SUPERVISOR_COPY.chipPopover}
        anchorRef={trigger}
        onDismiss={dismiss}
      >
        <h2 className="supervisor-chip__heading">{SUPERVISOR_COPY.chipPopover}</h2>
        <input
          aria-label="Filter models"
          value={modelFilter}
          onChange={(event) => setModelFilter(event.target.value)}
          placeholder="Find a model"
        />
        {error !== null ? <ErrorSurface error={error} variant="compact" /> : null}
        {groups === null ? (
          <p className="supervisor-chip__note" role="status">
            {SUPERVISOR_COPY.loadingModels}
          </p>
        ) : groups.length === 0 ? (
          <p className="supervisor-chip__note">{SUPERVISOR_COPY.noHarnesses}</p>
        ) : (
          <div className="supervisor-chip__groups">
            {groups.map((group) => (
              <fieldset
                key={group.harness}
                className="supervisor-chip__group"
                disabled={pending !== null}
              >
                <legend className="supervisor-chip__harness">{group.label}</legend>
                {group.models.length === 0 ? (
                  <p className="supervisor-chip__note">{SUPERVISOR_COPY.noModels}</p>
                ) : (
                  group.models
                    .filter(
                      (model) =>
                        modelFilter === '' ||
                        [model.id, model.displayName ?? '', ...(model.aliases ?? [])].some((name) =>
                          name.toLocaleLowerCase().includes(modelFilter.toLocaleLowerCase()),
                        ),
                    )
                    .map((model) => {
                      const current = shown.harness === group.harness && shown.model === model.id;
                      return (
                        <label
                          key={model.id}
                          className="supervisor-chip__option"
                          data-current={current ? true : undefined}
                        >
                          <input
                            className="sr-only"
                            type="radio"
                            name="supervisor-model"
                            value={`${group.harness}:${model.id}`}
                            checked={current}
                            onChange={() => chooseModel(group.harness, model.id)}
                          />
                          <span className="supervisor-chip__option-name">
                            {modelLabel(catalogue, group.harness, model.id)}
                            {catalogue?.phaseProviderModels.chat?.[group.harness]?.[0] ===
                            model.id ? (
                              <span aria-hidden="true" title="Recommended model">
                                {' '}
                                ★
                              </span>
                            ) : null}
                          </span>
                          <span className="supervisor-chip__check" aria-hidden="true">
                            {current ? '✓' : ''}
                          </span>
                        </label>
                      );
                    })
                )}
              </fieldset>
            ))}
          </div>
        )}
        {chosen ? (
          <fieldset ref={effortRef} className="supervisor-chip__effort" disabled={pending !== null}>
            <legend className="supervisor-chip__harness">Effort</legend>
            <div className="supervisor-chip__segments">
              {['', ...capabilities].map((level) => {
                const current = shown.effort === level;
                return (
                  <label
                    key={level === '' ? 'default' : level}
                    className="supervisor-chip__segment"
                    data-current={current ? true : undefined}
                  >
                    <input
                      className="sr-only"
                      type="radio"
                      name="supervisor-effort"
                      value={level}
                      checked={current}
                      onChange={() => void commit({ ...shown, effort: level })}
                    />
                    {level === '' && catalogue?.chatDefaultEffort?.[shown.harness]
                      ? `Default · ${effortLabel(catalogue.chatDefaultEffort[shown.harness] ?? '')}`
                      : effortLabel(level)}
                  </label>
                );
              })}
            </div>
          </fieldset>
        ) : null}
      </ToolbarPopover>
    </ToolbarPopoverAnchor>
  );
}
