import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type Dispatch,
  type ClipboardEvent,
  type FormEvent,
  type SetStateAction,
} from 'react';
import {
  ATTENTION_ALREADY_RESOLVED_NOTICE,
  CHAT_SESSION_ID,
  CREATION_IMAGE_LIMIT,
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
import { buildConversation, reconcileMessages } from '../features/transcript/conversation';
import { ConversationTranscript } from '../features/transcript/ConversationTranscript';
import { CloseIcon, MaximizeIcon } from './icons';
import { useModalDismiss } from './useModalDismiss';

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
  const [maximized, setMaximized] = useState(false);
  const [session, setSession] = useState<SessionDetail | null>(null);
  const [transcript, setTranscript] = useState<TranscriptState>({
    phase: 'idle',
    messages: [],
    cursor: EMPTY_CURSOR,
  });
  const [message, setMessage] = useState('');
  const [images, setImages] = useState<readonly string[]>([]);
  const [optimisticMessage, setOptimisticMessage] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState('');
  const [confirmingEnd, setConfirmingEnd] = useState(false);
  const [attentionBusy, setAttentionBusy] = useState<string | null>(null);
  const [localDrafts, setLocalDrafts] = useState(emptyAttentionDrafts);
  const [pinToBottom, setPinToBottom] = useState(0);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const confirmEndRef = useRef<HTMLButtonElement>(null);
  const modalRef = useRef<HTMLElement>(null);
  const maximizeRef = useRef<HTMLButtonElement>(null);
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
    () =>
      buildConversation(transcript.messages, {
        mode: 'chat',
        initialPrompt: session?.initialPrompt,
        optimisticMessage,
      }),
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

  const persistDrawer = useCallback((next: DrawerMode) => {
    setDrawer(next);
    window.agentico.updateSettings({ ama: { drawer: next } }).catch(() => undefined);
  }, []);

  const closeMaximized = useCallback(() => setMaximized(false), []);
  useModalDismiss(modalRef, closeMaximized, maximized);

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
            messages: reconcileMessages(current.messages, event.message),
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
    const submittedImages = images;
    setImages([]);
    setPinToBottom((value) => value + 1);
    setBusy(true);
    setNotice('');
    persistDrawer('expanded');
    try {
      await window.agentico.startChat({ message: text, images: [...submittedImages] });
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
      setImages(submittedImages);
      setNotice(error instanceof Error ? error.message : 'Could not send AMA message.');
    } finally {
      setBusy(false);
    }
  };

  const onComposerPaste = (event: ClipboardEvent<HTMLTextAreaElement>): void => {
    const imageFiles = Array.from(event.clipboardData.files).filter((file) =>
      file.type.startsWith('image/'),
    );
    const hasImage = Array.from(event.clipboardData.items ?? []).some((item) =>
      item.type.startsWith('image/'),
    );
    if (!hasImage && imageFiles.length === 0) return;
    event.preventDefault();
    const imported = window.agentico.importDroppedCreationFiles('image', imageFiles);
    if (imported.paths.length > 0) {
      setImages((current) => uniquePaths(current, imported.paths));
      return;
    }
    void window.agentico
      .readClipboardImage()
      .then((result) => setImages((current) => uniquePaths(current, result.paths)))
      .catch((error: unknown) =>
        setNotice(error instanceof Error ? error.message : 'Could not paste image.'),
      );
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
    <div
      className="ama-dock__modal-layer"
      data-open={maximized}
      onMouseDown={maximized ? closeMaximized : undefined}
    >
      <aside
        ref={modalRef}
        className="ama-dock"
        data-mode={maximized ? 'expanded' : drawer}
        aria-label={maximized ? 'Expanded AMA' : 'Ask Agentico'}
        role={maximized ? 'dialog' : undefined}
        aria-modal={maximized ? true : undefined}
        tabIndex={maximized ? -1 : undefined}
        onMouseDown={maximized ? (event) => event.stopPropagation() : undefined}
      >
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
          <button
            ref={maximizeRef}
            type="button"
            className="ama-dock__icon-button"
            aria-label={maximized ? 'Close expanded AMA' : 'Expand AMA'}
            title={maximized ? 'Close expanded AMA' : 'Expand AMA'}
            onClick={() => setMaximized((current) => !current)}
          >
            {maximized ? <CloseIcon /> : <MaximizeIcon />}
          </button>
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
        {drawer === 'expanded' || maximized ? (
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
            <ConversationTranscript
              className="ama-dock__transcript"
              ariaLabel="AMA transcript"
              items={conversation}
              waiting={waitingForAssistant}
              idleLabel="Thinking through your question"
              pinToBottomToken={pinToBottom}
              status={
                <>
                  {transcript.phase === 'loading' ? <p role="status">Loading transcript…</p> : null}
                  {transcript.phase === 'error' ? <p role="alert">{transcript.message}</p> : null}
                </>
              }
              emptyState={
                <div className="ama-dock__empty">
                  <strong>Ask anything about this workspace.</strong>
                  <span>
                    I can inspect the project, explain what is happening, and help you decide what
                    to do next.
                  </span>
                </div>
              }
            />
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
              End the active AMA session. The transcript stays read-only until a new AMA replaces
              it.
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
          {images.length > 0 ? (
            <ol className="composer__chips ama-dock__attachments" aria-label="Attached images">
              {images.map((image) => (
                <li key={image} className="composer__chip" data-kind="image">
                  <span>{basename(image)}</span>
                  <button
                    type="button"
                    aria-label={`Remove ${basename(image)}`}
                    onClick={() => setImages((current) => current.filter((item) => item !== image))}
                  >
                    ×
                  </button>
                </li>
              ))}
            </ol>
          ) : null}
          <textarea
            ref={inputRef}
            aria-label="Ask Agentico"
            value={message}
            onChange={(event) => setMessage(event.target.value)}
            onPaste={onComposerPaste}
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
    </div>
  );
}

function uniquePaths(current: readonly string[], additions: readonly string[]): string[] {
  return [...new Set([...current, ...additions])].slice(0, CREATION_IMAGE_LIMIT);
}

function basename(filePath: string): string {
  return filePath.split(/[\\/]/).at(-1) ?? filePath;
}
