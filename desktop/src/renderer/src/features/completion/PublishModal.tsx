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
  FeatureActionView,
  PullRequestEntryView,
  PublishFamilyActionRequest,
} from '../../../../shared/ipc';
import { E_REQUEST_TIMEOUT, buildCanonicalError } from '../../../../shared/errors';
import { ErrorSurface, type ErrorSurfaceAction } from '../../components/ErrorSurface';
import { useModalDismiss } from '../../components/useModalDismiss';
import { sentenceCase } from '../aftercareReceipt';
import { catalogErrorAction, errorLayerContext } from '../featureView';
import { parseIpcError } from '../../wizard/ipcError';
import type { CanonicalError } from '../../../../shared/ipc';
import { PrLinkButton, isEligibleForPublish } from './completionShared';
import { UNPUBLISHED_CHANGES, pendingDeliveryDetail } from './pendingDelivery';

const PUBLISH_TIMEOUT_LOCKED_MESSAGE =
  'Publish may still be running. Quit and reopen Agentico before publishing again.';
const PUBLISH_ACTION_ID = 'publish';
const REOPEN_ACTION_ID = 'reopen-pull-request';
const RECREATE_ACTION_ID = 'recreate-pull-request';

type LayerActionId = typeof REOPEN_ACTION_ID | typeof RECREATE_ACTION_ID;

/** The remediation action ids a repository row card knows how to resolve. */
const RESOLVABLE_ACTION_IDS: ReadonlySet<string> = new Set([
  PUBLISH_ACTION_ID,
  REOPEN_ACTION_ID,
  RECREATE_ACTION_ID,
]);

/**
 * The needs-action codes whose stored record parks a repository on a closed
 * stack layer pull request; their remediation offers the layer-scoped
 * resolutions.
 */
const CLOSED_PR_ERROR_CODES: ReadonlySet<string> = new Set([
  'publish_stack_pull_request_closed',
  'publish_reopen_failed',
  'publish_head_branch_missing',
  'publish_recreate_failed',
]);

/**
 * The publish timeout's canonical warning: the request outran its bound and
 * the operation may still complete server-side, so the sheet stays disarmed
 * and the quit-and-reopen guidance rides as the remediation hint.
 */
function publishTimeoutLockedError(): CanonicalError {
  return buildCanonicalError(E_REQUEST_TIMEOUT, {
    remediationHint: PUBLISH_TIMEOUT_LOCKED_MESSAGE,
  });
}

/**
 * A publish outcome is either success, the reconciling timeout state (the
 * mutation outran its request bound and is still running server-side), or a
 * rejection carrying the parsed IPC error so the compact ErrorSurface can
 * render the canonical object when the main process carried one.
 */
type PublishOutcome =
  | { ok: true; result: string }
  | { ok: false; reconciling: true; message: string }
  | { ok: false; reconciling: true; timeoutLocked: true }
  | { ok: false; reconciling?: undefined; error: CanonicalError };

export interface PublishModalProps {
  featureId: string;
  preflight: CompletionPreflightResult;
  /** The feature's server action catalog; each row card resolves `publish` in it. */
  actions: readonly FeatureActionView[];
  dispatchAction(request: PublishFamilyActionRequest): Promise<FeatureActionResult>;
  openExternal(url: string): Promise<{ ok: boolean }>;
  onDispatched(): void | Promise<void>;
  onClose(): void;
  publishTimeoutLocked: boolean;
  setPublishTimeoutLocked(locked: boolean): void;
}

