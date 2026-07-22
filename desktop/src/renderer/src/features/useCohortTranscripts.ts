import { useCallback, useEffect, useMemo, useState } from 'react';
import type { SessionSummary, TranscriptMessage } from '../../../shared/ipc';
import { reconcileMessages } from './transcript/conversation';
import {
  EMPTY_COHORT,
  computeCohort,
  isTerminalSessionStatus,
  resolveCohortSelection,
} from './liveCohort';

export interface CohortTranscripts {
  /** Ordered cohort sessions to render as tabs. */
  cohort: SessionSummary[];
  /** Live, per-session transcript rows keyed by session id. */
  transcripts: Record<string, TranscriptMessage[]>;
  selectedId: string | null;
  selectSession(id: string): void;
  /** Force a re-discovery of the run's sessions. */
  refresh(): void;
}

function membershipKey(sessionIds: readonly string[]): string {
  return sessionIds.join(',');
}

export function useCohortTranscripts(
  featureId: string,
  runNumber: number,
  currentPhase: string,
  shouldStream: boolean,
): CohortTranscripts {
  const [runSessions, setRunSessions] = useState<SessionSummary[]>([]);
  const [membership, setMembership] = useState(EMPTY_COHORT);
  const [transcripts, setTranscripts] = useState<Record<string, TranscriptMessage[]>>({});
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [generation, setGeneration] = useState(0);

  const refresh = useCallback(() => setGeneration((value) => value + 1), []);

  // Discover the run's sessions, then re-discover on relevant invalidations.
  useEffect(() => {
    let disposed = false;
    void window.agentico
      .listRunSessions({ featureId, runNumber })
      .then((result) => {
        if (!disposed) setRunSessions(result.sessions);
      })
      .catch(() => undefined);
    return () => {
      disposed = true;
    };
  }, [featureId, runNumber, generation]);

  useEffect(() => {
    return window.agentico.onAppEvent((event) => {
      if (event.type === 'status') return;
      if (
        event.kind === 'resync' ||
        (event.kind === 'session.updated' &&
          (event.featureId === undefined || event.featureId === featureId))
      ) {
        refresh();
      }
    });
  }, [featureId, refresh]);

  // Fold the discovered sessions into a stable, retention-aware cohort.
  useEffect(() => {
    setMembership((previous) => {
      const next = computeCohort(previous, runSessions, currentPhase);
      if (
        membershipKey(next.sessionIds) === membershipKey(previous.sessionIds) &&
        next.phase === previous.phase
      ) {
        return previous;
      }
      return next;
    });
  }, [runSessions, currentPhase]);

  const cohort = useMemo(
    () =>
      membership.sessionIds
        .map((id) => runSessions.find((session) => session.id === id))
        .filter((session): session is SessionSummary => session !== undefined),
    [membership, runSessions],
  );
  const cohortKey = membershipKey(membership.sessionIds);

  // Keep a valid selection as the cohort evolves.
  useEffect(() => {
    setSelectedId((previous) => resolveCohortSelection(cohort, previous));
  }, [cohort, cohortKey]);

  // Subscribe to output for every active cohort member; backfill terminal ones once.
  const activeKey = useMemo(
    () =>
      cohort
        .filter((session) => !isTerminalSessionStatus(session.status))
        .map((session) => session.id)
        .sort()
        .join(','),
    [cohort],
  );

  useEffect(() => {
    let disposed = false;
    const subscriptions: string[] = [];
    const members = membership.sessionIds;

    const unsubscribe = window.agentico.onSessionOutput((event) => {
      if (disposed || !members.includes(event.sessionId)) return;
      if (event.type !== 'record') return;
      setTranscripts((current) => ({
        ...current,
        [event.sessionId]: reconcileMessages(current[event.sessionId] ?? [], event.message),
      }));
    });

    const attach = async (session: SessionSummary): Promise<void> => {
      try {
        const backfill = await window.agentico.getSessionTranscript({
          sessionId: session.id,
          limit: 500,
        });
        if (disposed) return;
        setTranscripts((current) => ({
          ...current,
          [session.id]: reconcileMessages(current[session.id] ?? [], backfill.messages),
        }));
        if (!shouldStream || isTerminalSessionStatus(session.status)) return;
        const opened = await window.agentico.openSessionOutput({
          sessionId: session.id,
          from: backfill.cursor.end,
        });
        if (disposed) {
          void window.agentico.cancelSessionOutput(opened.subscriptionId);
          return;
        }
        subscriptions.push(opened.subscriptionId);
      } catch {
        // A session may deregister between discovery and attach; refresh handles it.
      }
    };

    void Promise.all(cohort.map(attach));

    return () => {
      disposed = true;
      unsubscribe();
      for (const id of subscriptions) void window.agentico.cancelSessionOutput(id);
    };
  }, [featureId, runNumber, cohortKey, activeKey, shouldStream, generation]);

  const selectSession = useCallback((id: string) => setSelectedId(id), []);

  return { cohort, transcripts, selectedId, selectSession, refresh };
}
