/**
 * Feature cockpit: always reloads the authoritative feature snapshot from the
 * server, renders server-owned setup/run/action state, and resolves the
 * feature's blocking attention items through the same controls as the inbox.
 * A vanished feature renders a close affordance instead of crashing. No runtime
 * files are read here.
 */
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type Dispatch,
  type RefObject,
  type SetStateAction,
} from 'react';
import {
  isPendingReviewStatus,
  type FeatureSnapshot,
  type RunDetailView,
  type SetupTaskView,
  type ResourceEntry,
} from '../../../shared/ipc';
import { PhaseSpine } from '../components/PhaseSpine';
import { useMediaQuery } from '../hooks';
import { parseIpcError, type WizardError } from '../wizard/ipcError';
import { RunTimeline } from './RunTimeline';
import { ReviewSurface } from './ReviewSurface';
import { ResourceEditor, useResolvedTheme } from './ResourceWorkspace';
import { ArchiveMode } from './ArchiveMode';
import { RewindJourney } from './RewindJourney';
import type { FeatureActionResult } from '../../../shared/ipc';
import {
  AttentionDetail,
  attentionActionNotice,
  attentionErrorMessage,
  runAttentionSubmit,
  type AttentionAction,
  type AttentionDrafts,
  type AttentionSubmitOptions,
} from './AttentionInbox';
import type { AttentionItem } from '../../../shared/ipc';
import {
  actionById,
  displayPhaseLabel,
  featureBranch,
  isReadyToStart,
  setupProgress,
  showsRun,
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
  attentionItems: AttentionItem[];
  refreshAttention(): Promise<AttentionItem[]>;
  attentionDrafts: AttentionDrafts;
  setAttentionDrafts: Dispatch<SetStateAction<AttentionDrafts>>;
  /** Selected sealed run number for archive mode; null/0 means current run. */
  selectedRunNumber?: number | null;
  /** Persist a new selected run number (or null to return to current). */
  onSelectRun?(runNumber: number | null): void;
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

function IdentityFacts({ snapshot, branch }: { snapshot: FeatureSnapshot; branch: string | null }) {
  return (
    <dl className="cockpit__facts">
      <div className="cockpit__fact">
        <dt>Status</dt>
        <dd aria-label={snapshot.status}>
          <code data-status={snapshot.status} />
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
  );
}

interface RewindLandingProps {
  outcome: FeatureActionResult;
  run: RunDetailView | null;
  onOpenSource(runNumber: number): void;
  onDismiss(): void;
}

/** Durable, current-run context after a completed seal-and-fork operation. */
function RewindLanding({ outcome, run, onOpenSource, onDismiss }: RewindLandingProps) {
  const sourceRun = outcome.sourceRunNumber ?? run?.carriedFromRun;
  const carriedPhases = run?.carriedPhases ?? [];
  const warnings = outcome.warnings ?? [];
  if (sourceRun === undefined && carriedPhases.length === 0 && warnings.length === 0) return null;

  return (
    <section className="cockpit__rewind-landing" aria-label="Rewind outcome" role="status">
      <div className="cockpit__rewind-landing-header">
        <div>
          <p className="cockpit__eyebrow">Current fork</p>
          <h3 className="setup-step__title">
            {outcome.newRunNumber !== undefined
              ? `Run ${outcome.newRunNumber} is active`
              : 'Fork active'}
          </h3>
        </div>
        <button type="button" className="cockpit__landing-dismiss" onClick={onDismiss}>
          Dismiss
        </button>
      </div>
      {sourceRun !== undefined ? (
        <p className="cockpit__rewind-source">
          This run forked from sealed{' '}
          <button
            type="button"
            className="cockpit__source-link"
            onClick={() => onOpenSource(sourceRun)}
          >
            Run {sourceRun}
          </button>
          .
        </p>
      ) : null}
      {carriedPhases.length > 0 ? (
        <div className="cockpit__carried-material">
          <span className="cockpit__carried-label">Carried material</span>
          {carriedPhases.map((phase) => (
            <span key={phase} className="cockpit__carried-badge">
              {displayPhaseLabel(phase)} · Carried from Run {sourceRun ?? 'source'}
            </span>
          ))}
        </div>
      ) : null}
      {warnings.length > 0 ? (
        <div className="cockpit__rewind-warnings" role="alert">
          <h4>Rewind warnings</h4>
          <p>Review these non-fatal recovery details before continuing work.</p>
          <ul>
            {warnings.map((warning) => (
              <li key={warning}>{warning}</li>
            ))}
          </ul>
        </div>
      ) : null}
    </section>
  );
}

function SetupDetails({ snapshot }: { snapshot: FeatureSnapshot }) {
  if (snapshot.setup === undefined) return null;
  const progress = setupProgress(snapshot.setup);
  return (
    <section className="cockpit__setup" aria-label="Durable setup">
      <h3 className="setup-step__title">Durable setup</h3>
      <p className="cockpit__progress" aria-live="polite">
        {progress.done} of {progress.total} tasks complete
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
  );
}

function InspectorContent({
  snapshot,
  branch,
  stale,
  startAction,
}: {
  snapshot: FeatureSnapshot;
  branch: string | null;
  stale: boolean;
  startAction: ReturnType<typeof actionById>;
}) {
  return (
    <>
      <header className="cockpit__header">
        <div className="cockpit__identity">
          <h2 className="cockpit__title">{snapshot.name}</h2>
          <IdentityFacts snapshot={snapshot} branch={branch} />
        </div>
        {stale ? (
          <p role="status" className="cockpit__stale">
            Refreshing from the runtime…
          </p>
        ) : null}
      </header>
      <SetupDetails snapshot={snapshot} />
      {startAction?.disabledReasons.map((reason) => (
        <p key={reason.code} className="cockpit__action-reason">
          Start unavailable: {reason.message}
        </p>
      ))}
    </>
  );
}

function CockpitActionBar({
  snapshot,
  setupAction,
  startAction,
  stopAction,
  busy,
  inspectorOpen,
  inspectorButtonRef,
  stopButtonRef,
  onOpenInspector,
  onRetrySetup,
  onStart,
  onStop,
  rewindEnabled,
  onOpenRewind,
  canOpenHistory,
  onOpenHistory,
}: {
  snapshot: FeatureSnapshot;
  setupAction: ReturnType<typeof actionById>;
  startAction: ReturnType<typeof actionById>;
  stopAction: ReturnType<typeof actionById>;
  busy: boolean;
  inspectorOpen: boolean;
  inspectorButtonRef: RefObject<HTMLButtonElement | null>;
  stopButtonRef: RefObject<HTMLButtonElement | null>;
  onOpenInspector(): void;
  onRetrySetup(): void;
  onStart(): void;
  onStop(): void;
  rewindEnabled: boolean;
  onOpenRewind(): void;
  canOpenHistory: boolean;
  onOpenHistory(): void;
}) {
  return (
    <div className="cockpit__actions" role="group" aria-label="Feature actions">
      {setupAction !== undefined ? (
        <span className="cockpit__action">
          <button
            type="button"
            className="setup-wizard__action"
            disabled={!setupAction.enabled || busy}
            onClick={onRetrySetup}
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
            <ul className="cockpit__action-reasons">
              <li>{setupAction.disabledReasons[0].message}</li>
            </ul>
          ) : null}
        </span>
      ) : null}
      {startAction !== undefined ? (
        <span className="cockpit__action">
          <button
            type="button"
            className="cockpit__start"
            disabled={!startAction.enabled || busy}
            onClick={onStart}
            aria-describedby={
              startAction.disabledReasons.length > 0 ? 'start-disabled-reasons' : undefined
            }
          >
            {busy ? 'Starting…' : 'Start'}
          </button>
          {startAction.disabledReasons.length > 0 ? (
            <ul id="start-disabled-reasons" className="cockpit__action-reasons">
              {startAction.disabledReasons.map((reason) => (
                <li key={`${reason.code}:${reason.message}`}>{reason.message}</li>
              ))}
            </ul>
          ) : null}
        </span>
      ) : null}
      {stopAction !== undefined ? (
        <span className="cockpit__action">
          <button
            ref={stopButtonRef}
            type="button"
            className="cockpit__stop"
            disabled={!stopAction.enabled || busy}
            onClick={onStop}
            aria-describedby={
              stopAction.disabledReasons.length > 0 ? 'stop-disabled-reasons' : undefined
            }
          >
            Stop
          </button>
          {stopAction.disabledReasons.length > 0 ? (
            <ul id="stop-disabled-reasons" className="cockpit__action-reasons">
              {stopAction.disabledReasons.map((reason) => (
                <li key={`${reason.code}:${reason.message}`}>{reason.message}</li>
              ))}
            </ul>
          ) : null}
        </span>
      ) : null}
      {rewindEnabled ? (
        <button
          type="button"
          className="cockpit__rewind-button"
          onClick={onOpenRewind}
          aria-label="Rewind feature"
        >
          ↺ Rewind
        </button>
      ) : null}
      {canOpenHistory ? (
        <button
          type="button"
          className="cockpit__history-button"
          onClick={onOpenHistory}
          aria-label="View run history"
        >
          ▦ History
        </button>
      ) : null}
      <p className="cockpit__phase-status" aria-label="Current feature status">
        <code>{snapshot.status}</code>
      </p>
      <button
        ref={inspectorButtonRef}
        type="button"
        className="cockpit__inspector-toggle"
        aria-expanded={inspectorOpen}
        aria-controls="cockpit-inspector-drawer"
        onClick={onOpenInspector}
      >
        Inspector
      </button>
    </div>
  );
}

