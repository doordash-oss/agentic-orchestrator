import type { RepoStatusView } from '../../../shared/ipc';
import { REBASE_STATUS_LABELS } from './RepositoryInstrument';

export function RebaseOperationsStage({
  repos,
}: {
  repos: readonly RepoStatusView[];
}): React.ReactElement {
  const operations = repos.filter(
    (repo) =>
      repo.rebaseStatus !== undefined ||
      repo.rebaseTarget !== undefined ||
      repo.conflictFiles !== undefined ||
      repo.lastError !== undefined,
  );
  return (
    <div className="live-preview cycle-workspace__operations" aria-label="Rebase operations">
      {operations.length === 0 ? (
        <p className="setup-step__empty">Preparing repository checks…</p>
      ) : (
        <ul>
          {operations.map((repo) => (
            <li key={repo.name} data-status={repo.rebaseStatus ?? 'checking'}>
              <div className="cycle-workspace__operation-heading">
                <code>{repo.name}</code>
                <strong>
                  {REBASE_STATUS_LABELS[repo.rebaseStatus ?? 'checking'] ??
                    repo.rebaseStatus ??
                    'Checking'}
                </strong>
              </div>
              {repo.rebaseTarget === undefined ? null : (
                <p>
                  Target <code>→ {repo.rebaseTarget}</code>
                </p>
              )}
              {repo.conflictFiles === undefined || repo.conflictFiles.length === 0 ? null : (
                <ul
                  className="cycle-workspace__operation-conflicts"
                  aria-label={`${repo.name} conflicts`}
                >
                  {repo.conflictFiles.map((file) => (
                    <li key={file}>
                      <code>{file}</code>
                    </li>
                  ))}
                </ul>
              )}
              {repo.lastError === undefined ? null : (
                <p className="cycle-workspace__operation-error" role="alert">
                  {repo.lastError}
                </p>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
