/**
 * Minimal feature cockpit: always reloads the authoritative snapshot from
 * the server (fetch on mount, refetch on matching SSE invalidations and on
 * resync), shows server-owned setup order/status/attempts with safe failure
 * data and diagnostics, and renders its action row FROM the authoritative
 * action catalogue. Start is shown exactly as the server authorizes it but
 * is never invoked in this phase; Retry re-dispatches durable setup for the
 * SAME feature. A vanished feature (404) renders a close affordance instead
 * of crashing. No runtime files are ever read here.
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import type { FeatureSnapshot, SetupTaskView } from '../../../shared/ipc';
import { PhaseSpine } from '../components/PhaseSpine';
import { parseIpcError, type WizardError } from '../wizard/ipcError';
import {
  actionById,
  featureBranch,
  isReadyToStart,
  setupProgress,
  spineActiveIndex,
  spineStages,
  spineTone,
} from './featureView';

type CockpitState =
  | { phase: 'loading' }
  | { phase: 'missing' }
  | { phase: 'error'; error: WizardError }
  | { phase: 'loaded'; snapshot: FeatureSnapshot };

export interface FeatureCockpitProps {
  featureId: string;
  /** Local presentation hint shown until the authoritative name loads. */
  titleHint: string;
  onClose(): void;
  /** Reports the authoritative feature name for the tab title hint. */
  onLoadedName(name: string): void;
}

const TASK_STATUS_TEXT: Record<SetupTaskView['status'], string> = {
  queued: 'Queued',
  running: 'Running',
  done: 'Done',
  failed: 'Failed',
};

const TASK_STATUS_ICON: Record<SetupTaskView['status'], string> = {
  queued: '○',
  running: '◐',
  done: '●',
  failed: '✕',
};

