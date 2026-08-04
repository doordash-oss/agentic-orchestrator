import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "../api/client";
import { useSessionWS } from "../ws/session";
import type {
  ConversationMsg,
  SDKContentBlock,
  SDKMessage,
} from "../api/types";

// PermissionDecider is the AttachDrawer-supplied callback that
// ControlRequestRow invokes when the user clicks allow or deny. The
// drawer is responsible for routing the call to the API client and
// updating its local "resolved" set so the banner clears.
type PermissionDecider = (requestId: string, allow: boolean) => void;

// AskUserResponder is the M5 callback for AskUserQuestion replies.
// answers is keyed by question text (matching what the agent sent
// on the control_request); the drawer forwards verbatim to
// /api/sessions/:id/ask-user. questions is the raw payload passed
// through so the protocol layer can validate it matches the prompt.
type AskUserResponder = (
  requestId: string,
  questions: unknown,
  answers: Record<string, string>,
) => void;

// AttachDrawer is a full-height side panel opened from the
// FeatureDetail sessions list. It owns a per-session WebSocket
// (useSessionWS), renders the conversation thread, lets the user
// filter tool noise, and sends new messages via
// POST /api/sessions/:id/message.

type Filter = "all" | "no-tools" | "text-only";

export function AttachDrawer({
  sessionId,
  featureId,
  open,
  onClose,
}: {
  sessionId: string | null;
  featureId?: string;
  open: boolean;
  onClose: () => void;
}) {
  const active = open ? sessionId : null;
  const conn = useSessionWS(active);
  const qc = useQueryClient();
  const [filter, setFilter] = useState<Filter>("all");
  const [draft, setDraft] = useState("");
  const threadRef = useRef<HTMLDivElement | null>(null);
  // Locally-resolved control requests. The server is the source of
  // truth (PendingControlRequests on the session), but the dashboard
  // only learns about resolutions via the message stream — which the
  // CLI does not echo. So we track the IDs we resolved in this tab
  // to hide the banner immediately. Another tab attaching after a
  // resolution will simply not see the request in PendingControl
  // because the session removed it on RespondToControl.
  const [resolved, setResolved] = useState<Set<string>>(() => new Set());
  // Reset the resolved set when the session id changes — old IDs
  // belong to a different conversation.
  useEffect(() => {
    setResolved(new Set());
  }, [sessionId]);

  // Auto-scroll to bottom on new messages.
  useEffect(() => {
    const el = threadRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [conn.messages.length]);

  const visible = useMemo(() => filterMessages(conn.messages, filter), [conn.messages, filter]);

  const send = useMutation({
    mutationFn: (text: string) => api.sessionMessage(sessionId!, text),
  });
  const stop = useMutation({
    mutationFn: () => api.sessionStop(sessionId!),
  });
  const decide = useMutation({
    mutationFn: ({ requestId, allow }: { requestId: string; allow: boolean }) =>
      api.respondToControl(sessionId!, requestId, allow),
    onSuccess: (_, vars) => {
      setResolved((cur) => {
        const next = new Set(cur);
        next.add(vars.requestId);
        return next;
      });
      invalidateFeatureQueries(qc, featureId);
    },
  });

  const askUser = useMutation({
    mutationFn: ({
      requestId,
      questions,
      answers,
    }: {
      requestId: string;
      questions: unknown;
      answers: Record<string, string>;
    }) =>
      api.respondToAskUser(sessionId!, {
        request_id: requestId,
        questions,
        answers,
      }),
    onSuccess: (_, vars) => {
      setResolved((cur) => {
        const next = new Set(cur);
        next.add(vars.requestId);
        return next;
      });
      invalidateFeatureQueries(qc, featureId);
    },
  });

  const onDecide: PermissionDecider = (requestId, allow) => {
    decide.mutate({ requestId, allow });
  };
  const onAskUser: AskUserResponder = (requestId, questions, answers) => {
    askUser.mutate({ requestId, questions, answers });
  };

  if (!open || !sessionId) return null;

  const submit = () => {
    const text = draft.trim();
    if (text === "") return;
    send.mutate(text);
    setDraft("");
  };

  return (
    <div
      className="fixed inset-0 z-40 flex justify-end"
      role="dialog"
      aria-modal="true"
    >
      <button
        aria-label="Close attach drawer"
        onClick={onClose}
        className="flex-1 bg-black/40 backdrop-blur-sm"
      />
      <aside className="w-[min(720px,92vw)] h-full flex flex-col bg-bg-secondary border-l border-border shadow-lg">
        <Header
          sessionId={sessionId}
          state={conn.state}
          done={conn.done}
          doneStatus={conn.doneStatus}
          filter={filter}
          onFilter={setFilter}
          onClose={onClose}
          onStop={() => stop.mutate()}
          stopPending={stop.isPending}
        />

        <div
          ref={threadRef}
          className="flex-1 overflow-auto p-3 space-y-2 text-sm"
        >
          {visible.length === 0 && (
            <EmptyConnState
              state={conn.state}
              error={conn.error}
              done={conn.done}
              doneStatus={conn.doneStatus}
              onClose={onClose}
            />
          )}
          {visible.map((m, i) => (
            <MessageRow
              key={`${i}-${m.type}`}
              msg={m}
              resolved={resolved}
              onDecide={onDecide}
              decidePending={decide.isPending}
              onAskUser={onAskUser}
              askUserPending={askUser.isPending}
            />
          ))}
        </div>

        <footer className="border-t border-border p-3 space-y-2">
          {(send.error || stop.error || decide.error || askUser.error) && (
            <p className="text-xs" style={{ color: "var(--status-error)" }}>
              {formatMutationError(
                send.error || stop.error || decide.error || askUser.error,
              )}
            </p>
          )}
          <div className="flex gap-2">
            <textarea
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                  e.preventDefault();
                  submit();
                }
              }}
              placeholder="Type a message (⌘/Ctrl + Enter to send)…"
              rows={2}
              data-persist-key="attach.input"
              className="flex-1 px-2 py-1.5 text-sm rounded-sm bg-bg-tertiary border border-border text-text-primary focus:outline-none focus:border-accent font-mono"
              disabled={conn.done || send.isPending}
            />
            <button
              type="button"
              onClick={submit}
              disabled={draft.trim() === "" || conn.done || send.isPending}
              className="px-3 py-1.5 text-sm rounded-sm text-text-inverse disabled:opacity-50"
              style={{ background: "var(--accent)" }}
              title="Send message (⌘/Ctrl + Enter)"
            >
              {send.isPending ? "sending…" : "send"}
            </button>
          </div>
        </footer>
      </aside>
    </div>
  );
}

