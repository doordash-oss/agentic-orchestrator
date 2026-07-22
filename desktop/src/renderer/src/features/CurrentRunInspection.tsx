import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type {
  LivePreviewView,
  ReviewGateView,
  RunArtifactsListResult,
  RunTextContent,
  SessionSummary,
  TranscriptMessage,
} from '../../../shared/ipc';
import { parseIpcError } from '../wizard/ipcError';
import {
  orderedReviewStatuses,
  reviewAxisShortName,
  reviewStatusSymbol,
  reviewStatusTone,
} from './reviewModel';
import { stripUnsafeAnsi } from './timelineModel';
import { buildConversation } from './transcript/conversation';
import { ConversationTranscript } from './transcript/ConversationTranscript';
import { HistoricalTimeline } from './RunTimeline';
import { useCohortTranscripts } from './useCohortTranscripts';
import { cohortTabLabels, cohortTabStatus, isTerminalSessionStatus } from './liveCohort';
import { CloseIcon, MaximizeIcon, MinimizeIcon } from '../components/icons';

const IDLE_ACTIVITY_LABEL = 'Thinking through the next step';

type PreviewView = 'conversation' | 'trace';

interface CurrentRunInspectionProps {
  featureId: string;
  runNumber: number;
  currentPhase: string;
  currentRoadmapPhase?: number;
  reviewGate: ReviewGateView;
  /** Only open live SSE while the run is actually streaming. */
  shouldStream?: boolean;
}

type RunArtifact = RunArtifactsListResult['artifacts'][number];

function artifactSortKey(id: string): [number, number, string] {
  const normalized = id.trim().toLowerCase();
  switch (normalized) {
    case 'inquire':
      return [10, 0, normalized];
    case 'research':
      return [20, 0, normalized];
    case 'design':
      return [30, 0, normalized];
    case 'roadmap':
      return [40, 0, normalized];
    case 'plan':
    case 'phase-plan':
      return [50, 0, normalized];
    default: {
      const phasePlan = /^phase-(\d+)-plan$/.exec(normalized);
      if (phasePlan !== null) {
        return [60, Number(phasePlan[1]), normalized];
      }
      return [100, 0, normalized];
    }
  }
}

export function orderRunArtifacts(artifacts: readonly RunArtifact[]): RunArtifact[] {
  return [...artifacts].sort((a, b) => {
    const left = artifactSortKey(a.id);
    const right = artifactSortKey(b.id);
    return (
      left[0] - right[0] ||
      left[1] - right[1] ||
      left[2].localeCompare(right[2], undefined, { numeric: true, sensitivity: 'base' })
    );
  });
}

