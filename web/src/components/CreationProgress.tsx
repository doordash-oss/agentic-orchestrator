import { useEffect, useRef, useState, type ReactNode } from "react";
import { Spinner, useDelayedFlag } from "./Spinner";

// Multi-stage progress display. Composes the existing Spinner with
// honest staged progress, per-stage status chips, and a rotating
// activity line. Visual cue borrowed from the nba-agent LWC loading
// screen (force-app/.../lwc/nbaAgent/nbaAgent.css) — minus the
// basketball.
//
// Designed to be reusable. Callers describe the work as a list of
// stages and advance `currentStageIndex` as each stage starts. The
// component owns purely visual concerns (drift, message cycling,
// elapsed clock) so consumers stay simple.

export interface Stage {
  /** Stable id; used as a React key. */
  key: string;
  /** Short label shown in the chip ("Save", "Start", "Ready"). */
  label: string;
  /**
   * Status messages cycled in the activity line while this stage is
   * active. One emoji + verb each — keep them short, they cycle at
   * 2-second intervals. Falls back to the label when empty.
   */
  messages?: string[];
}

export type StageState = "pending" | "active" | "done";

export function CreationProgress({
  stages,
  currentStageIndex,
  heading = "working…",
  startedAt,
  failed = false,
  errorLabel,
}: {
  stages: Stage[];
  /** -1 before any stage runs; equals stages.length once everything is done. */
  currentStageIndex: number;
  heading?: string;
  /** performance.now() timestamp when the overall operation started. */
  startedAt: number;
  failed?: boolean;
  errorLabel?: string;
}) {
  const total = stages.length;
  const clampedIdx = Math.max(0, Math.min(total, currentStageIndex));
  const done = currentStageIndex >= total;

  const progress = useDriftingProgress(clampedIdx, total, done);
  const elapsedMs = useElapsedClock(startedAt, done || failed);
  const messageIdx = useRotatingIndex(
    stages[clampedIdx]?.messages?.length ?? 0,
    clampedIdx,
  );

  const activeMessages = stages[clampedIdx]?.messages ?? [];
  const activityLine = activeMessages[messageIdx] ?? stages[clampedIdx]?.label ?? "";

  return (
    <div className="flex flex-col items-center gap-4 px-6 py-5 max-w-md mx-auto">
      <Spinner size="lg" ariaLabel={heading} />

      <div className="text-center">
        <div className="text-sm font-medium text-text-primary">{heading}</div>
        <div className="text-xs text-text-tertiary mt-0.5 tabular-nums">
          {failed
            ? errorLabel ?? "something went wrong"
            : `${formatElapsed(elapsedMs)} elapsed`}
        </div>
      </div>

      <ProgressTrack pct={progress} failed={failed} />

      <StageChipRow
        stages={stages}
        currentStageIndex={clampedIdx}
        done={done}
        failed={failed}
      />

      {!failed && !done && (
        <div className="text-xs text-text-secondary text-center min-h-[1.25rem] transition-opacity duration-200">
          {activityLine}
        </div>
      )}
      {done && !failed && (
        <div className="text-xs text-text-secondary text-center">
          ✨ ready
        </div>
      )}
    </div>
  );
}

function ProgressTrack({ pct, failed }: { pct: number; failed: boolean }) {
  return (
    <div className="w-full h-1.5 rounded-full overflow-hidden" style={{ background: "var(--bg-tertiary)" }}>
      <div
        className="h-full rounded-full transition-[width] duration-200 ease-out"
        style={{
          width: `${Math.round(pct * 100)}%`,
          background: failed
            ? "var(--status-error)"
            : "linear-gradient(90deg, color-mix(in srgb, var(--accent) 70%, transparent), var(--accent))",
          boxShadow: failed ? "none" : "0 0 8px var(--accent-glow)",
        }}
      />
    </div>
  );
}

// Public: just the chip row. Use inline (without the surrounding
// spinner/progress-bar/activity-line) when an existing dialog already
// drives the flow via its own buttons but you want a visual stage
// indicator strip (e.g. TweakDialog's start → commit → finish).
export function StageChipRow({
  stages,
  currentStageIndex,
  done = false,
  failed = false,
}: {
  stages: Stage[];
  currentStageIndex: number;
  done?: boolean;
  failed?: boolean;
}) {
  const clamped = Math.max(0, Math.min(stages.length, currentStageIndex));
  const isDone = done || currentStageIndex >= stages.length;
  return (
    <div className="flex items-center justify-center gap-4 w-full">
      {stages.map((s, i) => (
        <StageChip
          key={s.key}
          stage={s}
          state={stageStateFor(i, clamped, isDone, failed)}
        />
      ))}
    </div>
  );
}

