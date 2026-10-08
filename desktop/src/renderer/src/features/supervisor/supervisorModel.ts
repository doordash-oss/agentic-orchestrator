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
 * Pure helpers behind the Supervisor page: the copy it shows, how the model
 * catalogue becomes the chip's harness groups, the lifecycle predicates the
 * status line and the Send/Stop morph read, and how durable transcript
 * records plus live deltas become the shared conversation items.
 */
import {
  SUBAGENT_ORIGIN_LABEL,
  type CatalogueModel,
  type EffortLevel,
  type ModelCatalogue,
  type SupervisorLifecycle,
  type SupervisorMarker,
  type SupervisorRecord,
  type SupervisorSettings,
  type SupervisorState,
  type TranscriptMessage,
} from '../../../../shared/ipc';
import { EFFORT_LABELS } from '../ConfigEditor';
import {
  buildConversation,
  type ConversationItem,
  type ConversationNoticeTone,
} from '../transcript/conversation';

/** Stable user-visible strings; tests and packaged journeys select by these. */
export const SUPERVISOR_COPY = {
  title: 'Supervisor',
  composerLabel: 'Message the supervisor',
  placeholder: 'Message the supervisor',
  unsetPlaceholder: 'Choose a harness and model to start',
  pendingPlaceholder: 'Respond to the pending request above to continue',
  chipUnset: 'Choose harness and model',
  chipPopover: 'Harness and model',
  chipLocked: 'The harness and model are locked while the supervisor is running.',
  noModels: 'No usable models are available for this harness.',
  noHarnesses: 'No harnesses are available on this server.',
  loadingModels: 'Loading models…',
  effortDefault: 'Default',
  starting: 'Starting supervisor…',
  working: 'Working…',
  idle: 'Ready',
  paused: 'Paused — interrupted before restart. Send a message to continue.',
  failed: 'Supervisor failed — Retry',
  retry: 'Retry',
  retryNeedsText: 'Type a message to retry.',
  interruptedFooter: 'Interrupted',
  interruptedMarker: 'Interrupted before restart',
  launchFailedMarker: 'Supervisor failed to start',
  send: 'Send',
  stop: 'Stop',
  emptyHeading: 'Start a conversation with the supervisor.',
  emptyBody:
    'It runs on the harness and model you choose below and keeps this conversation across restarts.',
} as const;

/** "Rebuilding history for <Harness>…" while a launch rebuilds the native session. */
export function rebuildingStatus(harness: string): string {
  const label = harnessLabel(harness);
  return label === '' ? 'Rebuilding history…' : `Rebuilding history for ${label}…`;
}

/** The catalogue role whose eligible models a conversation may run on. */
export const SUPERVISOR_CATALOGUE_ROLE = 'chat';

const HARNESS_LABELS: Readonly<Record<string, string>> = {
  claude: 'Claude',
  codex: 'Codex',
  opencode: 'OpenCode',
};

/** The display name of a harness id: known harnesses by name, others capitalized. */
export function harnessLabel(harness: string): string {
  const known = HARNESS_LABELS[harness.toLocaleLowerCase()];
  if (known !== undefined) return known;
  return harness === '' ? harness : `${harness.charAt(0).toLocaleUpperCase()}${harness.slice(1)}`;
}

export interface SupervisorHarnessGroup {
  harness: string;
  label: string;
  /** Chat-eligible models in the server's order; empty means none are usable. */
  models: CatalogueModel[];
}

/**
 * Every detected harness in the catalogue's provider order, each with its
 * chat-eligible models. A harness with nothing eligible stays listed with an
 * empty model list, so the popover can say so instead of hiding it.
 */
export function supervisorHarnessGroups(catalogue: ModelCatalogue): SupervisorHarnessGroup[] {
  const eligibleByHarness = catalogue.phaseProviderModels[SUPERVISOR_CATALOGUE_ROLE] ?? {};
  return catalogue.providerOrder.map((harness) => {
    const known = catalogue.providerModels[harness] ?? [];
    const eligible = eligibleByHarness[harness] ?? [];
    return {
      harness,
      label: harnessLabel(harness),
      models: [...new Set(eligible)].map((id) => known.find((model) => model.id === id) ?? { id }),
    };
  });
}