function invalidateFeatureQueries(
  qc: ReturnType<typeof useQueryClient>,
  featureId?: string,
) {
  qc.invalidateQueries({ queryKey: ["features"] });
  if (featureId) {
    qc.invalidateQueries({ queryKey: ["feature", featureId] });
  } else {
    qc.invalidateQueries({ queryKey: ["feature"] });
  }
}

function Header({
  sessionId,
  state,
  done,
  doneStatus,
  filter,
  onFilter,
  onClose,
  onStop,
  stopPending,
}: {
  sessionId: string;
  state: string;
  done: boolean;
  doneStatus?: string;
  filter: Filter;
  onFilter: (f: Filter) => void;
  onClose: () => void;
  onStop: () => void;
  stopPending: boolean;
}) {
  return (
    <header className="px-4 py-3 border-b border-border flex items-center justify-between gap-3">
      <div className="min-w-0">
        <h2 className="text-sm font-semibold text-text-primary truncate">
          Session {sessionId}
        </h2>
        <div className="text-[0.7rem] text-text-tertiary flex items-center gap-2">
          <span
            aria-hidden
            className="inline-block w-1.5 h-1.5 rounded-full"
            style={{ background: stateColor(state, done) }}
          />
          <span>{done ? `done${doneStatus ? ` · ${doneStatus}` : ""}` : state}</span>
        </div>
      </div>
      <div className="flex items-center gap-2 text-xs">
        {(["all", "no-tools", "text-only"] as Filter[]).map((f) => (
          <button
            key={f}
            type="button"
            onClick={() => onFilter(f)}
            className="px-2 py-1 rounded-sm border"
            style={
              filter === f
                ? {
                    background: "var(--accent)",
                    borderColor: "var(--accent)",
                    color: "var(--text-inverse)",
                  }
                : {
                    borderColor: "var(--border-color)",
                    color: "var(--text-secondary)",
                  }
            }
          >
            {f}
          </button>
        ))}
        <button
          type="button"
          onClick={onStop}
          disabled={done || stopPending}
          className="px-2 py-1 rounded-sm border border-border text-text-secondary hover:bg-bg-tertiary disabled:opacity-50"
          title="Stop the session"
        >
          stop
        </button>
        <button
          type="button"
          onClick={onClose}
          className="text-text-tertiary hover:text-text-primary px-2"
          aria-label="Close attach drawer"
        >
          ✕
        </button>
      </div>
    </header>
  );
}

