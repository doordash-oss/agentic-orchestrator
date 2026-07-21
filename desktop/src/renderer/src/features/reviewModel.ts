import type { SessionSummary } from '../../../shared/ipc';

export const REVIEW_AXIS_ORDER = [
  'Architecture',
  'Structural',
  'Grounding',
  'Security',
  'Performance',
  'Testing',
  'Scope',
  'Craft',
  'Functionality/Evidence',
  'Cleanliness',
  'QA',
  'Design',
] as const;

const AXIS_INDEX = new Map<string, number>(REVIEW_AXIS_ORDER.map((name, index) => [name, index]));

export function orderedReviewStatuses(
  statuses: Readonly<Record<string, string>>,
): Array<readonly [string, string]> {
  return Object.entries(statuses).sort(([left], [right]) => {
    const leftIndex = AXIS_INDEX.get(left) ?? REVIEW_AXIS_ORDER.length;
    const rightIndex = AXIS_INDEX.get(right) ?? REVIEW_AXIS_ORDER.length;
    return leftIndex - rightIndex || left.localeCompare(right);
  });
}

export function reviewAxisShortName(name: string): string {
  const known: Readonly<Record<string, string>> = {
    Architecture: 'Arch',
    Structural: 'Struct',
    Grounding: 'Ground',
    Security: 'Sec',
    Performance: 'Perf',
    Testing: 'Test',
    Scope: 'Scope',
    Craft: 'Craft',
    'Functionality/Evidence': 'Func',
    Cleanliness: 'Clean',
    QA: 'QA',
    Design: 'Design',
  };
  return known[name] ?? name;
}

export type ReviewStatusTone = 'passed' | 'failed' | 'running' | 'pending';

export function reviewStatusTone(status: string): ReviewStatusTone {
  const normalized = status.trim().toLocaleLowerCase();
  if (['approved', 'passed', 'done', 'complete', 'completed'].includes(normalized)) return 'passed';
  if (['changes_requested', 'failed', 'error'].includes(normalized)) return 'failed';
  if (['running', 'active', 'implementing', 'reviewing', 'finalreviewing'].includes(normalized)) {
    return 'running';
  }
  return 'pending';
}

export function reviewStatusSymbol(status: string): string {
  const symbols: Record<ReviewStatusTone, string> = {
    passed: '✓',
    failed: '✕',
    running: '⟳',
    pending: '○',
  };
  return symbols[reviewStatusTone(status)];
}

function sessionGroup(session: SessionSummary): number {
  const phase = session.phase.trim().toLocaleLowerCase();
  if (session.kind === 'repo-impl' || phase === 'implement') return 0;
  if (session.kind === 'validator') return 1;
  if (session.kind === 'review-helper') return 2;
  if (session.kind === 'phase') return 3;
  return 4;
}

/** Implementation first, then known review axes in their durable display order. */
export function orderRunSessions(sessions: readonly SessionSummary[]): SessionSummary[] {
  return sessions
    .map((session, index) => ({ session, index }))
    .sort((left, right) => {
      const groupDelta = sessionGroup(left.session) - sessionGroup(right.session);
      if (groupDelta !== 0) return groupDelta;
      if (sessionGroup(left.session) === 1) {
        const leftLabel = left.session.label ?? '';
        const rightLabel = right.session.label ?? '';
        const axisDelta =
          (AXIS_INDEX.get(leftLabel) ?? REVIEW_AXIS_ORDER.length) -
          (AXIS_INDEX.get(rightLabel) ?? REVIEW_AXIS_ORDER.length);
        if (axisDelta !== 0) return axisDelta;
        const labelDelta = leftLabel.localeCompare(rightLabel);
        if (labelDelta !== 0) return labelDelta;
      }
      return left.index - right.index;
    })
    .map(({ session }) => session);
}

function isWaitingStatus(status: string): boolean {
  const normalized = status.replaceAll(/[_\s-]/g, '').toLocaleLowerCase();
  return normalized === 'waitinghelp' || normalized === 'waitingpermission';
}

function isActiveStatus(status: string): boolean {
  return [
    'running',
    'active',
    'starting',
    'stopping',
    'implementing',
    'reviewing',
    'finalreviewing',
    'waiting',
  ].includes(status.replaceAll(/[_\s-]/g, '').toLocaleLowerCase());
}

/** Mirrors attach focus: blocking work, then active work, then stable first tab. */
export function selectInitialRunSession(
  orderedSessions: readonly SessionSummary[],
): SessionSummary | null {
  return (
    orderedSessions.find((session) => isWaitingStatus(session.status)) ??
    orderedSessions.find((session) => isActiveStatus(session.status)) ??
    orderedSessions[0] ??
    null
  );
}

export function sessionDisplayLabel(session: SessionSummary): string {
  return session.label ?? session.repo ?? `${session.phase} · ${session.kind}`;
}
