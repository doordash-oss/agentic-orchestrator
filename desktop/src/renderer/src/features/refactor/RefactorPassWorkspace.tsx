import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type Dispatch,
  type SetStateAction,
} from 'react';
import {
  isPendingReviewStatus,
  type AttentionItem,
  type FeatureSnapshot,
  type RelationshipTransactionView,
} from '../../../../shared/ipc';
import { PhaseSpine } from '../../components/PhaseSpine';
import { parseIpcError } from '../../wizard/ipcError';
import {
  AttentionDetail,
  attentionActionNotice,
  attentionErrorMessage,
  runAttentionSubmit,
  type AttentionDrafts,
} from '../AttentionInbox';
import { useAttentionDraftSaves } from '../useAttentionDraftSaves';
import { NeedUserInputModal, type AttentionGate } from '../NeedUserInputModal';
import { CurrentRunInspection, type RunMetrics } from '../CurrentRunInspection';
import { ReviewSurface } from '../ReviewSurface';
import { ImpactPreviewList } from '../ImpactPreviewList';
import {
  displayStatusLabel,
  featureBranch,
  formatDuration,
  isRunAtRest,
  showsRun,
  spineActiveIndex,
  spineStages,
  spineTone,
} from '../featureView';
import { RefactorHistory } from './RefactorHistory';
import { custodyStations, passActions, passState, type PassAction } from './refactorPassModel';

export interface RefactorPassWorkspaceProps {
  parent: FeatureSnapshot;
  /** Reload the parent snapshot silently after a pass mutation. */
  onChanged(): void;
  /** Opens the paired review editor (the server applies it to both records). */
  onEditPairedReview(): void;
  attentionItems: AttentionItem[];
  refreshAttention(): Promise<AttentionItem[]>;
  attentionDrafts: AttentionDrafts;
  setAttentionDrafts: Dispatch<SetStateAction<AttentionDrafts>>;
}

type ChildState =
  | { phase: 'loading' }
  | { phase: 'error'; message: string }
  | { phase: 'loaded'; child: FeatureSnapshot };

/**
 * The parent workspace while a refactor pass (child feature) is active. The
 * pass is the thing that is running, so it owns the stage: custody strip on
 * top, the pass's own pipeline and live activity in the middle, the locked
 * parent reduced to a quiet card in the facts rail. Children never become
 * top-level tabs.
 */
