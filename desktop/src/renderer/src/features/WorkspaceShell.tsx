/**
 * The readiness-gated main surface: a translucent Bench sidebar — a pinned
 * Overview row plus five lane-grouped sections of every feature — with
 * exactly one content pane mounted at a time. Feature creation is a
 * focused, secondary flow reached from Overview; Settings is a temporary,
 * unpersisted content-pane view (a stub a later phase retires with a real
 * Settings window) reached only through the existing ⌘,/palette/menu/tray
 * entry points. Settings is a third, orthogonal state layered on top of the
 * persisted selection rather than a value that selection can hold: opening
 * it deselects every sidebar row without touching `shell.activeFeatureId`,
 * so closing it via a neutral affordance (the panel's own Back control)
 * simply reveals whatever was already selected underneath — only an
 * explicit choice (⌘1, or clicking a row) changes that selection.
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
  type CSSProperties,
  type Dispatch,
  type RefObject,
  type SetStateAction,
} from 'react';
import type {
  AttentionItem,
  FeatureSnapshot,
  RoutedRequest,
  ShellPrefs,
  UpdateState,
} from '../../../shared/ipc';
import {
  attentionOwnerFeatureId,
  defaultShellPrefs,
  isConnectionErrorState,
} from '../../../shared/ipc';
import { parseIpcError, type WizardError } from '../wizard/ipcError';
import { isEditingShortcutTarget } from '../components/CommandPalette';
import { CreateFeatureForm } from './CreateFeatureForm';
import { FeatureCockpit } from './FeatureCockpit';
import { SettingsPanel } from './SettingsPanel';
import { PipRail } from '../components/Pip';
import { UpdateNotice } from '../components/UpdateNotice';
import { emptyAttentionDrafts, type AttentionDrafts } from './AttentionInbox';
import { Toolbar } from './Toolbar';
import {
  childStatusSpineIndex,
  dashboardGroupId,
  dashboardState,
  displayStatusLabel,
  formatElapsed,
  groupDashboardFeatures,
  isRunAtRest,
  orderDashboardFeatures,
  railStageLabel,
  spineActiveIndex,
  spineStages,
  type SpineStage,
} from './featureView';
import { LANES, classifyFeaturesByLane, laneLabel, type Lane } from './laneClassification';
import { BulkPreviewPanel } from './BulkPreviewPanel';
import { RecoveryWorkspace } from './RecoveryWorkspace';
import { useConnectionState, useMediaQuery, usePrefersReducedMotion } from '../hooks';

type ListState =
  | { phase: 'loading' }
  | { phase: 'error'; error: WizardError }
  | { phase: 'loaded'; features: FeatureSnapshot[] };

type Selection = { kind: 'overview' } | { kind: 'feature'; featureId: string };

/** A single addressable sidebar row, in the order ⌘2-9 count by. */
type SidebarRowEntry = { kind: 'overview' } | { kind: 'feature'; featureId: string };

