import { useCallback, useEffect, useState } from 'react';
import type { FeatureActionView, FeatureSnapshot } from '../../../../shared/ipc';
import { parseIpcError } from '../../wizard/ipcError';

interface RelationshipWorkspaceProps {
  parent: FeatureSnapshot;
  onChanged(): void;
}

function trackState(snapshot: FeatureSnapshot): string {
  const child = snapshot.activeChild;
  if (child === undefined) return snapshot.childHistory?.[0]?.outcome ?? 'idle';
  if (child.attention.length > 0 || child.integrationState === 'attention') return 'attention';
  if (child.relationshipState === 'setting_up') return 'setup';
  if (child.integrationState !== '' && child.integrationState !== 'pending') return 'integration';
  return 'active';
}

function ImpactProjection({ action }: { action: FeatureActionView }): React.ReactElement {
  const preview = action.impactPreview;
  if (preview === undefined) {
    return <p role="alert">Impact projection is unavailable. Refresh before continuing.</p>;
  }
  return (
    <div className="relationship-impact">
      {preview.categories.map((category) => (
        <section key={category.key}>
          <h4>{category.label}</h4>
          {category.items.length === 0 ? (
            <p>None</p>
          ) : (
            <ul>
              {category.items.map((item) => (
                <li key={item}>{item}</li>
              ))}
            </ul>
          )}
        </section>
      ))}
      <h4>Retained</h4>
      {preview.retained.length === 0 ? (
        <p>None</p>
      ) : (
        <ul>
          {preview.retained.map((item) => (
            <li key={item}>{item}</li>
          ))}
        </ul>
      )}
    </div>
  );
}

