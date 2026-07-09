import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "../api/client";
import type { FeatureSummary } from "../api/types";
import { useUI } from "../store/ui";
import { useKeyAction } from "./KeymapProvider";
import { ConfirmDeleteModal } from "./ConfirmDeleteModal";

// Groupings shown in the left column. Each bucket runs the same
// status-set predicate the TUI uses so both UIs paint identically.
//
// "Stopped" carries Failed + Interrupted because those features need
// user action (restart, rewind, mark done) — they are not completed.
// Keeping them out of "Completed" prevents accidentally treating a
// crashed run as finished.
const BUCKETS: { key: string; label: string; match: (f: FeatureSummary) => boolean }[] = [
  {
    key: "in-progress",
    label: "In Progress",
    match: (f) =>
      f.is_running ||
      f.needs_review ||
      [
        "Created",
        "PlanReady",
        "ImplementReady",
        "ReviewPassed",
        "CodeReady",
        "InquireReady",
        "BrainstormReady",
        "NeedUserInput",
      ].includes(f.status),
  },
  {
    key: "stopped",
    label: "Stopped",
    match: (f) => ["Failed", "Interrupted"].includes(f.status),
  },
  { key: "published", label: "Published", match: (f) => f.status === "Published" },
  {
    key: "completed",
    label: "Completed",
    match: (f) => f.status === "Done",
  },
];

