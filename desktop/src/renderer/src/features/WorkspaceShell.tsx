/**
 * The readiness-gated main surface: a translucent Bench sidebar — a pinned
 * Overview row plus five lane-grouped sections of every feature — with
 * exactly one content pane mounted at a time. Feature creation descends over
 * that pane as a window-modal sheet reached from Overview — the pane beneath
 * stays mounted and navigable, so ⌘-digit shortcuts, routed navigation, and
 * attention deep-links change what is underneath without touching the draft.
 * Settings is not a state of this shell at all: it lives in its own window,
 * so every settings entry path is handled in the main process and nothing
 * here has a settings special case.
 * Local settings store ONLY the active feature id and sidebar collapse
 * state; every feature itself is always reloaded from the server, so
 * existing state survives app restarts without any local domain cache.
 */
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type Dispatch,
  type SetStateAction,
} from 'react';
import type {
  AttentionItem,
  FeatureActionView,
  FeatureSnapshot,
  MainWindowUiState,
  RoutedRequest,
  ShellPrefs,
  UpdateState,
} from '../../../shared/ipc';
import {
  attentionOwnerFeatureId,
  defaultShellPrefs,
  isConnectionErrorState,
  sameMainWindowUiState,
} from '../../../shared/ipc';
import { featureCommandEnablement, isFeatureCommandId } from '../../../shared/commands';
import { runFeatureCommand, toggleActiveInspector } from './featureCommands';
import { parseIpcError, type WizardError } from '../wizard/ipcError';
import { isEditingShortcutTarget } from '../components/CommandPalette';
import { CreateFeatureForm } from './CreateFeatureForm';
import { FeatureCockpit } from './FeatureCockpit';
import { PipRail } from '../components/Pip';
import { updateNoticePending } from '../components/UpdatePopover';
import { emptyAttentionDrafts, type AttentionDrafts } from './AttentionInbox';
import { Toolbar } from './Toolbar';
import {
  childStatusSpineIndex,
  displayStatusLabel,
  isRunAtRest,
  orderDashboardFeatures,
  runningPhaseSubline,
  spineActiveIndex,
  spineStages,
} from './featureView';
import {
  LANES,
  classifyFeaturesByLaneWithAttention,
  laneLabel,
  type Lane,
} from './laneClassification';
import { overviewHeadline, overviewSubline } from './overviewSummary';
import { BulkPreviewPanel } from './BulkPreviewPanel';
import { RecoveryWorkspace } from './RecoveryWorkspace';
import { useConnectionState, useMediaQuery } from '../hooks';

type ListState =
  | { phase: 'loading' }
  | { phase: 'error'; error: WizardError }
  | { phase: 'loaded'; features: FeatureSnapshot[] };

type Selection = { kind: 'overview' } | { kind: 'feature'; featureId: string };

/** A single addressable sidebar row, in the order ⌘2-9 count by. */
type SidebarRowEntry = { kind: 'overview' } | { kind: 'feature'; featureId: string };

