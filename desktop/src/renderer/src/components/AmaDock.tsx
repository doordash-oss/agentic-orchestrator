import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type Dispatch,
  type FormEvent,
  type SetStateAction,
} from 'react';
import {
  ATTENTION_ALREADY_RESOLVED_NOTICE,
  CHAT_SESSION_ID,
  isTerminalChatStatus,
  type AttentionItem,
  type RoutedRequest,
  type SessionDetail,
  type TranscriptMessage,
  type TranscriptCursor,
} from '../../../shared/ipc';
import {
  AttentionDetail,
  attentionActionNotice,
  attentionErrorMessage,
  emptyAttentionDrafts,
  runAttentionSubmit,
  type AttentionAction,
  type AttentionDrafts,
  type AttentionSubmitOptions,
} from '../features/AttentionInbox';
import { useAttentionDraftSaves } from '../features/useAttentionDraftSaves';

type DrawerMode = 'compact' | 'expanded';
type TranscriptState =
  | { phase: 'idle'; messages: TranscriptMessage[]; cursor: TranscriptCursor }
  | { phase: 'loading'; messages: TranscriptMessage[]; cursor: TranscriptCursor }
  | { phase: 'ready'; messages: TranscriptMessage[]; cursor: TranscriptCursor }
  | { phase: 'error'; message: string; messages: TranscriptMessage[]; cursor: TranscriptCursor };
type ConversationItem =
  | { kind: 'message'; key: string; role: 'user' | 'assistant'; text: string }
  | { kind: 'activity'; key: string; labels: string[] };

const EMPTY_CURSOR: TranscriptCursor = { total: 0, start: 0, end: 0 };
const MAX_TRANSCRIPT_MESSAGES = 200;

