import { useCallback, useEffect, useState } from 'react';
import type { AttentionItem, FeatureSnapshot, RebasePreflightResult } from '../../../../shared/ipc';
import { parseIpcError, type WizardError } from '../../wizard/ipcError';
import { CycleFooter, CycleGateNotice, humanizeFreshness, type CyclePhase } from './cycleShared';

export interface RebaseModalProps {
  featureId: string;
  snapshot: FeatureSnapshot;
  onCancel(): void;
  onDispatched(): void;
  attentionItems?: AttentionItem[];
  onOpenGate?: (featureId: string) => void;
}

export function RebaseModal({
  featureId,
  snapshot,
  onCancel,
  onDispatched,
  attentionItems,
  onOpenGate,
}: RebaseModalProps): React.ReactElement {
  const [phase, setPhase] = useState<CyclePhase>('loading');
  const [preflight, setPreflight] = useState<RebasePreflightResult | null>(null);
  const [error, setError] = useState<WizardError | null>(null);

  const loadPreflight = useCallback(async (preserveError = false) => {
    setPhase('loading');
    if (!preserveError) setError(null);
    try {
      setPreflight(await window.agentico.preflightRebase({ featureId }));
      setPhase('ready');
    } catch (err) {
      setPreflight(null);
      setError(parseIpcError(err));
      setPhase('error');
    }
  }, [featureId]);

  useEffect(() => {
    void loadPreflight();
  }, [loadPreflight]);

  const blockers =
    preflight?.repos.flatMap((repo) => [
      ...(repo.blocker === undefined || repo.blocker === '' ? [] : [repo.blocker]),
      ...(repo.conflictFiles ?? []).map((file) => `Conflict: ${file}`),
    ]) ?? [];

  const start = useCallback(async () => {
    if (preflight === null || blockers.length > 0) return;
    setPhase('dispatching');
    setError(null);
    try {
      await window.agentico.startRebase({
        featureId,
        sourceRevision: preflight.sourceRevision,
      });
      onDispatched();
      onCancel();
    } catch (err) {
      setError(parseIpcError(err));
      setPhase('error');
      await loadPreflight(true);
    }
  }, [blockers.length, featureId, loadPreflight, onCancel, onDispatched, preflight]);

  return (
    <div className="cycle-modal" data-phase={phase}>
      <CycleGateNotice
        featureId={featureId}
        snapshot={snapshot}
        attentionItems={attentionItems}
        onOpenGate={onOpenGate}
      />
      <p className="cycle-journey__description">
        Rebase each repository onto its target branch using this guarded preflight.
      </p>
      <div className="cycle-journey__preflight" aria-label="Rebase preflight">
        <p className="cycle-journey__preflight-heading">
          {phase === 'loading' ? 'Inspecting repository state…' : 'Repository manifest'}
        </p>
        {preflight !== null ? (
          <ul className="cycle-journey__preflight-list">
            {preflight.repos.map((repo) => {
              const blocked =
                (repo.blocker !== undefined && repo.blocker !== '') ||
                (repo.conflictFiles?.length ?? 0) > 0;
              return (
                <li
                  key={repo.repo}
                  className="cycle-journey__preflight-repo"
                  data-blocked={blocked}
                >
                  <span className="cycle-journey__preflight-repo-name">{repo.repo}</span>
                  <code className="cycle-journey__preflight-target">{repo.target}</code>
                  <span
                    className="cycle-journey__preflight-freshness"
                    data-freshness={repo.freshness}
                  >
                    {humanizeFreshness(repo.freshness)}
                  </span>
                  {repo.behind ? (
                    <span className="cycle-journey__preflight-status">Behind target</span>
                  ) : null}
                  {repo.blocker ? (
                    <span className="cycle-journey__preflight-blocker">{repo.blocker}</span>
                  ) : null}
                  {(repo.conflictFiles ?? []).map((file) => (
                    <span key={file} className="cycle-journey__preflight-blocker">
                      Conflict: {file}
                    </span>
                  ))}
                </li>
              );
            })}
          </ul>
        ) : null}
        {error !== null ? (
          <div className="cycle-journey__preflight-error">
            <p className="form-field__error" role="alert">
              {error.message}
            </p>
            <button
              type="button"
              className="cycle-journey__preflight-retry"
              disabled={phase === 'loading'}
              onClick={() => void loadPreflight()}
            >
              Retry preflight
            </button>
          </div>
        ) : null}
        {blockers.length > 0 ? (
          <p className="cycle-modal__blockers">Start is blocked: {blockers[0]}</p>
        ) : null}
      </div>
      <CycleFooter
        onCancel={onCancel}
        primaryLabel={
          phase === 'dispatching'
            ? 'Starting…'
            : phase === 'loading'
              ? 'Loading preflight…'
              : 'Start rebase'
        }
        primaryDisabled={preflight === null || blockers.length > 0}
        busy={phase === 'dispatching'}
        onPrimary={() => void start()}
      />
    </div>
  );
}