export function WorkspaceShell({
  attentionItems = [],
  refreshAttention = async () => [],
  attentionDrafts,
  setAttentionDrafts,
  attentionJump = null,
  onAttentionJumpHandled = () => {},
  onAttentionJump = () => {},
  routeRequest = null,
  updateState = null,
  updateDismissedVersion = null,
  schedulingUpdate = false,
  onDismissUpdate = () => {},
  onOpenUpdatesSettings = () => {},
  onInstallUpdateWhenIdle = async () => {},
  onOpenAma = () => {},
  amaSessionActive = false,
}: {
  attentionItems?: AttentionItem[];
  refreshAttention?: () => Promise<AttentionItem[]>;
  attentionDrafts?: AttentionDrafts;
  setAttentionDrafts?: Dispatch<SetStateAction<AttentionDrafts>>;
  attentionJump?: {
    requestId: number;
    featureId: string;
    attentionId?: string;
  } | null;
  onAttentionJumpHandled?: () => void;
  /** Owned by App: routes a bell/inbox jump into the shell's own selection. */
  onAttentionJump?(featureId: string, attentionId?: string): void;
  routeRequest?: RoutedRequest | null;
  updateState?: UpdateState | null;
  updateDismissedVersion?: string | null;
  schedulingUpdate?: boolean;
  onDismissUpdate?(version: string): void;
  onOpenUpdatesSettings?(): void;
  onInstallUpdateWhenIdle?(): Promise<void>;
  /** Owned by App: dispatches the same routeRequest the ⌘⇧M accelerator does. */
  onOpenAma?(): void;
  /** True while the singleton AMA chat session is running. */
  amaSessionActive?: boolean;
}) {
  // null while the local shell prefs are being restored.
  const [shell, setShell] = useState<ShellPrefs | null>(null);
  // A purely visual auto-collapse below ~700px: it never writes
  // `shell.sidebarCollapsed` (only the toolbar button and ⌘⌃S do that), so
  // widening back past the breakpoint restores whatever the user last chose
  // explicitly instead of fighting a value this hook itself set.
  const isNarrowForSidebar = useMediaQuery('(max-width: 700px)');
  const connection = useConnectionState();
  const runtimeReady = connection.status === 'ready';
  const runtimeLabel = runtimeReady
    ? 'Runtime ready'
    : isConnectionErrorState(connection)
      ? 'Runtime needs attention'
      : 'Connecting';
  const runtimeTone = runtimeReady
    ? 'ready'
    : isConnectionErrorState(connection)
      ? 'error'
      : 'progress';
  // A connection problem always wins the footer row: an active assistant must
  // never mask a runtime that needs attention.
  const showAmaActive = runtimeReady && amaSessionActive;
  const footerLabel = showAmaActive ? 'Ask Agentico is active' : runtimeLabel;
  const footerTone = showAmaActive ? 'ama' : runtimeTone;
  // The footer dot and the toolbar update button share one predicate, so they
  // appear and disappear together.
  const updatePending = updateNoticePending(updateState, updateDismissedVersion);
  const [actionsSlot, setActionsSlot] = useState<HTMLDivElement | null>(null);
  const [overflowSlot, setOverflowSlot] = useState<HTMLDivElement | null>(null);
  // The cockpit owns its inspector's open/closed state itself, so it resets
  // for free on every feature switch via the `key={featureId}` remount
  // below; this slot is only the chrome-owned mount point its wide-layout
  // toggle button portals into.
  const [inspectorSlot, setInspectorSlot] = useState<HTMLDivElement | null>(null);
  const shellStateRef = useRef<ShellPrefs | null>(null);
  const shellPersistenceRef = useRef<Promise<void>>(Promise.resolve());
  const [list, setList] = useState<ListState>({ phase: 'loading' });
  const [localAttentionDrafts, setLocalAttentionDrafts] = useState(emptyAttentionDrafts);
  const activeAttentionDrafts = attentionDrafts ?? localAttentionDrafts;
  const updateAttentionDrafts = setAttentionDrafts ?? setLocalAttentionDrafts;
  const newFeatureButtonRef = useRef<HTMLButtonElement | null>(null);
  const handledAttentionJump = useRef<number | null>(null);
  const [attentionPreviewRequest, setAttentionPreviewRequest] = useState<{
    requestId: number;
    featureId: string;
    attentionId?: string;
  } | null>(null);
  const handledRouteRequest = useRef<number | null>(null);
  const listRequestRef = useRef(0);
  const overviewActiveRef = useRef(false);
  const [creationOpen, setCreationOpen] = useState(false);
  // The cockpit-owned half of the native menu's summary: the live action
  // catalogue behind every feature verb, and the unpersisted inspector state
  // behind the View menu's Show/Hide label.
  const [cockpitUi, setCockpitUi] = useState<{
    featureId: string;
    actions: readonly FeatureActionView[] | null;
    inspectorOpen: boolean;
  } | null>(null);
  const pushedUiStateRef = useRef<MainWindowUiState | null>(null);
  const [bulkPreviewRequest, setBulkPreviewRequest] = useState<number | null>(null);
  const [selectedRuns, setSelectedRuns] = useState<Record<string, number | null>>({});
  const [expandedLanes, setExpandedLanes] = useState<Record<Lane, boolean>>({
    waiting: true,
    running: true,
    published: true,
    done: false,
    'at-rest': true,
  });

  // Read by the ⌘2-9/⌘⌃S global listener below, which is registered exactly
  // once on mount: the listener itself must stay referentially stable across
  // re-renders (there's nothing to key its effect deps on that wouldn't churn
  // every keystroke's worth of state), so it reaches through this ref for
  // whatever is current at the moment a shortcut actually fires instead of
  // closing over a stale render's callbacks.
  const shortcutRef = useRef<{
    allRows: SidebarRowEntry[];
    navigateFeature(featureId: string): void;
    toggleSidebar(): void;
  }>({ allRows: [], navigateFeature: () => {}, toggleSidebar: () => {} });

  // ⌘2-9: the 1st-8th feature by absolute sidebar position, counting across
  // every lane regardless of its disclosure state (unlike Arrow/Home/End,
  // which only ever land on a visible row). ⌘1 stays entirely on the
  // existing native-menu → routeRequest("home") path — nothing new here.
  // ⌘⌃S toggles the same persisted collapse the toolbar button does, and ⌘N
  // opens the creation sheet the File item and the palette entry open. All of
  // them bail out untouched when a text input, textarea, or contenteditable
  // element has focus, matching the ⌘K guard in CommandPalette.
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent): void => {
      if (isEditingShortcutTarget(event.target)) return;
      if (event.metaKey && event.ctrlKey && event.key.toLowerCase() === 's') {
        event.preventDefault();
        shortcutRef.current.toggleSidebar();
        return;
      }
      const commandKey = event.metaKey || event.ctrlKey;
      // ⌘N mirrors the File item and the palette entry: it opens the creation
      // sheet over whatever pane is current. Opening is idempotent, so the
      // native accelerator and this listener both firing is harmless — the
      // same duplicate-binding posture ⌘K already has.
      if (commandKey && !event.shiftKey && !event.altKey && event.key.toLowerCase() === 'n') {
        event.preventDefault();
        setCreationOpen(true);
        return;
      }
      if (commandKey && !event.shiftKey && !event.altKey && /^[2-9]$/.test(event.key)) {
        const row = shortcutRef.current.allRows[Number(event.key) - 1];
        if (row === undefined || row.kind !== 'feature') return;
        event.preventDefault();
        shortcutRef.current.navigateFeature(row.featureId);
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, []);

  // Restore ONLY identity/presentation state locally; corrupt or missing
  // settings fall back to an empty selection.
  useEffect(() => {
    let alive = true;
    window.agentico
      .getSettings()
      .then((settings) => {
        if (alive) {
          shellStateRef.current = settings.shell;
          setShell(settings.shell);
        }
      })
      .catch(() => {
        if (alive) {
          const fallback = defaultShellPrefs();
          shellStateRef.current = fallback;
          setShell(fallback);
        }
      });
    return () => {
      alive = false;
    };
  }, []);

  const loadList = useCallback(() => {
    const request = ++listRequestRef.current;
    window.agentico
      .listFeatures()
      .then((features) =>
        Promise.all(features.map((feature) => window.agentico.getFeature(feature.id))),
      )
      .then((features) => {
        if (request === listRequestRef.current) {
          setList({ phase: 'loaded', features: orderDashboardFeatures(features) });
        }
      })
      .catch((err: unknown) => {
        if (request === listRequestRef.current) {
          setList({ phase: 'error', error: parseIpcError(err) });
        }
      });
  }, []);

  // The Overview feature list follows the authoritative server state: fetch
  // on mount and refetch on any feature-scoped invalidation or full resync.
  useEffect(() => {
    loadList();
    return window.agentico.onAppEvent((event) => {
      if (event.type !== 'invalidated') {
        return;
      }
      if (
        event.kind === 'resync' ||
        event.kind.startsWith('feature') ||
        event.kind.startsWith('lifecycle') ||
        event.kind.startsWith('relationship') ||
        event.kind.startsWith('config')
      ) {
        loadList();
      }
    });
  }, [loadList]);

  // Out-of-band changes (a delete from another window, the CLI, or a missed
  // invalidation) can leave the queue stale. Refetch when the app regains focus
  // while Overview is showing, so returning to the window always shows server truth.
  useEffect(() => {
    const refreshOverview = () => {
      if (document.visibilityState === 'visible' && overviewActiveRef.current) {
        loadList();
      }
    };
    window.addEventListener('focus', refreshOverview);
    document.addEventListener('visibilitychange', refreshOverview);
    return () => {
      window.removeEventListener('focus', refreshOverview);
      document.removeEventListener('visibilitychange', refreshOverview);
    };
  }, [loadList]);

  /** Persist failures never block the UI — the shell selection is presentation only. */
  const persist = useCallback((next: ShellPrefs) => {
    shellStateRef.current = next;
    setShell(next);
    const write = () =>
      window.agentico
        .updateSettings({ shell: next })
        .then(() => undefined)
        .catch(() => {
          // The server-side feature state is unaffected; keep later writes moving.
        });
    shellPersistenceRef.current = shellPersistenceRef.current.then(write, write);
  }, []);

  const selectFeature = useCallback(
    (featureId: string) => {
      const base = shellStateRef.current ?? defaultShellPrefs();
      persist({ ...base, activeFeatureId: featureId });
    },
    [persist],
  );

  /**
   * The toolbar's leading sidebar toggle: click-to-collapse/expand, persisted
   * through the same settings IPC as the selection. The ⌘⌃S handler above
   * calls this same function; the visual-only auto-collapse below ~700px is
   * `effectiveSidebarCollapsed`, computed above the restore gate below.
   */
  const toggleSidebar = useCallback(() => {
    const base = shellStateRef.current ?? shell ?? defaultShellPrefs();
    persist({ ...base, sidebarCollapsed: !base.sidebarCollapsed });
  }, [persist, shell]);

  const selectOverview = useCallback(() => {
    const base = shellStateRef.current ?? defaultShellPrefs();
    persist({ ...base, activeFeatureId: null });
    loadList();
  }, [loadList, persist]);

  const handleFeatureDeleted = useCallback(
    (featureId: string) => {
      setList((current) =>
        current.phase === 'loaded'
          ? {
              ...current,
              features: current.features.filter((feature) => feature.id !== featureId),
            }
          : current,
      );
      // selectOverview() already refetches the authoritative list; a second
      // fetch here would race it and can resurrect the just-deleted feature.
      selectOverview();
    },
    [selectOverview],
  );

  const attentionByFeature = useMemo(() => {
    const counts = new Map<string, number>();
    for (const item of attentionItems) {
      // A refactor pass's prompts count against the parent it owns.
      const owner = attentionOwnerFeatureId(item);
      if (owner === undefined) continue;
      counts.set(owner, (counts.get(owner) ?? 0) + 1);
    }
    return counts;
  }, [attentionItems]);
  const attentionKindsByFeature = useMemo(() => {
    const kinds = new Map<string, Record<AttentionItem['kind'], number>>();
    for (const item of attentionItems) {
      const owner = attentionOwnerFeatureId(item);
      if (owner === undefined) continue;
      const entry =
        kinds.get(owner) ??
        ({ permission: 0, questions: 0, help: 0, gate: 0, review: 0, recovery: 0 } as Record<
          AttentionItem['kind'],
          number
        >);
      entry[item.kind] += 1;
      kinds.set(owner, entry);
    }
    return kinds;
  }, [attentionItems]);
  const featureLabel = useCallback(
    (featureId: string | undefined): string => {
      if (featureId === undefined) return 'Runtime';
      const listed =
        list.phase === 'loaded'
          ? list.features.find((feature) => feature.id === featureId)?.name
          : undefined;
      return listed ?? 'Untitled feature';
    },
    [list],
  );

  useEffect(() => {
    if (attentionJump === null) {
      handledAttentionJump.current = null;
      return;
    }
    if (shell === null || handledAttentionJump.current === attentionJump.requestId) return;
    handledAttentionJump.current = attentionJump.requestId;
    if (attentionJump.featureId === '__recovery__') {
      persist({ ...(shellStateRef.current ?? shell), activeFeatureId: null });
    } else {
      selectFeature(attentionJump.featureId);
      setAttentionPreviewRequest({
        requestId: attentionJump.requestId,
        featureId: attentionJump.featureId,
        ...(attentionJump.attentionId === undefined
          ? {}
          : { attentionId: attentionJump.attentionId }),
      });
    }
    onAttentionJumpHandled();
  }, [attentionJump, onAttentionJumpHandled, persist, selectFeature, shell]);

  const closeAttentionPreview = useCallback(() => setAttentionPreviewRequest(null), []);

  // A visual-only auto-collapse: never persisted, and an explicit user choice
  // (persisted `true`) always wins over the breakpoint once one exists — see
  // the module doc comment above `isNarrowForSidebar`. Computed here, ahead of
  // the restore gate below, because the pushed summary carries what the user
  // actually sees rather than only what is stored.
  const effectiveSidebarCollapsed =
    shell !== null && (shell.sidebarCollapsed || isNarrowForSidebar);

  /**
   * The coarse summary the native menu bar runs on. Recomputed only from the
   * things it names — selection, readiness, the two chrome toggles, and the
   * selected feature's live action catalogue.
   */
  const uiStateSummary: MainWindowUiState | null = useMemo(() => {
    if (shell === null) return null;
    const activeFeatureId = shell.activeFeatureId;
    const cockpitMatches = cockpitUi !== null && cockpitUi.featureId === activeFeatureId;
    return {
      activeFeatureId,
      runtimeReady,
      sidebarCollapsed: effectiveSidebarCollapsed,
      inspectorOpen: cockpitMatches ? cockpitUi.inspectorOpen : false,
      // Overview has nothing to inspect, so there is no toggle to offer.
      inspectorAvailable: activeFeatureId !== null,
      featureCommands: featureCommandEnablement(cockpitMatches ? cockpitUi.actions : null, {
        hasSelection: activeFeatureId !== null,
      }),
    };
  }, [cockpitUi, effectiveSidebarCollapsed, runtimeReady, shell]);

  // Push on change only: an identical summary — an unchanged snapshot refresh,
  // a re-render, a repeated readiness event — never reaches the main process.
  useEffect(() => {
    if (uiStateSummary === null) return;
    const previous = pushedUiStateRef.current;
    if (previous !== null && sameMainWindowUiState(previous, uiStateSummary)) return;
    pushedUiStateRef.current = uiStateSummary;
    void window.agentico.publishUiState(uiStateSummary).catch(() => {
      // The menu is a convenience surface; a failed push never disturbs the
      // window the user is looking at.
    });
  }, [uiStateSummary]);

  useEffect(() => {
    if (shell === null || routeRequest === null) return;
    if (handledRouteRequest.current === routeRequest.id) return;
    handledRouteRequest.current = routeRequest.id;
    // Routed navigation acts on the pane beneath an open creation sheet: it
    // never closes the sheet or touches the draft.
    if (routeRequest.event.target === 'home') {
      selectOverview();
    } else if (routeRequest.event.target === 'bulk') {
      setBulkPreviewRequest(routeRequest.id);
      selectOverview();
    } else if (routeRequest.event.target === 'new-feature') {
      setCreationOpen(true);
    } else if (routeRequest.event.target === 'toggle-sidebar') {
      shortcutRef.current.toggleSidebar();
    } else if (routeRequest.event.target === 'toggle-inspector') {
      toggleActiveInspector();
    } else if (routeRequest.event.target === 'feature-command') {
      // The route carries only the command's identity; the funnel resolves the
      // target from the live selection and re-checks its live enablement, so a
      // click that raced a selection change is a no-op.
      const command = routeRequest.event.command;
      if (command !== undefined && isFeatureCommandId(command)) {
        runFeatureCommand(command);
      }
    }
  }, [routeRequest, selectOverview, shell]);

  if (shell === null) {
    return (
      <section className="shell-card workspace" aria-label="Workspace">
        <p role="status" aria-live="polite" className="cockpit__loading">
          Restoring workspace…
        </p>
      </section>
    );
  }

  const activeFeatureId = shell.activeFeatureId;
  // The cockpit always reloads its own snapshot directly by id, so a
  // persisted selection is trusted even if the summary list hasn't returned
  // it yet (or ever) — the cockpit itself renders the "no longer exists"
  // state when the server truly has nothing under that id.
  const selection: Selection =
    activeFeatureId !== null
      ? { kind: 'feature', featureId: activeFeatureId }
      : { kind: 'overview' };
  // Read by the focus/visibility refresh so it only refetches when Overview is shown.
  overviewActiveRef.current = selection.kind === 'overview' && !creationOpen;

  const toggleLane = (lane: Lane, expanded: boolean) => {
    setExpandedLanes((current) => ({ ...current, [lane]: expanded }));
  };

  const features = list.phase === 'loaded' ? list.features : [];
  const laneGroups = classifyFeaturesByLaneWithAttention(features, attentionByFeature);
  const counts = Object.fromEntries(LANES.map((lane) => [lane, laneGroups[lane].length])) as Record<
    Lane,
    number
  >;

  // The absolute sidebar order ⌘2-9 count by: Overview, then every lane in
  // display order, every feature within it — regardless of which lanes are
  // currently expanded. (Arrow/Home/End instead walk the DOM directly, at
  // click time, so they only ever land on what a `<details>` disclosure
  // state is actually showing.)
  const allRows: SidebarRowEntry[] = [{ kind: 'overview' }];
  for (const lane of LANES) {
    for (const feature of laneGroups[lane]) {
      allRows.push({ kind: 'feature', featureId: feature.id });
    }
  }

  const selectedFeature =
    selection.kind === 'feature'
      ? features.find((feature) => feature.id === selection.featureId)
      : undefined;
  const showTrailingToolbar = selection.kind === 'feature';
  // Stays mounted under the creation sheet so closing it restores focus here.
  const showNewFeatureButton = selection.kind === 'overview';
  const toolbarTitle =
    selection.kind === 'feature' ? featureLabel(selection.featureId) : 'Overview';
  const toolbarSubline =
    selection.kind === 'feature' ? repoBranchSubline(selectedFeature) : undefined;

  // Keep the global ⌘2-9/⌘⌃S listener's stale-closure guard current every
  // render — see the ref's declaration above for why it isn't itself a hook.
  shortcutRef.current = { allRows, navigateFeature: selectFeature, toggleSidebar };

  /**
   * Roving tabindex: ArrowUp/ArrowDown/Home/End move focus AND selection
   * together ("selection follows focus") through every row the DOM
   * currently exposes as `role="option"` — rows inside a collapsed
   * `<details>` lane stay in the DOM (so ⌘2-9 above can still reach them)
   * but are excluded here by checking each row's nearest `<details>`
   * ancestor, matching the disclosure-aware roving-focus pattern already
   * used for the run-cohort roster in CurrentRunInspection.
   */
  const onSidebarListKeyDown = (event: React.KeyboardEvent<HTMLDivElement>): void => {
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;
    const rows = Array.from(
      event.currentTarget.querySelectorAll<HTMLElement>('[role="option"]'),
    ).filter((row) => {
      const details = row.closest('details');
      return details === null || details.open;
    });
    if (rows.length === 0) return;
    const current = rows.findIndex((row) => row === document.activeElement);
    const next =
      event.key === 'Home'
        ? 0
        : event.key === 'End'
          ? rows.length - 1
          : event.key === 'ArrowDown'
            ? (current + 1) % rows.length
            : (Math.max(current, 0) - 1 + rows.length) % rows.length;
    const target = rows[next];
    if (target === undefined) return;
    event.preventDefault();
    target.focus();
    if (target.id === 'sidebar-overview') {
      selectOverview();
    } else {
      selectFeature(target.id.slice('sidebar-row-'.length));
    }
  };

  return (
    <section
      className="workspace"
      aria-label="Workspace"
      data-sidebar-collapsed={effectiveSidebarCollapsed}
    >
      <nav
        className="sidebar"
        aria-label="Feature sidebar"
        data-collapsed={effectiveSidebarCollapsed}
      >
        <div className="sidebar__header" aria-hidden="true" />
        <div
          className="sidebar__list"
          role="listbox"
          aria-label="Features"
          onKeyDown={onSidebarListKeyDown}
        >
          <SidebarRow
            id="sidebar-overview"
            label="Overview"
            selected={selection.kind === 'overview'}
            onSelect={selectOverview}
          />
          {LANES.map((lane) => {
            const laneFeatures = laneGroups[lane];
            if (laneFeatures.length === 0) return null;
            return (
              <details
                key={lane}
                className="sidebar__lane"
                open={expandedLanes[lane]}
                onToggle={(event) => toggleLane(lane, event.currentTarget.open)}
              >
                <summary className="sidebar__lane-summary">
                  <span>{laneLabel(lane)}</span>
                  <span className="sidebar__lane-count" aria-hidden="true">
                    {counts[lane]}
                  </span>
                </summary>
                <div role="group" aria-label={laneLabel(lane)} className="sidebar__lane-rows">
                  {laneFeatures.map((feature) => (
                    <SidebarFeatureRow
                      key={feature.id}
                      lane={lane}
                      feature={feature}
                      attentionKinds={attentionKindsByFeature.get(feature.id)}
                      selected={selection.kind === 'feature' && selection.featureId === feature.id}
                      onSelect={() => selectFeature(feature.id)}
                    />
                  ))}
                </div>
              </details>
            );
          })}
        </div>
        <div className="sidebar__footer" data-tone={footerTone}>
          <span className="sidebar__runtime" role="status">
            <span aria-hidden="true">●</span> {footerLabel}
          </span>
          {/* Indicator only, never a target: the toolbar button stays the one
           * way into the update popover, and the footer keeps exactly one
           * interactive element. */}
          {updatePending ? (
            <span className="sidebar__update-dot" role="img" aria-label="Update available" />
          ) : null}
          <button type="button" className="sidebar__ama" onClick={onOpenAma}>
            Ask ⌥Space
          </button>
        </div>
      </nav>

      <div className="content-column">
        <Toolbar
          sidebarCollapsed={effectiveSidebarCollapsed}
          onToggleSidebar={toggleSidebar}
          title={toolbarTitle}
          subline={toolbarSubline}
          showTrailing={showTrailingToolbar}
          showNewFeature={showNewFeatureButton}
          onNewFeature={() => setCreationOpen(true)}
          newFeatureButtonRef={newFeatureButtonRef}
          attention={{
            items: attentionItems,
            refresh: refreshAttention,
            featureLabel,
            drafts: activeAttentionDrafts,
            setDrafts: updateAttentionDrafts,
            onJump: onAttentionJump,
            openRequest:
              routeRequest?.event.target === 'attention'
                ? { id: routeRequest.id, attentionId: routeRequest.event.attentionId }
                : null,
          }}
          update={{
            update: updateState,
            dismissedVersion: updateDismissedVersion,
            scheduling: schedulingUpdate,
            onDismiss: onDismissUpdate,
            onOpenSettings: onOpenUpdatesSettings,
            onInstallWhenIdle: onInstallUpdateWhenIdle,
          }}
          actionsSlotRef={setActionsSlot}
          overflowSlotRef={setOverflowSlot}
          inspectorSlotRef={setInspectorSlot}
        />
        <div
          className={
            selection.kind === 'feature' ? 'content-pane content-pane--flush' : 'content-pane'
          }
        >
          {selection.kind === 'feature' ? (
            <FeatureCockpit
              key={selection.featureId}
              active
              featureId={selection.featureId}
              titleHint={featureLabel(selection.featureId)}
              onClose={selectOverview}
              onDeleted={handleFeatureDeleted}
              onLoadedName={() => {}}
              attentionItems={attentionItems.filter(
                (item) =>
                  item.kind !== 'recovery' && attentionOwnerFeatureId(item) === selection.featureId,
              )}
              refreshAttention={refreshAttention}
              attentionDrafts={activeAttentionDrafts}
              setAttentionDrafts={updateAttentionDrafts}
              attentionPreviewRequest={
                attentionPreviewRequest?.featureId === selection.featureId
                  ? attentionPreviewRequest
                  : null
              }
              onAttentionPreviewClose={closeAttentionPreview}
              selectedRunNumber={selectedRuns[selection.featureId] ?? null}
              onSelectRun={(runNumber) => {
                const featureId = selection.featureId;
                setSelectedRuns((current) => ({ ...current, [featureId]: runNumber }));
              }}
              actionsHost={actionsSlot}
              overflowMenuHost={overflowSlot}
              inspectorToggleHost={inspectorSlot}
              onUiStateChange={setCockpitUi}
            />
          ) : (
            <div className="overview-surface">
              <header className="overview-surface__header">
                <h1 className="overview-surface__headline">
                  {overviewHeadline(counts, features.length)}
                </h1>
                <p className="overview-surface__subline">
                  {overviewSubline(laneGroups, attentionItems, features.length)}
                </p>
                {features.length === 0 ? (
                  <button
                    type="button"
                    className="overview-surface__cta"
                    onClick={() => setCreationOpen(true)}
                  >
                    Create a feature
                  </button>
                ) : null}
              </header>
              <OverviewLanes
                state={list}
                laneGroups={laneGroups}
                attentionItems={attentionItems}
                attentionKindsByFeature={attentionKindsByFeature}
                onOpen={(featureId) => selectFeature(featureId)}
                onAnswer={(featureId, attentionId) => {
                  if (attentionId === undefined) {
                    selectFeature(featureId);
                  } else {
                    onAttentionJump(featureId, attentionId);
                  }
                }}
                onRetry={loadList}
              />
              <RecoveryWorkspace onNavigateToFeature={(featureId) => selectFeature(featureId)} />
              <BulkPreviewPanel autoPreviewKey={bulkPreviewRequest} />
            </div>
          )}
        </div>
      </div>

      {creationOpen ? (
        <CreateFeatureForm
          onClose={() => setCreationOpen(false)}
          onCreated={({ featureId }) => {
            setCreationOpen(false);
            loadList();
            selectFeature(featureId);
          }}
        />
      ) : null}
    </section>
  );
}