// Public: full overlay (absolute-positioned backdrop + CreationProgress)
// wrapped with useDelayedFlag(active, delayMs) so fast operations never
// flash. The host modal must be `position: relative` for the overlay to
// align correctly.
export function ProgressOverlay({
  active,
  delayMs = 300,
  ...progressProps
}: {
  active: boolean;
  delayMs?: number;
  stages: Stage[];
  currentStageIndex: number;
  heading?: string;
  startedAt: number;
  failed?: boolean;
  errorLabel?: string;
}) {
  const visible = useDelayedFlag(active, delayMs);
  if (!visible) return null;
  return (
    <div
      className="absolute inset-0 z-10 flex items-center justify-center backdrop-blur-[1px]"
      style={{ background: "color-mix(in srgb, var(--bg-secondary) 80%, transparent)" }}
      role="status"
      aria-live="polite"
    >
      <CreationProgress {...progressProps} />
    </div>
  );
}

function StageChip({ stage, state }: { stage: Stage; state: StageState }) {
  const isActive = state === "active";
  const isDone = state === "done";

  const ringColor =
    isDone || isActive ? "var(--accent)" : "color-mix(in srgb, var(--accent) 18%, transparent)";
  const labelColor = isDone || isActive ? "var(--text-primary)" : "var(--text-tertiary)";

  return (
    <div className="flex flex-col items-center gap-1.5 min-w-[3.5rem]">
      <div
        className="relative flex items-center justify-center w-9 h-9 rounded-full border-2"
        style={{ borderColor: ringColor }}
        aria-label={`${stage.label} ${state}`}
      >
        {isActive && <Spinner size="xs" ariaLabel="" />}
        {isDone && <Check />}
        {state === "pending" && <Dot />}
      </div>
      <span
        className="text-[0.65rem] uppercase tracking-wider font-semibold"
        style={{ color: labelColor }}
      >
        {stage.label}
      </span>
    </div>
  );
}