function InspectorDrawer({
  snapshot,
  branch,
  stale,
  startAction,
  onClose,
}: {
  snapshot: FeatureSnapshot;
  branch: string | null;
  stale: boolean;
  startAction: ReturnType<typeof actionById>;
  onClose(): void;
}) {
  const drawerRef = useRef<HTMLElement>(null);
  useEffect(() => {
    drawerRef.current
      ?.querySelector<HTMLElement>(
        'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])',
      )
      ?.focus();
    const handleKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        onClose();
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [onClose]);

  return (
    <div className="cockpit__drawer-backdrop" onMouseDown={onClose}>
      <aside
        ref={drawerRef}
        id="cockpit-inspector-drawer"
        className="cockpit__drawer"
        role="dialog"
        aria-modal="true"
        aria-label="Feature inspector"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header>
          <h3>Feature inspector</h3>
          <button type="button" onClick={onClose}>
            Close inspector
          </button>
        </header>
        <InspectorContent
          snapshot={snapshot}
          branch={branch}
          stale={stale}
          startAction={startAction}
        />
      </aside>
    </div>
  );
}

function StopConfirmDialog({
  snapshot,
  liveSessionCount,
  busy,
  onClose,
  onConfirm,
}: {
  snapshot: FeatureSnapshot;
  liveSessionCount: number;
  busy: boolean;
  onClose(): void;
  onConfirm(): void;
}) {
  const dialogRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const handleKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key === 'Escape' && !busy) {
        event.preventDefault();
        onClose();
        return;
      }
      if (event.key !== 'Tab' || busy) return;
      const controls = [
        ...(dialogRef.current?.querySelectorAll<HTMLButtonElement>('button:not(:disabled)') ?? []),
      ];
      const first = controls[0];
      const last = controls.at(-1);
      if (first === undefined || last === undefined) return;
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [busy, onClose]);

  return (
    <div className="impact-dialog__backdrop">
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="stop-dialog-title"
        className="impact-dialog"
      >
        <span className="impact-dialog__eyebrow">Operational impact</span>
        <h3 id="stop-dialog-title">Stop {snapshot.name}?</h3>
        <p>
          This asks the runtime to stop <strong>{snapshot.currentPhase}</strong> for this feature.
          It currently affects {liveSessionCount}{' '}
          {liveSessionCount === 1 ? 'live session' : 'live sessions'}.
        </p>
        <p className="impact-dialog__note">
          Existing validated transcript content remains available for inspection.
        </p>
        <div className="impact-dialog__actions">
          <button type="button" onClick={onClose} disabled={busy} autoFocus>
            Keep running
          </button>
          <button type="button" className="cockpit__stop" onClick={onConfirm} disabled={busy}>
            {busy ? 'Stopping…' : 'Confirm stop'}
          </button>
        </div>
      </div>
    </div>
  );
}