/**
 * The toolbar's mono sub-line: the first repository plus its feature branch
 * (read from the matching setup task, when the server reported one), with a
 * `+N` suffix when more repositories exist beyond the first. Absent data —
 * no repos, no matching branch — is omitted outright rather than rendered as
 * "undefined" or a dangling separator. Overview never calls this.
 */
function repoBranchSubline(feature: FeatureSnapshot | undefined): string | undefined {
  if (feature === undefined) return undefined;
  const [firstRepo, ...restRepos] = feature.repos;
  if (firstRepo === undefined) return undefined;
  const branch = feature.setup?.tasks.find((task) => task.repo === firstRepo)?.branch;
  const base = branch !== undefined && branch !== '' ? `${firstRepo} · ${branch}` : firstRepo;
  return restRepos.length > 0 ? `${base} +${restRepos.length}` : base;
}

/** A single row in the Bench sidebar's listbox: Overview or a lane member. */
function SidebarRow({
  id,
  label,
  subline,
  glyphTone,
  pip,
  selected,
  onSelect,
}: {
  id: string;
  label: string;
  subline?: string;
  glyphTone?: 'attention' | 'progress' | 'ok' | 'quiet';
  pip?: {
    stageCount: number;
    activeIndex: number;
    atRest: boolean;
    tone: 'progress' | 'attention';
  };
  selected: boolean;
  onSelect(): void;
}) {
  return (
    <div
      id={id}
      role="option"
      aria-selected={selected}
      tabIndex={selected ? 0 : -1}
      className="sidebar__row"
      data-selected={selected}
      onClick={onSelect}
    >
      <span className="sidebar__row-glyph" data-tone={glyphTone ?? 'quiet'} aria-hidden="true" />
      <span className="sidebar__row-body">
        <span className="sidebar__row-name">{label}</span>
        {subline !== undefined ? <span className="sidebar__row-subline">{subline}</span> : null}
      </span>
      {pip !== undefined ? (
        <PipRail
          stageCount={pip.stageCount}
          activeIndex={pip.activeIndex}
          atRest={pip.atRest}
          tone={pip.tone}
          label={`${label} progress`}
        />
      ) : null}
    </div>
  );
}