// EmptyConnState explains why the conversation panel is blank:
// connecting, ended, errored, or no-events-yet. Replaces the
// terse "connecting…" placeholder that used to stay forever when
// the WS upgrade silently failed.
function EmptyConnState({
  state,
  error,
  done,
  doneStatus,
  onClose,
}: {
  state: string;
  error?: string;
  done: boolean;
  doneStatus?: string;
  onClose: () => void;
}) {
  if (error) {
    return (
      <div className="space-y-2">
        <p className="text-sm" style={{ color: "var(--status-error)" }}>
          {error}
        </p>
        <p className="text-xs text-text-tertiary">
          The session is either gone (cleaned up after a rewind or
          shutdown) or the backend is unreachable. Close this drawer
          and try a live session, or wait for the orchestrator to
          reconnect.
        </p>
        <button
          type="button"
          onClick={onClose}
          className="text-xs underline text-text-tertiary"
        >
          close drawer
        </button>
      </div>
    );
  }
  if (done) {
    return (
      <p className="text-text-tertiary italic">
        Session ended{doneStatus ? ` (${doneStatus})` : ""}. No messages
        were captured in this attach.
      </p>
    );
  }
  if (state === "open") {
    return <p className="text-text-tertiary italic">no messages yet</p>;
  }
  if (state === "connecting") {
    return <p className="text-text-tertiary italic">connecting…</p>;
  }
  return <p className="text-text-tertiary italic">disconnected</p>;
}

function stateColor(state: string, done: boolean): string {
  if (done) return "var(--text-tertiary)";
  switch (state) {
    case "open":
      return "var(--accent)";
    case "connecting":
      return "var(--status-warning)";
    default:
      return "var(--status-error)";
  }
}

function MessageRow({
  msg,
  resolved,
  onDecide,
  decidePending,
  onAskUser,
  askUserPending,
}: {
  msg: SDKMessage;
  resolved: Set<string>;
  onDecide: PermissionDecider;
  decidePending: boolean;
  onAskUser: AskUserResponder;
  askUserPending: boolean;
}) {
  switch (msg.type) {
    case "assistant":
      return <AssistantRow blocks={conversationContent(msg.message)} />;
    case "user":
      return <UserRow blocks={conversationContent(msg.message)} />;
    case "control_request":
      return (
        <ControlRequestRow
          msg={msg}
          resolved={resolved}
          onDecide={onDecide}
          decidePending={decidePending}
          onAskUser={onAskUser}
          askUserPending={askUserPending}
        />
      );
    case "result":
      return <ResultRow msg={msg} />;
    case "status":
      return <FaintRow text={typeof msg.message === "string" ? msg.message : ""} />;
    case "tool_progress":
      return <FaintRow text={`progress · ${msg.tool_name ?? ""}`} />;
    case "raw_output":
      return <RawOutputRow text={typeof msg.message === "string" ? msg.message : ""} />;
    case "rate_limit":
      return (
        <FaintRow
          text={`rate limited · ${typeof msg.message === "string" ? msg.message : "wait briefly"}`}
        />
      );
    case "system":
      if (msg.subtype === "init") {
        return <FaintRow text={`session init · ${msg.model ?? ""}`} />;
      }
      return null;
    default:
      return null;
  }
}

function conversationContent(m: SDKMessage["message"]): SDKContentBlock[] {
  if (m && typeof m === "object" && Array.isArray((m as ConversationMsg).content)) {
    return (m as ConversationMsg).content ?? [];
  }
  return [];
}

function AssistantRow({ blocks }: { blocks: SDKContentBlock[] }) {
  return (
    <article className="rounded-md border border-border bg-bg-tertiary p-2">
      <header className="text-[0.65rem] uppercase tracking-wide text-text-tertiary mb-1">
        assistant
      </header>
      <div className="space-y-1">
        {blocks.map((b, i) => (
          <BlockBody key={i} block={b} />
        ))}
      </div>
    </article>
  );
}

