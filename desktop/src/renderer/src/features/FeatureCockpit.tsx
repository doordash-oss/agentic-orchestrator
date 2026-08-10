/**
 * Feature cockpit: always reloads the authoritative feature snapshot from the
 * server, renders server-owned setup/run/action state, and resolves the
 * feature's blocking attention items through the same controls as the inbox.
 * A vanished feature renders a close affordance instead of crashing. No runtime
 * files are read here.
 */
import {
  Fragment,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type Dispatch,
  type ReactNode,
  type RefObject,
  type SetStateAction,
} from 'react';
import { createPortal } from 'react-dom';
import {
  attentionOwnerFeatureId,
  isPendingReviewStatus,
  isSyntheticHelpItem,
  type FeatureSnapshot,
  type RunDetailView,
  type RunSummaryView,
  type FeatureActionRequest,
  type FeatureActionView,
} from '../../../shared/ipc';
import { useDetailsDismiss } from '../components/useDetailsDismiss';
import { useModalDismiss } from '../components/useModalDismiss';
import { useMediaQuery } from '../hooks';
import { parseIpcError, type WizardError } from '../wizard/ipcError';
import { CurrentRunInspection, type RunMetrics } from './CurrentRunInspection';
import { classifyHold, railSegments, railTrio } from './phaseRail';
import { PhaseRail } from './PhaseRailRow';
import { ReviewSurface } from './ReviewSurface';
import { FeatureConfigPanel } from './ConfigEditor';
import { ArchiveMode } from './ArchiveMode';
import { RewindJourney } from './RewindJourney';
import { AftercareWorkspace } from './AftercareWorkspace';
import { AftercareFacts } from './AftercareFacts';
import { useAftercareEvidence } from './useAftercareEvidence';
import { InspectorContent } from './CockpitInspector';
import { InspectorDrawer } from './InspectorDrawer';
import { ImpactPreviewList } from './ImpactPreviewList';
import { NeedUserInputModal, type AttentionGate } from './NeedUserInputModal';
import {
  disabledReasonCopy,
  resolvePostImplementationMode,
  type AftercareAction,
  type AftercareModalId,
} from './postImplementationModel';
import { RefactorLauncher } from './refactor/RefactorLauncher';
import { ReviewFeedbackLauncher } from './reviewFeedback/ReviewFeedbackLauncher';
import { RefactorPassWorkspace, useRefactorPass } from './refactor/RefactorPassWorkspace';
import { refactoringStatusChip } from './refactor/refactorPassModel';
import { useCompletionPreflight } from './completion/useCompletionPreflight';
import { pendingDeliveryFact, pendingDeliverySummary } from './completion/pendingDelivery';
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
import { QuestionComposer, QuestionConversationTurn, questionAnswersRequest } from './QuestionTurn';
import { useAttentionDraftSaves } from './useAttentionDraftSaves';
import type { AttentionItem } from '../../../shared/ipc';
import {
  actionById,
  displayFeatureMessage,
  displayPhaseLabel,
  displayStatusLabel,
  featureBranch,
  isReadyToStart,
  isRunAtRest,
  showsRun,
} from './featureView';
import {
  createFeatureRefreshScheduler,
  type FeatureRefreshScheduler,
} from './featureRefreshScheduler';
import { registerFeatureCommandExecutor } from './featureCommands';
import type { FeatureCommandId } from '../../../shared/commands';

type CockpitState =
  | { phase: 'loading' }
  | { phase: 'missing' }
  | { phase: 'error'; error: WizardError }
  | { phase: 'loaded'; snapshot: FeatureSnapshot };

const FOCUSED_COMPLETION_SETTLE_MS = 500;

export interface FeatureCockpitProps {
  featureId: string;
  /** Whether this cockpit is the active workspace panel. */
  active?: boolean;
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
  /**
   * Toolbar-owned DOM node the cockpit's status chip, contextual primary
   * verbs, and completion controls portal into. When absent (e.g. this
   * cockpit rendered standalone in a test), they render inline exactly as
   * before.
   */
  actionsHost?: HTMLElement | null;
  /**
   * Toolbar-owned DOM node the cockpit's ⋯ overflow menu portals into. When
   * absent (e.g. this cockpit rendered standalone in a test), the menu
   * renders inline exactly as before.
   */
  overflowMenuHost?: HTMLElement | null;
  /**
   * Toolbar-owned DOM node the cockpit's wide-layout inspector-toggle button
   * portals into. Only mounted while `!isNarrow`; at narrow widths
   * nothing portals here and the cockpit's own in-content "Inspector" button
   * opens the drawer instead. When absent (e.g. standalone in a test), the
   * toggle renders inline exactly like the overflow menu does.
   */
  inspectorToggleHost?: HTMLElement | null;
  /**
   * Reports the cockpit-owned half of the shell's UI-state summary — the live
   * action catalogue behind every feature command's enablement, and the
   * deliberately-unpersisted inspector state behind the View menu's Show/Hide
   * label. Called on change only; the shell dedupes and pushes to the main
   * process from there.
   */
  onUiStateChange?(state: {
    featureId: string;
    actions: readonly FeatureActionView[] | null;
    inspectorOpen: boolean;
  }): void;
}

const MAX_ITERATIONS_RESTART_DELTA = 10;
const MAX_PLAN_ITERATIONS_RESTART_DELTA = 2;

