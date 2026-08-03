import type { FeatureSnapshot, RunDetailView } from '../../../shared/ipc';
import { displayStatusLabel, featureBranch, formatDuration } from './featureView';

export interface FeatureFactsRailProps {
  snapshot: FeatureSnapshot;
  run: RunDetailView | null;
  onOpenPullRequest(url: string): void;
}

export function FeatureFactsRail({
  snapshot,
  run,
  onOpenPullRequest,
}: FeatureFactsRailProps): React.ReactElement {
  const branch = featureBranch(snapshot);
  const repository = snapshot.repoStatus?.[0];
  const elapsed = run?.timing?.totalSeconds ?? snapshot.timing?.totalSeconds;
  const aftercarePasses = [
    ...(snapshot.activeChild === undefined ? [] : [snapshot.activeChild]),
    ...(snapshot.childHistory ?? []),
  ];
  const hasCost = run?.cost !== undefined || aftercarePasses.length > 0;
  const cost = hasCost
    ? (run?.cost?.totalUsd ?? 0) +
      aftercarePasses.reduce((total, pass) => total + pass.cost.totalUsd, 0)
    : undefined;
  const prUrl = repository?.prUrl;

  return (
    <aside className="feature-facts" aria-label="Feature facts">
      <p className="feature-facts__eyebrow">Feature facts</p>
      <dl className="feature-facts__list">
        <Fact label="Status" value={displayStatusLabel(snapshot.status)} />
        <Fact
          label={snapshot.repos.length === 1 ? 'Repository' : 'Repositories'}
          value={snapshot.repos.join(', ')}
          mono
        />
        {branch === null ? null : <Fact label="Branch" value={branch} mono />}
        <Fact label="Run" value={`#${snapshot.activeRun}`} mono />
        <Fact label="Elapsed" value={elapsed === undefined ? '—' : formatDuration(elapsed)} mono />
        <Fact label="Cost" value={cost === undefined ? '—' : `$${cost.toFixed(2)}`} mono />
        {prUrl === undefined ? null : (
          <div className="feature-facts__fact">
            <dt>Pull request</dt>
            <dd>
              <button
                type="button"
                className="feature-facts__link"
                onClick={() => onOpenPullRequest(prUrl)}
              >
                Open pull request <span aria-hidden="true">↗</span>
              </button>
            </dd>
          </div>
        )}
        <Fact
          label="Freshness"
          value={
            repository?.freshness === undefined ? 'Unavailable' : sentenceCase(repository.freshness)
          }
        />
      </dl>
    </aside>
  );
}

function Fact({
  label,
  value,
  mono = false,
}: {
  label: string;
  value: string;
  mono?: boolean;
}): React.ReactElement {
  return (
    <div className="feature-facts__fact">
      <dt>{label}</dt>
      <dd>{mono ? <code>{value}</code> : value}</dd>
    </div>
  );
}

function sentenceCase(value: string): string {
  const normalized = value.replace(/[_-]+/g, ' ').trim();
  return normalized === '' ? value : normalized.charAt(0).toUpperCase() + normalized.slice(1);
}
