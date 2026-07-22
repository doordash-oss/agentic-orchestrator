import type { TranscriptMessage } from '../../../../shared/ipc';

/** Upper bound on retained rows; keeps the live preview and AMA memory-safe. */
export const MAX_TRANSCRIPT_MESSAGES = 200;

/** Row types that are machinery, never shown as conversation. */
const SUPPRESSED_TYPES = ['usage_update', 'success', 'result', 'system', 'prompt'];

export type ConversationMode = 'chat' | 'assistant-only';

export type ConversationItem =
  | { kind: 'message'; key: string; role: 'user' | 'assistant'; text: string }
  | {
      kind: 'auto-pick';
      key: string;
      question: string;
      answer: string;
      confidence?: number;
    }
  | {
      kind: 'file-change';
      key: string;
      change: NonNullable<TranscriptMessage['fileChange']>;
    }
  | { kind: 'activity'; key: string; labels: string[] };

/** Stable identity for a row, unique across multi-block responses. */
export function messageKey(entry: Pick<TranscriptMessage, 'index' | 'blockIndex'>): string {
  return `${entry.index}:${entry.blockIndex ?? 0}`;
}

/** Merge incoming rows into an existing set, deduped by composite block key. */
export function reconcileMessages(
  messages: readonly TranscriptMessage[],
  incoming: TranscriptMessage | readonly TranscriptMessage[],
): TranscriptMessage[] {
  const byKey = new Map(messages.map((message) => [messageKey(message), message]));
  for (const message of Array.isArray(incoming) ? incoming : [incoming]) {
    byKey.set(messageKey(message), message);
  }
  return [...byKey.values()]
    .sort(
      (left, right) => left.index - right.index || (left.blockIndex ?? 0) - (right.blockIndex ?? 0),
    )
    .slice(-MAX_TRANSCRIPT_MESSAGES);
}

function isOperational(entry: TranscriptMessage): boolean {
  const type = entry.type.toLocaleLowerCase();
  return (
    entry.tool !== undefined ||
    entry.toolCall !== undefined ||
    entry.task !== undefined ||
    type.includes('tool') ||
    type.includes('task')
  );
}

/** Hidden reasoning is never a conversational message; its text stays redacted. */
function isHiddenReasoning(entry: TranscriptMessage): boolean {
  const type = entry.type.toLocaleLowerCase();
  return entry.redacted === true || type.includes('think') || type.includes('reason');
}

/**
 * A friendly, sanitized label for an operational row.
 * Returns `null` to drop the row, `''` for a label-less activity such as
 * hidden reasoning (never surfaces provider chain-of-thought).
 */
export function activityLabel(entry: TranscriptMessage): string | null {
  const type = entry.type.toLocaleLowerCase();
  if (SUPPRESSED_TYPES.includes(type)) return null;

  // Redaction hides row text, not the server-sanitized tool/task metadata —
  // most live tool rows arrive redacted, so label them before blanking.
  const tool = entry.tool ?? entry.task?.lastToolName;
  if (tool !== undefined && tool.trim() !== '') return `Using ${friendlyToolName(tool)}`;
  if (entry.task?.description?.trim()) return entry.task.description.trim();
  if (entry.toolCall?.summary?.trim()) return entry.toolCall.summary.trim();
  if (entry.redacted === true || type.includes('think') || type.includes('reason')) return '';
  if (type === 'read') {
    const target = entry.text?.split(/[\\/]/).at(-1)?.trim();
    return target ? `Reading ${target}` : 'Reading workspace files';
  }
  if (type.includes('tool')) return 'Using a workspace tool';
  if (type.includes('task')) return 'Working on a task';
  return null;
}

export function friendlyToolName(tool: string): string {
  return tool
    .trim()
    .replaceAll('_', ' ')
    .replace(/([a-z])([A-Z])/g, '$1 $2')
    .toLocaleLowerCase();
}

export interface BuildConversationOptions {
  /** `assistant-only` suppresses user turns (feature previews have no operator chat). */
  mode?: ConversationMode;
  initialPrompt?: string;
  optimisticMessage?: string | null;
}

/** Reduce a validated row stream into conversational responses and activity groups. */
export function buildConversation(
  messages: readonly TranscriptMessage[],
  options: BuildConversationOptions = {},
): ConversationItem[] {
  const { mode = 'chat', initialPrompt, optimisticMessage } = options;
  const includeUser = mode === 'chat';
  const items: ConversationItem[] = [];

  const initial = initialPrompt?.trim();
  const visibleUserMessages = messages.filter(
    (entry) => entry.role.toLocaleLowerCase() === 'user' && entry.text?.trim() !== '',
  );
  if (
    includeUser &&
    initial !== undefined &&
    initial !== '' &&
    !visibleUserMessages.some((entry) => entry.text?.trim() === initial)
  ) {
    items.push({ kind: 'message', key: 'initial-prompt', role: 'user', text: initial });
  }

  for (const entry of messages) {
    if (entry.fileChange !== undefined && entry.fileChange.path?.trim()) {
      items.push({
        kind: 'file-change',
        key: `file-change-${messageKey(entry)}`,
        change: entry.fileChange,
      });
      continue;
    }

    const role = entry.role.toLocaleLowerCase();
    const text = entry.text?.trim();
    if (entry.autoPicked === true && role === 'user' && text !== undefined && text !== '') {
      items.push({
        kind: 'auto-pick',
        key: `auto-pick-${messageKey(entry)}`,
        question: entry.autoPickQuestion?.trim() || 'Agent question',
        answer: text,
        ...(entry.autoPickConfidence === undefined ? {} : { confidence: entry.autoPickConfidence }),
      });
      continue;
    }
    const operational = isOperational(entry);
    const isMessage =
      !isHiddenReasoning(entry) &&
      (role === 'user' || (role === 'assistant' && !operational)) &&
      text !== undefined &&
      text !== '';
    if (isMessage) {
      if (role === 'user' && !includeUser) continue;
      items.push({
        kind: 'message',
        key: `message-${messageKey(entry)}`,
        role: role === 'user' ? 'user' : 'assistant',
        text: text as string,
      });
      continue;
    }

    const label = activityLabel(entry);
    if (label === null) continue;
    const previous = items.at(-1);
    if (previous?.kind === 'activity') {
      if (label !== '' && !previous.labels.includes(label)) previous.labels.push(label);
    } else {
      items.push({
        kind: 'activity',
        key: `activity-${messageKey(entry)}`,
        labels: label === '' ? [] : [label],
      });
    }
  }

  const optimistic = optimisticMessage?.trim();
  if (
    includeUser &&
    optimistic !== undefined &&
    optimistic !== '' &&
    !items.some(
      (item) => item.kind === 'message' && item.role === 'user' && item.text === optimistic,
    )
  ) {
    items.push({ kind: 'message', key: 'optimistic-message', role: 'user', text: optimistic });
  }
  return items;
}