export function FeatureList() {
  const selectedId = useUI((s) => s.selectedFeatureId);
  const selectFeature = useUI((s) => s.selectFeature);
  const collapsed = useUI((s) => s.collapsedSections);
  const toggleSection = useUI((s) => s.toggleSection);
  const openWizard = useUI((s) => s.openWizard);
  const [filter, setFilter] = useState("");

  const { data, isLoading, error } = useQuery({
    queryKey: ["features"],
    queryFn: ({ signal }) => api.featuresList(signal),
    refetchInterval: 10_000,
  });

  useEffect(() => {
    if (!data) return;
    const features = data.features;
    if (features.length === 0) {
      if (selectedId) selectFeature(null);
      return;
    }
    if (!selectedId || !features.some((f) => f.id === selectedId)) {
      selectFeature(features[0].id);
    }
  }, [data, selectedId, selectFeature]);

  const grouped = useMemo(() => {
    const features = data?.features ?? [];
    const needle = filter.trim().toLowerCase();
    const visible = needle
      ? features.filter(
          (f) =>
            f.name.toLowerCase().includes(needle) ||
            f.slug.toLowerCase().includes(needle) ||
            (f.tags ?? []).some((t) => t.toLowerCase().includes(needle)),
        )
      : features;

    return BUCKETS.map((b) => ({
      ...b,
      items: visible.filter(b.match),
    }));
  }, [data, filter]);

  // Grand total across every currently-visible feature. Mirrors the
  // TUI's footer total ($31.71 in the screenshot brief). Filter
  // narrows it so users can scope the figure to whatever bucket
  // they're inspecting.
  const grandTotal = useMemo(() => {
    let sum = 0;
    for (const g of grouped) {
      for (const f of g.items) sum += f.total_cost_usd ?? 0;
    }
    return sum;
  }, [grouped]);

  // j/k navigation: flatten the currently-visible features (across
  // open buckets) and step the selection index.
  const flat = useMemo(() => {
    const out: FeatureSummary[] = [];
    for (const g of grouped) {
      if (collapsed[g.key]) continue;
      for (const f of g.items) out.push(f);
    }
    return out;
  }, [grouped, collapsed]);

  useKeyAction("navDown", () => {
    if (flat.length === 0) return;
    const i = flat.findIndex((f) => f.id === selectedId);
    const nextIdx = i === -1 ? 0 : Math.min(flat.length - 1, i + 1);
    selectFeature(flat[nextIdx].id);
  });
  useKeyAction("navUp", () => {
    if (flat.length === 0) return;
    const i = flat.findIndex((f) => f.id === selectedId);
    const prevIdx = i === -1 ? 0 : Math.max(0, i - 1);
    selectFeature(flat[prevIdx].id);
  });
  useKeyAction("openSelected", () => {
    // Hand focus to the detail panel by scrolling it into view.
    if (!selectedId) return;
    const el = document.querySelector<HTMLElement>("section[aria-label='feature detail']");
    if (el) {
      el.scrollIntoView({ behavior: "smooth", block: "start" });
      el.focus?.();
    }
  });

  return (
    <section className="h-full flex flex-col bg-bg-secondary border-r border-border">
      <div className="p-3 border-b border-border space-y-2">
        <div className="flex gap-2 items-center">
          <label className="search-input">
            <svg
              className="search-input__icon"
              viewBox="0 0 16 16"
              fill="none"
              aria-hidden
            >
              <circle
                cx="7"
                cy="7"
                r="4.5"
                stroke="currentColor"
                strokeWidth="1.5"
              />
              <path
                d="m10.5 10.5 3 3"
                stroke="currentColor"
                strokeWidth="1.5"
                strokeLinecap="round"
              />
            </svg>
            <input
              type="search"
              placeholder="Filter features…"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
            />
          </label>
          <button
            type="button"
            onClick={openWizard}
            className="shrink-0 px-2.5 py-1 text-sm rounded-full text-text-inverse"
            style={{ background: "var(--accent)" }}
            title="Create a new feature (n)"
          >
            + new
          </button>
        </div>
      </div>

      <div className="flex-1 overflow-auto">
        {isLoading && (
          <div className="p-4 text-sm text-text-tertiary">loading…</div>
        )}
        {error && (
          <div className="p-4 text-sm" style={{ color: "var(--status-error)" }}>
            failed to load features
          </div>
        )}
        {!isLoading &&
          !error &&
          grouped.map((g) => {
            const isCollapsed = !!collapsed[g.key];
            return (
              <div key={g.key} className="border-b border-border last:border-b-0">
                <button
                  type="button"
                  onClick={() => toggleSection(g.key)}
                  className="w-full text-left px-3 py-2 text-xs font-semibold uppercase tracking-wide text-text-tertiary hover:bg-bg-tertiary flex items-center justify-between"
                >
                  <span>
                    <span className="inline-block w-3">{isCollapsed ? "▸" : "▾"}</span>{" "}
                    {g.label}
                  </span>
                  <span className="text-text-tertiary">{g.items.length}</span>
                </button>
                {!isCollapsed && (
                  <ul>
                    {g.items.map((f) => (
                      <FeatureRow
                        key={f.id}
                        f={f}
                        selected={f.id === selectedId}
                        onSelect={() => selectFeature(f.id)}
                      />
                    ))}
                    {g.items.length === 0 && (
                      <li className="px-3 py-2 text-xs text-text-tertiary italic">
                        none
                      </li>
                    )}
                  </ul>
                )}
              </div>
            );
          })}
      </div>
      {grandTotal > 0 && (
        <footer className="px-3 py-2 border-t border-border flex items-center justify-between text-[0.7rem] text-text-tertiary">
          <span>total</span>
          <span className="font-mono tabular-nums text-text-primary">
            {formatUSD(grandTotal)}
          </span>
        </footer>
      )}
    </section>
  );
}
// formatUSD renders a USD cost compactly. Mirrors the TUI: two-decimal
// precision for small numbers, $-prefixed.
function formatUSD(n: number): string {
  return `$${n.toFixed(2)}`;
}

