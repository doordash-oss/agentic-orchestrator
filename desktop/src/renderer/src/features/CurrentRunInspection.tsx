import { useCallback, useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react';
import type {
  LivePreviewView,
  ReviewGateView,
  RunArtifactsListResult,
  RunLogView,
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
import { displayStatusLabel, isRunAtRest } from './featureView';
import { stripUnsafeAnsi } from './timelineModel';
import { buildConversation } from './transcript/conversation';
import { ConversationTranscript } from './transcript/ConversationTranscript';
import { HistoricalTimeline } from './RunTimeline';
import { useCohortTranscripts } from './useCohortTranscripts';
import { cohortTabLabels, cohortTabStatus, isTerminalSessionStatus } from './liveCohort';
import { renderSanitizedMarkdown } from './sanitizedMarkdown';
import { CloseIcon, MaximizeIcon, MinimizeIcon } from '../components/icons';

const IDLE_ACTIVITY_LABEL = 'Thinking through the next step';

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

type PreviewView = 'conversation' | 'trace';

interface CurrentRunInspectionProps {
  featureId: string;
  runNumber: number;
  currentPhase: string;
  /** Server feature status; distinguishes a resting run from an active one. */
  featureStatus?: string;
  currentRoadmapPhase?: number;
  totalRoadmapPhases?: number;
  /** Implement-loop iteration within the current roadmap phase. */
  currentIteration?: number;
  /** Mid-flight status from the server ("implementing" | "reviewing"). */
  phaseStatus?: string;
  reviewGate: ReviewGateView;
  waitReason?: string;
  /** Only open live SSE while the run is actually streaming. */
  shouldStream?: boolean;
  /** Opens the conversation overlay for a newly routed attention item. */
  attentionRequestId?: number;
  /** Response controls docked beneath the expanded conversation. */
  attentionFooter?: ReactNode;
  onAttentionPreviewClose?(): void;
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

function ResourceSection({
  title,
  count,
  children,
}: {
  title: string;
  count: number;
  children: ReactNode;
}): React.ReactElement {
  const [expanded, setExpanded] = useState(false);
  const contentId = useId();

  return (
    <section className="current-inspection__resource">
      <h4>
        <button
          type="button"
          className="current-inspection__resource-toggle"
          aria-label={`${title} (${count})`}
          aria-expanded={expanded}
          aria-controls={contentId}
          onClick={() => setExpanded((value) => !value)}
        >
          <span className="current-inspection__resource-caret" aria-hidden="true" />
          <span>{title}</span>
          <span className="current-inspection__resource-count">{count}</span>
        </button>
      </h4>
      {expanded ? (
        <div id={contentId} className="current-inspection__resource-content">
          {children}
        </div>
      ) : null}
    </section>
  );
}

export function CurrentRunInspection({
  featureId,
  runNumber,
  currentPhase,
  featureStatus,
  currentRoadmapPhase,
  totalRoadmapPhases,
  currentIteration,
  phaseStatus,
  reviewGate,
  waitReason,
  shouldStream = true,
  attentionRequestId,
  attentionFooter,
  onAttentionPreviewClose,
}: CurrentRunInspectionProps): React.ReactElement {
  const [preview, setPreview] = useState<LivePreviewView | null>(null);
  const [artifacts, setArtifacts] = useState<RunArtifactsListResult['artifacts']>([]);
  const [logs, setLogs] = useState<RunLogView[]>([]);
  const [content, setContent] = useState<{
    kind: 'artifact' | 'log';
    label: string;
    value: RunTextContent;
  } | null>(null);
  const [loadingContent, setLoadingContent] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [logListError, setLogListError] = useState<string | null>(null);
  const [contentError, setContentError] = useState<string | null>(null);
  const [fullscreen, setFullscreen] = useState(false);
  const [artifactFullscreen, setArtifactFullscreen] = useState(false);
  const [view, setView] = useState<PreviewView>('conversation');
  const requestRef = useRef(0);

  const live = useCohortTranscripts(featureId, runNumber, currentPhase, shouldStream);
  const selectedSession = live.cohort.find((session) => session.id === live.selectedId) ?? null;
  const stage = useTranscriptStage(live.cohort, live.transcripts, selectedSession, preview);
  const closeFullscreen = useCallback(() => {
    setFullscreen(false);
    onAttentionPreviewClose?.();
  }, [onAttentionPreviewClose]);

  useEffect(() => {
    if (attentionRequestId === undefined) return;
    setView('conversation');
    setFullscreen(true);
  }, [attentionRequestId]);

  const refresh = useCallback(async () => {
    const request = ++requestRef.current;
    setError(null);
    try {
      const [nextPreview, nextArtifacts, logResult] = await Promise.all([
        window.agentico.getLivePreview(featureId),
        window.agentico.listRunArtifacts({ featureId, runNumber }),
        window.agentico
          .listRunLogs({ featureId, runNumber })
          .then((value) => ({ value, error: null }))
          .catch((cause: unknown) => ({
            value: { logs: [] as RunLogView[] },
            error: parseIpcError(cause).message,
          })),
      ]);
      if (request !== requestRef.current) return;
      setPreview(nextPreview);
      setArtifacts(orderRunArtifacts(nextArtifacts.artifacts));
      setLogs(logResult.value.logs);
      setLogListError(logResult.error);
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
    async (kind: 'artifact' | 'log', id: string, size?: number, label = id) => {
      setLoadingContent(true);
      setContentError(null);
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
                logId: id,
                offset: Math.max(0, (size ?? 0) - 64 * 1024),
                limit: 64 * 1024,
              });
        setContent({ kind, label, value });
        setArtifactFullscreen(false);
      } catch (cause) {
        setContentError(parseIpcError(cause).message);
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

      <RoadmapGauge
        currentPhase={currentPhase}
        featureStatus={featureStatus}
        currentRoadmapPhase={currentRoadmapPhase}
        totalRoadmapPhases={totalRoadmapPhases}
        currentIteration={currentIteration}
        phaseStatus={phaseStatus}
        reviewGate={reviewGate}
      />

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
              waitReason={waitReason}
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
        <ResourceSection
          key={`artifacts-${featureId}`}
          title="Run artifacts"
          count={artifacts.length}
        >
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
        </ResourceSection>
        <ResourceSection key={`logs-${featureId}`} title="Bounded logs" count={logs.length}>
          {logs.length === 0 ? (
            <p className="setup-step__empty">
              {logListError === null
                ? 'No run logs yet.'
                : `Could not refresh run logs: ${logListError}`}
            </p>
          ) : (
            <ol className="current-inspection__artifact-list" aria-label="Available run logs">
              {logs.map((log, index) => (
                <li key={log.id} className="current-inspection__artifact-item">
                  <span className="current-inspection__artifact-index" aria-hidden="true">
                    {String(index + 1).padStart(2, '0')}
                  </span>
                  <button
                    type="button"
                    className="current-inspection__artifact-button"
                    disabled={loadingContent}
                    aria-label={`Open log ${log.path}`}
                    title={`${log.path} · ${formatBytes(log.size)}`}
                    onClick={() => void openContent('log', log.id, log.size, log.path)}
                  >
                    {log.path} · {formatBytes(log.size)}
                  </button>
                </li>
              ))}
            </ol>
          )}
        </ResourceSection>
      </div>

      {contentError !== null ? (
        <p role="alert" className="form-field__error">
          Could not open this file: {contentError}
        </p>
      ) : null}

      {content !== null ? (
        <div className="current-inspection__content">
          <div className="current-inspection__content-header">
            <span>{content.label}</span>
            {content.kind === 'log' && content.value.offset > 0 ? <span>Latest 64 KB</span> : null}
            {content.value.truncated ? <span>Bounded page · more content remains</span> : null}
            {content.kind === 'artifact' ? (
              <button
                type="button"
                className="live-preview__icon-button"
                aria-label="Enlarge artifact"
                title="Enlarge artifact"
                onClick={() => setArtifactFullscreen(true)}
              >
                <MaximizeIcon />
              </button>
            ) : null}
            <button
              type="button"
              onClick={() => {
                setArtifactFullscreen(false);
                setContent(null);
              }}
            >
              Close
            </button>
          </div>
          {content.kind === 'artifact' ? (
            <RenderedArtifact text={content.value.text} ariaLabel="Current run artifact content" />
          ) : (
            <pre aria-label="Current run log content">{stripUnsafeAnsi(content.value.text)}</pre>
          )}
        </div>
      ) : null}

      {artifactFullscreen && content?.kind === 'artifact' ? (
        <ArtifactOverlay artifact={content.value} onClose={() => setArtifactFullscreen(false)} />
      ) : null}

      {fullscreen ? (
        <LivePreviewOverlay
          onClose={closeFullscreen}
          stage={stage}
          view={view}
          onChangeView={setView}
          selectedId={live.selectedId}
          selectSession={live.selectSession}
          preview={preview}
          waitReason={waitReason}
          attentionFooter={attentionFooter}
        />
      ) : null}
    </section>
  );
}

function RenderedArtifact({ text, ariaLabel }: { text: string; ariaLabel: string }) {
  return (
    <div
      className="review-surface__preview current-inspection__artifact-markdown"
      aria-label={ariaLabel}
      dangerouslySetInnerHTML={{ __html: renderSanitizedMarkdown(text) }}
    />
  );
}

function ArtifactOverlay({
  artifact,
  onClose,
}: {
  artifact: RunTextContent;
  onClose(): void;
}): React.ReactElement {
  const dialogRef = useRef<HTMLDivElement>(null);
  useModalDismiss(dialogRef, onClose);

  return (
    <div className="live-preview__overlay-backdrop" onMouseDown={onClose}>
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-label={`Expanded artifact ${artifact.id}`}
        className="current-inspection__artifact-overlay"
        tabIndex={-1}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="live-preview__overlay-header">
          <div>
            <p className="cockpit__eyebrow">Run artifact</p>
            <h2>{artifact.id}</h2>
          </div>
          <div className="live-preview__overlay-controls">
            <button
              type="button"
              className="live-preview__icon-button"
              aria-label="Exit enlarged artifact"
              title="Exit enlarged view"
              onClick={onClose}
            >
              <MinimizeIcon />
            </button>
            <button
              type="button"
              className="live-preview__icon-button"
              aria-label="Close enlarged artifact"
              title="Close"
              onClick={onClose}
            >
              <CloseIcon />
            </button>
          </div>
        </header>
        <RenderedArtifact text={artifact.text} ariaLabel="Expanded artifact content" />
      </div>
    </div>
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
  waitReason,
}: {
  stage: TranscriptStageModel;
  view: PreviewView;
  selectedId: string | null;
  selectSession(id: string): void;
  waitReason?: string;
}): React.ReactElement {
  const emptyState =
    stage.cohort.length === 0 && waitReason !== undefined && waitReason.trim() !== '' ? (
      <p className="setup-step__empty">{waitReason}</p>
    ) : (
      <p className="setup-step__empty">Waiting for the agent to respond…</p>
    );

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
          emptyState={emptyState}
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
  waitReason,
  attentionFooter,
}: {
  onClose(): void;
  stage: TranscriptStageModel;
  view: PreviewView;
  onChangeView(next: PreviewView): void;
  selectedId: string | null;
  selectSession(id: string): void;
  preview: LivePreviewView | null;
  waitReason?: string;
  attentionFooter?: ReactNode;
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
        data-view={view}
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
          waitReason={waitReason}
        />
        {preview !== null || attentionFooter !== undefined ? (
          <footer className="live-preview__overlay-footer">
            {preview !== null ? (
              <div className="live-preview__overlay-status">
                <p className="current-inspection__activity">{preview.activity}</p>
                <PreviewMetrics preview={preview} />
              </div>
            ) : null}
            {attentionFooter !== undefined ? (
              <section className="live-preview__attention" aria-label="Agent request">
                <p className="cockpit__eyebrow">Your response</p>
                {attentionFooter}
              </section>
            ) : null}
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

/** Mirrors the TUI's implement-loop status verb for the active roadmap phase. */
export function roadmapStatusLabel(
  currentPhase: string,
  reviewGate: ReviewGateView,
  currentIteration: number | undefined,
  phaseStatus: string | undefined,
): string {
  const phase = currentPhase.trim().toLocaleLowerCase();
  const iteration =
    currentIteration !== undefined && currentIteration > 0
      ? ` · Iteration ${currentIteration}`
      : '';
  if (phase === 'implement') {
    const reviewing =
      reviewGate.reviewingGate || phaseStatus?.trim().toLocaleLowerCase() === 'reviewing';
    return `${reviewing ? 'Reviewing' : 'Implementing'}${iteration}`;
  }
  if (phase === 'plan' || phase === 'planning') {
    return reviewGate.validatingPlan ? 'Validating plan' : 'Planning';
  }
  return currentPhase;
}

function RoadmapGauge({
  currentPhase,
  featureStatus,
  currentRoadmapPhase,
  totalRoadmapPhases,
  currentIteration,
  phaseStatus,
  reviewGate,
}: {
  currentPhase: string;
  featureStatus?: string;
  currentRoadmapPhase?: number;
  totalRoadmapPhases?: number;
  currentIteration?: number;
  phaseStatus?: string;
  reviewGate: ReviewGateView;
}): React.ReactElement | null {
  if (
    currentRoadmapPhase === undefined ||
    totalRoadmapPhases === undefined ||
    currentRoadmapPhase < 1 ||
    totalRoadmapPhases < 1
  ) {
    return null;
  }
  const atRest = featureStatus !== undefined && isRunAtRest(featureStatus);
  const total = Math.max(totalRoadmapPhases, currentRoadmapPhase);
  const status = atRest
    ? displayStatusLabel(featureStatus)
    : roadmapStatusLabel(currentPhase, reviewGate, currentIteration, phaseStatus);
  return (
    <section
      className="roadmap-gauge"
      aria-label={`Roadmap progress: phase ${currentRoadmapPhase} of ${total} — ${status}`}
    >
      <div className="roadmap-gauge__reading">
        <span className="roadmap-gauge__eyebrow">Roadmap</span>
        <span className="roadmap-gauge__phase">
          Phase {currentRoadmapPhase}
          <span className="roadmap-gauge__of"> of {total}</span>
        </span>
      </div>
      <ol className="roadmap-gauge__track" aria-hidden="true">
        {Array.from({ length: total }, (_, index) => {
          const phaseNumber = index + 1;
          const state =
            phaseNumber < currentRoadmapPhase || atRest
              ? 'done'
              : phaseNumber === currentRoadmapPhase
                ? 'active'
                : 'upcoming';
          return (
            <li
              key={phaseNumber}
              className="roadmap-gauge__segment"
              data-state={state}
              title={`Phase ${phaseNumber}`}
            />
          );
        })}
      </ol>
      <p className="roadmap-gauge__status" data-tone={atRest ? 'rest' : 'working'}>
        {status}
      </p>
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
