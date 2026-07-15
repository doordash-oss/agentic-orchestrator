import { useEffect, useRef } from 'react';

export interface ConsentDialogProps {
  /** Absolute folder the server would initialize. */
  path: string;
  busy: boolean;
  onConfirm(): void;
  onCancel(): void;
}

/**
 * Explicit consequence + consent step for server-owned `git init`. Cancel
 * always keeps the chosen folder editable in the repository step behind it.
 */
export function ConsentDialog({ path, busy, onConfirm, onCancel }: ConsentDialogProps) {
  const cancelRef = useRef<HTMLButtonElement | null>(null);

  // Focus lands on the safe action first; Escape cancels.
  useEffect(() => {
    cancelRef.current?.focus();
  }, []);

  return (
    <div className="consent-dialog__scrim">
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="consent-dialog-title"
        aria-describedby="consent-dialog-consequence"
        className="consent-dialog"
        onKeyDown={(event) => {
          if (event.key === 'Escape') {
            event.stopPropagation();
            onCancel();
          }
        }}
      >
        <h2 id="consent-dialog-title" className="consent-dialog__title">
          Initialize a new repository?
        </h2>
        <div id="consent-dialog-consequence" className="consent-dialog__body">
          <p>This folder is not a git repository yet:</p>
          <code className="consent-dialog__path">{path}</code>
          <p>
            The folder must be empty. If you continue, the Agentico runtime initializes a git
            repository there — the equivalent of running <code>git init</code> in that folder — and
            creates an initial empty commit so the repository is ready to use.
          </p>
          <p>
            If you cancel, nothing is created and you can pick a different folder or an existing
            repository instead.
          </p>
        </div>
        <div className="consent-dialog__actions">
          <button type="button" ref={cancelRef} onClick={onCancel} disabled={busy}>
            Cancel
          </button>
          <button
            type="button"
            className="consent-dialog__confirm"
            onClick={onConfirm}
            disabled={busy}
          >
            {busy ? 'Initializing…' : 'Initialize repository'}
          </button>
        </div>
      </div>
    </div>
  );
}
