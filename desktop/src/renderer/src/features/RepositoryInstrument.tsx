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

/**
 * Repository instrument panel: compact per-repository operational status
 * rendered from the server-authored feature detail. Shows publishability,
 * PR, freshness, rebase status, target branch, conflicts, and a publish
 * failure indication that links into the publish modal. Used as the cockpit
 * inspector's operational context.
 */
import type { RepoStatusView } from '../../../shared/ipc';

const FRESHNESS_LABELS: Record<string, string> = {
  'in sync': 'In sync',
  'local changes': 'Local changes',
  'local only': 'Local only',
  unknown: 'Unknown',
};

const REBASE_STATUS_LABELS: Record<string, string> = {
  checking: 'Checking',
  up_to_date: 'Up to date',
  changed: 'Changed',
  pending: 'Pending',
  running: 'Running',
  conflict: 'Conflict',
  failed: 'Failed',
  completed: 'Completed',
  interrupted: 'Interrupted',
};

export interface RepositoryInstrumentProps {
  repos: RepoStatusView[];
  onOpenPullRequest(url: string): void;
  /**
   * Opens the publish modal, whose repository row owns the full error card.
   * Absent on surfaces that host no publish modal; the indication then
   * renders the title without the link.
   */
  onOpenPublish?(): void;
}

export function RepositoryInstrument({
  repos,
  onOpenPullRequest,
  onOpenPublish,
}: RepositoryInstrumentProps) {
  if (repos.length === 0) return null;

  return (
    <section className="cockpit__repo-instrument" aria-label="Repository status">
      <h4 className="repo-instrument__title">Repositories</h4>
      <ul className="repo-instrument__list">
        {repos.map((repo) => (
          <li key={repo.name} className="repo-instrument__item" data-repo={repo.name}>
            <header className="repo-instrument__header">
              <code className="repo-instrument__name">{repo.name}</code>
              <span
                className="repo-instrument__publishable"
                data-publishable={repo.publishable}
                aria-label={repo.publishable ? 'Publishable' : 'Local only'}
              >
                {repo.publishable ? '●' : '○'}
              </span>
            </header>
            <dl className="repo-instrument__facts">
              {repo.freshness !== undefined ? (
                <div className="repo-instrument__fact">
                  <dt>Freshness</dt>
                  <dd>{FRESHNESS_LABELS[repo.freshness] ?? repo.freshness}</dd>
                </div>
              ) : null}
              {repo.prUrl !== undefined ? (
                <div className="repo-instrument__fact">
                  <dt>PR</dt>
                  <dd>
                    <button
                      type="button"
                      className="repo-instrument__pr-link"
                      aria-label="Open pull request"
                      onClick={() => onOpenPullRequest(repo.prUrl!)}
                    >
                      {repo.prUrl}
                    </button>
                  </dd>
                </div>
              ) : null}
              {repo.rebaseStatus !== undefined ? (
                <div className="repo-instrument__fact" data-rebase={repo.rebaseStatus}>
                  <dt>Rebase</dt>
                  <dd>
                    {REBASE_STATUS_LABELS[repo.rebaseStatus] ?? repo.rebaseStatus}
                    {repo.rebaseTarget !== undefined ? (
                      <code className="repo-instrument__rebase-target"> → {repo.rebaseTarget}</code>
                    ) : null}
                  </dd>
                </div>
              ) : null}
              {repo.conflictFiles !== undefined && repo.conflictFiles.length > 0 ? (
                <div className="repo-instrument__fact repo-instrument__conflicts">
                  <dt>Conflicts</dt>
                  <dd>
                    <ul className="repo-instrument__conflict-list">
                      {repo.conflictFiles.map((file) => (
                        <li key={file} className="repo-instrument__conflict-file">
                          <code>{file}</code>
                        </li>
                      ))}
                    </ul>
                  </dd>
                </div>
              ) : null}
            </dl>
            {repo.error !== undefined ? (
              <div className="repo-instrument__publish-attention" data-repo={repo.name}>
                <span className="repo-instrument__publish-attention-title">{repo.error.title}</span>
                {onOpenPublish !== undefined ? (
                  <button
                    type="button"
                    className="repo-instrument__open-publish"
                    onClick={onOpenPublish}
                  >
                    Open publish
                  </button>
                ) : null}
              </div>
            ) : null}
          </li>
        ))}
      </ul>
    </section>
  );
}