// FeatureRow owns its own per-feature stop/delete mutations. Hover or
// keyboard focus reveals the two icon actions on the right edge of the
// row; the rest of the row stays a click target for selection.
function FeatureRow({
  f,
  selected,
  onSelect,
}: {
  f: FeatureSummary;
  selected: boolean;
  onSelect: () => void;
}) {
  const qc = useQueryClient();
  const setSelected = useUI((s) => s.selectFeature);
  const [confirmOpen, setConfirmOpen] = useState(false);

  const stopMut = useMutation({
    mutationFn: () => api.stopFeature(f.id),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["features"] });
      await qc.invalidateQueries({ queryKey: ["feature", f.id] });
    },
  });
  const deleteMut = useMutation({
    mutationFn: () => api.deleteFeature(f.id),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["features"] });
      if (selected) setSelected(null);
      setConfirmOpen(false);
    },
  });
  const pendingCount = (f.help_pending ?? 0) + (f.permissions_pending ?? 0);
  const hasPendingInput = pendingCount > 0;
  const pendingTitle =
    [
      f.help_pending ? `${f.help_pending} help request${f.help_pending === 1 ? "" : "s"}` : "",
      f.permissions_pending
        ? `${f.permissions_pending} permission request${f.permissions_pending === 1 ? "" : "s"}`
        : "",
    ]
      .filter(Boolean)
      .join(", ") || "No pending requests";

  return (
    <li
      className="group relative border-b-2 border-dashed last:border-b-0"
      style={{ borderColor: "#ceeee9" }}
    >
      <button
        type="button"
        onClick={onSelect}
        className={`w-full text-left px-3 py-2 text-sm border-l-2 transition ${
          selected
            ? "border-l-[var(--accent)] bg-bg-tertiary text-text-primary"
            : hasPendingInput
              ? "border-l-[var(--status-warning)] bg-bg-tertiary text-text-secondary hover:bg-bg-tertiary"
              : "border-l-transparent text-text-secondary hover:bg-bg-tertiary"
        }`}
        title={`${f.status} · ${pendingTitle} · ${f.repo_names?.join(", ") ?? "—"}`}
      >
        <div className="flex items-center justify-between gap-2">
          <div className="flex items-center gap-2 min-w-0">
            <StatusIcon status={f.status} />
            {hasPendingInput && (
              <span
                aria-label={pendingTitle}
                className="shrink-0 inline-flex h-4 w-4 items-center justify-center rounded-sm text-[0.6rem] font-bold"
                style={{
                  background: "var(--banner-warning-icon)",
                  color: "var(--text-inverse)",
                }}
              >
                !
              </span>
            )}
            <span className="truncate">{f.name || f.slug}</span>
          </div>
          {typeof f.total_cost_usd === "number" && f.total_cost_usd > 0 && (
            <span
              className="shrink-0 text-[0.65rem] font-mono tabular-nums"
              style={{ color: "var(--text-tertiary)" }}
            >
              {formatUSD(f.total_cost_usd)}
            </span>
          )}
        </div>
        <div className="mt-1 flex items-center gap-1.5 flex-wrap">
          <span className={chipClassFor(f.status)}>{f.status}</span>
          {f.current_phase && (
            <span className="chip chip--slate">{f.current_phase}</span>
          )}
          {f.help_pending ? (
            <span className="chip chip--amber">
              {f.help_pending} help pending
            </span>
          ) : null}
          {f.permissions_pending ? (
            <span className="chip chip--amber">
              {f.permissions_pending === 1
                ? "permission pending"
                : `${f.permissions_pending} permissions pending`}
            </span>
          ) : null}
        </div>
      </button>
      <div
        className="absolute right-1.5 top-1.5 flex gap-1 opacity-0 group-hover:opacity-100 focus-within:opacity-100 transition-opacity"
        // Sibling of the main button — clicks on these don't bubble
        // to onSelect because they're not nested inside it.
      >
        {f.is_running && (
          <RowIconButton
            ariaLabel="Stop running sessions"
            title="Stop running sessions"
            disabled={stopMut.isPending}
            tone="warning"
            onClick={() => stopMut.mutate()}
          >
            {stopMut.isPending ? "…" : "■"}
          </RowIconButton>
        )}
        <RowIconButton
          ariaLabel="Delete feature"
          title="Delete feature"
          tone="error"
          onClick={() => setConfirmOpen(true)}
        >
          ✕
        </RowIconButton>
      </div>
      <ConfirmDeleteModal
        featureName={f.name || f.slug || f.id}
        open={confirmOpen}
        isRunning={f.is_running}
        pending={deleteMut.isPending}
        error={
          deleteMut.error instanceof ApiError
            ? deleteMut.error.message
            : deleteMut.error instanceof Error
              ? deleteMut.error.message
              : null
        }
        onCancel={() => setConfirmOpen(false)}
        onConfirm={() => deleteMut.mutate()}
      />
    </li>
  );
}

