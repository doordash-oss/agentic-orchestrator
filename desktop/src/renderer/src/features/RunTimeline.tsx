import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import type { SessionSummary, TranscriptMessage } from '../../../shared/ipc';
import { parseIpcError } from '../wizard/ipcError';
import {
  MAX_RENDERED_ENTRIES,
  reconcileTranscript,
  semanticTimeline,
  stripUnsafeAnsi,
  type SemanticEntry,
} from './timelineModel';
import { orderRunSessions, selectInitialRunSession, sessionDisplayLabel } from './reviewModel';

type StreamState = 'connecting' | 'live' | 'stale' | 'resetting' | 'unavailable';

const SESSION_DISCOVERY_RETRY_MS = 250;

export interface RunTimelineProps {
  featureId: string;
  activeRun: number;
  currentPhase: string;
  shouldStream?: boolean;
}

/** Read-only semantic/raw transcript inspection for sealed-run history. */
export function HistoricalTimeline({ messages }: { messages: readonly TranscriptMessage[] }) {
  const [selected, setSelected] = useState<TranscriptMessage | null>(null);
  const entries = useMemo(() => semanticTimeline(messages), [messages]);
  return (
    <div className="run-timeline__layout" data-history="true">
      <div className="run-timeline__reader">
        <div className="run-timeline__viewport" tabIndex={0} aria-label="Semantic timeline">
          {entries.length === 0 ? (
            <p className="setup-step__empty">This completed session has no transcript records.</p>
          ) : (
            <ol className="signal-trace">
              {entries.slice(-MAX_RENDERED_ENTRIES).map((entry) => (
                <TimelineEntry key={entry.id} entry={entry} onInspect={setSelected} />
              ))}
            </ol>
          )}
        </div>
      </div>
      <aside
        className="raw-inspector"
        aria-label="Raw record inspector"
        data-has-selection={selected !== null}
      >
        <div className="raw-inspector__heading">
          <h4>Validated source</h4>
        </div>
        {selected === null ? (
          <p>Select a trace entry to inspect its validated source record.</p>
        ) : (
          <pre>{JSON.stringify(selected, null, 2)}</pre>
        )}
      </aside>
    </div>
  );
}

