import { useMemo } from "react";
import { useIsFetching, useIsMutating, useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import { useUI } from "../store/ui";
import { useDelayedFlag } from "./Spinner";
import type { FeatureSummary } from "../api/types";

// Top chrome: branding, live summary, healthz indicator, theme toggle.
// Stays mounted across feature changes so layout doesn't reflow.
//
// The header is intentionally taller than a typical app bar (h-16) to
// give the AgenticLogo room to breathe and to surface a one-glance
// summary of the pipeline state — total features, running, needs
// review, failed — so the operator never has to scan the feature list
// just to know if something is on fire.
export function TopBar() {
  const theme = useUI((s) => s.theme);
  const toggleTheme = useUI((s) => s.toggleTheme);

  const { data: health, error: healthError } = useQuery({
    queryKey: ["health"],
    queryFn: ({ signal }) => api.health(signal),
    refetchInterval: 5_000,
    retry: false,
  });

  // Re-use the cached features list (FeatureList already populates this).
  // Header never *kicks off* the request — it just observes whatever the
  // sidebar has fetched so we don't double the request volume.
  const { data: featuresData } = useQuery({
    queryKey: ["features"],
    queryFn: ({ signal }) => api.featuresList(signal),
    refetchInterval: 5_000,
  });

  const summary = useMemo(
    () => buildSummary(featuresData?.features ?? []),
    [featuresData?.features],
  );

  // Count global in-flight work, excluding the background health poll so
  // the bar doesn't blink every 5s. Gated by useDelayedFlag(300ms) so
  // sub-300ms actions never paint the bar at all.
  const fetching = useIsFetching({
    predicate: (q) => q.queryKey[0] !== "health",
  });
  const mutating = useIsMutating();
  const busy = useDelayedFlag(fetching + mutating > 0, 300);

  const statusTone = healthError
    ? "var(--status-error)"
    : health
      ? "var(--accent)"
      : "var(--text-tertiary)";
  const statusLabel = healthError
    ? "backend unreachable"
    : health
      ? "online"
      : "connecting…";

  return (
    <header
      className="relative h-16 border-b border-border px-5 flex items-center justify-between shrink-0 overflow-hidden"
      style={{
        background:
          "linear-gradient(135deg, var(--bg-secondary) 0%, var(--bg-primary) 55%, var(--bg-secondary) 100%)",
      }}
    >
      {/* Subtle accent glow on the left edge — gives the header a sense
          of depth without dominating. Pointer-events-none so it never
          intercepts clicks on the brand mark. */}
      <div
        aria-hidden
        className="pointer-events-none absolute -top-12 -left-16 w-72 h-72 rounded-full opacity-40 blur-3xl"
        style={{
          background:
            "radial-gradient(circle, var(--accent-glow) 0%, transparent 70%)",
        }}
      />

      {busy && (
        <div className="progress-bar absolute inset-x-0 bottom-0" aria-hidden>
          <div className="progress-bar__fill" />
        </div>
      )}

      {/* Brand block — logo + wordmark + tagline. */}
      <div className="relative flex items-center gap-3 min-w-0">
        <AgenticLogo />
        <div className="flex flex-col leading-tight min-w-0">
          <div className="flex items-baseline gap-2">
            <span
              className="text-lg font-bold tracking-tight"
              style={{
                background:
                  "linear-gradient(90deg, var(--text-primary), var(--accent-hover))",
                WebkitBackgroundClip: "text",
                WebkitTextFillColor: "transparent",
                backgroundClip: "text",
              }}
            >
              Agentic
            </span>
            <span
              className="text-[0.65rem] uppercase tracking-[0.18em] font-semibold px-1.5 py-px rounded-sm"
              style={{
                color: "var(--accent-hover)",
                background:
                  "color-mix(in srgb, var(--accent) 14%, transparent)",
                border:
                  "1px solid color-mix(in srgb, var(--accent) 40%, transparent)",
              }}
            >
              AI
            </span>
          </div>
          <span
            className="text-[0.7rem] font-medium text-text-tertiary font-mono truncate"
            title={
              health?.app_version
                ? `Orchestrator v${health.app_version}`
                : "Loading version…"
            }
          >
            Orchestrator v{health?.app_version ?? "…"}
          </span>
          <span className="text-[0.65rem] font-medium text-text-tertiary/80 truncate">
            Research → Plan → Implement → Publish
          </span>
        </div>
      </div>

      {/* Middle: live summary chips. Hidden on narrow widths so the
          brand block stays legible on small screens; reappears at md. */}
      <div className="relative hidden md:flex items-center gap-2">
        <SummaryChip label="Features" value={summary.total} tone="slate" />
        <SummaryChip
          label="Running"
          value={summary.running}
          tone="teal"
          pulse={summary.running > 0}
        />
        <SummaryChip
          label="Review"
          value={summary.review}
          tone="amber"
          dim={summary.review === 0}
        />
        <SummaryChip
          label="Failed"
          value={summary.failed}
          tone="rose"
          dim={summary.failed === 0}
        />
      </div>

      {/* Right cluster: live status, uptime, theme, help. */}
      <div className="relative flex items-center gap-3 text-text-tertiary text-sm">
        <span
          className="hidden lg:inline-flex items-center gap-1.5 text-[0.7rem] font-medium"
          title={
            health
              ? `Orchestrator v${health.app_version} · assets_embedded=${health.assets_embedded} · up ${health.uptime_seconds}s`
              : (healthError as Error | undefined)?.message
          }
        >
          <span
            aria-hidden
            className="inline-block w-2 h-2 rounded-full"
            style={{
              background: statusTone,
              boxShadow: health
                ? "0 0 6px var(--accent-glow)"
                : undefined,
            }}
          />
          <span style={{ color: healthError ? "var(--status-error)" : undefined }}>
            {statusLabel}
          </span>
          {health && (
            <span className="text-text-tertiary/70 font-mono">
              · up {formatUptime(health.uptime_seconds)}
            </span>
          )}
        </span>
        <button
          type="button"
          onClick={toggleTheme}
          className="w-8 h-8 inline-flex items-center justify-center rounded-md border border-border hover:bg-bg-tertiary hover:border-[var(--accent)] transition-colors"
          aria-label={`switch to ${theme === "dark" ? "light" : "dark"} theme`}
          title={`Switch to ${theme === "dark" ? "light" : "dark"} theme`}
        >
          <span className="text-base leading-none">
            {theme === "dark" ? "☀" : "☾"}
          </span>
        </button>
        <kbd
          className="px-1.5 py-0.5 text-[0.7rem] font-mono rounded-sm border border-border bg-bg-tertiary text-text-secondary"
          title="Press ? anywhere for keyboard shortcuts"
        >
          ?
        </kbd>
      </div>
    </header>
  );
}

// AgenticLogo — a hand-drawn SVG mark depicting an agent network:
// three orbiting nodes around a central "brain" node, joined by an
// orbit ring. Picks up the design system's --accent so it re-tints
// automatically when the dashboard accent changes.
function AgenticLogo() {
  return (
    <span
      className="relative inline-flex items-center justify-center w-10 h-10 rounded-lg shrink-0"
      style={{
        background:
          "linear-gradient(135deg, color-mix(in srgb, var(--accent) 20%, transparent), color-mix(in srgb, var(--accent) 4%, transparent))",
        border:
          "1px solid color-mix(in srgb, var(--accent) 35%, transparent)",
        boxShadow: "0 0 12px var(--accent-glow)",
      }}
      aria-hidden
    >
      <svg
        viewBox="0 0 40 40"
        width="28"
        height="28"
        fill="none"
        xmlns="http://www.w3.org/2000/svg"
      >
        <defs>
          <linearGradient id="agentic-grad" x1="0" y1="0" x2="40" y2="40">
            <stop offset="0%" stopColor="var(--accent)" />
            <stop offset="100%" stopColor="var(--accent-hover)" />
          </linearGradient>
          <radialGradient id="agentic-core" cx="50%" cy="50%" r="50%">
            <stop offset="0%" stopColor="var(--accent)" stopOpacity="1" />
            <stop offset="100%" stopColor="var(--accent-hover)" stopOpacity="0.6" />
          </radialGradient>
        </defs>

        {/* Orbit ring */}
        <circle
          cx="20"
          cy="20"
          r="14"
          stroke="url(#agentic-grad)"
          strokeWidth="1.25"
          strokeDasharray="3 3"
          opacity="0.55"
        >
          <animateTransform
            attributeName="transform"
            type="rotate"
            from="0 20 20"
            to="360 20 20"
            dur="24s"
            repeatCount="indefinite"
          />
        </circle>

        {/* Connector spokes */}
        <g stroke="url(#agentic-grad)" strokeWidth="1.25" opacity="0.7">
          <line x1="20" y1="20" x2="20" y2="6" />
          <line x1="20" y1="20" x2="32.1" y2="27" />
          <line x1="20" y1="20" x2="7.9" y2="27" />
        </g>

        {/* Satellite nodes */}
        <circle cx="20" cy="6" r="2.6" fill="url(#agentic-grad)">
          <animate
            attributeName="r"
            values="2.6;3.1;2.6"
            dur="2.4s"
            repeatCount="indefinite"
          />
        </circle>
        <circle cx="32.1" cy="27" r="2.6" fill="url(#agentic-grad)">
          <animate
            attributeName="r"
            values="2.6;3.1;2.6"
            dur="2.4s"
            begin="0.8s"
            repeatCount="indefinite"
          />
        </circle>
        <circle cx="7.9" cy="27" r="2.6" fill="url(#agentic-grad)">
          <animate
            attributeName="r"
            values="2.6;3.1;2.6"
            dur="2.4s"
            begin="1.6s"
            repeatCount="indefinite"
          />
        </circle>

        {/* Core */}
        <circle cx="20" cy="20" r="5" fill="url(#agentic-core)" />
        <circle cx="20" cy="20" r="5" fill="none" stroke="var(--bg-primary)" strokeWidth="0.6" />
      </svg>
    </span>
  );
}

interface SummaryChipProps {
  label: string;
  value: number;
  tone: "slate" | "teal" | "amber" | "rose";
  /** Render the value pulse-ring (e.g. for live "running" count). */
  pulse?: boolean;
  /** Render at reduced opacity when value is zero (avoids noisy header). */
  dim?: boolean;
}

function SummaryChip({ label, value, tone, pulse, dim }: SummaryChipProps) {
  return (
    <div
      className={`group flex items-center gap-2 pl-2 pr-2.5 py-1 rounded-md border transition-opacity ${
        dim ? "opacity-55 hover:opacity-100" : ""
      }`}
      style={{
        background: `var(--chip-${tone}-bg)`,
        borderColor: `var(--chip-${tone}-border)`,
        color: `var(--chip-${tone}-text)`,
      }}
      title={`${label}: ${value}`}
    >
      <span className="relative inline-flex items-center justify-center w-1.5 h-1.5">
        <span
          className="absolute inset-0 rounded-full"
          style={{ background: `var(--chip-${tone}-text)`, opacity: 0.85 }}
        />
        {pulse && (
          <span
            className="absolute inset-[-3px] rounded-full"
            style={{
              background: `var(--chip-${tone}-text)`,
              opacity: 0.4,
              animation: "spinner-pulse 1.6s ease-in-out infinite",
            }}
            aria-hidden
          />
        )}
      </span>
      <span className="text-[0.65rem] uppercase tracking-[0.08em] font-semibold opacity-80">
        {label}
      </span>
      <span className="text-sm font-bold font-mono tabular-nums">{value}</span>
    </div>
  );
}

interface HeaderSummary {
  total: number;
  running: number;
  review: number;
  failed: number;
}

function buildSummary(features: FeatureSummary[]): HeaderSummary {
  let running = 0;
  let review = 0;
  let failed = 0;
  for (const f of features) {
    if (f.is_running) running++;
    if (f.needs_review) review++;
    if (f.status === "Failed" || f.status === "Interrupted") failed++;
  }
  return { total: features.length, running, review, failed };
}

// formatUptime — turn a raw seconds-since-startup value into the
// shortest readable expression. The TUI footer uses the same idea; we
// mirror it here so users moving between clients see consistent units.
function formatUptime(seconds: number): string {
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  if (seconds < 86400)
    return `${Math.floor(seconds / 3600)}h${Math.floor((seconds % 3600) / 60)}m`;
  return `${Math.floor(seconds / 86400)}d`;
}