const LANE_GLYPH_TONE: Readonly<Record<Lane, 'attention' | 'progress' | 'ok' | 'quiet'>> = {
  waiting: 'attention',
  running: 'progress',
  published: 'ok',
  done: 'ok',
  'at-rest': 'quiet',
};

/** Renders a feature row's mono sub-line and progress pip strictly from lane rules. */
function SidebarFeatureRow({
  lane,
  feature,
  attentionKinds,
  selected,
  onSelect,
}: {
  lane: Lane;
  feature: FeatureSnapshot;
  attentionKinds: Record<AttentionItem['kind'], number> | undefined;
  selected: boolean;
  onSelect(): void;
}) {
  const subline = laneSubline(lane, feature, attentionKinds);
  const pipInfo = lane === 'waiting' || lane === 'running' ? pipRailFor(feature) : null;
  return (
    <SidebarRow
      id={`sidebar-row-${feature.id}`}
      label={feature.name}
      subline={subline}
      glyphTone={LANE_GLYPH_TONE[lane]}
      selected={selected}
      onSelect={onSelect}
      pip={
        pipInfo === null
          ? undefined
          : {
              ...pipInfo,
              tone: lane === 'waiting' ? 'attention' : 'progress',
            }
      }
    />
  );
}

/** The pipeline position backing a sidebar row's pip strip, from the active child pass
 * when there is one, otherwise the parent's own pipeline. Returns null when the
 * status names no phase the rail can place a needle on. */
