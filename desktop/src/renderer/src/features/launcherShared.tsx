export function LauncherFooter({
  onCancel,
  primaryLabel,
  primaryDisabled = false,
  busy = false,
  onPrimary,
}: {
  onCancel(): void;
  primaryLabel: string;
  primaryDisabled?: boolean;
  busy?: boolean;
  onPrimary(): void;
}): React.ReactElement {
  return (
    <footer className="launcher-modal__footer">
      <button type="button" className="launcher-modal__cancel" onClick={onCancel} disabled={busy}>
        Cancel
      </button>
      <button
        type="button"
        className="launcher-modal__primary"
        disabled={primaryDisabled || busy}
        onClick={onPrimary}
      >
        {primaryLabel}
      </button>
    </footer>
  );
}
