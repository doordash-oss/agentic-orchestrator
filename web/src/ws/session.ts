import { useEffect, useState } from "react";
import {
  sessionPendingControls,
  sessionOutputTail,
  sessionTranscript,
  type ControlRequestDTO,
  type TranscriptMessageDTO,
} from "../api/client";
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
        const [rows, output, controls] = await Promise.all([
          sessionTranscript(sessionId, controller.signal),
          sessionOutputTail(sessionId, controller.signal),
          sessionPendingControls(sessionId, controller.signal),
        ]);
        if (cancelled) return;
        const transcriptMessages = rows
          .map(mapTranscriptMessage)
          .filter(hasVisibleContent);
        const outputMessage = output.trim()
          ? [mapRawOutput(output)]
          : [];
        setMessages(
          [
            ...transcriptMessages,
            ...outputMessage,
            ...controls.map(mapPendingControl),
          ].slice(-HISTORY_CAP),
        );
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

function hasVisibleContent(msg: SDKMessage): boolean {
  if (msg.type === "assistant" || msg.type === "user") {
    return conversationText(msg).trim() !== "";
  }
  if (msg.type === "tool_progress") {
    return Boolean(msg.tool_name || String(msg.data ?? "").trim());
  }
  if (typeof msg.message === "string") {
    return msg.message.trim() !== "";
  }
  return msg.type === "control_request";
}

function conversationText(msg: SDKMessage): string {
  const message = msg.message;
  if (!message || typeof message !== "object" || !Array.isArray(message.content)) {
    return "";
  }
  return message.content
    .map((block) => {
      if (block.type === "text") return block.text ?? "";
      if (block.type === "thinking") return block.thinking ?? "";
      return "";
    })
    .join("\n");
}

function mapRawOutput(output: string): SDKMessage {
  return {
    type: "raw_output",
    message: trimOutput(output),
  };
}

function trimOutput(output: string): string {
  const lines = output.split(/\r?\n/);
  const tail = lines.slice(-200).join("\n").trim();
  return tail || output.slice(-20_000);
}

function mapPendingControl(row: ControlRequestDTO): SDKMessage {
  return {
    type: "control_request",
    request_id: row.request_id,
    request: {
      tool_name: row.tool_name,
      input: row.questions ? { questions: row.questions } : row.input,
    },
    message: row.summary,
  };
}
