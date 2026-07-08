import { Spinner } from "./Spinner";

// Destructive-action gate for feature deletion. Surfaces:
//   - the feature's name (so the user can confirm they're targeting
//     the right record)
//   - a warning when the feature still has running sessions (delete
//     stops them as part of the same operation)
//   - the in-flight spinner inline in the confirm button so the modal
//     stays put until the orchestrator finishes the cleanup
//
// Deliberately small: a typed-confirmation field would be over-the-top
// for an orchestrator dashboard where features are routinely created,
// trialled, and discarded. The hover-revealed icon in the list row
// + this two-button confirm matches the pattern used by Linear, Vercel,
// and GitHub for repo-level destructive actions.

export function ConfirmDeleteModal({
  featureName,
  open,
  isRunning,
  pending,
  error,
  onCancel,
  onConfirm,
}: {
  featureName: string;
  open: boolean;
  isRunning: boolean;
  pending: boolean;
  error: string | null;
  onCancel: () => void;
  onConfirm: () => void;
  /** performance.now() when the delete request fired; reserved for a
   *  future ProgressOverlay variant. Unused today. */
  startedAt?: number;
}) {
  if (!open) return null;
  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm"
      role="dialog"
      aria-modal="true"
      aria-labelledby="confirm-delete-title"
    >
      <div className="relative bg-bg-secondary border border-border rounded-lg w-[min(440px,92vw)] shadow-lg">
        <header className="px-5 py-3 border-b border-border">
          <h2
            id="confirm-delete-title"
            className="text-sm font-semibold"
            style={{ color: "var(--status-error)" }}
          >
            Delete feature?
          </h2>
        </header>
        <div className="px-5 py-4 space-y-3 text-sm">
          <p className="text-text-primary">
            You're about to delete{" "}
            <span className="font-semibold">{featureName}</span>. This removes
            the feature record and its on-disk state.
          </p>
          {isRunning && (
            <div
              className="banner banner--warning"
              role="note"
            >
              <span className="banner-icon" aria-hidden>
                !
              </span>
              <div>
                <div className="banner-title">Feature is running</div>
                <div className="banner-body">
                  Active sessions will be stopped as part of the delete.
                </div>
              </div>
            </div>
          )}
          <p className="text-xs text-text-tertiary">
            This action can't be undone. The worktree on disk is left intact —
            clean it up separately if you no longer need it.
          </p>
          {error && (
            <p className="text-xs" style={{ color: "var(--status-error)" }}>
              {error}
            </p>
          )}
        </div>
        <footer className="flex justify-end gap-2 px-5 py-3 border-t border-border">
          <button
            type="button"
            onClick={onCancel}
            disabled={pending}
            className="px-3 py-1.5 text-sm border border-border rounded-sm text-text-secondary hover:bg-bg-tertiary disabled:opacity-50"
          >
            cancel
          </button>
          <button
            type="button"
            onClick={onConfirm}
            disabled={pending}
            className="px-3 py-1.5 text-sm rounded-sm text-text-inverse disabled:opacity-60 inline-flex items-center gap-2"
            style={{ background: "var(--status-error)" }}
            aria-busy={pending}
            autoFocus
          >
            {pending ? (
              <>
                <Spinner size="xs" ariaLabel="deleting" />
                <span>deleting…</span>
              </>
            ) : (
              "delete"
            )}
          </button>
        </footer>
      </div>
    </div>
  );
}
