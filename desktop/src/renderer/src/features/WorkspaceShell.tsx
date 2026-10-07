/*
Copyright 2026 DoorDash, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

/**
 * The readiness-gated main surface: a translucent Bench sidebar — the pinned
 * Supervisor row plus five lane-grouped sections of every feature — with
 * exactly one content pane mounted at a time: the Supervisor page (home) or
 * one feature's cockpit. Feature creation descends over that pane as a
 * window-modal sheet reached from the toolbar's "New feature" on every page —
 * the pane beneath stays mounted and navigable, so ⌘-digit shortcuts, routed
 * navigation, and attention deep-links change what is underneath without
 * touching the draft.
 * The Recovery sheet descends the same way from every page: it stacks the
 * recovery workspace above bulk resume/retry and is reached from the
 * toolbar, the Recovery and Bulk Resume / Retry commands, and the attention
 * inbox's recovery jump.
 * Settings is not a state of this shell at all: it lives in its own window,
 * so every settings entry path is handled in the main process and nothing
 * here has a settings special case.
 * Local settings store ONLY the active feature id and sidebar collapse
 * state; every feature itself is always reloaded from the server, so
 * existing state survives app restarts without any local domain cache.
 * "Home" is "no feature selected": a null persisted feature for the server
 * renders the Supervisor page, so fresh installs, relaunches, server
 * switches with no recorded selection, feature close and feature delete all
 * land there, and choosing Supervisor clears the persisted feature.
 */
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type Dispatch,
  type SetStateAction,
} from 'react';
import type {
  AttentionItem,
  FeatureActionView,
  FeatureSnapshot,
  FeatureSummaryView,
  MainWindowUiState,
  RoutedRequest,
  ShellPrefs,
  UpdateState,
} from '../../../shared/ipc';
import {
  applyShellPatch,
  attentionOwnerFeatureId,
  DEFAULT_RUNTIME_ID,
  defaultShellPrefs,
  isConnectionErrorState,
  sameMainWindowUiState,
  type ShellPatch,
} from '../../../shared/ipc';
import type { CanonicalError } from '../../../shared/api/parse';
import { featureCommandEnablement, isFeatureCommandId } from '../../../shared/commands';
import { runFeatureCommand, toggleActiveInspector } from './featureCommands';
import { parseIpcError } from '../wizard/ipcError';
import { ErrorSurface } from '../components/ErrorSurface';
import { isEditingShortcutTarget } from '../components/CommandPalette';
import { SidebarResizeHandle } from './SidebarResizeHandle';
import { CreateFeatureForm } from './CreateFeatureForm';
import { useCreationDrafts, useCreationDraftEntry } from './creationDrafts';
import { FeatureCockpit } from './FeatureCockpit';
import { PipRail } from '../components/Pip';
import { SupervisorIcon } from '../components/icons';
import { updateNoticePending } from '../components/UpdatePopover';
import {
  emptyAttentionDrafts,
  SUPERVISOR_ATTENTION_ROUTE,
  type AttentionDrafts,
} from './AttentionInbox';
import { SupervisorPage, type SupervisorComposeRequest } from './supervisor/SupervisorPage';
import { SidebarChromeControls } from './SidebarChromeControls';
import { ServerSwitcher } from '../components/ServerSwitcher';
import { Toolbar } from './Toolbar';
import {
  childStatusSpineIndex,
  displayStatusLabel,
  highestSeverityError,
  isRunAtRest,
  orderDashboardFeatures,
  runningPhaseSubline,
  spineActiveIndex,
  spineStages,
} from './featureView';
import {
  LANES,
  classifyFeaturesByLaneWithAttention,
  classifyLane,
  laneLabel,
  type Lane,
} from './laneClassification';
import { RecoverySheet } from './RecoverySheet';
import { retryAction, useConnectionState, useMediaQuery, type LoadState } from '../hooks';

type ListState = LoadState<{ phase: 'loaded'; features: FeatureSnapshot[] }>;

type Selection = { kind: 'supervisor' } | { kind: 'feature'; featureId: string };

/**
 * How many features one list load may fetch detail for. Detail responses cost
 * server-side git freshness probes per repository, so the list must never fan
 * out one per feature: only the selection and the active rows whose sub-line
 * and pip read detail-only fields are refined, and never more than this many.
 */
const MAX_DETAIL_FETCHES = 8;