/** Relationship-first parent workspace. Children never become top-level tabs. */
export function RelationshipWorkspace({
  parent,
  onChanged,
}: RelationshipWorkspaceProps): React.ReactElement | null {
  const [inspection, setInspection] = useState<'parent' | 'child'>('child');
  const [child, setChild] = useState<FeatureSnapshot | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);

  const loadChild = useCallback(() => {
    if (parent.activeChild === undefined) {
      setChild(null);
      return;
    }
    window.agentico
      .getFeature(parent.activeChild.id)
      .then(setChild)
      .catch(() => setChild(null));
  }, [parent.activeChild]);

  useEffect(loadChild, [loadChild]);
  useEffect(
    () =>
      window.agentico.onAppEvent((event) => {
        if (event.type !== 'invalidated') return;
        if (
          event.kind === 'resync' ||
          event.parentId === parent.id ||
          event.childId === parent.activeChild?.id
        )
          loadChild();
      }),
    [loadChild, parent.activeChild?.id, parent.id],
  );

  if (parent.activeChild === undefined && (parent.childHistory?.length ?? 0) === 0) return null;
  const active = parent.activeChild;
  const discardAction = child?.actions.find((action) => action.id === 'discard');

  const dispatch = async (action: 'setup' | 'start' | 'pause-stop' | 'resume' | 'restart') => {
    if (child === null || busy) return;
    setBusy(true);
    setNotice(null);
    try {
      if (action === 'setup') await window.agentico.dispatchFeatureSetup(child.id);
      else await window.agentico.dispatchFeatureAction({ featureId: child.id, action });
      await Promise.all([loadChild(), Promise.resolve(onChanged())]);
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
      if (result.status === 'completed') setConfirmDiscard(false);
      await Promise.all([loadChild(), Promise.resolve(onChanged())]);
    } catch (err) {
      setNotice(parseIpcError(err).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <section
      className="relationship-workspace"
      aria-label="Refactor relationship"
      data-track-state={trackState(parent)}
    >
      <div className="relationship-track" aria-label="Relationship transfer track">
        <span>Parent · Published/locked</span>
        <span aria-hidden="true">━━●</span>
        <span>Child · Review</span>
        <span aria-hidden="true">━━○</span>
        <span>Integrate</span>
      </div>
      {active !== undefined ? (
        <>
          <div
            className="relationship-switcher"
            role="tablist"
            aria-label="Inspect relationship record"
          >
            <button
              type="button"
              role="tab"
              aria-selected={inspection === 'parent'}
              onClick={() => setInspection('parent')}
            >
              {parent.name}
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={inspection === 'child'}
              onClick={() => setInspection('child')}
            >
              {active.displayToken} · {active.name}
            </button>
          </div>
          {inspection === 'parent' ? (
            <div className="relationship-parent-lock">
              <strong>Parent locked</strong>
              <p>Child {active.name} must close before parent mutations resume.</p>
            </div>
          ) : (
            <div className="relationship-child-inspector">
              <header>
                <strong>{active.displayState}</strong>
                <span>{active.pipeline}</span>
                <span>${active.cost.totalUsd.toFixed(2)}</span>
              </header>
              {child?.setupComplete === false ? (
                <p>Setup is queued. Start becomes available only after setup completes.</p>
              ) : null}
              <div className="relationship-child-actions" aria-label="Child actions">
                {child?.actions
                  .filter((action) =>
                    ['setup', 'start', 'pause-stop', 'resume', 'restart'].includes(action.id),
                  )
                  .map((action) => (
                    <button
                      key={action.id}
                      type="button"
                      disabled={!action.enabled || busy}
                      title={action.disabledReasons.map((reason) => reason.message).join(' ')}
                      onClick={() =>
                        void dispatch(
                          action.id as 'setup' | 'start' | 'pause-stop' | 'resume' | 'restart',
                        )
                      }
                    >
                      {action.id === 'setup' && child.setup?.status === 'failed'
                        ? 'Retry setup'
                        : action.id === 'pause-stop'
                          ? 'Stop'
                          : action.id.charAt(0).toUpperCase() + action.id.slice(1)}
                    </button>
                  ))}
                {discardAction !== undefined ? (
                  <button
                    type="button"
                    disabled={!discardAction.enabled || busy}
                    onClick={() => setConfirmDiscard(true)}
                  >
                    Discard
                  </button>
                ) : null}
              </div>
              <h3>Paired review settings</h3>
              <p>
                {parent.name} ↔ {active.name}. The child pipeline remains independent.
              </p>
              {child?.transaction !== undefined ? (
                <section className="relationship-transaction">
                  <h3>Integration · {child.transaction.phase ?? 'pending'}</h3>
                  {child.transaction.attention !== undefined ? (
                    <p role="alert">{child.transaction.attention}</p>
                  ) : null}
                  {(child.transaction.entries ?? []).map((entry, index) => (
                    <div key={entry.repo ?? index}>
                      <strong>{entry.repo ?? 'Repository'}</strong>
                      <span>
                        {entry.prepState ?? 'pending'} → {entry.applyState ?? 'pending'}
                      </span>
                      {(entry.conflictFiles ?? []).length > 0 ? (
                        <p>Conflicts: {entry.conflictFiles?.join(', ')}</p>
                      ) : null}
                      {entry.cleanupWarning !== undefined ? <p>{entry.cleanupWarning}</p> : null}
                    </div>
                  ))}
                </section>
              ) : null}
              {active.attention.map((item) => (
                <p key={`${item.code}:${item.repo ?? ''}`} role="alert">
                  {item.repo === undefined ? '' : `${item.repo}: `}
                  {item.message}
                </p>
              ))}
              {active.cleanupWarnings.map((item) => (
                <p key={`${item.repo ?? ''}:${item.message}`}>{item.message}</p>
              ))}
            </div>
          )}
        </>
      ) : null}
      {notice !== null ? <p role="status">{notice}</p> : null}
      {confirmDiscard && discardAction !== undefined ? (
        <div
          role="dialog"
          aria-modal="true"
          aria-label="Discard refactor child"
          className="relationship-impact-dialog"
        >
          <h3>Discard {active?.name}</h3>
          <ImpactProjection action={discardAction} />
          <button type="button" onClick={() => setConfirmDiscard(false)}>
            Cancel
          </button>
          <button
            type="button"
            disabled={discardAction.impactPreview === undefined || busy}
            onClick={() => void discard()}
          >
            Discard child
          </button>
        </div>
      ) : null}
      {(parent.childHistory?.length ?? 0) > 0 ? (
        <details className="relationship-history">
          <summary>Refactor history</summary>
          <ol>
            {parent.childHistory?.map((entry) => (
              <li key={entry.id}>
                <button type="button" onClick={() => setInspection('child')}>
                  {entry.displayState}
                </button>
                <span>{new Date(entry.closedAt ?? entry.startedAt).toLocaleDateString()}</span>
                <span>{entry.pipeline}</span>
                <span>${entry.cost.totalUsd.toFixed(2)}</span>
                {entry.diffSummary !== undefined ? <pre>{entry.diffSummary}</pre> : null}
              </li>
            ))}
          </ol>
        </details>
      ) : null}
    </section>
  );
}
