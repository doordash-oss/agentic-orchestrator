import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "../api/client";
import type {
  ArtifactRef,
  ReviewDecisionRequest,
  ReviewDecisionValue,
} from "../api/types";
import { MarkdownPreview } from "./MarkdownPreview";

// Renderer choice for the viewer pane. Default depends on the
// artifact's MIME (markdown auto-renders; everything else stays raw),
// and the user can flip via the small toolbar above the body. State
// resets to the format-appropriate default whenever the selected
// artifact changes.
type ViewerMode = "rendered" | "raw";

function defaultModeFor(mime: string | undefined): ViewerMode {
  return mime?.startsWith("text/markdown") ? "rendered" : "raw";
}

// Short label rendered next to each artifact key in the left rail.
function shortFormat(mime: string | undefined): string | null {
  if (!mime) return null;
  if (mime.startsWith("text/markdown")) return "MD";
  if (mime.startsWith("application/json")) return "JSON";
  if (mime.startsWith("application/x-yaml")) return "YAML";
  if (mime.startsWith("text/plain")) return "TXT";
  return null;
}

// ArtifactReviewModal lists a feature's artifacts (plan, design, etc.)
// in a left rail, renders the selected one as markdown source in a
// monospace viewer, and exposes the proceed / iterate verdict that
// HandleReviewDecision accepts. The viewer is read-only in M5; full
// in-modal markdown editing + the AI-chat lane land in a follow-up.