/**
 * Widens a list summary into the snapshot shape the lane, row, and pip helpers
 * read. Fields the summary DTO does not carry (pipeline profile, roadmap and
 * iteration counters, durable setup, the action catalogue) stay absent rather
 * than invented: a row renders one grade coarser until its detail arrives.
 */
function snapshotFromSummary(summary: FeatureSummaryView): FeatureSnapshot {
  return {
    id: summary.id,
    name: summary.name,
    // Absent from the summary DTO and never read from a list row.
    slug: '',
    status: summary.status,
    currentPhase: summary.currentPhase,
    repos: summary.repos,
    createdAt: summary.createdAt,
    activeRun: summary.activeRun,
    actions: [],
    reviewGate: {
      reviewingGate: false,
      reviewFixing: false,
      validatingPlan: false,
      validatorStatuses: {},
    },
    automaticReview: { mode: 'default', enabled: false, source: 'global' },
    warnings: summary.warnings,
    // The owned-error projection rides the summary, so lanes classify from
    // the same list the detail route will refine.
    errors: summary.errors,
    ...(summary.phaseStatus === undefined ? {} : { phaseStatus: summary.phaseStatus }),
    ...(summary.activeChild === undefined ? {} : { activeChild: summary.activeChild }),
    ...(summary.childHistory === undefined ? {} : { childHistory: summary.childHistory }),
    ...(summary.childHistoryTotal === undefined
      ? {}
      : { childHistoryTotal: summary.childHistoryTotal }),
    ...(summary.childHistoryTruncated === undefined
      ? {}
      : { childHistoryTruncated: summary.childHistoryTruncated }),
  };
}

