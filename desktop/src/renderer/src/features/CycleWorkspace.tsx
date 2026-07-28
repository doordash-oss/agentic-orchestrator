import type { FeatureSnapshot, RunDetailView } from '../../../shared/ipc';
import { CurrentRunInspection, type RunMetrics } from './CurrentRunInspection';
import { FeatureFactsRail } from './FeatureFactsRail';
import { cycleFailureDetail, type CyclePresentation } from './postImplementationModel';

export interface CycleWorkspaceProps {
  snapshot: FeatureSnapshot;
  run: RunDetailView | null;
  presentation: CyclePresentation;
  onRunMetrics(metrics: RunMetrics | null): void;
  onStop(): void;
  onResume(): void;
  onRetry(): void;
  onReturnToAftercare(): void;
  onOpenConfig(): void;
  onOpenRunRecord(): void;
  onOpenPullRequest(url: string): void;
}

export function CycleWorkspace({
  snapshot,
  run,
  presentation,
  onRunMetrics,
  onStop,
  onResume,
  onRetry,
  onReturnToAftercare,
  onOpenConfig,
  onOpenRunRecord,
  onOpenPullRequest,
}: CycleWorkspaceProps): React.ReactElement {
  const failed = snapshot.cycle?.status === 'failed';
  const interrupted = snapshot.cycle?.status === 'interrupted';
  const stoppable =
    !failed && snapshot.actions.some((action) => action.id === 'pause-stop' && action.enabled);
  const resumable =
    interrupted && snapshot.actions.some((action) => action.id === 'resume' && action.enabled);
  const retryAction = snapshot.actions.find((action) => action.id === 'retry');
  const failedRepos = snapshot.repoStatus?.filter((repo) => repo.lastError !== undefined) ?? [];

  return (
    <section
      className="post-workspace cycle-workspace"
      data-failed={failed || undefined}
      aria-label={`${cycleLabel(presentation.id)} cycle`}
    >
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
          <div className="cycle-workspace__header-actions">
            <button type="button" className="cycle-workspace__config" onClick={onOpenConfig}>
              Edit configuration…
            </button>
            {failed ? (
              <div className="cycle-workspace__failed-actions">
                <button
                  type="button"
                  className="cycle-workspace__primary"
                  disabled={retryAction?.enabled !== true}
                  title={
                    retryAction?.enabled === true
                      ? undefined
                      : retryAction?.disabledReasons[0]?.message
                  }
                  onClick={onRetry}
                >
                  Retry cycle
                </button>
                <button type="button" onClick={onReturnToAftercare}>
                  Return to Aftercare
                </button>
              </div>
            ) : resumable ? (
              <div className="cycle-workspace__failed-actions">
                <button type="button" className="cycle-workspace__primary" onClick={onResume}>
                  Resume cycle
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
          </div>
        </header>

        <ol
          className="cycle-workspace__spine"
          aria-label="Cycle progress"
          data-stage-count={presentation.stages.length}
        >
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
            <p className="post-workspace__eyebrow">Cycle failed</p>
            <h3>The agent could not finish this cycle.</h3>
            <p>{cycleFailureDetail(snapshot)}</p>
            {failedRepos.length <= 1 ? null : (
              <ul className="cycle-workspace__repo-failures">
                {failedRepos.map((repo) => (
                  <li key={repo.name}>
                    <code>{repo.name}</code>
                    <span>{repo.lastError}</span>
                  </li>
                ))}
              </ul>
            )}
          </section>
        ) : null}
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
          shouldStream={!failed && !interrupted}
          presentation="cycle"
          cycle={snapshot.cycle}
          repoStatus={snapshot.repoStatus}
          onRunMetrics={onRunMetrics}
        />
      </main>
      <FeatureFactsRail snapshot={snapshot} run={run} onOpenPullRequest={onOpenPullRequest} />
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