export function ArtifactReviewModal({
  featureId,
  open,
  onClose,
  pendingReviewPhase,
  defaultTargetPhase,
}: {
  featureId: string | null;
  open: boolean;
  onClose: () => void;
  // pendingReviewPhase is the feature's PendingReviewPhase (or "")
  // — drives auto-selection of the most-relevant artifact.
  pendingReviewPhase?: string;
  // defaultTargetPhase is forwarded as target_phase on the proceed
  // call. Usually the next phase to dispatch (computed at the call
  // site).
  defaultTargetPhase?: string;
}) {
  const qc = useQueryClient();
  const list = useQuery({
    queryKey: ["artifacts", featureId],
    queryFn: ({ signal }) => api.artifactsList(featureId!, signal),
    enabled: !!featureId && open,
    retry: false,
  });

  const items: ArtifactRef[] = list.data?.items ?? [];
  const initialKey = useMemo(() => {
    if (items.length === 0) return null;
    const phaseHit = pendingReviewPhase
      ? items.find((i) => i.phase === pendingReviewPhase || i.key === pendingReviewPhase)
      : undefined;
    return (phaseHit ?? items[0]).key;
  }, [items, pendingReviewPhase]);

  const [selected, setSelected] = useState<string | null>(initialKey);
  useEffect(() => {
    if (initialKey && selected === null) setSelected(initialKey);
  }, [initialKey, selected]);

  const selectedRef = useMemo(
    () => items.find((i) => i.key === selected) ?? null,
    [items, selected],
  );

  // Renderer mode resets to the format-appropriate default whenever
  // the selected artifact changes, so flipping to YAML doesn't leave
  // the viewer trying to markdown-render a config file.
  const [viewerMode, setViewerMode] = useState<ViewerMode>(() =>
    defaultModeFor(selectedRef?.mime),
  );
  useEffect(() => {
    setViewerMode(defaultModeFor(selectedRef?.mime));
  }, [selectedRef?.key, selectedRef?.mime]);

  const canRenderMarkdown =
    !!selectedRef?.mime && selectedRef.mime.startsWith("text/markdown");

  const [comment, setComment] = useState("");

  const content = useQuery({
    queryKey: ["artifact", featureId, selected],
    queryFn: ({ signal }) => api.artifactRead(featureId!, selected!, signal),
    enabled: !!featureId && !!selected && open,
    retry: false,
  });

  const decide = useMutation({
    mutationFn: (decision: ReviewDecisionValue) => {
      // The server infers Roadmap / PhasePlan / TargetPhase from the
      // feature's CurrentRoadmapPhase + Artifacts["roadmap"] +
      // PendingReviewPhase when the client does not route the decision
      // explicitly. The WebUI's unified modal can't tell which review
      // flavour it is (roadmap / phase-plan / gate / legacy plan), so
      // we only forward decision + comment and let the orchestrator
      // resolve the rest — matching the TUI's per-flavour handlers.
      const body: ReviewDecisionRequest = {
        decision,
        comment: comment.trim() || undefined,
        target_phase: defaultTargetPhase,
      };
      return api.reviewDecision(featureId!, body);
    },
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["features"] });
      await qc.invalidateQueries({ queryKey: ["feature", featureId] });
      onClose();
    },
  });

  if (!open || !featureId) return null;

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm"
      role="dialog"
      aria-modal="true"
    >
      <div className="bg-bg-secondary border border-border rounded-lg w-[min(1080px,96vw)] h-[min(85vh,800px)] flex flex-col shadow-lg">
        <header className="flex items-center justify-between px-5 py-3 border-b border-border">
          <div>
            <h2 className="text-sm font-semibold text-text-primary">
              Artifact review · {featureId}
            </h2>
            {pendingReviewPhase && (
              <p className="text-xs text-text-tertiary">
                feature is paused for review of: {pendingReviewPhase}
              </p>
            )}
          </div>
          <button
            type="button"
            onClick={onClose}
            className="text-text-tertiary hover:text-text-primary px-2"
            aria-label="Close review"
          >
            ✕
          </button>
        </header>

        <div className="flex-1 grid grid-cols-[200px_1fr] overflow-hidden">
          <aside className="border-r border-border overflow-auto bg-bg-tertiary">
            {list.isLoading && (
              <p className="p-3 text-xs text-text-tertiary">loading…</p>
            )}
            {list.error && (
              <p
                className="p-3 text-xs"
                style={{ color: "var(--status-error)" }}
              >
                failed to load artifacts
              </p>
            )}
            <ul>
              {items.map((it) => {
                const active = it.key === selected;
                return (
                  <li key={it.key}>
                    <button
                      type="button"
                      onClick={() => setSelected(it.key)}
                      className={`w-full text-left px-3 py-2 text-xs border-l-2 ${
                        active
                          ? "border-l-[var(--accent)] bg-bg-secondary text-text-primary"
                          : "border-l-transparent text-text-secondary hover:bg-bg-secondary"
                      }`}
                    >
                      <div className="flex items-center justify-between gap-2">
                        <span className="font-mono truncate">{it.key}</span>
                        {shortFormat(it.mime) && (
                          <span className="chip chip--slate shrink-0">
                            {shortFormat(it.mime)}
                          </span>
                        )}
                      </div>
                      {it.phase && it.phase !== it.key && (
                        <div className="text-[0.6rem] text-text-tertiary">
                          phase: {it.phase}
                        </div>
                      )}
                      {typeof it.bytes === "number" && it.bytes > 0 && (
                        <div className="text-[0.6rem] text-text-tertiary">
                          {it.bytes}b
                        </div>
                      )}
                    </button>
                  </li>
                );
              })}
              {items.length === 0 && !list.isLoading && !list.error && (
                <li className="p-3 text-xs text-text-tertiary italic">
                  no artifacts
                </li>
              )}
            </ul>
          </aside>

          <section className="flex-1 flex flex-col overflow-hidden">
            {canRenderMarkdown && (
              <div className="flex items-center justify-end gap-1 px-3 py-2 border-b border-border">
                <ViewerToggle
                  mode={viewerMode}
                  onChange={setViewerMode}
                />
              </div>
            )}
            <div className="flex-1 overflow-auto p-3">
              {content.isLoading && (
                <p className="text-xs text-text-tertiary">loading…</p>
              )}
              {content.error && (
                <p
                  className="text-xs"
                  style={{ color: "var(--status-error)" }}
                >
                  {(content.error as Error).message}
                </p>
              )}
              {content.data &&
                (canRenderMarkdown && viewerMode === "rendered" ? (
                  <MarkdownPreview source={content.data} />
                ) : (
                  <pre className="text-xs font-mono text-text-primary whitespace-pre-wrap break-words leading-snug">
                    {content.data}
                  </pre>
                ))}
            </div>
          </section>
        </div>

        <footer className="border-t border-border p-3 space-y-2">
          <textarea
            value={comment}
            onChange={(e) => setComment(e.target.value)}
            placeholder="optional feedback (sent on iterate; ignored on proceed)…"
            rows={2}
            data-persist-key="review.comment"
            className="w-full px-2 py-1 text-sm rounded-sm bg-bg-tertiary border border-border text-text-primary focus:outline-none focus:border-accent font-mono"
          />
          {decide.error && (
            <p className="text-xs" style={{ color: "var(--status-error)" }}>
              {decide.error instanceof ApiError
                ? `${decide.error.status}: ${decide.error.message}`
                : (decide.error as Error).message}
            </p>
          )}
          <div className="flex justify-end gap-2">
            <button
              type="button"
              onClick={() => decide.mutate("iterate")}
              disabled={decide.isPending}
              className="px-3 py-1.5 text-sm rounded-sm border disabled:opacity-50"
              style={{
                borderColor: "var(--banner-warning-border)",
                color: "var(--banner-warning-title)",
              }}
            >
              iterate
            </button>
            <button
              type="button"
              onClick={() => decide.mutate("proceed")}
              disabled={decide.isPending}
              className="px-3 py-1.5 text-sm rounded-sm text-text-inverse disabled:opacity-50"
              style={{ background: "var(--accent)" }}
            >
              {decide.isPending ? "submitting…" : "proceed"}
            </button>
          </div>
        </footer>
      </div>
    </div>
  );
}

// Two-state segmented control: 'rendered' (markdown preview) vs
// 'raw' (monospaced source). Only rendered when the selected artifact
// is markdown — non-markdown artifacts have no preview lens to flip
// to, so the toggle stays hidden to avoid implying one exists.
function ViewerToggle({
  mode,
  onChange,
}: {
  mode: ViewerMode;
  onChange: (m: ViewerMode) => void;
}) {
  const opts: { key: ViewerMode; label: string }[] = [
    { key: "rendered", label: "rendered" },
    { key: "raw", label: "raw" },
  ];
  return (
    <div
      className="inline-flex items-center text-[0.7rem] rounded-sm border border-border overflow-hidden"
      role="radiogroup"
      aria-label="Artifact viewer mode"
    >
      {opts.map((o) => {
        const active = mode === o.key;
        return (
          <button
            key={o.key}
            type="button"
            role="radio"
            aria-checked={active}
            onClick={() => onChange(o.key)}
            className="px-2 py-0.5 transition"
            style={{
              background: active ? "var(--accent)" : "transparent",
              color: active ? "var(--text-inverse)" : "var(--text-secondary)",
            }}
          >
            {o.label}
          </button>
        );
      })}
    </div>
  );
}
