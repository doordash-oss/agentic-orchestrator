import { useCallback, useMemo } from 'react';
import type { CompletionPreflightResult } from '../../../../shared/ipc';
import {
  ResultBox,
  useCompletionAction,
  STATUS_LABELS,
  type CompletionAction,
} from './completionShared';

export interface MergeModalProps {
  featureId: string;
  preflight: CompletionPreflightResult;
  dispatchAction: (
    featureId: string,
    action: CompletionAction,
    body?: Record<string, unknown>,
  ) => Promise<{ result: string; [k: string]: unknown }>;
  onDispatched: () => void;
  onHandoffToRebase?: () => void;
}

export function MergeModalBody({
  featureId,
  preflight,
  dispatchAction,
  onDispatched,
  onHandoffToRebase,
}: MergeModalProps): React.ReactElement {
  const mergeAction = useCompletionAction();

  const localMergeRepos = useMemo(
    () => preflight.repos.filter((r) => !r.publishable && r.touched),
    [preflight],
  );

  const handleMerge = useCallback(async () => {
    await mergeAction.run(
      () =>
        dispatchAction(featureId, 'merge', {
          source_revision: preflight.sourceRevision,
        }).then((r) => r.result),
      async () => onDispatched(),
    );
  }, [featureId, preflight, dispatchAction, onDispatched, mergeAction]);

  return (
    <div className="completion-workspace__merge">
      <p className="completion-workspace__merge-hint">
        A successful merge across every repository marks the feature Done. A conflict offers the
        rebase journey for the affected repository.
      </p>
      {localMergeRepos.length > 0 ? (
        <>
          <div className="completion-workspace__merge-scope">
            {localMergeRepos.map((repo) => (
              <div key={repo.repo} className="completion-workspace__merge-repo">
                <div className="completion-workspace__merge-repo-header">
                  <span className="completion-workspace__merge-repo-name">{repo.repo}</span>
                  <span className="completion-workspace__repo-status" data-status={repo.status}>
                    {STATUS_LABELS[repo.status] ?? repo.status}
                  </span>
                </div>
                <dl className="completion-workspace__merge-repo-meta">
                  {repo.baseBranch !== undefined && (
                    <div className="completion-workspace__merge-meta-item">
                      <dt>Base</dt>
                      <dd>
                        <code>{repo.baseBranch}</code>
                      </dd>
                    </div>
                  )}
                  {repo.branch !== undefined && (
                    <div className="completion-workspace__merge-meta-item">
                      <dt>Feature</dt>
                      <dd>
                        <code>{repo.branch}</code>
                      </dd>
                    </div>
                  )}
                  {repo.freshness !== undefined && (
                    <div className="completion-workspace__merge-meta-item">
                      <dt>Freshness</dt>
                      <dd>{repo.freshness}</dd>
                    </div>
                  )}
                  {repo.blocker !== undefined && (
                    <div className="completion-workspace__merge-meta-item">
                      <dt>Blocker</dt>
                      <dd>{repo.blocker}</dd>
                    </div>
                  )}
                </dl>
              </div>
            ))}
          </div>
          <button
            type="button"
            className="completion-workspace__action"
            disabled={mergeAction.busy}
            onClick={() => void handleMerge()}
          >
            {mergeAction.busy ? 'Merging…' : 'Merge'}
          </button>
        </>
      ) : (
        <div className="completion-workspace__merge-empty">
          No local repositories to merge. All touched repositories are publishable.
        </div>
      )}
      <ResultBox result={mergeAction.result} />
      {mergeAction.result !== null && !mergeAction.result.ok && onHandoffToRebase !== undefined ? (
        <div className="completion-workspace__merge-handoff">
          <p className="completion-workspace__merge-handoff-hint">
            A conflict or behind-base outcome can be resolved through the rebase journey. Hand off,
            then return here to retry the merge.
          </p>
          <button
            type="button"
            className="completion-workspace__secondary-action completion-workspace__merge-handoff-action"
            onClick={() => onHandoffToRebase()}
          >
            Hand off to rebase
          </button>
        </div>
      ) : null}
    </div>
  );
}