function pipRailFor(
  feature: FeatureSnapshot,
): { stageCount: number; activeIndex: number; atRest: boolean } | null {
  const child = feature.activeChild;
  if (child !== undefined) {
    const stages = spineStages(child.pipeline);
    const index = childStatusSpineIndex(child.status, stages);
    if (index === null) return null;
    return { stageCount: stages.length, activeIndex: index, atRest: false };
  }
  const stages = spineStages(feature.pipeline);
  return {
    stageCount: stages.length,
    activeIndex: spineActiveIndex(feature, stages),
    atRest: isRunAtRest(feature.status),
  };
}

/** One pending-attention summary counter, ordered by how a person should triage them. */
function attentionSummary(
  counts: Record<AttentionItem['kind'], number> | undefined,
): string | undefined {
  if (counts === undefined) return undefined;
  const plural = (count: number) => (count === 1 ? '' : 's');
  if (counts.questions > 0) return `Answer ${counts.questions} question${plural(counts.questions)}`;
  if (counts.permission > 0)
    return `Approve ${counts.permission} request${plural(counts.permission)}`;
  if (counts.gate > 0) return `Resolve ${counts.gate} gate${plural(counts.gate)}`;
  if (counts.help > 0) return `Respond to ${counts.help} prompt${plural(counts.help)}`;
  if (counts.review > 0) return `Review ${counts.review} item${plural(counts.review)}`;
  return undefined;
}

