/*
Copyright 2026 DoorDash, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

/**
 * Archive mode: a persistent, read-only cockpit view for a sealed run.
 * Renders a run selector, a "Sealed run · Read only" band, a sealed-tone
 * phase rail, historical artifacts/logs/timeline scoped to the selected
 * run, and a "Return to current run" control. No mutation controls are
 * mounted. Current-run invalidations produce non-disruptive badges while
 * history stays pinned.
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import {
  type RunDetailView,
  type RunArtifactView,
  type RunLogView,
  type RunSessionsListResult,
  type SessionSummary,
  type SessionTranscript,
} from '../../../shared/ipc';
import { parseIpcError, type WizardError } from '../wizard/ipcError';
import { PhaseRail } from './PhaseRailRow';
import { archiveRailSegments, railTrio } from './phaseRail';
import {
  displayPhaseLabel,
  formatDuration,
  spineActiveIndexForPhase,
  spineStages,
} from './featureView';
import { HistoricalTimeline } from './RunTimeline';

export interface ArchiveModeProps {
  featureId: string;
  selectedRunNumber: number;
  /** Whether this retained archive may perform authoritative refresh work. */
  active?: boolean;
  pipeline?: string;
  /** Non-disruptive notices from the live current run while history stays pinned. */
  currentRunBadges?: { changed: boolean; attention: boolean };
  /** Returns to the live run — the run/iteration popup is the only switcher now. */
  onReturnToCurrent(): void;
}

type ArchiveState =
  | { phase: 'loading' }
  | { phase: 'error'; error: WizardError }
  | {
      phase: 'loaded';
      detail: RunDetailView | null;
    };

