import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api/client";
import type { RecoveryActionValue, RecoveryItem } from "../api/types";

// RecoveryModal is auto-mounted from App. It fetches the recovery
// scan once on mount; if any items come back, it opens a modal that
// lets the user choose Resume / Kill / Skip per item and submit.
//
// Closes itself when:
//   - the scan returns an empty list (nothing to do)
//   - the user dismisses with "Skip all"
//   - the submit mutation completes

export function RecoveryModal() {
  const qc = useQueryClient();
  const [dismissed, setDismissed] = useState(false);

  const scan = useQuery({
    queryKey: ["recovery"],
    queryFn: ({ signal }) => api.recovery(signal),
    // Only ever runs once per app load: if you fix recovery in another
    // tab, refresh.
    refetchOnWindowFocus: false,
    retry: false,
  });

  const items = scan.data?.items ?? [];
  const open = !dismissed && items.length > 0;

  const [actions, setActions] = useState<Record<string, RecoveryActionValue>>({});

  // Initialise each item to "skip" the first time the list is seen.
  useMemo(() => {
    if (!open) return;
    setActions((cur) => {
      if (Object.keys(cur).length > 0) return cur;
      const next: Record<string, RecoveryActionValue> = {};
      for (const it of items) next[it.key] = "skip";
      return next;
    });
  }, [open, items]);

  const submit = useMutation({
    mutationFn: () => api.executeRecovery(scan.data!.snapshot_id, actions),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["features"] });
      setDismissed(true);
    },
  });

  if (!open) return null;

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm"
      role="dialog"
      aria-modal="true"
    >
      <div className="bg-bg-secondary border border-border rounded-lg w-[min(720px,92vw)] max-h-[88vh] flex flex-col shadow-lg">
        <header className="px-5 py-3 border-b border-border">
          <h2 className="text-sm font-semibold text-text-primary">
            Recover unfinished sessions
          </h2>
          <p className="text-xs text-text-tertiary mt-1">
            {items.length} session{items.length === 1 ? "" : "s"} from a previous
            run weren't closed cleanly. Pick what to do for each.
          </p>
        </header>

        <div className="flex-1 overflow-auto px-5 py-3 space-y-2">
          {items.map((it) => (
            <Row
              key={it.key}
              item={it}
              action={actions[it.key] ?? "skip"}
              onChange={(v) =>
                setActions((cur) => ({ ...cur, [it.key]: v }))
              }
            />
          ))}
        </div>

        <footer className="flex items-center justify-between px-5 py-3 border-t border-border gap-3">
          <div className="text-xs">
            {submit.error && (
              <span style={{ color: "var(--status-error)" }}>
                {(submit.error as Error).message}
              </span>
            )}
          </div>
          <div className="flex gap-2">
            <button
              type="button"
              onClick={() => setDismissed(true)}
              className="px-3 py-1.5 text-sm border border-border rounded-sm text-text-secondary hover:bg-bg-tertiary"
              disabled={submit.isPending}
            >
              dismiss
            </button>
            <button
              type="button"
              onClick={() => submit.mutate()}
              disabled={submit.isPending}
              className="px-3 py-1.5 text-sm rounded-sm text-text-inverse disabled:opacity-50"
              style={{ background: "var(--accent)" }}
            >
              {submit.isPending ? "applying…" : `apply (${items.length})`}
            </button>
          </div>
        </footer>
      </div>
    </div>
  );
}

function Row({
  item,
  action,
  onChange,
}: {
  item: RecoveryItem;
  action: RecoveryActionValue;
  onChange: (v: RecoveryActionValue) => void;
}) {
  const label =
    item.feature_name || item.feature_slug || item.feature_id;
  return (
    <div className="grid grid-cols-[1fr_auto] gap-3 p-2 rounded-sm bg-bg-tertiary border border-border">
      <div className="min-w-0">
        <div className="text-sm text-text-primary truncate">{label}</div>
        <div className="text-[0.7rem] text-text-tertiary">
          {[
            item.repo_name && `repo: ${item.repo_name}`,
            item.phase && `phase: ${item.phase}`,
            typeof item.iteration === "number" && `iter: ${item.iteration}`,
            typeof item.stale_seconds === "number" &&
              `stale: ${formatStale(item.stale_seconds)}`,
            item.process_alive ? "process: alive" : "process: dead",
          ]
            .filter(Boolean)
            .join(" · ")}
        </div>
      </div>
      <fieldset className="flex gap-1 text-xs">
        {(["resume", "kill", "skip"] as const).map((v) => (
          <label
            key={v}
            className="px-2 py-1 rounded-sm border cursor-pointer"
            style={
              action === v
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
            <input
              type="radio"
              className="sr-only"
              name={`act:${item.key}`}
              value={v}
              checked={action === v}
              onChange={() => onChange(v)}
            />
            {v}
          </label>
        ))}
      </fieldset>
    </div>
  );
}

function formatStale(seconds: number): string {
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  return `${Math.floor(seconds / 3600)}h`;
}
