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

import { useCallback, useState } from 'react';
import type {
  CanonicalError,
  EffortLevel,
  ModelCatalogue,
  SupervisorSettingsRequest,
} from '../../../../shared/ipc';
import { ErrorSurface } from '../../components/ErrorSurface';
import { SettingsConfirmationDialog } from '../SettingsConfirmationDialog';
import { parseIpcError } from '../../wizard/ipcError';
import {
  effortLabel,
  findCatalogueModel,
  harnessLabel,
  modelLabel,
  processExists,
} from './supervisorModel';
import type { SupervisorLifecycle } from '../../../../shared/ipc';

export interface SupervisorSwitchChoice {
  harness: string;
  model?: string;
}

export function SupervisorSwitchDialog({
  choice,
  catalogue,
  lifecycle,
  onCancel,
  onSwitch,
}: {
  choice: SupervisorSwitchChoice;
  catalogue: ModelCatalogue | null;
  lifecycle: SupervisorLifecycle;
  onCancel(): void;
  onSwitch(request: Pick<SupervisorSettingsRequest, 'harness' | 'model' | 'effort'>): Promise<void>;
}) {
  const models = catalogue?.phaseProviderModels.chat?.[choice.harness] ?? [];
  const [model, setModel] = useState(choice.model ?? models[0] ?? '');
  const [effort, setEffort] = useState('');
  const [modelTouched, setModelTouched] = useState(choice.model !== undefined);
  const [effortTouched, setEffortTouched] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<CanonicalError | null>(null);
  const close = useCallback(() => {
    if (!busy) onCancel();
  }, [busy, onCancel]);
  const capabilities: readonly EffortLevel[] =
    findCatalogueModel(catalogue, choice.harness, model)?.effortCapabilities ?? [];
  const submit = async () => {
    if (busy || models.length === 0) return;
    setBusy(true);
    setError(null);
    try {
      await onSwitch({
        harness: choice.harness,
        ...(modelTouched ? { model } : {}),
        ...(effortTouched ? { effort } : {}),
      });
    } catch (caught) {
      setError(parseIpcError(caught));
    } finally {
      setBusy(false);
    }
  };
  const label = harnessLabel(choice.harness);
  return (
    <SettingsConfirmationDialog ariaLabel={`Switch to ${label}`} onCancel={close}>
      <div className="supervisor-switch-dialog">
        <h2>Switch to {label}</h2>
        <p>This conversation will be rebuilt for {label} at the next message.</p>
        {processExists(lifecycle) ? (
          <p>Sub-agents and background shells from the current harness will not carry over.</p>
        ) : null}
        <label>
          Model
          <select
            value={model}
            disabled={busy || models.length === 0}
            onChange={(event) => {
              setModel(event.target.value);
              setModelTouched(true);
              setEffort('');
              setEffortTouched(false);
            }}
          >
            {models.map((id, index) => (
              <option key={id} value={id}>
                {modelLabel(catalogue, choice.harness, id)}
                {index === 0 ? ' ★' : ''}
              </option>
            ))}
          </select>
        </label>
        {models.length === 0 ? <p>No chat models available for {label}.</p> : null}
        <label>
          Effort
          <select
            value={effort}
            disabled={busy || models.length === 0}
            onChange={(event) => {
              setEffort(event.target.value);
              setEffortTouched(true);
            }}
          >
            <option value="">
              Default
              {catalogue?.chatDefaultEffort?.[choice.harness]
                ? ` · ${effortLabel(catalogue.chatDefaultEffort[choice.harness] ?? '')}`
                : ''}
            </option>
            {capabilities.map((level) => (
              <option key={level} value={level}>
                {effortLabel(level)}
              </option>
            ))}
          </select>
        </label>
        {error !== null ? <ErrorSurface error={error} variant="compact" /> : null}
        <div className="supervisor-switch-dialog__actions">
          <button type="button" onClick={close} disabled={busy}>
            Cancel
          </button>
          <button
            type="button"
            onClick={() => void submit()}
            disabled={busy || models.length === 0}
          >
            Switch
          </button>
        </div>
      </div>
    </SettingsConfirmationDialog>
  );
}
