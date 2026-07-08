import { useEffect, type ReactNode } from "react";
import { BINDINGS_BY_KEY, type KeyAction } from "../keymap";
import { useKeymapStore } from "../store/keymap";

// KeymapProvider listens at the document level and dispatches each
// keydown event against the global registry. Mounted once near the
// app root. Individual components opt into specific actions via
// useKeyAction(action, handler) below.
//
// Key handling rules:
//   - Ignore events with metaKey/ctrlKey/altKey set EXCEPT for the
//     ones we explicitly bind with a modifier (none today).
//   - Ignore events when the user is typing in a text input or
//     textarea — otherwise n/p/l would steal letters from the
//     description textarea, etc.
//   - Escape always fires (even in inputs) so users can dismiss
//     modals quickly from anywhere.

export function KeymapProvider({ children }: { children: ReactNode }) {
  const invoke = useKeymapStore((s) => s.invoke);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      // Always allow Escape to dismiss; deny modifiers everywhere
      // else to keep things predictable.
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      if (e.key !== "Escape" && isTypingInForm(e.target)) return;

      const binding = BINDINGS_BY_KEY[e.key];
      if (!binding) return;
      const handled = invoke(binding.action);
      if (handled) {
        e.preventDefault();
        e.stopPropagation();
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [invoke]);

  return <>{children}</>;
}

// useKeyAction registers a handler for an action while the component
// is mounted. Multiple components can register the same action; the
// LAST registration wins (so modals on top capture keys correctly).
export function useKeyAction(action: KeyAction, handler: () => void) {
  const register = useKeymapStore((s) => s.register);
  useEffect(() => register(action, handler), [register, action, handler]);
}

function isTypingInForm(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  const tag = target.tagName.toLowerCase();
  if (tag === "input" || tag === "textarea" || tag === "select") return true;
  if (target.isContentEditable) return true;
  return false;
}