export function ArchiveMode(props: ArchiveModeProps) {
  const {
    featureId,
    selectedRunNumber,
    active = true,
    pipeline,
    currentRunBadges = { changed: false, attention: false },
    onReturnToCurrent,
  } = props;

  const [state, setState] = useState<ArchiveState>({ phase: 'loading' });
  const [artifacts, setArtifacts] = useState<RunArtifactView[]>([]);
  const [logs, setLogs] = useState<RunLogView[]>([]);
  const [artifactContent, setArtifactContent] = useState<string | null>(null);
  const [logContent, setLogContent] = useState<{
    label: string;
    text: string;
    offset: number;
  } | null>(null);
  const [selectedSession, setSelectedSession] = useState<SessionTranscript | null>(null);
  const [sessions, setSessions] = useState<RunSessionsListResult | null>(null);
  const [loadingArtifact, setLoadingArtifact] = useState(false);
  const [inspectionError, setInspectionError] = useState<WizardError | null>(null);
  const [artifactLoadError, setArtifactLoadError] = useState<WizardError | null>(null);
  const [sessionLoadError, setSessionLoadError] = useState<WizardError | null>(null);
  const loadRef = useRef(0);
  const selectionRef = useRef<string | null>(null);

  const load = useCallback(
    async (runNumber: number) => {
      const reqId = ++loadRef.current;
      try {
        const runDetail = await window.agentico.getRun({ featureId, runNumber });
        if (loadRef.current !== reqId) return;
        setState({ phase: 'loaded', detail: runDetail });
      } catch (err) {
        if (loadRef.current !== reqId) return;
        setState({ phase: 'error', error: parseIpcError(err) });
      }
    },
    [featureId],
  );

  useEffect(() => {
    const selection = `${featureId}:${selectedRunNumber}`;
    if (selectionRef.current !== selection) {
      selectionRef.current = selection;
      setState({ phase: 'loading' });
    }
    if (!active) {
      loadRef.current += 1;
      return;
    }
    void load(selectedRunNumber);
    return () => {
      loadRef.current += 1;
    };
  }, [active, featureId, selectedRunNumber, load]);

  // Load artifacts + sessions for the selected run
  useEffect(() => {
    if (state.phase !== 'loaded') return;
    setArtifactContent(null);
    setLogContent(null);
    setSelectedSession(null);
    setArtifacts([]);
    setLogs([]);
    setSessions(null);
    setInspectionError(null);
    setArtifactLoadError(null);
    setSessionLoadError(null);
    Promise.all([
      window.agentico
        .listRunArtifacts({ featureId, runNumber: selectedRunNumber })
        .catch((error: unknown) => {
          setArtifactLoadError(parseIpcError(error));
          return { artifacts: [] };
        }),
      window.agentico
        .listRunSessions({ featureId, runNumber: selectedRunNumber })
        .catch((error: unknown) => {
          setSessionLoadError(parseIpcError(error));
          return { runNumber: selectedRunNumber, sessions: [] };
        }),
      window.agentico
        .listRunLogs({ featureId, runNumber: selectedRunNumber })
        .catch((error: unknown) => {
          setInspectionError(parseIpcError(error));
          return { logs: [] };
        }),
    ]).then(([arts, sess, runLogs]) => {
      setArtifacts(arts.artifacts);
      setSessions(sess);
      setLogs(runLogs.logs);
    });
  }, [featureId, selectedRunNumber, state.phase]);

  // App-event invalidation: refetch run list but stay pinned
  useEffect(() => {
    if (!active) return;
    const unsub = window.agentico.onAppEvent((event) => {
      if (
        event.type === 'invalidated' &&
        (event.kind === 'resync' || event.featureId === featureId)
      ) {
        void load(selectedRunNumber);
      }
    });
    return unsub;
  }, [active, featureId, selectedRunNumber, load]);

  const handleArtifactSelect = useCallback(
    async (artifactId: string) => {
      setLoadingArtifact(true);
      setArtifactContent(null);
      setInspectionError(null);
      try {
        const content = await window.agentico.getRunArtifactContent({
          featureId,
          runNumber: selectedRunNumber,
          artifactId,
        });
        setArtifactContent(content.text);
      } catch (error) {
        setInspectionError(parseIpcError(error));
      } finally {
        setLoadingArtifact(false);
      }
    },
    [featureId, selectedRunNumber],
  );

  const handleLogSelect = useCallback(
    async (log: RunLogView) => {
      setLoadingArtifact(true);
      setLogContent(null);
      setInspectionError(null);
      try {
        const content = await window.agentico.getRunLogContent({
          featureId,
          runNumber: selectedRunNumber,
          logId: log.id,
          offset: Math.max(0, log.size - 64 * 1024),
          limit: 64 * 1024,
        });
        setLogContent({ label: log.path, text: content.text, offset: content.offset });
      } catch (error) {
        setInspectionError(parseIpcError(error));
      } finally {
        setLoadingArtifact(false);
      }
    },
    [featureId, selectedRunNumber],
  );

  const handleSessionSelect = useCallback(async (sessionId: string) => {
    try {
      setInspectionError(null);
      setSelectedSession(await window.agentico.getSessionTranscript({ sessionId, limit: 500 }));
    } catch (error) {
      setSelectedSession(null);
      setInspectionError(parseIpcError(error));
    }
  }, []);

  if (state.phase === 'loading') {
    return <ArchiveShell state="loading" onReturnToCurrent={onReturnToCurrent} />;
  }

  if (state.phase === 'error') {
    return (
      <ArchiveShell state="error" onReturnToCurrent={onReturnToCurrent}>
        <div role="alert" className="archive-mode__error">
          {state.error.message}
        </div>
      </ArchiveShell>
    );
  }

  const { detail } = state;
  const stages = spineStages(pipeline);
  const activeIndex = detail?.currentPhase
    ? spineActiveIndexForPhase(detail.currentPhase, stages)
    : 0;
  // Trio from the sealed run's totals only — Context is omitted (never
  // "unavailable", just never asked for) and there is never a hold in
  // archive mode, regardless of the status the run carried when sealed.
  const railTrioEntries = railTrio({
    totalSeconds: detail?.timing?.totalSeconds,
    totalUsd: detail?.cost?.totalUsd,
    contextPercentage: undefined,
    hold: null,
  });

  return (
    <>
      <div className="archive-mode__band" role="status">
        <span className="archive-mode__band-label">Sealed run · Read only</span>
        {detail && (
          <span className="archive-mode__run-meta">
            Run {detail.runNumber}
            {detail.sealedAt ? ` · sealed ${new Date(detail.sealedAt).toLocaleDateString()}` : ''}
            {detail.sealReason ? ` · sealed by ${detail.sealReason.toLowerCase()}` : ''}
          </span>
        )}
        {currentRunBadges.changed && (
          <span className="archive-mode__badge" data-tone="changed" role="status">
            Current run changed
          </span>
        )}
        {currentRunBadges.attention && (
          <span className="archive-mode__badge" data-tone="attention" role="status">
            Current run needs attention
          </span>
        )}
      </div>

      <PhaseRail
        segments={archiveRailSegments(stages, activeIndex)}
        trio={railTrioEntries}
        hold={null}
        tone="sealed"
        label="Historical pipeline"
      />

      <div className="cockpit__content archive-mode__content">
        <main className="archive-mode__main">
          {detail && (
            <dl className="archive-mode__facts">
              {detail.currentPhase && (
                <div className="archive-mode__fact">
                  <dt>Phase</dt>
                  <dd>{displayPhaseLabel(detail.currentPhase)}</dd>
                </div>
              )}
              {detail.iteration !== undefined && detail.iteration > 0 && (
                <div className="archive-mode__fact">
                  <dt>Iteration</dt>
                  <dd>{detail.iteration}</dd>
                </div>
              )}
              {detail.roadmapPhase !== undefined && detail.roadmapTotal !== undefined && (
                <div className="archive-mode__fact">
                  <dt>Roadmap</dt>
                  <dd>
                    Phase {detail.roadmapPhase} of {detail.roadmapTotal}
                  </dd>
                </div>
              )}
              {detail.carriedFromRun !== undefined && detail.carriedFromRun > 0 && (
                <div className="archive-mode__fact">
                  <dt>Carried from</dt>
                  <dd>
                    <span className="archive-mode__carried-badge">Run {detail.carriedFromRun}</span>
                  </dd>
                </div>
              )}
              {detail.carriedPhases && detail.carriedPhases.length > 0 && (
                <div className="archive-mode__fact">
                  <dt>Carried phases</dt>
                  <dd>{detail.carriedPhases.map(displayPhaseLabel).join(', ')}</dd>
                </div>
              )}
              {detail.backupBranchRepos && detail.backupBranchRepos.length > 0 && (
                <div className="archive-mode__fact">
                  <dt>Backup repos</dt>
                  <dd>{detail.backupBranchRepos.join(', ')}</dd>
                </div>
              )}
              {detail.timing && (
                <div className="archive-mode__fact">
                  <dt>Duration</dt>
                  <dd>{formatDuration(detail.timing.totalSeconds)}</dd>
                </div>
              )}
              {detail.cost && detail.cost.totalUsd > 0 && (
                <div className="archive-mode__fact">
                  <dt>Cost</dt>
                  <dd>${detail.cost.totalUsd.toFixed(2)}</dd>
                </div>
              )}
            </dl>
          )}

          <div className="archive-mode__artifacts">
            {inspectionError !== null ? (
              <p className="archive-mode__error" role="alert">
                {inspectionError.message}
              </p>
            ) : null}
            <h3 className="archive-mode__section-title">Artifacts</h3>
            {artifactLoadError !== null ? (
              <p className="archive-mode__error" role="alert">
                Could not load artifacts: {artifactLoadError.message}
              </p>
            ) : artifacts.length === 0 ? (
              <p className="archive-mode__empty">No artifacts for this run.</p>
            ) : (
              <ul className="archive-mode__artifact-list">
                {artifacts.map((art) => (
                  <li key={art.id} className="archive-mode__artifact-item">
                    <button
                      className="archive-mode__artifact-button"
                      onClick={() => handleArtifactSelect(art.id)}
                      disabled={loadingArtifact}
                    >
                      {art.id}
                      {art.phase ? ` · ${displayPhaseLabel(art.phase)}` : ''}
                      {art.size !== undefined ? ` · ${formatBytes(art.size)}` : ''}
                    </button>
                  </li>
                ))}
              </ul>
            )}
            {artifactContent !== null && (
              <pre className="archive-mode__artifact-content" aria-label="Artifact content">
                {artifactContent.slice(0, 64 * 1024)}
                {artifactContent.length > 64 * 1024 ? '\n… (truncated)' : ''}
              </pre>
            )}
          </div>

          <div className="archive-mode__logs">
            <h3 className="archive-mode__section-title">Bounded logs</h3>
            <p className="archive-mode__hint">
              Read-only tails from authentic files in this sealed run. Live output is never opened.
            </p>
            {logs.length === 0 ? (
              <p className="archive-mode__empty">No logs were recorded for this run.</p>
            ) : (
              <ul className="archive-mode__artifact-list" aria-label="Available historical logs">
                {logs.map((log) => (
                  <li key={log.id} className="archive-mode__artifact-item">
                    <button
                      type="button"
                      className="archive-mode__artifact-button"
                      onClick={() => void handleLogSelect(log)}
                      disabled={loadingArtifact}
                      aria-label={`Open log ${log.path}`}
                    >
                      {log.path} · {formatBytes(log.size)}
                    </button>
                  </li>
                ))}
              </ul>
            )}
            {logContent !== null ? (
              <>
                <p className="archive-mode__hint">
                  {logContent.label}
                  {logContent.offset > 0 ? ' · Latest 64 KB' : ''}
                </p>
                <pre className="archive-mode__artifact-content" aria-label="Historical log content">
                  {logContent.text}
                </pre>
              </>
            ) : null}
          </div>

          {sessions && (
            <div className="archive-mode__sessions">
              <h3 className="archive-mode__section-title">Historical timeline</h3>
              {sessionLoadError !== null ? (
                <p className="archive-mode__error" role="alert">
                  Could not load historical sessions: {sessionLoadError.message}
                </p>
              ) : sessions.sessions.length === 0 ? (
                <p className="archive-mode__empty">
                  No completed sessions were recorded for this run.
                </p>
              ) : (
                <>
                  <p className="archive-mode__hint">
                    Choose a completed session to inspect its bounded transcript and raw records.
                  </p>
                  <ul className="archive-mode__session-list">
                    {sessions.sessions.map((sess: SessionSummary) => (
                      <li key={sess.id} className="archive-mode__session-item">
                        <button
                          type="button"
                          className="archive-mode__session-button"
                          onClick={() => void handleSessionSelect(sess.id)}
                        >
                          <span className="archive-mode__session-id">{sess.id}</span>
                          {sess.phase ? ` · ${displayPhaseLabel(sess.phase)}` : ''}
                          {sess.status ? ` · ${sess.status}` : ''}
                          {sess.startedAt ? ` · ${new Date(sess.startedAt).toLocaleString()}` : ''}
                        </button>
                      </li>
                    ))}
                  </ul>
                  {selectedSession !== null ? (
                    <HistoricalTimeline messages={selectedSession.messages} />
                  ) : null}
                </>
              )}
            </div>
          )}
        </main>
        <aside
          className="cockpit__inspector archive-mode__inspector"
          aria-label="Historical inspector"
        >
          <h3 className="archive-mode__section-title">Historical context</h3>
          <p className="archive-mode__hint">
            Every record below is bounded and scoped to Run {detail?.runNumber ?? selectedRunNumber}
            . Live output and lifecycle actions are unavailable in sealed history.
          </p>
          {detail?.sealedAt ? (
            <dl className="archive-mode__inspector-facts">
              <div>
                <dt>Sealed</dt>
                <dd>{new Date(detail.sealedAt).toLocaleString()}</dd>
              </div>
              {detail.sealReason ? (
                <div>
                  <dt>Reason</dt>
                  <dd>Sealed by {detail.sealReason.toLowerCase()}</dd>
                </div>
              ) : null}
            </dl>
          ) : null}
          <button className="archive-mode__return" onClick={onReturnToCurrent}>
            Return to current run
          </button>
        </aside>
      </div>
    </>
  );
}

function ArchiveShell({
  state,
  children,
  onReturnToCurrent,
}: {
  state: 'loading' | 'error';
  children?: React.ReactNode;
  onReturnToCurrent(): void;
}) {
  return (
    <>
      <div className="archive-mode__band">Sealed run · Read only</div>
      <div className="cockpit__content archive-mode__content" data-state={state}>
        <main className="archive-mode__main" aria-busy={state === 'loading'}>
          {children ?? <p className="archive-mode__loading">Loading run history…</p>}
        </main>
        <aside
          className="cockpit__inspector archive-mode__inspector"
          aria-label="Historical inspector"
        >
          <button className="archive-mode__return" onClick={onReturnToCurrent} autoFocus>
            Return to current run
          </button>
        </aside>
      </div>
    </>
  );
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}