/**
 * The row sub-line, entirely lane-scoped: waiting rows summarize what needs a
 * person (falling back to the status text when nothing raised a discrete
 * attention item yet), running rows name the active pass or the phase and
 * iteration, at-rest rows show the plain status text, and published/done
 * rows carry no sub-line at all — only the name and the status glyph, per
 * the mock. Any part the snapshot has no data for is omitted rather than
 * rendered as "undefined".
 */
function laneSubline(
  lane: Lane,
  feature: FeatureSnapshot,
  attentionKinds: Record<AttentionItem['kind'], number> | undefined,
): string | undefined {
  if (lane === 'waiting') {
    return attentionSummary(attentionKinds) ?? displayStatusLabel(feature.status);
  }
  if (lane === 'running') {
    if (feature.activeChild !== undefined) return feature.activeChild.name;
    return runningPhaseSubline(
      feature.currentPhase,
      feature.currentRoadmapPhase,
      feature.totalRoadmapPhases,
      feature.currentIteration,
    );
  }
  if (lane === 'at-rest') {
    return displayStatusLabel(feature.status);
  }
  // published / done: name + glyph only, no sub-line.
  return undefined;
}

interface OverviewLanesProps {
  state: ListState;
  laneGroups: Record<Lane, FeatureSnapshot[]>;
  attentionItems: readonly AttentionItem[];
  attentionKindsByFeature: ReadonlyMap<string, Record<AttentionItem['kind'], number>>;
  onOpen(featureId: string): void;
  onAnswer(featureId: string, attentionId?: string): void;
  onRetry(): void;
}

