import { useEffect, useState } from "react";

// Visual loading affordances. The Spinner itself is purely presentational;
// callers gate it via useDelayedFlag(active, 300) so it only appears when a
// screen action runs longer than ~300 ms — matching the "feels instant"
// threshold from Nielsen's response-time research. Anything faster never
// reveals the spinner, so quick interactions stay calm.
//
// Visual language borrowed from the nba-agent LWC loading screen
// (force-app/.../lwc/nbaAgent/nbaAgent.css): an outer ring that rotates and
// an inner core that pulses, rendered in the app's mint accent. Honours
// prefers-reduced-motion via the global media query in index.css.

export type SpinnerSize = "xs" | "sm" | "md" | "lg";

const SIZE_PX: Record<SpinnerSize, number> = {
  xs: 12,
  sm: 16,
  md: 28,
  lg: 56,
};

export function Spinner({
  size = "sm",
  label,
  ariaLabel,
}: {
  size?: SpinnerSize;
  label?: string;
  ariaLabel?: string;
}) {
  const px = SIZE_PX[size];
  return (
    <span
      className="inline-flex items-center gap-2"
      role="status"
      aria-live="polite"
      aria-label={ariaLabel ?? label ?? "loading"}
    >
      <span
        aria-hidden
        className="spinner-ring"
        style={{ width: px, height: px }}
      >
        <span className="spinner-ring__core" />
      </span>
      {label ? <span className="text-xs">{label}</span> : null}
    </span>
  );
}

// useDelayedFlag returns true only once `active` has stayed true for at
// least `delayMs`. Resets to false as soon as `active` flips back. Used to
// suppress brief flickers — a 50 ms create call should never paint a
// spinner only to immediately unmount it.
export function useDelayedFlag(active: boolean, delayMs = 300): boolean {
  const [delayed, setDelayed] = useState(false);
  useEffect(() => {
    if (!active) {
      setDelayed(false);
      return;
    }
    const t = window.setTimeout(() => setDelayed(true), delayMs);
    return () => window.clearTimeout(t);
  }, [active, delayMs]);
  return delayed;
}

// Full-modal overlay used while a long create/start/save is in flight.
// Sits inside the modal body (absolutely positioned) so the form keeps its
// shape but is visibly inert. Caller gates with useDelayedFlag(submitting).
export function BusyOverlay({
  label = "working…",
}: {
  label?: string;
}) {
  return (
    <div
      className="absolute inset-0 z-10 flex flex-col items-center justify-center gap-3 backdrop-blur-[1px]"
      style={{ background: "color-mix(in srgb, var(--bg-secondary) 70%, transparent)" }}
      role="status"
      aria-live="polite"
    >
      <Spinner size="lg" ariaLabel={label} />
      <span className="text-xs text-text-secondary">{label}</span>
    </div>
  );
}
