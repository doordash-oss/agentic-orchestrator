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

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type {
  CompletionPreflightResult,
  FeatureActionResult,
  PublishFeatureActionRequest,
} from '../../../../shared/ipc';
import { E_REQUEST_TIMEOUT } from '../../../../shared/errors';
import { useModalDismiss } from '../../components/useModalDismiss';
import { parseIpcError } from '../../wizard/ipcError';
import { PrLinkButton, isEligibleForPublish, type ActionResult } from './completionShared';
import { UNPUBLISHED_CHANGES, pendingDeliveryDetail } from './pendingDelivery';

const PUBLISH_FAILURE_COPY: Record<string, { title: string; next: string }> = {
  publish_remote_diverged: {
    title: "The pull-request branch contains changes that aren't in this workspace.",
    next: 'Review and reconcile the branch on GitHub, then refresh and retry.',
  },
  publish_remote_changed: {
    title: 'The pull-request branch changed while Agentico was publishing.',
    next: 'Refresh the publish state and retry. Agentico did not overwrite the newer branch.',
  },
};
const PUBLISH_TIMEOUT_LOCKED_MESSAGE =
  'Publish may still be running. Quit and reopen Agentico before publishing again.';
const UNKNOWN_PUBLISH_FAILURE_NEXT = 'Review the details, then refresh and retry.';

export interface PublishModalProps {
  featureId: string;
  preflight: CompletionPreflightResult;
  dispatchAction(request: PublishFeatureActionRequest): Promise<FeatureActionResult>;
  generatePublishDescription(
    featureId: string,
    repos: string[],
  ): Promise<{ featureId: string; title: string; body: string }>;
  openExternal(url: string): Promise<{ ok: boolean }>;
  onDispatched(): void | Promise<void>;
  onClose(): void;
  publishTimeoutLocked: boolean;
  setPublishTimeoutLocked(locked: boolean): void;
}

function PublishFailureNotice({
  result,
  noticeRef,
}: {
  result: ActionResult | null;
  noticeRef?: React.RefObject<HTMLDivElement | null>;
}) {
  if (result === null || result.ok || result.reconciling) return null;
  const copy = PUBLISH_FAILURE_COPY[result.code];
  return (
    <div ref={noticeRef} className="completion-publish-sheet__failure" role="alert" tabIndex={-1}>
      <strong>{copy?.title ?? "Agentico couldn't prepare this publish."}</strong>
      {copy !== undefined ? <p>{copy.next}</p> : null}
      {copy === undefined ? (
        <>
          <p>{result.remediation ?? UNKNOWN_PUBLISH_FAILURE_NEXT}</p>
          <details>
            <summary>Show details</summary>
            <p>{result.message}</p>
          </details>
        </>
      ) : null}
    </div>
  );
}

function PublishStatusNotice({
  result,
  publishTimeoutLocked,
}: {
  result: ActionResult | null;
  publishTimeoutLocked: boolean;
}) {
  if (result === null) {
    if (!publishTimeoutLocked) return null;
    return (
      <div className="completion-publish-sheet__status" role="status">
        {PUBLISH_TIMEOUT_LOCKED_MESSAGE}
      </div>
    );
  }
  if (result.ok) {
    return (
      <div className="completion-publish-sheet__status" role="status">
        {result.result}
      </div>
    );
  }
  if (result.reconciling) {
    return (
      <div className="completion-publish-sheet__status" role="status">
        {result.message}
      </div>
    );
  }
  return null;
}

