/**
 * Completion workspace: a guided diff-to-merge journey that starts from a
 * fresh server-authored completion preflight, lets users inspect bounded
 * diffs lazily in a read-only diff viewer with visual encoding, publish
 * reviewed changes across eligible repositories with a shared editable PR
 * title/body, recover local merges with explicit mark-done, clean completed
 * worktrees, and protect feature deletion with an exact-name confirmation.
 * The renderer never reproduces completion logic or reads feature files; it
 * renders the server-authored preview and sends the source revision back.
 */
import { useState, useCallback, useEffect, useMemo, useRef } from 'react';
import { useMediaQuery } from '../hooks';
import { parseIpcError } from '../wizard/ipcError';
import type { CompletionPreflightResult, RepositoryDiffResult } from '../../../shared/ipc';
import {
  DiffViewer,
  ResultBox,
  PrLinkButton,
  useCompletionAction,
  isEligibleForPublish,
  STATUS_LABELS,
  FILE_OP_GLYPH,
  type ActionResult,
  type CompletionAction,
  type DiffLayout,
} from './completion/completionShared';

export type CompletionStep = 'inspect' | 'publish' | 'merge' | 'done' | 'cleanup' | 'delete';

interface CompletionWorkspaceProps {
  featureId: string;
  featureName: string;
  onClose: () => void;
  initialStep?: CompletionStep;
  preflightCompletion: (featureId: string) => Promise<CompletionPreflightResult>;
  getRepositoryDiff: (
    featureId: string,
    repo: string,
    filePath?: string,
  ) => Promise<RepositoryDiffResult>;
  dispatchAction: (
    featureId: string,
    action: CompletionAction,
    body?: Record<string, unknown>,
  ) => Promise<{ result: string; [key: string]: unknown }>;
  generatePublishDescription: (
    featureId: string,
    repos: string[],
  ) => Promise<{ featureId: string; title: string; body: string }>;
  openExternal: (url: string) => Promise<{ ok: boolean }>;
  revealPath: (featureId: string, repo: string) => Promise<{ ok: boolean }>;
  onHandoffToRebase?: () => void;
}

