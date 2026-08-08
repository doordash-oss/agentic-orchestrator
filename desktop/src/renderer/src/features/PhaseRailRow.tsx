import type { ReactNode } from 'react';
import type { RailHold, RailSegment, TrioEntry } from './phaseRail';
import { formatWaitingDuration } from './phaseRail';

export interface PhaseRailTrackProps {
  segments: readonly RailSegment[];
  /** Accessible name for the labelled group; callers scope it to their surface. */
  label?: string;
  /**
   * Visual tone. 'error' colors only the current segment (a failed/broken
   * run). 'sealed' applies to every segment — archive mode's sealed runs
   * have no current segment (they render fully at rest), so the quiet
   * treatment has to reach completed/upcoming bars too, not just a current
   * one that will never exist.
   */
  tone?: 'progress' | 'error' | 'sealed';
  /**
   * Rendered above the current segment while a hold is open. Omitted by
   * every bare-track consumer (connection shell, setup wizard) — the track
   * itself never invents a held dot, it only has a slot for one.
   */
  dot?: ReactNode;
}

/**
 * The segment track alone: one equal-width bar per phase regardless of how
 * long it took, 3px gaps, geometry (thin vs filled) and weight distinguish
 * completed/current/upcoming so the states read without relying on color.
 * Reused bare — no trio, no held dot — by the connection shell's lifecycle
 * and the setup wizard's step indicator.
 */
export function PhaseRailTrack({
  segments,
  label = 'Run phases',
  tone = 'progress',
  dot,
}: PhaseRailTrackProps): React.ReactElement {
  return (
    <ol className="phase-rail__track" role="group" aria-label={label}>
      {segments.map((segment) => {
        const isCurrent = segment.state === 'current';
        return (
          <li
            key={segment.id}
            className="phase-rail__segment"
            data-state={segment.state}
            data-held={segment.held}
            data-tone={isCurrent || tone === 'sealed' ? tone : 'progress'}
            aria-label={segment.accessibleName}
            {...(isCurrent ? { 'aria-current': 'step' as const } : {})}
          >
            {isCurrent ? dot : null}
            <span className="phase-rail__bar" aria-hidden="true" />
            <span className="phase-rail__label" aria-hidden="true">
              {segment.label}
            </span>
          </li>
        );
      })}
    </ol>
  );
}

/** Native-tooltip copy for the held dot; omits the duration when unknown. */
function heldTooltip(hold: RailHold): string {
  const duration = formatWaitingDuration(hold.waitingSince);
  return duration === null ? 'Held for your answer' : `Held ${duration} for your answer`;
}

export interface PhaseRailProps {
  segments: readonly RailSegment[];
  trio: readonly TrioEntry[];
  hold: RailHold | null;
  /** Visual tone; see `PhaseRailTrackProps.tone` — 'sealed' is archive mode's at-rest treatment. */
  tone?: 'progress' | 'error' | 'sealed';
  label?: string;
}

/**
 * The full quiet row rendered under the toolbar: the segment track plus,
 * right-aligned, the mono Elapsed/Cost/Context trio (substituted by
 * Waiting/Paused while held). A hairline — not a filled background —
 * separates it from the content below (see `.phase-rail` in app.css).
 */
export function PhaseRail({
  segments,
  trio,
  hold,
  tone = 'progress',
  label = 'Run phases',
}: PhaseRailProps): React.ReactElement {
  const dot =
    hold === null ? undefined : (
      <span className="phase-rail__dot" aria-hidden="true" title={heldTooltip(hold)} />
    );
  return (
    <div className="phase-rail">
      <PhaseRailTrack segments={segments} label={label} tone={tone} dot={dot} />
      {trio.length > 0 ? (
        <dl className="phase-rail__trio">
          {trio.map((entry) => (
            <div
              key={entry.kind}
              className="phase-rail__trio-entry"
              data-attention={entry.attention}
            >
              <dt>{entry.label}</dt>
              <dd>{entry.value}</dd>
            </div>
          ))}
        </dl>
      ) : null}
    </div>
  );
}