export function RunTimeline({
  featureId,
  activeRun,
  currentPhase,
  shouldStream = true,
}: RunTimelineProps) {
  const [session, setSession] = useState<SessionSummary | null>(null);
  const [availableSessions, setAvailableSessions] = useState<SessionSummary[]>([]);
  const [messages, setMessages] = useState<TranscriptMessage[]>([]);
  const [streamState, setStreamState] = useState<StreamState>('connecting');
  const [streamDetail, setStreamDetail] = useState('Loading the current run…');
  const [selected, setSelected] = useState<TranscriptMessage | null>(null);
  const [followLive, setFollowLive] = useState(true);
  const [generation, setGeneration] = useState(0);
  const viewportRef = useRef<HTMLDivElement>(null);
  const subscriptionRef = useRef<string | null>(null);
  const globalStreamRef = useRef<'connecting' | 'live' | 'stale'>('live');
  const selectedSessionIdRef = useRef<string | null>(null);
  const runScopeRef = useRef('');

  useEffect(() => {
    return window.agentico.onAppEvent((event) => {
      if (event.type === 'status') {
        globalStreamRef.current = event.stream;
        if (event.stream === 'stale') setStreamState('stale');
        if (event.stream === 'connecting') setStreamState('resetting');
        if (event.stream === 'live' && subscriptionRef.current !== null) setStreamState('live');
      } else if (event.kind === 'resync') {
        setStreamState('resetting');
        setGeneration((value) => value + 1);
      } else if (
        event.kind === 'session.updated' &&
        (event.featureId === undefined || event.featureId === featureId)
      ) {
        // Start can advance the feature snapshot before the server has
        // registered its first session. The authoritative session
        // invalidation is the signal to retry discovery and backfill.
        setGeneration((value) => value + 1);
      }
    });
  }, [featureId]);

  useEffect(() => {
    let disposed = false;
    let subscriptionId: string | null = null;
    let discoveryRetry: ReturnType<typeof setTimeout> | null = null;
    const sessionIdRef = { current: '' };
    setStreamState(generation === 0 ? 'connecting' : 'resetting');

    const unsubscribe = window.agentico.onSessionOutput((event) => {
      if (disposed || event.sessionId !== sessionIdRef.current) return;
      if (subscriptionId !== null && event.subscriptionId !== subscriptionId) return;
      if (event.type === 'record') {
        setMessages((current) => reconcileTranscript(current, [event.message]));
        setStreamState('live');
        setStreamDetail('Receiving live output');
      } else if (event.type === 'done') {
        setStreamState('unavailable');
        setStreamDetail('This session has finished. Its transcript remains available.');
      } else {
        setStreamState('unavailable');
        setStreamDetail(event.error.message);
      }
    });

    const attachCurrentSession = async () => {
      try {
        const scope = `${featureId}:${activeRun}`;
        if (runScopeRef.current !== scope) {
          runScopeRef.current = scope;
          selectedSessionIdRef.current = null;
        }
        const sessions = orderRunSessions(
          (await window.agentico.listSessions()).filter(
            (candidate) => candidate.featureId === featureId && candidate.runNumber === activeRun,
          ),
        );
        if (!disposed) setAvailableSessions(sessions);
        const selectedSession =
          sessions.find((candidate) => candidate.id === selectedSessionIdRef.current) ??
          selectInitialRunSession(sessions);
        if (selectedSession === null) {
          if (!disposed) {
            setSession(null);
            if (shouldStream) {
              setStreamState(generation === 0 ? 'connecting' : 'resetting');
              setStreamDetail('Waiting for the current run session to register…');
              discoveryRetry = setTimeout(() => {
                discoveryRetry = null;
                void attachCurrentSession();
              }, SESSION_DISCOVERY_RETRY_MS);
            } else {
              setStreamState('unavailable');
              setStreamDetail('No session exists for the current run.');
            }
          }
          return;
        }
        selectedSessionIdRef.current = selectedSession.id;
        sessionIdRef.current = selectedSession.id;
        if (!disposed) setSession(selectedSession);
        await window.agentico.getSession(selectedSession.id);
        const backfill = await window.agentico.getSessionTranscript({
          sessionId: selectedSession.id,
          limit: 500,
        });
        if (disposed) return;
        setMessages(reconcileTranscript([], backfill.messages));
        if (!shouldStream || isTerminalSessionStatus(selectedSession.status)) {
          setStreamState('unavailable');
          setStreamDetail(
            shouldStream
              ? 'This session has finished. Its transcript remains available.'
              : 'Historical transcript · live output is closed',
          );
          return;
        }
        const opened = await window.agentico.openSessionOutput({
          sessionId: selectedSession.id,
          from: backfill.cursor.end,
        });
        if (disposed) {
          await window.agentico.cancelSessionOutput(opened.subscriptionId);
          return;
        }
        subscriptionId = opened.subscriptionId;
        subscriptionRef.current = opened.subscriptionId;
        setStreamState(
          globalStreamRef.current === 'live'
            ? 'live'
            : globalStreamRef.current === 'stale'
              ? 'stale'
              : 'resetting',
        );
        setStreamDetail('Backfill complete · live output connected');
      } catch (error) {
        if (!disposed) {
          setStreamState('unavailable');
          setStreamDetail(parseIpcError(error).message);
        }
      }
    };

    void attachCurrentSession();

    return () => {
      disposed = true;
      if (discoveryRetry !== null) clearTimeout(discoveryRetry);
      unsubscribe();
      if (subscriptionId !== null) void window.agentico.cancelSessionOutput(subscriptionId);
      if (subscriptionRef.current === subscriptionId) subscriptionRef.current = null;
    };
  }, [activeRun, currentPhase, featureId, generation, shouldStream]);

  const entries = useMemo(() => semanticTimeline(messages), [messages]);
  const visibleEntries = entries.slice(-MAX_RENDERED_ENTRIES);

  useLayoutEffect(() => {
    if (followLive && viewportRef.current !== null) {
      viewportRef.current.scrollTop = viewportRef.current.scrollHeight;
    }
  }, [followLive, visibleEntries.length]);

  useEffect(() => {
    if (selected === null) return;
    const replacement = messages.find((message) => message.index === selected.index) ?? null;
    if (replacement !== selected) setSelected(replacement);
  }, [messages, selected]);

  const jumpToLive = () => {
    setFollowLive(true);
    if (viewportRef.current !== null)
      viewportRef.current.scrollTop = viewportRef.current.scrollHeight;
  };

  return (
    <section className="run-timeline" aria-label="Current run timeline">
      <header className="run-timeline__header">
        <div>
          <h3 className="setup-step__title">Signal trace</h3>
          <p className="run-timeline__session">
            {session === null ? 'Current run' : `${session.phase} · ${session.kind}`}
          </p>
        </div>
        <p className="run-timeline__stream" data-state={streamState} role="status">
          <span aria-hidden="true">●</span> {streamState} · {streamDetail}
        </p>
      </header>

      {availableSessions.length > 1 ? (
        <div className="run-timeline__sessions" role="tablist" aria-label="Current run sessions">
          {availableSessions.map((candidate) => (
            <button
              key={candidate.id}
              type="button"
              role="tab"
              aria-selected={candidate.id === session?.id}
              data-status={candidate.status.toLocaleLowerCase()}
              onClick={() => {
                if (candidate.id === selectedSessionIdRef.current) return;
                selectedSessionIdRef.current = candidate.id;
                setGeneration((value) => value + 1);
              }}
            >
              <span>{sessionDisplayLabel(candidate)}</span>
              <span className="run-timeline__session-status">{candidate.status}</span>
            </button>
          ))}
        </div>
      ) : null}

      <div className="run-timeline__layout">
        <div className="run-timeline__reader">
          <div
            ref={viewportRef}
            className="run-timeline__viewport"
            tabIndex={0}
            onScroll={(event) => {
              const node = event.currentTarget;
              setFollowLive(node.scrollHeight - node.scrollTop - node.clientHeight < 24);
            }}
            aria-label="Semantic timeline"
          >
            {visibleEntries.length === 0 ? (
              <p className="setup-step__empty">Waiting for validated session output…</p>
            ) : (
              <ol className="signal-trace">
                {visibleEntries.map((entry) => (
                  <TimelineEntry key={entry.id} entry={entry} onInspect={setSelected} />
                ))}
              </ol>
            )}
          </div>
          {!followLive ? (
            <button type="button" className="run-timeline__jump" onClick={jumpToLive}>
              Jump to live
            </button>
          ) : null}
        </div>

        <aside
          className="raw-inspector"
          aria-label="Raw record inspector"
          data-has-selection={selected !== null}
        >
          <div className="raw-inspector__heading">
            <h4>Validated source</h4>
            {selected !== null ? (
              <button
                type="button"
                onClick={() => setSelected(null)}
                aria-label="Close raw inspector"
              >
                ✕
              </button>
            ) : null}
          </div>
          {selected === null ? (
            <p>Select a trace entry to inspect its validated source record.</p>
          ) : (
            <pre>{JSON.stringify(selected, null, 2)}</pre>
          )}
        </aside>
      </div>
    </section>
  );
}