type CreationDestination =
  { kind: 'overview' } | { kind: 'settings' } | { kind: 'feature'; featureId: string };

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
  const [view, setView] = useState<'overview' | 'create'>('overview');
  const [bulkPreviewRequest, setBulkPreviewRequest] = useState<number | null>(null);
  const [settingsOpen, setSettingsOpen] = useState(false);
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
  // ⌘⌃S toggles the same persisted collapse the toolbar button does. Both
  // bail out untouched when a text input, textarea, or contenteditable
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
      setSettingsOpen(false);
      setView('overview');
    },
    [persist],
  );

  /**
   * The toolbar's leading sidebar toggle: click-to-collapse/expand, persisted
   * through the same settings IPC as the selection. The ⌘⌃S handler above
   * calls this same function; the visual-only auto-collapse below ~700px is
   * `effectiveSidebarCollapsed`, computed later in this component.
   */
  const toggleSidebar = useCallback(() => {
    const base = shellStateRef.current ?? shell ?? defaultShellPrefs();
    persist({ ...base, sidebarCollapsed: !base.sidebarCollapsed });
  }, [persist, shell]);

  const selectOverview = useCallback(() => {
    const base = shellStateRef.current ?? defaultShellPrefs();
    persist({ ...base, activeFeatureId: null });
    setSettingsOpen(false);
    setView('overview');
    loadList();
  }, [loadList, persist]);

  const openSettings = useCallback(() => {
    setSettingsOpen(true);
    setView('overview');
  }, []);

  /**
   * The neutral "leave Settings" affordance: unlike `selectOverview`, this
   * never touches `shell.activeFeatureId`, so whichever row was selected
   * before Settings opened (including none, for Overview) is exactly what
   * reappears — the fix for Settings previously hardcoding a return to
   * Overview regardless of what was open beforehand.
   */
  const closeSettings = useCallback(() => {
    setSettingsOpen(false);
  }, []);

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
      setSettingsOpen(false);
      setView('overview');
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

  const completeCreationExit = useCallback(
    (destination: CreationDestination) => {
      if (destination.kind === 'settings') {
        setView('overview');
        openSettings();
        return;
      }
      const base = shellStateRef.current ?? defaultShellPrefs();
      const activeFeatureId = destination.kind === 'overview' ? null : destination.featureId;
      persist({ ...base, activeFeatureId });
      setSettingsOpen(false);
      setView('overview');
      if (destination.kind === 'overview') {
        requestAnimationFrame(() => newFeatureButtonRef.current?.focus());
      }
    },
    [openSettings, persist],
  );
  const creationGuard = useCreationGuard(completeCreationExit);

  const closeAttentionPreview = useCallback(() => setAttentionPreviewRequest(null), []);

  useEffect(() => {
    if (shell === null || routeRequest === null) return;
    if (handledRouteRequest.current === routeRequest.id) return;
    handledRouteRequest.current = routeRequest.id;
    if (routeRequest.event.target === 'home') {
      if (view === 'create') {
        creationGuard.leave({ kind: 'overview' });
      } else {
        selectOverview();
      }
    } else if (routeRequest.event.target === 'settings') {
      if (view === 'create') {
        creationGuard.leave({ kind: 'settings' });
      } else {
        openSettings();
      }
    } else if (routeRequest.event.target === 'bulk') {
      setBulkPreviewRequest(routeRequest.id);
      if (view === 'create') {
        creationGuard.leave({ kind: 'overview' });
      } else {
        selectOverview();
      }
    }
  }, [creationGuard, openSettings, routeRequest, selectOverview, shell, view]);

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
  overviewActiveRef.current = selection.kind === 'overview' && view !== 'create' && !settingsOpen;

  const navigateOverview = () => {
    if (view === 'create') {
      creationGuard.leave({ kind: 'overview' });
      return;
    }
    selectOverview();
  };

  const navigateFeature = (featureId: string) => {
    if (view === 'create') {
      creationGuard.leave({ kind: 'feature', featureId });
      return;
    }
    selectFeature(featureId);
  };

  const toggleLane = (lane: Lane, expanded: boolean) => {
    setExpandedLanes((current) => ({ ...current, [lane]: expanded }));
  };

  const features = list.phase === 'loaded' ? list.features : [];
  const laneGroups = reclassifyWithPendingAttention(
    classifyFeaturesByLane(features),
    attentionByFeature,
  );
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

  // A visual-only auto-collapse: never persisted, and an explicit user
  // choice (persisted `true`) always wins over the breakpoint once one
  // exists — see the module doc comment above `isNarrowForSidebar`.
  const effectiveSidebarCollapsed = shell.sidebarCollapsed || isNarrowForSidebar;

  const selectedFeature =
    selection.kind === 'feature'
      ? features.find((feature) => feature.id === selection.featureId)
      : undefined;
  const showTrailingToolbar = !settingsOpen && selection.kind === 'feature';
  const toolbarTitle = settingsOpen
    ? 'Settings'
    : selection.kind === 'feature'
      ? featureLabel(selection.featureId)
      : 'Overview';
  const toolbarSubline =
    !settingsOpen && selection.kind === 'feature' ? repoBranchSubline(selectedFeature) : undefined;

  // Keep the global ⌘2-9/⌘⌃S listener's stale-closure guard current every
  // render — see the ref's declaration above for why it isn't itself a hook.
  shortcutRef.current = { allRows, navigateFeature, toggleSidebar };

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
      navigateOverview();
    } else {
      navigateFeature(target.id.slice('sidebar-row-'.length));
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
            selected={!settingsOpen && selection.kind === 'overview'}
            onSelect={navigateOverview}
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
                      selected={
                        !settingsOpen &&
                        selection.kind === 'feature' &&
                        selection.featureId === feature.id
                      }
                      onSelect={() => navigateFeature(feature.id)}
                    />
                  ))}
                </div>
              </details>
            );
          })}
        </div>
        <div className="sidebar__footer" data-tone={runtimeTone}>
          <span className="sidebar__runtime" role="status">
            <span aria-hidden="true">●</span> {runtimeLabel}
          </span>
          {/* ⌥Space is Phase 9's shortcut; ⌘⇧M is what actually opens AMA today. */}
          <button type="button" className="sidebar__ama" onClick={onOpenAma}>
            Ask ⌘⇧M
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
          overflowSlotRef={setOverflowSlot}
          inspectorSlotRef={setInspectorSlot}
        />
        <UpdateNotice
          update={updateState}
          dismissedVersion={updateDismissedVersion}
          scheduling={schedulingUpdate}
          onDismiss={onDismissUpdate}
          onOpenSettings={onOpenUpdatesSettings}
          onInstallWhenIdle={onInstallUpdateWhenIdle}
        />
        <div
          className={
            !settingsOpen && selection.kind === 'feature'
              ? 'content-pane content-pane--flush'
              : 'content-pane'
          }
        >
          {settingsOpen ? (
            <div className="content-pane__settings">
              <header className="content-pane__settings-header">
                <button type="button" className="setup-wizard__action" onClick={closeSettings}>
                  Back
                </button>
              </header>
              <SettingsPanel
                routeRequest={routeRequest?.event.target === 'settings' ? routeRequest : null}
              />
            </div>
          ) : selection.kind === 'feature' ? (
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
              overflowMenuHost={overflowSlot}
              inspectorToggleHost={inspectorSlot}
            />
          ) : view === 'create' ? (
            <CreationFlow
              guard={creationGuard}
              onCreated={({ featureId }) => {
                creationGuard.reset();
                loadList();
                selectFeature(featureId);
              }}
            />
          ) : (
            <div className="home-surface">
              <header className="home-surface__header">
                <div>
                  <p className="home-surface__eyebrow">Agentico · Supervised runs</p>
                  <h1>Feature queue</h1>
                  <HomeReadout state={list} />
                </div>
                {list.phase !== 'loaded' || list.features.length > 0 ? (
                  <button
                    ref={newFeatureButtonRef}
                    type="button"
                    className="create-form__submit"
                    onClick={() => setView('create')}
                  >
                    New feature
                  </button>
                ) : null}
              </header>
              <FeatureList
                state={list}
                selectedFeatureId={activeFeatureId}
                attentionByFeature={attentionByFeature}
                onOpen={(featureId) => selectFeature(featureId)}
                onRetry={loadList}
                onCreate={() => setView('create')}
                createButtonRef={newFeatureButtonRef}
              />
              <RecoveryWorkspace onNavigateToFeature={(featureId) => selectFeature(featureId)} />
              <BulkPreviewPanel autoPreviewKey={bulkPreviewRequest} />
            </div>
          )}
        </div>
      </div>
    </section>
  );
}