function Check(): ReactNode {
  return (
    <svg
      width="14"
      height="14"
      viewBox="0 0 14 14"
      fill="none"
      aria-hidden
      style={{ color: "var(--accent)" }}
    >
      <path
        d="M2 7.5 5.5 11 12 4"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

function Dot(): ReactNode {
  return (
    <span
      className="block w-1.5 h-1.5 rounded-full"
      style={{ background: "var(--text-tertiary)" }}
      aria-hidden
    />
  );
}

function stageStateFor(
  i: number,
  currentIdx: number,
  done: boolean,
  failed: boolean,
): StageState {
  if (failed && i === currentIdx) return "active";
  if (done || i < currentIdx) return "done";
  if (i === currentIdx) return "active";
  return "pending";
}

// Drives the progress bar. Anchors the bar at i/total when stage i
// starts, then drifts asymptotically toward (i+1)/total with a 4-second
// time constant so the bar never sits perfectly still. When the whole
// op finishes we snap to 100%.
function useDriftingProgress(currentIdx: number, total: number, done: boolean): number {
  const [progress, setProgress] = useState(() => (total === 0 ? 0 : currentIdx / total));
  const rafRef = useRef<number | null>(null);

  useEffect(() => {
    if (rafRef.current !== null) cancelAnimationFrame(rafRef.current);
    if (total === 0) {
      setProgress(done ? 1 : 0);
      return;
    }
    if (done) {
      setProgress(1);
      return;
    }
    const anchor = currentIdx / total;
    const target = (currentIdx + 1) / total;
    setProgress((p) => (p < anchor ? anchor : p));
    const start = performance.now();
    const tau = 4_000; // 4-second time constant: approaches but never reaches target
    const tick = (now: number) => {
      const t = now - start;
      const next = anchor + (target - anchor) * (1 - Math.exp(-t / tau));
      setProgress(next);
      rafRef.current = requestAnimationFrame(tick);
    };
    rafRef.current = requestAnimationFrame(tick);
    return () => {
      if (rafRef.current !== null) cancelAnimationFrame(rafRef.current);
    };
  }, [currentIdx, total, done]);

  return progress;
}

// Re-renders the elapsed clock 4× a second while active.
function useElapsedClock(startedAt: number, stopped: boolean): number {
  const [elapsed, setElapsed] = useState(() => performance.now() - startedAt);
  useEffect(() => {
    if (stopped) return;
    const id = window.setInterval(() => {
      setElapsed(performance.now() - startedAt);
    }, 250);
    return () => window.clearInterval(id);
  }, [startedAt, stopped]);
  return elapsed;
}

// Cycles 0..len-1 every 2 seconds, resets when len or stageIdx changes.
function useRotatingIndex(len: number, resetKey: unknown): number {
  const [idx, setIdx] = useState(0);
  useEffect(() => {
    setIdx(0);
    if (len <= 1) return;
    const id = window.setInterval(() => {
      setIdx((i) => (i + 1) % len);
    }, 2_000);
    return () => window.clearInterval(id);
  }, [len, resetKey]);
  return idx;
}

function formatElapsed(ms: number): string {
  const secs = Math.max(0, ms / 1000);
  if (secs < 60) return `${secs.toFixed(secs < 10 ? 1 : 0)}s`;
  const m = Math.floor(secs / 60);
  const s = Math.floor(secs % 60);
  return `${m}m ${s.toString().padStart(2, "0")}s`;
}

// Convenience: returns the stages array used by the create-feature
// flow. Lives here so other consumers (Publish, Rebase) can define
// their own stage list with the same shape.
export function createFeatureStages(autoStart: boolean): Stage[] {
  const save: Stage = {
    key: "save",
    label: "Save",
    messages: [
      "📝 validating draft…",
      "💾 saving feature…",
      "🌲 creating worktree…",
    ],
  };
  const start: Stage = {
    key: "start",
    label: "Start",
    messages: [
      "🚀 starting orchestrator…",
      "🛰️ subscribing to events…",
      "🧠 loading agent persona…",
    ],
  };
  const ready: Stage = {
    key: "ready",
    label: "Ready",
    messages: ["🎉 wrapping up…"],
  };
  return autoStart ? [save, start, ready] : [save, ready];
}

// Maps a creation-flow phase to its index in createFeatureStages. Kept
// here so callers don't have to remember the ordering.
export type CreateFeaturePhase = "idle" | "save" | "start" | "ready" | "error";

export function stageIndexFor(
  phase: CreateFeaturePhase,
  autoStart: boolean,
): number {
  const stages = createFeatureStages(autoStart).map((s) => s.key);
  if (phase === "idle") return 0;
  if (phase === "ready") return stages.length;
  if (phase === "error") return Math.max(0, stages.indexOf("save"));
  return Math.max(0, stages.indexOf(phase));
}

// Stage definitions for the other feature-lifecycle modals. Each flow
// gets its own message palette so the rotating activity line stays
// honest about what's happening.

export const publishStages: Stage[] = [
  {
    key: "dispatch",
    label: "Dispatch",
    messages: [
      "📡 dispatching publish across repos…",
      "🔐 authenticating with GitHub…",
      "🚀 server is creating PRs…",
    ],
  },
];

export const refactorStages: Stage[] = [
  {
    key: "dispatch",
    label: "Dispatch",
    messages: [
      "🛠️ kicking off refactor…",
      "🧠 loading prompt context…",
      "🌲 walking worktrees…",
    ],
  },
];

export const rebaseStages: Stage[] = [
  {
    key: "rebase",
    label: "Rebase",
    messages: [
      "🔄 rebasing onto base branch…",
      "🪢 replaying commits…",
      "🔍 checking for conflicts…",
    ],
  },
];

export const forcePushStages: Stage[] = [
  {
    key: "force",
    label: "Force-push",
    messages: [
      "💪 force-pushing rebased branch…",
      "🔐 authenticating with GitHub…",
    ],
  },
];

// Tweak is button-driven, so we expose its stages without messages —
// only the StageChipRow is used to indicate progress.
export const tweakStages: Stage[] = [
  { key: "start", label: "Start" },
  { key: "commit", label: "Commit" },
  { key: "finish", label: "Finish" },
];

// Rewind is a two-leg flow:
//   1. Preview — dry-runs the rewind so the orchestrator can escalate
//      to an earlier phase if dependencies are stale and report
//      worktree warnings.
//   2. Dispatch — actually applies the rewind and re-enters the target
//      phase at its review gate.
// Between the two legs the user reviews the preview; that pause is
// user-driven and should NOT show the overlay (we gate on the
// in-flight mutations, not on stage index).
export const rewindStages: Stage[] = [
  {
    key: "preview",
    label: "Preview",
    messages: [
      "🔍 sealing the current run…",
      "🧭 resolving target phase…",
      "🌲 checking the worktree…",
    ],
  },
  {
    key: "dispatch",
    label: "Dispatch",
    messages: [
      "↩️ rewinding lifecycle…",
      "🚪 re-entering review gate…",
      "🚀 starting the target phase…",
    ],
  },
];