function isArchiveRunSelected(runNumber: number | null | undefined): boolean {
  return runNumber !== undefined && runNumber !== null && runNumber > 0;
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
          <p className="cockpit__caption">Current fork</p>
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
 * The cockpit control bar. It dissolves entirely into the shell toolbar's
 * trailing zone: the status chip and the verbs the current state actually
 * invites portal into `actionsHost`, the overflow menu (every other action,
 * including verbs that exist but are unavailable, which carry their reason
 * inline) portals into `overflowMenuHost`, and the inspector toggle into
 * `inspectorToggleHost`. Nothing renders in the cockpit's own content flow —
 * the stage starts at the rail. Absent a host (e.g. standalone in a test),
 * each piece renders inline exactly as before.
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
  inspectorOpen,
  onToggleInspector,
  actionsHost,
  inspectorToggleHost,
  overflowMenuHost,
}: {
  status: FeatureSnapshot['status'];
  /** Replaces the raw status label (e.g. "Refactoring" while a pass runs). */
  statusOverride?: { label: string; tone: 'info' | 'attention' };
  primaryActions: CockpitPrimaryAction[];
  menuActions: CockpitMenuAction[];
  extraControls?: ReactNode;
  isNarrow: boolean;
  inspectorButtonRef: RefObject<HTMLButtonElement | null>;
  /** Narrow-only: opens the slide-over drawer (it has its own close affordances). */
  onOpenInspector(): void;
  /** Whether the wide-layout trailing split-view pane is currently open. */
  inspectorOpen: boolean;
  /** Wide-only: flips the trailing split-view pane open/closed. */
  onToggleInspector(): void;
  actionsHost?: HTMLElement | null;
  inspectorToggleHost?: HTMLElement | null;
  overflowMenuHost?: HTMLElement | null;
}) {
  const menuRef = useRef<HTMLDetailsElement>(null);
  const closeMenu = () => {
    if (menuRef.current !== null) menuRef.current.open = false;
  };

  useDetailsDismiss(menuRef, '.cockpit__overflow-summary');

  const actionsInner = (
    <>
      <p className="cockpit__phase-status" role="status" aria-label="Current feature status">
        <code
          data-status={status}
          {...(statusOverride === undefined ? {} : { 'data-tone': statusOverride.tone })}
        >
          {statusOverride?.label ?? displayStatusLabel(status)}
        </code>
      </p>
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
    </>
  );
  // The toolbar owns a slot this row portals into once mounted; a cockpit
  // rendered without that host (e.g. standalone in a test) keeps it inline,
  // nested in the same "Feature actions" group as the overflow menu and
  // inspector toggle below, matching pre-portal behavior exactly.
  const actionsNode = actionsHost != null ? createPortal(actionsInner, actionsHost) : actionsInner;

  const overflow =
    menuActions.length > 0 ? (
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
    ) : null;
  // The toolbar owns a slot the ⋯ menu portals into once mounted; a cockpit
  // rendered without that host (e.g. standalone in a test) keeps the menu
  // inline exactly as before.
  const overflowNode =
    overflow === null
      ? null
      : overflowMenuHost != null
        ? createPortal(overflow, overflowMenuHost)
        : overflow;

  const inspectorToggle = isNarrow ? (
    <button
      ref={inspectorButtonRef}
      type="button"
      className="cockpit__inspector-toggle"
      aria-controls="cockpit-inspector-drawer"
      onClick={onOpenInspector}
    >
      Inspector
    </button>
  ) : (
    (() => {
      // The trailing split-view pane's toggle lives in the toolbar
      // chrome, not the cockpit's own action bar — it portals into the
      // toolbar-owned slot the same way the ⋯ overflow menu does, and
      // renders inline when no host is mounted (e.g. a standalone test).
      const toggle = (
        <button
          ref={inspectorButtonRef}
          type="button"
          className="toolbar__inspector-toggle"
          aria-label="Toggle inspector"
          aria-pressed={inspectorOpen}
          onClick={onToggleInspector}
        >
          <svg
            aria-hidden="true"
            width="18"
            height="16"
            viewBox="0 0 18 16"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.5"
            strokeLinecap="round"
          >
            <rect x="1.75" y="2.75" width="14.5" height="10.5" rx="2.75" />
            <line x1="11.25" y1="3.25" x2="11.25" y2="12.75" />
          </svg>
        </button>
      );
      return inspectorToggleHost != null ? createPortal(toggle, inspectorToggleHost) : toggle;
    })()
  );

  return (
    <div className="toolbar__cockpit-actions" role="group" aria-label="Feature actions">
      {actionsNode}
      {overflowNode}
      {inspectorToggle}
    </div>
  );
}

const RUN_SWITCHER_PAGE_SIZE = 8;

/**
 * The sole run switcher: a popup on the stage bar's trailing side. Its label
 * follows the viewed run — live: phase + iteration, absent parts omitted;
 * sealed: `Run N · sealed`. Its menu lists the current run first, then
 * sealed runs newest-first, with a load-older affordance replacing the old
 * archive selector's paging.
 */