function TimelineEntry({
  entry,
  onInspect,
}: {
  entry: SemanticEntry;
  onInspect(record: TranscriptMessage): void;
}) {
  if (entry.kind === 'routine-group') {
    return (
      <li className="signal-trace__entry" data-kind={entry.kind}>
        <details>
          <summary>{entry.text}</summary>
          <ul>
            {entry.records.map((record) => (
              <li key={record.index}>
                <button type="button" onClick={() => onInspect(record)}>
                  {record.tool ??
                    record.task?.description ??
                    record.fileChange?.path ??
                    record.type}
                </button>
              </li>
            ))}
          </ul>
        </details>
      </li>
    );
  }
  const record = entry.records[0]!;
  return (
    <li className="signal-trace__entry" data-kind={entry.kind}>
      <span className="signal-trace__label">{entry.label}</span>
      <p>{stripUnsafeAnsi(entry.text)}</p>
      <button
        type="button"
        onClick={() => onInspect(record)}
        aria-label={`Inspect raw record ${record.index}`}
      >
        raw
      </button>
    </li>
  );
}

function isTerminalSessionStatus(status: string): boolean {
  return [
    'complete',
    'completed',
    'done',
    'ended',
    'failed',
    'cancelled',
    'canceled',
    'stopped',
  ].includes(status.trim().toLocaleLowerCase());
}
