import { usePrefersReducedMotion } from '../hooks';

export interface PhaseSpineStage {
  id: string;
  label: string;
}

export interface PhaseSpineProps {
  stages: readonly PhaseSpineStage[];
  /** Index of the stage the needle points at. */
  activeIndex: number;
  /** Visual tone of the active tick. */
  tone: 'progress' | 'error';
  /** Accessible name for the rail. */
  label?: string;
}

/**
 * The signature instrument rail: one tick per lifecycle stage, the signal
 * needle on the current stage, and condensed display labels beneath. Compact
 * labels preserve the rail's rhythm when the available width is constrained.
 */
export function PhaseSpine({
  stages,
  activeIndex,
  tone,
  label = 'Connection lifecycle',
}: PhaseSpineProps) {
  const reducedMotion = usePrefersReducedMotion();
  return (
    <div className="phase-spine" role="group" aria-label={label}>
      <ol className="phase-spine__rail">
        {stages.map((stage, index) => {
          const state =
            index < activeIndex ? 'done' : index === activeIndex ? 'active' : 'upcoming';
          const isActive = state === 'active';
          return (
            <li
              key={stage.id}
              className="phase-spine__stage"
              data-state={state}
              data-tone={isActive ? tone : 'progress'}
              {...(isActive ? { 'aria-current': 'step' as const } : {})}
            >
              <span className="phase-spine__tick" aria-hidden="true">
                {isActive ? (
                  <span
                    className={
                      reducedMotion
                        ? 'phase-spine__needle'
                        : 'phase-spine__needle phase-spine__needle--pulse'
                    }
                  />
                ) : null}
              </span>
              <span className="phase-spine__label" title={stage.label} aria-label={stage.label}>
                <span className="phase-spine__label-text phase-spine__label-text--full">
                  {stage.label}
                </span>
                <span
                  className="phase-spine__label-text phase-spine__label-text--compact"
                  aria-hidden="true"
                >
                  {compactStageLabel(stage.label)}
                </span>
              </span>
            </li>
          );
        })}
      </ol>
    </div>
  );
}

function compactStageLabel(label: string): string {
  const words = label.trim().split(/\s+/);
  if (words.length > 1) {
    return words
      .map((word) => word.charAt(0))
      .join('')
      .toUpperCase();
  }
  return label.length <= 4 ? label.toUpperCase() : label.slice(0, 3).toUpperCase();
}
