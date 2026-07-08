import { useRef, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api, ApiError } from "../api/client";
import type { RewindResult } from "../api/types";
import { ProgressOverlay, rewindStages } from "./CreationProgress";

// RewindMenu is a two-step modal: pick a target phase -> the
// backend escalates if needed and surfaces warnings; the user
// confirms (or cancels) to actually dispatch via /rewind/proceed.
// RewindToPhase is itself state-mutating, so cancelling between the
// two steps leaves the feature paused at the new review gate; that's
// consistent with the TUI flow.

const REWIND_PHASES = [
  "knowledgebase",
  "inquire",
  "research",
  "brainstorm",
  "plan",
  "implement",
] as const;

type Phase = (typeof REWIND_PHASES)[number];

export function RewindMenu({
  featureId,
  open,
  onClose,
}: {
  featureId: string | null;
  open: boolean;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [target, setTarget] = useState<Phase>("plan");
  const [preview, setPreview] = useState<RewindResult | null>(null);
  // startedAt is updated immediately before each mutation fires so the
  // CreationProgress elapsed clock starts from "now", not from when the
  // modal opened. We use a single ref because only one leg is ever
  // in-flight at a time.
  const startedAtRef = useRef<number>(0);

  const initiate = useMutation({
    mutationFn: () =>
      api.rewind(featureId!, { target_phase: target }),
    onSuccess: (res) => setPreview(res),
  });

  const confirm = useMutation({
    mutationFn: () =>
      api.rewindProceed(featureId!, {
        target_phase: preview?.effective_phase ?? target,
      }),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["features"] });
      await qc.invalidateQueries({ queryKey: ["feature", featureId] });
      setPreview(null);
      onClose();
    },
  });

  // Overlay state: which leg, if any, is in flight. We deliberately
  // skip the overlay during the user-review pause between the two
  // legs — that interval is not "loading", it's "waiting for the
  // human to read the preview".
  const inFlight = initiate.isPending
    ? "preview"
    : confirm.isPending
      ? "dispatch"
      : null;
  const overlayStageIndex = inFlight === "dispatch" ? 1 : 0;
  const overlayHeading =
    inFlight === "dispatch" ? "dispatching rewind…" : "previewing rewind…";
  const overlayError = inFlight
    ? initiate.error || confirm.error
    : null;

  // If the rewind dry-run reported a missing worktree we refuse to
  // dispatch — proceeding would only mutate feature.yaml lifecycle
  // state with no underlying code to move, leaving the feature in an
  // inconsistent half-rewound state. The warnings still render so the
  // user can see why.
  const worktreeMissing =
    (preview?.warnings ?? []).some((w) =>
      /no such file or directory/i.test(w),
    );

  if (!open || !featureId) return null;

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm"
      role="dialog"
      aria-modal="true"
    >
      <div className="relative bg-bg-secondary border border-border rounded-lg w-[min(520px,92vw)] shadow-lg">
        <ProgressOverlay
          active={inFlight !== null}
          heading={overlayHeading}
          stages={rewindStages}
          currentStageIndex={overlayStageIndex}
          startedAt={startedAtRef.current}
          failed={!!overlayError}
          errorLabel={overlayError ? formatErr(overlayError) : undefined}
        />
        <header className="px-5 py-3 border-b border-border">
          <h2 className="text-sm font-semibold text-text-primary">
            Rewind feature
          </h2>
          <p className="text-xs text-text-tertiary mt-1">
            Seals the current run and re-enters a review gate at the
            chosen phase. The orchestrator may escalate to an earlier
            phase if dependencies are stale.
          </p>
        </header>

        <div className="p-5 space-y-3">
          {!preview && (
            <>
              <label className="block text-xs font-semibold uppercase tracking-wide text-text-secondary">
                Target phase
              </label>
              <div className="flex flex-wrap gap-2">
                {REWIND_PHASES.map((p) => (
                  <button
                    key={p}
                    type="button"
                    onClick={() => setTarget(p)}
                    className="px-3 py-1 text-sm rounded-sm border"
                    style={
                      target === p
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
                    {p}
                  </button>
                ))}
              </div>
            </>
          )}

          {preview && (
            <div className="space-y-2">
              <p className="text-sm text-text-primary">
                Effective target:{" "}
                <span className="font-mono text-accent">{preview.effective_phase}</span>
                {preview.effective_phase !== target && (
                  <span className="text-text-tertiary"> (escalated from {target})</span>
                )}
              </p>
              {preview.warnings && preview.warnings.length > 0 && (
                <ul className="text-xs space-y-0.5">
                  {preview.warnings.map((w, i) => (
                    <li key={i} style={{ color: "var(--banner-warning-title)" }}>
                      ⚠ {w}
                    </li>
                  ))}
                </ul>
              )}
              {worktreeMissing ? (
                <p
                  className="text-xs"
                  style={{ color: "var(--banner-warning-title)" }}
                >
                  Worktree directory is missing on disk — rewind cannot
                  proceed safely. Cancel and either recreate the
                  worktree or pick a different action.
                </p>
              ) : (
                <p className="text-xs text-text-tertiary">
                  Confirm to dispatch the target phase, or cancel to leave
                  the feature paused at the review gate.
                </p>
              )}
            </div>
          )}

          {(initiate.error || confirm.error) && (
            <p className="text-xs" style={{ color: "var(--status-error)" }}>
              {formatErr(initiate.error || confirm.error)}
            </p>
          )}
        </div>

        <footer className="border-t border-border px-5 py-3 flex justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            disabled={initiate.isPending || confirm.isPending}
            className="px-3 py-1.5 text-sm border border-border rounded-sm text-text-secondary hover:bg-bg-tertiary disabled:opacity-50"
          >
            cancel
          </button>
          {!preview && (
            <button
              type="button"
              onClick={() => {
                startedAtRef.current = performance.now();
                initiate.mutate();
              }}
              disabled={initiate.isPending}
              className="px-3 py-1.5 text-sm rounded-sm text-text-inverse disabled:opacity-50"
              style={{ background: "var(--accent)" }}
            >
              {initiate.isPending ? "rewinding…" : "rewind"}
            </button>
          )}
          {preview && (
            <button
              type="button"
              onClick={() => {
                startedAtRef.current = performance.now();
                confirm.mutate();
              }}
              disabled={confirm.isPending || worktreeMissing}
              title={
                worktreeMissing
                  ? "Worktree directory is missing — cannot dispatch"
                  : undefined
              }
              className="px-3 py-1.5 text-sm rounded-sm text-text-inverse disabled:opacity-50 disabled:cursor-not-allowed"
              style={{ background: "var(--accent)" }}
            >
              {confirm.isPending ? "dispatching…" : "confirm + dispatch"}
            </button>
          )}
        </footer>
      </div>
    </div>
  );
}

function formatErr(err: unknown): string {
  if (err instanceof ApiError) return `${err.status}: ${err.message}`;
  if (err instanceof Error) return err.message;
  return String(err);
}
