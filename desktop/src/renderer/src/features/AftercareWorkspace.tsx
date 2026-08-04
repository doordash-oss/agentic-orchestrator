import type { FeatureSnapshot, RunDetailView } from '../../../shared/ipc';
import { FeatureFactsRail } from './FeatureFactsRail';
import { RefactorHistory } from './refactor/RefactorHistory';
import { aftercareActions, type AftercareAction } from './postImplementationModel';

export interface AftercareWorkspaceProps {
  snapshot: FeatureSnapshot;
  run: RunDetailView | null;
  /** Action currently dispatching a one-click launch; its card renders busy. */
  busyAction?: { id: AftercareAction['id']; label: string };
  onAction(action: AftercareAction): void;
  onOpenRunRecord(): void;
  onOpenChanges(): void;
  onOpenPullRequest(url: string): void;
}

export function AftercareWorkspace({
  snapshot,
  run,
  busyAction,
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
              {actions.map((action, index) => {
                const busy = busyAction?.id === action.id;
                return (
                  <li key={action.id}>
                    <button
                      type="button"
                      disabled={busy}
                      aria-busy={busy || undefined}
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
                        {busy ? busyAction?.label : action.label}{' '}
                        <span aria-hidden="true">{busy ? '…' : '↗'}</span>
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
      <FeatureFactsRail snapshot={snapshot} run={run} onOpenPullRequest={onOpenPullRequest} />
    </section>
  );
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
