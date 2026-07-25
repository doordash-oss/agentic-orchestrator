import type { ReactNode } from 'react';
import type { FeatureSnapshot, RunDetailView } from '../../../shared/ipc';
import { CurrentRunInspection, type RunMetrics } from './CurrentRunInspection';
import { FeatureFactsRail } from './FeatureFactsRail';
import type { CyclePresentation } from './postImplementationModel';

export interface CycleWorkspaceProps {
  snapshot: FeatureSnapshot;
  run: RunDetailView | null;
  presentation: CyclePresentation;
  attentionFooter?: ReactNode;
  onRunMetrics(metrics: RunMetrics | null): void;
  onStop(): void;
  onRetry(): void;
  onReturnToAftercare(): void;
  onOpenRunRecord(): void;
}

export function CycleWorkspace({
  snapshot,
  run,
  presentation,
  attentionFooter,
  onRunMetrics,
  onStop,
  onRetry,
  onReturnToAftercare,
  onOpenRunRecord,
}: CycleWorkspaceProps): React.ReactElement {
  const failed = snapshot.cycle?.status === 'failed';
  const stoppable = !failed && snapshot.actions.some((action) => action.id === 'pause-stop' && action.enabled);

  return (
    <section className="post-workspace cycle-workspace" aria-label={`${cycleLabel(presentation.id)} cycle`}>
      <main className="cycle-workspace__main">
        <header className="cycle-workspace__header">
          <div>
            <p className="post-workspace__eyebrow">
              {cycleLabel(presentation.id)} · Cycle {presentation.count}
              <button type="button" onClick={onOpenRunRecord}>
                Run #{snapshot.activeRun}
              </button>
            </p>
            <h2>{presentation.headline}</h2>
          </div>
          {failed ? (
            <div className="cycle-workspace__failed-actions">
              <button type="button" className="cycle-workspace__primary" onClick={onRetry}>
                Retry cycle
              </button>
              <button type="button" onClick={onReturnToAftercare}>
                Return to Aftercare
              </button>
            </div>
          ) : stoppable ? (
            <button type="button" className="cycle-workspace__stop" onClick={onStop}>
              Stop cycle
            </button>
          ) : null}
        </header>

        <ol className="cycle-workspace__spine" aria-label="Cycle progress">
          {presentation.stages.map((stage) => (
            <li
              key={stage.id}
              data-state={stage.state}
              data-conditional={stage.conditional === true}
              aria-current={stage.state === 'active' ? 'step' : undefined}
            >
              <span aria-hidden="true" />
              <strong>{stage.label}</strong>
              {stage.conditional === true ? <small>If needed</small> : null}
            </li>
          ))}
        </ol>

        <div className="cycle-workspace__flight-line">
          <strong>{presentation.current}</strong>
          {presentation.next === undefined ? null : (
            <span>
              Next · <strong>{presentation.next}</strong>
            </span>
          )}
        </div>

        {failed ? (
          <section className="cycle-workspace__failure" role="alert">
            <p className="post-workspace__eyebrow">Cycle interrupted</p>
            <h3>The agent could not finish this cycle.</h3>
            <p>{failureMessage(snapshot)}</p>
          </section>
        ) : (
          <CurrentRunInspection
            featureId={snapshot.id}
            runNumber={snapshot.activeRun}
            currentPhase={snapshot.currentPhase}
            featureStatus={snapshot.status}
            currentRoadmapPhase={snapshot.currentRoadmapPhase}
            totalRoadmapPhases={snapshot.totalRoadmapPhases}
            currentIteration={snapshot.currentIteration}
            phaseStatus={snapshot.phaseStatus}
            reviewGate={snapshot.reviewGate}
            verificationItems={snapshot.verificationItems}
            waitReason={snapshot.waitReason}
            shouldStream
            presentation="cycle"
            attentionFooter={attentionFooter}
            onRunMetrics={onRunMetrics}
          />
        )}
      </main>
      <FeatureFactsRail snapshot={snapshot} run={run} />
    </section>
  );
}

function cycleLabel(id: CyclePresentation['id']): string {
  switch (id) {
    case 'rebase':
      return 'Rebase';
    case 'review-comments':
      return 'Review comments';
    case 'refactor':
      return 'Refactor';
  }
}

function failureMessage(snapshot: FeatureSnapshot): string {
  if (snapshot.failure?.message !== undefined) return snapshot.failure.message;
  const repositoryError = snapshot.repoStatus?.find((repository) => repository.lastError)?.lastError;
  return repositoryError ?? 'The runtime stopped before the cycle reached its next checkpoint.';
}
