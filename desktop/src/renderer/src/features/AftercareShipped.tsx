import type { ReactNode } from 'react';
import type {
  CompletionPreflightResult,
  FeatureSnapshot,
  RunDetailView,
} from '../../../shared/ipc';
import { archiveRailSegments } from './phaseRail';
import { PhaseRailTrack } from './PhaseRailRow';
import { spineStages } from './featureView';
import {
  changesFact,
  phasesFact,
  pullRequestRows,
  verificationFact,
  type ChangesFact,
} from './aftercareReceipt';
import type { AftercareEvidence } from './useAftercareEvidence';

export interface AftercareShippedProps {
  snapshot: FeatureSnapshot;
  run: RunDetailView | null;
  preflight: CompletionPreflightResult | null;
  evidence: AftercareEvidence;
  onOpenRunRecord(): void;
  onOpenChanges(): void;
  onOpenConfiguration(): void;
  onOpenPullRequest(url: string): void;
}

/**
 * The receipt for a finished run: one row per fact the read model carries,
 * each leading to the surface that holds the detail. Rows omit rather than
 * invent — no diff, no Changes row; no pull request, no PR rows — and the
 * Verification row always renders because it is this surface's one-click
 * anchor to the run record.
 */
export function AftercareShipped({
  snapshot,
  run,
  preflight,
  evidence,
  onOpenRunRecord,
  onOpenChanges,
  onOpenConfiguration,
  onOpenPullRequest,
}: AftercareShippedProps): React.ReactElement {
  const changes = changesFact(evidence.diffs, preflight);
  const verification = verificationFact(snapshot.verificationItems);
  const prRows = pullRequestRows(snapshot, preflight, evidence.reviewFeedback);
  const phases = phasesFact(snapshot, run);
  // Fully completed at rest: the pip row draws the pipeline, not progress.
  const phaseSegments = archiveRailSegments(spineStages(snapshot.pipeline), phases.stages - 1);
  const multiRepo = prRows.length > 1;

  return (
    <section className="aftercare-shipped" aria-labelledby="aftercare-shipped-title">
      <h3 className="aftercare-shipped__title" id="aftercare-shipped-title">
        What shipped
      </h3>
      <div className="aftercare-shipped__group">
        <dl className="aftercare-shipped__rows">
          {changes === null ? null : (
            <ShippedRow label="Changes" action="View changes" onAction={onOpenChanges}>
              <ChangesCell fact={changes} />
            </ShippedRow>
          )}
          <ShippedRow label="Verification" action="View run record" onAction={onOpenRunRecord}>
            {verification === null ? (
              <span className="aftercare-shipped__placeholder">
                Check results stay in the run record
              </span>
            ) : (
              <>
                <span className="aftercare-shipped__fact-text">{verification.summary}</span>
                <span className="aftercare-shipped__meta">{verification.names.join(' · ')}</span>
              </>
            )}
          </ShippedRow>
          {prRows.map((row) => (
            <ShippedRow
              key={row.repo}
              label={multiRepo ? `Pull request · ${row.repo}` : 'Pull request'}
              action="Open on GitHub"
              external
              onAction={() => onOpenPullRequest(row.url)}
            >
              <code className="aftercare-shipped__number">{row.number}</code>
              {row.clauses.length === 0 ? null : (
                <span className="aftercare-shipped__fact-text">{row.clauses.join(' · ')}</span>
              )}
            </ShippedRow>
          ))}
          <ShippedRow label="Phases run" action="Configuration" onAction={onOpenConfiguration}>
            <span className="aftercare-shipped__pips">
              <PhaseRailTrack segments={phaseSegments} label="Phases run" tone="sealed" />
            </span>
            {phases.summary === null ? null : (
              <span className="aftercare-shipped__fact-text">{phases.summary}</span>
            )}
          </ShippedRow>
        </dl>
      </div>
    </section>
  );
}

function ShippedRow({
  label,
  action,
  external = false,
  onAction,
  children,
}: {
  label: string;
  action: string;
  external?: boolean;
  onAction(): void;
  children: ReactNode;
}): React.ReactElement {
  return (
    <div className="aftercare-shipped__row">
      <dt className="aftercare-shipped__label">{label}</dt>
      <dd className="aftercare-shipped__fact">
        <span className="aftercare-shipped__fact-body">{children}</span>
        <button type="button" className="aftercare-shipped__action" onClick={onAction}>
          {action}
          {external ? <span aria-hidden="true"> ↗</span> : null}
        </button>
      </dd>
    </div>
  );
}

/**
 * The one sanctioned proportional-width element on the surface: a single bar
 * split by the aggregated additions/deletions ratio, beside the mono totals.
 */
function ChangesCell({ fact }: { fact: ChangesFact }): React.ReactElement {
  const total = fact.additions + fact.deletions;
  const addedPercent = total === 0 ? 50 : Math.round((fact.additions / total) * 100);
  return (
    <>
      <code className="aftercare-shipped__number">
        {fact.files} file{fact.files === 1 ? '' : 's'}
      </code>
      <span className="aftercare-shipped__bar" aria-hidden="true">
        <span className="aftercare-shipped__bar-added" style={{ width: `${addedPercent}%` }} />
        <span
          className="aftercare-shipped__bar-removed"
          style={{ width: `${100 - addedPercent}%` }}
        />
      </span>
      <code className="aftercare-shipped__added">+{fact.additions}</code>
      <code className="aftercare-shipped__removed">−{fact.deletions}</code>
      {fact.commitPhrase === undefined ? null : (
        <span className="aftercare-shipped__meta">{fact.commitPhrase}</span>
      )}
    </>
  );
}