function FeatureConfigEditor({ featureId }: { featureId: string }) {
  const [configResourceId, setConfigResourceId] = useState<string | null>(null);
  const [configError, setConfigError] = useState<string | null>(null);
  const [configLoaded, setConfigLoaded] = useState(false);
  const [showConfig, setShowConfig] = useState(false);
  const [retryNonce, setRetryNonce] = useState(0);
  const theme = useResolvedTheme();

  useEffect(() => {
    let alive = true;
    setConfigLoaded(false);
    setConfigError(null);
    void window.agentico
      .listResources('feature_config')
      .then((cat) => {
        if (!alive) return;
        const entry = cat.resources.find((e: ResourceEntry) => e.featureId === featureId);
        if (entry) {
          setConfigResourceId(entry.id);
          setConfigError(null);
        } else {
          setConfigResourceId(null);
        }
        setConfigLoaded(true);
      })
      .catch((e: unknown) => {
        if (!alive) return;
        setConfigError(parseIpcError(e).message);
        setConfigLoaded(true);
      });
    return () => {
      alive = false;
    };
  }, [featureId, retryNonce]);

  return (
    <section className="cockpit__config" aria-label="Feature configuration">
      <button
        type="button"
        className="cockpit__config-toggle"
        aria-expanded={showConfig}
        onClick={() => setShowConfig((v) => !v)}
      >
        <span aria-hidden="true">{showConfig ? '▾' : '▸'}</span>
        Configuration
      </button>
      {showConfig && (
        <div className="cockpit__config-body">
          {configError ? (
            <div className="cockpit__config-error">
              <p className="resource-editor__notice resource-editor__notice--error" role="alert">
                Could not load configuration — {configError}
              </p>
              <button
                type="button"
                className="resource-editor__btn"
                onClick={() => setRetryNonce((n) => n + 1)}
              >
                Retry
              </button>
            </div>
          ) : configResourceId ? (
            <ResourceEditor resourceId={configResourceId} theme={theme} />
          ) : configLoaded ? (
            <p className="resource-editor__notice">
              No configuration resource found for this feature.
            </p>
          ) : (
            <p className="resource-editor__notice">Loading configuration…</p>
          )}
        </div>
      )}
    </section>
  );
}