export function AmaDock({
  attentionItems,
  refreshAttention,
  attentionDrafts,
  setAttentionDrafts,
  routeRequest,
}: {
  attentionItems: AttentionItem[];
  refreshAttention(): Promise<AttentionItem[]>;
  attentionDrafts?: AttentionDrafts;
  setAttentionDrafts?: Dispatch<SetStateAction<AttentionDrafts>>;
  routeRequest: RoutedRequest | null;
}) {
  const [drawer, setDrawer] = useState<DrawerMode>('compact');
  const [session, setSession] = useState<SessionDetail | null>(null);
  const [transcript, setTranscript] = useState<TranscriptState>({
    phase: 'idle',
    messages: [],
    cursor: EMPTY_CURSOR,
  });
  const [message, setMessage] = useState('');
  const [optimisticMessage, setOptimisticMessage] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState('');
  const [confirmingEnd, setConfirmingEnd] = useState(false);
  const [attentionBusy, setAttentionBusy] = useState<string | null>(null);
  const [localDrafts, setLocalDrafts] = useState(emptyAttentionDrafts);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const confirmEndRef = useRef<HTMLButtonElement>(null);
  const transcriptRef = useRef<HTMLElement>(null);
  const stickToBottom = useRef(true);
  const subscriptionId = useRef<string | null>(null);
  const subscriptionGeneration = useRef(0);
  const activeDrafts = attentionDrafts ?? localDrafts;
  const updateDrafts = setAttentionDrafts ?? setLocalDrafts;

  const amaAttentionItems = useMemo(
    () =>
      attentionItems.filter((item) => 'sessionId' in item && item.sessionId === CHAT_SESSION_ID),
    [attentionItems],
  );
  const sessionActive = session !== null && !isTerminalChatStatus(session.status);
  const conversation = useMemo(
    () => buildConversation(transcript.messages, session?.initialPrompt, optimisticMessage),
    [optimisticMessage, session?.initialPrompt, transcript.messages],
  );
  const lastConversationItem = conversation.at(-1);
  const waitingForAssistant =
    busy ||
    (sessionActive &&
      session?.turnState === 'running' &&
      lastConversationItem?.kind !== 'message') ||
    (sessionActive &&
      lastConversationItem?.kind === 'message' &&
      lastConversationItem.role === 'user');

  useEffect(() => {
    const element = transcriptRef.current;
    if (element !== null && stickToBottom.current) element.scrollTop = element.scrollHeight;
  }, [conversation, waitingForAssistant]);

  const persistDrawer = useCallback((next: DrawerMode) => {
    setDrawer(next);
    window.agentico.updateSettings({ ama: { drawer: next } }).catch(() => undefined);
  }, []);

  const closeOutputSubscription = useCallback(() => {
    subscriptionGeneration.current += 1;
    const id = subscriptionId.current;
    subscriptionId.current = null;
    if (id !== null) void window.agentico.cancelSessionOutput(id);
  }, []);

  const replaceOutputSubscription = useCallback(async (from: number): Promise<void> => {
    const generation = subscriptionGeneration.current + 1;
    subscriptionGeneration.current = generation;
    const previous = subscriptionId.current;
    subscriptionId.current = null;
    if (previous !== null) void window.agentico.cancelSessionOutput(previous);

    const opened = await window.agentico.openSessionOutput({
      sessionId: CHAT_SESSION_ID,
      from,
    });
    if (subscriptionGeneration.current !== generation) {
      void window.agentico.cancelSessionOutput(opened.subscriptionId);
      return;
    }
    subscriptionId.current = opened.subscriptionId;
  }, []);

  const refreshSession = useCallback(async () => {
    try {
      const detail = await window.agentico.getSession(CHAT_SESSION_ID);
      setSession(detail);
      return detail;
    } catch {
      setSession(null);
      return null;
    }
  }, []);

  const loadTranscript = useCallback(async () => {
    setTranscript((current) => ({ ...current, phase: 'loading' }));
    try {
      const loaded = await window.agentico.getSessionTranscript({
        sessionId: CHAT_SESSION_ID,
        offset: 0,
        limit: 200,
      });
      setTranscript({ phase: 'ready', messages: loaded.messages, cursor: loaded.cursor });
      return loaded.cursor.end;
    } catch (error) {
      setTranscript((current) => ({
        phase: 'error',
        message: error instanceof Error ? error.message : 'Could not load AMA transcript.',
        messages: current.messages,
        cursor: current.cursor,
      }));
      return null;
    }
  }, []);

  useEffect(() => {
    let alive = true;
    window.agentico
      .getSettings()
      .then((settings) => {
        if (alive) setDrawer(settings.ama.drawer);
      })
      .catch(() => undefined);
    void refreshSession();
    return () => {
      alive = false;
    };
  }, [refreshSession]);

  useEffect(() => {
    if (routeRequest?.event.target !== 'ama') return;
    persistDrawer('expanded');
    requestAnimationFrame(() => inputRef.current?.focus());
  }, [persistDrawer, routeRequest]);

  useEffect(() => {
    if (drawer !== 'expanded') return;
    let cancelled = false;
    void refreshSession();
    void loadTranscript().then(async (from) => {
      if (cancelled || from === null) return;
      try {
        await replaceOutputSubscription(from);
        if (!cancelled) await loadTranscript();
      } catch {
        // A chat may not exist yet. Sending a message retries the subscription.
      }
    });
    return () => {
      cancelled = true;
      closeOutputSubscription();
    };
  }, [closeOutputSubscription, drawer, loadTranscript, refreshSession, replaceOutputSubscription]);

  useEffect(
    () =>
      window.agentico.onSessionOutput((event) => {
        if (
          event.sessionId !== CHAT_SESSION_ID ||
          event.subscriptionId !== subscriptionId.current
        ) {
          return;
        }
        if (event.type === 'record') {
          setTranscript((current) => ({
            phase: current.phase === 'error' ? 'ready' : current.phase,
            messages: upsertMessage(current.messages, event.message),
            cursor: {
              total: Math.max(current.cursor.total, event.index + 1),
              start: current.cursor.start,
              end: Math.max(current.cursor.end, event.index + 1),
            },
          }));
        } else if (event.type === 'done') {
          setTranscript((current) => ({
            ...current,
            cursor: {
              total: Math.max(current.cursor.total, event.nextIndex),
              start: current.cursor.start,
              end: Math.max(current.cursor.end, event.nextIndex),
            },
          }));
          void refreshSession();
          void loadTranscript();
        } else {
          setTranscript((current) => ({
            phase: 'error',
            message: event.error.message,
            messages: current.messages,
            cursor: current.cursor,
          }));
        }
      }),
    [loadTranscript, refreshSession],
  );

  const submit = async (event: FormEvent): Promise<void> => {
    event.preventDefault();
    const text = message.trim();
    if (text === '' || busy) return;
    setOptimisticMessage(text);
    setMessage('');
    stickToBottom.current = true;
    setBusy(true);
    setNotice('');
    persistDrawer('expanded');
    try {
      await window.agentico.startChat({ message: text });
      await refreshSession();
      const from = await loadTranscript();
      if (from !== null) {
        try {
          await replaceOutputSubscription(from);
          await loadTranscript();
        } catch {
          setNotice('Message sent, but live updates could not reconnect. Reopen AMA to retry.');
        }
      }
      setOptimisticMessage(null);
    } catch (error) {
      setOptimisticMessage(null);
      setMessage(text);
      setNotice(error instanceof Error ? error.message : 'Could not send AMA message.');
    } finally {
      setBusy(false);
    }
  };

  const askToEndChat = (): void => {
    if (!sessionActive || busy) return;
    setConfirmingEnd(true);
    requestAnimationFrame(() => confirmEndRef.current?.focus());
  };

  const endChat = async (): Promise<void> => {
    if (!sessionActive || busy) return;
    setBusy(true);
    setNotice('');
    try {
      await window.agentico.endChat();
      await refreshSession();
      await loadTranscript();
      setConfirmingEnd(false);
      setNotice('AMA ended.');
    } catch (error) {
      setNotice(error instanceof Error ? error.message : 'Could not end AMA.');
    } finally {
      setBusy(false);
    }
  };

  const submitAttention = async (
    id: string,
    action: AttentionAction,
    options: AttentionSubmitOptions = {},
  ): Promise<void> => {
    if (attentionBusy !== null) return;
    setAttentionBusy(id);
    setNotice('');
    try {
      const { latest, notice: nextNotice } = await runAttentionSubmit(action, refreshAttention, {
        collapseOnSuccess: false,
        ...options,
      });
      setNotice(
        latest.some((item) => item.id === id) ? nextNotice : ATTENTION_ALREADY_RESOLVED_NOTICE,
      );
    } catch (error) {
      setNotice(attentionErrorMessage(error));
    } finally {
      setAttentionBusy(null);
    }
  };

  const saveDraft = useAttentionDraftSaves({
    notify: (result, options) => setNotice(attentionActionNotice(result, options)),
    notifyError: (error) => setNotice(attentionErrorMessage(error)),
    onAlreadyResolved: async () => {
      await refreshAttention();
    },
  });

  return (
    <aside className="ama-dock" data-mode={drawer} aria-label="Ask Agentico">
      <header className="ama-dock__header">
        <button
          type="button"
          className="ama-dock__toggle"
          aria-expanded={drawer === 'expanded'}
          onClick={() => persistDrawer(drawer === 'expanded' ? 'compact' : 'expanded')}
        >
          AMA
        </button>
        <p className="ama-dock__status">
          {sessionActive
            ? 'Active'
            : transcript.messages.length > 0
              ? 'Read-only transcript'
              : 'Ready'}
          {amaAttentionItems.length > 0 ? ` · ${amaAttentionItems.length} pending` : ''}
        </p>
        {sessionActive ? (
          <button
            type="button"
            className="ama-dock__end"
            disabled={busy}
            aria-expanded={confirmingEnd}
            onClick={askToEndChat}
          >
            End AMA
          </button>
        ) : null}
      </header>
      {drawer === 'expanded' ? (
        <div className="ama-dock__drawer" data-has-attention={amaAttentionItems.length > 0}>
          {amaAttentionItems.length > 0 ? (
            <section className="ama-dock__attention" aria-label="AMA questions">
              {amaAttentionItems.map((item) => (
                <div key={`${item.kind}:${item.id}`} className="ama-dock__attention-item">
                  <AttentionDetail
                    item={item}
                    busy={attentionBusy === item.id}
                    drafts={activeDrafts}
                    setDrafts={updateDrafts}
                    submit={(action, options) => void submitAttention(item.id, action, options)}
                    saveDraft={(action, options) => saveDraft(item.id, action, options)}
                  />
                </div>
              ))}
            </section>
          ) : null}
          <section
            ref={transcriptRef}
            className="ama-dock__transcript"
            aria-label="AMA transcript"
            aria-live="polite"
            onScroll={(event) => {
              const element = event.currentTarget;
              stickToBottom.current =
                element.scrollHeight - element.scrollTop - element.clientHeight < 40;
            }}
          >
            {transcript.phase === 'loading' ? <p role="status">Loading transcript…</p> : null}
            {transcript.phase === 'error' ? <p role="alert">{transcript.message}</p> : null}
            {conversation.length === 0 && !waitingForAssistant ? (
              <div className="ama-dock__empty">
                <strong>Ask anything about this workspace.</strong>
                <span>
                  I can inspect the project, explain what is happening, and help you decide what to
                  do next.
                </span>
              </div>
            ) : null}
            {conversation.map((item, index) =>
              item.kind === 'message' ? (
                <article key={item.key} className="ama-dock__message" data-role={item.role}>
                  <span className="ama-dock__message-role">
                    {item.role === 'user' ? 'You' : 'Agentico'}
                  </span>
                  <p>{item.text}</p>
                </article>
              ) : (
                <ActivityIndicator
                  key={item.key}
                  labels={item.labels}
                  active={waitingForAssistant && index === conversation.length - 1}
                />
              ),
            )}
            {waitingForAssistant && conversation.at(-1)?.kind !== 'activity' ? (
              <ActivityIndicator labels={[]} active />
            ) : null}
          </section>
        </div>
      ) : null}
      {notice !== '' ? (
        <p className="ama-dock__notice" role="status" aria-live="polite">
          {notice}
        </p>
      ) : null}
      {confirmingEnd ? (
        <div
          className="bulk-preview__confirm ama-dock__confirm"
          role="group"
          aria-label="End AMA confirmation"
        >
          <p className="bulk-preview__confirm-text">
            End the active AMA session. The transcript stays read-only until a new AMA replaces it.
          </p>
          <div className="ama-dock__confirm-actions">
            <button
              type="button"
              className="ama-dock__toggle"
              disabled={busy}
              onClick={() => setConfirmingEnd(false)}
            >
              Cancel
            </button>
            <button
              ref={confirmEndRef}
              type="button"
              className="bulk-preview__run"
              disabled={busy}
              onClick={() => void endChat()}
            >
              End AMA
            </button>
          </div>
        </div>
      ) : null}
      <form className="ama-dock__composer" onSubmit={(event) => void submit(event)}>
        <textarea
          ref={inputRef}
          aria-label="Ask Agentico"
          value={message}
          onChange={(event) => setMessage(event.target.value)}
          onKeyDown={(event) => {
            if (event.key !== 'Enter' || event.shiftKey || event.nativeEvent.isComposing) return;
            event.preventDefault();
            event.currentTarget.form?.requestSubmit();
          }}
          placeholder="Ask about this workspace"
          rows={1}
        />
        <button type="submit" disabled={busy || message.trim() === ''}>
          Send
        </button>
      </form>
    </aside>
  );
}

