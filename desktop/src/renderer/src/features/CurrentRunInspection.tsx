import { useCallback, useEffect, useRef, useState } from 'react';
import type {
  LivePreviewView,
  ReviewGateView,
  RunArtifactsListResult,
  RunTextContent,
} from '../../../shared/ipc';
import { parseIpcError } from '../wizard/ipcError';
import {
  orderedReviewStatuses,
  reviewAxisShortName,
  reviewStatusSymbol,
  reviewStatusTone,
} from './reviewModel';
import { stripUnsafeAnsi } from './timelineModel';

interface CurrentRunInspectionProps {
  featureId: string;
  runNumber: number;
  currentPhase: string;
  currentRoadmapPhase?: number;
  reviewGate: ReviewGateView;
}

export function CurrentRunInspection({
  featureId,
  runNumber,
  currentPhase,
  currentRoadmapPhase,
  reviewGate,
}: CurrentRunInspectionProps): React.ReactElement {
  const [preview, setPreview] = useState<LivePreviewView | null>(null);
  const [artifacts, setArtifacts] = useState<RunArtifactsListResult['artifacts']>([]);
  const [content, setContent] = useState<{
    kind: 'artifact' | 'log';
    value: RunTextContent;
  } | null>(null);
  const [loadingContent, setLoadingContent] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const requestRef = useRef(0);

  const refresh = useCallback(async () => {
    const request = ++requestRef.current;
    setError(null);
    try {
      const [nextPreview, nextArtifacts] = await Promise.all([
        window.agentico.getLivePreview(featureId),
        window.agentico.listRunArtifacts({ featureId, runNumber }),
      ]);
      if (request !== requestRef.current) return;
      setPreview(nextPreview);
      setArtifacts(nextArtifacts.artifacts);
    } catch (cause) {
      if (request === requestRef.current) setError(parseIpcError(cause).message);
    }
  }, [featureId, runNumber]);

  useEffect(() => {
    void refresh();
    return () => {
      requestRef.current += 1;
    };
  }, [refresh]);

  const openContent = useCallback(
    async (kind: 'artifact' | 'log', id: string) => {
      setLoadingContent(true);
      setError(null);
      try {
        const value =
          kind === 'artifact'
            ? await window.agentico.getRunArtifactContent({
                featureId,
                runNumber,
                artifactId: id,
                limit: 64 * 1024,
              })
            : await window.agentico.getRunLogContent({
                featureId,
                runNumber,
                logId: id === 'session' ? 'session' : 'phase',
                limit: 64 * 1024,
              });
        setContent({ kind, value });
      } catch (cause) {
        setError(parseIpcError(cause).message);
      } finally {
        setLoadingContent(false);
      }
    },
    [featureId, runNumber],
  );

  return (
    <section className="current-inspection" aria-label="Current run inspection">
      <header className="current-inspection__header">
        <div>
          <p className="cockpit__eyebrow">Mutable current run</p>
          <h3 className="setup-step__title">Live preview and files</h3>
        </div>
        <button type="button" onClick={() => void refresh()}>
          Refresh
        </button>
      </header>

      {error !== null ? (
        <p role="alert" className="form-field__error">
          {error}
        </p>
      ) : null}

      {preview === null ? (
        <p className="setup-step__empty">Loading current run inspection…</p>
      ) : (
        <div className="current-inspection__preview">
          <p className="current-inspection__activity">{preview.activity}</p>
          <dl className="current-inspection__metrics">
            <div>
              <dt>Context</dt>
              <dd>
                {preview.contextPercentage < 0 ? 'Unavailable' : `${preview.contextPercentage}%`}
              </dd>
            </div>
            <div>
              <dt>Elapsed</dt>
              <dd>{preview.totalSeconds}s</dd>
            </div>
            <div>
              <dt>Cost</dt>
              <dd>${preview.totalUsd.toFixed(2)}</dd>
            </div>
          </dl>
        </div>
      )}

      <ReviewGateSummary
        gate={reviewGate}
        currentPhase={currentPhase}
        currentRoadmapPhase={currentRoadmapPhase}
      />

      <div className="current-inspection__resources">
        <div>
          <h4>Run artifacts</h4>
          {artifacts.length === 0 ? (
            <p className="setup-step__empty">No current-run artifacts yet.</p>
          ) : (
            <ul>
              {artifacts.map((artifact) => (
                <li key={artifact.id}>
                  <button
                    type="button"
                    disabled={artifact.contentAvailable === false || loadingContent}
                    aria-label={`Open artifact ${artifact.id}`}
                    onClick={() => void openContent('artifact', artifact.id)}
                  >
                    {artifact.id}
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
        <div>
          <h4>Bounded logs</h4>
          <button
            type="button"
            disabled={loadingContent}
            onClick={() => void openContent('log', 'session')}
          >
            Open session log
          </button>
          <button
            type="button"
            disabled={loadingContent}
            onClick={() => void openContent('log', 'phase')}
          >
            Open phase log
          </button>
        </div>
      </div>

      {content !== null ? (
        <div className="current-inspection__content">
          <div className="current-inspection__content-header">
            <span>{content.value.id}</span>
            {content.value.truncated ? <span>Bounded page · more content remains</span> : null}
            <button type="button" onClick={() => setContent(null)}>
              Close
            </button>
          </div>
          <pre
            aria-label={
              content.kind === 'artifact'
                ? 'Current run artifact content'
                : 'Current run log content'
            }
          >
            {content.kind === 'log' ? stripUnsafeAnsi(content.value.text) : content.value.text}
          </pre>
        </div>
      ) : null}
    </section>
  );
}

function ReviewGateSummary({
  gate,
  currentPhase,
  currentRoadmapPhase,
}: {
  gate: ReviewGateView;
  currentPhase: string;
  currentRoadmapPhase?: number;
}): React.ReactElement | null {
  const statuses = orderedReviewStatuses(gate.validatorStatuses);
  if (!gate.reviewingGate && !gate.validatingPlan && statuses.length === 0) return null;

  const target = gate.reviewingGate
    ? currentPhase.trim().toLocaleLowerCase() === 'implement'
      ? 'Reviewing implementation'
      : 'Final review'
    : currentRoadmapPhase === undefined
      ? 'Validating plan'
      : `Validating Phase ${currentRoadmapPhase} plan`;
  const counts = statuses.reduce(
    (result, [, status]) => {
      result[reviewStatusTone(status)] += 1;
      return result;
    },
    { passed: 0, failed: 0, running: 0, pending: 0 },
  );

  return (
    <section className="review-gate" aria-label="Review gate">
      <div className="review-gate__heading">
        <div>
          <span className="review-gate__eyebrow">Review gate</span>
          <h4>{target}</h4>
        </div>
        {gate.reviewFixing ? <span className="review-gate__fixing">Fix pass active</span> : null}
      </div>
      {statuses.length > 0 ? (
        <>
          <ul className="review-gate__axes" aria-label="Review axes">
            {statuses.map(([name, status]) => (
              <li key={name} data-status={reviewStatusTone(status)} title={`${name}: ${status}`}>
                <span>{reviewAxisShortName(name)}</span>
                <span aria-hidden="true">{reviewStatusSymbol(status)}</span>
              </li>
            ))}
          </ul>
          <p className="review-gate__counts">
            {counts.passed} passed · {counts.failed} changes requested · {counts.running} running
          </p>
        </>
      ) : (
        <p className="review-gate__counts">Review session is starting…</p>
      )}
    </section>
  );
}
