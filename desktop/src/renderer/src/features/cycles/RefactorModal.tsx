import { useCallback, useEffect, useMemo, useState } from 'react';
import type {
  AttentionItem,
  FeatureSnapshot,
  RefactorPreflightResult,
} from '../../../../shared/ipc';
import { parseIpcError, type WizardError } from '../../wizard/ipcError';
import { CycleFooter, CycleGateNotice, type CyclePhase } from './cycleShared';

export interface RefactorModalProps {
  featureId: string;
  snapshot: FeatureSnapshot;
  onCancel(): void;
  onDispatched(): void;
  attentionItems?: AttentionItem[];
  onOpenGate?: (featureId: string) => void;
}

export function RefactorModal({
  featureId,
  snapshot,
  onCancel,
  onDispatched,
  attentionItems,
  onOpenGate,
}: RefactorModalProps): React.ReactElement {
  const singleRepo = snapshot.repos.length === 1 ? snapshot.repos[0] : undefined;
  const [scope, setScope] = useState<'one' | 'all'>('one');
  const [repo, setRepo] = useState(singleRepo ?? '');
  const [prompt, setPrompt] = useState('');
  const [pipeline, setPipeline] = useState('');
  const [phase, setPhase] = useState<CyclePhase>('idle');
  const [preflight, setPreflight] = useState<RefactorPreflightResult | null>(null);
  const [error, setError] = useState<WizardError | null>(null);
  const action = snapshot.actions.find((candidate) => candidate.id === 'refactor');
  const pipelines = useMemo(
    () => action?.inputs?.find((input) => input.name === 'pipeline')?.options ?? [],
    [action],
  );
  const valid =
    prompt.trim() !== '' && (singleRepo !== undefined || scope === 'all' || repo !== '');

  const preflightRequest = useMemo(
    () => ({
      featureId,
      ...((singleRepo ?? (scope === 'one' ? repo : '')) === '' ? {} : { repo: singleRepo ?? repo }),
      prompt: prompt.trim(),
      ...(pipeline === '' ? {} : { pipeline }),
    }),
    [featureId, pipeline, prompt, repo, scope, singleRepo],
  );

  useEffect(() => {
    setPreflight(null);
    setError(null);
    if (!valid) {
      setPhase('idle');
      return;
    }
    let active = true;
    const timer = window.setTimeout(() => {
      setPhase('loading');
      void window.agentico
        .preflightRefactor(preflightRequest)
        .then((result) => {
          if (!active) return;
          setPreflight(result);
          setPhase('ready');
        })
        .catch((err: unknown) => {
          if (!active) return;
          setError(parseIpcError(err));
          setPhase('error');
        });
    }, 400);
    return () => {
      active = false;
      window.clearTimeout(timer);
    };
  }, [preflightRequest, valid]);

  const blockers = preflight?.blockers ?? [];
  const start = useCallback(async () => {
    if (preflight === null || blockers.length > 0) return;
    setPhase('dispatching');
    setError(null);
    try {
      await window.agentico.startRefactor({
        featureId,
        ...((singleRepo ?? (scope === 'one' ? repo : '')) === ''
          ? {}
          : { repo: singleRepo ?? repo }),
        prompt: prompt.trim(),
        ...(pipeline === '' ? {} : { pipeline }),
        sourceRevision: preflight.sourceRevision,
      });
      onDispatched();
      onCancel();
    } catch (err) {
      setError(parseIpcError(err));
      setPreflight(null);
      setPhase('error');
    }
  }, [
    blockers.length,
    featureId,
    onCancel,
    onDispatched,
    pipeline,
    preflight,
    prompt,
    repo,
    scope,
    singleRepo,
  ]);

  return (
    <div className="cycle-modal" data-phase={phase}>
      <CycleGateNotice
        featureId={featureId}
        snapshot={snapshot}
        attentionItems={attentionItems}
        onOpenGate={onOpenGate}
      />
      <label className="form-field cycle-modal__prompt">
        <span className="form-field__label">Refactor prompt</span>
        <textarea
          aria-label="Refactor prompt"
          autoFocus
          maxLength={4000}
          rows={6}
          placeholder="Describe the refactoring work…"
          value={prompt}
          disabled={phase === 'dispatching'}
          onChange={(event) => setPrompt(event.target.value)}
        />
      </label>
      {singleRepo !== undefined ? (
        <p className="cycle-modal__context">Runs on {singleRepo}</p>
      ) : (
        <>
          <fieldset className="cycle-journey__scope" aria-label="Scope">
            <legend>Scope</legend>
            <label>
              <input
                type="radio"
                name="refactor-scope"
                checked={scope === 'one'}
                onChange={() => setScope('one')}
              />
              One repository
            </label>
            <label>
              <input
                type="radio"
                name="refactor-scope"
                checked={scope === 'all'}
                onChange={() => {
                  setScope('all');
                  setRepo('');
                }}
              />
              All repositories
            </label>
          </fieldset>
          {scope === 'one' ? (
            <label className="form-field">
              <span className="form-field__label">Repository</span>
              <select
                aria-label="Repository"
                value={repo}
                onChange={(event) => setRepo(event.target.value)}
              >
                <option value="">Select a repository…</option>
                {snapshot.repos.map((repoName) => (
                  <option key={repoName} value={repoName}>
                    {repoName}
                  </option>
                ))}
              </select>
            </label>
          ) : null}
        </>
      )}
      {pipelines.length > 0 ? (
        <label className="form-field">
          <span className="form-field__label">Pipeline (optional)</span>
          <select
            aria-label="Pipeline (optional)"
            value={pipeline}
            onChange={(event) => setPipeline(event.target.value)}
          >
            <option value="">Default</option>
            {pipelines.map((option) => (
              <option key={option} value={option}>
                {option.charAt(0).toUpperCase() + option.slice(1)}
              </option>
            ))}
          </select>
        </label>
      ) : null}
      {phase === 'loading' ? (
        <p className="cycle-journey__preflight-note" role="status">
          Resolving refactor scope…
        </p>
      ) : null}
      {preflight !== null ? (
        <div className="cycle-journey__preflight">
          <p className="cycle-journey__resolved-repos">Applies to: {preflight.repos.join(', ')}</p>
          {blockers.length > 0 ? (
            <div className="cycle-modal__blockers">
              {blockers.map((blocker) => (
                <p key={blocker}>{blocker}</p>
              ))}
            </div>
          ) : null}
        </div>
      ) : null}
      {error !== null ? (
        <p className="form-field__error" role="alert">
          {error.message}
        </p>
      ) : null}
      <CycleFooter
        onCancel={onCancel}
        primaryLabel={phase === 'dispatching' ? 'Starting…' : 'Start refactor'}
        primaryDisabled={!valid || preflight === null || blockers.length > 0}
        busy={phase === 'dispatching'}
        onPrimary={() => void start()}
      />
    </div>
  );
}
