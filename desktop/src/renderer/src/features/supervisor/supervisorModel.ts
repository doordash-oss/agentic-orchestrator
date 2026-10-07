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
import type {
  CatalogueModel,
  EffortLevel,
  ModelCatalogue,
  SupervisorLifecycle,
  SupervisorRecord,
  SupervisorSettings,
  TranscriptMessage,
} from '../../../../shared/ipc';
import { EFFORT_LABELS } from '../ConfigEditor';
import { buildConversation, type ConversationItem } from '../transcript/conversation';

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
  send: 'Send',
  stop: 'Stop',
  emptyHeading: 'Start a conversation with the supervisor.',
  emptyBody:
    'It runs on the harness and model you choose below and keeps this conversation across restarts.',
} as const;

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

/**
 * The status line: starting outranks working, which outranks the resting
 * label. An in-flight send on a conversation with no process is a launch.
 */
export function supervisorStatusLine(lifecycle: SupervisorLifecycle, sending: boolean): string {
  if (lifecycle === 'starting' || (sending && !processExists(lifecycle))) {
    return SUPERVISOR_COPY.starting;
  }
  if (isTurnActive(lifecycle) || sending) return SUPERVISOR_COPY.working;
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
  const summary = request.summary ?? requestedSummary;
  const detail = summary !== undefined && summary.trim() !== '' ? ` · ${summary.trim()}` : '';
  if (record.kind === 'question') {
    return detail === '' ? 'Answered the question' : `Answered${detail}`;
  }
  const verb = request.outcome === 'allowed' ? 'Allowed' : 'Denied';
  return `${verb} ${request.toolName}${detail}`;
}

export interface SupervisorConversationOptions {
  /** The just-sent text, shown until its committed record arrives. */
  optimistic?: string | null;
  provisional?: readonly ProvisionalReply[];
}

/**
 * Projects committed records into the shared conversation items: content
 * records run through the existing builder (so finished turns fold behind
 * its activity labels), resolved permission and question records collapse to
 * one-line verdicts at their place in the stream, and requested-stage
 * records render nothing — a live request is the pending card below the
 * transcript, and its verdict replaces it once answered. Provisional replies
 * and the optimistic user row trail the committed history.
 */
export function buildSupervisorConversation(
  records: readonly SupervisorRecord[],
  options: SupervisorConversationOptions = {},
): ConversationItem[] {
  const requestedSummaries = new Map<string, string>();
  for (const record of records) {
    const summary = record.request?.summary;
    if (record.request?.stage === 'requested' && summary !== undefined) {
      requestedSummaries.set(record.request.requestId, summary);
    }
  }

  const items: ConversationItem[] = [];
  let segment: TranscriptMessage[] = [];
  const flush = (): void => {
    if (segment.length === 0) return;
    items.push(...buildConversation(segment, { mode: 'chat' }));
    segment = [];
  };
  for (const record of records) {
    if (!isRequestRecord(record)) {
      segment.push(...record.messages);
      continue;
    }
    const request = record.request;
    if (request === undefined || request.stage !== 'resolved') continue;
    flush();
    items.push({
      kind: 'verdict',
      key: `verdict-${record.seq}`,
      outcome:
        record.kind === 'question'
          ? 'answered'
          : request.outcome === 'allowed'
            ? 'allowed'
            : 'denied',
      text: verdictText(record, requestedSummaries.get(request.requestId)),
    });
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
