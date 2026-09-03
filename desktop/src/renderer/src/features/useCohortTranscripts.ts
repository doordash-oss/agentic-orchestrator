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

// The server only pushes session.updated when a session finishes, so a live
// run polls to pick up new sessions and late-bound fields (model, status).
const SESSION_DISCOVERY_REFRESH_MS = 3000;

export function useCohortTranscripts(
  featureId: string,
  runNumber: number,
  currentPhase: string,
  shouldStream: boolean,
  currentIteration?: number,
  currentReviewAxes?: readonly string[],
  active = true,
  onSessionSettled?: () => void,
): CohortTranscripts {
  const [runSessions, setRunSessions] = useState<SessionSummary[]>([]);
  const [membership, setMembership] = useState(EMPTY_COHORT);
  const [transcripts, setTranscripts] = useState<Record<string, TranscriptMessage[]>>({});
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [generation, setGeneration] = useState(0);

  const refresh = useCallback(() => setGeneration((value) => value + 1), []);

  // Discover the run's sessions, then re-discover on relevant invalidations
  // and, while streaming, on a foreground polling interval.
  useEffect(() => {
    if (!active) return;
    let disposed = false;
    const discover = (): void => {
      void window.agentico
        .listRunSessions({ featureId, runNumber })
        .then((result) => {
          if (!disposed) setRunSessions(result.sessions);
        })
        .catch(() => undefined);
    };
    discover();
    const interval = shouldStream
      ? setInterval(() => {
          if (!document.hidden) discover();
        }, SESSION_DISCOVERY_REFRESH_MS)
      : undefined;
    return () => {
      disposed = true;
      if (interval !== undefined) clearInterval(interval);
    };
  }, [active, featureId, runNumber, generation, shouldStream]);

  useEffect(() => {
    if (!active) return;
    return window.agentico.onAppEvent((event) => {
      if (event.type !== 'invalidated') return;
      if (
        event.kind === 'resync' ||
        (event.kind === 'session.updated' &&
          (event.featureId === undefined || event.featureId === featureId))
      ) {
        refresh();
      }
    });
  }, [active, featureId, refresh]);

  // Fold the discovered sessions into a stable, retention-aware cohort.
  useEffect(() => {
    setMembership((previous) => {
      const next = computeCohort(
        previous,
        runSessions,
        currentPhase,
        currentIteration,
        currentReviewAxes,
      );
      if (
        membershipKey(next.sessionIds) === membershipKey(previous.sessionIds) &&
        next.phase === previous.phase &&
        next.iteration === previous.iteration
      ) {
        return previous;
      }
      return next;
    });
  }, [runSessions, currentPhase, currentIteration, currentReviewAxes]);

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
    if (!active) return;
    let disposed = false;
    const subscriptions: string[] = [];
    const members = membership.sessionIds;

    const unsubscribe = window.agentico.onSessionOutput((event) => {
      if (disposed || !members.includes(event.sessionId)) return;
      if (event.type === 'done') {
        onSessionSettled?.();
        return;
      }
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
  }, [
    active,
    featureId,
    runNumber,
    cohortKey,
    activeKey,
    shouldStream,
    generation,
    onSessionSettled,
  ]);

  const selectSession = useCallback((id: string) => setSelectedId(id), []);

  return { cohort, transcripts, selectedId, selectSession, refresh };
}
