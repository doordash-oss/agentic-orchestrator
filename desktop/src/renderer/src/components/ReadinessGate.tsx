/**
 * Decides what a ready connection shows: nothing but a loading state until
 * the first authoritative readiness snapshot arrives (so an already-ready
 * runtime never flashes the wizard), the mandatory setup wizard while any
 * gate is unsatisfied, and the main view once everything passes. Mounted
 * fresh on every reconnect, so resume always starts from the server truth.
 */
import { useCallback, useEffect, useState } from 'react';
import type { ReadinessSnapshot } from '../../../shared/ipc';
import { deriveWizardState } from '../wizard/deriveWizardState';
import { parseIpcError, type WizardError } from '../wizard/ipcError';
import { SetupWizard } from './wizard/SetupWizard';

type GateState =
  | { phase: 'loading' }
  | { phase: 'error'; error: WizardError }
  | { phase: 'loaded'; snapshot: ReadinessSnapshot };

export function ReadinessGate() {
  const [state, setState] = useState<GateState>({ phase: 'loading' });

  const load = useCallback(() => {
    setState({ phase: 'loading' });
    window.agentico
      .getReadiness()
      .then((snapshot) => setState({ phase: 'loaded', snapshot }))
      .catch((err: unknown) => setState({ phase: 'error', error: parseIpcError(err) }));
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  if (state.phase === 'loading') {
    return (
      <section className="shell-card setup-gate" aria-label="Runtime readiness">
        <p className="setup-gate__loading" role="status" aria-live="polite">
          Checking runtime readiness…
        </p>
      </section>
    );
  }

  if (state.phase === 'error') {
    return (
      <section className="shell-card setup-gate" aria-label="Runtime readiness">
        <div className="shell-card__error">
          <div className="shell-card__error-head">
            <span className="shell-card__error-code">{state.error.code}</span>
          </div>
          <p className="shell-card__error-message">{state.error.message}</p>
          <p className="shell-card__error-remediation">
            The readiness check could not be completed. Retry once the runtime is reachable.
          </p>
          <button type="button" className="shell-card__retry" onClick={load}>
            Try again
          </button>
        </div>
      </section>
    );
  }

  const derived = deriveWizardState(state.snapshot);
  if (derived.complete) {
    return <WorkspaceHome snapshot={state.snapshot} />;
  }

  return (
    <SetupWizard
      snapshot={state.snapshot}
      onSnapshot={(snapshot) => setState({ phase: 'loaded', snapshot })}
    />
  );
}

interface WorkspaceHomeProps {
  snapshot: ReadinessSnapshot;
}

function WorkspaceHome({ snapshot }: WorkspaceHomeProps) {
  // STUB(Phase 1 Task 5): creation flow — this readiness-gated main view
  // gains the feature dashboard and the new-feature wizard in Task 5.
  const repositories = snapshot.repositories.filter((repository) => repository.valid);
  return (
    <section className="shell-card setup-home" aria-label="Workspace">
      <header className="shell-card__identity">
        <h1 className="shell-card__title">Agentico</h1>
        <span className="shell-card__version">runtime ready</span>
      </header>
      <p className="shell-card__status" role="status" aria-live="polite">
        <span className="shell-card__status-icon" aria-hidden="true">
          ●
        </span>
        <span className="shell-card__status-label" data-status="ready">
          Ready
        </span>
        <span className="shell-card__status-detail">
          {repositories.length === 1
            ? '1 repository available.'
            : `${repositories.length} repositories available.`}{' '}
          Feature creation arrives in the next milestone.
        </span>
      </p>
    </section>
  );
}
