import { useEffect, useState } from "react";
import { sessionTranscript, type TranscriptMessageDTO } from "../api/client";
import type {
  SDKMessage,
  SDKContentBlock,
} from "../api/types";

// PR #63 exposes session transcript windows over REST instead of the legacy
// per-session WebSocket. Keep the hook name so the Tailwind drawer stays
// unchanged, but poll the transcript endpoint while the drawer is open.

export type SessionState = "connecting" | "open" | "closed";

export interface SessionConnection {
  messages: SDKMessage[];
  state: SessionState;
  done: boolean;
  doneStatus?: string;
  error?: string;
}

const HISTORY_CAP = 1000;
const POLL_MS = 2_500;

export function useSessionWS(sessionId: string | null): SessionConnection {
  const [messages, setMessages] = useState<SDKMessage[]>([]);
  const [state, setState] = useState<SessionState>("connecting");
  const [done, setDone] = useState(false);
  const [doneStatus, setDoneStatus] = useState<string | undefined>(undefined);
  const [error, setError] = useState<string | undefined>(undefined);

  useEffect(() => {
    if (!sessionId) {
      setMessages([]);
      setState("closed");
      setDone(false);
      setDoneStatus(undefined);
      setError(undefined);
      return;
    }
    setMessages([]);
    setDone(false);
    setDoneStatus(undefined);
    setError(undefined);
    setState("connecting");

    const controller = new AbortController();
    let timer: number | null = null;
    let cancelled = false;

    const poll = async () => {
      try {
        const rows = await sessionTranscript(sessionId, controller.signal);
        if (cancelled) return;
        setMessages(rows.map(mapTranscriptMessage).slice(-HISTORY_CAP));
        setState("open");
        setDone(false);
        setDoneStatus(undefined);
      } catch (err) {
        if (cancelled || controller.signal.aborted) return;
        setError(err instanceof Error ? err.message : String(err));
        setState("closed");
      }
      timer = window.setTimeout(poll, POLL_MS);
    };

    void poll();

    return () => {
      cancelled = true;
      controller.abort();
      if (timer !== null) {
        window.clearTimeout(timer);
      }
    };
  }, [sessionId]);

  return { messages, state, done, doneStatus, error };
}

function mapTranscriptMessage(row: TranscriptMessageDTO): SDKMessage {
  if (row.role === "assistant" || row.role === "user") {
    return {
      type: row.role,
      message: {
        role: row.role,
        content: textBlocks(row.text),
      },
    };
  }
  if (row.tool) {
    return {
      type: "tool_progress",
      tool_name: row.tool,
      data: row.text,
    };
  }
  return {
    type: row.status ? "result" : "status",
    subtype: row.type,
    message: row.text ?? row.status ?? "",
    is_error: row.status === "error",
  };
}

function textBlocks(text: string | undefined): SDKContentBlock[] {
  return text ? [{ type: "text", text }] : [];
}
