import { create } from "zustand";
import type { KeyAction } from "../keymap";

// Global dispatcher state: registered handlers keyed by KeyAction.
// Components register their interest in a key (e.g. FeatureDetail
// owns "logs", "publish", "rewind"; App owns "help", "newFeature";
// FeatureList owns "focusFilter", "navUp", "navDown", "openSelected").
//
// The dispatcher hook (useKeymap in components/KeymapProvider.tsx)
// reads from this store and invokes the matching handler on each
// keydown event. Registration is reference-counted so multiple
// components can listen for the same action without trampling each
// other — last-registered wins, which matches the "modal-on-top
// captures keys" UX users expect.

type Handler = () => void;

interface Slot {
  handlers: Handler[];
}

interface KeymapStore {
  slots: Record<string, Slot>;
  register: (action: KeyAction, handler: Handler) => () => void;
  invoke: (action: KeyAction) => boolean;
}

export const useKeymapStore = create<KeymapStore>((set, get) => ({
  slots: {},
  register: (action, handler) => {
    set((s) => {
      const existing = s.slots[action]?.handlers ?? [];
      return {
        slots: {
          ...s.slots,
          [action]: { handlers: [...existing, handler] },
        },
      };
    });
    return () => {
      set((s) => {
        const existing = s.slots[action]?.handlers ?? [];
        const next = existing.filter((h) => h !== handler);
        return {
          slots: { ...s.slots, [action]: { handlers: next } },
        };
      });
    };
  },
  invoke: (action) => {
    const handlers = get().slots[action]?.handlers ?? [];
    if (handlers.length === 0) return false;
    // Last registered wins (modal-on-top semantics).
    const handler = handlers[handlers.length - 1];
    handler();
    return true;
  },
}));