function upsertMessage(
  messages: readonly TranscriptMessage[],
  incoming: TranscriptMessage,
): TranscriptMessage[] {
  const byIndex = new Map(messages.map((message) => [message.index, message]));
  byIndex.set(incoming.index, incoming);
  return [...byIndex.values()]
    .sort((left, right) => left.index - right.index)
    .slice(-MAX_TRANSCRIPT_MESSAGES);
}

function buildConversation(
  messages: readonly TranscriptMessage[],
  initialPrompt?: string,
  optimisticMessage?: string | null,
): ConversationItem[] {
  const items: ConversationItem[] = [];
  const initial = initialPrompt?.trim();
  const visibleUserMessages = messages.filter(
    (entry) => entry.role.toLocaleLowerCase() === 'user' && entry.text?.trim() !== '',
  );

  if (
    initial !== undefined &&
    initial !== '' &&
    !visibleUserMessages.some((entry) => entry.text?.trim() === initial)
  ) {
    items.push({ kind: 'message', key: 'initial-prompt', role: 'user', text: initial });
  }

  for (const entry of messages) {
    const role = entry.role.toLocaleLowerCase();
    const text = entry.text?.trim();
    const operational =
      entry.tool !== undefined ||
      entry.toolCall !== undefined ||
      entry.task !== undefined ||
      entry.type.toLocaleLowerCase().includes('tool') ||
      entry.type.toLocaleLowerCase().includes('task');
    if (
      (role === 'user' || (role === 'assistant' && !operational)) &&
      text !== undefined &&
      text !== ''
    ) {
      items.push({
        kind: 'message',
        key: `message-${entry.index}`,
        role,
        text,
      });
      continue;
    }

    const label = activityLabel(entry);
    if (label === null) continue;
    const previous = items.at(-1);
    if (previous?.kind === 'activity') {
      if (!previous.labels.includes(label)) previous.labels.push(label);
    } else {
      items.push({ kind: 'activity', key: `activity-${entry.index}`, labels: [label] });
    }
  }

  const optimistic = optimisticMessage?.trim();
  if (
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

function activityLabel(entry: TranscriptMessage): string | null {
  const type = entry.type.toLocaleLowerCase();
  if (['usage_update', 'success', 'result', 'system'].includes(type)) return null;

  const tool = entry.tool ?? entry.task?.lastToolName;
  if (tool !== undefined && tool.trim() !== '') return `Using ${friendlyToolName(tool)}`;
  if (entry.task?.description?.trim()) return entry.task.description.trim();
  if (entry.toolCall?.summary?.trim()) return entry.toolCall.summary.trim();
  if (type === 'read') {
    const target = entry.text?.split(/[\\/]/).at(-1)?.trim();
    return target ? `Reading ${target}` : 'Reading workspace files';
  }
  if (type.includes('tool')) return 'Using a workspace tool';
  if (type.includes('task')) return 'Working on a task';
  return null;
}

function friendlyToolName(tool: string): string {
  return tool
    .trim()
    .replaceAll('_', ' ')
    .replace(/([a-z])([A-Z])/g, '$1 $2')
    .toLocaleLowerCase();
}

function ActivityIndicator({ labels, active }: { labels: string[]; active: boolean }) {
  const shownLabels = labels.slice(-3);
  return (
    <div className="ama-dock__activity" data-active={active} role={active ? 'status' : undefined}>
      <span className="ama-dock__thinking" aria-hidden="true">
        {Array.from({ length: 8 }, (_, index) => (
          <span key={index} />
        ))}
      </span>
      <div className="ama-dock__activity-copy">
        <strong>{active ? 'Working' : 'Worked'}</strong>
        {shownLabels.length > 0 ? (
          <span>{shownLabels.join(' · ')}</span>
        ) : (
          <span>Thinking through your question</span>
        )}
      </div>
    </div>
  );
}