export function findCatalogueModel(
  catalogue: ModelCatalogue | null,
  harness: string,
  model: string,
): CatalogueModel | undefined {
  return catalogue?.providerModels[harness]?.find(
    (candidate) => candidate.id === model || (candidate.aliases ?? []).includes(model),
  );
}

export function modelLabel(catalogue: ModelCatalogue | null, harness: string, model: string) {
  const displayName = findCatalogueModel(catalogue, harness, model)?.displayName;
  return displayName !== undefined && displayName !== '' ? displayName : model;
}

/** An empty effort is the harness default. */
export function effortLabel(effort: string): string {
  if (effort === '') return SUPERVISOR_COPY.effortDefault;
  return EFFORT_LABELS[effort as EffortLevel] ?? effort;
}

export function settingsChosen(settings: SupervisorSettings): boolean {
  return settings.harness !== '' && settings.model !== '';
}

/** "<Harness> <Model> · <Effort>" for committed settings, else the call to choose. */
export function supervisorChipLabel(
  settings: SupervisorSettings,
  catalogue: ModelCatalogue | null,
): string {
  if (!settingsChosen(settings)) return SUPERVISOR_COPY.chipUnset;
  return `${harnessLabel(settings.harness)} ${modelLabel(
    catalogue,
    settings.harness,
    settings.model,
  )} · ${effortLabel(settings.effort)}`;
}

/** A turn is in flight (or launching): Send becomes Stop. */
export function isTurnActive(lifecycle: SupervisorLifecycle): boolean {
  return (
    lifecycle === 'starting' ||
    lifecycle === 'running' ||
    lifecycle === 'waiting_permission' ||
    lifecycle === 'waiting_question'
  );
}

/** A provider process exists: the settings are locked until it ends. */
export function processExists(lifecycle: SupervisorLifecycle): boolean {
  return lifecycle !== 'stopped' && lifecycle !== 'failed';
}

/** The read-model fields the status line and its lamp read. */
export type SupervisorStatusInput = Pick<
  SupervisorState,
  'lifecycle' | 'startingStep' | 'lastTurnOutcome' | 'interruptedBy'
> & { harness: string };

/**
 * True when a server restart cut the last turn and nothing has run since:
 * the conversation is paused, not merely at rest. A turn the person stopped
 * keeps the resting label.
 */
export function isPausedByRestart(status: SupervisorStatusInput): boolean {
  return (
    status.lifecycle === 'stopped' &&
    status.lastTurnOutcome === 'interrupted' &&
    status.interruptedBy === 'shutdown'
  );
}

/**
 * The status line: rebuilding history outranks starting, which outranks
 * working, then a failed launch, then a restart-paused conversation, then
 * the resting label. An in-flight send on a conversation with no process is
 * a launch.
 */
export function supervisorStatusLine(status: SupervisorStatusInput, sending: boolean): string {
  const { lifecycle } = status;
  if (lifecycle === 'starting' && status.startingStep === 'rebuilding') {
    return rebuildingStatus(status.harness);
  }
  if (lifecycle === 'starting' || (sending && !processExists(lifecycle))) {
    return SUPERVISOR_COPY.starting;
  }
  if (isTurnActive(lifecycle) || sending) return SUPERVISOR_COPY.working;
  if (lifecycle === 'failed') return SUPERVISOR_COPY.failed;
  if (isPausedByRestart(status)) return SUPERVISOR_COPY.paused;
  return SUPERVISOR_COPY.idle;
}

/** Streamed text for one assistant message that has not committed yet. */
export interface ProvisionalReply {
  streamMessageId: string;
  generation: number;
  chunks: ReadonlyMap<number, string>;
}

export function provisionalText(reply: ProvisionalReply): string {
  return [...reply.chunks.entries()]
    .sort(([left], [right]) => left - right)
    .map(([, text]) => text)
    .join('');
}

