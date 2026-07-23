/**
 * Publish modal body: lets users select eligible repositories and publish
 * reviewed changes with a shared PR title/body. Preflight is owned by the
 * cockpit; on a successful publish the cockpit is asked to refresh it.
 */
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
    () => new Set(preflight.repos.filter(isEligibleForPublish).map((r) => r.repo)),
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

  const handlePublish = useCallback(async () => {
    if (publishRepos.size === 0 || !publishTitle.trim()) return;
    await publishAction.run(
      () =>
        dispatchAction(featureId, 'publish', {
          source_revision: preflight.sourceRevision,
          repos: Array.from(publishRepos),
          title: publishTitle.trim(),
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
    dispatchAction,
    onDispatched,
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

  const eligibleRepos = useMemo(
    () => preflight.repos.filter(isEligibleForPublish),
    [preflight],
  );

  const publishedRepos = useMemo(
    () => preflight.repos.filter((r) => r.status === 'already_published'),
    [preflight],
  );

  const ineligibleRepos = useMemo(
    () =>
      preflight.repos.filter((r) => r.touched && !r.publishable && r.status !== 'completed'),
    [preflight],
  );

  const hasSourceRevision = preflight.sourceRevision.trim() !== '';
  const canPublish =
    hasSourceRevision &&
    publishRepos.size > 0 &&
    publishTitle.trim().length > 0 &&
    !publishAction.busy;

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
  );
}
