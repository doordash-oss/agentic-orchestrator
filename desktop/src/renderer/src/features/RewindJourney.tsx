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
 * Rewind journey: a focused, multi-step dialog for seal-and-fork rewind.
 * Phase 1: choose a target phase (hierarchical — phase first, then
 * conditional roadmap phase for Implement on a multi-phase roadmap).
 * Phase 2: optional Advanced pipeline upgrade (collapsed by default).
 * Phase 3: consequence summary from a fresh server preview + type-REWIND
 * confirmation. Submission is single-flight and locked; disconnect/timeout
 * reconciles against authoritative run state.
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import { type RewindPreviewView, type FeatureActionResult } from '../../../shared/ipc';
import { parseIpcError, type WizardError } from '../wizard/ipcError';
import { displayPhaseLabel } from './featureView';

export interface RewindJourneyProps {
  featureId: string;
  featureName: string;
  /** Server-provided valid phase choices (from the action catalogue). */
  validPhaseOptions: string[];
  /** Current roadmap phase (default selection for Implement). */
  currentRoadmapPhase?: number;
  totalRoadmapPhases?: number;
  /** Run active when the dialog opened, retained across cockpit surface changes. */
  reconcileSourceRunNumber?: number;
  onClose(): void;
  onRewindComplete(result: FeatureActionResult): void;
}

type JourneyStep = 'target' | 'confirm' | 'submitting' | 'determining' | 'success' | 'error';

/** Facts repeated between preview and confirmation stay consistent by sharing one renderer. */
function ConsequenceFacts({ preview }: { preview: RewindPreviewView }) {
  return (
    <>
      {preview.carriedPhases?.length ? (
        <div className="rewind-journey__preview-fact">
          <dt>Carried forward</dt>
          <dd>{preview.carriedPhases.map(displayPhaseLabel).join(', ')}</dd>
        </div>
      ) : null}
      {preview.prConsequences?.length ? (
        <div className="rewind-journey__preview-fact">
          <dt>PR consequences</dt>
          <dd>{preview.prConsequences.map((item) => item.repo).join(', ')}</dd>
        </div>
      ) : null}
      {preview.worktreeConsequences?.length ? (
        <div className="rewind-journey__preview-fact">
          <dt>Worktree consequences</dt>
          <dd>
            {preview.worktreeConsequences
              .map((item) => `${item.repo} (${item.resetKind})`)
              .join(', ')}
          </dd>
        </div>
      ) : null}
    </>
  );
}

