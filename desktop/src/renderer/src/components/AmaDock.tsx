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

type DrawerMode = 'compact' | 'expanded';
type TranscriptState =
  | { phase: 'idle'; messages: TranscriptMessage[]; cursor: TranscriptCursor }
  | { phase: 'loading'; messages: TranscriptMessage[]; cursor: TranscriptCursor }
  | { phase: 'ready'; messages: TranscriptMessage[]; cursor: TranscriptCursor }
  | { phase: 'error'; message: string; messages: TranscriptMessage[]; cursor: TranscriptCursor };

const EMPTY_CURSOR: TranscriptCursor = { total: 0, start: 0, end: 0 };

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
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState('');
  const [confirmingEnd, setConfirmingEnd] = useState(false);
  const [attentionBusy, setAttentionBusy] = useState<string | null>(null);
  const [localDrafts, setLocalDrafts] = useState(emptyAttentionDrafts);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const confirmEndRef = useRef<HTMLButtonElement>(null);
  const subscriptionId = useRef<string | null>(null);
  const activeDrafts = attentionDrafts ?? localDrafts;
  const updateDrafts = setAttentionDrafts ?? setLocalDrafts;

  const amaAttentionItems = useMemo(
    () =>
      attentionItems.filter((item) => 'sessionId' in item && item.sessionId === CHAT_SESSION_ID),
    [attentionItems],
  );
  const sessionActive = session !== null && !isTerminalChatStatus(session.status);

  const persistDrawer = useCallback((next: DrawerMode) => {
    setDrawer(next);
    window.agentico.updateSettings({ ama: { drawer: next } }).catch(() => undefined);
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
        const opened = await window.agentico.openSessionOutput({
          sessionId: CHAT_SESSION_ID,
          from,
        });
        if (cancelled) {
          void window.agentico.cancelSessionOutput(opened.subscriptionId);
          return;
        }
        subscriptionId.current = opened.subscriptionId;
      } catch {
        subscriptionId.current = null;
      }
    });
    return () => {
      cancelled = true;
      const id = subscriptionId.current;
      subscriptionId.current = null;
      if (id !== null) {
        void window.agentico.cancelSessionOutput(id);
      }
    };
  }, [drawer, loadTranscript, refreshSession]);

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
        } else {
          setTranscript((current) => ({
            phase: 'error',
            message: event.error.message,
            messages: current.messages,
            cursor: current.cursor,
          }));
        }
      }),
    [refreshSession],
  );

  const submit = async (event: FormEvent): Promise<void> => {
    event.preventDefault();
    const text = message.trim();
    if (text === '' || busy) return;
    setBusy(true);
    setNotice('');
    try {
      await window.agentico.startChat({ message: text });
      setMessage('');
      persistDrawer('expanded');
      await refreshSession();
      await loadTranscript();
    } catch (error) {
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

  const saveDraft = async (
    action: AttentionAction,
    options: AttentionSubmitOptions = { successNotice: 'Draft saved.' },
  ): Promise<void> => {
    try {
      const result = await action();
      setNotice(attentionActionNotice(result, options));
      if (result.alreadyResolved === true) {
        await refreshAttention();
      }
    } catch (error) {
      setNotice(attentionErrorMessage(error));
      throw error;
    }
  };

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
      <form className="ama-dock__composer" onSubmit={(event) => void submit(event)}>
        <textarea
          ref={inputRef}
          aria-label="Ask Agentico"
          value={message}
          onChange={(event) => setMessage(event.target.value)}
          placeholder="Ask about this workspace"
          rows={1}
        />
        <button type="submit" disabled={busy || message.trim() === ''}>
          Send
        </button>
      </form>
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
      {drawer === 'expanded' ? (
        <div className="ama-dock__drawer">
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
                    saveDraft={(action, options) => saveDraft(action, options)}
                  />
                </div>
              ))}
            </section>
          ) : null}
          <section className="ama-dock__transcript" aria-label="AMA transcript">
            {transcript.phase === 'loading' ? <p role="status">Loading transcript…</p> : null}
            {transcript.phase === 'error' ? <p role="alert">{transcript.message}</p> : null}
            {transcript.messages.length === 0 ? (
              <p className="ama-dock__empty">No AMA transcript yet.</p>
            ) : (
              transcript.messages.map((entry) => (
                <article key={entry.index} className="ama-dock__message" data-role={entry.role}>
                  <span className="ama-dock__message-role">{entry.role}</span>
                  <p>{messageText(entry)}</p>
                </article>
              ))
            )}
          </section>
        </div>
      ) : null}
    </aside>
  );
}

function upsertMessage(
  messages: readonly TranscriptMessage[],
  incoming: TranscriptMessage,
): TranscriptMessage[] {
  const byIndex = new Map(messages.map((message) => [message.index, message]));
  byIndex.set(incoming.index, incoming);
  return [...byIndex.values()].sort((left, right) => left.index - right.index);
}

function messageText(entry: TranscriptMessage): string {
  return (
    entry.text ??
    entry.task?.summary ??
    entry.toolCall?.summary ??
    entry.tool ??
    entry.status ??
    entry.type
  );
}