/** The first non-recovery attention item this feature owns, if any. */
function firstAttentionItemFor(
  featureId: string,
  attentionItems: readonly AttentionItem[],
): AttentionItem | undefined {
  return attentionItems.find(
    (item) => item.kind !== 'recovery' && attentionOwnerFeatureId(item) === featureId,
  );
}

/** The row's mono sub-line: the repo list, extended with the active pass name. */
function overviewRowSubline(feature: FeatureSnapshot): string {
  const repos = feature.repos.join(', ');
  const childName = feature.activeChild?.name;
  return childName !== undefined && childName !== '' ? `${repos} · ${childName}` : repos;
}

/** The lane-scoped fallback text for a lane whose sub-line cascade
 * (`laneSubline`) has nothing to report — e.g. a running row with no
 * active child and no current phase. */
const OVERVIEW_ROW_STATE_FALLBACK: Readonly<Record<Lane, string>> = {
  waiting: '',
  running: 'Running',
  'at-rest': '',
  published: 'Shipped',
  done: 'Done',
};

/** The row's state-cell text, one per lane per the mock: never invents a fact
 * the snapshot doesn't carry. Reuses the sidebar's own `laneSubline`
 * cascade so the two never drift apart, falling back only where the
 * sidebar's undefined sub-line (no sub-line shown there) needs Overview
 * copy instead. */