export function CurrentRunInspection({
  featureId,
  runNumber,
  currentPhase,
  currentRoadmapPhase,
  reviewGate,
  shouldStream = true,
}: CurrentRunInspectionProps): React.ReactElement {
  const [preview, setPreview] = useState<LivePreviewView | null>(null);
  const [artifacts, setArtifacts] = useState<RunArtifactsListResult['artifacts']>([]);
  const [content, setContent] = useState<{
    kind: 'artifact' | 'log';
    value: RunTextContent;
  } | null>(null);
  const [loadingContent, setLoadingContent] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [fullscreen, setFullscreen] = useState(false);
  const [view, setView] = useState<PreviewView>('conversation');
  const requestRef = useRef(0);

  const live = useCohortTranscripts(featureId, runNumber, currentPhase, shouldStream);
  const selectedSession = live.cohort.find((session) => session.id === live.selectedId) ?? null;
  const stage = useTranscriptStage(live.cohort, live.transcripts, selectedSession, preview);

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
      setArtifacts(orderRunArtifacts(nextArtifacts.artifacts));
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
        <button
          type="button"
          onClick={() => {
            void refresh();
            live.refresh();
          }}
        >
          Refresh
        </button>
      </header>

      {error !== null ? (
        <p role="alert" className="form-field__error">
          {error}
        </p>
      ) : null}

      {preview === null && live.cohort.length === 0 ? (
        <p className="setup-step__empty">Loading current run inspection…</p>
      ) : (
        <div className="current-inspection__preview">
          <div className="live-preview__frame">
            <div className="live-preview__bar">
              <p className="cockpit__eyebrow">Live agent activity</p>
              <div className="live-preview__bar-controls">
                <ViewToggle view={view} onChange={setView} />
                <button
                  type="button"
                  className="live-preview__icon-button"
                  aria-label="Expand live preview to full screen"
                  title="Full screen"
                  onClick={() => setFullscreen(true)}
                >
                  <MaximizeIcon />
                </button>
              </div>
            </div>
            <TranscriptStage
              stage={stage}
              view={view}
              selectedId={live.selectedId}
              selectSession={live.selectSession}
            />
          </div>
          {preview !== null ? (
            <>
              <p className="current-inspection__activity">{preview.activity}</p>
              <PreviewMetrics preview={preview} />
            </>
          ) : null}
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
            <ol className="current-inspection__artifact-list">
              {artifacts.map((artifact, index) => (
                <li key={artifact.id} className="current-inspection__artifact-item">
                  <span className="current-inspection__artifact-index" aria-hidden="true">
                    {String(index + 1).padStart(2, '0')}
                  </span>
                  <button
                    type="button"
                    className="current-inspection__artifact-button"
                    disabled={artifact.contentAvailable === false || loadingContent}
                    aria-label={`Open artifact ${artifact.id}`}
                    onClick={() => void openContent('artifact', artifact.id)}
                  >
                    {artifact.id}
                  </button>
                </li>
              ))}
            </ol>
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

      {fullscreen ? (
        <LivePreviewOverlay
          onClose={() => setFullscreen(false)}
          stage={stage}
          view={view}
          onChangeView={setView}
          selectedId={live.selectedId}
          selectSession={live.selectSession}
          preview={preview}
        />
      ) : null}
    </section>
  );
}

interface TranscriptStageModel {
  cohort: SessionSummary[];
  rows: TranscriptMessage[];
  items: ReturnType<typeof buildConversation>;
  waiting: boolean;
  labels: Map<string, string>;
  assistantName: string;
}

const EMPTY_ROWS: TranscriptMessage[] = [];

function useTranscriptStage(
  cohort: SessionSummary[],
  transcripts: Record<string, TranscriptMessage[]>,
  selectedSession: SessionSummary | null,
  preview: LivePreviewView | null,
): TranscriptStageModel {
  const usesFallback = cohort.length === 0;
  const rows = usesFallback
    ? (preview?.transcript ?? EMPTY_ROWS)
    : (transcripts[selectedSession?.id ?? ''] ?? EMPTY_ROWS);
  const items = useMemo(() => buildConversation(rows, { mode: 'assistant-only' }), [rows]);
  const labels = useMemo(() => cohortTabLabels(cohort), [cohort]);
  const activeSession = selectedSession ?? preview?.session ?? null;
  const waiting = activeSession !== null && !isTerminalSessionStatus(activeSession.status);
  const assistantName =
    (selectedSession !== null ? labels.get(selectedSession.id) : undefined) ??
    activeSession?.label ??
    activeSession?.repo ??
    'Agentico';
  return { cohort, rows, items, waiting, labels, assistantName };
}

function ViewToggle({
  view,
  onChange,
}: {
  view: PreviewView;
  onChange(next: PreviewView): void;
}): React.ReactElement {
  return (
    <div className="live-preview__views" role="group" aria-label="Preview view">
      <button
        type="button"
        className="live-preview__view"
        aria-pressed={view === 'conversation'}
        onClick={() => onChange('conversation')}
      >
        Conversation
      </button>
      <button
        type="button"
        className="live-preview__view"
        aria-pressed={view === 'trace'}
        onClick={() => onChange('trace')}
      >
        Signal trace
      </button>
    </div>
  );
}

function TranscriptStage({
  stage,
  view,
  selectedId,
  selectSession,
}: {
  stage: TranscriptStageModel;
  view: PreviewView;
  selectedId: string | null;
  selectSession(id: string): void;
}): React.ReactElement {
  return (
    <div className="live-preview">
      {stage.cohort.length > 1 ? (
        <div className="live-preview__tabs" role="tablist" aria-label="Live agents">
          {stage.cohort.map((session) => {
            const status = cohortTabStatus(session);
            return (
              <button
                key={session.id}
                type="button"
                role="tab"
                aria-selected={session.id === selectedId}
                className="live-preview__tab"
                data-status={status}
                onClick={() => selectSession(session.id)}
              >
                <span className="live-preview__tab-pip" data-status={status} aria-hidden="true" />
                <span className="live-preview__tab-label">{stage.labels.get(session.id)}</span>
                <span className="live-preview__tab-status">{status}</span>
              </button>
            );
          })}
        </div>
      ) : null}
      {view === 'conversation' ? (
        <ConversationTranscript
          className="live-preview__transcript"
          ariaLabel="Live agent transcript"
          items={stage.items}
          waiting={stage.waiting}
          idleLabel={IDLE_ACTIVITY_LABEL}
          assistantName={stage.assistantName}
          emptyState={<p className="setup-step__empty">Waiting for the agent to respond…</p>}
        />
      ) : (
        <div className="live-preview__trace">
          <HistoricalTimeline messages={stage.rows} />
        </div>
      )}
    </div>
  );
}

function PreviewMetrics({ preview }: { preview: LivePreviewView }): React.ReactElement {
  return (
    <dl className="current-inspection__metrics">
      <div>
        <dt>Context</dt>
        <dd>{preview.contextPercentage < 0 ? 'Unavailable' : `${preview.contextPercentage}%`}</dd>
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
  );
}

function LivePreviewOverlay({
  onClose,
  stage,
  view,
  onChangeView,
  selectedId,
  selectSession,
  preview,
}: {
  onClose(): void;
  stage: TranscriptStageModel;
  view: PreviewView;
  onChangeView(next: PreviewView): void;
  selectedId: string | null;
  selectSession(id: string): void;
  preview: LivePreviewView | null;
}): React.ReactElement {
  const dialogRef = useRef<HTMLDivElement>(null);
  useModalDismiss(dialogRef, onClose);
  return (
    <div className="live-preview__overlay-backdrop" onMouseDown={onClose}>
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-label="Live agent preview"
        className="live-preview__overlay"
        tabIndex={-1}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="live-preview__overlay-header">
          <p className="cockpit__eyebrow">Live agent activity</p>
          <div className="live-preview__overlay-controls">
            <ViewToggle view={view} onChange={onChangeView} />
            <button
              type="button"
              className="live-preview__icon-button"
              aria-label="Exit full screen"
              title="Exit full screen"
              onClick={onClose}
            >
              <MinimizeIcon />
            </button>
            <button
              type="button"
              className="live-preview__icon-button"
              aria-label="Close live preview"
              title="Close"
              onClick={onClose}
            >
              <CloseIcon />
            </button>
          </div>
        </header>
        <TranscriptStage
          stage={stage}
          view={view}
          selectedId={selectedId}
          selectSession={selectSession}
        />
        {preview !== null ? (
          <footer className="live-preview__overlay-footer">
            <p className="current-inspection__activity">{preview.activity}</p>
            <PreviewMetrics preview={preview} />
          </footer>
        ) : null}
      </div>
    </div>
  );
}

const FOCUSABLE_SELECTOR =
  'button:not([disabled]), [href], input:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

/** Escape-to-close, Tab focus trap, focus restoration, and body-scroll lock. */
function useModalDismiss(ref: React.RefObject<HTMLElement | null>, onClose: () => void): void {
  useEffect(() => {
    const node = ref.current;
    const previouslyFocused =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';

    const focusable = (): HTMLElement[] =>
      node === null ? [] : Array.from(node.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR));
    (focusable()[0] ?? node)?.focus();

    const onKey = (event: KeyboardEvent): void => {
      if (event.key === 'Escape') {
        event.preventDefault();
        onClose();
        return;
      }
      if (event.key !== 'Tab' || node === null) return;
      const items = focusable();
      if (items.length === 0) {
        event.preventDefault();
        node.focus();
        return;
      }
      const first = items[0]!;
      const last = items[items.length - 1]!;
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };

    window.addEventListener('keydown', onKey);
    return () => {
      window.removeEventListener('keydown', onKey);
      document.body.style.overflow = previousOverflow;
      requestAnimationFrame(() => previouslyFocused?.focus());
    };
  }, [ref, onClose]);
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
