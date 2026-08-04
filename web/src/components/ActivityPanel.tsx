import { useMemo } from "react";
import { useWSRecent, useWSState } from "../ws/provider";
import type { AnyEnvelope, EventType } from "../ws/types";

// Right column. Renders the most-recent WebSocket envelopes received
// from the orchestrator's event bus, newest first. The list caps at
// 200 entries (in the provider). Rows mirror the observability-panel
// inspiration: timestamp · check icon · semantic chip · summary · gap
// since the previous event.

// Number of buckets across the histogram strip. Wider than the feed
// itself so individual seconds read clearly; bucketed across the span
// of envelopes currently in memory.
const HIST_BUCKETS = 40;

// Cap for the per-row duration bar. Anything longer than this fills
// the track entirely; the visual is meant to show relative quiet vs
// burst, not absolute milliseconds.
const DURATION_CAP_MS = 60_000;

export function ActivityPanel() {
  const recent = useWSRecent();
  const state = useWSState();

  // Reverse for newest-first display without mutating provider state.
  const ordered = useMemo(() => [...recent].reverse(), [recent]);
  const histogram = useMemo(() => buildHistogram(recent, HIST_BUCKETS), [recent]);

  return (
    <section className="h-full flex flex-col bg-bg-secondary">
      <header className="px-3 py-2 border-b border-border">
        <div className="flex items-center justify-between">
          <h2 className="text-xs font-semibold uppercase tracking-wide text-text-tertiary">
            Activity
          </h2>
          <span className="text-[0.65rem] flex items-center gap-1.5">
            <span
              aria-hidden
              className="inline-block w-1.5 h-1.5 rounded-full"
              style={{ background: toneFor(state) }}
            />
            <span className="text-text-tertiary">{state}</span>
          </span>
        </div>
        {histogram && (
          <div className="mt-2">
            <div className="histogram" aria-hidden>
              {histogram.bars.map((h, i) => {
                let mod = "";
                if (h === 0) mod = " histogram__bar--empty";
                else if (h === histogram.peak) mod = " histogram__bar--peak";
                return (
                  <span
                    key={i}
                    className={`histogram__bar${mod}`}
                    style={{
                      height:
                        h === 0
                          ? "10%"
                          : `${Math.max(12, (h / histogram.peak) * 100)}%`,
                    }}
                  />
                );
              })}
            </div>
            <div className="histogram__axis">
              <span>{histogram.startLabel}</span>
              <span className="text-text-secondary tabular-nums">
                {recent.length} events
              </span>
              <span>{histogram.endLabel}</span>
            </div>
          </div>
        )}
      </header>
      <div className="flex-1 overflow-auto text-xs">
        {ordered.length === 0 && (
          <div className="p-3 text-text-tertiary italic">
            Waiting for events from the orchestrator…
          </div>
        )}
        {ordered.map((env, i) => {
          // Gap to the previous (older) envelope — `ordered` is newest
          // first, so the older envelope is at index i+1.
          const prev = ordered[i + 1];
          const gapMs = gapBetween(env, prev);
          return (
            <ActivityRow
              key={env.seq ?? i}
              env={env}
              gapMs={gapMs}
            />
          );
        })}
      </div>
    </section>
  );
}

// buildHistogram buckets envelope timestamps into evenly-sized time
// slices across the observed span and returns per-bucket counts plus
// formatted start/end labels for the axis. Returns null if there's
// nothing to plot (≤ 1 envelope, no usable timestamps).
function buildHistogram(
  envelopes: readonly AnyEnvelope[],
  bucketCount: number,
): { bars: number[]; peak: number; startLabel: string; endLabel: string } | null {
  if (envelopes.length < 2) return null;
  const times: number[] = [];
  for (const e of envelopes) {
    const t = Date.parse(e.ts);
    if (!Number.isNaN(t)) times.push(t);
  }
  if (times.length < 2) return null;
  const min = Math.min(...times);
  const max = Math.max(...times);
  if (max <= min) return null;
  const span = max - min;
  const bars = new Array(bucketCount).fill(0);
  for (const t of times) {
    const idx = Math.min(
      bucketCount - 1,
      Math.floor(((t - min) / span) * bucketCount),
    );
    bars[idx]++;
  }
  const peak = Math.max(...bars, 1);
  return {
    bars,
    peak,
    startLabel: formatHistogramTime(min),
    endLabel: formatHistogramTime(max),
  };
}