function PublishStatusNotice({
  result,
  publishTimeoutLocked,
}: {
  result: PublishOutcome | null;
  publishTimeoutLocked: boolean;
}) {
  // Success and the in-flight reconciliation stay plain status lines; the
  // timeout-locked state is a failure condition and renders as the
  // E_REQUEST_TIMEOUT canonical warning through a compact ErrorSurface.
  if (result === null) {
    if (!publishTimeoutLocked) return null;
    return <ErrorSurface error={publishTimeoutLockedError()} variant="compact" />;
  }
  if (result.ok) {
    return (
      <div className="completion-publish-sheet__status" role="status">
        {result.result}
      </div>
    );
  }
  if (result.reconciling === true) {
    if ('timeoutLocked' in result) {
      return <ErrorSurface error={publishTimeoutLockedError()} variant="compact" />;
    }
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
  actions,
  dispatchAction,
  openExternal,
  onDispatched,
  onClose,
  publishTimeoutLocked,
  setPublishTimeoutLocked,
}: PublishModalProps): React.ReactElement {
  const dialogRef = useRef<HTMLDivElement>(null);
  const failureRef = useRef<HTMLDivElement>(null);
  const [publishRepos, setPublishRepos] = useState<Set<string>>(
    () =>
      new Set(
        preflight.repos
          .filter((repo) => isEligibleForPublish(repo) || repo.status === UNPUBLISHED_CHANGES)
          .map((repo) => repo.repo),
      ),
  );
  const [publishBusy, setPublishBusy] = useState(false);
  const [timedOutThisOpen, setTimedOutThisOpen] = useState(false);
  const [publishResult, setPublishResult] = useState<PublishOutcome | null>(null);
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
    (dirtySelected.length === 0 || commitConfirmed) &&
    !publishBusy &&
    !publishLocked;

  // The repository row cards own publish-failure presentation: once any
  // selected repository carries a stored record, a rejected publish renders
  // no whole-sheet rejection notice.
  const selectedRepoCarriesError = useMemo(
    () => preflight.repos.some((repo) => publishRepos.has(repo.repo) && repo.error !== undefined),
    [preflight, publishRepos],
  );

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
    if (publishResult !== null && !publishResult.ok && publishResult.reconciling !== true) {
      failureRef.current?.focus();
    }
  }, [publishResult]);

  const togglePublishRepo = useCallback((repo: string) => {
    setPublishRepos((previous) => {
      const next = new Set(previous);
      if (next.has(repo)) next.delete(repo);
      else next.add(repo);
      return next;
    });
  }, []);

  const runActionRequest = useCallback(
    async (request: PublishFamilyActionRequest) => {
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
            reconciling: true,
            message: 'Publish may still be running. Refreshing the latest publish state…',
          });
          try {
            await onDispatched();
          } catch {
            // Refresh hooks can deliberately absorb their own transport errors.
          }
          setPublishResult({ ok: false, reconciling: true, timeoutLocked: true });
        } else {
          try {
            await onDispatched();
          } catch {
            // Preserve the publish failure when the best-effort refresh also fails.
          }
          setPublishResult({ ok: false, error: parsed });
        }
      } finally {
        setPublishBusy(false);
      }
    },
    [dispatchAction, onDispatched, setPublishTimeoutLocked],
  );

  const runPublish = useCallback(
    (repos: string[]) =>
      runActionRequest({
        featureId,
        action: 'publish',
        body: {
          source_revision: preflight.sourceRevision,
          repos,
        },
      }),
    [featureId, preflight.sourceRevision, runActionRequest],
  );

  const handlePublish = useCallback(async () => {
    if (!canPublish) return;
    await runPublish(Array.from(publishRepos));
  }, [canPublish, publishRepos, runPublish]);

  const handleRetryPublish = useCallback(
    async (repo: PublishRepo) => {
      // The retry button is disabled under these preconditions; the guard
      // keeps a stale dispatch from racing a just-changed form.
      if (publishBusy || publishLocked || preflight.sourceRevision.trim() === '') return;
      await runPublish([repo.repo]);
    },
    [preflight.sourceRevision, publishLocked, publishBusy, runPublish],
  );

  // Reopen and recreate dispatch with the repository and layer read from the
  // row's own error context and the preflight's source revision, then refresh
  // exactly as retry publish does — the same runner owns busy, timeout lock,
  // and the post-dispatch preflight refresh.
  const handleLayerAction = useCallback(
    async (repo: PublishRepo, action: LayerActionId) => {
      if (publishBusy || publishLocked || preflight.sourceRevision.trim() === '') return;
      const layer = closedPullRequestLayerOf(repo);
      if (layer === null) return;
      const body = {
        repository: layer.repository,
        layer: layer.layer,
        source_revision: preflight.sourceRevision,
      };
      await runActionRequest(
        action === REOPEN_ACTION_ID
          ? { featureId, action: 'reopen-pull-request', body }
          : { featureId, action: 'recreate-pull-request', body },
      );
    },
    [featureId, preflight.sourceRevision, publishLocked, publishBusy, runActionRequest],
  );

  // One action handler per row card: the layer-scoped resolutions dispatch
  // their own typed requests; every other resolved id (publish) retries only
  // this repository.
  const rowActionHandler = useCallback(
    (repo: PublishRepo) => (actionId: string) => {
      if (actionId === REOPEN_ACTION_ID || actionId === RECREATE_ACTION_ID) {
        void handleLayerAction(repo, actionId);
        return;
      }
      void handleRetryPublish(repo);
    },
    [handleLayerAction, handleRetryPublish],
  );

  // One resolver per row card: the labels are fixed, the enabled state is the
  // catalog's state plus the modal's own preconditions, and the disabled
  // reason reports whichever blocks it. The layer-scoped resolutions are
  // offered only when the row's own error is a closed-PR family record whose
  // context names the failing layer.
  const retryActionFor = useCallback(
    (repo: PublishRepo) => {
      const modalReason =
        publishBusy || publishLocked
          ? 'A publish is already running.'
          : preflight.sourceRevision.trim() === ''
            ? 'Refresh the preflight, then retry.'
            : undefined;
      const layer = closedPullRequestLayerOf(repo);
      return (actionId: string): ErrorSurfaceAction | undefined => {
        if (!RESOLVABLE_ACTION_IDS.has(actionId)) return undefined;
        if (actionId !== PUBLISH_ACTION_ID && layer === null) return undefined;
        const label =
          actionId === REOPEN_ACTION_ID
            ? 'Reopen pull request'
            : actionId === RECREATE_ACTION_ID
              ? 'Recreate pull request'
              : 'Retry publish';
        const base = catalogErrorAction({ actions }, actionId, label);
        if (base === undefined) return undefined;
        const reason = base.enabled ? modalReason : base.disabledReason;
        return {
          ...base,
          enabled: base.enabled && modalReason === undefined,
          ...(reason === undefined ? {} : { disabledReason: reason }),
        };
      };
    },
    [actions, preflight.sourceRevision, publishBusy, publishLocked],
  );

  const rejection =
    publishResult !== null && !publishResult.ok && publishResult.reconciling !== true
      ? publishResult.error
      : null;

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
                Agentico generates each pull request's narrative from its own changes.
              </p>
            </div>
            <div className="completion-workspace__publish-repos">
              {eligibleRepos.map((repo) => (
                <PublishRepoRow
                  key={repo.repo}
                  repo={repo}
                  featureId={featureId}
                  checked={publishRepos.has(repo.repo)}
                  onToggle={togglePublishRepo}
                  openExternal={openExternal}
                  resolveAction={retryActionFor(repo)}
                  onAction={rowActionHandler(repo)}
                />
              ))}
              {unpublishedRepos.length > 0 ? (
                <div className="completion-workspace__pending-repos">
                  <h4>Unpublished changes</h4>
                  {unpublishedRepos.map((repo) => (
                    <PublishRepoRow
                      key={repo.repo}
                      repo={repo}
                      featureId={featureId}
                      checked={publishRepos.has(repo.repo)}
                      onToggle={togglePublishRepo}
                      openExternal={openExternal}
                      resolveAction={retryActionFor(repo)}
                      onAction={rowActionHandler(repo)}
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
                <div className="completion-workspace__published-repos">
                  <h4>Already published</h4>
                  {publishedRepos.map((repo) => (
                    <PublishRepoRow
                      key={repo.repo}
                      repo={repo}
                      featureId={featureId}
                      checked={publishRepos.has(repo.repo)}
                      onToggle={togglePublishRepo}
                      openExternal={openExternal}
                      resolveAction={retryActionFor(repo)}
                      onAction={rowActionHandler(repo)}
                      selectable={false}
                    />
                  ))}
                </div>
              ) : null}
              {ineligibleRepos.length > 0 ? <IneligibleRepoGroup repos={ineligibleRepos} /> : null}
            </div>
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
            {rejection !== null && !selectedRepoCarriesError ? (
              <ErrorSurface
                error={rejection}
                variant="compact"
                caption="Publish was rejected"
                rootRef={failureRef}
                rootTabIndex={-1}
              />
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
            {publishBusy ? 'Publishing…' : publishLocked ? 'Reconciling…' : 'Publish updates'}
          </button>
        </footer>
      </div>
    </div>
  );
}

type PublishRepo = CompletionPreflightResult['repos'][number];

/**
 * The closed stack layer the row's own error names: the repository and layer
 * position read from a closed-PR family record's repository context. Null
 * when the row carries no such record or its context names no layer, in
 * which case the layer-scoped resolutions are not offerable.
 */
function closedPullRequestLayerOf(repo: PublishRepo): { repository: string; layer: number } | null {
  const error = repo.error;
  if (error === undefined || !CLOSED_PR_ERROR_CODES.has(error.code)) return null;
  return errorLayerContext(error);
}

/**
 * The publish row's freshness line: the server's freshness phrase with the
 * rebase hint appended, joined by an em dash — nothing when the preflight
 * carries neither. The hint is advisory text in the sheet's own voice, never
 * error markup (ErrorSurface owns that).
 */
function repoFreshnessHint(repo: PublishRepo): string | null {
  const parts: string[] = [];
  if (repo.freshness !== undefined) parts.push(sentenceCase(repo.freshness));
  if (repo.rebaseHint !== undefined) parts.push(repo.rebaseHint);
  return parts.length === 0 ? null : parts.join(' — ');
}

/**
 * The publish verb for one stack layer: the no-commits marker and merged/closed
 * states speak for themselves, a carried push mode is quoted verbatim, and a
 * push-mode-less entry (a snapshot-sourced preview) derives from the recorded
 * state — an open, not-pushed-up-to-date PR reads as an update.
 */
function stackPreviewVerb(entry: PullRequestEntryView): string {
  if (entry.noCommits) return 'no changes in this repository';
  if (entry.state === 'merged') return 'merged';
  if (entry.state === 'closed') return 'closed';
  switch (entry.pushMode) {
    case 'create':
      return 'will create';
    case 'fast_forward':
      return 'will update';
    case 'rewrite':
      return 'will rewrite';
    case 'none':
      return 'up to date';
    default:
      break;
  }
  if (entry.state === 'open') return entry.pushedUpToDate ? 'up to date' : 'will update';
  return 'will create';
}

function stackPreviewVerbKey(verb: string): string {
  return verb.replaceAll(' ', '-');
}

/**
 * The per-repository stack preview: one line per layer with its position,
 * title, and publish verb, linking the layers that already have a PR.
 */
function StackPreview({
  entries,
  openExternal,
}: {
  entries: PullRequestEntryView[];
  openExternal(url: string): Promise<{ ok: boolean }>;
}) {
  if (entries.length === 0) return null;
  return (
    <ul className="completion-workspace__stack-preview">
      {entries.map((entry) => {
        const verb = stackPreviewVerb(entry);
        return (
          <li key={entry.position} className="completion-workspace__stack-preview-line">
            <span className="completion-workspace__stack-preview-position">
              Layer {entry.position}
            </span>
            <span className="completion-workspace__stack-preview-title">{entry.title}</span>
            <span
              className="completion-workspace__stack-preview-verb"
              data-verb={stackPreviewVerbKey(verb)}
            >
              {verb}
            </span>
            {entry.url === undefined ? null : (
              <PrLinkButton url={entry.url} openExternal={openExternal} />
            )}
          </li>
        );
      })}
    </ul>
  );
}

function PublishRepoRow({
  repo,
  featureId,
  checked,
  onToggle,
  openExternal,
  resolveAction,
  onAction,
  selectable = true,
}: {
  repo: PublishRepo;
  featureId: string;
  checked: boolean;
  onToggle(repo: string): void;
  openExternal(url: string): Promise<{ ok: boolean }>;
  resolveAction(actionId: string): ErrorSurfaceAction | undefined;
  onAction(actionId: string): void;
  /**
   * False for rows the modal cannot select for publish (already published):
   * no checkbox, but the full row — stack preview, freshness with its rebase
   * hint, and the canonical error with its catalog-resolved actions — still
   * renders, because a parked repository needs its recovery controls
   * regardless of publish eligibility.
   */
  selectable?: boolean;
}): React.ReactElement {
  const freshnessHint = repoFreshnessHint(repo);
  return (
    <div className="completion-workspace__publish-repo">
      <div className="completion-workspace__publish-repo-main">
        {selectable ? (
          <input
            type="checkbox"
            aria-label={repo.repo}
            checked={checked}
            onChange={() => onToggle(repo.repo)}
          />
        ) : null}
        <span className="completion-workspace__publish-repo-name">{repo.repo}</span>
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
        {freshnessHint === null ? null : (
          <span className="completion-workspace__freshness">{freshnessHint}</span>
        )}
      </div>
      <StackPreview entries={repo.pullRequests ?? []} openExternal={openExternal} />
      {repo.pushMode === 'rewrite' ? (
        <p className="completion-workspace__pending-note">
          Rewrites the pull-request branch with a safety lease.
        </p>
      ) : null}
      {repo.error !== undefined ? (
        <ErrorSurface
          error={repo.error}
          resolveAction={resolveAction}
          onAction={onAction}
          explain={{
            // The stored publish-failure record lives on the repository's
            // own state; the modal has no feature name in scope, so the
            // question names only the card's title and code.
            reference: {
              scope: 'repository',
              code: repo.error.code,
              featureId,
              repository: repo.repo,
            },
          }}
        />
      ) : null}
    </div>
  );
}

function IneligibleRepoGroup({ repos }: { repos: PublishRepo[] }) {
  return (
    <div className="completion-workspace__ineligible-repos">
      <h4>Not publishable</h4>
      {repos.map((repo) => (
        <div key={repo.repo} className="completion-workspace__ineligible-repo-row">
          <span>{repo.repo}</span>
          <span className="completion-workspace__ineligible-repo-reason">
            {repo.blocker ?? 'Local-only repository'}
          </span>
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
