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

import type { FeatureSnapshot, PullRequestEntryView, RunDetailView } from '../../../shared/ipc';
import { displayStatusLabel, featureBranch, formatDuration } from './featureView';
import { sentenceCase } from './aftercareReceipt';

function publishedEntries(repo: { pullRequests?: PullRequestEntryView[] }): PullRequestEntryView[] {
  return (repo.pullRequests ?? []).filter((entry) => entry.url !== undefined && entry.url !== '');
}

export interface AftercareFactsProps {
  snapshot: FeatureSnapshot;
  run: RunDetailView | null;
  pendingFact?: { label: string; value: string };
  /**
   * Server-authored rebase hint from the completion preflight, present when a
   * merged layer whose entry still holds a tip sits below kept work with
   * commits. Rendered beside the freshness fact; omitted when absent.
   */
  rebaseHint?: string;
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
  rebaseHint,
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
  // The headline link: the highest layer's PR (the last entry with a URL) of
  // the first repository that has one, plus the count of published PRs across
  // every repository. Nothing renders when no repository has a PR.
  const publishedEntriesByRepo = (snapshot.repoStatus ?? []).map(publishedEntries);
  const pullRequestCount = publishedEntriesByRepo.reduce(
    (total, entries) => total + entries.length,
    0,
  );
  const pullRequestRepoCount = publishedEntriesByRepo.reduce(
    (total, entries) => (entries.length > 0 ? total + 1 : total),
    0,
  );
  const firstRepoWithPullRequests = publishedEntriesByRepo.find((entries) => entries.length > 0);
  const topPullRequestUrl = firstRepoWithPullRequests?.at(-1)?.url;
  const pullRequestRepoWord = pullRequestRepoCount === 1 ? 'repository' : 'repositories';
  const pullRequestCountPhrase =
    pullRequestCount > 1
      ? `${pullRequestCount} pull requests across ${pullRequestRepoCount} ${pullRequestRepoWord}`
      : null;

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
        {topPullRequestUrl === undefined ? null : (
          <div className="aftercare-facts__fact">
            <dt>Pull request</dt>
            <dd>
              <button
                type="button"
                className="aftercare-facts__link"
                onClick={() => onOpenPullRequest(topPullRequestUrl)}
              >
                Open pull request <span aria-hidden="true">↗</span>
              </button>
              {pullRequestCountPhrase === null ? null : (
                <span className="aftercare-facts__pr-count">{pullRequestCountPhrase}</span>
              )}
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
          {...(rebaseHint === undefined ? {} : { hint: rebaseHint })}
        />
      </dl>
    </section>
  );
}

function Fact({
  label,
  value,
  mono = false,
  hint,
}: {
  label: string;
  value: string;
  mono?: boolean;
  /** Server-authored rebase hint rendered under the fact's value. */
  hint?: string;
}): React.ReactElement {
  return (
    <div className="aftercare-facts__fact">
      <dt>{label}</dt>
      <dd>
        {mono ? <code>{value}</code> : value}
        {hint === undefined ? null : <p className="aftercare-facts__rebase-hint">{hint}</p>}
      </dd>
    </div>
  );
}
