import {
  isConnectionErrorState,
  type AppRouteEvent,
  type AttentionItem,
  type FeatureSummaryView,
  type RoutedRequest,
  type ThemePreference,
  type UpdateState,
} from '../../shared/ipc';
import { canInstallInApp, hasActiveWork, installWhenIdleLabel } from '../../shared/updateState';
import { ConnectionShell } from './components/ConnectionShell';
import { AmaDock } from './components/AmaDock';
import { CommandPalette } from './components/CommandPalette';
import { HelpOverlay } from './components/HelpOverlay';
import { ReadinessGate } from './components/ReadinessGate';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  AttentionInbox,
  emptyAttentionDrafts,
  type AttentionDrafts,
} from './features/AttentionInbox';
import { useConnectionState, useTheme } from './hooks';

const THEME_OPTIONS: readonly { value: ThemePreference; label: string }[] = [
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
  { value: 'system', label: 'System' },
];

export default function App() {
  const { preference, setPreference } = useTheme();
  const connection = useConnectionState();
  const runtimeReady = connection.status === 'ready';
  const [serverAttentionItems, setServerAttentionItems] = useState<AttentionItem[]>([]);
  const [recoveryItems, setRecoveryItems] = useState<{
    liveCount: number;
    deadCount: number;
    firstSeenAt: string;
  } | null>(null);
  const [attentionDrafts, setAttentionDrafts] = useState<AttentionDrafts>(emptyAttentionDrafts);
  const [featureNames, setFeatureNames] = useState<Record<string, string>>({});
  const [attentionJump, setAttentionJump] = useState<{
    requestId: number;
    featureId: string;
    attentionId?: string;
  } | null>(null);
  const [routeRequest, setRouteRequest] = useState<RoutedRequest | null>(null);
  const [updateState, setUpdateState] = useState<UpdateState | null>(null);
  const [updateDismissedVersion, setUpdateDismissedVersion] = useState<string | null>(null);
  const [schedulingUpdate, setSchedulingUpdate] = useState(false);
  const routeSequence = useRef(0);

  const requestRoute = useCallback((event: AppRouteEvent) => {
    routeSequence.current += 1;
    setRouteRequest({ id: routeSequence.current, event });
  }, []);

  useEffect(() => window.agentico.onRouteRequest(requestRoute), [requestRoute]);

  const refreshUpdates = useCallback(async () => {
    try {
      setUpdateState(await window.agentico.getUpdates());
    } catch {
      setUpdateState(null);
    }
  }, []);

  useEffect(() => {
    void refreshUpdates();
    const interval = setInterval(() => void refreshUpdates(), 60000);
    return () => clearInterval(interval);
  }, [refreshUpdates]);

  useEffect(
    () =>
      window.agentico.onAppEvent((event) => {
        if (event.type === 'invalidated' && event.kind === 'updates.changed') {
          void refreshUpdates();
        }
      }),
    [refreshUpdates],
  );

  const refreshAttention = useCallback(async () => {
    if (!runtimeReady) {
      setServerAttentionItems([]);
      return [];
    }
    try {
      const snapshot = await window.agentico.getAttention();
      setServerAttentionItems(snapshot.items);
      return snapshot.items;
    } catch {
      setServerAttentionItems([]);
      return [];
    }
  }, [runtimeReady]);

  const refreshRecovery = useCallback(async () => {
    if (!runtimeReady) {
      setRecoveryItems(null);
      return;
    }
    try {
      const snapshot = await window.agentico.scanRecovery();
      const live = snapshot.items.filter((i) => i.processAlive).length;
      const dead = snapshot.items.length - live;
      if (live > 0 || dead > 0) {
        setRecoveryItems((prev) =>
          prev === null
            ? { liveCount: live, deadCount: dead, firstSeenAt: new Date().toISOString() }
            : { ...prev, liveCount: live, deadCount: dead },
        );
      } else {
        setRecoveryItems(null);
      }
    } catch {
      setRecoveryItems(null);
    }
  }, [runtimeReady]);

  useEffect(() => {
    if (!runtimeReady) {
      setServerAttentionItems([]);
      setRecoveryItems(null);
      return;
    }
    void refreshAttention();
    void refreshRecovery();
    return window.agentico.onAppEvent((event) => {
      if (event.type === 'status') {
        void refreshAttention();
        return;
      }
      if (event.kind === 'resync') {
        void refreshAttention();
        void refreshRecovery();
        return;
      }
      if (
        event.kind === 'permission.updated' ||
        event.kind === 'prompt.updated' ||
        event.kind.startsWith('feature') ||
        event.kind.startsWith('lifecycle')
      ) {
        void refreshAttention();
      }
    });
  }, [refreshAttention, refreshRecovery, runtimeReady]);

  const refreshFeatureNames = useCallback(async () => {
    if (!runtimeReady) {
      setFeatureNames({});
      return;
    }
    try {
      const features = await window.agentico.listFeatures();
      setFeatureNames(namesById(features));
    } catch {
      setFeatureNames({});
    }
  }, [runtimeReady]);

  useEffect(() => {
    if (!runtimeReady) {
      setFeatureNames({});
      return;
    }
    void refreshFeatureNames();
    return window.agentico.onAppEvent((event) => {
      if (
        event.type === 'invalidated' &&
        (event.kind === 'resync' ||
          event.kind.startsWith('feature') ||
          event.kind.startsWith('lifecycle'))
      ) {
        void refreshFeatureNames();
      }
    });
  }, [refreshFeatureNames, runtimeReady]);

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

  // Recovery items sort ahead of all other attention so recovery receives
  // contextual priority (Task 8 acceptance criterion 1).
  const attentionItems = useMemo<AttentionItem[]>(() => {
    if (recoveryItems === null) return serverAttentionItems;
    const recoveryAttention: AttentionItem = {
      kind: 'recovery',
      id: 'recovery-scan',
      waitingSince: recoveryItems.firstSeenAt,
      liveCount: recoveryItems.liveCount,
      deadCount: recoveryItems.deadCount,
    };
    return [recoveryAttention, ...serverAttentionItems];
  }, [recoveryItems, serverAttentionItems]);

  return (
    <div className="app-frame">
      <header className="global-bar">
        <div className="global-bar__brand">
          <span className="global-bar__mark" aria-hidden="true">
            A
          </span>
          <h1>Agentico</h1>
        </div>
        <p className="global-bar__runtime" role="status" data-tone={runtimeTone}>
          <span aria-hidden="true">●</span> {runtimeLabel}
        </p>
        <AttentionInbox
          items={attentionItems}
          refresh={refreshAttention}
          featureLabel={(featureId) =>
            featureId === undefined ? 'Runtime' : (featureNames[featureId] ?? 'Untitled feature')
          }
          drafts={attentionDrafts}
          setDrafts={setAttentionDrafts}
          onJump={(featureId, attentionId) => {
            routeSequence.current += 1;
            setAttentionJump({
              requestId: routeSequence.current,
              featureId,
              ...(attentionId === undefined ? {} : { attentionId }),
            });
          }}
          openRequest={
            routeRequest?.event.target === 'attention'
              ? {
                  id: routeRequest.id,
                  attentionId: routeRequest.event.attentionId,
                }
              : null
          }
        />
        <fieldset className="theme-switcher" role="radiogroup" aria-label="Theme">
          <legend className="sr-only">Theme</legend>
          {THEME_OPTIONS.map((option) => (
            <label key={option.value} className="theme-switcher__option">
              <input
                type="radio"
                name="theme"
                value={option.value}
                checked={preference === option.value}
                onChange={() => setPreference(option.value)}
              />
              <span>{option.label}</span>
            </label>
          ))}
        </fieldset>
      </header>
      <UpdateNotice
        update={updateState}
        dismissedVersion={updateDismissedVersion}
        scheduling={schedulingUpdate}
        onDismiss={(version) => setUpdateDismissedVersion(version)}
        onOpenSettings={() => requestRoute({ target: 'settings', settingsSection: 'updates' })}
        onInstallWhenIdle={async () => {
          try {
            setSchedulingUpdate(true);
            setUpdateState(await window.agentico.installUpdateWhenIdle());
          } finally {
            setSchedulingUpdate(false);
          }
        }}
      />
      {runtimeReady ? (
        <>
          <ReadinessGate
            attentionItems={attentionItems}
            refreshAttention={refreshAttention}
            attentionDrafts={attentionDrafts}
            setAttentionDrafts={setAttentionDrafts}
            attentionJump={attentionJump}
            onAttentionJumpHandled={() => setAttentionJump(null)}
            routeRequest={routeRequest}
          />
          <AmaDock
            attentionItems={attentionItems}
            refreshAttention={refreshAttention}
            attentionDrafts={attentionDrafts}
            setAttentionDrafts={setAttentionDrafts}
            routeRequest={routeRequest}
          />
        </>
      ) : (
        <ConnectionShell />
      )}
      <CommandPalette ready={runtimeReady} routeRequest={routeRequest} onRoute={requestRoute} />
      <HelpOverlay routeRequest={routeRequest} />
    </div>
  );
}

