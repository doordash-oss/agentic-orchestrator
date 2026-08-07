import type { FeatureSnapshot, RunDetailView } from '../../../shared/ipc';
import { displayStatusLabel, featureBranch, formatDuration } from './featureView';
import { sentenceCase } from './aftercareReceipt';

export interface AftercareFactsProps {
  snapshot: FeatureSnapshot;
  run: RunDetailView | null;
  pendingFact?: { label: string; value: string };
  /** Rendered above the facts; omitted where the presentation already has a header. */
  title?: string;
  onOpenPullRequest(url: string): void;
}

/**
 * The aftercare inspector pane's content: the facts the always-open rail used
 * to hold, computed exactly as before, now behind the toolbar's inspector
 * toggle (trailing pane when wide, drawer when narrow) so the reading column
 * owns the full width by default.
 */
export function AftercareFacts({
  snapshot,
  run,
  pendingFact,
  title,
  onOpenPullRequest,
}: AftercareFactsProps): React.ReactElement {
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
    <section className="aftercare-facts" aria-label="Feature facts">
      {title === undefined ? null : <h3 className="aftercare-facts__title">{title}</h3>}
      <dl className="aftercare-facts__list">
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
          <div className="aftercare-facts__fact">
            <dt>Pull request</dt>
            <dd>
              <button
                type="button"
                className="aftercare-facts__link"
                onClick={() => onOpenPullRequest(prUrl)}
              >
                Open pull request <span aria-hidden="true">↗</span>
              </button>
            </dd>
          </div>
        )}
        {pendingFact === undefined ? null : (
          <Fact label={pendingFact.label} value={pendingFact.value} />
        )}
        <Fact
          label="Freshness"
          value={
            repository?.freshness === undefined ? 'Unavailable' : sentenceCase(repository.freshness)
          }
        />
      </dl>
    </section>
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
    <div className="aftercare-facts__fact">
      <dt>{label}</dt>
      <dd>{mono ? <code>{value}</code> : value}</dd>
    </div>
  );
}
