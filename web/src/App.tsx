import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  Panel,
  PanelGroup,
  PanelResizeHandle,
} from "react-resizable-panels";
import { TopBar } from "./components/TopBar";
import { Footer } from "./components/Footer";
import { FeatureList } from "./components/FeatureList";
import { FeatureDetail } from "./components/FeatureDetail";
import { ActivityPanel } from "./components/ActivityPanel";
import { KeymapHelpOverlay } from "./components/KeymapHelpOverlay";
import { KeymapProvider, useKeyAction } from "./components/KeymapProvider";
import { RecoveryModal } from "./components/RecoveryModal";
import { Wizard } from "./components/Wizard";
import { WSProvider } from "./ws/provider";
import { useUI } from "./store/ui";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // Surface errors quickly while we're still wiring things up.
      retry: 1,
      staleTime: 2_000,
    },
  },
});

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <WSProvider>
      <KeymapProvider>
      <GlobalShortcuts />
      <div className="h-full flex flex-col">
        <TopBar />
        <PanelGroup
          direction="horizontal"
          autoSaveId="agentic.web.layout"
          className="flex-1"
        >
          <Panel
            defaultSize={22}
            minSize={15}
            maxSize={40}
            collapsible
            order={1}
          >
            <FeatureList />
          </Panel>
          <ResizeHandle />
          <Panel defaultSize={56} minSize={30} order={2}>
            <FeatureDetail />
          </Panel>
          <ResizeHandle />
          <Panel
            defaultSize={22}
            minSize={15}
            maxSize={40}
            collapsible
            order={3}
          >
            <ActivityPanel />
          </Panel>
        </PanelGroup>
        <Footer />
        <Wizard />
        <RecoveryModal />
        <KeymapHelpOverlay />
      </div>
      </KeymapProvider>
      </WSProvider>
    </QueryClientProvider>
  );
}

// GlobalShortcuts owns the app-wide keybindings (open wizard, focus
// search). Lives inside KeymapProvider so useKeyAction wires up.
function GlobalShortcuts() {
  const openWizard = useUI((s) => s.openWizard);
  useKeyAction("newFeature", openWizard);
  useKeyAction("focusFilter", () => {
    const el = document.querySelector<HTMLInputElement>(
      'input[type="search"]',
    );
    if (el) el.focus();
  });
  return null;
}

// 6 px draggable gutter. Light mode shows a static warm cream so the
// split between panels is always visible; dark mode keeps it transparent.
// Hover/active pick up the accent. react-resizable-panels handles keyboard
// activation (focus + arrow keys) and touch out of the box.
function ResizeHandle() {
  return (
    <PanelResizeHandle className="w-1.5 bg-[var(--resize-handle-bg)] hover:bg-[var(--resize-handle-hover-bg)] data-[resize-handle-active]:bg-[var(--resize-handle-hover-bg)] transition-colors" />
  );
}