function RowIconButton({
  children,
  onClick,
  ariaLabel,
  title,
  disabled,
  tone,
}: {
  children: React.ReactNode;
  onClick: () => void;
  ariaLabel: string;
  title: string;
  disabled?: boolean;
  tone: "warning" | "error";
}) {
  const color =
    tone === "warning" ? "var(--status-warning)" : "var(--status-error)";
  return (
    <button
      type="button"
      onClick={(e) => {
        // Defensive: prevent any bubbling that React-portal'd children
        // might otherwise cause. The action is a sibling of the
        // selection button in the DOM, but we keep this for clarity.
        e.stopPropagation();
        onClick();
      }}
      disabled={disabled}
      aria-label={ariaLabel}
      title={title}
      className="w-6 h-6 inline-flex items-center justify-center rounded-sm border text-[0.7rem] leading-none hover:bg-bg-secondary disabled:opacity-50 disabled:cursor-not-allowed"
      style={{
        color,
        borderColor: `color-mix(in srgb, ${color} 40%, transparent)`,
        background: "var(--bg-primary)",
      }}
    >
      {children}
    </button>
  );
}

// statusFamily groups every feature status into one of six families
// used for both the leading icon and the chip colour. `interrupted` is
// split out from `error` because a user-cancelled run isn't a failure —
// it deserves its own (purple) treatment so the eye doesn't read it as
// red.
type StatusFamily =
  | "ok"
  | "warn"
  | "error"
  | "interrupted"
  | "muted"
  | "running";

function statusFamily(status: string): StatusFamily {
  if (status === "Done" || status === "Published") return "ok";
  if (status === "Failed") return "error";
  if (status === "Interrupted") return "interrupted";
  if (status === "NeedUserInput" || status.endsWith("NeedsReview"))
    return "warn";
  if (status === "Created") return "muted";
  return "running";
}

// chipClassFor returns the full chip class as a literal string so
// Tailwind's content scanner detects every variant. Template-literal
// construction like `chip--${family}` would let the scanner prune
// rules whose suffix isn't seen elsewhere as a literal.
function chipClassFor(status: string): string {
  switch (statusFamily(status)) {
    case "ok":
      return "chip chip--green";
    case "error":
      return "chip chip--rose";
    case "interrupted":
      return "chip chip--purple";
    case "warn":
      return "chip chip--amber";
    case "muted":
      return "chip chip--slate";
    case "running":
      return "chip chip--teal";
  }
}

function StatusIcon({ status }: { status: string }) {
  const family = statusFamily(status);
  if (family === "error") {
    return (
      <span className="check-icon check-icon--error" aria-hidden>
        <svg viewBox="0 0 10 10" fill="none">
          <path
            d="M2 2l6 6M8 2l-6 6"
            stroke="currentColor"
            strokeWidth="1.5"
            strokeLinecap="round"
          />
        </svg>
      </span>
    );
  }
  if (family === "interrupted") {
    return (
      <span
        className="check-icon"
        aria-hidden
        style={{
          borderColor: "var(--chip-purple-text)",
          color: "var(--chip-purple-text)",
        }}
      >
        <svg viewBox="0 0 10 10" fill="none">
          <path
            d="M4 2.5v5M6 2.5v5"
            stroke="currentColor"
            strokeWidth="1.6"
            strokeLinecap="round"
          />
        </svg>
      </span>
    );
  }
  if (family === "warn") {
    return (
      <span className="check-icon check-icon--warning" aria-hidden>
        <svg viewBox="0 0 10 10" fill="none">
          <path
            d="M5 2.5v3M5 7.25v.25"
            stroke="currentColor"
            strokeWidth="1.6"
            strokeLinecap="round"
          />
        </svg>
      </span>
    );
  }
  if (family === "ok") {
    return (
      <span className="check-icon" aria-hidden>
        <svg viewBox="0 0 10 10" fill="none">
          <path
            d="M2 5.25l2 2 4-4.5"
            stroke="currentColor"
            strokeWidth="1.6"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </svg>
      </span>
    );
  }
  if (family === "running") {
    return (
      <span className="check-icon" aria-hidden style={{ borderColor: "var(--accent)" }}>
        <svg viewBox="0 0 10 10" fill="none">
          <circle
            cx="5"
            cy="5"
            r="1.5"
            fill="var(--accent)"
          />
        </svg>
      </span>
    );
  }
  return (
    <span className="check-icon check-icon--muted" aria-hidden>
      <svg viewBox="0 0 10 10" fill="none">
        <circle cx="5" cy="5" r="1.25" fill="currentColor" />
      </svg>
    </span>
  );
}