function UserRow({ blocks }: { blocks: SDKContentBlock[] }) {
  return (
    <article
      className="rounded-md p-2"
      style={{ background: "var(--banner-success-bg)" }}
    >
      <header
        className="text-[0.65rem] uppercase tracking-wide mb-1"
        style={{ color: "var(--banner-success-title)" }}
      >
        user
      </header>
      <div className="space-y-1">
        {blocks.map((b, i) => (
          <BlockBody key={i} block={b} />
        ))}
      </div>
    </article>
  );
}

function RawOutputRow({ text }: { text: string }) {
  if (text.trim() === "") return null;
  return (
    <article className="rounded-md border border-border bg-bg-tertiary p-2">
      <header className="text-[0.65rem] uppercase tracking-wide text-text-tertiary mb-1">
        live output
      </header>
      <pre className="text-[0.7rem] font-mono text-text-primary whitespace-pre-wrap break-words max-h-[40vh] overflow-auto">
        {text}
      </pre>
    </article>
  );
}

function BlockBody({ block }: { block: SDKContentBlock }) {
  switch (block.type) {
    case "text":
      return (
        <p className="text-text-primary whitespace-pre-wrap break-words">
          {block.text ?? ""}
        </p>
      );
    case "thinking":
      return (
        <p className="text-text-tertiary italic whitespace-pre-wrap break-words">
          {block.thinking ?? ""}
        </p>
      );
    case "tool_use":
      return (
        <div className="text-xs">
          <span
            className="px-1.5 py-0.5 rounded-sm font-mono"
            style={{
              background: "var(--bg-secondary)",
              color: "var(--accent)",
            }}
          >
            {block.name ?? "tool"}
          </span>
          {block.input !== undefined && (
            <pre className="mt-1 text-[0.7rem] font-mono text-text-tertiary whitespace-pre-wrap break-all">
              {safeJSON(block.input)}
            </pre>
          )}
        </div>
      );
    case "tool_result":
      return (
        <div className="text-xs">
          <span className="text-text-tertiary">result</span>
          {block.content !== undefined && (
            <pre className="mt-1 text-[0.7rem] font-mono text-text-primary whitespace-pre-wrap break-all">
              {safeJSON(block.content)}
            </pre>
          )}
        </div>
      );
    default:
      return null;
  }
}

function ControlRequestRow({
  msg,
  resolved,
  onDecide,
  decidePending,
  onAskUser,
  askUserPending,
}: {
  msg: SDKMessage;
  resolved: Set<string>;
  onDecide: PermissionDecider;
  decidePending: boolean;
  onAskUser: AskUserResponder;
  askUserPending: boolean;
}) {
  const id = msg.request_id ?? "";
  const inner = msg.request;
  const isResolved = id !== "" && resolved.has(id);

  if (isResolved) {
    return (
      <article
        className="rounded-md p-2 text-xs"
        style={{
          background: "var(--delta-positive-bg)",
          color: "var(--delta-positive-text)",
        }}
      >
        resolved · {inner?.tool_name ?? "tool"}
      </article>
    );
  }

  const askingUser = inner?.tool_name === "AskUserQuestion";
  const title = askingUser ? "Agent is asking a question" : "Permission requested";

  return (
    <article className="banner banner--warning">
      <span className="banner-icon" aria-hidden>
        !
      </span>
      <div className="flex-1">
        <div className="banner-title">{title}</div>
        <div className="banner-body">
          {inner?.tool_name ?? "unknown tool"}
          {inner?.subtype ? ` · ${inner.subtype}` : ""}
          {!askingUser && inner?.input !== undefined && (
            <pre className="mt-1 text-[0.7rem] font-mono whitespace-pre-wrap break-all opacity-80">
              {safeJSON(inner.input)}
            </pre>
          )}
        </div>
        {id !== "" && !askingUser && (
          <div className="mt-2 flex gap-2">
            <button
              type="button"
              onClick={() => onDecide(id, true)}
              disabled={decidePending}
              className="px-3 py-1 text-xs rounded-sm text-text-inverse disabled:opacity-50"
              style={{ background: "var(--accent)" }}
            >
              allow
            </button>
            <button
              type="button"
              onClick={() => onDecide(id, false)}
              disabled={decidePending}
              className="px-3 py-1 text-xs rounded-sm border disabled:opacity-50"
              style={{
                borderColor: "var(--banner-warning-border)",
                color: "var(--banner-warning-title)",
              }}
            >
              deny
            </button>
          </div>
        )}
        {askingUser && id !== "" && (
          <AskUserAnswerForm
            requestId={id}
            input={inner?.input}
            onSubmit={onAskUser}
            pending={askUserPending}
          />
        )}
      </div>
    </article>
  );
}

