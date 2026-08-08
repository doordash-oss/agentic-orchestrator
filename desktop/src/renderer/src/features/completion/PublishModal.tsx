import { useState, useCallback, useMemo } from 'react';
import { parseIpcError } from '../../wizard/ipcError';
import type { CompletionPreflightResult } from '../../../../shared/ipc';
import {
  ResultBox,
  PrLinkButton,
  useCompletionAction,
  isEligibleForPublish,
  type ActionResult,
  type CompletionAction,
} from './completionShared';
import { UNPUBLISHED_CHANGES, pendingDeliveryDetail } from './pendingDelivery';

export interface PublishModalProps {
  featureId: string;
  preflight: CompletionPreflightResult;
  dispatchAction: (
    featureId: string,
    action: CompletionAction,
    body?: Record<string, unknown>,
  ) => Promise<{ result: string; [k: string]: unknown }>;
  generatePublishDescription: (
    featureId: string,
    repos: string[],
  ) => Promise<{ featureId: string; title: string; body: string }>;
  openExternal: (url: string) => Promise<{ ok: boolean }>;
  onDispatched: () => void;
}

export function PublishModalBody({
  featureId,
  preflight,
  dispatchAction,
  generatePublishDescription,
  openExternal,
  onDispatched,
}: PublishModalProps): React.ReactElement {
  const [publishRepos, setPublishRepos] = useState<Set<string>>(
    () =>
      new Set(
        preflight.repos
          .filter((r) => isEligibleForPublish(r) || r.status === UNPUBLISHED_CHANGES)
          .map((r) => r.repo),
      ),
  );
  const [publishTitle, setPublishTitle] = useState('');
  const [publishBody, setPublishBody] = useState('');
  const [generatingDescription, setGeneratingDescription] = useState(false);
  const [publishGenResult, setPublishGenResult] = useState<ActionResult | null>(null);
  const publishAction = useCompletionAction();

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

  const eligibleRepos = useMemo(() => preflight.repos.filter(isEligibleForPublish), [preflight]);

  const unpublishedRepos = useMemo(
    () => preflight.repos.filter((r) => r.status === UNPUBLISHED_CHANGES),
    [preflight],
  );

  const publishedRepos = useMemo(
    () => preflight.repos.filter((r) => r.status === 'already_published'),
    [preflight],
  );

  const ineligibleRepos = useMemo(
    () => preflight.repos.filter((r) => r.touched && !r.publishable && r.status !== 'completed'),
    [preflight],
  );

  const titleRequired = useMemo(
    () => eligibleRepos.some((r) => publishRepos.has(r.repo)),
    [eligibleRepos, publishRepos],
  );

  const dirtySelected = useMemo(
    () => preflight.repos.filter((r) => publishRepos.has(r.repo) && r.pendingDirty === true),
    [preflight, publishRepos],
  );
  // The confirmation is derived from exactly which dirty repos (and how many
  // files each carries) it was ticked against, so it cannot outlive the
  // selection it confirmed — reselecting a different dirty set, or a refreshed
  // preflight that changes a file count, invalidates the tick.
  const dirtyKey = dirtySelected.map((r) => `${r.repo}:${r.pendingDirtyFileTotal ?? 0}`).join('|');
  const [confirmedKey, setConfirmedKey] = useState<string | null>(null);
  const commitConfirmed = confirmedKey === dirtyKey;

  const hasSourceRevision = preflight.sourceRevision.trim() !== '';
  const canPublish =
    hasSourceRevision &&
    publishRepos.size > 0 &&
    (!titleRequired || publishTitle.trim().length > 0) &&
    (dirtySelected.length === 0 || commitConfirmed) &&
    !publishAction.busy &&
    !publishAction.reconciling;

  const handlePublish = useCallback(async () => {
    const title = publishTitle.trim();
    if (publishRepos.size === 0 || (titleRequired && title === '')) return;
    if (dirtySelected.length > 0 && !commitConfirmed) return;
    await publishAction.run(
      () =>
        dispatchAction(featureId, 'publish', {
          source_revision: preflight.sourceRevision,
          repos: Array.from(publishRepos),
          ...(title === '' ? {} : { title }),
          ...(publishBody.trim() === '' ? {} : { body: publishBody }),
        }).then((r) => r.result),
      async () => onDispatched(),
    );
  }, [
    featureId,
    preflight,
    publishRepos,
    publishTitle,
    publishBody,
    titleRequired,
    dirtySelected,
    commitConfirmed,
    dispatchAction,
    onDispatched,
    publishAction,
  ]);

  return (
    <div className="completion-workspace__publish">
      <p className="completion-workspace__publish-hint">
        Select repositories to publish. A shared PR title and body will be used for all selected
        repositories.
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
                <span className="completion-workspace__repo-outcome-detail">{repo.lastError}</span>
              </div>
            ) : null}
            {repo.prUrl !== undefined ? (
              <PrLinkButton url={repo.prUrl} openExternal={openExternal} />
            ) : null}
          </div>
        ))}
        {eligibleRepos.length === 0 && unpublishedRepos.length === 0 && (
          <p className="completion-workspace__publish-empty">
            No eligible repositories to publish.
          </p>
        )}
        {unpublishedRepos.length > 0 && (
          <div className="completion-workspace__pending-repos">
            <h4>Unpublished changes</h4>
            {unpublishedRepos.map((repo) => (
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
                <span className="completion-workspace__pending-detail">
                  {pendingDeliveryDetail({
                    commits: repo.pendingCommits ?? 0,
                    dirty: repo.pendingDirty ?? false,
                  })}
                </span>
                {repo.pushMode === 'rewrite' ? (
                  <p className="completion-workspace__pending-note">
                    Force-updates the pull-request branch.
                  </p>
                ) : null}
                {repo.prUrl !== undefined ? (
                  <PrLinkButton url={repo.prUrl} openExternal={openExternal} />
                ) : null}
              </div>
            ))}
          </div>
        )}
        {publishedRepos.length > 0 && (
          <div className="completion-workspace__published-repos">
            <h4>Already published</h4>
            {publishedRepos.map((repo) => (
              <div key={repo.repo} className="completion-workspace__published-repo-row">
                <span className="completion-workspace__published-repo-name">{repo.repo}</span>
                <div className="completion-workspace__repo-outcome completion-workspace__repo-outcome--success">
                  <span className="completion-workspace__repo-outcome-label">Published</span>
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
                <span className="completion-workspace__ineligible-repo-name">{repo.repo}</span>
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
          aria-label="PR title"
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
          aria-label="PR body"
          value={publishBody}
          onChange={(e) => setPublishBody(e.target.value)}
          maxLength={4000}
          placeholder="Enter PR description"
          rows={6}
        />
      </label>
      {dirtySelected.length > 0 ? (
        <div className="completion-workspace__dirty-notice">
          <h4>Uncommitted changes</h4>
          {dirtySelected.map((repo) => {
            const total = repo.pendingDirtyFileTotal ?? 0;
            const files = repo.pendingDirtyFiles ?? [];
            return (
              <div key={repo.repo} className="completion-workspace__dirty-repo">
                {total > 0 ? (
                  <>
                    <p className="completion-workspace__dirty-repo-name">
                      {`${repo.repo} — ${total} uncommitted ${
                        total === 1 ? 'file' : 'files'
                      } will be committed and pushed:`}
                    </p>
                    <ul className="completion-workspace__dirty-files">
                      {files.map((path) => (
                        <li key={path}>
                          <code>{path}</code>
                        </li>
                      ))}
                    </ul>
                    {files.length < total ? (
                      <p className="completion-workspace__dirty-more">
                        {`+${total - files.length} more`}
                      </p>
                    ) : null}
                  </>
                ) : (
                  <>
                    <p className="completion-workspace__dirty-repo-name">{repo.repo}</p>
                    <p className="completion-workspace__dirty-unknown">
                      Could not list the files this publish would commit.
                    </p>
                  </>
                )}
              </div>
            );
          })}
          <label className="completion-workspace__dirty-confirm">
            <input
              type="checkbox"
              aria-label="Commit uncommitted files"
              checked={commitConfirmed}
              onChange={() => setConfirmedKey(commitConfirmed ? null : dirtyKey)}
            />
            <span>Commit these files as part of this publish</span>
          </label>
        </div>
      ) : null}
      <button
        type="button"
        className="completion-workspace__action"
        disabled={!canPublish}
        onClick={() => void handlePublish()}
      >
        {publishAction.busy
          ? 'Publishing…'
          : publishAction.reconciling
            ? 'Reconciling…'
            : titleRequired
              ? 'Publish'
              : 'Publish updates'}
      </button>
      <ResultBox result={publishAction.result} />
      {publishGenResult !== null && publishAction.result === null ? (
        <ResultBox result={publishGenResult} />
      ) : null}
    </div>
  );
}