function formatHistogramTime(ms: number): string {
  try {
    return new Date(ms).toLocaleTimeString("en-GB", {
      hour: "2-digit",
      minute: "2-digit",
    });
  } catch {
    return "";
  }
}

function gapBetween(a: AnyEnvelope, b: AnyEnvelope | undefined): number | null {
  if (!b) return null;
  const ta = Date.parse(a.ts);
  const tb = Date.parse(b.ts);
  if (Number.isNaN(ta) || Number.isNaN(tb)) return null;
  return Math.max(0, ta - tb);
}

// chipClassForCategory returns the full literal chip class so
// Tailwind's content scanner can detect every variant. Template-string
// construction (`chip--${color}`) would let the scanner prune rules
// whose suffix isn't seen elsewhere as a literal.
function chipClassForCategory(
  color: ReturnType<typeof categoryFor>["color"],
): string {
  switch (color) {
    case "teal":
      return "chip chip--teal";
    case "blue":
      return "chip chip--blue";
    case "purple":
      return "chip chip--purple";
    case "amber":
      return "chip chip--amber";
    case "rose":
      return "chip chip--rose";
    case "green":
      return "chip chip--green";
    case "slate":
      return "chip chip--slate";
  }
}

function ActivityRow({ env, gapMs }: { env: AnyEnvelope; gapMs: number | null }) {
  const cat = categoryFor(env.type);
  return (
    <article className="px-3 py-2 border-b border-border last:border-b-0 hover:bg-bg-tertiary transition-colors">
      <div className="flex items-center gap-2">
        <time className="text-[0.65rem] text-text-tertiary tabular-nums shrink-0">
          {formatTime(env.ts)}
        </time>
        <StatusIcon kind={cat.status} />
        <span className={chipClassForCategory(cat.color)}>
          {shortType(env.type)}
        </span>
        <span className="flex-1 min-w-0 text-text-primary leading-snug truncate">
          {summarise(env)}
        </span>
        {gapMs !== null && <DurationBar ms={gapMs} />}
      </div>
    </article>
  );
}

function StatusIcon({
  kind,
}: {
  kind: "ok" | "warn" | "error" | "interrupted" | "muted";
}) {
  const mod =
    kind === "warn"
      ? " check-icon--warning"
      : kind === "error"
        ? " check-icon--error"
        : kind === "muted"
          ? " check-icon--muted"
          : "";
  const style =
    kind === "interrupted"
      ? {
          borderColor: "var(--chip-purple-text)",
          color: "var(--chip-purple-text)",
        }
      : undefined;
  return (
    <span className={`check-icon${mod}`} aria-hidden style={style}>
      {kind === "error" ? (
        <svg viewBox="0 0 10 10" fill="none">
          <path
            d="M2 2l6 6M8 2l-6 6"
            stroke="currentColor"
            strokeWidth="1.5"
            strokeLinecap="round"
          />
        </svg>
      ) : kind === "interrupted" ? (
        <svg viewBox="0 0 10 10" fill="none">
          <path
            d="M4 2.5v5M6 2.5v5"
            stroke="currentColor"
            strokeWidth="1.6"
            strokeLinecap="round"
          />
        </svg>
      ) : kind === "warn" ? (
        <svg viewBox="0 0 10 10" fill="none">
          <path
            d="M5 2.5v3M5 7.25v.25"
            stroke="currentColor"
            strokeWidth="1.5"
            strokeLinecap="round"
          />
        </svg>
      ) : (
        <svg viewBox="0 0 10 10" fill="none">
          <path
            d="M2 5.25l2 2 4-4.5"
            stroke="currentColor"
            strokeWidth="1.6"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </svg>
      )}
    </span>
  );
}

function DurationBar({ ms }: { ms: number }) {
  // 0 ms still shows a sliver so the bar is visible at burst time.
  const pct = Math.max(4, Math.min(100, (ms / DURATION_CAP_MS) * 100));
  return (
    <span className="duration-bar shrink-0" title={`${ms} ms since previous event`}>
      <span className="duration-bar__track">
        <span className="duration-bar__fill" style={{ width: `${pct}%` }} />
      </span>
      <span className="duration-bar__label">{formatGap(ms)}</span>
    </span>
  );
}