/** Adds records to a seq-ordered list, deduplicated by seq (the newest copy wins). */
export function mergeRecords(
  current: readonly SupervisorRecord[],
  incoming: readonly SupervisorRecord[],
): SupervisorRecord[] {
  if (incoming.length === 0) return [...current];
  const bySeq = new Map(current.map((record) => [record.seq, record]));
  for (const record of incoming) bySeq.set(record.seq, record);
  return [...bySeq.values()].sort((left, right) => left.seq - right.seq);
}

function isRequestRecord(record: SupervisorRecord): boolean {
  return record.kind === 'permission' || record.kind === 'question';
}

function verdictText(record: SupervisorRecord, requestedSummary: string | undefined): string {
  const request = record.request!;
  if (request.origin === 'child') return subagentVerdictText(record, requestedSummary);
  // A request the restart cut was never answered: name what it was for.
  if (request.outcome === 'interrupted') {
    return `Interrupted · ${record.kind === 'question' ? 'Question' : request.toolName}`;
  }
  const summary = request.summary ?? requestedSummary;
  const detail = summary !== undefined && summary.trim() !== '' ? ` · ${summary.trim()}` : '';
  if (record.kind === 'question') {
    return detail === '' ? 'Answered the question' : `Answered${detail}`;
  }
  const verb = request.outcome === 'allowed' ? 'Allowed' : 'Denied';
  return `${verb} ${request.toolName}${detail}`;
}

/**
 * A sub-agent's verdict reads as dot-separated facts — what happened, to
 * what, who asked — so the origin sits in the same rhythm as the tool:
 * "Allowed · Bash · Sub-agent · make test".
 */
function subagentVerdictText(
  record: SupervisorRecord,
  requestedSummary: string | undefined,
): string {
  const request = record.request!;
  const subject = record.kind === 'question' ? 'Question' : request.toolName;
  const verb =
    request.outcome === 'interrupted'
      ? 'Interrupted'
      : record.kind === 'question'
        ? 'Answered'
        : request.outcome === 'allowed'
          ? 'Allowed'
          : 'Denied';
  const parts = [verb, subject, SUBAGENT_ORIGIN_LABEL];
  const summary = (request.summary ?? requestedSummary)?.trim();
  if (request.outcome !== 'interrupted' && summary !== undefined && summary !== '') {
    parts.push(summary);
  }
  return parts.join(' · ');
}

function verdictOutcome(
  record: SupervisorRecord,
): 'allowed' | 'denied' | 'answered' | 'interrupted' {
  const outcome = record.request?.outcome;
  if (outcome === 'interrupted') return 'interrupted';
  if (record.kind === 'question') return 'answered';
  return outcome === 'allowed' ? 'allowed' : 'denied';
}

const NOTICE_TONES: Readonly<Record<SupervisorMarker['marker'], ConversationNoticeTone>> = {
  interrupted: 'interrupted',
  error: 'failed',
  history_not_restored: 'caveat',
  permission_restricted: 'caveat',
  settings_changed: 'neutral',
  harness_change: 'neutral',
  settings_reverted: 'caveat',
  compacted: 'neutral',
};

/** The one-line copy of a marker; the interrupted notice reads the same whatever the server wrote. */
export function markerNoticeText(marker: SupervisorMarker): string {
  const text = marker.text.trim();
  switch (marker.marker) {
    case 'interrupted':
      return SUPERVISOR_COPY.interruptedMarker;
    case 'error':
      return text === ''
        ? SUPERVISOR_COPY.launchFailedMarker
        : `${SUPERVISOR_COPY.launchFailedMarker} · ${text}`;
    case 'compacted':
      return marker.code === 'checkpoint_unavailable'
        ? 'Conversation compacted · history checkpoint unavailable'
        : 'Conversation compacted';
    default:
      return text;
  }
}

export interface SupervisorConversationOptions {
  /** The just-sent text, shown until its committed record arrives. */
  optimistic?: string | null;
  provisional?: readonly ProvisionalReply[];
}

