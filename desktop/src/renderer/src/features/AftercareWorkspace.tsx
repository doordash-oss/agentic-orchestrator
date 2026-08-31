import type {
  CompletionPreflightResult,
  FeatureSnapshot,
  RelationshipChildView,
  RunDetailView,
} from '../../../shared/ipc';
import { formatDuration } from './featureView';
import { AftercareShipped } from './AftercareShipped';
import { AftercareSymbol } from './AftercareSymbol';
import { RefactorHistory } from './refactor/RefactorHistory';
import { aftercareActions, type AftercareAction } from './postImplementationModel';
import type { PendingDelivery } from './completion/pendingDelivery';
import { EMPTY_AFTERCARE_EVIDENCE, type AftercareEvidence } from './useAftercareEvidence';

export interface AftercareWorkspaceProps {
  snapshot: FeatureSnapshot;
  run: RunDetailView | null;
  pending?: PendingDelivery;
  /** Completion preflight, for the receipt's carried delivery facts. */
  preflight?: CompletionPreflightResult | null;
  evidence?: AftercareEvidence;
  actionError?: AftercareActionError | null;
  /** Action currently dispatching a one-click launch; its row renders busy. */
  busyAction?: { id: AftercareAction['id']; label: string };
  /** Fetches the complete pass history — bodies included — from the feature detail. */
  onLoadFullChildHistory?: () => Promise<readonly RelationshipChildView[]>;
  onAction(action: AftercareAction): void;
  onOpenRunRecord(): void;
  onOpenChanges(): void;
  onOpenConfiguration(): void;
  onOpenPullRequest(url: string): void;
}

interface AftercareActionError {
  action: string;
  error: {
    code: string;
    message: string;
  };
}

export function AftercareWorkspace({
  snapshot,
  run,
  pending = undefined,
  preflight = null,
  evidence = EMPTY_AFTERCARE_EVIDENCE,
  actionError = null,
  busyAction,
  onLoadFullChildHistory,
  onAction,
  onOpenRunRecord,
  onOpenChanges,
  onOpenConfiguration,
  onOpenPullRequest,
}: AftercareWorkspaceProps): React.ReactElement {
  const actions = aftercareActions(snapshot, pending);
  const elapsed = run?.timing?.totalSeconds ?? snapshot.timing?.totalSeconds;
  const copy = aftercareCopy(snapshot.status, snapshot.activeRun, elapsed);

  return (
    <section className="post-workspace aftercare-workspace" aria-label="Feature aftercare">
      <div className="aftercare-workspace__main">
        <header className="aftercare-workspace__header">
          <h2 className="aftercare-workspace__headline">{copy.headline}</h2>
          <p className="aftercare-workspace__subline">{copy.subline}</p>
        </header>

        <section className="aftercare-workspace__runway" aria-label="Follow-up actions">
          {actionError === null ? null : (
            <div role="alert" className="create-form__error aftercare-workspace__action-error">
              <span className="create-form__error-code">{actionError.error.code}</span>
              <p className="create-form__error-message">
                {aftercareActionErrorMessage(actionError)}
              </p>
            </div>
          )}
          {actions.length === 0 ? (
            <p className="aftercare-workspace__empty">No action is needed right now.</p>
          ) : (
            <div className="aftercare-workspace__group">
              <ul className="aftercare-workspace__rows">
                {actions.map((action) => {
                  const busy = busyAction?.id === action.id;
                  const blocked = action.disabledReason !== undefined;
                  return (
                    <li key={action.id} className="aftercare-workspace__row">
                      <button
                        type="button"
                        className="aftercare-workspace__row-hit"
                        disabled={busy || blocked}
                        aria-busy={busy || undefined}
                        data-blocked={blocked || undefined}
                        onClick={() => onAction(action)}
                      >
                        <AftercareSymbol id={action.id} />
                        <span className="aftercare-workspace__row-body">
                          <strong className="aftercare-workspace__row-title">{action.title}</strong>
                          <small className="aftercare-workspace__row-desc">
                            {action.description}
                          </small>
                        </span>
                        {blocked ? (
                          <span className="aftercare-workspace__action-blocked">
                            {action.disabledReason}
                          </span>
                        ) : (
                          <span className="aftercare-workspace__action-label">
                            {busy ? `${busyAction?.label ?? action.label}…` : action.label}
                          </span>
                        )}
                      </button>
                    </li>
                  );
                })}
              </ul>
            </div>
          )}
        </section>

        <AftercareShipped
          snapshot={snapshot}
          run={run}
          preflight={preflight}
          evidence={evidence}
          onOpenRunRecord={onOpenRunRecord}
          onOpenChanges={onOpenChanges}
          onOpenConfiguration={onOpenConfiguration}
          onOpenPullRequest={onOpenPullRequest}
        />

        <RefactorHistory
          entries={snapshot.childHistory ?? []}
          {...(snapshot.childHistoryTotal === undefined
            ? {}
            : { total: snapshot.childHistoryTotal })}
          {...(snapshot.childHistoryTruncated === undefined
            ? {}
            : { truncated: snapshot.childHistoryTruncated })}
          {...(onLoadFullChildHistory === undefined
            ? {}
            : { onLoadFullHistory: onLoadFullChildHistory })}
        />
      </div>
    </section>
  );
}

function aftercareActionErrorMessage(actionError: AftercareActionError): string {
  if (actionError.error.code === 'rebase_already_up_to_date') {
    return `Already up to date: ${actionError.error.message}`;
  }
  return `${actionError.action} was rejected — ${actionError.error.message}`;
}

/**
 * The reading-column voice: a headline stating the situation, then one plain
 * sub-line carrying the run number and elapsed time where the read model has
 * them, plus the guidance the deleted lede used to carry. Every per-status
 * distinction the old lede drew survives in one of the two lines.
 */
function aftercareCopy(
  status: string,
  runNumber: number,
  elapsedSeconds: number | undefined,
): { headline: string; subline: string } {
  const elapsed = elapsedSeconds === undefined ? null : formatDuration(elapsedSeconds);
  switch (status) {
    case 'CodeReady':
      return {
        headline: 'The work is ready to go out',
        subline: `${runClause(runNumber, elapsed, 'finished', 'has finished')} Pick one follow-up, or leave the run at rest.`,
      };
    case 'Done':
      return {
        headline: 'This feature is closed out',
        subline: `${runClause(runNumber, elapsed, 'closed after', 'is closed')} The record stays available whenever another focused pass is useful.`,
      };
    case 'Published':
      return {
        headline: 'The work is in service',
        subline: `${runClause(runNumber, elapsed, 'published', 'is published')} Pick one follow-up, or leave the feature at rest.`,
      };
    default:
      return {
        headline: 'The work is at rest',
        subline: `${runClause(runNumber, elapsed, 'finished', 'has finished')} Pick one follow-up, or leave the feature at rest.`,
      };
  }
}

function runClause(
  runNumber: number,
  elapsed: string | null,
  timedVerb: string,
  bareVerb: string,
): string {
  return elapsed === null
    ? `Run #${runNumber} ${bareVerb}.`
    : `Run #${runNumber} ${timedVerb} ${elapsed} of work.`;
}