/**
 * `classifyLane` only sees a feature's own snapshot, which has no top-level
 * "pending attention" field for a standalone feature — the schema only
 * represents attention on an active child relationship
 * (`activeChild.attention`). A feature's own directly-owned attention item
 * (no child pass involved, e.g. a permission prompt or question) is tracked
 * separately in the app-wide attention list, which is exactly what
 * `attentionByFeature` carries. The plan requires "any feature with a
 * pending attention count classifies as Waiting on you regardless of
 * status," so re-bucket with that list here rather than teach the pure
 * classifier about component-level state it was never given.
 */
function reclassifyWithPendingAttention(
  laneGroups: Record<Lane, FeatureSnapshot[]>,
  attentionByFeature: Map<string, number>,
): Record<Lane, FeatureSnapshot[]> {
  if (attentionByFeature.size === 0) return laneGroups;
  const next: Record<Lane, FeatureSnapshot[]> = {
    waiting: [...laneGroups.waiting],
    running: [],
    published: [],
    done: [],
    'at-rest': [],
  };
  for (const lane of LANES) {
    if (lane === 'waiting') continue;
    for (const feature of laneGroups[lane]) {
      if ((attentionByFeature.get(feature.id) ?? 0) > 0) {
        next.waiting.push(feature);
      } else {
        next[lane].push(feature);
      }
    }
  }
  return next;
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

interface CreationGuard {
  leave(destination?: CreationDestination): void;
  reset(): void;
  onDirtyChange(dirty: boolean): void;
  discardOpen: boolean;
  keepEditing(): void;
  discard(): void;
}

function useCreationGuard(onExit: (destination: CreationDestination) => void): CreationGuard {
  const [dirty, setDirty] = useState(false);
  const [discardOpen, setDiscardOpen] = useState(false);
  const [pendingDestination, setPendingDestination] = useState<CreationDestination | null>(null);

  const reset = useCallback(() => {
    setDirty(false);
    setDiscardOpen(false);
    setPendingDestination(null);
  }, []);
  const leave = useCallback(
    (destination: CreationDestination = { kind: 'overview' }) => {
      if (dirty) {
        setPendingDestination(destination);
        setDiscardOpen(true);
        return;
      }
      reset();
      onExit(destination);
    },
    [dirty, onExit, reset],
  );
  const discard = useCallback(() => {
    const destination = pendingDestination ?? { kind: 'overview' };
    reset();
    onExit(destination);
  }, [onExit, pendingDestination, reset]);

  return {
    leave,
    reset,
    onDirtyChange: setDirty,
    discardOpen,
    keepEditing: () => setDiscardOpen(false),
    discard,
  };
}

function CreationFlow({
  guard,
  onCreated,
}: {
  guard: CreationGuard;
  onCreated(created: { featureId: string; name: string }): void;
}) {
  return (
    <section className="creation-flow" aria-label="New feature flow">
      <header className="creation-flow__header">
        <button type="button" className="setup-wizard__action" onClick={() => guard.leave()}>
          Back to Overview
        </button>
        <p>Feature definition</p>
      </header>
      <CreateFeatureForm onDirtyChange={guard.onDirtyChange} onCreated={onCreated} />
      {guard.discardOpen ? (
        <div className="impact-dialog__backdrop">
          <div
            className="impact-dialog"
            role="dialog"
            aria-modal="true"
            aria-label="Discard feature draft"
          >
            <h2>Discard this feature draft?</h2>
            <p>Your entered feature details have not been created.</p>
            <div className="impact-dialog__actions">
              <button type="button" onClick={guard.keepEditing}>
                Keep editing
              </button>
              <button type="button" className="cockpit__stop" onClick={guard.discard}>
                Discard draft
              </button>
            </div>
          </div>
        </div>
      ) : null}
    </section>
  );
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
    const phase = feature.currentPhase;
    if (phase === undefined || phase === '') return undefined;
    return feature.currentIteration !== undefined
      ? `${phase} · ${feature.currentIteration}`
      : phase;
  }
  if (lane === 'at-rest') {
    return displayStatusLabel(feature.status);
  }
  // published / done: name + glyph only, no sub-line.
  return undefined;
}

