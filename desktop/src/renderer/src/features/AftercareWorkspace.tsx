import type { FeatureSnapshot, RunDetailView } from '../../../shared/ipc';
import { FeatureFactsRail } from './FeatureFactsRail';
import { RefactorHistory } from './refactor/RefactorHistory';
import {
  aftercareActions,
  type AftercareAction,
  type CycleReceipt,
} from './postImplementationModel';

export interface AftercareWorkspaceProps {
  snapshot: FeatureSnapshot;
  run: RunDetailView | null;
  receipt?: CycleReceipt;
  onRetry(): void;
  onReopenCycle(): void;
  onAction(action: AftercareAction): void;
  onOpenRunRecord(): void;
  onOpenChanges(): void;
  onOpenPullRequest(url: string): void;
}

export function AftercareWorkspace({
  snapshot,
  run,
  receipt,
  onRetry,
  onReopenCycle,
  onAction,
  onOpenRunRecord,
  onOpenChanges,
  onOpenPullRequest,
}: AftercareWorkspaceProps): React.ReactElement {
  const actions = aftercareActions(snapshot);
  const copy = aftercareCopy(snapshot.status);

  return (
    <section className="post-workspace aftercare-workspace" aria-label="Feature aftercare">
      <main className="aftercare-workspace__main">
        <header className="aftercare-workspace__header">
          <p className="post-workspace__eyebrow">Aftercare · Ready</p>
          <h2>{copy.heading}</h2>
          <p>{copy.description}</p>
        </header>

        {receipt === undefined ? null : (
          <section
            className="aftercare-workspace__receipt"
            data-outcome={receipt.outcome}
            role={receipt.outcome === 'failed' ? 'alert' : 'status'}
          >
            <span aria-hidden="true">
              {receipt.outcome === 'failed' ? '✕' : receipt.outcome === 'stopped' ? '■' : '✓'}
            </span>
            <div>
              <strong>{receipt.message}</strong>
              {receipt.detail === undefined ? null : <small>{receipt.detail}</small>}
              {receipt.outcome === 'failed' ? (
                <div className="aftercare-workspace__receipt-actions">
                  <button
                    type="button"
                    disabled={
                      snapshot.actions.find((action) => action.id === 'retry')?.enabled !== true
                    }
                    onClick={onRetry}
                  >
                    Retry cycle
                  </button>
                  <button type="button" onClick={onReopenCycle}>
                    Reopen cycle
                  </button>
                </div>
              ) : (
                <small>The feature is back at rest.</small>
              )}
            </div>
          </section>
        )}

        <section className="aftercare-workspace__runway" aria-labelledby="aftercare-actions-title">
          <div className="aftercare-workspace__section-heading">
            <div>
              <p className="post-workspace__eyebrow">Available next steps</p>
              <h3 id="aftercare-actions-title">Choose one focused action</h3>
            </div>
            <span>One at a time</span>
          </div>
          {actions.length === 0 ? (
            <p className="aftercare-workspace__empty">No action is needed right now.</p>
          ) : (
            <ol className="aftercare-workspace__actions">
              {actions.map((action, index) => (
                <li key={action.id}>
                  <button type="button" onClick={() => onAction(action)}>
                    <span className="aftercare-workspace__action-index" aria-hidden="true">
                      {String(index + 1).padStart(2, '0')}
                    </span>
                    <span className="aftercare-workspace__action-copy">
                      <strong>{action.title}</strong>
                      <small>{action.description}</small>
                    </span>
                    <span className="aftercare-workspace__action-label">
                      {action.label} <span aria-hidden="true">↗</span>
                    </span>
                  </button>
                </li>
              ))}
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
      <FeatureFactsRail snapshot={snapshot} run={run} onOpenPullRequest={onOpenPullRequest} />
    </section>
  );
}

function aftercareCopy(status: string): { heading: string; description: string } {
  switch (status) {
    case 'CodeReady':
      return {
        heading: 'Implementation complete.',
        description: 'Publish the feature, start a maintenance cycle, or leave the run at rest.',
      };
    case 'Published':
      return {
        heading: 'Published. Choose what comes next.',
        description: 'Start a focused maintenance cycle, or leave the feature at rest.',
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
