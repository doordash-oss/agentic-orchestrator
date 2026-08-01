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
  type ReactNode,
  type RefObject,
  type SetStateAction,
} from 'react';
import {
  isPendingReviewStatus,
  type CycleView,
  type FeatureSnapshot,
  type RunDetailView,
  type FeatureActionRequest,
  type FeatureActionView,
} from '../../../shared/ipc';
import { PhaseLadder } from '../components/PhaseLadder';
import { useModalDismiss } from '../components/useModalDismiss';
import { useMediaQuery } from '../hooks';
import { parseIpcError, type WizardError } from '../wizard/ipcError';
import { CurrentRunInspection, type RunMetrics } from './CurrentRunInspection';
import { ReviewSurface } from './ReviewSurface';
import { FeatureConfigPanel } from './ConfigEditor';
import { ArchiveMode } from './ArchiveMode';
import { RewindJourney } from './RewindJourney';
import { RepositoryInstrument } from './RepositoryInstrument';
import { AftercareWorkspace } from './AftercareWorkspace';
import { CycleWorkspace } from './CycleWorkspace';
import { ImpactPreviewList } from './ImpactPreviewList';
import { NeedUserInputModal, type AttentionGate } from './NeedUserInputModal';
import {
  cycleIdentity,
  ownsPostImplementationStage,
  receiptForCycleEnd,
  resolvePostImplementationMode,
  type AftercareAction,
  type AftercareCycleId,
  type CycleReceipt,
} from './postImplementationModel';
import { RebaseModal } from './cycles/RebaseModal';
import { ReviewCommentsModal } from './cycles/ReviewCommentsModal';
import { RefactorLauncher } from './refactor/RefactorLauncher';
import { RefactorPassWorkspace } from './refactor/RefactorPassWorkspace';
import { refactoringStatusChip } from './refactor/refactorPassModel';
import { useCompletionPreflight } from './completion/useCompletionPreflight';
import {
  completionBarModel,
  type CompletionVerb,
  type CompletionVerbModel,
} from './completion/completionBarModel';
import { ChangesSurface } from './completion/ChangesSurface';
import { PublishModalBody } from './completion/PublishModal';
import { MergeModalBody } from './completion/MergeModal';
import { MarkDoneModalBody } from './completion/MarkDoneModal';
import { CleanupConfirm } from './completion/CleanupConfirm';
import type { CompletionAction } from './completion/completionShared';
import type { FeatureActionResult } from '../../../shared/ipc';
import {
  AttentionDetail,
  attentionActionNotice,
  attentionErrorMessage,
  runAttentionSubmit,
  type AttentionDrafts,
} from './AttentionInbox';
import { useAttentionDraftSaves } from './useAttentionDraftSaves';
import type { AttentionItem } from '../../../shared/ipc';
import {
  actionById,
  displayFeatureMessage,
  displayPhaseLabel,
  displayStatusLabel,
  featureBranch,
  formatDuration,
  isReadyToStart,
  isRunAtRest,
  showsRun,
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
  onDeleted?(featureId: string): void;
  /** Reports the authoritative feature name for the tab title hint. */
  onLoadedName(name: string): void;
  attentionItems: AttentionItem[];
  refreshAttention(): Promise<AttentionItem[]>;
  attentionDrafts: AttentionDrafts;
  setAttentionDrafts: Dispatch<SetStateAction<AttentionDrafts>>;
  attentionPreviewRequest?: { requestId: number; attentionId?: string } | null;
  onAttentionPreviewClose?(): void;
  /** Selected sealed run number for archive mode; null/0 means current run. */
  selectedRunNumber?: number | null;
  /** Persist a new selected run number (or null to return to current). */
  onSelectRun?(runNumber: number | null): void;
}

const MAX_ITERATIONS_RESTART_DELTA = 10;
const MAX_PLAN_ITERATIONS_RESTART_DELTA = 2;