interface FeatureListProps {
  state: ListState;
  selectedFeatureId: string | null;
  attentionByFeature: ReadonlyMap<string, number>;
  onOpen(featureId: string): void;
  onRetry(): void;
  onCreate(): void;
  createButtonRef: RefObject<HTMLButtonElement | null>;
}

/** The masthead run tally: active, waiting-on-you, and shipped counts. */
function HomeReadout({ state }: { state: ListState }) {
  if (state.phase !== 'loaded' || state.features.length === 0) return null;
  let running = 0;
  let waiting = 0;
  let shipped = 0;
  for (const feature of state.features) {
    const group = dashboardGroupId(feature);
    if (group === 'published' || group === 'done') {
      shipped += 1;
      continue;
    }
    const { bucket } = dashboardState(feature);
    if (bucket === 'active') running += 1;
    else if (bucket === 'intervention') waiting += 1;
  }
  return (
    <p className="home-surface__readout">
      <b>{running}</b> running<span aria-hidden="true"> · </span>
      <b>{waiting}</b> waiting on you<span aria-hidden="true"> · </span>
      <b>{shipped}</b> shipped
    </p>
  );
}

function FeatureList({
  state,
  selectedFeatureId,
  attentionByFeature,
  onOpen,
  onRetry,
  onCreate,
  createButtonRef,
}: FeatureListProps) {
  return (
    <section className="feature-list" aria-label="Existing features">
      {state.phase === 'loading' ? (
        <p role="status" className="setup-step__empty">
          Loading features…
        </p>
      ) : state.phase === 'error' ? (
        <div className="feature-list__error">
          <p className="form-field__error">
            {state.error.code}: {state.error.message}
          </p>
          <button type="button" className="setup-wizard__action" onClick={onRetry}>
            Try again
          </button>
        </div>
      ) : state.features.length === 0 ? (
        <div className="feature-list__empty">
          <p className="home-surface__eyebrow">Runtime ready · workspace clear</p>
          <h3>Turn a goal into a supervised run.</h3>
          <p>
            Define the work, choose its repositories, set the pipeline, then review the exact run
            contract before anything is created.
          </p>
          <button
            ref={createButtonRef}
            type="button"
            className="setup-wizard__action"
            onClick={onCreate}
          >
            New feature
          </button>
        </div>
      ) : (
        <div className="feature-list__groups">
          {groupDashboardFeatures(state.features).map((group) => (
            <section
              key={group.id}
              className="feature-list__group"
              aria-labelledby={`feature-list-group-${group.id}`}
            >
              <div className="feature-list__group-head" data-kind={group.id}>
                <h3 id={`feature-list-group-${group.id}`} className="feature-list__group-title">
                  {group.label}
                </h3>
                <span className="feature-list__group-count" aria-hidden="true">
                  {group.id === 'in-progress'
                    ? `· ${group.features.length}`
                    : `× ${group.features.length}`}
                </span>
              </div>
              {group.id === 'in-progress' ? (
                <ul className="run-grid">
                  {group.features.map((feature) => (
                    <RunningCard
                      key={feature.id}
                      feature={feature}
                      isOpen={selectedFeatureId === feature.id}
                      attentionCount={attentionByFeature.get(feature.id) ?? 0}
                      onOpen={onOpen}
                    />
                  ))}
                </ul>
              ) : (
                <ul className="queue-rows">
                  {group.features.map((feature) => (
                    <QueueRow
                      key={feature.id}
                      feature={feature}
                      isOpen={selectedFeatureId === feature.id}
                      onOpen={onOpen}
                    />
                  ))}
                </ul>
              )}
            </section>
          ))}
        </div>
      )}
    </section>
  );
}

