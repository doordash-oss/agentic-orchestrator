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

import type { FeatureSnapshot } from '../../../shared/ipc';
import type { RunMetrics } from './CurrentRunInspection';
import { RepositoryInstrument } from './RepositoryInstrument';
import { displayStatusLabel, formatDuration } from './featureView';

export function IdentityFacts({
  snapshot,
  branch,
}: {
  snapshot: FeatureSnapshot;
  branch: string | null;
}) {
  return (
    <dl className="cockpit__facts">
      <div className="cockpit__fact">
        <dt>Status</dt>
        <dd aria-label={snapshot.status}>
          <code data-status={snapshot.status}>{displayStatusLabel(snapshot.status)}</code>
        </dd>
      </div>
      {branch !== null ? (
        <div className="cockpit__fact">
          <dt>Branch</dt>
          <dd>
            <code>{branch}</code>
          </dd>
        </div>
      ) : null}
    </dl>
  );
}

/** The inspector rail: identity and run totals. The phase rail above owns pipeline presentation. */
export function InspectorContent({
  snapshot,
  branch,
  stale,
  runMetrics,
  onOpenPullRequest,
  onOpenPublish,
}: {
  snapshot: FeatureSnapshot;
  branch: string | null;
  stale: boolean;
  runMetrics: RunMetrics | null;
  onOpenPullRequest(url: string): void;
  onOpenPublish?(): void;
}) {
  return (
    <>
      <header className="cockpit__header">
        <div className="cockpit__identity">
          <h2 className="cockpit__title">{snapshot.name}</h2>
          <IdentityFacts snapshot={snapshot} branch={branch} />
        </div>
        {stale ? (
          <p role="status" className="cockpit__stale">
            Refreshing from the runtime…
          </p>
        ) : null}
      </header>
      {runMetrics !== null ? (
        <section className="cockpit__run-totals" aria-label="This run">
          <h3 className="setup-step__title">This run</h3>
          <dl className="cockpit__facts">
            <div className="cockpit__fact">
              <dt>Total elapsed</dt>
              <dd>
                <code>{formatDuration(runMetrics.totalSeconds)}</code>
              </dd>
            </div>
            <div className="cockpit__fact">
              <dt>Total cost</dt>
              <dd>
                <code>${runMetrics.totalUsd.toFixed(2)}</code>
              </dd>
            </div>
          </dl>
        </section>
      ) : null}
      {snapshot.repoStatus !== undefined && snapshot.repoStatus.length > 0 ? (
        <RepositoryInstrument
          repos={snapshot.repoStatus}
          onOpenPullRequest={onOpenPullRequest}
          onOpenPublish={onOpenPublish}
        />
      ) : null}
    </>
  );
}
