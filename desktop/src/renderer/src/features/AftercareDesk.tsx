import type { FeatureSnapshot, RunDetailView } from '../../../shared/ipc';
import { formatDuration } from './featureView';
import {
  aftercareHeadline,
  aftercareRepositories,
  availableAftercareCycles,
  type AftercareCycleId,
} from './aftercareModel';

export interface AftercareDeskProps {
  snapshot: FeatureSnapshot;
  run: RunDetailView | null;
  onOpenCycle(id: AftercareCycleId): void;
}

export function AftercareDesk({
  snapshot,
  run,
  onOpenCycle,
}: AftercareDeskProps): React.ReactElement {
  const headline = aftercareHeadline(snapshot.status);
  const cycles = availableAftercareCycles(snapshot);
  const repositories = aftercareRepositories(snapshot);
  const duration = run?.timing?.totalSeconds;
  const cost = run?.cost?.totalUsd;

  return (
    <section className="aftercare" aria-label="Feature aftercare">
      <header className="aftercare__handoff">
        <div className="aftercare__handoff-copy">
          <p className="aftercare__overline">
            <span>Aftercare</span>
            <span aria-hidden="true">/</span>
            <span>Run {snapshot.activeRun}</span>
          </p>
          <h2>{headline.heading}</h2>
          <p className="aftercare__introduction">{headline.description}</p>
        </div>
        <div className="aftercare__state" aria-label={`Current state: ${headline.statusLabel}`}>
          <span className="aftercare__state-mark" aria-hidden="true">
            ✓
          </span>
          <span>
            <strong>{headline.statusLabel}</strong>
            <small>No agent activity</small>
          </span>
        </div>
      </header>

      <div className="aftercare__instrument">
        <section className="aftercare__runway" aria-labelledby="aftercare-runway-title">
          <div className="aftercare__section-heading">
            <div>
              <p className="cockpit__eyebrow">Available cycles</p>
              <h3 id="aftercare-runway-title">Maintenance runway</h3>
            </div>
            <span>{cycles.length === 0 ? 'At rest' : `${cycles.length} ready`}</span>
          </div>

          {cycles.length === 0 ? (
            <p className="aftercare__empty">No maintenance cycle is available from this state.</p>
          ) : (
            <ol className="aftercare__cycle-list">
              {cycles.map((cycle, index) => (
                <li key={cycle.id}>
                  <button
                    type="button"
                    className="aftercare__cycle"
                    data-cycle={cycle.id}
                    aria-label={`${cycle.verb}: ${cycle.title}`}
                    onClick={() => onOpenCycle(cycle.id)}
                  >
                    <span className="aftercare__cycle-index" aria-hidden="true">
                      {String(index + 1).padStart(2, '0')}
                    </span>
                    <span className="aftercare__cycle-copy">
                      <strong>{cycle.title}</strong>
                      <small>{cycle.description}</small>
                    </span>
                    <span className="aftercare__cycle-scope">{cycle.scope}</span>
                    <span className="aftercare__cycle-verb">
                      {cycle.verb}
                      <span aria-hidden="true"> ↗</span>
                    </span>
                  </button>
                </li>
              ))}
            </ol>
          )}
        </section>

        <section className="aftercare__ledger" aria-label="Run ledger">
          <div className="aftercare__section-heading">
            <div>
              <p className="cockpit__eyebrow">Completed run</p>
              <h3>Run ledger</h3>
            </div>
            <span>#{snapshot.activeRun}</span>
          </div>
          <dl className="aftercare__ledger-grid">
            <div>
              <dt>Duration</dt>
              <dd>{duration === undefined ? '—' : formatDuration(duration)}</dd>
            </div>
            <div>
              <dt>Spend</dt>
              <dd>{cost === undefined ? '—' : `$${cost.toFixed(2)}`}</dd>
            </div>
            <div>
              <dt>Artifacts</dt>
              <dd>{run?.artifactCount ?? '—'}</dd>
            </div>
            <div>
              <dt>Outcome</dt>
              <dd>{headline.statusLabel}</dd>
            </div>
          </dl>
          <p className="aftercare__ledger-note">
            The transcript, trace, artifacts, and bounded logs remain available in Run record.
          </p>
        </section>
      </div>

      <section className="aftercare__repositories" aria-labelledby="aftercare-repositories-title">
        <div className="aftercare__section-heading">
          <div>
            <p className="cockpit__eyebrow">Current handoff</p>
            <h3 id="aftercare-repositories-title">Repository readiness</h3>
          </div>
          <span>
            {repositories.length} {repositories.length === 1 ? 'repository' : 'repositories'}
          </span>
        </div>
        <ul className="aftercare__repo-list">
          {repositories.map((repository) => (
            <li
              key={repository.name}
              className="aftercare__repo"
              aria-label={`${repository.name} readiness`}
            >
              <code>{repository.name}</code>
              <span className="aftercare__repo-fact" data-tone={freshnessTone(repository.freshness)}>
                <span aria-hidden="true">●</span> {repository.freshness}
              </span>
              {repository.prUrl === undefined ? (
                <span className="aftercare__repo-fact">{repository.pullRequest}</span>
              ) : (
                <a
                  href={repository.prUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="aftercare__repo-link"
                >
                  {repository.pullRequest}
                </a>
              )}
              <span className="aftercare__repo-fact">{repository.publishability}</span>
              {repository.cycle === undefined ? null : (
                <span className="aftercare__repo-cycle">{repository.cycle}</span>
              )}
            </li>
          ))}
        </ul>
      </section>
    </section>
  );
}

function freshnessTone(freshness: string): 'healthy' | 'attention' | 'muted' {
  const normalized = freshness.toLowerCase();
  if (normalized === 'in sync' || normalized === 'up to date') return 'healthy';
  if (normalized.includes('unavailable') || normalized === 'unknown') return 'muted';
  return 'attention';
}