interface RunRowProps {
  feature: FeatureSnapshot;
  isOpen: boolean;
  onOpen(featureId: string): void;
}

/** A feature still in flight: full phase rail, live/needs-you badge, one action. */
function RunningCard({
  feature,
  isOpen,
  attentionCount,
  onOpen,
}: RunRowProps & { attentionCount: number }) {
  const stages = spineStages(feature.pipeline);
  const activeIndex = spineActiveIndex(feature, stages);
  const { bucket, label } = dashboardState(feature);
  const needsYou = attentionCount > 0 || bucket === 'intervention';
  const railTone = needsYou ? 'attention' : 'progress';
  // Amber when a run wants you, blue pulse while it's genuinely working, quiet
  // otherwise — so a resting "Code ready" run never reads as live.
  const badgeState = needsYou ? 'attention' : bucket === 'active' ? 'live' : 'quiet';
  // Name the specific reason a parked run needs you (Interrupted / Failed /
  // Review needed / Input needed); a live run with a pending prompt just says
  // "Needs you"; otherwise Live or the resting state.
  const badge =
    bucket === 'intervention'
      ? label
      : needsYou
        ? 'Needs you'
        : bucket === 'active'
          ? label === 'Active'
            ? 'Live'
            : label
          : label;
  const elapsed = formatElapsed(feature);
  return (
    <li className="run-card" data-state={badgeState}>
      <div className="run-card__head">
        <h4 className="run-card__title">{feature.name}</h4>
        <span className="run-card__badges">
          <AttentionBadge count={attentionCount} label={`Blocking input for ${feature.name}`} />
          <span className="run-card__badge">
            <span className="run-card__dot" aria-hidden="true" />
            {badge}
          </span>
        </span>
      </div>
      <p className="run-card__meta">
        <span>
          repo <b>{feature.repos.join(', ')}</b>
        </span>
        <span>
          status{' '}
          <b>
            {displayStatusLabel(feature.status)}
            {feature.activeChild !== undefined ? ' · locked' : ''}
          </b>
        </span>
        {feature.activeChild === undefined && elapsed !== null ? (
          <span>
            elapsed <b>{elapsed}</b>
          </span>
        ) : null}
      </p>
      {feature.activeChild !== undefined ? (
        // The pass is what is moving; the parent's at-rest rail would read as
        // a finished run inside the In-progress section.
        <PassLane child={feature.activeChild} tone={railTone} />
      ) : (
        <FlightRail
          stages={stages}
          activeIndex={activeIndex}
          atRest={isRunAtRest(feature.status)}
          tone={railTone}
          label={`Pipeline for ${feature.name}`}
        />
      )}
      {feature.failure?.message !== undefined ? (
        <p className="run-card__failure">
          <span aria-hidden="true">! </span>
          {feature.failure.message}
        </p>
      ) : null}
      <div className="run-card__actions">
        <button type="button" className="run-card__action" onClick={() => onOpen(feature.id)}>
          {isOpen ? 'Show' : 'Open'}
        </button>
      </div>
    </li>
  );
}

