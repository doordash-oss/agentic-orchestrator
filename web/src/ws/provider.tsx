import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useQueryClient } from "@tanstack/react-query";
import { createWSClient, type WSClient, type WSState } from "./client";
import type { AnyEnvelope, Envelope, FeaturePayload, FeaturePhasePayload, FeatureMessagePayload, PhaseResultPayload, FeatureCreatedPayload } from "./types";

// WSProvider owns one WebSocket connection per app instance and feeds
// envelopes into a small in-memory ring buffer for the activity feed
// and into TanStack Query cache invalidation so the existing list +
// detail views refetch on relevant events.

interface WSContextValue {
  client: WSClient;
  recent: AnyEnvelope[];
  state: WSState;
}

const WSContext = createContext<WSContextValue | null>(null);

const RECENT_CAP = 200; // ring-buffer size for the activity feed

export function WSProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient();
  const [state, setState] = useState<WSState>("connecting");
  const [recent, setRecent] = useState<AnyEnvelope[]>([]);
  // Keep a stable client across renders — React Strict Mode double-runs
  // effects so a useEffect-created client would churn.
  const clientRef = useRef<WSClient | null>(null);
  if (clientRef.current === null) {
    clientRef.current = createWSClient();
  }

  useEffect(() => {
    const client = clientRef.current!;
    const offState = client.onState(setState);
    const offMsg = client.subscribe((env) => {
      // Activity buffer: append, cap at RECENT_CAP.
      setRecent((prev) => {
        const next = prev.length >= RECENT_CAP ? prev.slice(1) : prev.slice();
        next.push(env);
        return next;
      });
      invalidateFor(env, qc);
    });
    return () => {
      offState();
      offMsg();
      // Don't close the client — Strict Mode would reconnect immediately.
      // The browser closes the socket on unload.
    };
  }, [qc]);

  const value = useMemo<WSContextValue>(
    () => ({ client: clientRef.current!, recent, state }),
    [recent, state],
  );

  return <WSContext.Provider value={value}>{children}</WSContext.Provider>;
}

export function useWSRecent(): AnyEnvelope[] {
  const ctx = useContext(WSContext);
  if (!ctx) throw new Error("useWSRecent must be used inside WSProvider");
  return ctx.recent;
}

export function useWSState(): WSState {
  const ctx = useContext(WSContext);
  if (!ctx) throw new Error("useWSState must be used inside WSProvider");
  return ctx.state;
}

// invalidateFor maps each event type onto the TanStack Query keys it
// touches. The convention: any event that changes a feature's
// summary or detail invalidates both ["features"] and the matching
// ["feature", id] entry. Server-drop notices invalidate everything so
// missed events don't leave stale state.
function invalidateFor(env: AnyEnvelope, qc: ReturnType<typeof useQueryClient>) {
  switch (env.type) {
    case "hello":
    case "pong":
      return;
    case "server.dropped":
      qc.invalidateQueries({ queryKey: ["features"] });
      qc.invalidateQueries({ queryKey: ["feature"] });
      return;
    case "feature.created":
    case "feature.started":
    case "feature.completed":
    case "feature.interrupted":
    case "feature.config_changed":
    case "tweak.review_approved":
    case "publish.started":
    case "publish.completed":
    case "review.required":
    case "phase.started":
    case "phase.completed":
    case "feature.advanced":
    case "feature.failed":
    case "need_user_input.required": {
      const id = featureIdFromPayload(env);
      qc.invalidateQueries({ queryKey: ["features"] });
      if (id) qc.invalidateQueries({ queryKey: ["feature", id] });
      return;
    }
    case "recovery.scanned":
    case "recovery.executed":
      // Recovery view doesn't have a dedicated cache yet; the list is
      // not affected. Surfaced in the activity feed only.
      return;
  }
}

// featureIdFromPayload extracts the feature_id field from any payload
// shape that has one. Returns null for shapes that don't.
function featureIdFromPayload(env: AnyEnvelope): string | null {
  const p = env.payload as
    | FeaturePayload
    | FeaturePhasePayload
    | FeatureMessagePayload
    | PhaseResultPayload
    | FeatureCreatedPayload
    | undefined;
  if (p && typeof p === "object" && "feature_id" in p) {
    const id = (p as { feature_id?: unknown }).feature_id;
    if (typeof id === "string" && id !== "") return id;
  }
  return null;
}

// Re-export Envelope so consumers can narrow without dipping into types.
export type { Envelope };