export function UpdateNotice({
  update,
  dismissedVersion,
  scheduling,
  onDismiss,
  onOpenSettings,
  onInstallWhenIdle,
}: {
  update: UpdateState | null;
  dismissedVersion: string | null;
  scheduling: boolean;
  onDismiss(version: string): void;
  onOpenSettings(): void;
  onInstallWhenIdle(): Promise<void>;
}) {
  if (
    update === null ||
    update.targetVersion === undefined ||
    dismissedVersion === update.targetVersion ||
    !['ready', 'scheduled', 'available'].includes(update.status)
  ) {
    return null;
  }
  const updateHasActiveWork = hasActiveWork(update);
  const isScheduled = update.status === 'scheduled';
  return (
    <section className="update-notice" aria-label="Update available">
      <div>
        <strong>Agentico {update.targetVersion} is available</strong>
        <span>{update.message}</span>
        {update.activeWorkSummary && <span>{update.activeWorkSummary}</span>}
      </div>
      <div className="update-notice__actions">
        <button type="button" className="setup-wizard__action" onClick={onOpenSettings}>
          Updates
        </button>
        {canInstallInApp(update) && updateHasActiveWork && (
          <button
            type="button"
            className="setup-wizard__action setup-wizard__action--primary"
            onClick={() => void onInstallWhenIdle()}
            disabled={scheduling || isScheduled}
          >
            {installWhenIdleLabel({ scheduling, scheduled: isScheduled })}
          </button>
        )}
        <button
          type="button"
          className="settings-panel__root-btn"
          aria-label="Dismiss update notice"
          onClick={() => onDismiss(update.targetVersion!)}
        >
          Dismiss
        </button>
      </div>
    </section>
  );
}

function namesById(features: readonly FeatureSummaryView[]): Record<string, string> {
  return Object.fromEntries(features.map((feature) => [feature.id, feature.name]));
}