function RunSwitcherPopup({
  featureId,
  currentRunNumber,
  liveLabel,
  isArchiveMode,
  selectedRunNumber,
  onSelectRun,
}: {
  featureId: string;
  currentRunNumber: number;
  /** The live run's own label (phase + iteration), used for the button and menu. */
  liveLabel: string;
  isArchiveMode: boolean;
  selectedRunNumber: number | null;
  onSelectRun(runNumber: number | null): void;
}) {
  const menuRef = useRef<HTMLDetailsElement>(null);
  const [runs, setRuns] = useState<RunSummaryView[]>([]);
  const [page, setPage] = useState(1);
  const [totalPages, setTotalPages] = useState(1);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState<WizardError | null>(null);
  const loadRequestRef = useRef(0);

  const load = useCallback(
    (nextPage: number) => {
      const request = ++loadRequestRef.current;
      setLoading(true);
      setLoadError(null);
      window.agentico
        .listRuns({ featureId, page: nextPage, pageSize: RUN_SWITCHER_PAGE_SIZE })
        .then((result) => {
          if (request !== loadRequestRef.current) return;
          setRuns((current) => (nextPage === 1 ? result.runs : [...current, ...result.runs]));
          setPage(result.page);
          setTotalPages(result.totalPages);
        })
        .catch((err: unknown) => {
          if (request !== loadRequestRef.current) return;
          setLoadError(parseIpcError(err));
        })
        .finally(() => {
          if (request === loadRequestRef.current) setLoading(false);
        });
    },
    [featureId],
  );

  const closeMenu = () => {
    if (menuRef.current !== null) menuRef.current.open = false;
  };

  useDetailsDismiss(menuRef);

  const buttonLabel = isArchiveMode ? `Run ${selectedRunNumber} · sealed` : liveLabel;
  const sealedRuns = runs.filter((run) => run.runNumber !== currentRunNumber);

  return (
    <details
      ref={menuRef}
      className="cockpit__run-switcher"
      onToggle={(event) => {
        if ((event.target as HTMLDetailsElement).open) load(1);
      }}
    >
      <summary className="cockpit__run-switcher-summary">
        {buttonLabel} <span aria-hidden="true">▾</span>
      </summary>
      <div className="cockpit__run-switcher-menu" role="menu" aria-label="Switch run">
        <button
          type="button"
          role="menuitem"
          className="cockpit__run-switcher-item"
          aria-current={!isArchiveMode}
          onClick={() => {
            closeMenu();
            onSelectRun(null);
          }}
        >
          {liveLabel} · current
        </button>
        {sealedRuns.map((run) => (
          <button
            key={run.runNumber}
            type="button"
            role="menuitem"
            className="cockpit__run-switcher-item"
            aria-current={isArchiveMode && selectedRunNumber === run.runNumber}
            onClick={() => {
              closeMenu();
              onSelectRun(run.runNumber);
            }}
          >
            Run {run.runNumber} · sealed
          </button>
        ))}
        {page < totalPages ? (
          <button
            type="button"
            className="cockpit__run-switcher-more"
            disabled={loading}
            onClick={() => load(page + 1)}
          >
            {loading ? 'Loading…' : 'Load older'}
          </button>
        ) : null}
        {loadError !== null ? (
          <p className="cockpit__run-switcher-error" role="status">
            Could not load run history — {loadError.message}
          </p>
        ) : null}
      </div>
    </details>
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
  useDetailsDismiss(menuRef, '.cockpit__wrapup-summary');
  return (
    <details ref={menuRef} className="cockpit__wrapup">
      <summary className="cockpit__wrapup-summary" aria-label="Wrap up">
        Wrap up <span aria-hidden="true">▾</span>
      </summary>
      <div className="cockpit__wrapup-menu" role="menu" aria-label="Wrap up">
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
  active = true,
  actionsHost = null,
  overflowMenuHost = null,
  inspectorToggleHost = null,
  onUiStateChange,
}: FeatureCockpitProps) {
  const [state, setState] = useState<CockpitState>({ phase: 'loading' });
  const [liveExpandHost, setLiveExpandHost] = useState<HTMLDivElement | null>(null);
  // The Live surface has no frame of its own, so its view toggle and refresh
  // ride the stage bar alongside the expand control.
  const [liveControlsHost, setLiveControlsHost] = useState<HTMLDivElement | null>(null);
  const [streamStale, setStreamStale] = useState(false);
  const [refreshFailed, setRefreshFailed] = useState(false);
  const stale = streamStale || refreshFailed;
  const [busy, setBusy] = useState(false);
  const [announcement, setAnnouncement] = useState('');
  const [actionError, setActionError] = useState<{
    action: 'Start' | 'Stop' | 'Resume' | 'Retry' | 'Restart' | 'Delete' | 'Rebase';
    error: WizardError;
  } | null>(null);
  const [rebaseLaunchBusy, setRebaseLaunchBusy] = useState(false);
  const [stopDialog, setStopDialog] = useState(false);
  const [restartDialog, setRestartDialog] = useState(false);
  const [deleteDialog, setDeleteDialog] = useState(false);
  const [liveSessionCount, setLiveSessionCount] = useState(0);
  const [inspectorOpen, setInspectorOpen] = useState(false);
  const [attentionBusy, setAttentionBusy] = useState<string | null>(null);
  const [rewindDialog, setRewindDialog] = useState(false);
  const [rewindSourceRunNumber, setRewindSourceRunNumber] = useState<number | undefined>();
  const [launcherModal, setLauncherModal] = useState<AftercareModalId | null>(null);
  const [completionModal, setCompletionModal] = useState<CompletionVerb | null>(null);
  const [runRecordOpen, setRunRecordOpen] = useState(false);
  const [changesOpen, setChangesOpen] = useState(false);
  const [dismissedGateId, setDismissedGateId] = useState<string | undefined>();
  const [configOpen, setConfigOpen] = useState(false);
  const [runMetrics, setRunMetrics] = useState<RunMetrics | null>(null);
  const [aftercareRun, setAftercareRun] = useState<RunDetailView | null>(null);
  const [activeSurface, setActiveSurface] = useState<'live' | 'changes' | 'document' | 'files'>(
    'document',
  );
  const [rewindLanding, setRewindLanding] = useState<{
    outcome: FeatureActionResult;
    run: RunDetailView | null;
  } | null>(null);
  const [currentRunBadges, setCurrentRunBadges] = useState({ changed: false, attention: false });
  const [documentVisible, setDocumentVisible] = useState(
    () => document.visibilityState === 'visible',
  );
  const retainedEffectsActive = active && documentVisible;
  const isNarrow = useMediaQuery('(max-width: 900px)');
  const actionInFlightRef = useRef(false);
  const loadRequestRef = useRef(0);
  const schedulerRef = useRef<FeatureRefreshScheduler | null>(null);
  const completionRefreshRef = useRef<(() => Promise<void>) | null>(null);
  const completionSettleTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const selectedRunNumberRef = useRef(selectedRunNumber);
  selectedRunNumberRef.current = selectedRunNumber;
  const stopButtonRef = useRef<HTMLButtonElement>(null);
  const inspectorButtonRef = useRef<HTMLButtonElement>(null);
  const onLoadedNameRef = useRef(onLoadedName);
  onLoadedNameRef.current = onLoadedName;
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  const onDeletedRef = useRef(onDeleted);
  onDeletedRef.current = onDeleted;
  const onUiStateChangeRef = useRef(onUiStateChange);
  onUiStateChangeRef.current = onUiStateChange;

  /**
   * The funnel's view of this cockpit, refreshed every render (the flows it
   * calls are plain closures over the loaded snapshot, so they cannot be
   * hooks) and read at invocation time so a stale command can never dispatch.
   */
  const commandTargetRef = useRef<{
    actions: readonly FeatureActionView[] | null;
    run(id: FeatureCommandId): void;
  }>({ actions: null, run: () => {} });

  useEffect(
    () =>
      registerFeatureCommandExecutor({
        featureId,
        actions: () => commandTargetRef.current.actions,
        run: (id) => commandTargetRef.current.run(id),
        toggleInspector: () => setInspectorOpen((current) => !current),
      }),
    [featureId],
  );

  useEffect(() => {
    onUiStateChangeRef.current?.({
      featureId,
      actions: state.phase === 'loaded' ? state.snapshot.actions : null,
      inspectorOpen,
    });
  }, [featureId, inspectorOpen, state]);

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

  const runFeatureRefresh = useCallback(
    (options: { silent?: boolean } = {}) => {
      const request = ++loadRequestRef.current;
      if (options.silent !== true) {
        setRefreshFailed(false);
        setState({ phase: 'loading' });
      }
      const featureRefresh = window.agentico
        .getFeature(featureId)
        .then((snapshot) => {
          if (request !== loadRequestRef.current) return;
          setRefreshFailed(false);
          setState({ phase: 'loaded', snapshot });
          onLoadedNameRef.current(snapshot.name);
        })
        .catch((err: unknown) => {
          if (request !== loadRequestRef.current) return;
          const parsed = parseIpcError(err);
          if (parsed.code === 'not_found') {
            setRefreshFailed(false);
            setState({ phase: 'missing' });
          } else if (options.silent === true) {
            setRefreshFailed(true);
          } else {
            setState({ phase: 'error', error: parsed });
          }
        });
      const completionRefresh = completionRefreshRef.current?.() ?? Promise.resolve();
      return Promise.all([featureRefresh, completionRefresh]).then(() => undefined);
    },
    [featureId],
  );

  const refreshFeature = useCallback(
    (options: { silent?: boolean } = {}) =>
      schedulerRef.current?.refresh(options) ?? Promise.resolve(),
    [],
  );

  const cancelCompletionSettle = useCallback(() => {
    if (completionSettleTimerRef.current === null) return;
    clearTimeout(completionSettleTimerRef.current);
    completionSettleTimerRef.current = null;
  }, []);

  const scheduleCompletionSettle = useCallback(() => {
    if (!retainedEffectsActive || completionSettleTimerRef.current !== null) return;

    completionSettleTimerRef.current = setTimeout(() => {
      completionSettleTimerRef.current = null;
      void refreshFeature({ silent: true });
    }, FOCUSED_COMPLETION_SETTLE_MS);
  }, [refreshFeature, retainedEffectsActive]);

  useEffect(() => {
    if (!retainedEffectsActive) cancelCompletionSettle();
    return cancelCompletionSettle;
  }, [cancelCompletionSettle, retainedEffectsActive]);

  // The detail route is the only carrier of the preserved diff bodies and of
  // the closed passes a list projection caps away.
  const loadFullChildHistory = useCallback(
    () => window.agentico.getFeature(featureId).then((detail) => detail.childHistory ?? []),
    [featureId],
  );

  // Aftercare widens the gate deliberately: undelivered work has to stay
  // detectable in terminal states, where every completion verb can be disabled.
  const completionEnabled =
    state.phase === 'loaded' &&
    ((['publish', 'merge', 'mark-done', 'cleanup'] as const).some(
      (id) => actionById(state.snapshot, id)?.enabled === true,
    ) ||
      resolvePostImplementationMode(state.snapshot).kind === 'aftercare');
  const preflightCompletion = useCallback(
    (id: string) => window.agentico.preflightCompletion({ featureId: id }),
    [],
  );
  const completion = useCompletionPreflight(featureId, completionEnabled, preflightCompletion);
  completionRefreshRef.current = completionEnabled ? completion.refresh : null;
  const pendingDelivery = useMemo(
    () => pendingDeliverySummary(completion.preflight),
    [completion.preflight],
  );
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
    void refreshFeature({ silent: true });
  }, [refreshFeature]);

  const onPassChanged = useCallback(() => {
    void refreshFeature({ silent: true });
  }, [refreshFeature]);
  const refactorPass = useRefactorPass(
    state.phase === 'loaded' ? state.snapshot : null,
    onPassChanged,
    retainedEffectsActive,
  );

  /**
   * One-click rebase child launch, shared by the aftercare card and the cockpit
   * overflow entry. Zero-input, single-flight with the lifecycle actions: the
   * triggering control shows a busy state while the call is in flight, success
   * arms auto-start and silently reloads the parent so the cockpit's
   * activeChild-driven branch flips into the pass workspace, and typed
   * failures render inline through the persistent action-error alert near the
   * aftercare surface.
   */
  const launchRebase = useCallback(() => {
    if (actionInFlightRef.current || rebaseLaunchBusy) return;
    actionInFlightRef.current = true;
    setRebaseLaunchBusy(true);
    setActionError(null);
    setCompletionModal(null);
    setLauncherModal(null);
    setAnnouncement('Starting rebase pass…');
    window.agentico
      .launchRebaseChild({ featureId })
      .then((launch) => {
        refactorPass.armAutoStart(launch.childId);
        setAnnouncement('Rebase pass launched. Refreshing authoritative state…');
        void refreshFeature({ silent: true });
      })
      .catch((err: unknown) => {
        setActionError({ action: 'Rebase', error: parseIpcError(err) });
        setAnnouncement('');
        void refreshFeature({ silent: true });
      })
      .finally(() => {
        actionInFlightRef.current = false;
        setRebaseLaunchBusy(false);
      });
  }, [featureId, refreshFeature, refactorPass, rebaseLaunchBusy]);

  // Fetch on mount; refetch on relevant invalidations; track stream health
  // so the view can show that it is refreshing after a reconnect.
  useEffect(() => {
    const scheduler = createFeatureRefreshScheduler(runFeatureRefresh, {
      // Initial loading is independent of panel activity and window visibility.
      // The actual values are applied immediately after its first request starts.
      active: true,
      visible: true,
    });
    schedulerRef.current = scheduler;
    const onVisibilityChange = () => {
      const visible = document.visibilityState === 'visible';
      setDocumentVisible(visible);
      scheduler.setVisible(visible);
    };
    document.addEventListener('visibilitychange', onVisibilityChange);
    const unsubscribe = window.agentico.onAppEvent((event) => {
      if (event.type === 'status') {
        setStreamStale(event.stream !== 'live');
        return;
      }
      if (event.type !== 'invalidated') return;
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
        if (isArchiveRunSelected(selectedRunNumberRef.current)) {
          setCurrentRunBadges((badges) => ({
            ...badges,
            changed: true,
          }));
        }
        scheduler.invalidate();
      }
    });
    void scheduler.refresh();
    scheduler.setActive(active);
    onVisibilityChange();
    return () => {
      scheduler.dispose();
      schedulerRef.current = null;
      document.removeEventListener('visibilitychange', onVisibilityChange);
      loadRequestRef.current += 1;
      unsubscribe();
    };
  }, [featureId, runFeatureRefresh]);

  useEffect(() => {
    schedulerRef.current?.setActive(active);
  }, [active]);

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
    if (
      attentionItems.some(
        (item) =>
          item.kind !== 'recovery' && !isSyntheticHelpItem(item) && item.featureId === featureId,
      )
    ) {
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
        return refreshFeature({ silent: true });
      })
      .catch((err: unknown) => {
        const parsed = parseIpcError(err);
        setAnnouncement(`Retry failed — ${parsed.message}`);
        return refreshFeature({ silent: true });
      })
      .finally(() => {
        actionInFlightRef.current = false;
        setBusy(false);
      });
  }, [featureId, refreshFeature]);

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
          return refreshFeature({ silent: true });
        })
        .catch((err: unknown) => {
          setActionError({ action: errorLabel, error: parseIpcError(err) });
          setAnnouncement('');
          return refreshFeature({ silent: true });
        })
        .finally(() => {
          actionInFlightRef.current = false;
          setBusy(false);
        });
    },
    [refreshFeature],
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
        return refreshFeature({ silent: true });
      })
      .catch((err: unknown) => {
        setActionError({ action: 'Delete', error: parseIpcError(err) });
        setAnnouncement('');
        return refreshFeature({ silent: true });
      })
      .finally(() => {
        actionInFlightRef.current = false;
        setBusy(false);
      });
  }, [featureId, onClose, onDeleted, refreshFeature]);

  const closeStopDialog = useCallback(() => {
    setStopDialog(false);
    stopButtonRef.current?.focus();
  }, []);

  const saveAttentionDraft = useAttentionDraftSaves({
    notify: (result, options) => setAnnouncement(attentionActionNotice(result, options)),
    notifyError: (error) => setAnnouncement(attentionErrorMessage(error)),
    onAlreadyResolved: async () => {
      await refreshAttention();
      await refreshFeature({ silent: true });
    },
  });

  const closeInspector = useCallback(() => {
    setInspectorOpen(false);
    requestAnimationFrame(() => inspectorButtonRef.current?.focus());
  }, []);

  /** Wide layout only: flips the trailing split-view pane open/closed. */
  const toggleInspector = useCallback(() => {
    setInspectorOpen((current) => !current);
  }, []);

  useEffect(() => {
    if (!isNarrow) {
      setInspectorOpen(false);
    }
  }, [isNarrow]);

  // The aftercare receipt's on-demand facts. Gated on the surface actually
  // being aftercare so no other view pays for the fetches, and keyed on the
  // repository names inside the hook so the polling refresh cannot restart
  // them.
  const aftercareSurface =
    state.phase === 'loaded' &&
    !isArchiveRunSelected(selectedRunNumber) &&
    resolvePostImplementationMode(state.snapshot).kind === 'aftercare' &&
    state.snapshot.activeChild === undefined;
  const aftercareEvidence = useAftercareEvidence(
    featureId,
    state.phase === 'loaded' ? state.snapshot.repos : [],
    state.phase === 'loaded' &&
      (state.snapshot.repoStatus ?? []).some((repo) => repo.prUrl !== undefined),
    aftercareSurface,
  );

  if (state.phase !== 'loaded') {
    // No authoritative catalogue, no funnel target: every feature command
    // resolves to a no-op until the snapshot lands.
    commandTargetRef.current = { actions: null, run: () => {} };
  }

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
        <button type="button" className="setup-wizard__action" onClick={() => refreshFeature()}>
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
  const refactorAction = actionById(snapshot, 'refactor');
  const reviewFeedbackAction = actionById(snapshot, 'review-feedback');
  const hasPendingReview = isPendingReviewStatus(snapshot.status);
  const isArchiveMode = isArchiveRunSelected(selectedRunNumber);

  // The rail's hold looks at every open item this feature owns (including a
  // refactor pass's routed items), not the review-filtered list the question/
  // gate modals use — a paused *NeedsReview checkpoint still needs its review
  // item's `waitingSince` for the Paused Nm duration.
  const railOpenAttentionItems = attentionItems.filter(
    (item) => attentionOwnerFeatureId(item) === featureId,
  );
  const railHold = classifyHold(snapshot.status, railOpenAttentionItems);
  const railSegmentsList = railSegments(snapshot, railHold);
  const railTrioEntries = railTrio({
    totalSeconds: runMetrics?.totalSeconds,
    totalUsd: runMetrics?.totalUsd,
    contextPercentage: runMetrics?.contextPercentage,
    hold: railHold,
  });

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
        return refreshFeature({ silent: true });
      })
      .then(() => closeStopDialog())
      .catch((error: unknown) => {
        setActionError({ action: 'Stop', error: parseIpcError(error) });
        setStopDialog(false);
        return refreshFeature({ silent: true });
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
  const questionsAttention =
    activeAttentionItem?.kind === 'questions' ? activeAttentionItem : undefined;
  const pendingQuestion = questionsAttention?.questions[0];
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
          await refreshFeature({ silent: true });
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

  const submitQuestionAnswers = () => {
    if (questionsAttention === undefined) return;
    void submitAttention(questionsAttention, () =>
      window.agentico.answerQuestions(questionAnswersRequest(questionsAttention, attentionDrafts)),
    );
  };

  // The stage carries at most one surface at a time. The segmented control is
  // fixed — all four segments always render for a run-facing view — but only
  // one surface can actually exist at a time, so unavailable segments render
  // disabled rather than being hidden or reordered. An attention preview
  // always forces the live surface.
  const documentAvailable = hasPendingReview && !isArchiveMode;
  const liveAvailable = showsRun(snapshot) && !isArchiveMode;
  const filesAvailable = liveAvailable;
  const stageSurfaces: {
    id: 'live' | 'changes' | 'document' | 'files';
    label: string;
    available: boolean;
  }[] = [
    { id: 'live', label: 'Live', available: liveAvailable },
    { id: 'changes', label: 'Changes', available: completionEnabled && !isArchiveMode },
    { id: 'document', label: 'Review doc', available: documentAvailable },
    { id: 'files', label: 'Files', available: filesAvailable },
  ];
  const availableSurfaceIds = stageSurfaces
    .filter((surface) => surface.available)
    .map((surface) => surface.id);
  const forcedLive =
    attentionPreviewRequest?.attentionId !== undefined &&
    routedAttentionItem?.kind !== 'gate' &&
    liveAvailable;
  const resolvedSurface: 'live' | 'changes' | 'document' | 'files' | null = forcedLive
    ? 'live'
    : ready
      ? null
      : availableSurfaceIds.includes(activeSurface)
        ? activeSurface
        : (availableSurfaceIds[0] ?? null);

  // The run/iteration popup's label while live: phase + iteration, absent
  // parts omitted (e.g. "Implement #3").
  const liveLabel = [
    displayPhaseLabel(snapshot.currentPhase),
    snapshot.currentIteration !== undefined ? `#${snapshot.currentIteration}` : undefined,
  ]
    .filter((part): part is string => part !== undefined && part.trim() !== '')
    .join(' ');

  // One home for the switcher's wiring: the persistent stage bar and the
  // aftercare bar mount the same popup. Aftercare only renders outside archive
  // mode, so the shared archive state is correct at both sites.
  const runSwitcher =
    onSelectRun === undefined ? null : (
      <RunSwitcherPopup
        featureId={featureId}
        currentRunNumber={snapshot.activeRun}
        liveLabel={liveLabel}
        isArchiveMode={isArchiveMode}
        selectedRunNumber={selectedRunNumber ?? null}
        onSelectRun={onSelectRun}
      />
    );
  const showsLiveExpand = !isArchiveMode && resolvedSurface === 'live';

  // The stage bar row is persistent across live and sealed runs: the popup
  // is the sole run switcher, so it (and the segmented control, disabled
  // while a sealed run is shown) renders above whichever surface — live or
  // archive — is currently below it.
  const stageBarRow =
    !ready || isArchiveMode ? (
      <div className="cockpit__stage-bar">
        <div className="cockpit__segmented" role="tablist" aria-label="Stage view">
          {stageSurfaces.map((surface) => (
            <button
              key={surface.id}
              type="button"
              role="tab"
              aria-selected={resolvedSurface === surface.id}
              disabled={!surface.available}
              className="cockpit__segment"
              data-active={resolvedSurface === surface.id}
              onClick={() => setActiveSurface(surface.id)}
            >
              {surface.label}
              {surface.id === 'live' && resolvedSurface !== 'live' && stopAction !== undefined ? (
                <span className="cockpit__segment-dot" aria-label="Live activity in progress" />
              ) : null}
            </button>
          ))}
        </div>
        {runSwitcher !== null || showsLiveExpand ? (
          <div className="cockpit__stage-bar-trailing">
            {runSwitcher}
            {showsLiveExpand ? (
              <>
                <div className="cockpit__stage-bar-controls" ref={setLiveControlsHost} />
                <div className="cockpit__stage-bar-expand" ref={setLiveExpandHost} />
              </>
            ) : null}
          </div>
        ) : null}
      </div>
    ) : null;

  const reasonsOf = (action: ReturnType<typeof actionById>): string[] =>
    action?.disabledReasons.map((reason) => displayFeatureMessage(disabledReasonCopy(reason))) ??
    [];
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
      label: rebaseLaunchBusy ? 'Rebasing…' : 'Rebase',
      enabled: !rebaseLaunchBusy,
      onClick: () => launchRebase(),
    });
  }
  if (refactorAction?.enabled === true) {
    menuActions.push({
      key: 'refactor',
      label: 'Refactor',
      enabled: true,
      onClick: () => setLauncherModal('refactor'),
    });
  }
  if (reviewFeedbackAction?.enabled === true) {
    menuActions.push({
      key: 'review-feedback',
      label: 'Address review feedback',
      enabled: true,
      onClick: () => setLauncherModal('review-feedback'),
    });
  } else if (reviewFeedbackAction !== undefined) {
    menuActions.push({
      key: 'review-feedback',
      label: 'Address review feedback',
      enabled: false,
      reasons: reasonsOf(reviewFeedbackAction),
      onClick: () => {},
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

  /**
   * The funnel's dispatch table: every entry is the *same* callback the
   * cockpit's own control for that verb uses, so a palette or Feature-menu
   * invocation lands on the identical flow — immediate dispatch for the three
   * lifecycle verbs, confirmation dialogs for Stop/Restart/Delete, the
   * completion preflight modals for the four wrap-up verbs, and the existing
   * launchers and editors for the rest.
   */
  const featureCommandFlows: Record<FeatureCommandId, () => void> = {
    'feature.start': start,
    'feature.pause-stop': () => void openStopDialog(),
    'feature.resume': resume,
    'feature.retry': retry,
    'feature.restart': restart,
    'feature.rewind': () => {
      setRewindSourceRunNumber(snapshot.activeRun);
      setRewindDialog(true);
    },
    'feature.publish': () => openCompletionModal('publish'),
    'feature.merge': () => openCompletionModal('merge'),
    'feature.mark-done': () => openCompletionModal('mark-done'),
    'feature.cleanup': () => openCompletionModal('cleanup'),
    'feature.rebase': () => launchRebase(),
    'feature.refactor': () => setLauncherModal('refactor'),
    'feature.review-feedback': () => setLauncherModal('review-feedback'),
    'feature.configuration': () => setConfigOpen(true),
    'feature.delete': () => setDeleteDialog(true),
  };
  commandTargetRef.current = {
    actions: snapshot.actions,
    run: (id) => featureCommandFlows[id](),
  };
  const renderCompletionControls = (
    verbs: CompletionVerbModel[],
    options: { showBlocker?: boolean } = {},
  ): ReactNode =>
    verbs.length === 0 ? null : isNarrow ? (
      <CompletionWrapUpMenu verbs={verbs} onSelect={openCompletionModal} />
    ) : (
      <>
        {verbs.map((v) =>
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
            <Fragment key={v.verb}>
              <button
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
              {options.showBlocker === true && v.state === 'blocked' && v.blocker !== undefined ? (
                <span className="cockpit__completion-blocker" title={v.blocker}>
                  {v.blocker}
                </span>
              ) : null}
            </Fragment>
          ),
        )}
      </>
    );
  const completionControls = completionEnabled ? renderCompletionControls(barVerbs) : null;
  // Aftercare's toolbar owns wrap-up only: delivery (Publish/Merge) lives on
  // the runway, so the trailing zone reduces to Clean up plus a prominent
  // Mark done — the verb that actually closes the feature out.
  const aftercareVerbs = barVerbs
    .filter((v) => v.verb === 'cleanup' || v.verb === 'mark-done')
    .map((v) => ({ ...v, primary: v.verb === 'mark-done' }));
  const aftercareCompletionControls = completionEnabled
    ? renderCompletionControls(aftercareVerbs, { showBlocker: true })
    : null;

  const postImplementationMode = resolvePostImplementationMode(snapshot);
  const postMenuActions = menuActions.filter((action) => {
    // Passes live on the aftercare runway in both their enabled and blocked
    // forms; the overflow menu would only duplicate them.
    if (['start', 'stop', 'setup', 'rebase', 'refactor', 'review-feedback'].includes(action.key)) {
      return false;
    }
    return true;
  });
  const openAftercareAction = (action: AftercareAction): void => {
    if (action.id === 'publish' || action.id === 'publish-updates') {
      openCompletionModal('publish');
      return;
    }
    if (action.id === 'merge' || action.id === 'merge-updates') {
      openCompletionModal('merge');
      return;
    }
    if (action.id === 'rebase') {
      launchRebase();
      return;
    }
    setLauncherModal(action.id);
  };
  // One facts element for both aftercare inspector presentations: the trailing
  // pane when wide, the drawer when narrow.
  const aftercarePendingFact = pendingDeliveryFact(pendingDelivery);
  const aftercareFactsFor = (presentation: 'pane' | 'drawer') => (
    <AftercareFacts
      snapshot={snapshot}
      run={aftercareRun}
      {...(aftercarePendingFact === null ? {} : { pendingFact: aftercarePendingFact })}
      {...(presentation === 'pane' ? { title: 'Feature' } : {})}
      onOpenPullRequest={(url) => {
        void window.agentico.openExternal({ url });
      }}
    />
  );
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
                primaryActions={refactorPass.actions.map((action) => ({
                  key: `pass-${action.id}`,
                  label: action.label,
                  busyLabel: action.label,
                  variant:
                    action.id === 'pause-stop'
                      ? ('stop' as const)
                      : action.id === 'resume' || action.id === 'retry'
                        ? ('resume' as const)
                        : action.id === 'restart'
                          ? ('restart' as const)
                          : ('primary' as const),
                  onClick: () => void refactorPass.dispatch(action.id),
                  busy: refactorPass.busy,
                  disabled: refactorPass.busy,
                }))}
                menuActions={postMenuActions}
                extraControls={
                  refactorPass.discardAction === undefined ? null : (
                    <button
                      type="button"
                      className="cockpit__discard-pass"
                      disabled={!refactorPass.discardAction.enabled || refactorPass.busy}
                      title={
                        refactorPass.discardAction.enabled
                          ? undefined
                          : refactorPass.discardAction.disabledReasons
                              .map((reason) => reason.message)
                              .join(' ')
                      }
                      onClick={refactorPass.openDiscard}
                    >
                      Discard pass…
                    </button>
                  )
                }
                isNarrow={isNarrow}
                inspectorButtonRef={inspectorButtonRef}
                onOpenInspector={() => setInspectorOpen(true)}
                inspectorOpen={inspectorOpen}
                onToggleInspector={toggleInspector}
                inspectorToggleHost={inspectorToggleHost}
                overflowMenuHost={overflowMenuHost}
                actionsHost={actionsHost}
              />
              {standaloneAttention}
              <RefactorPassWorkspace
                parent={snapshot}
                pass={refactorPass}
                active={retainedEffectsActive}
                attentionPreviewRequest={attentionPreviewRequest}
                attentionItems={attentionItems}
                refreshAttention={refreshAttention}
                attentionDrafts={attentionDrafts}
                setAttentionDrafts={setAttentionDrafts}
                isNarrow={isNarrow}
                inspectorOpen={inspectorOpen}
                onCloseInspector={closeInspector}
              />
            </>
          ) : (
            <>
              <CockpitActionBar
                status={snapshot.status}
                primaryActions={[]}
                menuActions={postMenuActions}
                extraControls={aftercareCompletionControls}
                isNarrow={isNarrow}
                inspectorButtonRef={inspectorButtonRef}
                onOpenInspector={() => setInspectorOpen(true)}
                inspectorOpen={inspectorOpen}
                onToggleInspector={toggleInspector}
                inspectorToggleHost={inspectorToggleHost}
                overflowMenuHost={overflowMenuHost}
                actionsHost={actionsHost}
              />
              {runSwitcher === null ? null : (
                <div className="cockpit__stage-bar cockpit__stage-bar--aftercare">
                  <div className="cockpit__stage-bar-trailing">{runSwitcher}</div>
                </div>
              )}
              {standaloneAttention}
              {isNarrow && inspectorOpen ? (
                <InspectorDrawer onClose={closeInspector}>
                  {aftercareFactsFor('drawer')}
                </InspectorDrawer>
              ) : null}
              <div
                className={
                  !isNarrow && inspectorOpen
                    ? 'cockpit__content cockpit__content--inspector-open'
                    : 'cockpit__content'
                }
              >
                <AftercareWorkspace
                  snapshot={snapshot}
                  run={aftercareRun}
                  actionError={actionError}
                  pending={pendingDelivery}
                  preflight={completion.preflight}
                  evidence={aftercareEvidence}
                  busyAction={
                    rebaseLaunchBusy ? { id: 'rebase', label: 'Starting rebase pass' } : undefined
                  }
                  onLoadFullChildHistory={loadFullChildHistory}
                  onAction={openAftercareAction}
                  onOpenRunRecord={() => setRunRecordOpen(true)}
                  onOpenChanges={() => setChangesOpen(true)}
                  onOpenConfiguration={() => setConfigOpen(true)}
                  onOpenPullRequest={(url) => {
                    void window.agentico.openExternal({ url });
                  }}
                />
                {!isNarrow && inspectorOpen ? (
                  <aside className="cockpit__inspector" aria-label="Feature inspector">
                    {aftercareFactsFor('pane')}
                  </aside>
                ) : null}
              </div>
            </>
          )
        ) : null}

        {actionError === null || snapshot.activeChild === undefined ? null : (
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
            title={snapshot.activeChild !== undefined ? 'Paired configuration' : 'Configuration'}
            ariaLabel="Feature configuration"
            onClose={() => setConfigOpen(false)}
          >
            {snapshot.activeChild !== undefined ? (
              <p className="config-editor__paired-note">
                Review changes apply to both <b>{snapshot.name}</b> and{' '}
                <b>{snapshot.activeChild.name}</b>. Pipeline is preserved per record.
              </p>
            ) : null}
            <FeatureConfigPanel featureId={featureId} />
          </CockpitModal>
        ) : null}

        {activeGate === undefined ? null : (
          <NeedUserInputModal
            item={activeGate}
            busy={attentionBusy === activeGate.id}
            drafts={attentionDrafts}
            setDrafts={setAttentionDrafts}
            phase={snapshot.currentPhase}
            onAnswerLater={() => {
              setDismissedGateId(activeGate.id);
              onAttentionPreviewClose?.();
            }}
            onResolved={async () => {
              setDismissedGateId(activeGate.id);
              await refreshAttention();
              await refreshFeature({ silent: true });
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
              active={retainedEffectsActive}
              currentPhase={snapshot.currentPhase}
              currentRoadmapPhase={snapshot.currentRoadmapPhase}
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

        {restartDialog ? (
          <RestartConfirmDialog
            snapshot={snapshot}
            busy={busy}
            onClose={() => setRestartDialog(false)}
            onConfirm={confirmRestart}
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
              void refreshFeature({ silent: true });
            }}
          />
        ) : null}

        {launcherModal === 'refactor' ? (
          <CockpitModal
            title="Start refactor"
            ariaLabel="Start refactor"
            onClose={() => setLauncherModal(null)}
          >
            <RefactorLauncher
              featureId={featureId}
              snapshot={snapshot}
              onDispatched={(launch) => {
                if (launch.autoStart) refactorPass.armAutoStart(launch.childId);
                void refreshFeature({ silent: true });
              }}
              onCancel={() => setLauncherModal(null)}
            />
          </CockpitModal>
        ) : null}

        {launcherModal === 'review-feedback' ? (
          <CockpitModal
            title="Address review feedback"
            ariaLabel="Address review feedback"
            onClose={() => setLauncherModal(null)}
          >
            <ReviewFeedbackLauncher
              featureId={featureId}
              snapshot={snapshot}
              onDispatched={(launch) => {
                refactorPass.armAutoStart(launch.childId);
                void refreshFeature({ silent: true });
              }}
              onCancel={() => setLauncherModal(null)}
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
          {stageBarRow}
          <ArchiveMode
            featureId={featureId}
            selectedRunNumber={selectedRunNumber!}
            active={retainedEffectsActive}
            pipeline={snapshot.pipeline}
            currentRunBadges={currentRunBadges}
            onReturnToCurrent={() => {
              onSelectRun?.(null);
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
            inspectorOpen={inspectorOpen}
            onToggleInspector={toggleInspector}
            inspectorToggleHost={inspectorToggleHost}
            overflowMenuHost={overflowMenuHost}
            actionsHost={actionsHost}
          />

          <PhaseRail
            segments={railSegmentsList}
            trio={railTrioEntries}
            hold={railHold}
            tone={snapshot.status === 'Failed' ? 'error' : 'progress'}
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
            <InspectorDrawer onClose={closeInspector}>
              <InspectorContent
                snapshot={snapshot}
                branch={branch}
                stale={stale}
                runMetrics={runMetrics}
                onOpenPullRequest={(url) => {
                  void window.agentico.openExternal({ url });
                }}
              />
            </InspectorDrawer>
          ) : null}

          <div
            className={
              !isNarrow && inspectorOpen
                ? 'cockpit__content cockpit__content--inspector-open'
                : 'cockpit__content'
            }
          >
            <main className="cockpit__stage">
              <>
                {resolvedSurface !== 'live' ? standaloneAttention : null}

                {stageBarRow}

                {resolvedSurface === 'document' ? (
                  <div className="cockpit__surface cockpit__surface--document">
                    <ReviewSurface
                      featureId={featureId}
                      onResolved={() => refreshFeature({ silent: true })}
                    />
                  </div>
                ) : null}

                {resolvedSurface === 'live' ? (
                  <div className="cockpit__surface cockpit__surface--live">
                    <CurrentRunInspection
                      featureId={featureId}
                      runNumber={snapshot.activeRun}
                      active={retainedEffectsActive}
                      currentPhase={snapshot.currentPhase}
                      currentRoadmapPhase={snapshot.currentRoadmapPhase}
                      currentIteration={snapshot.currentIteration}
                      phaseStatus={snapshot.phaseStatus}
                      reviewGate={snapshot.reviewGate}
                      verificationItems={snapshot.verificationItems}
                      waitReason={snapshot.waitReason}
                      shouldStream={stopAction !== undefined}
                      expandHost={liveExpandHost}
                      controlsHost={liveControlsHost}
                      attentionRequestId={
                        attentionPreviewRequest?.attentionId === undefined ||
                        routedAttentionItem?.kind === 'gate'
                          ? undefined
                          : attentionPreviewRequest.requestId
                      }
                      onAttentionPreviewClose={onAttentionPreviewClose}
                      onRunMetrics={setRunMetrics}
                      onSessionSettled={scheduleCompletionSettle}
                      suppressQuestion={suppressQuestion}
                      attentionTurn={
                        questionsAttention === undefined ? undefined : (
                          <QuestionConversationTurn
                            key={`${questionsAttention.kind}:${questionsAttention.id}`}
                            item={questionsAttention}
                            busy={attentionBusy === questionsAttention.id}
                            drafts={attentionDrafts}
                            setDrafts={setAttentionDrafts}
                            onSubmit={submitQuestionAnswers}
                          />
                        )
                      }
                      attentionFooter={
                        activeAttentionItem === undefined ? undefined : questionsAttention !==
                          undefined ? (
                          <QuestionComposer
                            item={questionsAttention}
                            busy={attentionBusy === questionsAttention.id}
                            drafts={attentionDrafts}
                            setDrafts={setAttentionDrafts}
                            onSubmit={submitQuestionAnswers}
                          />
                        ) : (
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

                {resolvedSurface === 'files' ? (
                  <div className="cockpit__surface cockpit__surface--live">
                    <CurrentRunInspection
                      featureId={featureId}
                      runNumber={snapshot.activeRun}
                      active={retainedEffectsActive}
                      currentPhase={snapshot.currentPhase}
                      currentRoadmapPhase={snapshot.currentRoadmapPhase}
                      currentIteration={snapshot.currentIteration}
                      phaseStatus={snapshot.phaseStatus}
                      reviewGate={snapshot.reviewGate}
                      verificationItems={snapshot.verificationItems}
                      waitReason={snapshot.waitReason}
                      // The files surface renders no transcript, so it never
                      // subscribes to cohort output.
                      shouldStream={false}
                      mode="files"
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
            {!isNarrow && inspectorOpen ? (
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
              phase={snapshot.currentPhase}
              onAnswerLater={() => {
                setDismissedGateId(activeGate.id);
                onAttentionPreviewClose?.();
              }}
              onResolved={async () => {
                setDismissedGateId(activeGate.id);
                await refreshAttention();
                await refreshFeature({ silent: true });
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
                void refreshFeature({ silent: true });
              }}
            />
          ) : null}

          {launcherModal === 'refactor' ? (
            <CockpitModal
              title="Refactor"
              ariaLabel="Refactor"
              onClose={() => setLauncherModal(null)}
            >
              <RefactorLauncher
                featureId={featureId}
                snapshot={snapshot}
                onDispatched={(launch) => {
                  if (launch.autoStart) refactorPass.armAutoStart(launch.childId);
                  void refreshFeature({ silent: true });
                }}
                onCancel={() => setLauncherModal(null)}
              />
            </CockpitModal>
          ) : null}

          {launcherModal === 'review-feedback' ? (
            <CockpitModal
              title="Address review feedback"
              ariaLabel="Address review feedback"
              onClose={() => setLauncherModal(null)}
            >
              <ReviewFeedbackLauncher
                featureId={featureId}
                snapshot={snapshot}
                onDispatched={(launch) => {
                  refactorPass.armAutoStart(launch.childId);
                  void refreshFeature({ silent: true });
                }}
                onCancel={() => setLauncherModal(null)}
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
