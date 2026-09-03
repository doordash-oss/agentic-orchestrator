/*
Copyright 2026 DoorDash, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

import { useCallback, useMemo } from 'react';
import type { CompletionPreflightResult } from '../../../../shared/ipc';
import {
  ResultBox,
  useCompletionAction,
  STATUS_LABELS,
  type CompletionAction,
} from './completionShared';
import { UNMERGED_CHANGES } from './pendingDelivery';

/**
 * The rebase handoff rides as the failure card's remediation hint: a conflict
 * or behind-base merge is resolved by a rebase pass, not by retrying in place.
 */
const REBASE_HANDOFF_HINT =
  "A conflict or behind-base outcome can be resolved with a rebase pass. Use Start rebase pass in the feature's aftercare workspace, then return here to retry the merge.";

export interface MergeModalProps {
  featureId: string;
  preflight: CompletionPreflightResult;
  dispatchAction: (
    featureId: string,
    action: CompletionAction,
    body?: Record<string, unknown>,
  ) => Promise<{ result: string; [k: string]: unknown }>;
  onDispatched: () => void;
}

export function MergeModalBody({
  featureId,
  preflight,
  dispatchAction,
  onDispatched,
}: MergeModalProps): React.ReactElement {
  const mergeAction = useCompletionAction();

  const localMergeRepos = useMemo(
    () => preflight.repos.filter((r) => !r.publishable && r.touched),
    [preflight],
  );

  const hasUnmerged = useMemo(
    () => preflight.repos.some((r) => r.status === UNMERGED_CHANGES),
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
        A successful merge across every repository marks the feature Done. A conflict can be
        resolved with a rebase pass.
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
                  {repo.pendingCommits !== undefined && repo.pendingCommits > 0 && (
                    <div className="completion-workspace__merge-meta-item">
                      <dt>Unmerged</dt>
                      <dd>
                        {`${repo.pendingCommits} commit${repo.pendingCommits === 1 ? '' : 's'} not in ${
                          repo.baseBranch ?? 'the base branch'
                        }`}
                      </dd>
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
            disabled={mergeAction.busy || mergeAction.reconciling}
            onClick={() => void handleMerge()}
          >
            {mergeAction.busy
              ? 'Merging…'
              : mergeAction.reconciling
                ? 'Reconciling…'
                : hasUnmerged
                  ? 'Merge updates'
                  : 'Merge'}
          </button>
        </>
      ) : (
        <div className="completion-workspace__merge-empty">
          No local repositories to merge. All touched repositories are publishable.
        </div>
      )}
      <ResultBox result={mergeAction.result} remediationHint={REBASE_HANDOFF_HINT} />
    </div>
  );
}
