import { create } from "zustand";
import { persist } from "zustand/middleware";

// Theme persisted as a plain string so it survives schema bumps.
export type Theme = "light" | "dark";

interface UIState {
  theme: Theme;
  selectedFeatureId: string | null;
  collapsedSections: Record<string, boolean>; // section key -> collapsed
  wizardOpen: boolean;
  setTheme: (t: Theme) => void;
  toggleTheme: () => void;
  selectFeature: (id: string | null) => void;
  toggleSection: (key: string) => void;
  openWizard: () => void;
  closeWizard: () => void;
}

// Single persisted Zustand store. Splitter widths live in
// react-resizable-panels' own autoSaveId mechanism (different
// storage key) so we don't fight that library.
export const useUI = create<UIState>()(
  persist(
    (set) => ({
      theme: "dark",
      selectedFeatureId: null,
      collapsedSections: {},
      wizardOpen: false,
      setTheme: (theme) => set({ theme }),
      toggleTheme: () =>
        set((s) => ({ theme: s.theme === "dark" ? "light" : "dark" })),
      selectFeature: (id) => set({ selectedFeatureId: id }),
      toggleSection: (key) =>
        set((s) => ({
          collapsedSections: {
            ...s.collapsedSections,
            [key]: !s.collapsedSections[key],
          },
        })),
      openWizard: () => set({ wizardOpen: true }),
      closeWizard: () => set({ wizardOpen: false }),
    }),
    {
      name: "agentic.web.ui",
      version: 1,
      // Skip transient UI state from persistence — wizardOpen should
      // start fresh on every load. The persisted slice keeps user
      // preferences only (theme, selected feature, collapsed
      // sections).
      partialize: (s) => ({
        theme: s.theme,
        selectedFeatureId: s.selectedFeatureId,
        collapsedSections: s.collapsedSections,
      }),
    },
  ),
);

// Apply the persisted theme to <html data-theme> on every change.
// Runs once at module load so the first paint matches the stored theme
// without a flash.
export function applyTheme(theme: Theme) {
  document.documentElement.setAttribute("data-theme", theme);
}

// Subscribe at import time. The store's persist middleware hydrates
// from localStorage before this fires, so the first call uses the
// stored value.
useUI.subscribe((state, prev) => {
  if (state.theme !== prev.theme) {
    applyTheme(state.theme);
  }
});

// Run the initial application explicitly because the subscribe above
// only fires on changes, not on hydration.
applyTheme(useUI.getState().theme);
