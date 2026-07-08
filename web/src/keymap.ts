// Shared keymap table. Used by both the global keydown dispatcher
// (web/src/components/KeymapProvider.tsx) and the help overlay
// (web/src/components/KeymapHelpOverlay.tsx) so a single source of
// truth describes which key does what.
//
// Bindings mirror the TUI's keys.go where the action makes sense in
// a browser: 'n' new feature, 'l' logs, 'g' review comments, 'p'
// publish, 'r' artifact review, 'b' rewind, '/' focus search, '?'
// help overlay, 'esc' close topmost modal, j/k navigate list.

export type KeyAction =
  | "help"
  | "newFeature"
  | "focusFilter"
  | "closeTop"
  | "navUp"
  | "navDown"
  | "openSelected"
  | "logs"
  | "reviewComments"
  | "artifactReview"
  | "rewind"
  | "publish"
  | "stopFeature"
  | "deleteFeature";

export interface KeyBinding {
  /** Action name dispatched to handlers. */
  action: KeyAction;
  /** Key the listener compares against `event.key`. */
  key: string;
  /** Whether Ctrl/Cmd is required. */
  modifier?: "ctrl" | "shift" | "alt" | "none";
  /** Human-readable label shown in the help overlay. */
  label: string;
  /** Section grouping in the help overlay. */
  section: "global" | "list" | "feature";
}

export const KEYBINDINGS: KeyBinding[] = [
  { action: "help", key: "?", label: "Open keyboard help", section: "global" },
  { action: "newFeature", key: "n", label: "New feature wizard", section: "global" },
  { action: "focusFilter", key: "/", label: "Focus feature filter", section: "global" },
  { action: "closeTop", key: "Escape", label: "Close topmost modal", section: "global" },

  { action: "navUp", key: "k", label: "Move selection up", section: "list" },
  { action: "navDown", key: "j", label: "Move selection down", section: "list" },
  { action: "openSelected", key: "Enter", label: "Open detail (focus right pane)", section: "list" },

  { action: "logs", key: "l", label: "Logs", section: "feature" },
  { action: "reviewComments", key: "g", label: "GitHub review comments", section: "feature" },
  { action: "artifactReview", key: "r", label: "Artifact review", section: "feature" },
  { action: "rewind", key: "b", label: "Rewind menu", section: "feature" },
  { action: "publish", key: "p", label: "Publish wizard", section: "feature" },
  { action: "stopFeature", key: "s", label: "Stop running sessions", section: "feature" },
  { action: "deleteFeature", key: "d", label: "Delete feature", section: "feature" },
];

/** Map by key for fast dispatch in the listener. Doesn't include
 *  modifier handling — the dispatcher checks event.metaKey etc.
 *  before consulting this map. */
export const BINDINGS_BY_KEY: Record<string, KeyBinding> = Object.fromEntries(
  KEYBINDINGS.map((b) => [b.key, b]),
);
