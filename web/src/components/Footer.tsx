import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import { useUI } from "../store/ui";
import type { FeatureSummary } from "../api/types";

// Web mirror of the TUI's dashboard footer (internal/tui/dashboard.go
// renderFooter): left-aligned context-sensitive key hints, right-aligned
// brand-coloured Help hint. Hints are derived from the currently
// selected feature so the strip changes shape as you move through the
// list. The mint brand colour matches the TUI's [/] Ask / [?] Help
// emphasis so users learning either client see the same visual cue.
//
// Keys shown here are the ones already wired up in the web keymap
// (web/src/keymap.ts); we deliberately omit TUI-only actions like
// [tab] Panel (no kb-driven panel focus on the web) and [s] Stop /
// [r] Restart (no web action yet).

interface Hint {
  key: string;
  label: string;
  /** When true, render in warning amber (used for "needs review"). */
  warn?: boolean;
}

export function Footer() {
  const id = useUI((s) => s.selectedFeatureId);

  // Use the cached features list so we don't fire an extra request.
  // FeatureList already populates this query.
  const { data } = useQuery({
    queryKey: ["features"],
    queryFn: ({ signal }) => api.featuresList(signal),
    refetchInterval: 5_000,
  });
  const selected = data?.features.find((f) => f.id === id);

  const hints = buildHints(selected);
  const layout = layoutHint();

  return (
    <footer className="h-7 border-t border-border bg-bg-secondary px-3 flex items-center justify-between text-[0.7rem] font-mono text-text-tertiary shrink-0">
      <div className="flex items-center gap-4 overflow-hidden whitespace-nowrap">
        {hints.map((h) => (
          <KeyHint key={h.key + h.label} hint={h} />
        ))}
      </div>
      <div className="flex items-center gap-4 shrink-0">
        <span className="text-text-tertiary/70">Layout: {layout}</span>
        <BrandHint k="/" label="Search" />
        <BrandHint k="?" label="Help" />
      </div>
    </footer>
  );
}

function KeyHint({ hint }: { hint: Hint }) {
  const labelColor = hint.warn ? "var(--status-warning)" : undefined;
  return (
    <span className="inline-flex items-center gap-1.5">
      <kbd
        className="px-1 py-0 rounded-sm border border-border bg-bg-tertiary text-text-secondary"
        style={hint.warn ? { color: "var(--status-warning)" } : undefined}
      >
        {hint.key}
      </kbd>
      <span style={labelColor ? { color: labelColor } : undefined}>
        {hint.label}
      </span>
    </span>
  );
}

function BrandHint({ k, label }: { k: string; label: string }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <kbd
        className="px-1 py-0 rounded-sm border font-semibold"
        style={{
          color: "var(--accent)",
          borderColor: "color-mix(in srgb, var(--accent) 50%, transparent)",
          background: "color-mix(in srgb, var(--accent) 8%, transparent)",
        }}
      >
        {k}
      </kbd>
      <span className="font-semibold" style={{ color: "var(--accent)" }}>
        {label}
      </span>
    </span>
  );
}

function buildHints(f: FeatureSummary | undefined): Hint[] {
  const hints: Hint[] = [{ key: "n", label: "New" }];
  if (!f) return hints;

  // Mirror the TUI's selected-feature hints, restricted to actions the
  // web keymap currently dispatches.
  if (f.needs_review) {
    hints.push({ key: "r", label: "Review", warn: true });
  } else {
    hints.push({ key: "r", label: "Artifact review" });
  }
  hints.push({ key: "l", label: "Logs" });
  hints.push({ key: "g", label: "Reviews" });
  hints.push({ key: "b", label: "Rewind" });
  if (isPublishableStatus(f)) {
    hints.push({ key: "p", label: "Publish" });
  }
  // Stop only when there are running sessions to stop; delete is
  // always available (with confirm gate).
  if (f.is_running) {
    hints.push({ key: "s", label: "Stop" });
  }
  hints.push({ key: "d", label: "Delete" });
  return hints;
}

function isPublishableStatus(f: FeatureSummary): boolean {
  // Match the TUI's "show Publish" predicate as closely as we can with
  // just summary fields. The detail view has stricter checks; the
  // footer hint is a discoverability cue, not an authority on whether
  // publish will succeed.
  return f.status === "CodeReady" || f.status === "Published";
}

function layoutHint(): string {
  if (typeof navigator === "undefined") return "—";
  // navigator.language is locale (e.g. "en-GB"). Strip to region for a
  // compact display matching the TUI's "US" / "GB" style hint.
  const lang = navigator.language || "en-US";
  const parts = lang.split("-");
  return (parts[1] || parts[0] || "—").toUpperCase();
}
