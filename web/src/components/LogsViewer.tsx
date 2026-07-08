import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api, ApiError } from "../api/client";

// LogsViewer fetches the per-feature log index first so the phase +
// iteration pickers only show options that actually exist on disk.
// Defaulting to phase=implement / iter=1 (the old behaviour) tripped
// 404s on every feature whose plan phase wasn't done yet; with the
// index in hand we pick the latest iteration of the latest written
// phase by default and surface a real empty state when nothing has
// been written yet.

export function LogsViewer({
  featureId,
  open,
  onClose,
}: {
  featureId: string | null;
  open: boolean;
  onClose: () => void;
}) {
  const index = useQuery({
    queryKey: ["logs-index", featureId],
    queryFn: ({ signal }) => api.logsIndex(featureId!, signal),
    enabled: !!featureId && open,
    retry: false,
  });

  const entries = index.data?.entries ?? [];

  // Default selection: pick the LAST phase + LAST iteration that has
  // a log. That's almost always the most-relevant view after a phase
  // transition. Users can change via the pickers.
  const initialPhase = useMemo(
    () => (entries.length > 0 ? entries[entries.length - 1].phase : ""),
    [entries],
  );
  const [phase, setPhase] = useState("");
  const [iter, setIter] = useState(0);

  useEffect(() => {
    if (phase === "" && initialPhase) {
      const entry = entries.find((e) => e.phase === initialPhase);
      const iters = entry?.iterations ?? [];
      const last = iters.length > 0 ? iters[iters.length - 1] : 1;
      setPhase(initialPhase);
      setIter(last);
    }
  }, [initialPhase, entries, phase]);

  const iters = entries.find((e) => e.phase === phase)?.iterations ?? [];

  const logQuery = useQuery({
    queryKey: ["logs", featureId, phase, iter],
    queryFn: ({ signal }) => api.logs(featureId!, phase, iter, signal),
    enabled: !!featureId && open && phase !== "" && iter > 0,
    retry: false,
  });

  if (!open || !featureId) return null;

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm"
      role="dialog"
      aria-modal="true"
    >
      <div className="bg-bg-secondary border border-border rounded-lg w-[min(960px,94vw)] max-h-[90vh] flex flex-col shadow-lg">
        <header className="flex items-center justify-between px-5 py-3 border-b border-border gap-3">
          <h2 className="text-sm font-semibold text-text-primary">
            Logs · {featureId}
          </h2>
          <div className="flex items-center gap-2 text-xs">
            <label className="flex items-center gap-1">
              <span className="text-text-tertiary">phase</span>
              <select
                value={phase}
                onChange={(e) => {
                  const next = e.target.value;
                  setPhase(next);
                  const entry = entries.find((x) => x.phase === next);
                  const list = entry?.iterations ?? [];
                  setIter(list.length > 0 ? list[list.length - 1] : 1);
                }}
                disabled={entries.length === 0}
                className="px-2 py-1 rounded-sm bg-bg-tertiary border border-border text-text-primary focus:outline-none focus:border-accent disabled:opacity-50"
              >
                {entries.length === 0 && <option value="">—</option>}
                {entries.map((e) => (
                  <option key={e.phase} value={e.phase}>
                    {e.phase}
                  </option>
                ))}
              </select>
            </label>
            <label className="flex items-center gap-1">
              <span className="text-text-tertiary">iter</span>
              <select
                value={iter}
                onChange={(e) => setIter(Number(e.target.value))}
                disabled={iters.length === 0}
                className="px-2 py-1 rounded-sm bg-bg-tertiary border border-border text-text-primary focus:outline-none focus:border-accent disabled:opacity-50"
              >
                {iters.length === 0 && <option value={0}>—</option>}
                {iters.map((n) => (
                  <option key={n} value={n}>
                    {n}
                  </option>
                ))}
              </select>
            </label>
            <button
              type="button"
              onClick={onClose}
              className="text-text-tertiary hover:text-text-primary px-2"
              aria-label="Close logs"
            >
              ✕
            </button>
          </div>
        </header>
        <div className="flex-1 overflow-auto p-3">
          <Body
            indexLoading={index.isLoading}
            indexError={index.error}
            entries={entries}
            phase={phase}
            iter={iter}
            logLoading={logQuery.isLoading}
            logError={logQuery.error}
            logData={logQuery.data}
          />
        </div>
      </div>
    </div>
  );
}

function Body({
  indexLoading,
  indexError,
  entries,
  phase,
  iter,
  logLoading,
  logError,
  logData,
}: {
  indexLoading: boolean;
  indexError: unknown;
  entries: { phase: string; iterations: number[] }[];
  phase: string;
  iter: number;
  logLoading: boolean;
  logError: unknown;
  logData: string | undefined;
}) {
  if (indexLoading) {
    return <p className="text-text-tertiary text-sm">loading…</p>;
  }
  if (indexError) {
    return (
      <p className="text-sm" style={{ color: "var(--status-error)" }}>
        {formatErr(indexError)}
      </p>
    );
  }
  if (entries.length === 0) {
    return (
      <p className="text-text-tertiary text-sm italic">
        No logs written yet. The feature hasn't completed any iterations,
        or its active run was reset by a rewind.
      </p>
    );
  }
  if (phase === "" || iter === 0) {
    return (
      <p className="text-text-tertiary text-sm italic">
        Pick a phase and iteration above.
      </p>
    );
  }
  if (logLoading) {
    return <p className="text-text-tertiary text-sm">loading…</p>;
  }
  if (logError) {
    return (
      <p className="text-sm" style={{ color: "var(--status-error)" }}>
        {formatErr(logError)}
      </p>
    );
  }
  if (logData !== undefined) {
    return (
      <pre className="text-xs font-mono text-text-primary whitespace-pre-wrap break-all leading-snug">
        {logData || "(empty log)"}
      </pre>
    );
  }
  return null;
}

function formatErr(err: unknown): string {
  if (err instanceof ApiError) return `${err.status}: ${err.message}`;
  if (err instanceof Error) return err.message;
  return String(err);
}