export function FeatureCockpit({
  featureId,
  titleHint,
  onClose,
  onLoadedName,
}: FeatureCockpitProps) {
  const [state, setState] = useState<CockpitState>({ phase: 'loading' });
  const [stale, setStale] = useState(false);
  const [busy, setBusy] = useState(false);
  const [announcement, setAnnouncement] = useState('');
  const onLoadedNameRef = useRef(onLoadedName);
  onLoadedNameRef.current = onLoadedName;

  const load = useCallback(
    (options: { silent?: boolean } = {}) => {
      if (options.silent !== true) {
        setState({ phase: 'loading' });
      }
      window.agentico
        .getFeature(featureId)
        .then((snapshot) => {
          setState({ phase: 'loaded', snapshot });
          onLoadedNameRef.current(snapshot.name);
        })
        .catch((err: unknown) => {
          const parsed = parseIpcError(err);
          if (parsed.code === 'not_found') {
            setState({ phase: 'missing' });
          } else {
            setState({ phase: 'error', error: parsed });
          }
        });
    },
    [featureId],
  );

  // Fetch on mount; refetch on relevant invalidations; track stream health
  // so the view can show that it is refreshing after a reconnect.
  useEffect(() => {
    load();
    const unsubscribe = window.agentico.onAppEvent((event) => {
      if (event.type === 'status') {
        setStale(event.stream !== 'live');
        return;
      }
      const relevant =
        event.kind === 'resync' || event.featureId === featureId || event.resourceId === featureId;
      if (relevant) {
        load({ silent: true });
      }
    });
    return unsubscribe;
  }, [featureId, load]);

  const retrySetup = useCallback(() => {
    if (busy) {
      return;
    }
    setBusy(true);
    setAnnouncement('');
    window.agentico
      .dispatchFeatureSetup(featureId)
      .then(() => {
        setAnnouncement('Setup dispatched. Progress updates below.');
        load({ silent: true });
      })
      .catch((err: unknown) => {
        const parsed = parseIpcError(err);
        setAnnouncement(`Retry failed — ${parsed.message}`);
        load({ silent: true });
      })
      .finally(() => setBusy(false));
  }, [busy, featureId, load]);

  const startPlaceholder = useCallback(() => {
    // STUB(Phase 2): invoke start action via the server catalogue.
    setAnnouncement("Nothing was started — starting isn't available yet.");
  }, []);

  if (state.phase === 'loading') {
    return (
      <section className="cockpit" aria-label={`Feature ${titleHint}`}>
        <p role="status" aria-live="polite" className="cockpit__loading">
          Loading {titleHint} from the runtime…
        </p>
      </section>
    );
  }

  if (state.phase === 'missing') {
    return (
      <section className="cockpit" aria-label={`Feature ${titleHint}`}>
        <div role="alert" className="cockpit__missing">
          <p className="cockpit__missing-message">This feature no longer exists on the server.</p>
          <button type="button" className="setup-wizard__action" onClick={onClose}>
            Close tab
          </button>
        </div>
      </section>
    );
  }

  if (state.phase === 'error') {
    return (
      <section className="cockpit" aria-label={`Feature ${titleHint}`}>
        <div role="alert" className="create-form__error">
          <span className="create-form__error-code">{state.error.code}</span>
          <p className="create-form__error-message">{state.error.message}</p>
        </div>
        <button type="button" className="setup-wizard__action" onClick={() => load()}>
          Try again
        </button>
      </section>
    );
  }

  const { snapshot } = state;
  const stages = spineStages(snapshot.pipeline);
  const branch = featureBranch(snapshot);
  const ready = isReadyToStart(snapshot);
  const setupAction = actionById(snapshot, 'setup');
  const startAction = actionById(snapshot, 'start');

  return (
    <section className="cockpit" aria-label={`Feature ${snapshot.name}`}>
      <header className="cockpit__header">
        <div className="cockpit__identity">
          <h2 className="cockpit__title">{snapshot.name}</h2>
          <dl className="cockpit__facts">
            <div className="cockpit__fact">
              <dt>Status</dt>
              <dd>
                <code>{snapshot.status}</code>
              </dd>
            </div>
            {branch !== null ? (
              <div className="cockpit__fact">
                <dt>Branch</dt>
                <dd>
                  <code>{branch}</code>
                </dd>
              </div>
            ) : null}
            <div className="cockpit__fact">
              <dt>{snapshot.repos.length === 1 ? 'Repository' : 'Repositories'}</dt>
              <dd>
                <code>{snapshot.repos.join(', ')}</code>
              </dd>
            </div>
          </dl>
        </div>
        {stale ? (
          <p role="status" className="cockpit__stale">
            Refreshing from the runtime…
          </p>
        ) : null}
      </header>

      <PhaseSpine
        stages={stages}
        activeIndex={spineActiveIndex(snapshot, stages)}
        tone={spineTone(snapshot)}
        label="Feature pipeline"
      />

      {ready ? (
        <p className="cockpit__ready" role="status">
          <span aria-hidden="true">●</span> Ready to start
        </p>
      ) : null}

      {snapshot.failure?.message !== undefined ? (
        <div role="alert" className="create-form__error">
          <span className="create-form__error-code">{snapshot.failure.type ?? 'failure'}</span>
          <p className="create-form__error-message">{snapshot.failure.message}</p>
        </div>
      ) : null}

      <p className="cockpit__announcement" role="status" aria-live="polite">
        {announcement}
      </p>

      {snapshot.setup !== undefined ? (
        <section className="cockpit__setup" aria-label="Durable setup">
          <h3 className="setup-step__title">Durable setup</h3>
          <p className="cockpit__progress" aria-live="polite">
            {setupProgress(snapshot.setup).done} of {setupProgress(snapshot.setup).total} tasks
            complete
            {snapshot.setup.status === 'failed' ? ' — setup failed' : ''}
            {snapshot.setup.attempt > 1 ? ` (attempt ${snapshot.setup.attempt})` : ''}
          </p>
          {snapshot.setup.lastError !== undefined ? (
            <p className="form-field__error">{snapshot.setup.lastError}</p>
          ) : null}
          <ol className="task-list">
            {snapshot.setup.tasks.map((task) => (
              <li key={task.key} className="task-row" data-status={task.status}>
                <span className="task-row__state" data-status={task.status}>
                  <span aria-hidden="true">{TASK_STATUS_ICON[task.status]}</span>{' '}
                  {TASK_STATUS_TEXT[task.status]}
                </span>
                <span className="task-row__label">{task.label}</span>
                <span className="task-row__meta">
                  {task.repo !== undefined ? <code>{task.repo}</code> : null}
                  {task.branch !== undefined ? <code>{task.branch}</code> : null}
                  {task.attempt > 1 ? <code>attempt {task.attempt}</code> : null}
                </span>
                {task.error !== undefined ? (
                  <details className="task-row__diagnostics">
                    <summary className="task-row__error">{task.error}</summary>
                    <p className="task-row__diagnostics-detail">
                      Reported by the runtime for <code>{task.key}</code>
                      {task.attempt > 0 ? ` after attempt ${task.attempt}` : ''}. Retry re-runs only
                      unfinished tasks on this feature.
                    </p>
                  </details>
                ) : null}
              </li>
            ))}
          </ol>
        </section>
      ) : null}

      <div className="cockpit__actions" role="group" aria-label="Feature actions">
        {setupAction !== undefined ? (
          <span className="cockpit__action">
            <button
              type="button"
              className="setup-wizard__action"
              disabled={!setupAction.enabled || busy}
              onClick={retrySetup}
              {...(setupAction.disabledReasons[0] !== undefined
                ? { title: setupAction.disabledReasons[0].message }
                : {})}
            >
              {busy
                ? 'Dispatching…'
                : snapshot.setup?.status === 'failed'
                  ? 'Retry setup'
                  : 'Run setup'}
            </button>
            {!setupAction.enabled && setupAction.disabledReasons[0] !== undefined ? (
              <span className="cockpit__action-reason">
                {setupAction.disabledReasons[0].message}
              </span>
            ) : null}
          </span>
        ) : null}
        {startAction !== undefined ? (
          <span className="cockpit__action">
            <button
              type="button"
              className="cockpit__start"
              disabled={!startAction.enabled}
              onClick={startPlaceholder}
              {...(startAction.disabledReasons[0] !== undefined
                ? { title: startAction.disabledReasons[0].message }
                : {})}
            >
              Start
            </button>
            {!startAction.enabled && startAction.disabledReasons[0] !== undefined ? (
              <span className="cockpit__action-reason">
                {startAction.disabledReasons[0].message}
              </span>
            ) : null}
            {startAction.enabled ? (
              <span className="cockpit__action-reason">Starting isn&apos;t available yet.</span>
            ) : null}
          </span>
        ) : null}
      </div>
    </section>
  );
}