function IdentityFacts({ snapshot, branch }: { snapshot: FeatureSnapshot; branch: string | null }) {
  return (
    <dl className="cockpit__facts">
      <div className="cockpit__fact">
        <dt>Status</dt>
        <dd aria-label={snapshot.status}>
          <code data-status={snapshot.status}>{displayStatusLabel(snapshot.status)}</code>
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

function InspectorContent({
  snapshot,
  branch,
  stale,
  runMetrics,
  onOpenPullRequest,
}: {
  snapshot: FeatureSnapshot;
  branch: string | null;
  stale: boolean;
  runMetrics: RunMetrics | null;
  onOpenPullRequest(url: string): void;
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
      <PhaseLadder snapshot={snapshot} />
      {runMetrics !== null ? (
        <section className="cockpit__run-totals" aria-label="This run">
          <h3 className="setup-step__title">This run</h3>
          <dl className="cockpit__facts">
            <div className="cockpit__fact">
              <dt>Total elapsed</dt>
              <dd>
                <code>{formatDuration(runMetrics.totalSeconds)}</code>
              </dd>
            </div>
            <div className="cockpit__fact">
              <dt>Total cost</dt>
              <dd>
                <code>${runMetrics.totalUsd.toFixed(2)}</code>
              </dd>
            </div>
          </dl>
        </section>
      ) : null}
      {snapshot.repoStatus !== undefined && snapshot.repoStatus.length > 0 ? (
        <RepositoryInstrument repos={snapshot.repoStatus} onOpenPullRequest={onOpenPullRequest} />
      ) : null}
    </>
  );
}

const PRIMARY_CLASS: Record<CockpitPrimaryAction['variant'], string> = {
  setup: 'setup-wizard__action',
  primary: 'cockpit__start',
  stop: 'cockpit__stop',
  resume: 'cockpit__resume',
  restart: 'cockpit__restart',
};

/** A contextual verb the current state invites, rendered as a bar button. */
interface CockpitPrimaryAction {
  key: string;
  label: string;
  /** Shown (with an ellipsis) while a dispatch is in flight. */
  busyLabel?: string;
  ariaLabel?: string;
  variant: 'setup' | 'primary' | 'stop' | 'resume' | 'restart';
  onClick(): void;
  busy?: boolean;
  disabled?: boolean;
  buttonRef?: RefObject<HTMLButtonElement | null>;
}

/** An action that lives in the overflow menu; disabled ones carry their reason. */
interface CockpitMenuAction {
  key: string;
  label: string;
  ariaLabel?: string;
  onClick(): void;
  enabled: boolean;
  reasons?: string[];
  variant?: 'default' | 'danger';
}

/**
 * The cockpit control bar. It surfaces only the status and the verbs the
 * current state actually invites; every other action — including verbs that
 * exist but are unavailable, which carry their reason inline — lives in the
 * overflow menu so the bar stays quiet and the stage keeps its height.
 */
function CockpitActionBar({
  status,
  statusOverride,
  primaryActions,
  menuActions,
  extraControls,
  isNarrow,
  inspectorButtonRef,
  onOpenInspector,
}: {
  status: FeatureSnapshot['status'];
  /** Replaces the raw status label (e.g. "Refactoring" while a pass runs). */
  statusOverride?: { label: string; tone: 'info' | 'attention' };
  primaryActions: CockpitPrimaryAction[];
  menuActions: CockpitMenuAction[];
  extraControls?: ReactNode;
  isNarrow: boolean;
  inspectorButtonRef: RefObject<HTMLButtonElement | null>;
  onOpenInspector(): void;
}) {
  const menuRef = useRef<HTMLDetailsElement>(null);
  const closeMenu = () => {
    if (menuRef.current !== null) menuRef.current.open = false;
  };

  // Native <details> stays open on outside interaction; close it on an outside
  // pointer or Escape so it never lingers over drawers opened elsewhere.
  useEffect(() => {
    const onPointerDown = (event: PointerEvent) => {
      const menu = menuRef.current;
      if (menu?.open === true && !menu.contains(event.target as Node)) menu.open = false;
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      const menu = menuRef.current;
      if (menu?.open === true) {
        menu.open = false;
        menu.querySelector<HTMLElement>('.cockpit__overflow-summary')?.focus();
      }
    };
    document.addEventListener('pointerdown', onPointerDown);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('pointerdown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, []);

  return (
    <div className="cockpit__actions" role="group" aria-label="Feature actions">
      <p className="cockpit__phase-status" role="status" aria-label="Current feature status">
        <code
          data-status={status}
          {...(statusOverride === undefined ? {} : { 'data-tone': statusOverride.tone })}
        >
          {statusOverride?.label ?? displayStatusLabel(status)}
        </code>
      </p>
      <span className="cockpit__actions-spacer" />
      {primaryActions.map((action) => (
        <button
          key={action.key}
          ref={action.buttonRef}
          type="button"
          className={PRIMARY_CLASS[action.variant]}
          disabled={action.disabled === true}
          onClick={action.onClick}
          {...(action.ariaLabel !== undefined ? { 'aria-label': action.ariaLabel } : {})}
        >
          {action.busy === true ? `${action.busyLabel ?? action.label}…` : action.label}
        </button>
      ))}
      {extraControls}
      {menuActions.length > 0 ? (
        <details ref={menuRef} className="cockpit__overflow">
          <summary className="cockpit__overflow-summary" aria-label="More actions">
            <span aria-hidden="true">⋯</span>
          </summary>
          <div className="cockpit__overflow-menu" role="menu">
            {menuActions.map((action) => (
              <div
                key={action.key}
                className="cockpit__overflow-item"
                data-variant={action.variant ?? 'default'}
              >
                <button
                  type="button"
                  role="menuitem"
                  disabled={!action.enabled}
                  onClick={() => {
                    closeMenu();
                    action.onClick();
                  }}
                  {...(action.ariaLabel !== undefined ? { 'aria-label': action.ariaLabel } : {})}
                >
                  {action.label}
                </button>
                {!action.enabled && action.reasons !== undefined && action.reasons.length > 0 ? (
                  <ul className="cockpit__overflow-reasons">
                    {action.reasons.map((reason) => (
                      <li key={reason}>{reason}</li>
                    ))}
                  </ul>
                ) : null}
              </div>
            ))}
          </div>
        </details>
      ) : null}
      {isNarrow ? (
        <button
          ref={inspectorButtonRef}
          type="button"
          className="cockpit__inspector-toggle"
          aria-controls="cockpit-inspector-drawer"
          onClick={onOpenInspector}
        >
          Inspector
        </button>
      ) : null}
    </div>
  );
}

function CompletionWrapUpMenu({
  verbs,
  onSelect,
}: {
  verbs: CompletionVerbModel[];
  onSelect: (verb: CompletionVerb) => void;
}) {
  const menuRef = useRef<HTMLDetailsElement>(null);
  useEffect(() => {
    const onPointerDown = (event: PointerEvent) => {
      const menu = menuRef.current;
      if (menu?.open === true && !menu.contains(event.target as Node)) menu.open = false;
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      const menu = menuRef.current;
      if (menu?.open === true) {
        menu.open = false;
        menu.querySelector<HTMLElement>('.cockpit__wrapup-summary')?.focus();
      }
    };
    document.addEventListener('pointerdown', onPointerDown);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('pointerdown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, []);
  return (
    <details ref={menuRef} className="cockpit__wrapup">
      <summary className="cockpit__wrapup-summary" aria-label="Wrap up">
        Wrap up <span aria-hidden="true">▾</span>
      </summary>
      <div className="cockpit__wrapup-menu" role="menu">
        {verbs.map((v) => (
          <div key={v.verb} className="cockpit__wrapup-item">
            <button
              type="button"
              role="menuitem"
              disabled={v.state === 'blocked'}
              onClick={() => {
                if (menuRef.current !== null) menuRef.current.open = false;
                onSelect(v.verb);
              }}
            >
              {v.label}
              <span className="cockpit__wrapup-state" aria-hidden="true">
                {v.state === 'done' ? '✓' : v.state === 'blocked' ? v.blocker : ''}
              </span>
            </button>
          </div>
        ))}
      </div>
    </details>
  );
}

function InspectorDrawer({
  snapshot,
  branch,
  stale,
  runMetrics,
  onOpenPullRequest,
  onClose,
}: {
  snapshot: FeatureSnapshot;
  branch: string | null;
  stale: boolean;
  runMetrics: RunMetrics | null;
  onOpenPullRequest(url: string): void;
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
          runMetrics={runMetrics}
          onOpenPullRequest={onOpenPullRequest}
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

function RestartConfirmDialog({
  snapshot,
  busy,
  onClose,
  onConfirm,
}: {
  snapshot: FeatureSnapshot;
  busy: boolean;
  onClose(): void;
  onConfirm(): void;
}) {
  const dialogRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    dialogRef.current?.focus();
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

  const repos = snapshot.repos.join(', ');
  const extendsIterationBudget = snapshot.failure?.type === 'max_iterations';

  return (
    <div className="impact-dialog__backdrop">
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="restart-dialog-title"
        className="impact-dialog"
        tabIndex={-1}
      >
        <span className="impact-dialog__eyebrow">Operational impact</span>
        <h3 id="restart-dialog-title">Restart {snapshot.name}?</h3>
        <p>
          This reruns the <strong>{snapshot.currentPhase}</strong> phase for this feature
          {repos.length > 0 ? ` across ${repos}` : ''}.
        </p>
        {extendsIterationBudget ? (
          <p className="impact-dialog__note">
            A maximum-iteration restart applies the established extra iteration budget increments
            for this phase.
          </p>
        ) : (
          <p className="impact-dialog__note">
            The current run is sealed and a fresh run begins from this phase.
          </p>
        )}
        <div className="impact-dialog__actions">
          <button type="button" onClick={onClose} disabled={busy} autoFocus>
            Cancel
          </button>
          <button type="button" className="cockpit__restart" onClick={onConfirm} disabled={busy}>
            {busy ? 'Restarting…' : 'Confirm restart'}
          </button>
        </div>
      </div>
    </div>
  );
}

function DeleteConfirmDialog({
  snapshot,
  action,
  busy,
  onClose,
  onConfirm,
}: {
  snapshot: FeatureSnapshot;
  action: FeatureActionView | undefined;
  busy: boolean;
  onClose(): void;
  onConfirm(): void;
}) {
  const dialogRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    dialogRef.current?.focus();
    const handleKeyDown = (event: globalThis.KeyboardEvent) => {
      if (event.key === 'Escape' && !busy) {
        event.preventDefault();
        onClose();
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [busy, onClose]);

  const preview = action?.impactPreview;
  const relationshipDelete =
    snapshot.parentId !== undefined ||
    snapshot.activeChild !== undefined ||
    (snapshot.childHistory?.length ?? 0) > 0;
  const projectionMissing = relationshipDelete && preview === undefined;

  return (
    <div className="impact-dialog__backdrop">
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="delete-dialog-title"
        className="impact-dialog"
        tabIndex={-1}
      >
        <span className="impact-dialog__eyebrow">Operational impact</span>
        <h3 id="delete-dialog-title">Delete {snapshot.name}?</h3>
        {projectionMissing ? (
          <p role="alert">Impact projection is missing or stale. Close this dialog and refresh.</p>
        ) : preview === undefined ? (
          <p>This removes the feature record and any remaining worktrees.</p>
        ) : (
          <ImpactPreviewList preview={preview} />
        )}
        <div className="impact-dialog__actions">
          <button type="button" onClick={onClose} disabled={busy} autoFocus>
            Keep feature
          </button>
          <button
            type="button"
            className="cockpit__delete-button"
            onClick={onConfirm}
            disabled={busy || projectionMissing}
          >
            {busy ? 'Deleting…' : 'Delete feature'}
          </button>
        </div>
      </div>
    </div>
  );
}

/**
 * Centered modal for a do-and-dismiss task (configuration, cycles): a real
 * scrim, a titled card, and a body that scrolls inside the card. Trays
 * (attention inbox, narrow inspector) never use this shell.
 */
function CockpitModal({
  title,
  ariaLabel,
  onClose,
  variant = 'default',
  children,
}: {
  title: string;
  ariaLabel: string;
  onClose(): void;
  variant?: 'default' | 'workspace';
  children: ReactNode;
}) {
  const modalRef = useRef<HTMLDivElement>(null);
  useModalDismiss(modalRef, onClose);

  return (
    <div className="cockpit__modal-overlay" onMouseDown={onClose}>
      <div
        ref={modalRef}
        className={`cockpit__modal ${variant === 'workspace' ? 'cockpit__modal--workspace' : ''}`}
        role="dialog"
        aria-modal="true"
        aria-label={ariaLabel}
        tabIndex={-1}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="cockpit__modal-header">
          <h3>{title}</h3>
          <button type="button" className="cockpit__modal-close" onClick={onClose}>
            Close
          </button>
        </header>
        <div
          className={`cockpit__modal-body ${
            variant === 'workspace' ? 'cockpit__modal-body--workspace' : ''
          }`}
        >
          {children}
        </div>
      </div>
    </div>
  );
}

export function FeatureCockpit({
  featureId,
  titleHint,
  onClose,
  onDeleted,
  onLoadedName,
  attentionItems,
  refreshAttention,
  attentionDrafts,
  setAttentionDrafts,
  attentionPreviewRequest = null,
  onAttentionPreviewClose,
  selectedRunNumber,
  onSelectRun,
}: FeatureCockpitProps) {
  const [state, setState] = useState<CockpitState>({ phase: 'loading' });
  const [stale, setStale] = useState(false);
  const [busy, setBusy] = useState(false);
  const [announcement, setAnnouncement] = useState('');
  const [actionError, setActionError] = useState<{
    action: 'Start' | 'Stop' | 'Resume' | 'Retry' | 'Restart' | 'Delete';
    error: WizardError;
  } | null>(null);
  const [stopDialog, setStopDialog] = useState(false);
  const [restartDialog, setRestartDialog] = useState(false);
  const [deleteDialog, setDeleteDialog] = useState(false);
  const [liveSessionCount, setLiveSessionCount] = useState(0);
  const [inspectorOpen, setInspectorOpen] = useState(false);
  const [attentionBusy, setAttentionBusy] = useState<string | null>(null);
  const [rewindDialog, setRewindDialog] = useState(false);
  const [rewindSourceRunNumber, setRewindSourceRunNumber] = useState<number | undefined>();
  const [cycleModal, setCycleModal] = useState<AftercareCycleId | null>(null);
  const [completionModal, setCompletionModal] = useState<CompletionVerb | null>(null);
  const [runRecordOpen, setRunRecordOpen] = useState(false);
  const [changesOpen, setChangesOpen] = useState(false);
  const [dismissedCycleId, setDismissedCycleId] = useState<string | undefined>();
  const [cycleReceipt, setCycleReceipt] = useState<CycleReceipt | undefined>();
  const [dismissedGateId, setDismissedGateId] = useState<string | undefined>();
  const [configOpen, setConfigOpen] = useState(false);
  const [runMetrics, setRunMetrics] = useState<RunMetrics | null>(null);
  const [aftercareRun, setAftercareRun] = useState<RunDetailView | null>(null);
  const [activeSurface, setActiveSurface] = useState<'document' | 'live' | 'changes'>('document');
  const [rewindLanding, setRewindLanding] = useState<{
    outcome: FeatureActionResult;
    run: RunDetailView | null;
  } | null>(null);
  const [currentRunBadges, setCurrentRunBadges] = useState({ changed: false, attention: false });
  const isNarrow = useMediaQuery('(max-width: 900px)');
  const actionInFlightRef = useRef(false);
  const loadRequestRef = useRef(0);
  const previousCycleRef = useRef<{ featureId: string; cycle?: CycleView } | null>(null);
  const stopButtonRef = useRef<HTMLButtonElement>(null);
  const inspectorButtonRef = useRef<HTMLButtonElement>(null);
  const onLoadedNameRef = useRef(onLoadedName);
  onLoadedNameRef.current = onLoadedName;
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  const onDeletedRef = useRef(onDeleted);
  onDeletedRef.current = onDeleted;

  useEffect(() => {
    if (attentionPreviewRequest === null || attentionPreviewRequest.attentionId !== undefined) {
      return;
    }
    setActiveSurface('document');
    onAttentionPreviewClose?.();
  }, [attentionPreviewRequest, onAttentionPreviewClose]);

  useEffect(() => {
    if (attentionPreviewRequest?.attentionId !== undefined) {
      setDismissedGateId(undefined);
    }
  }, [attentionPreviewRequest]);

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

  const completionEnabled =
    state.phase === 'loaded' &&
    (['publish', 'merge', 'mark-done', 'cleanup'] as const).some(
      (id) => actionById(state.snapshot, id)?.enabled === true,
    );
  const preflightCompletion = useCallback(
    (id: string) => window.agentico.preflightCompletion({ featureId: id }),
    [],
  );
  const completion = useCompletionPreflight(featureId, completionEnabled, preflightCompletion);
  const dispatchCompletion = useCallback(
    (id: string, action: CompletionAction, body?: Record<string, unknown>) =>
      window.agentico.dispatchFeatureAction({
        featureId: id,
        action,
        ...(body === undefined ? {} : { body }),
      } as FeatureActionRequest),
    [],
  );
  const onCompletionDispatched = useCallback(() => {
    void completion.refresh();
    void load({ silent: true });
  }, [completion, load]);

  // Fetch on mount; refetch on relevant invalidations; track stream health
  // so the view can show that it is refreshing after a reconnect.
  useEffect(() => {
    load();
    const unsubscribe = window.agentico.onAppEvent((event) => {
      if (event.type === 'status') {
        setStale(event.stream !== 'live');
        return;
      }
      if (event.relationshipDeleted === true && event.parentId === featureId) {
        if (onDeletedRef.current !== undefined) onDeletedRef.current(featureId);
        else onCloseRef.current();
        return;
      }
      const relevant =
        event.kind === 'resync' ||
        event.featureId === featureId ||
        event.resourceId === featureId ||
        event.parentId === featureId;
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
    if (state.phase !== 'loaded') return;
    const previous =
      previousCycleRef.current?.featureId === featureId
        ? previousCycleRef.current.cycle
        : undefined;
    const current = state.snapshot.cycle;
    if (ownsPostImplementationStage(previous) && !ownsPostImplementationStage(current)) {
      setCycleReceipt(receiptForCycleEnd(previous, state.snapshot));
    }
    previousCycleRef.current = {
      featureId,
      ...(current === undefined ? {} : { cycle: current }),
    };
  }, [featureId, state]);

  useEffect(() => {
    if (state.phase !== 'loaded' || !isRunAtRest(state.snapshot.status)) {
      setAftercareRun(null);
      return;
    }
    let current = true;
    setAftercareRun(null);
    setRunMetrics(null);
    void window.agentico
      .getRun({ featureId, runNumber: state.snapshot.activeRun })
      .then((run) => {
        if (!current) return;
        setAftercareRun(run);
        if (run.timing !== undefined && run.cost !== undefined) {
          setRunMetrics({
            totalSeconds: run.timing.totalSeconds,
            totalUsd: run.cost.totalUsd,
          });
        }
      })
      .catch(() => {
        if (current) {
          setAftercareRun(null);
          setRunMetrics(null);
        }
      });
    return () => {
      current = false;
    };
  }, [featureId, state]);

  useEffect(() => {
    if (selectedRunNumber === undefined || selectedRunNumber === null || selectedRunNumber <= 0) {
      return;
    }
    if (attentionItems.some((item) => item.kind !== 'recovery' && item.featureId === featureId)) {
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

  const dispatchLifecycleAction = useCallback(
    (
      request: FeatureActionRequest,
      pendingAnnouncement: string,
      acceptedAnnouncement: string,
      errorLabel: 'Start' | 'Resume' | 'Retry' | 'Restart',
    ) => {
      if (actionInFlightRef.current) return;
      actionInFlightRef.current = true;
      setBusy(true);
      setActionError(null);
      setAnnouncement(pendingAnnouncement);
      window.agentico
        .dispatchFeatureAction(request)
        .then(() => {
          setAnnouncement(acceptedAnnouncement);
          return load({ silent: true });
        })
        .catch((err: unknown) => {
          setActionError({ action: errorLabel, error: parseIpcError(err) });
          setAnnouncement('');
          return load({ silent: true });
        })
        .finally(() => {
          actionInFlightRef.current = false;
          setBusy(false);
        });
    },
    [load],
  );

  const start = useCallback(
    () =>
      dispatchLifecycleAction(
        { featureId, action: 'start' },
        'Starting from the current server snapshot…',
        'Start accepted. Refreshing authoritative run state…',
        'Start',
      ),
    [dispatchLifecycleAction, featureId],
  );

  const resume = useCallback(
    () =>
      dispatchLifecycleAction(
        { featureId, action: 'resume' },
        'Resuming from the paused gate…',
        'Resume accepted. Refreshing authoritative state…',
        'Resume',
      ),
    [dispatchLifecycleAction, featureId],
  );

  const retry = useCallback(
    () =>
      dispatchLifecycleAction(
        { featureId, action: 'retry' },
        'Retrying from the server snapshot…',
        'Retry accepted. Refreshing authoritative state…',
        'Retry',
      ),
    [dispatchLifecycleAction, featureId],
  );

  const restartExtendsIterationBudget =
    state.phase === 'loaded' && state.snapshot.failure?.type === 'max_iterations';
  const confirmRestart = useCallback(() => {
    setRestartDialog(false);
    const request: FeatureActionRequest = restartExtendsIterationBudget
      ? {
          featureId,
          action: 'restart',
          body: {
            max_iterations_delta: MAX_ITERATIONS_RESTART_DELTA,
            max_plan_iterations_delta: MAX_PLAN_ITERATIONS_RESTART_DELTA,
          },
        }
      : { featureId, action: 'restart' };
    dispatchLifecycleAction(
      request,
      'Restarting from the server snapshot…',
      'Restart accepted. Refreshing authoritative state…',
      'Restart',
    );
  }, [dispatchLifecycleAction, featureId, restartExtendsIterationBudget]);

  const restart = useCallback(() => {
    setRestartDialog(true);
  }, []);

  const confirmDelete = useCallback(() => {
    if (actionInFlightRef.current) return;
    actionInFlightRef.current = true;
    setBusy(true);
    setActionError(null);
    setAnnouncement('Deleting the feature and its worktrees…');
    window.agentico
      .deleteFeatureCascade({ featureId })
      .then((result) => {
        if (result.status === 'completed') {
          setDeleteDialog(false);
          if (onDeleted !== undefined) onDeleted(featureId);
          else onClose();
          return;
        }
        setAnnouncement(
          result.status === 'cleanup_pending'
            ? 'Deletion is draining cleanup; this workspace remains open.'
            : 'Deletion requires attention; review the server diagnostics and retry.',
        );
        return load({ silent: true });
      })
      .catch((err: unknown) => {
        setActionError({ action: 'Delete', error: parseIpcError(err) });
        setAnnouncement('');
        return load({ silent: true });
      })
      .finally(() => {
        actionInFlightRef.current = false;
        setBusy(false);
      });
  }, [featureId, load, onClose, onDeleted]);

  const closeStopDialog = useCallback(() => {
    setStopDialog(false);
    stopButtonRef.current?.focus();
  }, []);

  const saveAttentionDraft = useAttentionDraftSaves({
    notify: (result, options) => setAnnouncement(attentionActionNotice(result, options)),
    notifyError: (error) => setAnnouncement(attentionErrorMessage(error)),
    onAlreadyResolved: async () => {
      await refreshAttention();
      await load({ silent: true });
    },
  });

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
  const branch = featureBranch(snapshot);
  const ready = isReadyToStart(snapshot);
  const setupAction = actionById(snapshot, 'setup');
  const startAction = actionById(snapshot, 'start');
  const stopAction = actionById(snapshot, 'pause-stop');
  const resumeAction = actionById(snapshot, 'resume');
  const retryAction = actionById(snapshot, 'retry');
  const restartAction = actionById(snapshot, 'restart');
  const rewindAction = actionById(snapshot, 'rewind');
  const deleteAction = actionById(snapshot, 'delete');
  const rebaseAction = actionById(snapshot, 'rebase');
  const reviewCommentsAction = actionById(snapshot, 'review-comments');
  const refactorAction = actionById(snapshot, 'refactor');
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

  const visibleAttentionItems = attentionItems.filter((item) => item.kind !== 'review');
  const featureAttentionItems = visibleAttentionItems.filter(
    (item) => item.kind !== 'recovery' && item.featureId === featureId,
  );
  const routedAttentionItem =
    attentionPreviewRequest === null || attentionPreviewRequest.attentionId === undefined
      ? undefined
      : featureAttentionItems.find((item) => item.id === attentionPreviewRequest.attentionId);
  const activeAttentionItem =
    routedAttentionItem?.kind === 'gate'
      ? featureAttentionItems.find((item) => item.kind !== 'gate')
      : (routedAttentionItem ?? featureAttentionItems.find((item) => item.kind !== 'gate'));
  const pendingQuestion =
    activeAttentionItem?.kind === 'questions' ? activeAttentionItem.questions[0] : undefined;
  const suppressQuestion =
    pendingQuestion === undefined
      ? undefined
      : {
          prompt: pendingQuestion.key,
          optionLabels: pendingQuestion.options.map((option) => option.label),
        };
  const preferredGate =
    routedAttentionItem?.kind === 'gate'
      ? routedAttentionItem
      : featureAttentionItems.find((item): item is AttentionGate => item.kind === 'gate');
  const activeGate = preferredGate?.id === dismissedGateId ? undefined : preferredGate;

  const submitAttention = async (
    item: AttentionItem,
    action: Parameters<typeof runAttentionSubmit>[0],
    options?: Parameters<typeof runAttentionSubmit>[2],
  ) => {
    if (attentionBusy !== null) return;
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
    }
  };

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

  // The stage carries at most one surface at a time. At-rest runs land on
  // Aftercare and retain their transcript as Run record; active work keeps its
  // live surface. An attention preview always forces the live surface.
  const atRest = isRunAtRest(snapshot.status);
  const documentAvailable = hasPendingReview;
  const liveAvailable = showsRun(snapshot);
  const stageSurfaces: {
    id: 'document' | 'live' | 'changes';
    label: string;
  }[] = [];
  if (documentAvailable) stageSurfaces.push({ id: 'document', label: 'Document' });
  if (liveAvailable) {
    stageSurfaces.push({ id: 'live', label: atRest ? 'Run record' : 'Live activity' });
  }
  if (completionEnabled) stageSurfaces.push({ id: 'changes', label: 'Changes' });
  const surfaceIds = stageSurfaces.map((surface) => surface.id);
  const forcedLive =
    attentionPreviewRequest?.attentionId !== undefined &&
    routedAttentionItem?.kind !== 'gate' &&
    liveAvailable;
  const resolvedSurface: 'document' | 'live' | 'changes' | null = forcedLive
    ? 'live'
    : ready
      ? null
      : surfaceIds.includes(activeSurface)
        ? activeSurface
        : (surfaceIds[0] ?? null);

  const reasonsOf = (action: ReturnType<typeof actionById>): string[] =>
    action?.disabledReasons.map((reason) => displayFeatureMessage(reason.message)) ?? [];
  const primaryActions: CockpitPrimaryAction[] = [];
  if (setupAction?.enabled === true) {
    primaryActions.push({
      key: 'setup',
      label: snapshot.setup?.status === 'failed' ? 'Retry setup' : 'Run setup',
      busyLabel: 'Dispatching',
      variant: 'setup',
      onClick: retrySetup,
      busy,
      disabled: busy,
    });
  }
  if (startAction?.enabled === true) {
    primaryActions.push({
      key: 'start',
      label: 'Start',
      busyLabel: 'Starting',
      variant: 'primary',
      onClick: start,
      busy,
      disabled: busy,
    });
  }
  if (stopAction?.enabled === true) {
    primaryActions.push({
      key: 'stop',
      label: 'Stop',
      variant: 'stop',
      onClick: () => void openStopDialog(),
      disabled: busy,
      buttonRef: stopButtonRef,
    });
  }
  if (resumeAction?.enabled === true) {
    primaryActions.push({
      key: 'resume',
      label: 'Resume',
      busyLabel: 'Resuming',
      variant: 'resume',
      onClick: resume,
      busy,
      disabled: busy,
    });
  }
  if (retryAction?.enabled === true) {
    primaryActions.push({
      key: 'retry',
      label: 'Retry',
      busyLabel: 'Retrying',
      variant: 'resume',
      onClick: retry,
      busy,
      disabled: busy,
    });
  }
  const menuActions: CockpitMenuAction[] = [];
  if (startAction !== undefined && !startAction.enabled) {
    menuActions.push({
      key: 'start',
      label: 'Start',
      enabled: false,
      reasons: reasonsOf(startAction),
      onClick: () => {},
    });
  }
  if (stopAction !== undefined && !stopAction.enabled) {
    menuActions.push({
      key: 'stop',
      label: 'Stop',
      enabled: false,
      reasons: reasonsOf(stopAction),
      onClick: () => {},
    });
  }
  if (setupAction !== undefined && !setupAction.enabled) {
    menuActions.push({
      key: 'setup',
      label: snapshot.setup?.status === 'failed' ? 'Retry setup' : 'Run setup',
      enabled: false,
      reasons: reasonsOf(setupAction),
      onClick: () => {},
    });
  }
  if (restartAction?.enabled === true) {
    menuActions.push({ key: 'restart', label: 'Restart', enabled: true, onClick: restart });
  }
  menuActions.push({
    key: 'edit-config',
    label: 'Edit configuration…',
    enabled: true,
    onClick: () => setConfigOpen(true),
  });
  if (rebaseAction?.enabled === true) {
    menuActions.push({
      key: 'rebase',
      label: 'Rebase',
      enabled: true,
      onClick: () => setCycleModal('rebase'),
    });
  }
  if (reviewCommentsAction?.enabled === true) {
    menuActions.push({
      key: 'review-comments',
      label: 'Review comments',
      enabled: true,
      onClick: () => setCycleModal('review-comments'),
    });
  }
  if (refactorAction?.enabled === true) {
    menuActions.push({
      key: 'refactor',
      label: 'Refactor',
      enabled: true,
      onClick: () => setCycleModal('refactor'),
    });
  }
  if (rewindAction?.enabled === true) {
    menuActions.push({
      key: 'rewind',
      label: 'Rewind',
      ariaLabel: 'Rewind feature',
      enabled: true,
      onClick: () => {
        setRewindSourceRunNumber(snapshot.activeRun);
        setRewindDialog(true);
      },
    });
  }
  if (onSelectRun !== undefined) {
    menuActions.push({
      key: 'history',
      label: 'Run history',
      ariaLabel: 'View run history',
      enabled: true,
      onClick: openHistory,
    });
  }
  if (deleteAction !== undefined) {
    menuActions.push({
      key: 'delete',
      label: 'Delete',
      ariaLabel: 'Delete feature',
      enabled: deleteAction.enabled && !busy,
      reasons: reasonsOf(deleteAction),
      variant: 'danger',
      onClick: () => setDeleteDialog(true),
    });
  }

  const completionCandidates = new Set<CompletionVerb>(
    (['publish', 'merge', 'mark-done', 'cleanup'] as CompletionVerb[]).filter(
      (verb) => actionById(snapshot, verb) !== undefined,
    ),
  );
  const barVerbs =
    completion.preflight !== null
      ? completionBarModel(completion.preflight, completionCandidates)
      : [];
  const openCompletionModal = (verb: CompletionVerb): void => {
    setCompletionModal(null);
    void completion.refresh().then(() => setCompletionModal(verb));
  };
  const completionControls =
    completionEnabled && barVerbs.length > 0 ? (
      isNarrow ? (
        <CompletionWrapUpMenu verbs={barVerbs} onSelect={openCompletionModal} />
      ) : (
        <>
          {barVerbs.map((v) =>
            v.state === 'done' ? (
              <button
                key={v.verb}
                type="button"
                className="cockpit__completion-chip"
                onClick={() => openCompletionModal(v.verb)}
                aria-label={`${v.label} — reopen`}
              >
                {v.label} ✓
              </button>
            ) : (
              <button
                key={v.verb}
                type="button"
                className={
                  v.primary ? 'cockpit__completion-button' : 'cockpit__completion-secondary'
                }
                disabled={v.state === 'blocked'}
                title={v.state === 'blocked' ? v.blocker : undefined}
                onClick={() => openCompletionModal(v.verb)}
              >
                {v.label}
              </button>
            ),
          )}
        </>
      )
    ) : null;

  const postImplementationMode = resolvePostImplementationMode(snapshot, dismissedCycleId);
  const postMenuActions = menuActions.filter(
    (action) =>
      !['start', 'stop', 'setup', 'rebase', 'review-comments', 'refactor'].includes(action.key),
  );
  const openAftercareAction = (action: AftercareAction): void => {
    if (action.id === 'publish') {
      openCompletionModal('publish');
      return;
    }
    setCycleModal(action.id);
  };
  const standaloneAttention =
    activeAttentionItem === undefined ? null : (
      <section className="live-preview__attention" aria-label="Agent request">
        <AttentionDetail
          key={`${activeAttentionItem.kind}:${activeAttentionItem.id}`}
          item={activeAttentionItem}
          busy={attentionBusy === activeAttentionItem.id}
          drafts={attentionDrafts}
          setDrafts={setAttentionDrafts}
          saveDraft={(action, options) =>
            saveAttentionDraft(activeAttentionItem.id, action, options)
          }
          submit={(action, options) => void submitAttention(activeAttentionItem, action, options)}
        />
      </section>
    );

  if (!isArchiveMode && postImplementationMode.kind !== 'regular') {
    return (
      <section
        className="cockpit cockpit--post-implementation"
        aria-label={`Feature ${snapshot.name}`}
      >
        {postImplementationMode.kind === 'aftercare' ? (
          snapshot.activeChild !== undefined ? (
            <>
              <CockpitActionBar
                status={snapshot.status}
                statusOverride={refactoringStatusChip(snapshot.activeChild)}
                primaryActions={[]}
                menuActions={postMenuActions}
                extraControls={completionControls}
                isNarrow={isNarrow}
                inspectorButtonRef={inspectorButtonRef}
                onOpenInspector={() => setInspectorOpen(true)}
              />
              {standaloneAttention}
              <RefactorPassWorkspace
                parent={snapshot}
                onChanged={() => void load({ silent: true })}
                onEditPairedReview={() => setConfigOpen(true)}
                attentionItems={attentionItems}
                refreshAttention={refreshAttention}
                attentionDrafts={attentionDrafts}
                setAttentionDrafts={setAttentionDrafts}
              />
            </>
          ) : (
            <>
              <CockpitActionBar
                status={snapshot.status}
                primaryActions={[]}
                menuActions={postMenuActions}
                extraControls={completionControls}
                isNarrow={isNarrow}
                inspectorButtonRef={inspectorButtonRef}
                onOpenInspector={() => setInspectorOpen(true)}
              />
              {standaloneAttention}
              <AftercareWorkspace
                snapshot={snapshot}
                run={aftercareRun}
                receipt={cycleReceipt}
                onRetry={retry}
                onReopenCycle={() => setDismissedCycleId(undefined)}
                onAction={openAftercareAction}
                onOpenRunRecord={() => setRunRecordOpen(true)}
                onOpenChanges={() => setChangesOpen(true)}
                onOpenPullRequest={(url) => {
                  void window.agentico.openExternal({ url });
                }}
              />
            </>
          )
        ) : (
          <>
            {standaloneAttention}
            <CycleWorkspace
              snapshot={snapshot}
              run={aftercareRun}
              presentation={postImplementationMode.cycle}
              onRunMetrics={setRunMetrics}
              onStop={() => void openStopDialog()}
              onResume={resume}
              onRetry={retry}
              onReturnToAftercare={() => {
                setCycleReceipt(receiptForCycleEnd(snapshot.cycle, snapshot));
                setDismissedCycleId(cycleIdentity(snapshot) ?? undefined);
              }}
              onOpenConfig={() => setConfigOpen(true)}
              onOpenRunRecord={() => setRunRecordOpen(true)}
              onOpenPullRequest={(url) => {
                void window.agentico.openExternal({ url });
              }}
            />
          </>
        )}

        {actionError === null ? null : (
          <div role="alert" className="create-form__error">
            <span className="create-form__error-code">{actionError.error.code}</span>
            <p className="create-form__error-message">
              {actionError.action} was rejected — {actionError.error.message}
            </p>
          </div>
        )}
        <p className="cockpit__announcement" role="status" aria-live="polite">
          {announcement}
        </p>

        {configOpen ? (
          <CockpitModal
            title="Configuration"
            ariaLabel="Feature configuration"
            onClose={() => setConfigOpen(false)}
          >
            <FeatureConfigPanel featureId={featureId} />
          </CockpitModal>
        ) : null}

        {activeGate === undefined ? null : (
          <NeedUserInputModal
            item={activeGate}
            busy={attentionBusy === activeGate.id}
            drafts={attentionDrafts}
            setDrafts={setAttentionDrafts}
            onAnswerLater={() => {
              setDismissedGateId(activeGate.id);
              onAttentionPreviewClose?.();
            }}
            onResolved={async () => {
              setDismissedGateId(activeGate.id);
              await refreshAttention();
              await load({ silent: true });
              onAttentionPreviewClose?.();
            }}
          />
        )}

        {runRecordOpen ? (
          <CockpitModal
            title={`Run ${snapshot.activeRun} record`}
            ariaLabel="Run record"
            onClose={() => setRunRecordOpen(false)}
            variant="workspace"
          >
            <CurrentRunInspection
              featureId={featureId}
              runNumber={snapshot.activeRun}
              currentPhase={snapshot.currentPhase}
              featureStatus={snapshot.status}
              currentRoadmapPhase={snapshot.currentRoadmapPhase}
              totalRoadmapPhases={snapshot.totalRoadmapPhases}
              currentIteration={snapshot.currentIteration}
              phaseStatus={snapshot.phaseStatus}
              reviewGate={snapshot.reviewGate}
              verificationItems={snapshot.verificationItems}
              waitReason={snapshot.waitReason}
              shouldStream={false}
              presentation="record"
            />
          </CockpitModal>
        ) : null}

        {changesOpen ? (
          <CockpitModal
            title="Changes"
            ariaLabel="Feature changes"
            onClose={() => setChangesOpen(false)}
            variant="workspace"
          >
            <ChangesSurface
              featureId={featureId}
              preflight={completion.preflight}
              loading={completion.loading}
              error={completion.error}
              onRetry={() => void completion.refresh()}
              getRepositoryDiff={(id, repo, filePath) =>
                window.agentico.getRepositoryDiff({
                  featureId: id,
                  repo,
                  ...(filePath === undefined ? {} : { filePath }),
                })
              }
              openExternal={(url) => window.agentico.openExternal({ url })}
              revealPath={(id, repo) => window.agentico.revealPath({ featureId: id, repo })}
            />
          </CockpitModal>
        ) : null}

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
            reconcileSourceRunNumber={rewindSourceRunNumber}
            onClose={() => {
              setRewindDialog(false);
              setRewindSourceRunNumber(undefined);
            }}
            onRewindComplete={(result: FeatureActionResult) => {
              setRewindDialog(false);
              setRewindSourceRunNumber(undefined);
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

        {cycleModal === 'rebase' ? (
          <CockpitModal title="Rebase" ariaLabel="Rebase" onClose={() => setCycleModal(null)}>
            <RebaseModal
              featureId={featureId}
              snapshot={snapshot}
              onDispatched={() => load({ silent: true })}
              onCancel={() => setCycleModal(null)}
              attentionItems={attentionItems}
              onOpenGate={() => setCycleModal(null)}
            />
          </CockpitModal>
        ) : null}

        {cycleModal === 'review-comments' ? (
          <CockpitModal
            title="Review comments"
            ariaLabel="Review comments"
            onClose={() => setCycleModal(null)}
          >
            <ReviewCommentsModal
              featureId={featureId}
              snapshot={snapshot}
              onDispatched={() => load({ silent: true })}
              onCancel={() => setCycleModal(null)}
              attentionItems={attentionItems}
              onOpenGate={() => setCycleModal(null)}
            />
          </CockpitModal>
        ) : null}

        {cycleModal === 'refactor' ? (
          <CockpitModal
            title="Start refactor"
            ariaLabel="Start refactor"
            onClose={() => setCycleModal(null)}
          >
            <RefactorLauncher
              featureId={featureId}
              snapshot={snapshot}
              onDispatched={() => load({ silent: true })}
              onCancel={() => setCycleModal(null)}
              attentionItems={attentionItems}
              onOpenGate={() => setCycleModal(null)}
            />
          </CockpitModal>
        ) : null}

        {completionModal === 'publish' && completion.preflight !== null ? (
          <CockpitModal
            title="Publish"
            ariaLabel="Publish reviewed changes"
            onClose={() => setCompletionModal(null)}
          >
            <PublishModalBody
              featureId={featureId}
              preflight={completion.preflight}
              dispatchAction={dispatchCompletion}
              generatePublishDescription={(id, repos) =>
                window.agentico.generatePublishDescription({ featureId: id, repos })
              }
              openExternal={(url) => window.agentico.openExternal({ url })}
              onDispatched={onCompletionDispatched}
            />
          </CockpitModal>
        ) : null}

        {completionModal === 'merge' && completion.preflight !== null ? (
          <CockpitModal
            title="Merge"
            ariaLabel="Merge local repositories"
            onClose={() => setCompletionModal(null)}
          >
            <MergeModalBody
              featureId={featureId}
              preflight={completion.preflight}
              dispatchAction={dispatchCompletion}
              onDispatched={onCompletionDispatched}
              onHandoffToRebase={() => {
                setCompletionModal(null);
                setCycleModal('rebase');
              }}
            />
          </CockpitModal>
        ) : null}

        {completionModal === 'mark-done' && completion.preflight !== null ? (
          <CockpitModal
            title="Mark done"
            ariaLabel="Mark feature done"
            onClose={() => setCompletionModal(null)}
          >
            <MarkDoneModalBody
              featureId={featureId}
              preflight={completion.preflight}
              dispatchAction={dispatchCompletion}
              onDispatched={() => {
                onCompletionDispatched();
                setCompletionModal(null);
              }}
            />
          </CockpitModal>
        ) : null}

        {completionModal === 'cleanup' && completion.preflight !== null ? (
          <CleanupConfirm
            featureId={featureId}
            preflight={completion.preflight}
            dispatchAction={dispatchCompletion}
            onClose={() => setCompletionModal(null)}
            onDispatched={onCompletionDispatched}
          />
        ) : null}

        {deleteDialog ? (
          <DeleteConfirmDialog
            snapshot={snapshot}
            action={deleteAction}
            busy={busy}
            onClose={() => setDeleteDialog(false)}
            onConfirm={confirmDelete}
          />
        ) : null}
      </section>
    );
  }

  return (
    <section className="cockpit" aria-label={`Feature ${snapshot.name}`}>
      {isArchiveMode ? (
        <div className="cockpit__archive">
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
        </div>
      ) : (
        <>
          <CockpitActionBar
            status={snapshot.status}
            primaryActions={primaryActions}
            menuActions={menuActions}
            extraControls={completionControls}
            isNarrow={isNarrow}
            inspectorButtonRef={inspectorButtonRef}
            onOpenInspector={() => setInspectorOpen(true)}
          />

          {rewindLanding !== null ? (
            <RewindLanding
              outcome={rewindLanding.outcome}
              run={rewindLanding.run}
              onOpenSource={(runNumber) => onSelectRun?.(runNumber)}
              onDismiss={() => setRewindLanding(null)}
            />
          ) : null}

          {isNarrow && inspectorOpen ? (
            <InspectorDrawer
              snapshot={snapshot}
              branch={branch}
              stale={stale}
              runMetrics={runMetrics}
              onOpenPullRequest={(url) => {
                void window.agentico.openExternal({ url });
              }}
              onClose={closeInspector}
            />
          ) : null}

          <div className="cockpit__content">
            <main className="cockpit__stage">
              <>
                {resolvedSurface !== 'live' ? standaloneAttention : null}

                {stageSurfaces.length > 1 ? (
                  <div className="cockpit__stage-tabs" role="tablist" aria-label="Stage view">
                    {stageSurfaces.map((surface) => (
                      <button
                        key={surface.id}
                        type="button"
                        role="tab"
                        aria-selected={resolvedSurface === surface.id}
                        className="cockpit__stage-tab"
                        data-active={resolvedSurface === surface.id}
                        onClick={() => setActiveSurface(surface.id)}
                      >
                        {surface.label}
                        {surface.id === 'live' &&
                        resolvedSurface !== 'live' &&
                        stopAction !== undefined ? (
                          <span
                            className="cockpit__stage-tab-dot"
                            aria-label="Live activity in progress"
                          />
                        ) : null}
                      </button>
                    ))}
                  </div>
                ) : null}

                {resolvedSurface === 'document' ? (
                  <div className="cockpit__surface cockpit__surface--document">
                    <ReviewSurface
                      featureId={featureId}
                      onResolved={() => load({ silent: true })}
                    />
                  </div>
                ) : null}

                {resolvedSurface === 'live' ? (
                  <div className="cockpit__surface cockpit__surface--live">
                    <CurrentRunInspection
                      featureId={featureId}
                      runNumber={snapshot.activeRun}
                      currentPhase={snapshot.currentPhase}
                      featureStatus={snapshot.status}
                      currentRoadmapPhase={snapshot.currentRoadmapPhase}
                      totalRoadmapPhases={snapshot.totalRoadmapPhases}
                      currentIteration={snapshot.currentIteration}
                      phaseStatus={snapshot.phaseStatus}
                      reviewGate={snapshot.reviewGate}
                      verificationItems={snapshot.verificationItems}
                      waitReason={snapshot.waitReason}
                      shouldStream={stopAction !== undefined}
                      attentionRequestId={
                        attentionPreviewRequest?.attentionId === undefined ||
                        routedAttentionItem?.kind === 'gate'
                          ? undefined
                          : attentionPreviewRequest.requestId
                      }
                      onAttentionPreviewClose={onAttentionPreviewClose}
                      onRunMetrics={setRunMetrics}
                      suppressQuestion={suppressQuestion}
                      attentionFooter={
                        activeAttentionItem === undefined ? undefined : (
                          <AttentionDetail
                            key={`${activeAttentionItem.kind}:${activeAttentionItem.id}`}
                            item={activeAttentionItem}
                            busy={attentionBusy === activeAttentionItem.id}
                            drafts={attentionDrafts}
                            setDrafts={setAttentionDrafts}
                            saveDraft={(action, options) =>
                              saveAttentionDraft(activeAttentionItem.id, action, options)
                            }
                            submit={(action, options) =>
                              void submitAttention(activeAttentionItem, action, options)
                            }
                          />
                        )
                      }
                    />
                  </div>
                ) : null}

                {resolvedSurface === 'changes' ? (
                  <div className="cockpit__surface cockpit__surface--live">
                    <ChangesSurface
                      featureId={featureId}
                      preflight={completion.preflight}
                      loading={completion.loading}
                      error={completion.error}
                      onRetry={() => void completion.refresh()}
                      getRepositoryDiff={(id, repo, filePath) =>
                        window.agentico.getRepositoryDiff({
                          featureId: id,
                          repo,
                          ...(filePath === undefined ? {} : { filePath }),
                        })
                      }
                      openExternal={(url) => window.agentico.openExternal({ url })}
                      revealPath={(id, repo) => window.agentico.revealPath({ featureId: id, repo })}
                    />
                  </div>
                ) : null}

                {resolvedSurface === null && ready ? (
                  <div className="cockpit__empty-state" role="status">
                    <span aria-hidden="true">●</span> Ready to start
                    <p>Start runs the {snapshot.currentPhase} phase for this feature.</p>
                  </div>
                ) : null}

                <div className="cockpit__stage-status">
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
                </div>
              </>
            </main>
            {!isNarrow ? (
              <aside className="cockpit__inspector" aria-label="Feature inspector">
                <InspectorContent
                  snapshot={snapshot}
                  branch={branch}
                  stale={stale}
                  runMetrics={runMetrics}
                  onOpenPullRequest={(url) => {
                    void window.agentico.openExternal({ url });
                  }}
                />
              </aside>
            ) : null}
          </div>

          {configOpen ? (
            <CockpitModal
              title="Configuration"
              ariaLabel="Feature configuration"
              onClose={() => setConfigOpen(false)}
            >
              <FeatureConfigPanel featureId={featureId} />
            </CockpitModal>
          ) : null}

          {activeGate === undefined ? null : (
            <NeedUserInputModal
              item={activeGate}
              busy={attentionBusy === activeGate.id}
              drafts={attentionDrafts}
              setDrafts={setAttentionDrafts}
              onAnswerLater={() => {
                setDismissedGateId(activeGate.id);
                onAttentionPreviewClose?.();
              }}
              onResolved={async () => {
                setDismissedGateId(activeGate.id);
                await refreshAttention();
                await load({ silent: true });
                onAttentionPreviewClose?.();
              }}
            />
          )}

          {stopDialog ? (
            <StopConfirmDialog
              snapshot={snapshot}
              liveSessionCount={liveSessionCount}
              busy={busy}
              onClose={closeStopDialog}
              onConfirm={confirmStop}
            />
          ) : null}

          {restartDialog ? (
            <RestartConfirmDialog
              snapshot={snapshot}
              busy={busy}
              onClose={() => setRestartDialog(false)}
              onConfirm={confirmRestart}
            />
          ) : null}

          {deleteDialog ? (
            <DeleteConfirmDialog
              snapshot={snapshot}
              action={deleteAction}
              busy={busy}
              onClose={() => setDeleteDialog(false)}
              onConfirm={confirmDelete}
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
              reconcileSourceRunNumber={rewindSourceRunNumber}
              onClose={() => {
                setRewindDialog(false);
                setRewindSourceRunNumber(undefined);
              }}
              onRewindComplete={(result: FeatureActionResult) => {
                setRewindDialog(false);
                setRewindSourceRunNumber(undefined);
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

          {cycleModal === 'rebase' ? (
            <CockpitModal title="Rebase" ariaLabel="Rebase" onClose={() => setCycleModal(null)}>
              <RebaseModal
                featureId={featureId}
                snapshot={snapshot}
                onDispatched={() => load({ silent: true })}
                onCancel={() => setCycleModal(null)}
                attentionItems={attentionItems}
                onOpenGate={() => setCycleModal(null)}
              />
            </CockpitModal>
          ) : null}

          {cycleModal === 'review-comments' ? (
            <CockpitModal
              title="Review comments"
              ariaLabel="Review comments"
              onClose={() => setCycleModal(null)}
            >
              <ReviewCommentsModal
                featureId={featureId}
                snapshot={snapshot}
                onDispatched={() => load({ silent: true })}
                onCancel={() => setCycleModal(null)}
                attentionItems={attentionItems}
                onOpenGate={() => setCycleModal(null)}
              />
            </CockpitModal>
          ) : null}

          {cycleModal === 'refactor' ? (
            <CockpitModal title="Refactor" ariaLabel="Refactor" onClose={() => setCycleModal(null)}>
              <RefactorLauncher
                featureId={featureId}
                snapshot={snapshot}
                onDispatched={() => load({ silent: true })}
                onCancel={() => setCycleModal(null)}
                attentionItems={attentionItems}
                onOpenGate={() => setCycleModal(null)}
              />
            </CockpitModal>
          ) : null}

          {completionModal === 'publish' && completion.preflight !== null ? (
            <CockpitModal
              title="Publish"
              ariaLabel="Publish reviewed changes"
              onClose={() => setCompletionModal(null)}
            >
              <PublishModalBody
                featureId={featureId}
                preflight={completion.preflight}
                dispatchAction={dispatchCompletion}
                generatePublishDescription={(id, repos) =>
                  window.agentico.generatePublishDescription({ featureId: id, repos })
                }
                openExternal={(url) => window.agentico.openExternal({ url })}
                onDispatched={onCompletionDispatched}
              />
            </CockpitModal>
          ) : null}

          {completionModal === 'merge' && completion.preflight !== null ? (
            <CockpitModal
              title="Merge"
              ariaLabel="Merge local repositories"
              onClose={() => setCompletionModal(null)}
            >
              <MergeModalBody
                featureId={featureId}
                preflight={completion.preflight}
                dispatchAction={dispatchCompletion}
                onDispatched={onCompletionDispatched}
                onHandoffToRebase={() => {
                  setCompletionModal(null);
                  setCycleModal('rebase');
                }}
              />
            </CockpitModal>
          ) : null}

          {completionModal === 'mark-done' && completion.preflight !== null ? (
            <CockpitModal
              title="Mark done"
              ariaLabel="Mark feature done"
              onClose={() => setCompletionModal(null)}
            >
              <MarkDoneModalBody
                featureId={featureId}
                preflight={completion.preflight}
                dispatchAction={dispatchCompletion}
                onDispatched={() => {
                  onCompletionDispatched();
                  setCompletionModal(null);
                }}
              />
            </CockpitModal>
          ) : null}

          {completionModal === 'cleanup' && completion.preflight !== null ? (
            <CleanupConfirm
              featureId={featureId}
              preflight={completion.preflight}
              dispatchAction={dispatchCompletion}
              onClose={() => setCompletionModal(null)}
              onDispatched={onCompletionDispatched}
            />
          ) : null}
        </>
      )}
    </section>
  );
}
