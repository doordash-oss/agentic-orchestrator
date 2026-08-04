import { useCallback, useEffect, useId, useMemo, useRef, useState, type ReactNode } from 'react';
import type {
  LivePreviewView,
  ModelCatalogue,
  ReviewGateView,
  RunArtifactsListResult,
  RunDetailView,
  RunLogView,
  RunTextContent,
  SessionSummary,
  TranscriptMessage,
  VerificationItemView,
} from '../../../shared/ipc';
import { parseIpcError } from '../wizard/ipcError';
import {
  orderedReviewStatuses,
  reviewAxisShortName,
  reviewStatusSymbol,
  reviewStatusTone,
} from './reviewModel';
import {
  isVerifyingPhase,
  verificationCounts,
  verificationSymbol,
  verificationTone,
} from './verificationModel';
import {
  displayModelName,
  displayStatusLabel,
  formatDuration,
  isRunAtRest,
  phaseMetric,
} from './featureView';
import { stripUnsafeAnsi } from './timelineModel';
import { buildConversation, type BuildConversationOptions } from './transcript/conversation';
import { ConversationTranscript } from './transcript/ConversationTranscript';
import { HistoricalTimeline } from './RunTimeline';
import { useCohortTranscripts } from './useCohortTranscripts';
import {
  cohortSections,
  cohortTabLabels,
  cohortTabStatus,
  isTerminalSessionStatus,
} from './liveCohort';
import { renderSanitizedMarkdown } from './sanitizedMarkdown';
import { CloseIcon, MaximizeIcon, MinimizeIcon } from '../components/icons';
import { useModalDismiss } from '../components/useModalDismiss';

const IDLE_ACTIVITY_LABEL = 'Thinking through the next step';
const LIVE_METRICS_REFRESH_MS = 1000;

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

type PreviewView = 'conversation' | 'trace' | 'files';

export interface CurrentRunInspectionProps {
  featureId: string;
  runNumber: number;
  currentPhase: string;
  /** Server feature status; distinguishes a resting run from an active one. */
  featureStatus?: string;
  currentRoadmapPhase?: number;
  totalRoadmapPhases?: number;
  /** Implement-loop iteration within the current roadmap phase. */
  currentIteration?: number;
  /** Mid-flight status from the server ("implementing" | "reviewing" | "verifying"). */
  phaseStatus?: string;
  reviewGate: ReviewGateView;
  /** Ordered per-command harness verification state during "verifying". */
  verificationItems?: VerificationItemView[];
  waitReason?: string;
  /** Only open live SSE while the run is actually streaming. */
  shouldStream?: boolean;
  /** Opens the conversation overlay for a newly routed attention item. */
  attentionRequestId?: number;
  /** Response controls docked beneath the expanded conversation. */
  attentionFooter?: ReactNode;
  /** Pending question rendered as the newest turn inside the transcript. */
  attentionTurn?: ReactNode;
  /** Pending structured question whose raw transcript prose should be hidden. */
  suppressQuestion?: BuildConversationOptions['suppressQuestion'];
  onAttentionPreviewClose?(): void;
  /** Reports this run's totals up so the inspector sidebar can show them. */
  onRunMetrics?(metrics: RunMetrics | null): void;
  /** Record view supplies its own header label and hides live controls. */
  presentation?: 'regular' | 'record';
}

