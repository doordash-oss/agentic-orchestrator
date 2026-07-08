import { useState } from "react";
import { KEYBINDINGS, type KeyBinding } from "../keymap";
import { useKeyAction } from "./KeymapProvider";

// KeymapHelpOverlay is the `?` overlay listing every keyboard shortcut
// grouped by section. Auto-mounted at app root by App.tsx.

export function KeymapHelpOverlay() {
  const [open, setOpen] = useState(false);

  useKeyAction("help", () => setOpen((v) => !v));
  useKeyAction("closeTop", () => {
    if (open) setOpen(false);
  });

  if (!open) return null;

  const sections: { label: string; key: KeyBinding["section"] }[] = [
    { label: "Global", key: "global" },
    { label: "Feature list", key: "list" },
    { label: "Feature detail", key: "feature" },
  ];

  return (
    <div
      className="fixed inset-0 z-[60] flex items-center justify-center bg-black/60 backdrop-blur-sm"
      role="dialog"
      aria-modal="true"
      aria-label="Keyboard shortcuts"
    >
      <div className="bg-bg-secondary border border-border rounded-lg w-[min(640px,94vw)] max-h-[80vh] flex flex-col shadow-lg">
        <header className="flex items-center justify-between px-5 py-3 border-b border-border">
          <h2 className="text-sm font-semibold text-text-primary">
            Keyboard shortcuts
          </h2>
          <button
            type="button"
            onClick={() => setOpen(false)}
            className="text-text-tertiary hover:text-text-primary px-2"
            aria-label="Close keyboard help"
          >
            ✕
          </button>
        </header>
        <div className="flex-1 overflow-auto p-5 space-y-4">
          {sections.map((s) => (
            <Section key={s.key} label={s.label} sectionKey={s.key} />
          ))}
          <p className="text-[0.7rem] text-text-tertiary italic">
            Shortcuts are ignored while you're typing in a text field
            (except <Kbd>Esc</Kbd>, which always dismisses the topmost
            modal).
          </p>
        </div>
      </div>
    </div>
  );
}

function Section({
  label,
  sectionKey,
}: {
  label: string;
  sectionKey: KeyBinding["section"];
}) {
  const items = KEYBINDINGS.filter((b) => b.section === sectionKey);
  return (
    <section>
      <h3 className="text-xs font-semibold uppercase tracking-wide text-text-secondary mb-2">
        {label}
      </h3>
      <dl className="grid grid-cols-[80px_1fr] gap-y-1 gap-x-4 text-sm">
        {items.map((b) => (
          <DDRow key={b.action} keyName={b.key} label={b.label} />
        ))}
      </dl>
    </section>
  );
}

function DDRow({ keyName, label }: { keyName: string; label: string }) {
  return (
    <>
      <dt>
        <Kbd>{prettyKey(keyName)}</Kbd>
      </dt>
      <dd className="text-text-secondary">{label}</dd>
    </>
  );
}

function Kbd({ children }: { children: React.ReactNode }) {
  return (
    <kbd className="inline-block px-1.5 py-0.5 text-[0.7rem] font-mono rounded-sm border border-border bg-bg-tertiary text-text-primary">
      {children}
    </kbd>
  );
}

function prettyKey(key: string): string {
  switch (key) {
    case "Escape":
      return "Esc";
    case "Enter":
      return "Enter";
    case " ":
      return "Space";
  }
  return key;
}