export function PublishModal({
  featureId,
  preflight,
  dispatchAction,
  generatePublishDescription,
  openExternal,
  onDispatched,
  onClose,
  publishTimeoutLocked,
  setPublishTimeoutLocked,
}: PublishModalProps): React.ReactElement {
  const dialogRef = useRef<HTMLDivElement>(null);
  const titleRef = useRef<HTMLInputElement>(null);
  const failureRef = useRef<HTMLDivElement>(null);
  const [publishRepos, setPublishRepos] = useState<Set<string>>(
    () =>
      new Set(
        preflight.repos
          .filter((repo) => isEligibleForPublish(repo) || repo.status === UNPUBLISHED_CHANGES)
          .map((repo) => repo.repo),
      ),
  );
  const [publishTitle, setPublishTitle] = useState('');
  const [publishBody, setPublishBody] = useState('');
  const [titleVisited, setTitleVisited] = useState(false);
  const [generatingDescription, setGeneratingDescription] = useState(false);
  const [publishGenResult, setPublishGenResult] = useState<ActionResult | null>(null);
  const [publishBusy, setPublishBusy] = useState(false);
  const [timedOutThisOpen, setTimedOutThisOpen] = useState(false);
  const [publishResult, setPublishResult] = useState<ActionResult | null>(null);
  const publishLocked = publishTimeoutLocked || timedOutThisOpen;

  const eligibleRepos = useMemo(() => preflight.repos.filter(isEligibleForPublish), [preflight]);
  const unpublishedRepos = useMemo(
    () => preflight.repos.filter((repo) => repo.status === UNPUBLISHED_CHANGES),
    [preflight],
  );
  const publishedRepos = useMemo(
    () => preflight.repos.filter((repo) => repo.status === 'already_published'),
    [preflight],
  );
  const ineligibleRepos = useMemo(
    () =>
      preflight.repos.filter(
        (repo) => repo.touched && !repo.publishable && repo.status !== 'completed',
      ),
    [preflight],
  );
  const titleRequired = useMemo(
    () => eligibleRepos.some((repo) => publishRepos.has(repo.repo)),
    [eligibleRepos, publishRepos],
  );
  const titleInvalid = titleRequired && titleVisited && publishTitle.trim() === '';
  const dirtySelected = useMemo(
    () =>
      preflight.repos.filter((repo) => publishRepos.has(repo.repo) && repo.pendingDirty === true),
    [preflight, publishRepos],
  );
  const dirtyKey = dirtySelected
    .map((repo) => `${repo.repo}:${repo.pendingDirtyFileTotal ?? 0}`)
    .join('|');
  const [confirmedKey, setConfirmedKey] = useState<string | null>(null);
  const commitConfirmed = confirmedKey === dirtyKey;
  const canPublish =
    preflight.sourceRevision.trim() !== '' &&
    publishRepos.size > 0 &&
    (!titleRequired || publishTitle.trim() !== '') &&
    (dirtySelected.length === 0 || commitConfirmed) &&
    !publishBusy &&
    !publishLocked;

  const requestClose = useCallback(() => {
    if (!publishBusy) onClose();
  }, [onClose, publishBusy]);
  useModalDismiss(dialogRef, requestClose);

  useEffect(() => {
    const publishable = new Set(
      preflight.repos
        .filter((repo) => isEligibleForPublish(repo) || repo.status === UNPUBLISHED_CHANGES)
        .map((repo) => repo.repo),
    );
    setPublishRepos((previous) => new Set([...previous].filter((repo) => publishable.has(repo))));
  }, [preflight]);

  useEffect(() => {
    if (
      (publishResult !== null && !publishResult.ok && !publishResult.reconciling) ||
      (publishGenResult !== null && !publishGenResult.ok)
    ) {
      failureRef.current?.focus();
    }
  }, [publishGenResult, publishResult]);

  const togglePublishRepo = useCallback((repo: string) => {
    setPublishRepos((previous) => {
      const next = new Set(previous);
      if (next.has(repo)) next.delete(repo);
      else next.add(repo);
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
    } catch (error) {
      const parsed = parseIpcError(error);
      setPublishGenResult({
        ok: false,
        code: parsed.code,
        message: parsed.message,
        ...(parsed.remediation === undefined ? {} : { remediation: parsed.remediation }),
      });
    } finally {
      setGeneratingDescription(false);
    }
  }, [featureId, generatePublishDescription, publishRepos]);

  const handlePublish = useCallback(async () => {
    const title = publishTitle.trim();
    if (titleRequired && title === '') {
      setTitleVisited(true);
      titleRef.current?.focus();
      return;
    }
    if (!canPublish) return;
    const request: PublishFeatureActionRequest = {
      featureId,
      action: 'publish',
      body: {
        source_revision: preflight.sourceRevision,
        repos: Array.from(publishRepos),
        ...(title === '' ? {} : { title }),
        ...(publishBody.trim() === '' ? {} : { body: publishBody }),
      },
    };
    setPublishBusy(true);
    setPublishResult(null);
    try {
      const result = await dispatchAction(request);
      await onDispatched();
      setPublishResult({ ok: true, result: result.result });
    } catch (error) {
      const parsed = parseIpcError(error);
      if (parsed.code === E_REQUEST_TIMEOUT) {
        setTimedOutThisOpen(true);
        setPublishTimeoutLocked(true);
        setPublishResult({
          ok: false,
          code: parsed.code,
          message: 'Publish may still be running. Refreshing the latest publish state…',
          reconciling: true,
        });
        try {
          await onDispatched();
        } catch {
          // Refresh hooks can deliberately absorb their own transport errors.
        }
        setPublishResult({
          ok: false,
          code: parsed.code,
          message: PUBLISH_TIMEOUT_LOCKED_MESSAGE,
          reconciling: true,
        });
      } else {
        try {
          await onDispatched();
        } catch {
          // Preserve the publish failure when the best-effort refresh also fails.
        }
        setPublishResult({
          ok: false,
          code: parsed.code,
          message: parsed.message,
          ...(parsed.remediation === undefined ? {} : { remediation: parsed.remediation }),
        });
      }
    } finally {
      setPublishBusy(false);
    }
  }, [
    canPublish,
    dispatchAction,
    featureId,
    onDispatched,
    preflight.sourceRevision,
    setPublishTimeoutLocked,
    publishBody,
    publishRepos,
    publishTitle,
    titleRequired,
  ]);

  return (
    <div className="sheet-scrim completion-publish-sheet__scrim" onMouseDown={requestClose}>
      <div
        ref={dialogRef}
        className="sheet completion-publish-sheet"
        role="dialog"
        aria-modal="true"
        aria-label="Publish reviewed changes"
        tabIndex={-1}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="sheet__body completion-publish-sheet__body">
          <div className="completion-workspace__publish">
            <div>
              <h3>Publish updates</h3>
              <p className="completion-workspace__publish-hint">
                {titleRequired
                  ? 'Choose the repositories whose reviewed work is ready. Existing pull requests are updated without changing their narrative.'
                  : 'Agentico will update the selected pull-request branches.'}
              </p>
            </div>
            <div className="completion-workspace__publish-repos">
              {eligibleRepos.map((repo) => (
                <PublishRepoRow
                  key={repo.repo}
                  repo={repo}
                  checked={publishRepos.has(repo.repo)}
                  onToggle={togglePublishRepo}
                  openExternal={openExternal}
                />
              ))}
              {unpublishedRepos.length > 0 ? (
                <div className="completion-workspace__pending-repos">
                  <h4>Unpublished changes</h4>
                  {unpublishedRepos.map((repo) => (
                    <PublishRepoRow
                      key={repo.repo}
                      repo={repo}
                      checked={publishRepos.has(repo.repo)}
                      onToggle={togglePublishRepo}
                      openExternal={openExternal}
                    />
                  ))}
                </div>
              ) : null}
              {eligibleRepos.length === 0 && unpublishedRepos.length === 0 ? (
                <p className="completion-workspace__publish-empty">
                  No eligible repositories to publish.
                </p>
              ) : null}
              {publishedRepos.length > 0 ? (
                <RepoGroup
                  title="Already published"
                  repos={publishedRepos}
                  openExternal={openExternal}
                />
              ) : null}
              {ineligibleRepos.length > 0 ? (
                <RepoGroup
                  title="Not publishable"
                  repos={ineligibleRepos}
                  openExternal={openExternal}
                />
              ) : null}
            </div>
            {titleRequired ? (
              <section
                className="completion-publish-sheet__details"
                aria-label="Pull request details"
              >
                <div className="completion-publish-sheet__details-heading">
                  <h4>Pull request details</h4>
                  <button
                    type="button"
                    className="completion-workspace__secondary-action"
                    disabled={generatingDescription}
                    onClick={() => void handleGeneratePublishDescription()}
                  >
                    {generatingDescription ? 'Generating…' : 'Generate narrative'}
                  </button>
                </div>
                <label className="completion-workspace__field">
                  <span>
                    PR title <em>Required</em>
                  </span>
                  <input
                    ref={titleRef}
                    aria-label="PR title"
                    aria-invalid={titleInvalid || undefined}
                    aria-describedby={titleInvalid ? 'publish-title-error' : undefined}
                    value={publishTitle}
                    onChange={(event) => {
                      setPublishTitle(event.target.value);
                      if (event.target.value.trim() !== '') setTitleVisited(false);
                    }}
                    onBlur={() => setTitleVisited(true)}
                    maxLength={200}
                    placeholder="Enter PR title"
                  />
                </label>
                {titleInvalid ? (
                  <p id="publish-title-error" className="completion-publish-sheet__field-error">
                    Add a title to create the pull request.
                  </p>
                ) : null}
                <label className="completion-workspace__field">
                  <span>
                    PR body <em>Optional</em>
                  </span>
                  <textarea
                    aria-label="PR body"
                    value={publishBody}
                    onChange={(event) => setPublishBody(event.target.value)}
                    maxLength={4000}
                    rows={5}
                    placeholder="Enter PR description"
                  />
                </label>
              </section>
            ) : null}
            {dirtySelected.length > 0 ? (
              <div className="completion-workspace__dirty-notice">
                <h4>Uncommitted changes</h4>
                {dirtySelected.map((repo) => (
                  <DirtyRepo key={repo.repo} repo={repo} />
                ))}
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
            <PublishStatusNotice
              result={publishResult}
              publishTimeoutLocked={publishTimeoutLocked}
            />
            <PublishFailureNotice result={publishResult} noticeRef={failureRef} />
            {publishGenResult !== null && publishResult === null ? (
              <PublishFailureNotice result={publishGenResult} noticeRef={failureRef} />
            ) : null}
          </div>
        </div>
        <footer className="sheet__footer">
          <button type="button" className="sheet__footer-secondary" onClick={requestClose}>
            Cancel
          </button>
          <button
            type="button"
            className="sheet__footer-primary"
            disabled={!canPublish}
            onClick={() => void handlePublish()}
          >
            {publishBusy
              ? 'Publishing…'
              : publishLocked
                ? 'Reconciling…'
                : titleRequired
                  ? 'Publish'
                  : 'Publish updates'}
          </button>
        </footer>
      </div>
    </div>
  );
}

type PublishRepo = CompletionPreflightResult['repos'][number];

function PublishRepoRow({
  repo,
  checked,
  onToggle,
  openExternal,
}: {
  repo: PublishRepo;
  checked: boolean;
  onToggle(repo: string): void;
  openExternal(url: string): Promise<{ ok: boolean }>;
}) {
  return (
    <div className="completion-workspace__publish-repo">
      <div className="completion-workspace__publish-repo-main">
        <input
          type="checkbox"
          aria-label={repo.repo}
          checked={checked}
          onChange={() => onToggle(repo.repo)}
        />
        <span className="completion-workspace__publish-repo-name">{repo.repo}</span>
        {repo.lastError !== undefined ? (
          <span className="completion-workspace__repo-outcome-detail" role="alert">
            {repo.lastError}
          </span>
        ) : null}
      </div>
      <div className="completion-workspace__publish-repo-meta">
        {repo.status === UNPUBLISHED_CHANGES ? (
          <span className="completion-workspace__pending-detail">
            {pendingDeliveryDetail({
              commits: repo.pendingCommits ?? 0,
              dirty: repo.pendingDirty ?? false,
            })}
          </span>
        ) : null}
        {repo.prUrl !== undefined ? (
          <PrLinkButton url={repo.prUrl} openExternal={openExternal} />
        ) : null}
      </div>
      {repo.pushMode === 'rewrite' ? (
        <p className="completion-workspace__pending-note">
          Rewrites the pull-request branch with a safety lease.
        </p>
      ) : null}
    </div>
  );
}

function RepoGroup({
  title,
  repos,
  openExternal,
}: {
  title: string;
  repos: PublishRepo[];
  openExternal(url: string): Promise<{ ok: boolean }>;
}) {
  return (
    <div
      className={
        title === 'Already published'
          ? 'completion-workspace__published-repos'
          : 'completion-workspace__ineligible-repos'
      }
    >
      <h4>{title}</h4>
      {repos.map((repo) => (
        <div
          key={repo.repo}
          className={
            title === 'Already published'
              ? 'completion-workspace__published-repo-row'
              : 'completion-workspace__ineligible-repo-row'
          }
        >
          <span>{repo.repo}</span>
          {title === 'Already published' && repo.prUrl !== undefined ? (
            <PrLinkButton url={repo.prUrl} openExternal={openExternal} />
          ) : (
            <span className="completion-workspace__ineligible-repo-reason">
              {repo.blocker ?? 'Local-only repository'}
            </span>
          )}
        </div>
      ))}
    </div>
  );
}

function DirtyRepo({ repo }: { repo: PublishRepo }) {
  const total = repo.pendingDirtyFileTotal ?? 0;
  const files = repo.pendingDirtyFiles ?? [];
  return (
    <div className="completion-workspace__dirty-repo">
      {total > 0 ? (
        <>
          <p className="completion-workspace__dirty-repo-name">{`${repo.repo} — ${total} uncommitted ${total === 1 ? 'file' : 'files'} will be committed and pushed:`}</p>
          <ul className="completion-workspace__dirty-files">
            {files.map((path) => (
              <li key={path}>
                <code>{path}</code>
              </li>
            ))}
          </ul>
          {files.length < total ? (
            <p className="completion-workspace__dirty-more">+{total - files.length} more</p>
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
}