export function CompletionWorkspace({
  featureId,
  featureName,
  onClose,
  initialStep = 'inspect',
  preflightCompletion,
  getRepositoryDiff,
  dispatchAction,
  generatePublishDescription,
  openExternal,
  revealPath,
  onHandoffToRebase,
}: CompletionWorkspaceProps): React.ReactElement {
  const [step, setStep] = useState<CompletionStep>(initialStep);
  const [preflight, setPreflight] = useState<CompletionPreflightResult | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [selectedRepo, setSelectedRepo] = useState<string | null>(null);
  const [diff, setDiff] = useState<RepositoryDiffResult | null>(null);
  const [diffLoading, setDiffLoading] = useState(false);
  const [diffLayoutOverride, setDiffLayoutOverride] = useState<DiffLayout | null>(null);
  const [selectedFile, setSelectedFile] = useState<string | null>(null);
  const [fileDiff, setFileDiff] = useState<string | null>(null);
  const [fileLoading, setFileLoading] = useState(false);
  const [publishRepos, setPublishRepos] = useState<Set<string>>(new Set());
  const [publishTitle, setPublishTitle] = useState('');
  const [publishBody, setPublishBody] = useState('');
  const [generatingDescription, setGeneratingDescription] = useState(false);
  const [publishGenResult, setPublishGenResult] = useState<ActionResult | null>(null);
  const [deleteConfirm, setDeleteConfirm] = useState('');
  const publishAction = useCompletionAction();
  const mergeAction = useCompletionAction();
  const doneAction = useCompletionAction();
  const cleanupAction = useCompletionAction();
  const deleteAction = useCompletionAction();
  const preflightRequestRef = useRef(0);
  const repoDiffRequestRef = useRef(0);
  const fileDiffRequestRef = useRef(0);
  const constrainedLayout = useMediaQuery('(max-width: 900px)');
  const diffLayout: DiffLayout =
    diffLayoutOverride ?? (constrainedLayout ? 'unified' : 'side-by-side');

  const refreshPreflight = useCallback(async () => {
    const request = ++preflightRequestRef.current;
    setLoading(true);
    setError(null);
    try {
      const result = await preflightCompletion(featureId);
      if (request !== preflightRequestRef.current) return;
      setPreflight(result);
      const eligible = result.repos.filter(isEligibleForPublish);
      setPublishRepos(new Set(eligible.map((r) => r.repo)));
    } catch (err) {
      if (request !== preflightRequestRef.current) return;
      setError(parseIpcError(err).message);
    } finally {
      if (request === preflightRequestRef.current) {
        setLoading(false);
      }
    }
  }, [featureId, preflightCompletion]);

  useEffect(() => {
    void refreshPreflight();
    return () => {
      preflightRequestRef.current += 1;
      repoDiffRequestRef.current += 1;
      fileDiffRequestRef.current += 1;
    };
  }, [refreshPreflight]);

  const loadRepoDiff = useCallback(
    async (repo: string) => {
      const request = ++repoDiffRequestRef.current;
      fileDiffRequestRef.current += 1;
      setDiffLoading(true);
      setDiff(null);
      setSelectedFile(null);
      setFileDiff(null);
      try {
        const result = await getRepositoryDiff(featureId, repo);
        if (request !== repoDiffRequestRef.current) return;
        setDiff(result);
      } catch (err) {
        if (request !== repoDiffRequestRef.current) return;
        setError(parseIpcError(err).message);
      } finally {
        if (request === repoDiffRequestRef.current) {
          setDiffLoading(false);
        }
      }
    },
    [featureId, getRepositoryDiff],
  );

  const loadFileDiff = useCallback(
    async (repo: string, filePath: string) => {
      const request = ++fileDiffRequestRef.current;
      setFileLoading(true);
      setFileDiff(null);
      try {
        const result = await getRepositoryDiff(featureId, repo, filePath);
        if (request !== fileDiffRequestRef.current) return;
        if (result.fileDiff) {
          setFileDiff(result.fileDiff);
        } else if (result.fileBinary) {
          setFileDiff('Binary file — diff content unavailable');
        } else if (result.fileUnavailable) {
          setFileDiff('File content unavailable');
        }
      } catch (err) {
        if (request !== fileDiffRequestRef.current) return;
        setError(parseIpcError(err).message);
      } finally {
        if (request === fileDiffRequestRef.current) {
          setFileLoading(false);
        }
      }
    },
    [featureId, getRepositoryDiff],
  );

  const handleRepoSelect = useCallback(
    (repo: string) => {
      setSelectedRepo(repo);
      void loadRepoDiff(repo);
    },
    [loadRepoDiff],
  );

  const handleFileSelect = useCallback(
    (filePath: string) => {
      setSelectedFile(filePath);
      if (selectedRepo) {
        void loadFileDiff(selectedRepo, filePath);
      }
    },
    [loadFileDiff, selectedRepo],
  );

  const togglePublishRepo = useCallback((repo: string) => {
    setPublishRepos((prev) => {
      const next = new Set(prev);
      if (next.has(repo)) {
        next.delete(repo);
      } else {
        next.add(repo);
      }
      return next;
    });
  }, []);

  const handlePublish = useCallback(async () => {
    if (preflight === null || publishRepos.size === 0 || !publishTitle.trim()) return;
    await publishAction.run(
      () =>
        dispatchAction(featureId, 'publish', {
          source_revision: preflight.sourceRevision,
          repos: Array.from(publishRepos),
          title: publishTitle.trim(),
          ...(publishBody.trim() === '' ? {} : { body: publishBody }),
        }).then((r) => r.result),
      refreshPreflight,
    );
  }, [
    featureId,
    preflight,
    publishRepos,
    publishTitle,
    publishBody,
    dispatchAction,
    refreshPreflight,
    publishAction,
  ]);

  const handleGeneratePublishDescription = useCallback(async () => {
    setGeneratingDescription(true);
    setPublishGenResult(null);
    try {
      const result = await generatePublishDescription(featureId, Array.from(publishRepos));
      setPublishTitle(result.title);
      setPublishBody(result.body);
    } catch (err) {
      setPublishGenResult({ ok: false, message: parseIpcError(err).message });
    } finally {
      setGeneratingDescription(false);
    }
  }, [featureId, generatePublishDescription, publishRepos]);

  const handleMerge = useCallback(async () => {
    if (preflight === null) return;
    await mergeAction.run(
      () =>
        dispatchAction(featureId, 'merge', {
          source_revision: preflight.sourceRevision,
        }).then((r) => r.result),
      refreshPreflight,
    );
  }, [featureId, preflight, dispatchAction, refreshPreflight, mergeAction]);

  const handleMarkDone = useCallback(async () => {
    if (preflight === null) return;
    await doneAction.run(
      () =>
        dispatchAction(featureId, 'mark-done', {
          source_revision: preflight.sourceRevision,
        }).then((r) => r.result),
      refreshPreflight,
    );
  }, [featureId, preflight, dispatchAction, refreshPreflight, doneAction]);

  const handleCleanup = useCallback(async () => {
    if (preflight === null) return;
    await cleanupAction.run(
      () =>
        dispatchAction(featureId, 'cleanup', {
          source_revision: preflight.sourceRevision,
          target: 'worktrees',
        }).then((r) => r.result),
      refreshPreflight,
    );
  }, [featureId, preflight, dispatchAction, refreshPreflight, cleanupAction]);

  const handleDelete = useCallback(async () => {
    if (preflight === null || deleteConfirm !== featureName) return;
    const success = await deleteAction.run(() =>
      dispatchAction(featureId, 'delete', {
        source_revision: preflight.sourceRevision,
      }).then((r) => r.result),
    );
    if (success) onClose();
  }, [featureId, featureName, preflight, deleteConfirm, dispatchAction, onClose, deleteAction]);

  const eligibleRepos = useMemo(
    () => preflight?.repos.filter(isEligibleForPublish) ?? [],
    [preflight],
  );

  const publishedRepos = useMemo(
    () => preflight?.repos.filter((r) => r.status === 'already_published') ?? [],
    [preflight],
  );

  const ineligibleRepos = useMemo(
    () =>
      preflight?.repos.filter((r) => r.touched && !r.publishable && r.status !== 'completed') ?? [],
    [preflight],
  );

  const hasSourceRevision = preflight !== null && preflight.sourceRevision.trim() !== '';
  const canPublish =
    hasSourceRevision &&
    !loading &&
    publishRepos.size > 0 &&
    publishTitle.trim().length > 0 &&
    !publishAction.busy;
  const canDelete =
    hasSourceRevision && !loading && deleteConfirm === featureName && !deleteAction.busy;
  const localMergeRepos = useMemo(
    () => preflight?.repos.filter((r) => !r.publishable && r.touched) ?? [],
    [preflight],
  );

  return (
    <section className="completion-workspace" data-step={step} aria-label="Completion workspace">
      <div className="completion-workspace__header">
        <h2 className="completion-workspace__title">Completion</h2>
        <div className="completion-workspace__steps">
          {(['inspect', 'publish', 'merge', 'done', 'cleanup', 'delete'] as CompletionStep[]).map(
            (s) => (
              <button
                type="button"
                key={s}
                className={`completion-workspace__step ${step === s ? 'is-active' : ''}`}
                onClick={() => setStep(s)}
                aria-label={`Completion step: ${s}`}
              >
                {s.charAt(0).toUpperCase() + s.slice(1)}
              </button>
            ),
          )}
        </div>
        <button
          type="button"
          className="completion-workspace__close"
          onClick={onClose}
          aria-label="Close completion"
        >
          ×
        </button>
      </div>

      {error && (
        <div className="completion-workspace__error" role="alert">
          {error}
          <button type="button" onClick={() => void refreshPreflight()}>
            Retry
          </button>
        </div>
      )}

      {loading && (
        <div className="completion-workspace__loading">Loading completion preflight…</div>
      )}

      {preflight && (
        <div className="completion-workspace__body">
          {step === 'inspect' && (
            <div className="completion-workspace__inspect">
              <div className="completion-workspace__repos">
                <h3>Repositories</h3>
                {preflight.repos.map((repo) => (
                  <div
                    key={repo.repo}
                    className={`completion-workspace__repo ${selectedRepo === repo.repo ? 'is-selected' : ''}`}
                  >
                    <button
                      type="button"
                      className="completion-workspace__repo-select"
                      onClick={() => handleRepoSelect(repo.repo)}
                    >
                      <span className="completion-workspace__repo-name">{repo.repo}</span>
                      <span className="completion-workspace__repo-status" data-status={repo.status}>
                        {STATUS_LABELS[repo.status] ?? repo.status}
                      </span>
                    </button>
                    {repo.prUrl !== undefined ? (
                      <PrLinkButton url={repo.prUrl} openExternal={openExternal} />
                    ) : null}
                    <button
                      type="button"
                      className="completion-workspace__reveal"
                      onClick={() => void revealPath(featureId, repo.repo)}
                    >
                      Reveal
                    </button>
                  </div>
                ))}
              </div>

              {selectedRepo && diffLoading && (
                <div className="completion-workspace__diff-loading">Loading diff…</div>
              )}

              {selectedRepo && diff && (
                <div className="completion-workspace__diff">
                  <div className="completion-workspace__diff-toolbar">
                    <label className="completion-workspace__layout-toggle">
                      <input
                        type="checkbox"
                        checked={diffLayout === 'side-by-side'}
                        onChange={(e) =>
                          setDiffLayoutOverride(e.target.checked ? 'side-by-side' : 'unified')
                        }
                      />
                      Side-by-side
                    </label>
                    {diff.partialFailure && (
                      <span className="completion-workspace__partial-failure">
                        {diff.partialFailure}
                      </span>
                    )}
                    {diff.truncated && (
                      <span className="completion-workspace__truncated">Truncated</span>
                    )}
                  </div>
                  <div className="completion-workspace__files">
                    {diff.files.map((file) => (
                      <button
                        key={file.path + (file.oldPath ?? '')}
                        type="button"
                        className={`completion-workspace__file ${selectedFile === file.path ? 'is-selected' : ''}`}
                        onClick={() => handleFileSelect(file.path)}
                      >
                        <span className="completion-workspace__file-op" data-op={file.operation}>
                          {FILE_OP_GLYPH[file.operation] ?? 'M'}
                        </span>
                        <span className="completion-workspace__file-path">{file.path}</span>
                        {file.addedLines !== undefined && file.removedLines !== undefined && (
                          <span className="completion-workspace__file-lines">
                            +{file.addedLines} −{file.removedLines}
                          </span>
                        )}
                        {file.binary && (
                          <span className="completion-workspace__file-binary">binary</span>
                        )}
                      </button>
                    ))}
                    {diff.files.length === 0 && !diff.partialFailure && (
                      <p className="completion-workspace__no-changes">No changes</p>
                    )}
                  </div>

                  {selectedFile && fileLoading && (
                    <div className="completion-workspace__file-loading">Loading file diff…</div>
                  )}

                  {selectedFile && !fileLoading && fileDiff && (
                    <div className="completion-workspace__file-diff">
                      <DiffViewer
                        diffText={fileDiff}
                        renderSideBySide={diffLayout === 'side-by-side'}
                      />
                    </div>
                  )}

                  {selectedFile && !fileLoading && !fileDiff && (
                    <div className="completion-workspace__file-placeholder">
                      No diff content available for this file.
                    </div>
                  )}
                </div>
              )}
            </div>
          )}

          {step === 'publish' && (
            <div className="completion-workspace__publish">
              <h3>Publish reviewed changes</h3>
              <p className="completion-workspace__publish-hint">
                Select repositories to publish. A shared PR title and body will be used for all
                selected repositories.
              </p>
              <div className="completion-workspace__publish-repos">
                {eligibleRepos.map((repo) => (
                  <div key={repo.repo} className="completion-workspace__publish-repo">
                    <label className="completion-workspace__publish-repo-check">
                      <input
                        type="checkbox"
                        aria-label={repo.repo}
                        checked={publishRepos.has(repo.repo)}
                        onChange={() => togglePublishRepo(repo.repo)}
                      />
                      <span className="completion-workspace__publish-repo-name">{repo.repo}</span>
                    </label>
                    {repo.lastError !== undefined ? (
                      <div
                        className="completion-workspace__repo-outcome completion-workspace__repo-outcome--failure"
                        role="alert"
                      >
                        <span className="completion-workspace__repo-outcome-label">Failed</span>
                        <span className="completion-workspace__repo-outcome-detail">
                          {repo.lastError}
                        </span>
                      </div>
                    ) : null}
                    {repo.prUrl !== undefined ? (
                      <PrLinkButton url={repo.prUrl} openExternal={openExternal} />
                    ) : null}
                  </div>
                ))}
                {eligibleRepos.length === 0 && (
                  <p className="completion-workspace__publish-empty">
                    No eligible repositories to publish.
                  </p>
                )}
                {publishedRepos.length > 0 && (
                  <div className="completion-workspace__published-repos">
                    <h4>Already published</h4>
                    {publishedRepos.map((repo) => (
                      <div key={repo.repo} className="completion-workspace__published-repo-row">
                        <span className="completion-workspace__published-repo-name">
                          {repo.repo}
                        </span>
                        <div className="completion-workspace__repo-outcome completion-workspace__repo-outcome--success">
                          <span className="completion-workspace__repo-outcome-label">
                            Published
                          </span>
                          {repo.prUrl !== undefined ? (
                            <PrLinkButton url={repo.prUrl} openExternal={openExternal} />
                          ) : null}
                        </div>
                      </div>
                    ))}
                  </div>
                )}
                {ineligibleRepos.length > 0 && (
                  <div className="completion-workspace__ineligible-repos">
                    <h4>Not publishable</h4>
                    {ineligibleRepos.map((repo) => (
                      <div key={repo.repo} className="completion-workspace__ineligible-repo-row">
                        <span className="completion-workspace__ineligible-repo-name">
                          {repo.repo}
                        </span>
                        <span className="completion-workspace__ineligible-repo-reason">
                          {repo.blocker !== undefined ? repo.blocker : 'Local-only repository'}
                        </span>
                      </div>
                    ))}
                  </div>
                )}
              </div>
              <label className="completion-workspace__field">
                <span>PR title</span>
                <input
                  type="text"
                  value={publishTitle}
                  onChange={(e) => setPublishTitle(e.target.value)}
                  maxLength={200}
                  placeholder="Enter PR title"
                />
              </label>
              <button
                type="button"
                className="completion-workspace__secondary-action"
                disabled={generatingDescription}
                onClick={() => void handleGeneratePublishDescription()}
              >
                {generatingDescription ? 'Generating…' : 'Generate PR narrative'}
              </button>
              <label className="completion-workspace__field">
                <span>PR body</span>
                <textarea
                  value={publishBody}
                  onChange={(e) => setPublishBody(e.target.value)}
                  maxLength={4000}
                  placeholder="Enter PR description"
                  rows={6}
                />
              </label>
              <button
                type="button"
                className="completion-workspace__action"
                disabled={!canPublish}
                onClick={() => void handlePublish()}
              >
                {publishAction.busy ? 'Publishing…' : 'Publish'}
              </button>
              <ResultBox result={publishAction.result} />
              {publishGenResult !== null && publishAction.result === null ? (
                <ResultBox result={publishGenResult} />
              ) : null}
            </div>
          )}

          {step === 'merge' && (
            <div className="completion-workspace__merge">
              <h3>Merge local repositories</h3>
              <p className="completion-workspace__merge-hint">
                Merging records per-repository outcomes but does not mark the feature Done. A
                conflict offers the rebase journey for the affected repository.
              </p>
              {localMergeRepos.length > 0 ? (
                <>
                  <div className="completion-workspace__merge-scope">
                    {localMergeRepos.map((repo) => (
                      <div key={repo.repo} className="completion-workspace__merge-repo">
                        <div className="completion-workspace__merge-repo-header">
                          <span className="completion-workspace__merge-repo-name">{repo.repo}</span>
                          <span
                            className="completion-workspace__repo-status"
                            data-status={repo.status}
                          >
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
                    disabled={loading || mergeAction.busy}
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
              {mergeAction.result !== null &&
              !mergeAction.result.ok &&
              onHandoffToRebase !== undefined ? (
                <div className="completion-workspace__merge-handoff">
                  <p className="completion-workspace__merge-handoff-hint">
                    A conflict or behind-base outcome can be resolved through the rebase journey.
                    Hand off, then return here to retry the merge.
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
          )}

          {step === 'done' && (
            <div className="completion-workspace__done">
              <h3>Mark Done</h3>
              <p className="completion-workspace__done-hint">
                Marking Done is a separate server-authorized action. It transitions the feature only
                after the server confirms success.
              </p>
              {preflight.canMarkDone ? (
                <button
                  type="button"
                  className="completion-workspace__action"
                  disabled={loading || doneAction.busy}
                  onClick={() => void handleMarkDone()}
                >
                  {doneAction.busy ? 'Marking Done…' : 'Mark Done'}
                </button>
              ) : (
                <div className="completion-workspace__blocked">
                  {preflight.markDoneBlocker ?? 'Mark Done is not available'}
                </div>
              )}
              <ResultBox result={doneAction.result} />
            </div>
          )}

          {step === 'cleanup' && (
            <div className="completion-workspace__cleanup">
              <h3>Clean worktrees</h3>
              <div className="completion-workspace__cleanup-consequences">
                <div className="completion-workspace__consequence-group">
                  <h4>Removes</h4>
                  <ul>
                    <li>Completed feature worktrees</li>
                  </ul>
                </div>
                <div className="completion-workspace__consequence-group">
                  <h4>Preserves</h4>
                  <ul>
                    <li>Branches</li>
                    <li>Feature/run history</li>
                    <li>Artifacts</li>
                    <li>PR metadata</li>
                    <li>Repository-cycle records</li>
                  </ul>
                </div>
              </div>
              <button
                type="button"
                className="completion-workspace__action"
                disabled={loading || cleanupAction.busy}
                onClick={() => void handleCleanup()}
              >
                {cleanupAction.busy ? 'Cleaning…' : 'Clean worktrees'}
              </button>
              <ResultBox result={cleanupAction.result} />
            </div>
          )}

          {step === 'delete' && (
            <div className="completion-workspace__delete">
              <h3>Delete feature</h3>
              <div className="completion-workspace__delete-consequences">
                <p className="completion-workspace__delete-warning">
                  Deletion is irreversible. It removes the feature, its runs, artifacts, and
                  worktrees. Type the exact feature name to confirm.
                </p>
                <div className="completion-workspace__consequence-group completion-workspace__consequence-group--context">
                  <h4>Cleanup already applied — reversible</h4>
                  <ul>
                    <li>Removed: completed feature worktrees</li>
                    <li>
                      Preserved: branches, feature/run history, artifacts, PR metadata, cycle
                      records
                    </li>
                  </ul>
                </div>
                <div className="completion-workspace__consequence-group">
                  <h4>Removes permanently</h4>
                  <ul>
                    <li>The feature and all its runs</li>
                    <li>All artifacts and worktrees</li>
                    <li>PR metadata and cycle records</li>
                  </ul>
                </div>
              </div>
              <label className="completion-workspace__field">
                <span>Type feature name to confirm</span>
                <input
                  type="text"
                  value={deleteConfirm}
                  onChange={(e) => setDeleteConfirm(e.target.value)}
                  placeholder={featureName}
                />
              </label>
              <button
                type="button"
                className="completion-workspace__action completion-workspace__action--danger"
                disabled={!canDelete}
                onClick={() => void handleDelete()}
              >
                {deleteAction.busy ? 'Deleting…' : 'Delete feature'}
              </button>
              <ResultBox result={deleteAction.result} />
            </div>
          )}
        </div>
      )}
    </section>
  );
}