export function FeatureCockpit({
  featureId,
  titleHint,
  onClose,
  onLoadedName,
  attentionItems,
  refreshAttention,
  attentionDrafts,
  setAttentionDrafts,
  selectedRunNumber,
  onSelectRun,
}: FeatureCockpitProps) {
  const [state, setState] = useState<CockpitState>({ phase: 'loading' });
  const [stale, setStale] = useState(false);
  const [busy, setBusy] = useState(false);
  const [announcement, setAnnouncement] = useState('');
  const [actionError, setActionError] = useState<{
    action: 'Start' | 'Stop';
    error: WizardError;
  } | null>(null);
  const [stopDialog, setStopDialog] = useState(false);
  const [liveSessionCount, setLiveSessionCount] = useState(0);
  const [inspectorOpen, setInspectorOpen] = useState(false);
  const [attentionBusy, setAttentionBusy] = useState<string | null>(null);
  const [rewindDialog, setRewindDialog] = useState(false);
  const [rewindLanding, setRewindLanding] = useState<{
    outcome: FeatureActionResult;
    run: RunDetailView | null;
  } | null>(null);
  const [currentRunBadges, setCurrentRunBadges] = useState({ changed: false, attention: false });
  const isNarrow = useMediaQuery('(max-width: 900px)');
  const actionInFlightRef = useRef(false);
  const loadRequestRef = useRef(0);
  const attentionDraftSaves = useRef(new Map<string, Promise<void>>());
  const stopButtonRef = useRef<HTMLButtonElement>(null);
  const attentionRegionRef = useRef<HTMLElement>(null);
  const inspectorButtonRef = useRef<HTMLButtonElement>(null);
  const onLoadedNameRef = useRef(onLoadedName);
  onLoadedNameRef.current = onLoadedName;

  const load = useCallback(
    (options: { silent?: boolean } = {}) => {
      const request = ++loadRequestRef.current;
      if (options.silent !== true) {
        setState({ phase: 'loading' });
      }
      return window.agentico
        .getFeature(featureId)
        .then((snapshot) => {
          if (request !== loadRequestRef.current) return;
          setState({ phase: 'loaded', snapshot });
          onLoadedNameRef.current(snapshot.name);
        })
        .catch((err: unknown) => {
          if (request !== loadRequestRef.current) return;
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
        if (
          selectedRunNumber !== undefined &&
          selectedRunNumber !== null &&
          selectedRunNumber > 0
        ) {
          setCurrentRunBadges((badges) => ({
            ...badges,
            changed: true,
          }));
        }
        load({ silent: true });
      }
    });
    return () => {
      loadRequestRef.current += 1;
      unsubscribe();
    };
  }, [featureId, load, selectedRunNumber]);

  useEffect(() => {
    if (selectedRunNumber === undefined || selectedRunNumber === null || selectedRunNumber <= 0) {
      return;
    }
    if (attentionItems.some((item) => item.featureId === featureId)) {
      setCurrentRunBadges((badges) => ({ ...badges, attention: true }));
    }
  }, [attentionItems, featureId, selectedRunNumber]);

  const retrySetup = useCallback(() => {
    if (actionInFlightRef.current) {
      return;
    }
    actionInFlightRef.current = true;
    setBusy(true);
    setActionError(null);
    setAnnouncement('');
    window.agentico
      .dispatchFeatureSetup(featureId)
      .then(() => {
        setAnnouncement('Setup dispatched. Progress updates below.');
        return load({ silent: true });
      })
      .catch((err: unknown) => {
        const parsed = parseIpcError(err);
        setAnnouncement(`Retry failed — ${parsed.message}`);
        return load({ silent: true });
      })
      .finally(() => {
        actionInFlightRef.current = false;
        setBusy(false);
      });
  }, [featureId, load]);

  const start = useCallback(() => {
    if (actionInFlightRef.current) {
      return;
    }
    actionInFlightRef.current = true;
    setBusy(true);
    setActionError(null);
    setAnnouncement('Starting from the current server snapshot…');
    window.agentico
      .dispatchFeatureAction({ featureId, action: 'start' })
      .then(() => {
        setAnnouncement('Start accepted. Refreshing authoritative run state…');
        return load({ silent: true });
      })
      .catch((err: unknown) => {
        const parsed = parseIpcError(err);
        setActionError({ action: 'Start', error: parsed });
        setAnnouncement('');
        return load({ silent: true });
      })
      .finally(() => {
        actionInFlightRef.current = false;
        setBusy(false);
      });
  }, [featureId, load]);

  const closeStopDialog = useCallback(() => {
    setStopDialog(false);
    stopButtonRef.current?.focus();
  }, []);

  const saveAttentionDraft = (
    id: string,
    action: AttentionAction,
    options: AttentionSubmitOptions = { successNotice: 'Draft saved.' },
  ): Promise<void> => {
    const previous = attentionDraftSaves.current.get(id) ?? Promise.resolve();
    const run = previous
      .catch(() => undefined)
      .then(async () => {
        try {
          const result = await action();
          setAnnouncement(attentionActionNotice(result, options));
          if (result.alreadyResolved === true) {
            await refreshAttention();
            await load({ silent: true });
          }
        } catch (error) {
          setAnnouncement(attentionErrorMessage(error));
          throw error;
        }
      });
    const tracked = run.finally(() => {
      if (attentionDraftSaves.current.get(id) === tracked) {
        attentionDraftSaves.current.delete(id);
      }
    });
    attentionDraftSaves.current.set(id, tracked);
    return tracked;
  };

  const closeInspector = useCallback(() => {
    setInspectorOpen(false);
    requestAnimationFrame(() => inspectorButtonRef.current?.focus());
  }, []);

  useEffect(() => {
    if (!isNarrow) {
      setInspectorOpen(false);
    }
  }, [isNarrow]);

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
  const stopAction = actionById(snapshot, 'pause-stop');
  const rewindAction = actionById(snapshot, 'rewind');
  const hasPendingReview = isPendingReviewStatus(snapshot.status);
  const isArchiveMode =
    selectedRunNumber !== undefined && selectedRunNumber !== null && selectedRunNumber > 0;

  const openStopDialog = async () => {
    setActionError(null);
    try {
      const sessions = await window.agentico.listSessions();
      const count = sessions.filter(
        (session) =>
          session.featureId === featureId &&
          session.runNumber === snapshot.activeRun &&
          ['running', 'active', 'starting', 'stopping'].includes(session.status.toLowerCase()),
      ).length;
      setLiveSessionCount(count);
      setStopDialog(true);
    } catch (error) {
      setActionError({ action: 'Stop', error: parseIpcError(error) });
    }
  };

  const confirmStop = () => {
    if (actionInFlightRef.current) return;
    actionInFlightRef.current = true;
    setBusy(true);
    setActionError(null);
    setAnnouncement('Stopping authorized work…');
    window.agentico
      .dispatchFeatureAction({ featureId, action: 'pause-stop' })
      .then(() => {
        setAnnouncement('Stop accepted. Refreshing authoritative state…');
        return load({ silent: true });
      })
      .then(() => closeStopDialog())
      .catch((error: unknown) => {
        setActionError({ action: 'Stop', error: parseIpcError(error) });
        setStopDialog(false);
        return load({ silent: true });
      })
      .finally(() => {
        actionInFlightRef.current = false;
        setBusy(false);
        stopButtonRef.current?.focus();
      });
  };

  const visibleAttentionItems = attentionItems.filter(
    (item) => !(hasPendingReview && item.kind === 'review'),
  );

  const openHistory = () => {
    void window.agentico
      // Fetch a bounded first page that includes a sealed run when the active
      // run is newest. A one-item page can only contain current.
      .listRuns({ featureId, page: 1, pageSize: 20 })
      .then((result) => {
        const sealed = result.runs.find((run) => run.runNumber !== snapshot.activeRun);
        if (sealed !== undefined) {
          onSelectRun?.(sealed.runNumber);
          return;
        }
        setAnnouncement('No sealed runs are available for this feature yet.');
      })
      .catch((error: unknown) => {
        setAnnouncement(`Could not open run history — ${parseIpcError(error).message}`);
      });
  };

  return (
    <section className="cockpit" aria-label={`Feature ${snapshot.name}`}>
      {isArchiveMode ? (
        <ArchiveMode
          featureId={featureId}
          selectedRunNumber={selectedRunNumber!}
          currentRunNumber={snapshot.activeRun}
          pipeline={snapshot.pipeline}
          currentRunBadges={currentRunBadges}
          onReturnToCurrent={() => {
            onSelectRun?.(null);
            setCurrentRunBadges({ changed: false, attention: false });
          }}
          onSelectRun={(runNumber) => {
            onSelectRun?.(runNumber);
            setCurrentRunBadges({ changed: false, attention: false });
          }}
        />
      ) : (
        <>
          <PhaseSpine
            stages={stages}
            activeIndex={spineActiveIndex(snapshot, stages)}
            tone={spineTone(snapshot)}
            label="Feature pipeline"
          />

          <CockpitActionBar
            snapshot={snapshot}
            setupAction={setupAction}
            startAction={startAction}
            stopAction={stopAction}
            busy={busy}
            inspectorOpen={isNarrow && inspectorOpen}
            inspectorButtonRef={inspectorButtonRef}
            stopButtonRef={stopButtonRef}
            onOpenInspector={() => setInspectorOpen(true)}
            onRetrySetup={retrySetup}
            onStart={start}
            onStop={() => void openStopDialog()}
            rewindEnabled={rewindAction?.enabled === true}
            onOpenRewind={() => setRewindDialog(true)}
            canOpenHistory={onSelectRun !== undefined}
            onOpenHistory={openHistory}
          />

          {isNarrow && inspectorOpen ? (
            <InspectorDrawer
              snapshot={snapshot}
              branch={branch}
              stale={stale}
              startAction={startAction}
              onClose={closeInspector}
            />
          ) : null}

          <div className="cockpit__content">
            <main className="cockpit__canvas">
              {rewindLanding !== null ? (
                <RewindLanding
                  outcome={rewindLanding.outcome}
                  run={rewindLanding.run}
                  onOpenSource={(runNumber) => onSelectRun?.(runNumber)}
                  onDismiss={() => setRewindLanding(null)}
                />
              ) : null}
              <FeatureConfigEditor featureId={featureId} />
              {hasPendingReview ? (
                <ReviewSurface featureId={featureId} onResolved={() => load({ silent: true })} />
              ) : null}
              {visibleAttentionItems.length > 0 ? (
                <section
                  ref={attentionRegionRef}
                  className="cockpit__attention"
                  aria-label="Feature attention"
                  tabIndex={-1}
                >
                  <h3>Blocking input</h3>
                  {visibleAttentionItems.map((item) => (
                    <AttentionDetail
                      key={`${item.kind}:${item.id}`}
                      item={item}
                      busy={attentionBusy === item.id}
                      drafts={attentionDrafts}
                      setDrafts={setAttentionDrafts}
                      saveDraft={(action, options) => saveAttentionDraft(item.id, action, options)}
                      submit={async (action, options) => {
                        if (attentionBusy !== null) return;
                        const invoker =
                          document.activeElement instanceof HTMLElement
                            ? document.activeElement
                            : null;
                        setAttentionBusy(item.id);
                        try {
                          const { notice } = await runAttentionSubmit(
                            action,
                            async () => {
                              const latest = await refreshAttention();
                              await load({ silent: true });
                              return latest;
                            },
                            options,
                          );
                          setAnnouncement(notice);
                        } catch (error) {
                          setAnnouncement(attentionErrorMessage(error));
                        } finally {
                          setAttentionBusy(null);
                          requestAnimationFrame(() => {
                            if (invoker !== null && document.body.contains(invoker)) {
                              invoker.focus();
                            } else {
                              attentionRegionRef.current?.focus();
                            }
                          });
                        }
                      }}
                    />
                  ))}
                </section>
              ) : null}

              {showsRun(snapshot) && !hasPendingReview ? (
                <RunTimeline
                  featureId={featureId}
                  activeRun={snapshot.activeRun}
                  currentPhase={snapshot.currentPhase}
                  shouldStream={stopAction !== undefined}
                />
              ) : null}

              {ready ? (
                <div className="cockpit__empty-state" role="status">
                  <span aria-hidden="true">●</span> Ready to start
                  <p>Start runs the {snapshot.currentPhase} phase for this feature.</p>
                </div>
              ) : null}

              {snapshot.failure?.message !== undefined ? (
                <div role="alert" className="create-form__error">
                  <span className="create-form__error-code">
                    {snapshot.failure.type ?? 'failure'}
                  </span>
                  <p className="create-form__error-message">{snapshot.failure.message}</p>
                </div>
              ) : null}

              {actionError !== null ? (
                <div role="alert" className="create-form__error">
                  <span className="create-form__error-code">{actionError.error.code}</span>
                  <p className="create-form__error-message">
                    {actionError.action} was rejected — {actionError.error.message}
                  </p>
                </div>
              ) : null}

              <p className="cockpit__announcement" role="status" aria-live="polite">
                {announcement}
              </p>
            </main>
            {!isNarrow ? (
              <aside className="cockpit__inspector" aria-label="Feature inspector">
                <InspectorContent
                  snapshot={snapshot}
                  branch={branch}
                  stale={stale}
                  startAction={startAction}
                />
              </aside>
            ) : null}
          </div>

          {stopDialog ? (
            <StopConfirmDialog
              snapshot={snapshot}
              liveSessionCount={liveSessionCount}
              busy={busy}
              onClose={closeStopDialog}
              onConfirm={confirmStop}
            />
          ) : null}

          {rewindDialog ? (
            <RewindJourney
              featureId={featureId}
              featureName={snapshot.name}
              validPhaseOptions={
                rewindAction?.inputs?.find((input) => input.name === 'target_phase')?.options ?? []
              }
              currentRoadmapPhase={snapshot.currentRoadmapPhase}
              totalRoadmapPhases={snapshot.totalRoadmapPhases}
              onClose={() => setRewindDialog(false)}
              onRewindComplete={(result: FeatureActionResult) => {
                setRewindDialog(false);
                onSelectRun?.(null);
                setAnnouncement(
                  result.newRunNumber !== undefined
                    ? `Rewind complete. Run ${result.sourceRunNumber ?? ''} sealed, Run ${result.newRunNumber} is now active.`
                    : 'Rewind complete.',
                );
                if (result.newRunNumber !== undefined) {
                  void window.agentico
                    .getRun({ featureId, runNumber: result.newRunNumber })
                    .then((run) => setRewindLanding({ outcome: result, run }))
                    .catch(() => setRewindLanding({ outcome: result, run: null }));
                } else {
                  setRewindLanding({ outcome: result, run: null });
                }
                void load({ silent: true });
              }}
            />
          ) : null}
        </>
      )}
    </section>
  );
}
