import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type Dispatch,
  type ClipboardEvent,
  type FormEvent,
  type PointerEvent as ReactPointerEvent,
  type SetStateAction,
} from 'react';
import {
  ATTENTION_ALREADY_RESOLVED_NOTICE,
  CHAT_SESSION_ID,
  CREATION_IMAGE_LIMIT,
  defaultAmaGeometry,
  isTerminalChatStatus,
  type AmaGeometry,
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
import { useConnectionState } from '../hooks';
import {
  failPendingUploads,
  isBlockingStagedItem,
  isStagedOnOtherServer,
  pendingUploadItems,
  reconcileUploadResults,
  STAGED_ITEMS_BLOCK_SUBMIT,
  STAGED_ON_OTHER_SERVER,
  submittableReferences,
  type ComposerUploadItem,
} from '../features/stagedItems';
import { parseIpcError } from '../wizard/ipcError';
import { buildConversation, reconcileMessages } from '../features/transcript/conversation';
import { ConversationTranscript } from '../features/transcript/ConversationTranscript';
import {
  clampAmaGeometry,
  dragAmaGeometry,
  resizeAmaGeometry,
  RESIZE_EDGES,
  type ResizeEdge,
} from './amaGeometry';
import { CloseIcon, MaximizeIcon, MinimizeIcon } from './icons';
import { useModalDismiss } from './useModalDismiss';

type TranscriptState =
  | { phase: 'idle'; messages: TranscriptMessage[]; cursor: TranscriptCursor }
  | { phase: 'loading'; messages: TranscriptMessage[]; cursor: TranscriptCursor }
  | { phase: 'ready'; messages: TranscriptMessage[]; cursor: TranscriptCursor }
  | { phase: 'error'; message: string; messages: TranscriptMessage[]; cursor: TranscriptCursor };

/** A live drag or resize: the pointer and geometry the gesture started from. */
interface Gesture {
  pointerId: number;
  kind: 'move' | ResizeEdge;
  startX: number;
  startY: number;
  start: AmaGeometry;
}

const EMPTY_CURSOR: TranscriptCursor = { total: 0, start: 0, end: 0 };

function viewport(): { width: number; height: number } {
  return { width: window.innerWidth, height: window.innerHeight };
}

/**
 * The floating Ask Agentico panel: a draggable, resizable panel inside the
 * main window, closed by default and persisted through the AMA settings
 * sub-schema (`drawer` carries closed/open, `geometry` the placement) over the
 * existing settings round trip. ⌥Space toggles it from anywhere in the window;
 * every routed `ama` request opens, expands, and focuses the composer.
 */
export function AmaPanel({
  attentionItems,
  refreshAttention,
  attentionDrafts,
  setAttentionDrafts,
  routeRequest,
  onSessionActiveChange,
}: {
  attentionItems: AttentionItem[];
  refreshAttention(): Promise<AttentionItem[]>;
  attentionDrafts?: AttentionDrafts;
  setAttentionDrafts?: Dispatch<SetStateAction<AttentionDrafts>>;
  routeRequest: RoutedRequest | null;
  /** Lets the shell's sidebar footer show the active-session state. */
  onSessionActiveChange?(active: boolean): void;
}) {
  const [open, setOpen] = useState(false);
  const [geometry, setGeometry] = useState<AmaGeometry>(defaultAmaGeometry());
  const [gesture, setGesture] = useState<Gesture | null>(null);
  const [maximized, setMaximized] = useState(false);
  const [session, setSession] = useState<SessionDetail | null>(null);
  const [transcript, setTranscript] = useState<TranscriptState>({
    phase: 'idle',
    messages: [],
    cursor: EMPTY_CURSOR,
  });
  const [message, setMessage] = useState('');
  const [images, setImages] = useState<readonly string[]>([]);
  const [imageUploads, setImageUploads] = useState<readonly ComposerUploadItem[]>([]);
  const [optimisticMessage, setOptimisticMessage] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState('');
  const [confirmingEnd, setConfirmingEnd] = useState(false);
  const [attentionBusy, setAttentionBusy] = useState<string | null>(null);
  const [localDrafts, setLocalDrafts] = useState(emptyAttentionDrafts);
  const [pinToBottom, setPinToBottom] = useState(0);
  const [focusToken, setFocusToken] = useState(0);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const confirmEndRef = useRef<HTMLButtonElement>(null);
  const modalRef = useRef<HTMLElement>(null);
  const maximizeRef = useRef<HTMLButtonElement>(null);
  const subscriptionId = useRef<string | null>(null);
  const subscriptionGeneration = useRef(0);
  // The panel always persists both AMA fields together: the settings patch
  // replaces the whole `ama` object, so sending one field would reset the other.
  const openRef = useRef(open);
  const geometryRef = useRef(geometry);
  openRef.current = open;
  geometryRef.current = geometry;
  const activeDrafts = attentionDrafts ?? localDrafts;
  const updateDrafts = setAttentionDrafts ?? setLocalDrafts;
  // Remote connections stage pasted/dropped images through the upload
  // channel; local connections submit paths as today.
  const connection = useConnectionState();
  const remote = connection.status === 'ready' && connection.kind === 'remote';
  const serverKey = connection.status === 'ready' ? (connection.serverKey ?? null) : null;
  // In-progress, failed, or foreign-server uploads stay in the composer but
  // block sending until removed (or switched back to).
  const uploadsBlocking = imageUploads.some((item) => isBlockingStagedItem(item, serverKey));

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
        taskActivities: session?.taskActivities ?? [],
      }),
    [optimisticMessage, session?.initialPrompt, session?.taskActivities, transcript.messages],
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
    onSessionActiveChange?.(sessionActive);
  }, [onSessionActiveChange, sessionActive]);

  const persistPrefs = useCallback((next: { open?: boolean; geometry?: AmaGeometry }): void => {
    const nextOpen = next.open ?? openRef.current;
    const nextGeometry = next.geometry ?? geometryRef.current;
    window.agentico
      .updateSettings({
        ama: { drawer: nextOpen ? 'expanded' : 'compact', geometry: nextGeometry },
      })
      .catch(() => undefined);
  }, []);

  const setOpenPersisted = useCallback(
    (next: boolean): void => {
      setOpen(next);
      if (!next) setMaximized(false);
      persistPrefs({ open: next });
    },
    [persistPrefs],
  );

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
      // Before the first question there is no chat session to read, which is
      // the empty state rather than a failure.
      if (parseIpcError(error).code === 'not_found') {
        setTranscript({ phase: 'ready', messages: [], cursor: EMPTY_CURSOR });
        return null;
      }
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
        if (!alive) return;
        setOpen(settings.ama.drawer === 'expanded');
        setGeometry(clampAmaGeometry(settings.ama.geometry, viewport()));
      })
      .catch(() => undefined);
    void refreshSession();
    return () => {
      alive = false;
    };
  }, [refreshSession]);

  // The panel is always fully inside the window, including when the window is
  // resized around it — down to the window minimum, where it shrinks to fit.
  useEffect(() => {
    const onResize = (): void => setGeometry((current) => clampAmaGeometry(current, viewport()));
    window.addEventListener('resize', onResize);
    return () => window.removeEventListener('resize', onResize);
  }, []);

  // ⌥Space toggles the panel from anywhere in the window, including from a
  // focused text field: the default is prevented so macOS never inserts the
  // non-breaking space the chord would otherwise type.
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent): void => {
      if (!event.altKey || event.metaKey || event.ctrlKey) return;
      // macOS reports ⌥Space as a non-breaking space, not ' '.
      if (event.code !== 'Space' && event.key !== ' ' && event.key !== '\u00A0') return;
      event.preventDefault();
      setOpenPersisted(!openRef.current);
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [setOpenPersisted]);

  // Every routed `ama` request opens, expands, and focuses the composer; a
  // route never closes an open panel. The composer only exists once the panel
  // is open, so the focus is a separate effect gated on both.
  useEffect(() => {
    if (routeRequest?.event.target !== 'ama') return;
    setOpenPersisted(true);
    setFocusToken((token) => token + 1);
  }, [routeRequest, setOpenPersisted]);

  useEffect(() => {
    if (focusToken === 0 || !open) return;
    inputRef.current?.focus();
  }, [focusToken, open]);

  useEffect(() => {
    if (!open) return;
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
  }, [closeOutputSubscription, loadTranscript, open, refreshSession, replaceOutputSubscription]);

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

  // Drag and resize run off window-level pointer listeners so a gesture keeps
  // tracking when the pointer leaves the panel, and persist once on release.
  useEffect(() => {
    if (gesture === null) return;
    const onMove = (event: PointerEvent): void => {
      const delta = { x: event.clientX - gesture.startX, y: event.clientY - gesture.startY };
      setGeometry(
        gesture.kind === 'move'
          ? dragAmaGeometry(gesture.start, delta, viewport())
          : resizeAmaGeometry(gesture.start, gesture.kind, delta, viewport()),
      );
    };
    const onEnd = (): void => {
      setGesture(null);
      persistPrefs({ geometry: geometryRef.current });
    };
    window.addEventListener('pointermove', onMove);
    window.addEventListener('pointerup', onEnd);
    window.addEventListener('pointercancel', onEnd);
    return () => {
      window.removeEventListener('pointermove', onMove);
      window.removeEventListener('pointerup', onEnd);
      window.removeEventListener('pointercancel', onEnd);
    };
  }, [gesture, persistPrefs]);

  const beginGesture = (event: ReactPointerEvent, kind: Gesture['kind']): void => {
    if (maximized || event.button !== 0) return;
    event.preventDefault();
    setGesture({
      pointerId: event.pointerId,
      kind,
      startX: event.clientX,
      startY: event.clientY,
      start: geometryRef.current,
    });
  };

  const submit = async (event: FormEvent): Promise<void> => {
    event.preventDefault();
    const text = message.trim();
    if (text === '' || busy || uploadsBlocking) return;
    setOptimisticMessage(text);
    setMessage('');
    const submittedImages = images;
    setImages([]);
    const submittedUploads = imageUploads;
    setImageUploads([]);
    setPinToBottom((value) => value + 1);
    setBusy(true);
    setNotice('');
    setOpenPersisted(true);
    try {
      const uploadRefs = submittableReferences(submittedUploads, 'image', serverKey);
      await window.agentico.startChat({
        message: text,
        images: [...submittedImages],
        ...(uploadRefs.length === 0 ? {} : { imageUploads: uploadRefs }),
      });
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
      setImageUploads(submittedUploads);
      setNotice(error instanceof Error ? error.message : 'Could not send AMA message.');
    } finally {
      setBusy(false);
    }
  };

  /**
   * Stages local image paths on the connected server (remote flow): pending
   * chips appear immediately, then flip per-file to ready or failed.
   */
  const stageImagesRemotely = (paths: readonly string[]): void => {
    if (paths.length === 0) return;
    const pending = pendingUploadItems('image', paths);
    setImageUploads((items) => {
      const known = new Set(items.map((item) => item.sourcePath));
      const additions = pending.filter((item) => !known.has(item.sourcePath));
      return [...items, ...additions].slice(0, CREATION_IMAGE_LIMIT);
    });
    window.agentico
      .uploadCreationFiles('image', paths)
      .then((result) =>
        setImageUploads((items) => reconcileUploadResults(items, pending, result.results)),
      )
      .catch((error: unknown) => {
        const message = parseIpcError(error).message;
        setImageUploads((items) => failPendingUploads(items, pending, message));
      });
  };

  const retryUpload = (item: ComposerUploadItem): void => {
    setImageUploads((items) => items.filter((candidate) => candidate.id !== item.id));
    stageImagesRemotely([item.sourcePath]);
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
    if (remote) {
      if (imported.paths.length > 0) {
        stageImagesRemotely(imported.paths);
        return;
      }
      void window.agentico
        .readClipboardImage()
        .then((result) => stageImagesRemotely(result.paths))
        .catch((error: unknown) =>
          setNotice(error instanceof Error ? error.message : 'Could not paste image.'),
        );
      return;
    }
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

  if (!open) return null;

  return (
    <div
      className="ama-panel__modal-layer"
      data-open={maximized}
      onMouseDown={maximized ? closeMaximized : undefined}
    >
      <aside
        ref={modalRef}
        className="ama-panel"
        data-maximized={maximized}
        data-dragging={gesture !== null}
        style={
          maximized
            ? undefined
            : {
                right: `${geometry.right}px`,
                bottom: `${geometry.bottom}px`,
                width: `${geometry.width}px`,
                height: `${geometry.height}px`,
              }
        }
        aria-label={maximized ? 'Expanded AMA' : 'Ask Agentico'}
        role={maximized ? 'dialog' : undefined}
        aria-modal={maximized ? true : undefined}
        tabIndex={maximized ? -1 : undefined}
        onMouseDown={maximized ? (event) => event.stopPropagation() : undefined}
      >
        <header
          className="ama-panel__header"
          onPointerDown={(event) => {
            if ((event.target as HTMLElement).closest('button') !== null) return;
            beginGesture(event, 'move');
          }}
        >
          <p className="ama-panel__title">Ask Agentico</p>
          <p className="ama-panel__status">
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
            className="ama-panel__icon-button"
            aria-label={maximized ? 'Close expanded AMA' : 'Expand AMA'}
            title={maximized ? 'Close expanded AMA' : 'Expand AMA'}
            onClick={() => setMaximized((current) => !current)}
          >
            {maximized ? <MinimizeIcon /> : <MaximizeIcon />}
          </button>
          <button
            type="button"
            className="ama-panel__icon-button"
            aria-label="Close Ask Agentico"
            title="Close Ask Agentico"
            onClick={() => setOpenPersisted(false)}
          >
            <CloseIcon />
          </button>
        </header>
        <div className="ama-panel__body" data-has-attention={amaAttentionItems.length > 0}>
          {amaAttentionItems.length > 0 ? (
            <section className="ama-panel__attention" aria-label="AMA questions">
              {amaAttentionItems.map((item) => (
                <div key={`${item.kind}:${item.id}`} className="ama-panel__attention-item">
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
            className="ama-panel__transcript"
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
              <div className="ama-panel__empty">
                <strong>Ask anything about this workspace.</strong>
                <span>
                  I can inspect the project, explain what is happening, and help you decide what to
                  do next.
                </span>
              </div>
            }
          />
        </div>
        {notice !== '' ? (
          <p className="ama-panel__notice" role="status" aria-live="polite">
            {notice}
          </p>
        ) : null}
        {confirmingEnd ? (
          <div className="ama-panel__confirm" role="group" aria-label="End session confirmation">
            <p className="ama-panel__confirm-text">
              End the active AMA session. The transcript stays read-only until a new AMA replaces
              it.
            </p>
            <div className="ama-panel__confirm-actions">
              <button
                type="button"
                className="ama-panel__secondary"
                disabled={busy}
                onClick={() => setConfirmingEnd(false)}
              >
                Cancel
              </button>
              <button
                ref={confirmEndRef}
                type="button"
                className="ama-panel__danger"
                disabled={busy}
                onClick={() => void endChat()}
              >
                End session
              </button>
            </div>
          </div>
        ) : null}
        <form className="ama-panel__composer" onSubmit={(event) => void submit(event)}>
          {images.length > 0 || imageUploads.length > 0 ? (
            <ol className="ama-panel__attachments" aria-label="Attached images">
              {images.map((image) => (
                <li key={image} className="ama-panel__attachment" data-kind="image">
                  <span aria-hidden="true" className="ama-panel__attachment-glyph" />
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
              {imageUploads.map((item) => (
                <li
                  key={item.id}
                  className="ama-panel__attachment"
                  data-kind="image"
                  data-state={item.state}
                >
                  <span aria-hidden="true" className="ama-panel__attachment-glyph" />
                  <span>{item.name}</span>
                  {item.state === 'uploading' ? (
                    <span className="ama-panel__attachment-state">Uploading…</span>
                  ) : null}
                  {item.state === 'failed' ? (
                    <>
                      <span className="ama-panel__attachment-message" title={item.message}>
                        {item.message ?? 'Upload failed.'}
                      </span>
                      <button
                        type="button"
                        aria-label={`Retry ${item.name}`}
                        onClick={() => retryUpload(item)}
                      >
                        ↻
                      </button>
                    </>
                  ) : null}
                  {isStagedOnOtherServer(item, serverKey) ? (
                    <span className="ama-panel__attachment-badge">{STAGED_ON_OTHER_SERVER}</span>
                  ) : null}
                  <button
                    type="button"
                    aria-label={`Remove ${item.name}`}
                    onClick={() =>
                      setImageUploads((current) =>
                        current.filter((candidate) => candidate.id !== item.id),
                      )
                    }
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
          <div className="ama-panel__composer-actions">
            {sessionActive ? (
              <button
                type="button"
                className="ama-panel__danger"
                disabled={busy}
                aria-expanded={confirmingEnd}
                onClick={askToEndChat}
              >
                End session
              </button>
            ) : null}
            <button
              type="submit"
              className="ama-panel__send"
              disabled={busy || uploadsBlocking || message.trim() === ''}
              title={uploadsBlocking ? STAGED_ITEMS_BLOCK_SUBMIT : undefined}
            >
              Send
              <span aria-hidden="true"> ↵</span>
            </button>
          </div>
        </form>
        {maximized
          ? null
          : RESIZE_EDGES.map((edge) => (
              <span
                key={edge}
                className="ama-panel__grip"
                data-edge={edge}
                aria-hidden="true"
                onPointerDown={(event) => beginGesture(event, edge)}
              />
            ))}
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