export function RewindJourney(props: RewindJourneyProps) {
  const {
    featureId,
    featureName,
    validPhaseOptions,
    currentRoadmapPhase,
    totalRoadmapPhases,
    reconcileSourceRunNumber,
    onClose,
    onRewindComplete,
  } = props;

  const [step, setStep] = useState<JourneyStep>('target');
  const [targetPhase, setTargetPhase] = useState<string>('');
  const [roadmapPhase, setRoadmapPhase] = useState<number | null>(null);
  const [upgradePipeline, setUpgradePipeline] = useState<string>('');
  const [preview, setPreview] = useState<RewindPreviewView | null>(null);
  const [previewLoading, setPreviewLoading] = useState(false);
  const [confirmText, setConfirmText] = useState('');
  const [error, setError] = useState<WizardError | null>(null);
  const [result, setResult] = useState<FeatureActionResult | null>(null);
  const submitRef = useRef(false);

  const isImplement = targetPhase === 'implement';
  const showRoadmapPicker =
    isImplement && totalRoadmapPhases !== undefined && totalRoadmapPhases > 1;
  const canConfirm = confirmText === 'REWIND' && preview?.eligible === true;

  // A successful rewind can change the cockpit surface before the mutation
  // response is rendered, remounting this dialog. Reconcile that remount from
  // the source run captured by the parent instead of presenting a fresh form.
  useEffect(() => {
    if (reconcileSourceRunNumber === undefined) return;
    let disposed = false;
    void window.agentico
      .getFeature(featureId)
      .then((feature) => {
        if (disposed || feature.activeRun <= reconcileSourceRunNumber) return;
        setResult({
          featureId,
          action: 'rewind',
          result: 'rewound',
          sessionIds: [],
          sourceRunNumber: reconcileSourceRunNumber,
          newRunNumber: feature.activeRun,
        });
        setStep('success');
      })
      .catch(() => {});
    return () => {
      disposed = true;
    };
  }, [featureId, reconcileSourceRunNumber]);

  // Fetch fresh preview whenever inputs change
  const fetchPreview = useCallback(async () => {
    if (!targetPhase) return;
    setPreviewLoading(true);
    try {
      const p = await window.agentico.getRewindPreview({
        featureId,
        targetPhase,
        ...(roadmapPhase !== null ? { roadmapPhase } : {}),
        ...(upgradePipeline !== '' ? { upgradePipeline } : {}),
      });
      setPreview(p);
      setError(null);
    } catch (err) {
      setPreview(null);
      setError(parseIpcError(err));
    } finally {
      setPreviewLoading(false);
    }
  }, [featureId, targetPhase, roadmapPhase, upgradePipeline]);

  useEffect(() => {
    if (targetPhase && step === 'target') {
      void fetchPreview();
    }
  }, [targetPhase, roadmapPhase, upgradePipeline, step, fetchPreview]);

  const handleConfirm = useCallback(async () => {
    if (submitRef.current || !canConfirm || !preview) return;
    submitRef.current = true;
    setStep('submitting');
    setError(null);
    try {
      const res = await window.agentico.executeRewind({
        featureId,
        targetPhase,
        ...(roadmapPhase !== null ? { roadmapPhase } : {}),
        ...(upgradePipeline !== '' ? { upgradePipeline } : {}),
        ...(preview.sourceRunNumber !== undefined
          ? { sourceRunNumber: preview.sourceRunNumber }
          : {}),
        ...(preview.sourceRevision !== '' ? { sourceRevision: preview.sourceRevision } : {}),
      });
      setResult(res);
      setStep('success');
    } catch (err) {
      setError(parseIpcError(err));
      // Check if the feature's active run changed (fork may have succeeded)
      setStep('determining');
      try {
        const feature = await window.agentico.getFeature(featureId);
        // If the active run advanced, the rewind likely succeeded
        if (preview.sourceRunNumber !== undefined && feature.activeRun > preview.sourceRunNumber) {
          setResult({
            featureId,
            action: 'rewind',
            result: 'rewound',
            sessionIds: [],
            sourceRunNumber: preview.sourceRunNumber,
            newRunNumber: feature.activeRun,
          });
          setStep('success');
        } else {
          setStep('error');
        }
      } catch {
        setStep('error');
      }
    } finally {
      submitRef.current = false;
    }
  }, [canConfirm, preview, featureId, targetPhase, roadmapPhase, upgradePipeline]);

  // Escape cancels (no mutation) at target/confirm steps
  useEffect(() => {
    if (step === 'submitting' || step === 'determining') return;
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        onClose();
      }
    };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, [step, onClose]);

  return (
    <div
      className="rewind-journey__backdrop"
      role="dialog"
      aria-modal="true"
      aria-label={`Rewind — ${featureName}`}
    >
      <div className="rewind-journey">
        <header className="rewind-journey__header">
          <h2 className="rewind-journey__title">Rewind — {featureName}</h2>
          <p className="rewind-journey__subtitle">
            Seals the current run and forks a fresh one. This is irreversible.
          </p>
        </header>

        {step === 'target' && (
          <div className="rewind-journey__body">
            <fieldset className="rewind-journey__field">
              <legend className="rewind-journey__legend">Target phase</legend>
              <div className="rewind-journey__options" role="radiogroup">
                {validPhaseOptions.map((phase) => (
                  <label key={phase} className="rewind-journey__option">
                    <input
                      type="radio"
                      name="rewind-target-phase"
                      value={phase}
                      checked={targetPhase === phase}
                      onChange={() => {
                        setTargetPhase(phase);
                        setRoadmapPhase(
                          phase === 'implement' && (totalRoadmapPhases ?? 0) > 1
                            ? (currentRoadmapPhase ?? 1)
                            : null,
                        );
                        setUpgradePipeline('');
                      }}
                    />
                    <span>{displayPhaseLabel(phase)}</span>
                  </label>
                ))}
              </div>
              {validPhaseOptions.length === 0 && (
                <p className="rewind-journey__loading" role="status">
                  Rewind targets are no longer available. Refresh the feature and try again.
                </p>
              )}
            </fieldset>

            {showRoadmapPicker && (
              <fieldset className="rewind-journey__field">
                <legend className="rewind-journey__legend">Roadmap phase</legend>
                <select
                  className="rewind-journey__select"
                  value={roadmapPhase ?? currentRoadmapPhase ?? 1}
                  onChange={(e) => setRoadmapPhase(Number(e.target.value))}
                >
                  {(
                    preview?.validRoadmapPhases ??
                    Array.from({ length: totalRoadmapPhases ?? 0 }, (_, i) => i + 1)
                  ).map((p: number) => (
                    <option key={p} value={p}>
                      Phase {p}
                      {p === currentRoadmapPhase ? ' (current)' : ''}
                    </option>
                  ))}
                </select>
              </fieldset>
            )}

            <details className="rewind-journey__advanced">
              <summary className="rewind-journey__advanced-summary">Advanced</summary>
              <div className="rewind-journey__advanced-body">
                {(preview?.upgradePipelineOptions?.length ?? 0) > 0 ? (
                  <label className="rewind-journey__field-label">
                    <span>Upgrade pipeline</span>
                    <select
                      className="rewind-journey__select"
                      value={upgradePipeline}
                      onChange={(e) => setUpgradePipeline(e.target.value)}
                    >
                      <option value="">No upgrade</option>
                      {preview?.upgradePipelineOptions?.map((opt: string) => (
                        <option key={opt} value={opt}>
                          {opt}
                        </option>
                      ))}
                    </select>
                  </label>
                ) : (
                  <p className="rewind-journey__loading">No pipeline upgrades are available.</p>
                )}
              </div>
            </details>

            {previewLoading && <p className="rewind-journey__loading">Computing consequences…</p>}

            {preview && !previewLoading && (
              <div className="rewind-journey__preview" data-eligible={preview.eligible}>
                <h3 className="rewind-journey__preview-title">Consequences</h3>
                <dl className="rewind-journey__preview-facts">
                  <div className="rewind-journey__preview-fact">
                    <dt>Effective target</dt>
                    <dd>{displayPhaseLabel(preview.effectivePhase)}</dd>
                  </div>
                  <ConsequenceFacts preview={preview} />
                  {preview.backupBranchRepos && preview.backupBranchRepos.length > 0 && (
                    <div className="rewind-journey__preview-fact">
                      <dt>Backup branches</dt>
                      <dd>{preview.backupBranchRepos.join(', ')}</dd>
                    </div>
                  )}
                </dl>
                {preview.validationFindings && preview.validationFindings.length > 0 && (
                  <ul className="rewind-journey__findings" role="alert">
                    {preview.validationFindings.map((f: string, i: number) => (
                      <li key={i}>{f}</li>
                    ))}
                  </ul>
                )}
              </div>
            )}

            {error && (
              <div role="alert" className="rewind-journey__error">
                {error.message}
              </div>
            )}

            <div className="rewind-journey__actions">
              <button className="rewind-journey__cancel" onClick={onClose}>
                Cancel
              </button>
              <button
                className="rewind-journey__next"
                onClick={() => setStep('confirm')}
                disabled={!preview?.eligible}
              >
                Continue
              </button>
            </div>
          </div>
        )}

        {step === 'confirm' && preview && (
          <div className="rewind-journey__body">
            <div className="rewind-journey__confirm-summary">
              <h3 className="rewind-journey__section-title">Confirm rewind</h3>
              <p className="rewind-journey__warning">
                This will seal Run {preview.sourceRunNumber} and create a new run. The sealed run is
                preserved as read-only history, but the current worktree will be reset. This action
                cannot be undone.
              </p>
              <dl className="rewind-journey__preview-facts">
                <div className="rewind-journey__preview-fact">
                  <dt>Source run</dt>
                  <dd>Run {preview.sourceRunNumber}</dd>
                </div>
                <div className="rewind-journey__preview-fact">
                  <dt>Target phase</dt>
                  <dd>{displayPhaseLabel(preview.targetPhase)}</dd>
                </div>
                <div className="rewind-journey__preview-fact">
                  <dt>Effective phase</dt>
                  <dd>{displayPhaseLabel(preview.effectivePhase)}</dd>
                </div>
                {roadmapPhase !== null && (
                  <div className="rewind-journey__preview-fact">
                    <dt>Roadmap phase</dt>
                    <dd>Phase {roadmapPhase}</dd>
                  </div>
                )}
                <div className="rewind-journey__preview-fact">
                  <dt>Advanced pipeline</dt>
                  <dd>{upgradePipeline !== '' ? upgradePipeline : 'No upgrade selected'}</dd>
                </div>
                <ConsequenceFacts preview={preview} />
              </dl>
            </div>

            <div className="rewind-journey__type-confirm">
              <label className="rewind-journey__type-label" htmlFor="rewind-confirm-input">
                Type <code>REWIND</code> to confirm
              </label>
              <input
                id="rewind-confirm-input"
                className="rewind-journey__type-input"
                type="text"
                value={confirmText}
                onChange={(e) => setConfirmText(e.target.value)}
                autoComplete="off"
                spellCheck={false}
                autoFocus
              />
            </div>

            <div className="rewind-journey__actions">
              <button
                className="rewind-journey__cancel"
                onClick={() => {
                  setStep('target');
                  setConfirmText('');
                }}
              >
                Back
              </button>
              <button
                className="rewind-journey__submit"
                onClick={handleConfirm}
                disabled={!canConfirm}
              >
                Rewind
              </button>
            </div>
          </div>
        )}

        {step === 'submitting' && (
          <div className="rewind-journey__body">
            <div className="rewind-journey__progress" aria-live="polite">
              <p className="rewind-journey__progress-text">Sealing and forking…</p>
              <div className="rewind-journey__spinner" role="presentation" />
            </div>
          </div>
        )}

        {step === 'determining' && (
          <div className="rewind-journey__body">
            <div className="rewind-journey__progress" aria-live="polite">
              <p className="rewind-journey__progress-text">
                Determining the outcome of the rewind. The run will reconcile against the
                authoritative server state.
              </p>
              <div className="rewind-journey__spinner" role="presentation" />
            </div>
          </div>
        )}

        {step === 'success' && result && (
          <div className="rewind-journey__body">
            <div className="rewind-journey__success" role="status">
              <h3 className="rewind-journey__section-title">Rewind complete</h3>
              <p>
                {result.sourceRunNumber !== undefined && `Run ${result.sourceRunNumber} sealed. `}
                {result.newRunNumber !== undefined &&
                  `New run ${result.newRunNumber} is now active.`}
              </p>
              {result.warnings && result.warnings.length > 0 && (
                <div className="rewind-journey__warnings">
                  <h4 className="rewind-journey__warnings-title">Warnings</h4>
                  <ul className="rewind-journey__warnings-list">
                    {result.warnings.map((w: string, i: number) => (
                      <li key={i} className="rewind-journey__warning-item">
                        {w}
                      </li>
                    ))}
                  </ul>
                </div>
              )}
              <button
                className="rewind-journey__done"
                onClick={() => onRewindComplete(result)}
                autoFocus
              >
                Open new run
              </button>
            </div>
          </div>
        )}

        {step === 'error' && error && (
          <div className="rewind-journey__body">
            <div className="rewind-journey__error-result" role="alert">
              <h3 className="rewind-journey__section-title">Rewind could not be completed</h3>
              <p>{error.message}</p>
              <p className="rewind-journey__recovery">
                The original run is preserved. You can safely retry the rewind.
              </p>
              <div className="rewind-journey__actions">
                <button className="rewind-journey__cancel" onClick={onClose}>
                  Close
                </button>
                <button
                  className="rewind-journey__retry"
                  onClick={() => {
                    setStep('target');
                    setError(null);
                  }}
                >
                  Try again
                </button>
              </div>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
