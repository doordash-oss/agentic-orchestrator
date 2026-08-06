/**
 * The Bench sidebar's row-scale progress indicator: a strip of equal-width
 * pips standing in for the cockpit's fuller stage rail, sized for
 * a single sidebar row rather than a card or cockpit header. It carries no
 * label per stop — a row is compact enough that only the fill position
 * (done/active/upcoming) reads at a glance.
 */
export interface PipRailProps {
  /** Total pipeline stops. */
  stageCount: number;
  /** -1 = not started; index of the current stop otherwise. */
  activeIndex: number;
  /** True when the active stop is the run's final resting state, not a live needle. */
  atRest?: boolean;
  tone?: 'progress' | 'attention';
  /** Accessible label for the whole strip (e.g. "<feature> progress"). */
  label: string;
}

/** An equal-width pip strip: the only new shared primitive the Bench sidebar needs. */
export function PipRail({
  stageCount,
  activeIndex,
  atRest = false,
  tone = 'progress',
  label,
}: PipRailProps) {
  const stops = Array.from({ length: Math.max(stageCount, 1) }, (_, index) => index);
  return (
    <span className="pip-rail" role="img" aria-label={label} data-tone={tone}>
      {stops.map((index) => {
        const state =
          index < activeIndex || (atRest && index === activeIndex)
            ? 'done'
            : index === activeIndex
              ? 'active'
              : 'upcoming';
        return <i key={index} className="pip-rail__pip" data-state={state} />;
      })}
    </span>
  );
}