/** This run's cumulative totals, surfaced to the inspector sidebar. */
export interface RunMetrics {
  totalSeconds: number;
  totalUsd: number;
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

/** Friendly pipeline-stage label for an artifact id; falls back to the raw id. */
function artifactStageLabel(id: string): string {
  const normalized = id.trim().toLowerCase();
  switch (normalized) {
    case 'inquire':
      return 'Inquire';
    case 'research':
      return 'Research';
    case 'design':
      return 'Design';
    case 'roadmap':
      return 'Roadmap';
    case 'plan':
    case 'phase-plan':
      return 'Plan';
    default: {
      const phasePlan = /^phase-(\d+)-plan$/.exec(normalized);
      return phasePlan !== null ? `Phase ${Number(phasePlan[1])}` : id;
    }
  }
}

interface LogChannel {
  /** Directory prefix (with trailing slash), or '' for root-level logs. */
  channel: string;
  logs: RunLogView[];
}

/**
 * Logs are a stream, not a list: bucket the flat paths by their directory so
 * hundreds of `phase-06/verification-events/*` entries collapse into one
 * countable channel the reader can triage before drilling in.
 */
export function groupLogsByChannel(logs: readonly RunLogView[]): LogChannel[] {
  const buckets = new Map<string, RunLogView[]>();
  for (const log of logs) {
    const slash = log.path.lastIndexOf('/');
    const channel = slash >= 0 ? log.path.slice(0, slash + 1) : '';
    const bucket = buckets.get(channel);
    if (bucket === undefined) buckets.set(channel, [log]);
    else bucket.push(log);
  }
  return [...buckets.entries()]
    .map(([channel, channelLogs]) => ({ channel, logs: channelLogs }))
    .sort((a, b) => b.logs.length - a.logs.length || a.channel.localeCompare(b.channel));
}

/** Split a channel into a dim parent path and its highlighted trailing segment. */
function splitChannel(channel: string): { parent: string; leaf: string } {
  const trimmed = channel.endsWith('/') ? channel.slice(0, -1) : channel;
  if (trimmed === '') return { parent: '', leaf: '(root)' };
  const slash = trimmed.lastIndexOf('/');
  return slash >= 0
    ? { parent: trimmed.slice(0, slash + 1), leaf: trimmed.slice(slash + 1) }
    : { parent: '', leaf: trimmed };
}

interface OpenedContent {
  kind: 'artifact' | 'log';
  label: string;
  value: RunTextContent;
}

/**
 * The "Files" preview view: the artifact spine and log channels side by side.
 * It shares the transcript's pane, so browsing files never pushes the live
 * activity down the page; an opened file floats in a modal (see FileOverlay).
 */
function FilesSurface({
  artifacts,
  logs,
  logListError,
  loadingContent,
  contentError,
  onOpen,
}: {
  artifacts: RunArtifactsListResult['artifacts'];
  logs: RunLogView[];
  logListError: string | null;
  loadingContent: boolean;
  contentError: string | null;
  onOpen(kind: 'artifact' | 'log', id: string, size?: number, label?: string): void;
}): React.ReactElement {
  const channels = logs.length === 0 ? [] : groupLogsByChannel(logs);
  return (
    <div className="current-inspection__files" aria-label="Run files">
      {contentError !== null ? (
        <p role="alert" className="form-field__error">
          Could not open this file: {contentError}
        </p>
      ) : null}
      <div className="current-inspection__files-columns">
        <section className="current-inspection__files-column" aria-label="Run artifacts">
          <h4 className="current-inspection__files-heading">
            <span>Run artifacts</span>
            <span className="current-inspection__resource-count">{artifacts.length}</span>
          </h4>
          {artifacts.length === 0 ? (
            <p className="setup-step__empty">No current-run artifacts yet.</p>
          ) : (
            <ol className="current-inspection__spine">
              {artifacts.map((artifact) => (
                <li key={artifact.id}>
                  <button
                    type="button"
                    className="current-inspection__spine-node"
                    disabled={artifact.contentAvailable === false || loadingContent}
                    aria-label={`Open artifact ${artifact.id}`}
                    onClick={() => onOpen('artifact', artifact.id)}
                  >
                    <span className="current-inspection__spine-dot" aria-hidden="true" />
                    <span className="current-inspection__spine-stage">
                      {artifactStageLabel(artifact.id)}
                    </span>
                    <span className="current-inspection__spine-meta">
                      <span className="current-inspection__spine-id">{artifact.id}</span>
                      {artifact.size === undefined ? null : (
                        <span className="current-inspection__spine-size">
                          {' · '}
                          {formatBytes(artifact.size)}
                        </span>
                      )}
                    </span>
                  </button>
                </li>
              ))}
            </ol>
          )}
        </section>
        <section className="current-inspection__files-column" aria-label="Bounded logs">
          <h4 className="current-inspection__files-heading">
            <span>Bounded logs</span>
            <span className="current-inspection__resource-count">{logs.length}</span>
          </h4>
          {logs.length === 0 ? (
            <p className="setup-step__empty">
              {logListError === null
                ? 'No run logs yet.'
                : `Could not refresh run logs: ${logListError}`}
            </p>
          ) : (
            <ul className="current-inspection__channels" aria-label="Available run logs">
              {channels.map((group) => (
                <LogChannelGroup
                  key={group.channel}
                  channel={group.channel}
                  logs={group.logs}
                  share={Math.round((group.logs.length / logs.length) * 100)}
                  defaultExpanded={channels.length === 1}
                  disabled={loadingContent}
                  onOpen={(log) => onOpen('log', log.id, log.size, log.path)}
                />
              ))}
            </ul>
          )}
        </section>
      </div>
    </div>
  );
}

/**
 * One collapsible log channel: a share-metered header (count + proportion of
 * the run's total) that expands to the individual files beneath it. A lone
 * channel opens by default — there is nothing to triage.
 */
function LogChannelGroup({
  channel,
  logs,
  share,
  defaultExpanded,
  disabled,
  onOpen,
}: {
  channel: string;
  logs: RunLogView[];
  share: number;
  defaultExpanded: boolean;
  disabled: boolean;
  onOpen(log: RunLogView): void;
}): React.ReactElement {
  const [expanded, setExpanded] = useState(defaultExpanded);
  const contentId = useId();
  const { parent, leaf } = splitChannel(channel);
  const noun = logs.length === 1 ? 'file' : 'files';
  return (
    <li className="current-inspection__channel">
      <button
        type="button"
        className="current-inspection__channel-toggle"
        aria-expanded={expanded}
        aria-controls={contentId}
        aria-label={`${parent}${leaf} channel — ${logs.length} ${noun}`}
        onClick={() => setExpanded((value) => !value)}
      >
        <span className="current-inspection__channel-caret" aria-hidden="true" />
        <span className="current-inspection__channel-name">
          {parent === '' ? null : <span className="current-inspection__channel-dim">{parent}</span>}
          <span className="current-inspection__channel-leaf">{leaf}</span>
        </span>
        <span className="current-inspection__channel-count" aria-hidden="true">
          {logs.length}
        </span>
        <span className="current-inspection__channel-meter" aria-hidden="true">
          <span className="current-inspection__channel-fill" style={{ width: `${share}%` }} />
        </span>
      </button>
      {expanded ? (
        <ul id={contentId} className="current-inspection__channel-logs">
          {logs.map((log) => (
            <li key={log.id}>
              <button
                type="button"
                className="current-inspection__channel-log"
                disabled={disabled}
                aria-label={`Open log ${log.path}`}
                title={`${log.path} · ${formatBytes(log.size)}`}
                onClick={() => onOpen(log)}
              >
                <span className="current-inspection__channel-log-name">
                  {log.path.slice(channel.length) || log.path}
                </span>
                <span className="current-inspection__channel-log-size">
                  {formatBytes(log.size)}
                </span>
              </button>
            </li>
          ))}
        </ul>
      ) : null}
    </li>
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
  verificationItems,
  waitReason,
  shouldStream = true,
  attentionRequestId,
  attentionFooter,
  attentionTurn,
  suppressQuestion,
  onAttentionPreviewClose,
  onRunMetrics,
  presentation = 'regular',
}: CurrentRunInspectionProps): React.ReactElement {
  const [preview, setPreview] = useState<LivePreviewView | null>(null);
  const [runDetail, setRunDetail] = useState<RunDetailView | null>(null);
  const [modelCatalogue, setModelCatalogue] = useState<ModelCatalogue | null>(null);
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
  const [view, setView] = useState<PreviewView>('conversation');
  const requestRef = useRef(0);
  const catalogueRequestRef = useRef(0);

  const currentReviewAxes = useMemo(
    () =>
      reviewGate.reviewingGate
        ? orderedReviewStatuses(reviewGate.validatorStatuses).map(([name]) => name)
        : undefined,
    [reviewGate.reviewingGate, reviewGate.validatorStatuses],
  );
  const live = useCohortTranscripts(
    featureId,
    runNumber,
    currentPhase,
    shouldStream,
    currentIteration,
    currentReviewAxes,
  );
  const presentedCohort = live.cohort;
  const selectedSession =
    presentedCohort.find((session) => session.id === live.selectedId) ?? presentedCohort[0] ?? null;
  const metricPhase = currentPhase;
  const metricRoadmapPhase = currentRoadmapPhase;
  const stage = useTranscriptStage(
    presentedCohort,
    live.transcripts,
    selectedSession,
    preview,
    suppressQuestion,
  );
  const initialLoading =
    preview === null &&
    presentedCohort.length === 0 &&
    attentionFooter === undefined &&
    attentionTurn === undefined;
  // The review gate wins over a stale "verifying" marker: while an axis review
  // is active there is no harness contract running to display.
  const verifying = isVerifyingPhase(phaseStatus, verificationItems) && !reviewGate.reviewingGate;
  const closeFullscreen = useCallback(() => {
    setFullscreen(false);
    onAttentionPreviewClose?.();
  }, [onAttentionPreviewClose]);

  useEffect(() => {
    if (attentionRequestId === undefined) return;
    setView('conversation');
    setFullscreen(true);
  }, [attentionRequestId]);

  useEffect(() => {
    const request = ++catalogueRequestRef.current;
    void window.agentico
      .getModelCatalogue()
      .then((catalogue) => {
        if (request === catalogueRequestRef.current) setModelCatalogue(catalogue);
      })
      .catch(() => undefined);
    return () => {
      catalogueRequestRef.current += 1;
    };
  }, []);

  const refresh = useCallback(async () => {
    const request = ++requestRef.current;
    setError(null);
    try {
      const [nextPreview, nextArtifacts, logResult, nextRunDetail] = await Promise.all([
        window.agentico.getLivePreview(featureId),
        window.agentico.listRunArtifacts({ featureId, runNumber }),
        window.agentico
          .listRunLogs({ featureId, runNumber })
          .then((value) => ({ value, error: null }))
          .catch((cause: unknown) => ({
            value: { logs: [] as RunLogView[] },
            error: parseIpcError(cause).message,
          })),
        // Per-phase timing/cost. Absent for runs without recorded detail; a
        // failure must not blank the whole inspection, so it degrades to null.
        runNumber >= 1
          ? window.agentico.getRun({ featureId, runNumber }).catch(() => null)
          : Promise.resolve(null),
      ]);
      if (request !== requestRef.current) return;
      setPreview(nextPreview);
      setArtifacts(orderRunArtifacts(nextArtifacts.artifacts));
      setLogs(logResult.value.logs);
      setLogListError(logResult.error);
      setRunDetail(nextRunDetail);
      onRunMetrics?.(
        nextRunDetail === null
          ? { totalSeconds: nextPreview.totalSeconds, totalUsd: nextPreview.totalUsd }
          : {
              totalSeconds: nextRunDetail.timing?.totalSeconds ?? nextPreview.totalSeconds,
              totalUsd: nextRunDetail.cost?.totalUsd ?? nextPreview.totalUsd,
            },
      );
    } catch (cause) {
      if (request === requestRef.current) setError(parseIpcError(cause).message);
    }
  }, [featureId, runNumber, onRunMetrics]);

  // Clear the sidebar totals when the live surface goes away.
  useEffect(() => () => onRunMetrics?.(null), [onRunMetrics]);

  useEffect(() => {
    void refresh();
    return () => {
      requestRef.current += 1;
    };
  }, [refresh, metricPhase, metricRoadmapPhase]);

  useEffect(() => {
    if (!shouldStream || presentation === 'record' || runNumber < 1) return;
    let disposed = false;
    let inFlight = false;
    const poll = async (): Promise<void> => {
      if (inFlight || document.hidden) return;
      inFlight = true;
      try {
        const [nextRunDetail, nextPreview] = await Promise.all([
          window.agentico.getRun({ featureId, runNumber }),
          window.agentico.getLivePreview(featureId),
        ]);
        if (disposed) return;
        setRunDetail(nextRunDetail);
        setPreview(nextPreview);
        onRunMetrics?.({
          totalSeconds: nextRunDetail.timing?.totalSeconds ?? nextPreview.totalSeconds,
          totalUsd: nextRunDetail.cost?.totalUsd ?? nextPreview.totalUsd,
        });
      } catch {
        // Keep the last good snapshot; the next tick or manual refresh can recover.
      } finally {
        inFlight = false;
      }
    };
    const interval = setInterval(() => void poll(), LIVE_METRICS_REFRESH_MS);
    return () => {
      disposed = true;
      clearInterval(interval);
    };
  }, [featureId, onRunMetrics, presentation, runNumber, shouldStream]);

  // Switching runs must not leak the previous run's opened file into the new one.
  useEffect(() => {
    setContent(null);
    setContentError(null);
  }, [featureId, runNumber]);

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
      } catch (cause) {
        setContentError(parseIpcError(cause).message);
      } finally {
        setLoadingContent(false);
      }
    },
    [featureId, runNumber],
  );

  const filesSurface = (
    <FilesSurface
      artifacts={artifacts}
      logs={logs}
      logListError={logListError}
      loadingContent={loadingContent}
      contentError={contentError}
      onOpen={(kind, id, size, label) => void openContent(kind, id, size, label)}
    />
  );

  const livePreviewFrame = (
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
      {initialLoading ? (
        <p className="setup-step__empty">Loading current run inspection…</p>
      ) : view === 'files' ? (
        filesSurface
      ) : verifying && verificationItems !== undefined ? (
        <VerificationStage items={verificationItems} />
      ) : (
        <TranscriptStage
          stage={stage}
          view={view}
          selectedId={live.selectedId}
          selectSession={live.selectSession}
          waitReason={waitReason}
          attentionTurn={attentionTurn}
        />
      )}
    </div>
  );

  return (
    <section
      className="current-inspection"
      aria-label="Current run inspection"
      data-presentation={presentation}
    >
      <header className="current-inspection__header">
        <div>
          <p className="cockpit__eyebrow">
            {presentation === 'record' ? 'Sealed run' : 'Mutable current run'}
          </p>
          <h3 className="setup-step__title">
            {presentation === 'record' ? 'Activity and artifacts' : 'Live preview and files'}
          </h3>
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

      {initialLoading && presentation === 'record' ? (
        <RunRecordSkeleton />
      ) : initialLoading ? (
        <p className="setup-step__empty">Loading current run inspection…</p>
      ) : (
        <div className="current-inspection__preview">
          {livePreviewFrame}
          {!initialLoading && preview !== null ? (
            <>
              {verifying ? null : (
                <p className="current-inspection__activity">{preview.activity}</p>
              )}
              <PreviewMetrics
                preview={preview}
                runDetail={runDetail}
                currentPhase={metricPhase}
                currentRoadmapPhase={metricRoadmapPhase}
                modelCatalogue={modelCatalogue}
                model={preview.session?.model ?? selectedSession?.model ?? null}
                fallbackContextPercentage={selectedSession?.contextPercentage}
                verifying={verifying}
              />
            </>
          ) : null}
          {attentionFooter !== undefined && !fullscreen ? (
            <section className="live-preview__attention" aria-label="Agent request">
              {attentionFooter}
            </section>
          ) : null}
        </div>
      )}

      {initialLoading && presentation === 'record' ? null : (
        <>
          {presentation === 'record' ? (
            verifying && verificationItems !== undefined ? (
              <VerificationSummary items={verificationItems} />
            ) : (
              <ReviewGateSummary
                gate={reviewGate}
                currentPhase={currentPhase}
                currentRoadmapPhase={currentRoadmapPhase}
              />
            )
          ) : null}
        </>
      )}

      {fullscreen ? (
        <LivePreviewOverlay
          onClose={closeFullscreen}
          stage={stage}
          view={view}
          onChangeView={setView}
          selectedId={live.selectedId}
          selectSession={live.selectSession}
          preview={preview}
          runDetail={runDetail}
          currentPhase={metricPhase}
          currentRoadmapPhase={metricRoadmapPhase}
          modelCatalogue={modelCatalogue}
          model={preview?.session?.model ?? selectedSession?.model ?? null}
          fallbackContextPercentage={selectedSession?.contextPercentage}
          waitReason={waitReason}
          attentionFooter={attentionFooter}
          attentionTurn={attentionTurn}
          verifying={verifying}
          verificationItems={verificationItems}
          filesSurface={filesSurface}
        />
      ) : null}

      {content !== null ? <FileOverlay content={content} onClose={() => setContent(null)} /> : null}
    </section>
  );
}