/** The bounded detail set: the current selection first, then the active rows. */
function detailFetchIds(
  rows: readonly FeatureSnapshot[],
  activeFeatureId: string | null,
): string[] {
  const ids: string[] = [];
  const add = (id: string): void => {
    if (ids.length < MAX_DETAIL_FETCHES && !ids.includes(id)) ids.push(id);
  };
  if (activeFeatureId !== null && rows.some((row) => row.id === activeFeatureId)) {
    add(activeFeatureId);
  }
  for (const row of rows) {
    const lane = classifyLane(row);
    if (lane === 'waiting' || lane === 'failed' || lane === 'running') add(row.id);
  }
  return ids;
}

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
  onOpenPalette = () => {},
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
  /** Owned by App: dispatches the same 'palette' routeRequest ⌘K resolves to. */
  onOpenPalette?(): void;
}) {
  // null while the local shell prefs are being restored.
  const [sidebarPreviewWidth, setSidebarPreviewWidth] = useState<number | null>(null);
  const [shell, setShell] = useState<ShellPrefs | null>(null);
  // A purely visual auto-collapse below ~700px: it never writes
  // `shell.sidebarCollapsed` (only the toolbar button and ⌘⌃S do that), so
  // widening back past the breakpoint restores whatever the user last chose
  // explicitly instead of fighting a value this hook itself set.
  const isNarrowForSidebar = useMediaQuery('(max-width: 700px)');
  const connection = useConnectionState();
  const runtimeReady = connection.status === 'ready';
  // Per-server scoping: the active-feature selection is keyed on the
  // connected server's identity, so switching servers restores each server's
  // own selection. The default scope only covers a ready state older than
  // the serverKey field.
  const scopeKey = connection.serverKey ?? DEFAULT_RUNTIME_ID;
  const scopeKeyRef = useRef(scopeKey);
  scopeKeyRef.current = scopeKey;
  // A named server introduces itself by name; a name-less (older) server
  // keeps the generic ready label. Display-only — the footer row still owns
  // exactly one interactive element.
  const runtimeLabel = runtimeReady
    ? (connection.serverName ?? 'Runtime ready')
    : isConnectionErrorState(connection)
      ? 'Runtime needs attention'
      : 'Connecting';
  const runtimeTone = runtimeReady
    ? 'ready'
    : isConnectionErrorState(connection)
      ? 'error'
      : 'progress';
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
  // A routed 'supervisor' request waits here until the Supervisor page —
  // which may only mount after the selection switches to it — consumes it;
  // clearing it then keeps a later remount from replaying the draft.
  const [supervisorCompose, setSupervisorCompose] = useState<SupervisorComposeRequest | null>(null);
  const clearSupervisorCompose = useCallback(() => setSupervisorCompose(null), []);
  // Route-and-focus signal for the footer's server switcher (the menu and
  // palette "Switch Server…" command lands here).
  const [switcherRoute, setSwitcherRoute] = useState<{ id: number } | null>(null);
  // Cleared when the switcher has consumed a route: the control moves
  // between two homes across the collapse breakpoint, and a kept id would
  // reopen the popover on the remount.
  const clearSwitcherRoute = useCallback(() => setSwitcherRoute(null), []);
  const listRequestRef = useRef(0);
  // The creation sheet's open flag and retained draft live per server in a
  // store that survives this shell's unmount (a disconnect or a server
  // switch unmounts the whole ready tree; the store lives above it in App).
  const creationDrafts = useCreationDrafts();
  const creationEntry = useCreationDraftEntry(creationDrafts, scopeKey);
  const creationOpen = creationEntry?.open ?? false;
  const openCreation = useCallback(() => {
    creationDrafts.openDraft(scopeKey);
  }, [creationDrafts, scopeKey]);
  const closeCreation = useCallback(() => {
    // Explicit discard retires the server's draft and its associations.
    creationDrafts.retire(scopeKey);
  }, [creationDrafts, scopeKey]);
  // The cockpit-owned half of the native menu's summary: the live action
  // catalogue behind every feature verb, and the unpersisted inspector state
  // behind the View menu's Show/Hide label.
  const [cockpitUi, setCockpitUi] = useState<{
    featureId: string;
    actions: readonly FeatureActionView[] | null;
    inspectorOpen: boolean;
  } | null>(null);
  const pushedUiStateRef = useRef<MainWindowUiState | null>(null);
  // The Recovery sheet, null while closed. Closing unmounts it, so every open
  // is a fresh sheet: Recovery auto-scans once per open, and a bulk-routed
  // open's preview request (`bulkPreviewKey`) dies with that instance.
  const [recoverySheet, setRecoverySheet] = useState<{ bulkPreviewKey: number | null } | null>(
    null,
  );
  /**
   * A plain open (`null`) keeps an already-open sheet as it is; a bulk-routed
   * open passes its route id, which loads the preview on open — or reloads
   * it when the sheet is already showing, since ⌘⇧B is an explicit ask.
   */
  const openRecoverySheet = useCallback((bulkPreviewKey: number | null) => {
    setRecoverySheet((current) =>
      current !== null && bulkPreviewKey === null ? current : { bulkPreviewKey },
    );
  }, []);
  const closeRecoverySheet = useCallback(() => setRecoverySheet(null), []);
  const [selectedRuns, setSelectedRuns] = useState<Record<string, number | null>>({});
  const [creationWarnings, setCreationWarnings] = useState<
    Record<string, readonly CanonicalError[]>
  >({});
  const [expandedLanes, setExpandedLanes] = useState<Record<Lane, boolean>>({
    failed: true,
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
    featureOrder: string[];
    navigateFeature(featureId: string): void;
    toggleSidebar(): void;
  }>({ featureOrder: [], navigateFeature: () => {}, toggleSidebar: () => {} });

  // ⌘2-9: the 1st-8th feature by absolute sidebar position, counting across
  // every lane regardless of its disclosure state (unlike Arrow/Home/End,
  // which only ever land on a visible row); the pinned Supervisor row is not
  // numbered. ⌘1 — Supervisor — stays entirely on the native-menu →
  // routeRequest("home") path.
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
        openCreation();
        return;
      }
      if (commandKey && !event.shiftKey && !event.altKey && /^[2-9]$/.test(event.key)) {
        const featureId = shortcutRef.current.featureOrder[Number(event.key) - 2];
        if (featureId === undefined) return;
        event.preventDefault();
        shortcutRef.current.navigateFeature(featureId);
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

  /**
   * Refines a bounded few sidebar rows with their server detail — the
   * selection first, then the active rows whose sub-line and pip read
   * detail-only fields. Settles per feature: a rejected detail leaves that
   * row rendering from its summary, one grade coarser, and every other row
   * still refines.
   */
  const refineDetails = useCallback(
    async (rows: readonly FeatureSnapshot[], isCurrent: () => boolean) => {
      const ids = detailFetchIds(
        rows,
        shellStateRef.current?.featureByServer[scopeKeyRef.current] ?? null,
      );
      if (ids.length === 0) return;
      const settled = await Promise.allSettled(ids.map((id) => window.agentico.getFeature(id)));
      if (!isCurrent()) return;
      const details = new Map<string, FeatureSnapshot>();
      ids.forEach((id, index) => {
        const result = settled[index];
        if (result?.status === 'fulfilled') details.set(id, result.value);
      });
      if (details.size === 0) return;
      setList((current) =>
        current.phase !== 'loaded'
          ? current
          : {
              phase: 'loaded',
              features: orderDashboardFeatures(
                current.features.map((feature) => details.get(feature.id) ?? feature),
              ),
            },
      );
    },
    [],
  );

  /**
   * The sidebar renders from the list summaries alone, so every row stays
   * alive even when a single feature's detail is unusable.
   */
  const loadList = useCallback(() => {
    const request = ++listRequestRef.current;
    const isCurrent = () => request === listRequestRef.current;
    window.agentico.listFeatures().then(
      (list) => {
        if (!isCurrent()) return;
        const rows = orderDashboardFeatures(list.features.map(snapshotFromSummary));
        setList({ phase: 'loaded', features: rows });
        void refineDetails(rows, isCurrent).catch(() => {
          // Refinement is additive; the summary-derived rows already render.
        });
      },
      (err: unknown) => {
        // Only a failed LIST is fatal: there is nothing to render without it.
        if (isCurrent()) setList({ phase: 'error', error: parseIpcError(err) });
      },
    );
  }, [refineDetails]);

  // The sidebar's feature list follows the authoritative server state: fetch
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

  /** Persist failures never block the UI — the shell selection is presentation only. */
  const persistPatch = useCallback((patch: ShellPatch) => {
    const base = shellStateRef.current ?? defaultShellPrefs();
    const next = applyShellPatch(base, patch);
    shellStateRef.current = next;
    setShell(next);
    const write = () =>
      window.agentico
        .updateSettings({ shell: patch })
        .then(() => undefined)
        .catch(() => {
          // The server-side feature state is unaffected; keep later writes moving.
        });
    shellPersistenceRef.current = shellPersistenceRef.current.then(write, write);
  }, []);

  const selectFeature = useCallback(
    (featureId: string) => {
      persistPatch({ setActiveFeature: { serverKey: scopeKey, featureId } });
    },
    [persistPatch, scopeKey],
  );

  /**
   * The sidebar header's chrome-cluster toggle (the toolbar's leading zone
   * hosts it while collapsed): click-to-collapse/expand, persisted
   * through the same settings IPC as the selection. The ⌘⌃S handler above
   * calls this same function; the visual-only auto-collapse below ~700px is
   * `effectiveSidebarCollapsed`, computed above the restore gate below.
   */
  const toggleSidebar = useCallback(() => {
    const base = shellStateRef.current ?? shell ?? defaultShellPrefs();
    persistPatch({ sidebarCollapsed: !base.sidebarCollapsed });
  }, [persistPatch, shell]);

  /**
   * Goes home: the Supervisor page is "no feature selected", so selecting it
   * is clearing the persisted feature — written only when one is recorded.
   */
  const selectSupervisor = useCallback(() => {
    if ((shellStateRef.current?.featureByServer[scopeKey] ?? null) !== null) {
      persistPatch({ setActiveFeature: { serverKey: scopeKey, featureId: null } });
    }
  }, [persistPatch, scopeKey]);

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
      selectSupervisor();
      // One authoritative refetch; the optimistic filter above already hides the row.
      loadList();
    },
    [loadList, selectSupervisor],
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
        ({
          permission: 0,
          questions: 0,
          help: 0,
          gate: 0,
          review: 0,
          recovery: 0,
          error: 0,
        } as Record<AttentionItem['kind'], number>);
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
      // Recovery opens over whatever page is showing; the selection stays.
      openRecoverySheet(null);
    } else if (attentionJump.featureId === SUPERVISOR_ATTENTION_ROUTE) {
      selectSupervisor();
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
  }, [
    attentionJump,
    onAttentionJumpHandled,
    openRecoverySheet,
    selectFeature,
    selectSupervisor,
    shell,
  ]);

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
    const activeFeatureId = shell.featureByServer[scopeKey] ?? null;
    const cockpitMatches = cockpitUi !== null && cockpitUi.featureId === activeFeatureId;
    return {
      activeFeatureId,
      runtimeReady,
      sidebarCollapsed: effectiveSidebarCollapsed,
      inspectorOpen: cockpitMatches ? cockpitUi.inspectorOpen : false,
      // The Supervisor page has nothing to inspect, so there is no toggle to offer.
      inspectorAvailable: activeFeatureId !== null,
      featureCommands: featureCommandEnablement(cockpitMatches ? cockpitUi.actions : null, {
        hasSelection: activeFeatureId !== null,
      }),
    };
  }, [cockpitUi, effectiveSidebarCollapsed, runtimeReady, scopeKey, shell]);

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
      selectSupervisor();
    } else if (routeRequest.event.target === 'supervisor') {
      selectSupervisor();
      const { draft, errorReference } = routeRequest.event;
      setSupervisorCompose({
        id: routeRequest.id,
        ...(draft === undefined ? {} : { draft }),
        ...(errorReference === undefined ? {} : { errorReference }),
      });
    } else if (routeRequest.event.target === 'recovery') {
      openRecoverySheet(null);
    } else if (routeRequest.event.target === 'bulk') {
      openRecoverySheet(routeRequest.id);
    } else if (routeRequest.event.target === 'new-feature') {
      openCreation();
    } else if (routeRequest.event.target === 'toggle-sidebar') {
      shortcutRef.current.toggleSidebar();
    } else if (routeRequest.event.target === 'toggle-inspector') {
      toggleActiveInspector();
    } else if (routeRequest.event.target === 'select-feature') {
      const featureId = routeRequest.event.featureId;
      if (featureId !== undefined) {
        selectFeature(featureId);
      }
    } else if (routeRequest.event.target === 'switch-server') {
      setSwitcherRoute({ id: routeRequest.id });
    } else if (routeRequest.event.target === 'feature-command') {
      // The route carries only the command's identity; the funnel resolves the
      // target from the live selection and re-checks its live enablement, so a
      // click that raced a selection change is a no-op.
      const command = routeRequest.event.command;
      if (command !== undefined && isFeatureCommandId(command)) {
        runFeatureCommand(command);
      }
    }
  }, [openRecoverySheet, routeRequest, selectFeature, selectSupervisor, shell]);

  if (shell === null) {
    return (
      <section className="shell-card workspace" aria-label="Workspace">
        <p role="status" aria-live="polite" className="cockpit__loading">
          Restoring workspace…
        </p>
      </section>
    );
  }

  const activeFeatureId = shell.featureByServer[scopeKey] ?? null;
  // The cockpit always reloads its own snapshot directly by id, so a
  // persisted selection is trusted even if the summary list hasn't returned
  // it yet (or ever) — the cockpit itself renders the "no longer exists"
  // state when the server truly has nothing under that id.
  const selection: Selection =
    activeFeatureId !== null
      ? { kind: 'feature', featureId: activeFeatureId }
      : { kind: 'supervisor' };

  const toggleLane = (lane: Lane, expanded: boolean) => {
    setExpandedLanes((current) => ({ ...current, [lane]: expanded }));
  };

  const features = list.phase === 'loaded' ? list.features : [];
  const laneGroups = classifyFeaturesByLaneWithAttention(features, attentionByFeature);
  const counts = Object.fromEntries(LANES.map((lane) => [lane, laneGroups[lane].length])) as Record<
    Lane,
    number
  >;

  // The absolute feature order ⌘2-9 count by: every lane in display order,
  // every feature within it — regardless of which lanes are currently
  // expanded. (Arrow/Home/End instead walk the DOM directly, at click time,
  // so they only ever land on what a `<details>` disclosure state is
  // actually showing, the Supervisor row included.)
  const featureOrder = LANES.flatMap((lane) => laneGroups[lane].map((feature) => feature.id));

  const selectedFeature =
    selection.kind === 'feature'
      ? features.find((feature) => feature.id === selection.featureId)
      : undefined;
  const showTrailingToolbar = selection.kind === 'feature';
  const toolbarTitle =
    selection.kind === 'feature' ? featureLabel(selection.featureId) : 'Supervisor';
  const toolbarSubline =
    selection.kind === 'feature' ? repoBranchSubline(selectedFeature) : undefined;

  // Keep the global ⌘2-9/⌘⌃S listener's stale-closure guard current every
  // render — see the ref's declaration above for why it isn't itself a hook.
  shortcutRef.current = { featureOrder, navigateFeature: selectFeature, toggleSidebar };

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
    if (target.id === 'sidebar-supervisor') {
      selectSupervisor();
    } else {
      selectFeature(target.id.slice('sidebar-row-'.length));
    }
  };

  // The Claude-Desktop-style window-chrome cluster: exactly one instance,
  // moved between two homes rather than rendered twice. While the sidebar is
  // visible it lives in the sidebar header (right of the traffic lights);
  // once the sidebar collapses that header disappears with it, so the same
  // cluster moves into the toolbar's leading zone, which then carries the
  // traffic-light clearance — the buttons never leave the top-left corner.
  const chromeControls = (
    <SidebarChromeControls
      sidebarCollapsed={effectiveSidebarCollapsed}
      onToggleSidebar={toggleSidebar}
      onOpenPalette={onOpenPalette}
    />
  );

  /**
   * One ServerSwitcher instance, homed where it stays visible: the sidebar
   * footer while the sidebar shows, a dock under the toolbar once the
   * sidebar is collapsed (a narrow window's auto-collapse or the user's
   * explicit choice hides the whole footer — and with it the only landing
   * spot for the routed "Switch Server…" command — otherwise).
   */
  const serverSwitcher = runtimeReady ? (
    <ServerSwitcher
      currentLabel={runtimeLabel}
      tone={runtimeTone}
      enabled
      openRequest={switcherRoute}
      onRouteHandled={clearSwitcherRoute}
    />
  ) : null;

  return (
    <section
      className="workspace"
      style={
        {
          '--sidebar-width': `${sidebarPreviewWidth ?? shell?.sidebarWidth ?? 260}px`,
        } as CSSProperties
      }
      aria-label="Workspace"
      data-sidebar-collapsed={effectiveSidebarCollapsed}
    >
      <nav
        id="feature-sidebar"
        className="sidebar"
        aria-label="Feature sidebar"
        data-collapsed={effectiveSidebarCollapsed}
      >
        <div className="sidebar__header">{effectiveSidebarCollapsed ? null : chromeControls}</div>
        <div
          className="sidebar__list"
          role="listbox"
          aria-label="Features"
          onKeyDown={onSidebarListKeyDown}
        >
          <SidebarRow
            id="sidebar-supervisor"
            label="Supervisor"
            glyph="supervisor"
            selected={selection.kind === 'supervisor'}
            onSelect={selectSupervisor}
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
        {/* Only a failed LIST is surfaced: the Supervisor page stays usable
         * beneath it, and Retry reloads the list in place. */}
        {list.phase === 'error' ? (
          <div className="sidebar__list-error">
            <ErrorSurface
              error={list.error}
              variant="compact"
              localAction={retryAction(loadList)}
            />
          </div>
        ) : null}
        <div className="sidebar__footer" data-tone={runtimeTone}>
          {/* The runtime pill is the server control while connected; every
           * non-ready state keeps the passive pill. */}
          {!effectiveSidebarCollapsed && serverSwitcher !== null ? (
            serverSwitcher
          ) : (
            <span className="sidebar__runtime" role="status">
              <span aria-hidden="true">●</span> {runtimeLabel}
            </span>
          )}
          {/* Indicator only, never a target: the toolbar button stays the one
           * way into the update popover, and the footer keeps exactly one
           * interactive element. */}
          {updatePending ? (
            <span className="sidebar__update-dot" role="img" aria-label="Update available" />
          ) : null}
        </div>
        {!effectiveSidebarCollapsed && (
          <SidebarResizeHandle
            width={shell?.sidebarWidth ?? 260}
            onPreview={setSidebarPreviewWidth}
            onCommit={(sidebarWidth) => {
              setSidebarPreviewWidth(null);
              persistPatch({ sidebarWidth });
            }}
          />
        )}
      </nav>

      <div className="content-column">
        {/* The collapsed-sidebar home of the switcher above: out of flow so
         * the column's toolbar/content-pane layout never shifts. */}
        {effectiveSidebarCollapsed && serverSwitcher !== null ? (
          <div className="server-switcher-dock">{serverSwitcher}</div>
        ) : null}
        <Toolbar
          leading={effectiveSidebarCollapsed ? chromeControls : undefined}
          title={toolbarTitle}
          subline={toolbarSubline}
          showTrailing={showTrailingToolbar}
          onNewFeature={() => openCreation()}
          newFeatureButtonRef={newFeatureButtonRef}
          recovery={{
            attention: attentionItems.some((item) => item.kind === 'recovery'),
            onOpen: () => openRecoverySheet(null),
          }}
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
            selection.kind === 'feature'
              ? 'content-pane content-pane--flush'
              : 'content-pane content-pane--conversation'
          }
        >
          {selection.kind === 'supervisor' ? (
            <SupervisorPage
              key={scopeKey}
              attentionDrafts={activeAttentionDrafts}
              setAttentionDrafts={updateAttentionDrafts}
              refreshAttention={refreshAttention}
              composeRequest={supervisorCompose}
              onComposeRequestHandled={clearSupervisorCompose}
            />
          ) : (
            <FeatureCockpit
              key={selection.featureId}
              active
              featureId={selection.featureId}
              creationWarnings={creationWarnings[selection.featureId] ?? []}
              titleHint={featureLabel(selection.featureId)}
              onClose={selectSupervisor}
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
          )}
        </div>
      </div>

      {recoverySheet !== null ? (
        <RecoverySheet
          bulkPreviewKey={recoverySheet.bulkPreviewKey}
          onClose={closeRecoverySheet}
          onNavigateToFeature={(featureId) => {
            closeRecoverySheet();
            selectFeature(featureId);
          }}
        />
      ) : null}

      {creationOpen ? (
        <CreateFeatureForm
          key={scopeKey}
          retainedDraft={creationEntry?.state}
          onDraftDetach={(state) => creationDrafts.persist(scopeKey, state)}
          onClose={closeCreation}
          onCreated={({ featureId, warnings }) => {
            // A successful creation retires the draft only after the
            // existing success handling completes.
            if (warnings !== undefined && warnings.length > 0) {
              setCreationWarnings((current) => ({ ...current, [featureId]: warnings }));
            }
            loadList();
            selectFeature(featureId);
            closeCreation();
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
 * "undefined" or a dangling separator. The Supervisor page never calls this.
 */
function repoBranchSubline(feature: FeatureSnapshot | undefined): string | undefined {
  if (feature === undefined) return undefined;
  const [firstRepo, ...restRepos] = feature.repos;
  if (firstRepo === undefined) return undefined;
  const branch = feature.setup?.tasks.find((task) => task.repo === firstRepo)?.branch;
  const base = branch !== undefined && branch !== '' ? `${firstRepo} · ${branch}` : firstRepo;
  return restRepos.length > 0 ? `${base} +${restRepos.length}` : base;
}

/** A single row in the Bench sidebar's listbox: the pinned Supervisor row or a lane member. */
function SidebarRow({
  id,
  label,
  subline,
  glyphTone,
  glyph = 'dot',
  pip,
  selected,
  onSelect,
}: {
  id: string;
  label: string;
  subline?: string;
  glyphTone?: 'danger' | 'attention' | 'progress' | 'ok' | 'quiet';
  /**
   * Feature rows show a status dot; the pinned Supervisor row shows a text
   * bubble instead, since it is a place with no status to report.
   * Decorative either way — the row's accessible name is the label alone.
   */
  glyph?: 'dot' | 'supervisor';
  pip?: {
    stageCount: number;
    activeIndex: number;
    atRest: boolean;
    tone: 'progress' | 'attention' | 'danger';
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
      {glyph === 'supervisor' ? (
        <span className="sidebar__row-glyph sidebar__row-glyph--supervisor" aria-hidden="true">
          <SupervisorIcon />
        </span>
      ) : (
        <span className="sidebar__row-glyph" data-tone={glyphTone ?? 'quiet'} aria-hidden="true" />
      )}
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

const LANE_GLYPH_TONE: Readonly<
  Record<Lane, 'danger' | 'attention' | 'progress' | 'ok' | 'quiet'>
> = {
  failed: 'danger',
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
  const pipInfo =
    lane === 'waiting' || lane === 'failed' || lane === 'running' ? pipRailFor(feature) : null;
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
              tone: lane === 'running' ? 'progress' : lane === 'failed' ? 'danger' : 'attention',
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
 * The row sub-line, entirely lane-scoped: failed rows carry the blocking
 * error's catalog title, waiting rows summarize what needs a person (the
 * prompt cascade first, then the highest-severity error's catalog title,
 * then the status text), running rows name the active pass or the phase and
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
  if (lane === 'failed') {
    return highestSeverityError(feature.errors)?.error.title;
  }
  if (lane === 'waiting') {
    return (
      attentionSummary(attentionKinds) ??
      highestSeverityError(feature.errors)?.error.title ??
      displayStatusLabel(feature.status)
    );
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
