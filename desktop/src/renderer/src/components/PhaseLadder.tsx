import type { FeatureSnapshot } from '../../../shared/ipc';
import {
  displayStatusLabel,
  isRunAtRest,
  spineActiveIndex,
  spineStages,
  spineTone,
} from '../features/featureView';

export interface PhaseLadderProps {
  snapshot: FeatureSnapshot;
}

export function PhaseLadder({ snapshot }: PhaseLadderProps): React.ReactElement {
  const stages = spineStages(snapshot.pipeline);
  const activeIndex = spineActiveIndex(snapshot, stages);
  const atRest = isRunAtRest(snapshot.status);
  const tone = spineTone(snapshot);
  const statusLabel = displayStatusLabel(snapshot.status);

  return (
    <section className="phase-ladder" role="group" aria-label="Feature pipeline" data-tone={tone}>
      <h3 className="cockpit__eyebrow">Pipeline</h3>
      <ol className="phase-ladder__steps">
        {stages.map((stage, index) => {
          const state =
            index < activeIndex || (atRest && index === activeIndex)
              ? 'done'
              : index === activeIndex
                ? 'active'
                : 'upcoming';
          const accessibleState =
            state === 'done'
              ? 'completed'
              : state === 'active'
                ? `active, ${statusLabel}`
                : 'upcoming';
          return (
            <li
              key={stage.id}
              className="phase-ladder__step"
              data-state={state}
              aria-current={state === 'active' ? 'step' : undefined}
              aria-label={`${stage.label}, ${accessibleState}`}
            >
              <span className="phase-ladder__pip" aria-hidden="true">
                {state === 'done' ? '●' : state === 'active' ? '◉' : '○'}
              </span>
              <span className="phase-ladder__label">{stage.label}</span>
              {state === 'active' ? (
                <span className="phase-ladder__status">{statusLabel}</span>
              ) : null}
            </li>
          );
        })}
      </ol>
    </section>
  );
}
