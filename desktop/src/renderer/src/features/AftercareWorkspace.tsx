import type { FeatureSnapshot, RunDetailView } from '../../../shared/ipc';
import { FeatureFactsRail } from './FeatureFactsRail';
import { RefactorHistory } from './refactor/RefactorHistory';
import { aftercareActions, type AftercareAction } from './postImplementationModel';
import { pendingDeliveryFact, type PendingDelivery } from './completion/pendingDelivery';

export interface AftercareWorkspaceProps {
  snapshot: FeatureSnapshot;
  run: RunDetailView | null;
  pending?: PendingDelivery;
  actionError?: AftercareActionError | null;
  /** Action currently dispatching a one-click launch; its card renders busy. */
  busyAction?: { id: AftercareAction['id']; label: string };
  onAction(action: AftercareAction): void;
  onOpenRunRecord(): void;
  onOpenChanges(): void;
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
  actionError = null,
  busyAction,
  onAction,
  onOpenRunRecord,
  onOpenChanges,
  onOpenPullRequest,
}: AftercareWorkspaceProps): React.ReactElement {
  const actions = aftercareActions(snapshot, pending);
  const pendingFact = pending === undefined ? null : pendingDeliveryFact(pending);
  const copy = aftercareCopy(snapshot.status);

  return (
    <section className="post-workspace aftercare-workspace" aria-label="Feature aftercare">
      <main className="aftercare-workspace__main">
        <header className="aftercare-workspace__header">
          <p className="post-workspace__eyebrow">Aftercare · Ready</p>
          <h2>{copy.heading}</h2>
          <p>{copy.description}</p>
        </header>

        <section className="aftercare-workspace__runway" aria-labelledby="aftercare-actions-title">
          <div className="aftercare-workspace__section-heading">
            <div>
              <p className="post-workspace__eyebrow">Available next steps</p>
              <h3 id="aftercare-actions-title">Choose one focused action</h3>
            </div>
            <span>One at a time</span>
          </div>
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
            <ol className="aftercare-workspace__actions">
              {actions.map((action, index) => {
                const busy = busyAction?.id === action.id;
                const blocked = action.disabledReason !== undefined;
                return (
                  <li key={action.id}>
                    <button
                      type="button"
                      disabled={busy || blocked}
                      aria-busy={busy || undefined}
                      data-blocked={blocked || undefined}
                      onClick={() => onAction(action)}
                    >
                      <span className="aftercare-workspace__action-index" aria-hidden="true">
                        {String(index + 1).padStart(2, '0')}
                      </span>
                      <span className="aftercare-workspace__action-copy">
                        <strong>{action.title}</strong>
                        <small>{action.description}</small>
                      </span>
                      <span className="aftercare-workspace__action-label">
                        {blocked ? (
                          action.disabledReason
                        ) : (
                          <>
                            {busy ? busyAction?.label : action.label}{' '}
                            <span aria-hidden="true">{busy ? '…' : '↗'}</span>
                          </>
                        )}
                      </span>
                    </button>
                  </li>
                );
              })}
            </ol>
          )}
        </section>

        <RefactorHistory entries={snapshot.childHistory ?? []} />

        <nav className="aftercare-workspace__archive" aria-label="Completed run resources">
          <button type="button" onClick={onOpenRunRecord}>
            Run record
          </button>
          <button type="button" onClick={onOpenChanges}>
            Changes
          </button>
        </nav>
      </main>
      <FeatureFactsRail
        snapshot={snapshot}
        run={run}
        {...(pendingFact === null ? {} : { pendingFact })}
        onOpenPullRequest={onOpenPullRequest}
      />
    </section>
  );
}

function aftercareActionErrorMessage(actionError: AftercareActionError): string {
  if (actionError.error.code === 'rebase_already_up_to_date') {
    return `Already up to date: ${actionError.error.message}`;
  }
  return `${actionError.action} was rejected — ${actionError.error.message}`;
}

function aftercareCopy(status: string): { heading: string; description: string } {
  switch (status) {
    case 'CodeReady':
      return {
        heading: 'Implementation complete.',
        description: 'Publish the feature, start a focused pass, or leave the run at rest.',
      };
    case 'Published':
      return {
        heading: 'Published. Choose what comes next.',
        description: 'Start a focused pass, or leave the feature at rest.',
      };
    case 'Done':
      return {
        heading: 'Work complete.',
        description: 'The record remains available whenever another focused pass is useful.',
      };
    default:
      return {
        heading: 'Ready for what comes next.',
        description: 'Choose one focused action, or leave the feature at rest.',
      };
  }
}