function formatGap(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(s < 10 ? 1 : 0)}s`;
  const m = Math.floor(s / 60);
  return `${m}m`;
}

function shortType(t: EventType): string {
  // Drop the namespace prefix and replace remaining dots with spaces
  // so the chip stays compact (`feature.advanced` → `advanced`).
  const dot = t.lastIndexOf(".");
  return dot >= 0 ? t.slice(dot + 1) : t;
}

// categoryFor groups every event type into a colour family + a
// status (ok / warn / error / interrupted / muted). Co-located with the
// type switch in summarise() so the two stay in sync.
function categoryFor(t: EventType): {
  color: "teal" | "blue" | "purple" | "amber" | "rose" | "green" | "slate";
  status: "ok" | "warn" | "error" | "interrupted" | "muted";
} {
  switch (t) {
    case "feature.failed":
    case "server.dropped":
      return { color: "rose", status: "error" };
    case "feature.interrupted":
      return { color: "purple", status: "interrupted" };
    case "feature.completed":
    case "publish.completed":
    case "tweak.review_approved":
      return { color: "green", status: "ok" };
    case "feature.created":
    case "feature.started":
    case "feature.advanced":
    case "feature.config_changed":
      return { color: "teal", status: "ok" };
    case "phase.started":
    case "phase.completed":
      return { color: "purple", status: "ok" };
    case "publish.started":
      return { color: "blue", status: "ok" };
    case "review.required":
    case "need_user_input.required":
      return { color: "amber", status: "warn" };
    case "recovery.scanned":
    case "recovery.executed":
      return { color: "slate", status: "muted" };
    case "hello":
    case "pong":
      return { color: "slate", status: "muted" };
    case "session.history":
    case "session.message":
    case "session.done":
      return { color: "slate", status: "muted" };
  }
}

function summarise(env: AnyEnvelope): string {
  const p = env.payload as Record<string, unknown> | undefined;
  switch (env.type) {
    case "hello":
      return "connected";
    case "server.dropped":
      return `server dropped ${asNumber(p?.["dropped"])} events — caches refreshed`;
    case "pong":
      return "heartbeat";
    case "feature.created":
      return `feature ${asString(p?.["name"]) || asString(p?.["feature_id"])} created`;
    case "feature.started":
      return `feature ${asString(p?.["feature_id"])} started`;
    case "feature.advanced":
      return `${asString(p?.["feature_id"])} → phase ${asString(p?.["phase"])}`;
    case "feature.completed":
      return `feature ${asString(p?.["feature_id"])} completed`;
    case "feature.failed":
      return `feature ${asString(p?.["feature_id"])} failed${msgSuffix(p)}`;
    case "feature.interrupted":
      return `feature ${asString(p?.["feature_id"])} interrupted`;
    case "feature.config_changed":
      return `feature ${asString(p?.["feature_id"])} config updated`;
    case "phase.started":
      return `${asString(p?.["feature_id"])} starting phase ${asString(p?.["phase"])}`;
    case "phase.completed":
      return `${asString(p?.["feature_id"])} finished ${asString(p?.["phase"])}${p?.["errored"] ? " (with error)" : ""}`;
    case "review.required":
      return `${asString(p?.["feature_id"])} needs review for ${asString(p?.["phase"])}`;
    case "publish.started":
      return `${asString(p?.["feature_id"])} publishing`;
    case "publish.completed":
      return `${asString(p?.["feature_id"])} publish completed${p?.["errored"] ? " (with error)" : ""}`;
    case "recovery.scanned":
      return `recovery scan: ${asString(p?.["message"])}`;
    case "recovery.executed":
      return `recovery: ${asString(p?.["feature_id"])} ${asString(p?.["message"])}`;
    case "tweak.review_approved":
      return `${asString(p?.["feature_id"])} tweak approved`;
    case "need_user_input.required":
      return `${asString(p?.["feature_id"])} needs input${msgSuffix(p)}`;
    case "session.history":
    case "session.message":
    case "session.done":
      // Session frames flow on a separate per-session WS, not the
      // global bus — they shouldn't appear in this feed in practice,
      // but the exhaustive switch needs a branch for them.
      return "";
  }
}

function msgSuffix(p?: Record<string, unknown>): string {
  const m = asString(p?.["message"]);
  return m ? ` — ${m}` : "";
}

function asString(v: unknown): string {
  return typeof v === "string" ? v : "";
}

function asNumber(v: unknown): number {
  return typeof v === "number" ? v : 0;
}

function formatTime(ts: string): string {
  if (!ts) return "";
  try {
    const d = new Date(ts);
    return d.toLocaleTimeString("en-GB", {
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    });
  } catch {
    return ts;
  }
}

function toneFor(state: string): string {
  switch (state) {
    case "open":
      return "var(--accent)";
    case "connecting":
    case "reconnecting":
      return "var(--status-warning)";
    default:
      return "var(--text-tertiary)";
  }
}
