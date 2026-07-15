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
 * The signature instrument rail: one tick per lifecycle stage, the amber
 * needle on the current stage, mono labels beneath. Reused by later phases
 * for the feature pipeline spine.
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
              <span className="phase-spine__label">{stage.label}</span>
            </li>
          );
        })}
      </ol>
    </div>
  );
}