function overviewRowStateText(
  lane: Lane,
  feature: FeatureSnapshot,
  attentionKinds: Record<AttentionItem['kind'], number> | undefined,
): string {
  return laneSubline(lane, feature, attentionKinds) ?? OVERVIEW_ROW_STATE_FALLBACK[lane];
}

/** The row-scale pip rail: sidebar's `pipRailFor` for waiting/running rows,
 * an all-done rail for the resting lanes (at-rest/published/done). Returns
 * null for a waiting/running row whose status names no phase the rail can
 * place a needle on — mirroring the sidebar's `SidebarFeatureRow`, which
 * renders no pip at all in that case rather than inventing a fully-filled,
 * done-looking rail for a row that hasn't finished anything. */
function overviewRowPip(
  lane: Lane,
  feature: FeatureSnapshot,
): {
  stageCount: number;
  activeIndex: number;
  atRest: boolean;
  tone: 'progress' | 'attention';
} | null {
  if (lane === 'waiting' || lane === 'running') {
    const info = pipRailFor(feature);
    return info === null ? null : { ...info, tone: lane === 'waiting' ? 'attention' : 'progress' };
  }
  const stages = spineStages(feature.activeChild?.pipeline ?? feature.pipeline);
  return {
    stageCount: stages.length,
    activeIndex: stages.length - 1,
    atRest: true,
    tone: 'progress',
  };
}

/** Every non-empty lane rendered as its own grouped inset list, in sidebar order. */
function OverviewLanes({
  state,
  laneGroups,
  attentionItems,
  attentionKindsByFeature,
  onOpen,
  onAnswer,
  onRetry,
}: OverviewLanesProps) {
  const totalFeatures = LANES.reduce((sum, lane) => sum + laneGroups[lane].length, 0);
  return (
    <section className="overview-lanes" aria-label="Existing features">
      {state.phase === 'loading' ? (
        <p role="status" className="setup-step__empty">
          Loading features…
        </p>
      ) : state.phase === 'error' ? (
        <div className="overview-lanes__error">
          <p className="form-field__error">
            {state.error.code}: {state.error.message}
          </p>
          <button type="button" className="setup-wizard__action" onClick={onRetry}>
            Try again
          </button>
        </div>
      ) : totalFeatures === 0 ? null : (
        <div className="overview-lanes__groups">
          {LANES.map((lane) =>
            laneGroups[lane].length === 0 ? null : (
              <OverviewLaneSection
                key={lane}
                lane={lane}
                features={laneGroups[lane]}
                attentionItems={attentionItems}
                attentionKindsByFeature={attentionKindsByFeature}
                onOpen={onOpen}
                onAnswer={onAnswer}
              />
            ),
          )}
        </div>
      )}
    </section>
  );
}

function OverviewLaneSection({
  lane,
  features,
  attentionItems,
  attentionKindsByFeature,
  onOpen,
  onAnswer,
}: {
  lane: Lane;
  features: FeatureSnapshot[];
  attentionItems: readonly AttentionItem[];
  attentionKindsByFeature: ReadonlyMap<string, Record<AttentionItem['kind'], number>>;
  onOpen(featureId: string): void;
  onAnswer(featureId: string, attentionId?: string): void;
}) {
  return (
    <section className="overview-lane" aria-labelledby={`overview-lane-${lane}`}>
      <div className="overview-lane__head">
        <h2 id={`overview-lane-${lane}`} className="overview-lane__title">
          {laneLabel(lane)}
        </h2>
        <span className="overview-lane__count" aria-hidden="true">
          {features.length}
        </span>
      </div>
      <div className="overview-lane__group">
        <ul className="overview-lane__rows">
          {features.map((feature) => (
            <OverviewRow
              key={feature.id}
              lane={lane}
              feature={feature}
              attentionKinds={attentionKindsByFeature.get(feature.id)}
              onOpen={() => onOpen(feature.id)}
              onAnswer={() =>
                onAnswer(feature.id, firstAttentionItemFor(feature.id, attentionItems)?.id)
              }
            />
          ))}
        </ul>
      </div>
    </section>
  );
}

function OverviewRow({
  lane,
  feature,
  attentionKinds,
  onOpen,
  onAnswer,
}: {
  lane: Lane;
  feature: FeatureSnapshot;
  attentionKinds: Record<AttentionItem['kind'], number> | undefined;
  onOpen(): void;
  onAnswer(): void;
}) {
  const pip = overviewRowPip(lane, feature);
  const tone = LANE_GLYPH_TONE[lane];
  return (
    <li className="overview-row" data-lane={lane}>
      <button type="button" className="overview-row__hit" onClick={onOpen}>
        <span className="overview-row__body">
          <span className="overview-row__name">{feature.name}</span>
          <span className="overview-row__subline">{overviewRowSubline(feature)}</span>
        </span>
        <span className="overview-row__state-col">
          <span className="overview-row__state" data-tone={tone}>
            <span className="overview-row__state-dot" data-tone={tone} aria-hidden="true" />
            <span className="overview-row__state-text">
              {overviewRowStateText(lane, feature, attentionKinds)}
            </span>
          </span>
          {pip === null ? null : (
            <PipRail
              stageCount={pip.stageCount}
              activeIndex={pip.activeIndex}
              atRest={pip.atRest}
              tone={pip.tone}
              label={`${feature.name} progress`}
            />
          )}
        </span>
      </button>
      <button
        type="button"
        className="overview-row__action"
        onClick={lane === 'waiting' ? onAnswer : onOpen}
      >
        {lane === 'waiting' ? 'Answer' : 'Open'}
      </button>
    </li>
  );
}
