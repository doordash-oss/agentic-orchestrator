/**
 * Repository instrument panel: compact per-repository operational status
 * rendered from the server-authored feature detail. Shows publishability,
 * PR, freshness, active cycle/count/status, target branch, conflicts, and
 * safe failure details. Used as the cockpit inspector's operational context.
 */
import type { RepoStatusView } from '../../../shared/ipc';

const FRESHNESS_LABELS: Record<string, string> = {
  'in sync': 'In sync',
  'local changes': 'Local changes',
  'local only': 'Local only',
  unknown: 'Unknown',
};

const CYCLE_STATUS_LABELS: Record<string, string> = {
  running: 'Running',
  reviewing: 'Reviewing',
  need_user_input: 'Needs input',
  failed: 'Failed',
  completed: 'Completed',
};

export const REBASE_STATUS_LABELS: Record<string, string> = {
  checking: 'Checking',
  rebasing: 'Rebasing',
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
}

export function RepositoryInstrument({ repos, onOpenPullRequest }: RepositoryInstrumentProps) {
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
              {repo.cycleType !== undefined ? (
                <div className="repo-instrument__fact" data-cycle={repo.cycleType}>
                  <dt>Cycle</dt>
                  <dd>
                    <code>{repo.cycleType}</code>
                    {repo.cycleStatus !== undefined ? (
                      <span
                        className="repo-instrument__cycle-status"
                        data-status={repo.cycleStatus}
                      >
                        {' '}
                        {CYCLE_STATUS_LABELS[repo.cycleStatus] ?? repo.cycleStatus}
                      </span>
                    ) : null}
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
              {repo.lastError !== undefined ? (
                <div className="repo-instrument__fact repo-instrument__error" role="alert">
                  <dt>Error</dt>
                  <dd>{repo.lastError}</dd>
                </div>
              ) : null}
            </dl>
          </li>
        ))}
      </ul>
    </section>
  );
}
