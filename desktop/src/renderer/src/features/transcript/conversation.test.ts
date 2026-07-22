import { describe, expect, it } from 'vitest';
import type { TranscriptMessage } from '../../../../shared/ipc';
import {
  MAX_TRANSCRIPT_MESSAGES,
  activityLabel,
  buildConversation,
  friendlyToolName,
  messageKey,
  reconcileMessages,
} from './conversation';

function row(
  overrides: Partial<TranscriptMessage> & Pick<TranscriptMessage, 'index'>,
): TranscriptMessage {
  return { role: 'assistant', type: 'text', ...overrides };
}

describe('friendlyToolName', () => {
  it('humanizes snake_case and camelCase tool names', () => {
    expect(friendlyToolName('Read')).toBe('read');
    expect(friendlyToolName('run_command')).toBe('run command');
    expect(friendlyToolName('WebSearch')).toBe('web search');
  });
});

describe('activityLabel', () => {
  it('suppresses machinery rows', () => {
    for (const type of ['usage_update', 'success', 'result', 'system', 'prompt']) {
      expect(activityLabel(row({ index: 0, type }))).toBeNull();
    }
  });

  it('labels tool use in friendly terms', () => {
    expect(activityLabel(row({ index: 0, type: 'tool_use', tool: 'Bash' }))).toBe('Using bash');
  });

  it('returns a label-less activity for hidden reasoning without exposing text', () => {
    expect(
      activityLabel(row({ index: 0, type: 'thinking', text: 'secret chain of thought' })),
    ).toBe('');
    expect(activityLabel(row({ index: 1, type: 'text', redacted: true, text: 'redacted' }))).toBe(
      '',
    );
  });

  it('reads a filename from read rows', () => {
    expect(activityLabel(row({ index: 0, type: 'read', text: 'src/main/README.md' }))).toBe(
      'Reading README.md',
    );
  });
});

describe('buildConversation', () => {
  it('renders assistant text and hides system/usage rows', () => {
    const items = buildConversation([
      row({ index: 0, type: 'usage_update' }),
      row({ index: 1, type: 'tool_use', tool: 'Read', text: 'src/app.ts' }),
      row({ index: 2, type: 'text', text: 'Here is what I found.' }),
    ]);
    expect(items).toEqual([
      { kind: 'activity', key: 'activity-1:0', labels: ['Using read'] },
      { kind: 'message', key: 'message-2:0', role: 'assistant', text: 'Here is what I found.' },
    ]);
  });

  it('merges consecutive activity rows and keeps hidden reasoning label-less', () => {
    const items = buildConversation([
      row({ index: 0, type: 'thinking', text: 'chain of thought' }),
      row({ index: 1, type: 'tool_use', tool: 'bash' }),
    ]);
    expect(items).toEqual([{ kind: 'activity', key: 'activity-0:0', labels: ['Using bash'] }]);
  });

  it('keeps a pure reasoning run as a label-less thinking activity', () => {
    const items = buildConversation([row({ index: 0, type: 'thinking', text: 'hidden' })]);
    expect(items).toEqual([{ kind: 'activity', key: 'activity-0:0', labels: [] }]);
  });

  it('suppresses user turns in assistant-only mode but keeps them in chat mode', () => {
    const rows = [
      row({ index: 0, role: 'user', type: 'text', text: 'do the thing' }),
      row({ index: 1, role: 'assistant', type: 'text', text: 'done' }),
    ];
    expect(buildConversation(rows, { mode: 'chat' })).toHaveLength(2);
    expect(buildConversation(rows, { mode: 'assistant-only' })).toEqual([
      { kind: 'message', key: 'message-1:0', role: 'assistant', text: 'done' },
    ]);
  });

  it('prepends the initial prompt in chat mode only', () => {
    const rows = [row({ index: 0, role: 'assistant', type: 'text', text: 'ok' })];
    expect(buildConversation(rows, { mode: 'chat', initialPrompt: 'hello' })[0]).toEqual({
      kind: 'message',
      key: 'initial-prompt',
      role: 'user',
      text: 'hello',
    });
    expect(
      buildConversation(rows, { mode: 'assistant-only', initialPrompt: 'hello' }),
    ).toHaveLength(1);
  });

  it('keeps multi-block assistant responses as distinct items', () => {
    const items = buildConversation([
      row({ index: 4, blockIndex: 0, type: 'text', text: 'first block' }),
      row({ index: 4, blockIndex: 1, type: 'text', text: 'second block' }),
    ]);
    expect(items.map((item) => item.key)).toEqual(['message-4:0', 'message-4:1']);
  });
});

describe('reconcileMessages', () => {
  it('dedupes by composite block key without collapsing multi-block rows', () => {
    const merged = reconcileMessages(
      [row({ index: 4, blockIndex: 0, text: 'a' })],
      [row({ index: 4, blockIndex: 1, text: 'b' }), row({ index: 4, blockIndex: 0, text: 'a2' })],
    );
    expect(merged.map((message) => `${messageKey(message)}=${message.text}`)).toEqual([
      '4:0=a2',
      '4:1=b',
    ]);
  });

  it('sorts by index then block index and bounds retention', () => {
    const many = Array.from({ length: MAX_TRANSCRIPT_MESSAGES + 25 }, (_, index) =>
      row({ index, text: `row-${index}` }),
    );
    const merged = reconcileMessages([], many);
    expect(merged).toHaveLength(MAX_TRANSCRIPT_MESSAGES);
    expect(merged[0]?.index).toBe(25);
    expect(merged.at(-1)?.index).toBe(MAX_TRANSCRIPT_MESSAGES + 24);
  });
});