/**
 * The nested refactor-pass lane shows the active child with its own live rail.
 * The needle is derived from the pass's status;
 * a Created pass shows the rail with no needle and every stop upcoming, and
 * statuses that don't name a phase (paused, waiting, failed) show no rail
 * rather than an approximate needle.
 */
function PassLane({
  child,
  tone,
}: {
  child: NonNullable<FeatureSnapshot['activeChild']>;
  tone: 'progress' | 'attention';
}) {
  const stages = spineStages(child.pipeline);
  const index = childStatusSpineIndex(child.status, stages);
  return (
    <div className="run-card__pass" data-tone={tone}>
      <span className="run-card__pass-glyph" aria-hidden="true">
        ↳
      </span>
      <div className="run-card__pass-body">
        <p className="run-card__pass-line">
          <b>{child.name}</b>
          <span>
            {child.status === 'Created' ? 'Not started' : displayStatusLabel(child.status)}
          </span>
        </p>
        {index === null ? null : (
          <FlightRail
            stages={stages}
            activeIndex={index}
            atRest={false}
            tone={tone}
            label={`Pass pipeline for ${child.name}`}
          />
        )}
      </div>
    </div>
  );
}

/** A shipped feature: compact row with an all-done rail and one action. */
function QueueRow({ feature, isOpen, onOpen }: RunRowProps) {
  const stages = spineStages(feature.pipeline);
  const stateLabel = dashboardGroupId(feature) === 'done' ? 'Done' : 'Shipped';
  return (
    <li className="queue-row">
      <span className="queue-row__name">{feature.name}</span>
      <span className="queue-row__repo">{feature.repos.join(', ')}</span>
      <span className="queue-row__rail" aria-hidden="true">
        {stages.map((stage) => (
          <i key={stage.id} className="queue-row__pip" />
        ))}
      </span>
      <span className="queue-row__state">{stateLabel}</span>
      <button type="button" className="queue-row__action" onClick={() => onOpen(feature.id)}>
        {isOpen ? 'Show' : 'Open'}
      </button>
    </li>
  );
}

/**
 * The signature: a horizontal pipeline rail with a filled track to the active
 * stop, a pulsing needle on the current stage, and condensed stage labels.
 */
function FlightRail({
  stages,
  activeIndex,
  atRest,
  tone,
  label,
}: {
  stages: readonly SpineStage[];
  activeIndex: number;
  atRest: boolean;
  tone: 'progress' | 'attention';
  label: string;
}) {
  const reducedMotion = usePrefersReducedMotion();
  const denom = Math.max(stages.length - 1, 1);
  // activeIndex -1 = not started: zero fill, every stop upcoming, no needle.
  const fillPct = (Math.min(Math.max(activeIndex, 0), denom) / denom) * 100;
  return (
    <div
      className="flight-rail"
      data-tone={tone}
      role="group"
      aria-label={label}
      style={{ '--stops': stages.length } as CSSProperties}
    >
      <div className="flight-rail__track">
        <span className="flight-rail__fill" style={{ width: `${fillPct}%` }} />
      </div>
      <div className="flight-rail__stops">
        {stages.map((stage, index) => {
          const state =
            index < activeIndex || (atRest && index === activeIndex)
              ? 'done'
              : index === activeIndex
                ? 'active'
                : 'upcoming';
          const isActive = state === 'active';
          return (
            <span
              key={stage.id}
              className="flight-rail__stop"
              data-state={state}
              data-tone={tone}
              {...(isActive ? { 'aria-current': 'step' as const } : {})}
            >
              <span
                className={
                  isActive && !reducedMotion
                    ? 'flight-rail__stop-dot flight-rail__stop-dot--pulse'
                    : 'flight-rail__stop-dot'
                }
                aria-hidden="true"
              />
              <span className="flight-rail__stop-label" title={stage.label}>
                {railStageLabel(stage.label, stages.length)}
              </span>
            </span>
          );
        })}
      </div>
    </div>
  );
}

function AttentionBadge({ count, label }: { count: number; label: string }) {
  if (count === 0) return null;
  return (
    <span className="attention-badge" role="status" aria-label={`${label}: ${count} pending`}>
      {count}
    </span>
  );
}
