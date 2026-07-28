import type { SessionTaskActivity, TranscriptMessage } from '../../../../shared/ipc';

/** Upper bound on retained rows; keeps the live preview and AMA memory-safe. */
export const MAX_TRANSCRIPT_MESSAGES = 200;

/** Row types that are machinery, never shown as conversation. */
const SUPPRESSED_TYPES = ['usage_update', 'success', 'result', 'system', 'prompt'];

export type ConversationMode = 'chat' | 'assistant-only';

export type SubagentState = 'running' | 'done' | 'failed' | 'cancelled';

/** Live view of one delegated sub-agent, folded from its task lifecycle rows. */
export interface SubagentActivity {
  id: string;
  state: SubagentState;
  description?: string;
  taskType?: string;
  lastTool?: string;
  summary?: string;
}

export type ConversationItem =
  | { kind: 'message'; key: string; role: 'user' | 'assistant'; text: string }
  | {
      kind: 'auto-pick';
      key: string;
      text: string;
    }
  | { kind: 'status'; key: string; text: string }
  | {
      kind: 'file-change';
      key: string;
      change: NonNullable<TranscriptMessage['fileChange']>;
    }
  | { kind: 'activity'; key: string; labels: string[] }
  | { kind: 'subagents'; key: string; agents: SubagentActivity[] };

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
  /** Durable provider-neutral task snapshots; authoritative over bounded transcript rows. */
  taskActivities?: readonly SessionTaskActivity[];
}

/** Reduce a validated row stream into conversational responses and activity groups. */
export function buildConversation(
  messages: readonly TranscriptMessage[],
  options: BuildConversationOptions = {},
): ConversationItem[] {
  const { mode = 'chat', initialPrompt, optimisticMessage, taskActivities = [] } = options;
  const includeUser = mode === 'chat';
  const items: ConversationItem[] = [];
  const subagentsById = new Map<string, SubagentActivity>();

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
    const type = entry.type.toLocaleLowerCase();
    const text = entry.text?.trim();
    if (entry.autoPicked === true && role === 'user' && text !== undefined && text !== '') {
      items.push({
        kind: 'auto-pick',
        key: `auto-pick-${messageKey(entry)}`,
        text: `Option ${autoPickOptionNumber(entry.autoPickQuestion, text)}: ${text}`,
      });
      continue;
    }
    if (type === 'status' && text !== undefined && text !== '') {
      items.push({ kind: 'status', key: `status-${messageKey(entry)}`, text });
      continue;
    }
    // Task lifecycle rows fold into a live sub-agent group: `task_started`
    // spawns an entry, `task_progress` updates its current tool in place, and
    // `task_notification` settles it as done or failed.
    const taskId = entry.task?.id?.trim();
    if (entry.task !== undefined && taskId !== undefined && taskId !== '') {
      const known = subagentsById.get(taskId);
      const agent: SubagentActivity = known ?? { id: taskId, state: 'running' };
      if (entry.task.description?.trim()) agent.description = entry.task.description.trim();
      if (entry.task.taskType?.trim()) agent.taskType = entry.task.taskType.trim();
      if (entry.task.lastToolName?.trim()) agent.lastTool = entry.task.lastToolName.trim();
      if (type === 'task_notification') {
        const status = entry.task.status?.trim().toLocaleLowerCase() ?? '';
        if (status === 'failed' || status === 'error') agent.state = 'failed';
        else if (status === 'cancelled' || status === 'canceled') agent.state = 'cancelled';
        else agent.state = 'done';
        if (entry.task.summary?.trim()) agent.summary = entry.task.summary.trim();
      }
      if (known === undefined) {
        subagentsById.set(taskId, agent);
        const previous = items.at(-1);
        if (previous?.kind === 'subagents') previous.agents.push(agent);
        else
          items.push({ kind: 'subagents', key: `subagents-${messageKey(entry)}`, agents: [agent] });
      }
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

  for (const task of taskActivities) {
    const known = subagentsById.get(task.taskId);
    const agent: SubagentActivity = known ?? { id: task.taskId, state: 'running' };
    if (task.description?.trim()) agent.description = task.description.trim();
    if (task.lastToolName?.trim()) agent.lastTool = task.lastToolName.trim();
    if (task.summary?.trim()) agent.summary = task.summary.trim();
    if (task.state === 'running') agent.state = 'running';
    else if (task.state === 'failed') agent.state = 'failed';
    else if (task.state === 'cancelled') agent.state = 'cancelled';
    else agent.state = 'done';
    if (known !== undefined) continue;

    subagentsById.set(task.taskId, agent);
    const lastGroup = [...items].reverse().find((item) => item.kind === 'subagents');
    if (lastGroup?.kind === 'subagents') lastGroup.agents.push(agent);
    else
      items.push({ kind: 'subagents', key: `subagents-registry-${task.taskId}`, agents: [agent] });
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

function autoPickOptionNumber(question: string | undefined, answer: string): number {
  if (question !== undefined) {
    for (const line of question.split('\n')) {
      const match = line.match(/^\s*(\d+)\.\s+(.+)$/);
      if (match === null) continue;
      const option = match[2]?.trim();
      if (option?.startsWith(answer) || answer.startsWith(option ?? '')) {
        return Number(match[1]);
      }
    }
  }
  return 1;
}