function RunRecordSkeleton(): React.ReactElement {
  return (
    <div
      className="current-inspection__record-skeleton"
      aria-label="Loading run record"
      role="status"
    >
      <div className="current-inspection__record-skeleton-activity">
        <strong>Loading run record…</strong>
        <span />
        <span />
        <span />
      </div>
      <div className="current-inspection__record-skeleton-archive">
        <span />
        <span />
        <span />
        <span />
        <span />
      </div>
    </div>
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

/**
 * An opened file floats above everything so it stays visible no matter where
 * the run list has scrolled. Artifacts render as sanitized markdown; logs as a
 * plain, ANSI-stripped pre.
 */
function FileOverlay({
  content,
  onClose,
}: {
  content: OpenedContent;
  onClose(): void;
}): React.ReactElement {
  const dialogRef = useRef<HTMLDivElement>(null);
  useModalDismiss(dialogRef, onClose);
  const isArtifact = content.kind === 'artifact';

  return (
    <div className="live-preview__overlay-backdrop" onMouseDown={onClose}>
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-label={`${isArtifact ? 'Run artifact' : 'Run log'} ${content.label}`}
        className="current-inspection__artifact-overlay"
        tabIndex={-1}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <header className="live-preview__overlay-header">
          <div>
            <p className="cockpit__eyebrow">{isArtifact ? 'Run artifact' : 'Run log'}</p>
            <h2>{content.label}</h2>
            {content.kind === 'log' && content.value.offset > 0 ? (
              <p className="current-inspection__overlay-note">Latest 64 KB</p>
            ) : null}
            {content.value.truncated ? (
              <p className="current-inspection__overlay-note">
                Bounded page · more content remains
              </p>
            ) : null}
          </div>
          <div className="live-preview__overlay-controls">
            <button
              type="button"
              className="live-preview__icon-button"
              aria-label="Close file"
              title="Close"
              onClick={onClose}
            >
              <CloseIcon />
            </button>
          </div>
        </header>
        {isArtifact ? (
          <RenderedArtifact text={content.value.text} ariaLabel="Current run artifact content" />
        ) : (
          <pre className="current-inspection__overlay-log" aria-label="Current run log content">
            {stripUnsafeAnsi(content.value.text)}
          </pre>
        )}
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
  suppressQuestion?: BuildConversationOptions['suppressQuestion'],
): TranscriptStageModel {
  const usesFallback = cohort.length === 0;
  const rows = usesFallback
    ? (preview?.transcript ?? EMPTY_ROWS)
    : (transcripts[selectedSession?.id ?? ''] ?? EMPTY_ROWS);
  const activeSession = selectedSession ?? preview?.session ?? null;
  const items = useMemo(
    () =>
      buildConversation(rows, {
        mode: 'assistant-only',
        taskActivities: activeSession?.taskActivities ?? [],
        suppressQuestion,
      }),
    [activeSession?.taskActivities, rows, suppressQuestion],
  );
  const labels = useMemo(() => cohortTabLabels(cohort), [cohort]);
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
      <button
        type="button"
        className="live-preview__view"
        aria-pressed={view === 'files'}
        onClick={() => onChange('files')}
      >
        Files
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
  attentionTurn,
}: {
  stage: TranscriptStageModel;
  view: PreviewView;
  selectedId: string | null;
  selectSession(id: string): void;
  waitReason?: string;
  attentionTurn?: ReactNode;
}): React.ReactElement {
  const emptyState =
    stage.cohort.length === 0 && waitReason !== undefined && waitReason.trim() !== '' ? (
      <p className="setup-step__empty">{waitReason}</p>
    ) : (
      <p className="setup-step__empty">Waiting for the agent to respond…</p>
    );

  const withRoster = stage.cohort.length > 1;
  return (
    <div className={withRoster ? 'live-preview live-preview--cohort' : 'live-preview'}>
      {withRoster ? (
        <CohortRoster
          cohort={stage.cohort}
          labels={stage.labels}
          selectedId={selectedId}
          selectSession={selectSession}
        />
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
          trailing={attentionTurn}
        />
      ) : (
        <div className="live-preview__trace">
          <HistoricalTimeline messages={stage.rows} />
        </div>
      )}
    </div>
  );
}

/**
 * Grouped agent roster beside the transcript: the implementer, then the
 * review panel in its durable axis order. One leading mark per row — a
 * pulsing pip while running, ✓/✕ once terminal.
 */
function CohortRoster({
  cohort,
  labels,
  selectedId,
  selectSession,
}: {
  cohort: SessionSummary[];
  labels: Map<string, string>;
  selectedId: string | null;
  selectSession(id: string): void;
}): React.ReactElement {
  const sections = useMemo(() => cohortSections(cohort), [cohort]);

  const onKeyDown = (event: React.KeyboardEvent<HTMLDivElement>): void => {
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;
    const tabs = Array.from(
      event.currentTarget.querySelectorAll<HTMLButtonElement>('[role="tab"]'),
    );
    if (tabs.length === 0) return;
    const current = tabs.findIndex((tab) => tab === document.activeElement);
    const next =
      event.key === 'Home'
        ? 0
        : event.key === 'End'
          ? tabs.length - 1
          : event.key === 'ArrowDown'
            ? (current + 1) % tabs.length
            : (Math.max(current, 0) - 1 + tabs.length) % tabs.length;
    event.preventDefault();
    const target = tabs[next];
    if (target === undefined) return;
    target.focus();
    const id = target.dataset['sessionId'];
    if (id !== undefined) selectSession(id);
  };

  return (
    <div
      className="live-preview__roster"
      role="tablist"
      aria-label="Live agents"
      aria-orientation="vertical"
      onKeyDown={onKeyDown}
    >
      {sections.map((section) => (
        <div key={section.key} className="live-preview__roster-group" role="presentation">
          <p className="live-preview__roster-title" aria-hidden="true">
            {section.title}
          </p>
          {section.sessions.map((session) => {
            const status = cohortTabStatus(session);
            const label = labels.get(session.id) ?? session.id;
            return (
              <button
                key={session.id}
                type="button"
                role="tab"
                aria-selected={session.id === selectedId}
                aria-label={`${label} — ${status}`}
                tabIndex={session.id === selectedId ? 0 : -1}
                className="live-preview__agent"
                data-status={status}
                data-session-id={session.id}
                title={`${label} — ${status}`}
                onClick={() => selectSession(session.id)}
              >
                <span className="live-preview__agent-state" aria-hidden="true">
                  {status === 'running' ? (
                    <span className="live-preview__agent-pip" />
                  ) : status === 'completed' ? (
                    '✓'
                  ) : (
                    '✕'
                  )}
                </span>
                <span className="live-preview__agent-name">{label}</span>
              </button>
            );
          })}
        </div>
      ))}
    </div>
  );
}

/**
 * Current-scope run metrics beneath the live activity: the session's context,
 * and how long the current phase has run plus its cost and model. Run totals
 * live in the inspector sidebar, not here.
 */
function PreviewMetrics({
  preview,
  runDetail,
  currentPhase,
  currentRoadmapPhase,
  modelCatalogue,
  model,
  fallbackContextPercentage,
  verifying = false,
}: {
  preview: LivePreviewView;
  runDetail: RunDetailView | null;
  currentPhase: string;
  currentRoadmapPhase?: number;
  modelCatalogue: ModelCatalogue | null;
  model: string | null;
  fallbackContextPercentage?: number;
  verifying?: boolean;
}): React.ReactElement {
  const phaseSeconds = phaseMetric(runDetail?.timing?.byPhase, currentPhase, currentRoadmapPhase);
  const phaseUsd = phaseMetric(runDetail?.cost?.byPhase, currentPhase, currentRoadmapPhase);
  const contextPercentage =
    preview.contextPercentage >= 0
      ? preview.contextPercentage
      : (fallbackContextPercentage ?? preview.session?.contextPercentage ?? -1);
  return (
    <dl className="current-inspection__metrics">
      {/* The harness runs the contract with no live LLM session, so the
          context-window reading is stale during verification. */}
      {verifying ? null : (
        <div>
          <dt>Context</dt>
          <dd>{contextPercentage < 0 ? 'Unavailable' : `${contextPercentage}%`}</dd>
        </div>
      )}
      <div>
        <dt>Phase elapsed</dt>
        <dd>{phaseSeconds === undefined ? '—' : formatDuration(phaseSeconds)}</dd>
      </div>
      <div>
        <dt>Phase cost</dt>
        <dd className="current-inspection__cost">
          <span>{phaseUsd === undefined ? '—' : `$${phaseUsd.toFixed(2)}`}</span>
          {model !== null ? (
            <span className="current-inspection__model" title={model}>
              {displayModelName(model, modelCatalogue)}
            </span>
          ) : null}
        </dd>
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
  runDetail,
  currentPhase,
  currentRoadmapPhase,
  modelCatalogue,
  model,
  fallbackContextPercentage,
  waitReason,
  attentionFooter,
  attentionTurn,
  verifying,
  verificationItems,
  filesSurface,
}: {
  onClose(): void;
  stage: TranscriptStageModel;
  view: PreviewView;
  onChangeView(next: PreviewView): void;
  selectedId: string | null;
  selectSession(id: string): void;
  preview: LivePreviewView | null;
  runDetail: RunDetailView | null;
  currentPhase: string;
  currentRoadmapPhase?: number;
  modelCatalogue: ModelCatalogue | null;
  model: string | null;
  fallbackContextPercentage?: number;
  waitReason?: string;
  attentionFooter?: ReactNode;
  attentionTurn?: ReactNode;
  verifying: boolean;
  verificationItems?: VerificationItemView[];
  filesSurface: ReactNode;
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
        {view === 'files' ? (
          filesSurface
        ) : verifying && verificationItems !== undefined ? (
          <VerificationStage items={verificationItems} />
        ) : (
          <TranscriptStage
            stage={stage}
            view={view}
            selectedId={selectedId}
            selectSession={selectSession}
            waitReason={waitReason}
            attentionTurn={attentionTurn}
          />
        )}
        {preview !== null || attentionFooter !== undefined ? (
          <footer className="live-preview__overlay-footer">
            {preview !== null ? (
              <div className="live-preview__overlay-status">
                {verifying ? null : (
                  <p className="current-inspection__activity">{preview.activity}</p>
                )}
                <PreviewMetrics
                  preview={preview}
                  runDetail={runDetail}
                  currentPhase={currentPhase}
                  currentRoadmapPhase={currentRoadmapPhase}
                  modelCatalogue={modelCatalogue}
                  model={model}
                  fallbackContextPercentage={fallbackContextPercentage}
                  verifying={verifying}
                />
              </div>
            ) : null}
            {attentionFooter !== undefined ? (
              <section className="live-preview__attention" aria-label="Agent request">
                {attentionFooter}
              </section>
            ) : null}
          </footer>
        ) : null}
      </div>
    </div>
  );
}

/** Status verb for the active roadmap phase of the implement loop. */
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
    const normalizedStatus = phaseStatus?.trim().toLocaleLowerCase();
    const reviewing = reviewGate.reviewingGate || normalizedStatus === 'reviewing';
    if (!reviewing && normalizedStatus === 'verifying') {
      return `Verifying implementation${iteration}`;
    }
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
  // Final review is a whole-feature stage after every roadmap phase is built,
  // not another implementation phase — so it gets its own separated marker
  // rather than lighting up the last phase segment.
  const finalReview =
    !atRest &&
    (currentPhase.trim().toLocaleLowerCase() === 'final review' ||
      (featureStatus?.startsWith('FinalReview') ?? false));
  const status = atRest
    ? displayStatusLabel(featureStatus)
    : roadmapStatusLabel(currentPhase, reviewGate, currentIteration, phaseStatus);
  const ariaLabel = finalReview
    ? `Roadmap progress: final review — ${status}`
    : `Roadmap progress: phase ${currentRoadmapPhase} of ${total} — ${status}`;
  return (
    <section className="roadmap-gauge" aria-label={ariaLabel}>
      <div className="roadmap-gauge__reading">
        <span className="roadmap-gauge__eyebrow">Roadmap</span>
        {finalReview ? (
          <span className="roadmap-gauge__phase">Final review</span>
        ) : (
          <span className="roadmap-gauge__phase">
            Phase {currentRoadmapPhase}
            <span className="roadmap-gauge__of"> of {total}</span>
          </span>
        )}
      </div>
      <ol className="roadmap-gauge__track" aria-hidden="true">
        {Array.from({ length: total }, (_, index) => {
          const phaseNumber = index + 1;
          const state =
            finalReview || phaseNumber < currentRoadmapPhase || atRest
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
        {finalReview ? (
          <li
            className="roadmap-gauge__segment roadmap-gauge__segment--final"
            data-state="active"
            title="Final review"
          />
        ) : null}
      </ol>
      <p className="roadmap-gauge__status" data-tone={atRest ? 'rest' : 'working'}>
        {status}
      </p>
    </section>
  );
}

/**
 * Replaces the live transcript while the harness runs the testing contract:
 * there is no agent session to watch, so a stale prior-session transcript
 * would masquerade as live. Renders a per-command execution log instead.
 */
function VerificationStage({
  items,
}: {
  items: readonly VerificationItemView[];
}): React.ReactElement {
  return (
    <div className="live-preview">
      <div className="live-preview__verification" aria-label="Verification progress">
        <p className="setup-step__empty">
          Verification in progress — no agent session to watch; see the live preview.
        </p>
        <ul className="live-preview__verification-log">
          {items.map((item, index) => (
            <li key={`${item.name}-${index}`} data-status={verificationTone(item.state)}>
              <span aria-hidden="true">{verificationSymbol(item.state)}</span>
              <span className="live-preview__verification-name">{item.name}</span>
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}

/** Overview counts and per-command status for an active harness contract. */
function VerificationSummary({
  items,
}: {
  items: readonly VerificationItemView[];
}): React.ReactElement {
  const counts = verificationCounts(items);
  return (
    <section className="review-gate" aria-label="Verification">
      <div className="review-gate__heading">
        <div>
          <span className="review-gate__eyebrow">Verification</span>
          <h4>
            Verifying implementation · {counts.done}/{counts.total}
          </h4>
        </div>
      </div>
      <ul className="review-gate__axes" aria-label="Verification commands">
        {items.map((item, index) => (
          <li
            key={`${item.name}-${index}`}
            data-status={verificationTone(item.state)}
            title={`${item.name}: ${item.state}`}
          >
            <span>{item.name}</span>
            <span aria-hidden="true">{verificationSymbol(item.state)}</span>
          </li>
        ))}
      </ul>
      <p className="review-gate__counts">
        {counts.done}/{counts.total} complete
        {counts.failed > 0 ? ` · ✕${counts.failed}` : ''}
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

  const target = reviewGateTarget(gate.reviewingGate, currentPhase, currentRoadmapPhase);
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

function reviewGateTarget(
  reviewingGate: boolean,
  currentPhase: string,
  currentRoadmapPhase?: number,
): string {
  if (reviewingGate) {
    return currentPhase.trim().toLocaleLowerCase() === 'implement'
      ? 'Reviewing implementation'
      : 'Final review';
  }
  return currentRoadmapPhase === undefined
    ? 'Validating plan'
    : `Validating Phase ${currentRoadmapPhase} plan`;
}
