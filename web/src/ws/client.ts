import type { AnyEnvelope } from "./types";

// EventSource-backed client for the PR #63 server. The rest of the Tailwind UI
// still consumes the legacy "WS" hook names, so this adapter normalizes SSE
// server events into the old activity envelope shape.

export type EventHandler = (env: AnyEnvelope) => void;

export interface WSClient {
  /** Subscribe a handler called for every received envelope (including
   *  hello). Returns an unsubscribe fn. */
  subscribe(handler: EventHandler): () => void;
  /** Current connection state — exposed for the status indicator. */
  state(): WSState;
  /** Subscribe to state transitions. */
  onState(handler: (s: WSState) => void): () => void;
  /** Stop reconnect attempts and close the current socket. */
  close(): void;
}

export type WSState = "connecting" | "open" | "reconnecting" | "closed";

interface WSClientOptions {
  /** Override the default SSE URL (same-origin /api/v1/events). */
  url?: string;
  /** Initial backoff after a drop, ms. Doubles up to maxBackoffMs. */
  initialBackoffMs?: number;
  /** Cap on backoff between reconnect attempts, ms. */
  maxBackoffMs?: number;
}

export function createWSClient(opts: WSClientOptions = {}): WSClient {
  const url = opts.url ?? "/api/v1/events";
  const initial = opts.initialBackoffMs ?? 250;
  const maxBackoff = opts.maxBackoffMs ?? 8_000;

  const handlers = new Set<EventHandler>();
  const stateHandlers = new Set<(s: WSState) => void>();
  let source: EventSource | null = null;
  let backoff = initial;
  let closed = false;
  let state: WSState = "connecting";
  let seq = 0;
  let reconnectTimer: number | null = null;

  const setState = (s: WSState) => {
    state = s;
    for (const h of stateHandlers) {
      try {
        h(s);
      } catch (err) {
        console.error("ws onState handler threw", err);
      }
    }
  };

  const dispatch = (env: AnyEnvelope) => {
    for (const h of handlers) {
      try {
        h(env);
      } catch (err) {
        console.error("ws event handler threw", err);
      }
    }
  };

  const connect = () => {
    if (closed) return;
    setState(source ? "reconnecting" : "connecting");
    try {
      source = new EventSource(url);
    } catch (err) {
      console.error("sse construct failed", err);
      scheduleReconnect();
      return;
    }

    source.addEventListener("open", () => {
      backoff = initial;
      setState("open");
    });

    source.addEventListener("error", () => {
      if (closed) return;
      source?.close();
      source = null;
      if (closed) return;
      scheduleReconnect();
    });

    for (const kind of eventKinds) {
      source.addEventListener(kind, (message) => {
        const event = parseServerEvent(message as MessageEvent<string>);
        if (event) {
          seq += 1;
          dispatch(normalizeServerEvent(event, seq));
        }
      });
    }
  };

  const scheduleReconnect = () => {
    if (closed) return;
    setState("reconnecting");
    const delay = backoff;
    backoff = Math.min(maxBackoff, backoff * 2);
    reconnectTimer = window.setTimeout(connect, delay);
  };

  connect();

  return {
    subscribe(handler) {
      handlers.add(handler);
      return () => {
        handlers.delete(handler);
      };
    },
    state: () => state,
    onState(handler) {
      stateHandlers.add(handler);
      return () => {
        stateHandlers.delete(handler);
      };
    },
    close() {
      closed = true;
      setState("closed");
      if (reconnectTimer !== null) {
        window.clearTimeout(reconnectTimer);
        reconnectTimer = null;
      }
      if (source) {
        source.close();
        source = null;
      }
    },
  };
}

const eventKinds = [
  "connected",
  "heartbeat",
  "lifecycle.updated",
  "session.updated",
  "prompt.updated",
  "permission.updated",
  "recovery.updated",
  "shutdown.updated",
  "config.updated",
  "log.updated",
  "backpressure.coalesced",
] as const;

interface ServerEvent {
  id: string;
  kind: string;
  at: string;
  resource: {
    type: string;
    id?: string;
    feature_id?: string;
    phase?: string;
  };
  summary?: string;
}

function parseServerEvent(message: MessageEvent<string>): ServerEvent | null {
  try {
    return JSON.parse(message.data) as ServerEvent;
  } catch {
    return null;
  }
}

function normalizeServerEvent(event: ServerEvent, seq: number): AnyEnvelope {
  const payload = {
    feature_id: event.resource.feature_id ?? event.resource.id,
    phase: event.resource.phase,
    message: event.summary,
  };
  switch (event.kind) {
    case "connected":
      return { v: 1, type: "hello", seq, ts: event.at, payload: { protocol_version: 1, server_time: event.at } };
    case "heartbeat":
      return { v: 1, type: "pong", seq, ts: event.at, payload };
    case "prompt.updated":
      return { v: 1, type: "need_user_input.required", seq, ts: event.at, payload };
    case "recovery.updated":
      return { v: 1, type: "recovery.scanned", seq, ts: event.at, payload };
    case "backpressure.coalesced":
      return { v: 1, type: "server.dropped", seq, ts: event.at, payload: { dropped: 1 } };
    case "config.updated":
      return { v: 1, type: "feature.config_changed", seq, ts: event.at, payload };
    case "session.updated":
    case "log.updated":
      return { v: 1, type: "phase.completed", seq, ts: event.at, payload };
    default:
      return { v: 1, type: "feature.advanced", seq, ts: event.at, payload };
  }
}