// AskUserAnswerForm extracts the question list from the
// control_request's input JSON (shape: `{questions:[{question, header,
// options:[{label,description}]}]}`). For questions that carry options,
// it renders a radio-button picker plus a final "Other" option that
// reveals a free-text input. Questions without options fall back to a
// plain textarea. The submitted answer string is either the picked
// option's label or the free-text input verbatim — the agent receives
// the same text shape either way.
const OTHER_SENTINEL = "__other__";

function AskUserAnswerForm({
  requestId,
  input,
  onSubmit,
  pending,
}: {
  requestId: string;
  input: unknown;
  onSubmit: AskUserResponder;
  pending: boolean;
}) {
  const questions = extractQuestions(input);
  // answers: final string sent to the agent (option label or free text).
  const [answers, setAnswers] = useState<Record<string, string>>({});
  // selected: which radio is currently picked (option label or OTHER_SENTINEL).
  // Kept separate so the user can pick "Other" and start typing without the
  // empty input being mistaken for "no selection".
  const [selected, setSelected] = useState<Record<string, string>>({});
  // otherText: the typed free-text content per question while "Other" is picked.
  const [otherText, setOtherText] = useState<Record<string, string>>({});

  if (questions.length === 0) {
    return (
      <p className="mt-2 text-[0.7rem] italic opacity-80">
        AskUserQuestion payload could not be parsed; respond via the
        message input below.
      </p>
    );
  }

  const ready = questions.every((q) => (answers[q.text] ?? "").trim() !== "");
  const submit = () => {
    if (!ready || pending) return;
    onSubmit(requestId, input, answers);
  };

  const pickOption = (questionText: string, label: string) => {
    setSelected((cur) => ({ ...cur, [questionText]: label }));
    setAnswers((cur) => ({ ...cur, [questionText]: label }));
  };
  const pickOther = (questionText: string) => {
    setSelected((cur) => ({ ...cur, [questionText]: OTHER_SENTINEL }));
    setAnswers((cur) => ({ ...cur, [questionText]: otherText[questionText] ?? "" }));
  };
  const updateOtherText = (questionText: string, text: string) => {
    setOtherText((cur) => ({ ...cur, [questionText]: text }));
    setAnswers((cur) => ({ ...cur, [questionText]: text }));
  };

  return (
    <form
      className="mt-2 space-y-3"
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      {questions.map((q) => {
        const groupName = `auq-${requestId}-${q.text}`;
        const isOtherSelected = selected[q.text] === OTHER_SENTINEL;
        return (
          <div key={q.text} className="space-y-1">
            <label className="text-[0.7rem] font-semibold text-text-primary block">
              {q.text}
            </label>
            {q.header && (
              <p className="text-[0.65rem] opacity-70">{q.header}</p>
            )}
            {q.options && q.options.length > 0 ? (
              <fieldset className="space-y-1">
                {q.options.map((o) => (
                  <label
                    key={o.label}
                    className="flex items-start gap-2 text-xs text-text-primary cursor-pointer"
                  >
                    <input
                      type="radio"
                      name={groupName}
                      value={o.label}
                      checked={selected[q.text] === o.label}
                      onChange={() => pickOption(q.text, o.label)}
                      className="mt-0.5 accent-[var(--accent)]"
                    />
                    <span className="flex-1">
                      <span>{o.label}</span>
                      {o.description && (
                        <span className="block text-[0.65rem] opacity-70">
                          {o.description}
                        </span>
                      )}
                    </span>
                  </label>
                ))}
                <label className="flex items-start gap-2 text-xs text-text-primary cursor-pointer">
                  <input
                    type="radio"
                    name={groupName}
                    value={OTHER_SENTINEL}
                    checked={isOtherSelected}
                    onChange={() => pickOther(q.text)}
                    className="mt-0.5 accent-[var(--accent)]"
                  />
                  <span className="flex-1">Other…</span>
                </label>
                {isOtherSelected && (
                  <textarea
                    autoFocus
                    value={otherText[q.text] ?? ""}
                    onChange={(e) => updateOtherText(q.text, e.target.value)}
                    rows={2}
                    placeholder="type your answer…"
                    className="w-full px-2 py-1 text-xs rounded-sm bg-bg-tertiary border border-border text-text-primary focus:outline-none focus:border-accent font-mono"
                  />
                )}
              </fieldset>
            ) : (
              <textarea
                value={answers[q.text] ?? ""}
                onChange={(e) =>
                  setAnswers((cur) => ({ ...cur, [q.text]: e.target.value }))
                }
                rows={2}
                placeholder="answer…"
                className="w-full px-2 py-1 text-xs rounded-sm bg-bg-tertiary border border-border text-text-primary focus:outline-none focus:border-accent font-mono"
              />
            )}
          </div>
        );
      })}
      <div className="flex justify-end">
        <button
          type="submit"
          disabled={!ready || pending}
          className="px-3 py-1 text-xs rounded-sm text-text-inverse disabled:opacity-50"
          style={{ background: "var(--accent)" }}
        >
          {pending ? "sending…" : "send answers"}
        </button>
      </div>
    </form>
  );
}