export function RefactorPassWorkspace({
  parent,
  onChanged,
  onEditPairedReview,
  attentionItems,
  refreshAttention,
  attentionDrafts,
  setAttentionDrafts,
}: RefactorPassWorkspaceProps): React.ReactElement | null {
  const view = parent.activeChild;
  const [childState, setChildState] = useState<ChildState>({ phase: 'loading' });
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const [discardOpen, setDiscardOpen] = useState(false);
  const [attentionBusy, setAttentionBusy] = useState<string | null>(null);
  const [dismissedGateId, setDismissedGateId] = useState<string | undefined>();
  const [runMetrics, setRunMetrics] = useState<RunMetrics | null>(null);
  const loadRequestRef = useRef(0);

  const childId = view?.id;
  const loadChild = useCallback(() => {
    if (childId === undefined) return;
    const request = ++loadRequestRef.current;
    window.agentico
      .getFeature(childId)
      .then((snapshot) => {
        if (request !== loadRequestRef.current) return;
        setChildState({ phase: 'loaded', child: snapshot });
      })
      .catch((err: unknown) => {
        if (request !== loadRequestRef.current) return;
        setChildState({ phase: 'error', message: parseIpcError(err).message });
      });
  }, [childId]);

  useEffect(() => {
    setChildState({ phase: 'loading' });
    loadChild();
    return () => {
      loadRequestRef.current += 1;
    };
  }, [loadChild]);

  useEffect(
    () =>
      window.agentico.onAppEvent((event) => {
        if (event.type !== 'invalidated') return;
        if (
          event.kind === 'resync' ||
          event.parentId === parent.id ||
          event.childId === childId ||
          event.featureId === childId ||
          event.resourceId === childId
        ) {
          loadChild();
        }
      }),
    [loadChild, childId, parent.id],
  );

  const saveAttentionDraft = useAttentionDraftSaves({
    notify: (result, options) => setNotice(attentionActionNotice(result, options)),
    notifyError: (error) => setNotice(attentionErrorMessage(error)),
    onAlreadyResolved: async () => {
      await refreshAttention();
      loadChild();
    },
  });

  if (view === undefined) return null;
  const child = childState.phase === 'loaded' ? childState.child : null;
  const state = child === null ? null : passState(child);
  const stations = custodyStations(parent, child, view);
  const actions = child === null ? [] : passActions(child);
  const discardAction = child?.actions.find((action) => action.id === 'discard');
  const stopEnabled =
    child?.actions.some((action) => action.id === 'pause-stop' && action.enabled) === true;

  const passAttentionItems = attentionItems.filter(
    (item) => item.kind !== 'recovery' && item.kind !== 'review' && item.featureId === view.id,
  );
  const gate = passAttentionItems.find((item): item is AttentionGate => item.kind === 'gate');
  const activeGate = gate?.id === dismissedGateId ? undefined : gate;
  const inlineAttention = passAttentionItems.find((item) => item.kind !== 'gate');

  const refreshBoth = useCallback(() => {
    loadChild();
    onChanged();
  }, [loadChild, onChanged]);

  const dispatch = async (action: PassAction['id']) => {
    if (child === null || busy) return;
    setBusy(true);
    setNotice(null);
    try {
      await window.agentico.dispatchFeatureAction({ featureId: child.id, action });
      refreshBoth();
    } catch (err) {
      setNotice(parseIpcError(err).message);
    } finally {
      setBusy(false);
    }
  };

  const discard = async () => {
    if (child === null || discardAction?.impactPreview === undefined || busy) return;
    setBusy(true);
    setNotice(null);
    try {
      const result = await window.agentico.discardRefactorChild({ childId: child.id });
      setNotice(result.result);
      if (result.status === 'completed' || result.status === 'draining') setDiscardOpen(false);
      refreshBoth();
    } catch (err) {
      setNotice(parseIpcError(err).message);
    } finally {
      setBusy(false);
    }
  };

  const submitAttention = async (
    item: AttentionItem,
    action: Parameters<typeof runAttentionSubmit>[0],
    options?: Parameters<typeof runAttentionSubmit>[2],
  ) => {
    if (attentionBusy !== null) return;
    setAttentionBusy(item.id);
    try {
      const { notice: submitted } = await runAttentionSubmit(
        action,
        async () => {
          const latest = await refreshAttention();
          refreshBoth();
          return latest;
        },
        options,
      );
      setNotice(submitted);
    } catch (error) {
      setNotice(attentionErrorMessage(error));
    } finally {
      setAttentionBusy(null);
    }
  };

  const stages = child === null ? [] : spineStages(child.pipeline);

  return (
    <section
      className="post-workspace refactor-pass"
      aria-label="Refactor pass"
      data-state={state?.id ?? 'loading'}
    >
      <main className="refactor-pass__main">
        <ol className="refactor-pass__custody" aria-label="Custody of the work">
          {stations.map((station) => (
            <li
              key={station.id}
              className="refactor-pass__station"
              data-station={station.id}
              data-state={station.state}
              aria-current={station.state === 'live' ? 'step' : undefined}
            >
              <p className="refactor-pass__station-eyebrow">{station.eyebrow}</p>
              <strong className="refactor-pass__station-title">{station.title}</strong>
              <span className="refactor-pass__station-detail">
                <span className="refactor-pass__station-dot" aria-hidden="true" />
                {station.detail}
              </span>
            </li>
          ))}
        </ol>

        <header className="refactor-pass__header">
          <p className="post-workspace__eyebrow">
            Refactor pass
            {child === null ? null : <span> · Run #{child.activeRun}</span>}
          </p>
          <h2>{view.name}</h2>
          {childState.phase === 'loading' ? (
            <p className="refactor-pass__state" role="status">
              Loading the pass from the runtime…
            </p>
          ) : childState.phase === 'error' ? (
            <p className="refactor-pass__state" role="alert">
              The pass could not be loaded — {childState.message}{' '}
              <button type="button" className="refactor-pass__retry-load" onClick={loadChild}>
                Try again
              </button>
            </p>
          ) : (
            <p className="refactor-pass__state" role="status" data-tone={state?.tone}>
              {state?.sentence}
            </p>
          )}
          <div className="refactor-pass__actions">
            {actions.map((action) => (
              <button
                key={action.id}
                type="button"
                className={
                  action.kind === 'primary'
                    ? action.id === 'pause-stop'
                      ? 'cockpit__stop'
                      : 'cockpit__start'
                    : 'refactor-pass__secondary'
                }
                disabled={busy}
                onClick={() => void dispatch(action.id)}
              >
                {busy && action.kind === 'primary' ? `${action.label}…` : action.label}
              </button>
            ))}
            {discardAction !== undefined ? (
              <button
                type="button"
                className="refactor-pass__discard"
                disabled={!discardAction.enabled || busy}
                title={
                  discardAction.enabled
                    ? undefined
                    : discardAction.disabledReasons.map((reason) => reason.message).join(' ')
                }
                onClick={() => setDiscardOpen(true)}
              >
                Discard pass…
              </button>
            ) : null}
          </div>
        </header>

        {child !== null && stages.length > 0 ? (
          <PhaseSpine
            stages={stages}
            activeIndex={spineActiveIndex(child, stages)}
            tone={spineTone(child)}
            atRest={isRunAtRest(child.status)}
            label="Pass pipeline"
          />
        ) : null}

        {notice !== null ? (
          <p className="refactor-pass__notice" role="status">
            {notice}
          </p>
        ) : null}

        {view.attention.map((item) => (
          <p key={`${item.code}:${item.repo ?? ''}`} className="refactor-pass__alert" role="alert">
            {item.repo === undefined ? '' : `${item.repo}: `}
            {item.message}
          </p>
        ))}

        {inlineAttention !== undefined ? (
          <section className="live-preview__attention" aria-label="Agent request">
            <AttentionDetail
              key={`${inlineAttention.kind}:${inlineAttention.id}`}
              item={inlineAttention}
              busy={attentionBusy === inlineAttention.id}
              drafts={attentionDrafts}
              setDrafts={setAttentionDrafts}
              saveDraft={(action, options) =>
                saveAttentionDraft(inlineAttention.id, action, options)
              }
              submit={(action, options) => void submitAttention(inlineAttention, action, options)}
            />
          </section>
        ) : null}

        {child !== null && isPendingReviewStatus(child.status) ? (
          <div className="refactor-pass__surface">
            <ReviewSurface
              featureId={child.id}
              onResolved={async () => {
                refreshBoth();
              }}
            />
          </div>
        ) : child !== null && showsRun(child) ? (
          <div className="refactor-pass__surface">
            <CurrentRunInspection
              featureId={child.id}
              runNumber={child.activeRun}
              currentPhase={child.currentPhase}
              featureStatus={child.status}
              currentRoadmapPhase={child.currentRoadmapPhase}
              totalRoadmapPhases={child.totalRoadmapPhases}
              currentIteration={child.currentIteration}
              phaseStatus={child.phaseStatus}
              reviewGate={child.reviewGate}
              verificationItems={child.verificationItems}
              waitReason={child.waitReason}
              shouldStream={stopEnabled}
              onRunMetrics={setRunMetrics}
            />
          </div>
        ) : state?.id === 'ready' ? (
          <div className="cockpit__empty-state" role="status">
            <span aria-hidden="true">●</span> Ready to start
            <p>Start runs the {child?.currentPhase ?? 'first'} phase for this pass.</p>
          </div>
        ) : null}

        {child?.transaction !== undefined ? (
          <IntegrationPanel transaction={child.transaction} />
        ) : null}

        {view.cleanupWarnings.length > 0 ? (
          <ul className="refactor-pass__warnings">
            {view.cleanupWarnings.map((item) => (
              <li key={`${item.repo ?? ''}:${item.message}`}>{item.message}</li>
            ))}
          </ul>
        ) : null}

        <RefactorHistory entries={parent.childHistory ?? []} />
      </main>

      <aside className="feature-facts" aria-label="Pass facts">
        <p className="feature-facts__eyebrow">Pass facts</p>
        <dl className="feature-facts__list">
          <PassFact
            label="Status"
            value={child === null ? view.displayState : displayStatusLabel(child.status)}
          />
          <PassFact label="Pass" value={view.displayToken} mono />
          <PassFact label="Pipeline" value={view.pipeline} mono />
          {child === null ? null : (
            <PassFact
              label={child.repos.length === 1 ? 'Repository' : 'Repositories'}
              value={child.repos.join(', ')}
              mono
            />
          )}
          {child !== null && featureBranch(child) !== null ? (
            <PassFact label="Branch" value={featureBranch(child) ?? ''} mono />
          ) : null}
          {child === null ? null : <PassFact label="Run" value={`#${child.activeRun}`} mono />}
          <PassFact
            label="Elapsed"
            value={formatElapsedSeconds(runMetrics?.totalSeconds ?? child?.timing?.totalSeconds)}
            mono
          />
          <PassFact
            label="Cost"
            value={`$${(runMetrics?.totalUsd ?? view.cost.totalUsd).toFixed(2)}`}
            mono
          />
        </dl>
        <section className="refactor-pass__parent-card" aria-label="Parent feature">
          <p className="feature-facts__eyebrow">Parent</p>
          <strong>{parent.name}</strong>
          <p>
            Locked while the pass runs. Review settings are paired — changes apply to both records,
            and each keeps its own pipeline.
          </p>
          <button type="button" onClick={onEditPairedReview}>
            Edit paired review
          </button>
        </section>
      </aside>

      {activeGate !== undefined ? (
        <NeedUserInputModal
          item={activeGate}
          busy={attentionBusy === activeGate.id}
          drafts={attentionDrafts}
          setDrafts={setAttentionDrafts}
          onAnswerLater={() => setDismissedGateId(activeGate.id)}
          onResolved={async () => {
            setDismissedGateId(activeGate.id);
            await refreshAttention();
            refreshBoth();
          }}
        />
      ) : null}

      {discardOpen && discardAction !== undefined ? (
        <DiscardPassDialog
          passName={view.name}
          action={discardAction}
          busy={busy}
          onClose={() => setDiscardOpen(false)}
          onConfirm={() => void discard()}
        />
      ) : null}
    </section>
  );
}

function PassFact({
  label,
  value,
  mono = false,
}: {
  label: string;
  value: string;
  mono?: boolean;
}): React.ReactElement {
  return (
    <div className="feature-facts__fact">
      <dt>{label}</dt>
      <dd>{mono ? <code>{value}</code> : value}</dd>
    </div>
  );
}

function formatElapsedSeconds(totalSeconds: number | undefined): string {
  if (totalSeconds === undefined || totalSeconds <= 0) return '—';
  return formatDuration(totalSeconds);
}

function IntegrationPanel({
  transaction,
}: {
  transaction: RelationshipTransactionView;
}): React.ReactElement {
  const phase = transaction.phase ?? 'pending';
  return (
    <section className="refactor-pass__integration" aria-label="Integration" data-phase={phase}>
      <h3>
        Integration <span className="refactor-pass__integration-phase">{phase}</span>
      </h3>
      {transaction.attention !== undefined ? <p role="alert">{transaction.attention}</p> : null}
      {(transaction.entries ?? []).map((entry, index) => (
        <div key={entry.repo ?? index} className="refactor-pass__integration-repo">
          <code>{entry.repo ?? 'Repository'}</code>
          <span>
            {entry.prepState ?? 'pending'} → {entry.applyState ?? 'pending'}
          </span>
          {(entry.conflictFiles ?? []).length > 0 ? (
            <p>Conflicts: {entry.conflictFiles?.join(', ')}</p>
          ) : null}
          {entry.cleanupWarning !== undefined ? <p>{entry.cleanupWarning}</p> : null}
          {entry.diagnostics !== undefined ? <p>{entry.diagnostics}</p> : null}
        </div>
      ))}
    </section>
  );
}

function DiscardPassDialog({
  passName,
  action,
  busy,
  onClose,
  onConfirm,
}: {
  passName: string;
  action: NonNullable<FeatureSnapshot['actions'][number]>;
  busy: boolean;
  onClose(): void;
  onConfirm(): void;
}): React.ReactElement {
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

  const preview = action.impactPreview;
  return (
    <div className="impact-dialog__backdrop">
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby="discard-pass-title"
        className="impact-dialog"
        tabIndex={-1}
      >
        <span className="impact-dialog__eyebrow">Operational impact</span>
        <h3 id="discard-pass-title">Discard {passName}?</h3>
        {preview === undefined ? (
          <p role="alert">Impact projection is unavailable. Refresh before continuing.</p>
        ) : (
          <ImpactPreviewList preview={preview} />
        )}
        <p className="impact-dialog__note">
          This cannot be undone. The pass becomes immutable history.
        </p>
        <div className="impact-dialog__actions">
          <button type="button" onClick={onClose} disabled={busy} autoFocus>
            Keep the pass
          </button>
          <button
            type="button"
            className="cockpit__delete-button"
            disabled={preview === undefined || busy}
            onClick={onConfirm}
          >
            {busy ? 'Discarding…' : 'Discard pass'}
          </button>
        </div>
      </div>
    </div>
  );
}
