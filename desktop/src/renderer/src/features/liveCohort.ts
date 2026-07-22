import { CHAT_SESSION_ID, type SessionSummary } from '../../../shared/ipc';
import { orderRunSessions, sessionDisplayLabel } from './reviewModel';

export type CohortTabStatus = 'running' | 'completed' | 'failed';

const FAILED_STATUSES = ['failed', 'error', 'cancelled', 'canceled', 'stopped', 'crashed'];
const COMPLETED_STATUSES = ['complete', 'completed', 'done', 'ended', 'succeeded'];

function normalize(status: string): string {
  return status.trim().toLocaleLowerCase();
}

export function isTerminalSessionStatus(status: string): boolean {
  const normalized = normalize(status);
  return FAILED_STATUSES.includes(normalized) || COMPLETED_STATUSES.includes(normalized);
}

/** Coarse status marker shown on each cohort tab. */
export function cohortTabStatus(session: SessionSummary): CohortTabStatus {
  const normalized = normalize(session.status);
  if (FAILED_STATUSES.includes(normalized)) return 'failed';
  if (COMPLETED_STATUSES.includes(normalized)) return 'completed';
  return 'running';
}

function isChatSession(session: SessionSummary): boolean {
  return session.id === CHAT_SESSION_ID || normalize(session.kind) === 'chat';
}

export interface CohortMembership {
  /** Ordered session ids that make up the visible cohort. */
  sessionIds: string[];
  /** Feature phase this cohort was captured at; a change resets retention. */
  phase: string;
}

export const EMPTY_COHORT: CohortMembership = { sessionIds: [], phase: '' };

/**
 * Resolve the current concurrent cohort from the run's sessions.
 *
 * - Starts with every active non-chat session in the run.
 * - Retains completed members until the feature phase changes.
 * - Replaces the cohort when the retained batch is fully terminal and a
 *   disjoint active batch begins (e.g. a review retry).
 */
export function computeCohort(
  previous: CohortMembership,
  runSessions: readonly SessionSummary[],
  currentPhase: string,
): CohortMembership {
  const candidates = runSessions.filter((session) => !isChatSession(session));
  const byId = new Map(candidates.map((session) => [session.id, session]));
  const activeIds = candidates
    .filter((session) => !isTerminalSessionStatus(session.status))
    .map((session) => session.id);

  const allIds = candidates.map((session) => session.id);
  let memberIds: string[];
  if (currentPhase.trim() !== previous.phase.trim()) {
    // Fresh phase: the active batch, or the whole set for an already-terminal run.
    memberIds = activeIds.length > 0 ? activeIds : allIds;
  } else {
    const retained = previous.sessionIds.filter((id) => byId.has(id));
    const retainedAllTerminal =
      retained.length > 0 && retained.every((id) => isTerminalSessionStatus(byId.get(id)!.status));
    const activeNotRetained = activeIds.filter((id) => !retained.includes(id));
    memberIds =
      retainedAllTerminal && activeNotRetained.length > 0
        ? activeIds
        : [...retained, ...activeNotRetained];
  }
  // Never hide a run that only has terminal agents (failed or sealed current run).
  if (memberIds.length === 0) memberIds = allIds;

  const ordered = orderRunSessions(memberIds.map((id) => byId.get(id)!)).map(
    (session) => session.id,
  );
  return { sessionIds: ordered, phase: currentPhase };
}

/** Preserve the selected tab while it survives; otherwise prefer an active agent. */
export function resolveCohortSelection(
  cohort: readonly SessionSummary[],
  previousSelectedId: string | null,
): string | null {
  if (cohort.length === 0) return null;
  if (previousSelectedId !== null && cohort.some((session) => session.id === previousSelectedId)) {
    return previousSelectedId;
  }
  const active = cohort.find((session) => !isTerminalSessionStatus(session.status));
  return (active ?? cohort[0]!).id;
}

/** Human labels for each tab, disambiguated when two agents share a base label. */
export function cohortTabLabels(cohort: readonly SessionSummary[]): Map<string, string> {
  const base = new Map<string, string>();
  const counts = new Map<string, number>();
  for (const session of cohort) {
    const label = sessionDisplayLabel(session);
    base.set(session.id, label);
    counts.set(label, (counts.get(label) ?? 0) + 1);
  }

  const seen = new Map<string, number>();
  const resolved = new Map<string, string>();
  for (const session of cohort) {
    const label = base.get(session.id)!;
    if ((counts.get(label) ?? 0) <= 1) {
      resolved.set(session.id, label);
      continue;
    }
    const repo = session.repo?.trim();
    const repoDisambiguates =
      repo !== undefined &&
      repo !== '' &&
      cohort.filter((other) => base.get(other.id) === label && other.repo?.trim() === repo)
        .length === 1;
    if (repoDisambiguates) {
      resolved.set(session.id, `${label} · ${repo}`);
    } else {
      const ordinal = (seen.get(label) ?? 0) + 1;
      seen.set(label, ordinal);
      resolved.set(session.id, `${label} #${ordinal}`);
    }
  }
  return resolved;
}