interface AskUserParsedQuestion {
  text: string;
  header?: string;
  options?: { label: string; description?: string }[];
}

function extractQuestions(input: unknown): AskUserParsedQuestion[] {
  if (!input || typeof input !== "object") return [];
  const obj = input as { questions?: unknown };
  if (!Array.isArray(obj.questions)) return [];
  const out: AskUserParsedQuestion[] = [];
  for (const raw of obj.questions) {
    if (!raw || typeof raw !== "object") continue;
    const q = raw as {
      question?: unknown;
      header?: unknown;
      options?: unknown;
    };
    const text = typeof q.question === "string" ? q.question : "";
    if (text === "") continue;
    const item: AskUserParsedQuestion = { text };
    if (typeof q.header === "string") item.header = q.header;
    if (Array.isArray(q.options)) {
      const opts: { label: string; description?: string }[] = [];
      for (const o of q.options) {
        if (!o || typeof o !== "object") continue;
        const oo = o as { label?: unknown; description?: unknown };
        if (typeof oo.label === "string") {
          opts.push({
            label: oo.label,
            description:
              typeof oo.description === "string" ? oo.description : undefined,
          });
        }
      }
      if (opts.length > 0) item.options = opts;
    }
    out.push(item);
  }
  return out;
}

function ResultRow({ msg }: { msg: SDKMessage }) {
  const ok = !msg.is_error;
  return (
    <article
      className="rounded-md p-2 text-xs"
      style={{
        background: ok ? "var(--delta-positive-bg)" : "var(--delta-negative-bg)",
        color: ok ? "var(--delta-positive-text)" : "var(--delta-negative-text)",
      }}
    >
      result · {msg.subtype ?? (ok ? "success" : "error")}
      {typeof msg.duration_ms === "number" && ` · ${msg.duration_ms}ms`}
      {typeof msg.total_cost_usd === "number" &&
        ` · $${msg.total_cost_usd.toFixed(4)}`}
    </article>
  );
}

function FaintRow({ text }: { text: string }) {
  if (!text) return null;
  return (
    <p className="text-[0.7rem] text-text-tertiary italic">{text}</p>
  );
}

function formatMutationError(err: unknown): string {
  if (err instanceof ApiError) {
    return `${err.status}: ${err.message}`;
  }
  if (err instanceof Error) {
    return err.message;
  }
  return String(err);
}

function safeJSON(v: unknown): string {
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}

function filterMessages(msgs: SDKMessage[], filter: Filter): SDKMessage[] {
  if (filter === "all") return msgs;
  if (filter === "no-tools") {
    return msgs.filter(
      (m) =>
        m.type !== "tool_progress" &&
        m.type !== "raw_output" &&
        m.type !== "system",
    );
  }
  // text-only: only assistant/user with text
  return msgs.filter((m) => {
    if (m.type !== "assistant" && m.type !== "user") return false;
    const blocks = conversationContent(m.message);
    return blocks.some((b) => b.type === "text" && (b.text ?? "").trim() !== "");
  });
}