/**
 * Projects committed records into the shared conversation items: content
 * records run through the existing builder one turn at a time (so finished
 * turns fold behind its activity labels), resolved permission and question
 * records collapse to one-line verdicts at their place in the stream, and
 * requested-stage records render nothing — a live request is the pending
 * card below the transcript, and its verdict replaces it once answered.
 * Marker records become one-line notices; an interrupted marker also marks
 * the newest assistant message of its turn with an "Interrupted" footer.
 * Provisional replies and the optimistic user row trail the committed history.
 */
export function buildSupervisorConversation(
  records: readonly SupervisorRecord[],
  options: SupervisorConversationOptions = {},
): ConversationItem[] {
  const requestedSummaries = new Map<string, string>();
  for (const record of records) {
    if (record.kind === 'note' || record.kind === 'checkpoint') continue;
    const summary = record.request?.summary;
    if (record.request?.stage === 'requested' && summary !== undefined) {
      requestedSummaries.set(record.request.requestId, summary);
    }
  }

  const items: ConversationItem[] = [];
  // The turn each item belongs to, index-aligned with `items`.
  const itemTurns: string[] = [];
  const push = (turnId: string, produced: readonly ConversationItem[]): void => {
    items.push(...produced);
    itemTurns.push(...produced.map(() => turnId));
  };
  let segment: TranscriptMessage[] = [];
  let segmentTurn = '';
  const flush = (): void => {
    if (segment.length === 0) return;
    push(segmentTurn, buildConversation(segment, { mode: 'chat' }));
    segment = [];
  };
  const footInterrupted = (turnId: string): void => {
    if (turnId === '') return;
    for (let index = items.length - 1; index >= 0 && itemTurns[index] === turnId; index -= 1) {
      const item = items[index];
      if (item?.kind === 'message' && item.role === 'assistant') {
        items[index] = { ...item, footer: SUPERVISOR_COPY.interruptedFooter };
        return;
      }
    }
  };
  for (const record of records) {
    if (record.kind === 'note' || record.kind === 'checkpoint') continue;
    if (record.kind === 'marker') {
      flush();
      const marker = record.marker;
      if (marker === undefined) continue;
      if (marker.marker === 'interrupted') footInterrupted(record.turnId);
      const text = markerNoticeText(marker);
      if (text === '') continue;
      push(record.turnId, [
        {
          kind: 'notice',
          key: `notice-${record.seq}`,
          tone: marker.code === 'checkpoint_unavailable' ? 'caveat' : NOTICE_TONES[marker.marker],
          text,
          ...(marker.marker === 'compacted' && marker.summary !== undefined && marker.summary !== ''
            ? { summary: marker.summary, summaryTruncated: marker.truncated }
            : {}),
        },
      ]);
      continue;
    }
    if (!isRequestRecord(record)) {
      if (record.turnId !== segmentTurn) flush();
      segmentTurn = record.turnId;
      segment.push(...record.messages);
      continue;
    }
    const request = record.request;
    if (request === undefined || request.stage !== 'resolved') continue;
    flush();
    push(record.turnId, [
      {
        kind: 'verdict',
        key: `verdict-${record.seq}`,
        outcome: verdictOutcome(record),
        text: verdictText(record, requestedSummaries.get(request.requestId)),
      },
    ]);
  }
  flush();

  const committedStreams = new Set(
    records.flatMap((record) =>
      record.streamMessageId === undefined ? [] : [record.streamMessageId],
    ),
  );
  for (const reply of options.provisional ?? []) {
    if (committedStreams.has(reply.streamMessageId)) continue;
    const text = provisionalText(reply);
    if (text.trim() === '') continue;
    items.push({
      kind: 'message',
      key: `provisional-${reply.streamMessageId}`,
      role: 'assistant',
      text,
    });
  }

  const optimistic = options.optimistic?.trim();
  if (optimistic !== undefined && optimistic !== '') {
    items.push({ kind: 'message', key: 'optimistic-message', role: 'user', text: optimistic });
  }
  return items;
}
