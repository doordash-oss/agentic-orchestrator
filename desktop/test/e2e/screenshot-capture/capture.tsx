import React from 'react';
import { createRoot } from 'react-dom/client';

import '@fontsource/barlow-condensed/500.css';
import '@fontsource/barlow-condensed/600.css';
import '@fontsource/atkinson-hyperlegible/400.css';
import '@fontsource/atkinson-hyperlegible/700.css';
import '@fontsource/ibm-plex-mono/400.css';
import '@fontsource/ibm-plex-mono/500.css';

import '../../../src/renderer/src/styles/tokens.css';
import '../../../src/renderer/src/styles/app.css';

import { installMockApi, CYCLES_FEATURE_SNAPSHOT, REBASE_FEATURE_SNAPSHOT } from './mock-api';
import { ArchiveMode } from '../../../src/renderer/src/features/ArchiveMode';
import { RewindJourney } from '../../../src/renderer/src/features/RewindJourney';
import { RepositoryInstrument } from '../../../src/renderer/src/features/RepositoryInstrument';
import { CurrentRunInspection } from '../../../src/renderer/src/features/CurrentRunInspection';
import { CycleJourneys } from '../../../src/renderer/src/features/CycleJourneys';
import { BulkPreviewPanel } from '../../../src/renderer/src/features/BulkPreviewPanel';
import { RecoveryWorkspace } from '../../../src/renderer/src/features/RecoveryWorkspace';
import { CompletionWorkspace } from '../../../src/renderer/src/features/CompletionWorkspace';
import { SettingsPanel } from '../../../src/renderer/src/features/SettingsPanel';
import { WorkspaceShell } from '../../../src/renderer/src/features/WorkspaceShell';
import { UpdateNotice } from '../../../src/renderer/src/App';
import { AmaDock } from '../../../src/renderer/src/components/AmaDock';
import { CommandPalette } from '../../../src/renderer/src/components/CommandPalette';
import {
  AttentionInbox,
  emptyAttentionDrafts,
} from '../../../src/renderer/src/features/AttentionInbox';
import { PhaseSpine } from '../../../src/renderer/src/components/PhaseSpine';
import {
  spineStages,
  spineActiveIndex,
  spineTone,
} from '../../../src/renderer/src/features/featureView';
import type {
  AgenticoApi,
  AppRouteEvent,
  AttentionItem,
  FeatureActionRequest,
} from '../../../src/shared/ipc';
import type { CompletionStep } from '../../../src/renderer/src/features/CompletionWorkspace';

function getScene(): string {
  const params = new URLSearchParams(window.location.search);
  return params.get('scene') ?? 'archive';
}

function ArchiveScene({ scene }: { scene: string }) {
  const [selectedRun, setSelectedRun] = React.useState(7);
  const badge = scene === 'pinned' ? ('changed' as const) : null;

  return (
    <div
      className="workspace-shell__content"
      style={{ height: '100vh', display: 'flex', flexDirection: 'column' }}
    >
      <div
        className="cockpit"
        aria-label="Feature History and Rewind"
        style={{ flex: 1, overflow: 'auto' }}
      >
        <ArchiveMode
          featureId="abcd1234ef567890"
          selectedRunNumber={selectedRun}
          currentRunNumber={8}
          pipeline="large"
          currentRunBadges={{ changed: badge === 'changed', attention: false }}
          onReturnToCurrent={() => setSelectedRun(0)}
          onSelectRun={(n) => setSelectedRun(n)}
        />
      </div>
    </div>
  );
}

function RewindScene() {
  return (
    <div style={{ position: 'fixed', inset: 0, background: 'var(--bg-elevation-1, #1a1a1a)' }}>
      <RewindJourney
        featureId="abcd1234ef567890"
        featureName="History and Rewind"
        validPhaseOptions={['inquire', 'research', 'design', 'plan', 'implement']}
        currentRoadmapPhase={2}
        totalRoadmapPhases={4}
        onClose={() => {}}
        onRewindComplete={() => {}}
      />
    </div>
  );
}

function RepoInstrumentScene() {
  const snapshot = CYCLES_FEATURE_SNAPSHOT;
  const stages = spineStages(snapshot.pipeline);
  return (
    <div
      className="workspace-shell__content"
      style={{
        height: '100vh',
        display: 'flex',
        flexDirection: 'column',
        padding: 'var(--space-4)',
      }}
    >
      <div className="cockpit" aria-label="Feature cockpit" style={{ flex: 1, overflow: 'auto' }}>
        <PhaseSpine
          stages={stages}
          activeIndex={spineActiveIndex(snapshot, stages)}
          tone={spineTone(snapshot)}
          label="Feature pipeline"
        />
        <div className="cockpit__actions" role="group" aria-label="Feature actions">
          <button type="button" className="cockpit__stop">
            Stop
          </button>
          <button type="button" className="cockpit__restart">
            Restart
          </button>
          <button type="button" className="cockpit__rewind-button">
            ↺ Rewind
          </button>
          <button type="button" className="cockpit__cycles-button">
            ⟳ Cycles
          </button>
          <button type="button" className="cockpit__inspector-toggle" aria-expanded={true}>
            Inspector
          </button>
          <p className="cockpit__phase-status" aria-label="Current feature status">
            <code>{snapshot.status}</code>
          </p>
        </div>
        <div className="cockpit__content">
          <main className="cockpit__canvas">
            <h2 className="cockpit__title">{snapshot.name}</h2>
            <dl className="cockpit__facts">
              <div className="cockpit__fact">
                <dt>Status</dt>
                <dd>
                  <code data-status={snapshot.status} />
                </dd>
              </div>
              <div className="cockpit__fact">
                <dt>Repositories</dt>
                <dd>
                  <code>{snapshot.repos.join(', ')}</code>
                </dd>
              </div>
            </dl>
          </main>
          <aside className="cockpit__inspector" aria-label="Feature inspector">
            <header className="cockpit__header">
              <div className="cockpit__identity">
                <h2 className="cockpit__title">{snapshot.name}</h2>
              </div>
            </header>
            {snapshot.repoStatus !== undefined && (
              <RepositoryInstrument repos={snapshot.repoStatus} />
            )}
          </aside>
        </div>
      </div>
    </div>
  );
}

function RunGaugeScene() {
  return (
    <div
      className="workspace-shell__content"
      style={{
        height: '100vh',
        display: 'flex',
        flexDirection: 'column',
        overflow: 'auto',
        padding: 'var(--space-4)',
      }}
    >
      <div className="cockpit" aria-label="Feature cockpit" style={{ flex: 1 }}>
        <div className="cockpit__content">
          <main className="cockpit__canvas">
            <CurrentRunInspection
              featureId="abcd1234ef567890"
              runNumber={8}
              currentPhase="Implement"
              currentRoadmapPhase={2}
              totalRoadmapPhases={5}
              currentIteration={3}
              phaseStatus="implementing"
              reviewGate={{
                reviewingGate: false,
                reviewFixing: false,
                validatingPlan: false,
                validatorStatuses: {},
              }}
              shouldStream={false}
            />
          </main>
        </div>
      </div>
    </div>
  );
}

function CycleJourneysScene({ scene }: { scene: string }) {
  const snapshot = scene === 'rebase-preflight' ? REBASE_FEATURE_SNAPSHOT : CYCLES_FEATURE_SNAPSHOT;
  const gateItems =
    scene === 'cycle-gate'
      ? [
          {
            kind: 'gate' as const,
            id: 'cycle-gate-001',
            featureId: 'abcd1234ef567890',
            waitingSince: new Date(Date.now() - 120_000).toISOString(),
            cycleType: 'review-comments',
            repoName: 'signal-lab',
            summary: 'Review-comments cycle is paused waiting for answers.',
            questions: [],
          },
        ]
      : [];
  const isConstrained = scene === 'cycle-gate';
  return (
    <div
      className="workspace-shell__content"
      style={{
        height: '100vh',
        display: 'flex',
        flexDirection: 'column',
        overflow: 'hidden',
        padding: 'var(--space-4)',
      }}
    >
      <div
        className="cockpit__cycles-drawer"
        style={{
          padding: '0',
          flex: isConstrained ? '1 1 auto' : undefined,
          minHeight: 0,
          overflow: 'auto',
        }}
      >
        <header className="cockpit__cycles-header">
          <h3>Repository cycles</h3>
          <button type="button" className="cockpit__cycles-close">
            Close
          </button>
        </header>
        <div style={{ padding: 'var(--space-3) var(--space-4)' }}>
          <CycleJourneys
            featureId="abcd1234ef567890"
            snapshot={snapshot}
            onComplete={() => {}}
            attentionItems={gateItems}
            onOpenGate={() => {}}
          />
        </div>
      </div>
      {scene === 'cycle-gate' ? (
        <div
          className="recovery-workspace"
          aria-label="Recovery workspace"
          style={{
            marginTop: 'var(--space-3)',
            padding: 'var(--space-3)',
            flexShrink: 0,
          }}
        >
          <header className="recovery-workspace__header">
            <div>
              <h3 className="recovery-workspace__title">Recovery</h3>
              <p className="recovery-workspace__summary">1 live · 0 dead · 1 total</p>
            </div>
          </header>
          <div className="recovery-attention" aria-label="Recovery priority attention" role="alert">
            <span className="recovery-attention__priority" role="status">
              Recovery priority — 1 live orphan process
            </span>
          </div>
          <ul className="recovery-workspace__queue" aria-label="Recovery items">
            <li className="recovery-workspace__item" data-alive="true" data-outcome="pending">
              <header className="recovery-workspace__item-header">
                <span
                  className="recovery-workspace__item-process"
                  data-alive="true"
                  aria-label="Live process"
                >
                  ●
                </span>
                <span className="recovery-workspace__item-name">
                  <button
                    type="button"
                    className="recovery-workspace__item-link"
                    onClick={() => {}}
                  >
                    Signal Lab telemetry
                  </button>
                </span>
                <code className="recovery-workspace__item-repo">signal-lab</code>
                <code className="recovery-workspace__item-phase">implement</code>
              </header>
            </li>
          </ul>
        </div>
      ) : null}
    </div>
  );
}

function BulkPreviewScene() {
  return (
    <div
      className="workspace-shell__content"
      style={{
        height: '100vh',
        display: 'flex',
        flexDirection: 'column',
        overflow: 'auto',
        padding: '1rem',
      }}
    >
      <BulkPreviewPanel />
    </div>
  );
}

function RecoveryScene({ scene }: { scene: string }) {
  const [attentionItems] = React.useState<
    { kind: 'recovery'; id: string; waitingSince: string; liveCount: number; deadCount: number }[]
  >(
    scene === 'recovery' || scene === 'recovery-constrained'
      ? [
          {
            kind: 'recovery' as const,
            id: 'recovery-scan',
            waitingSince: new Date(Date.now() - 120_000).toISOString(),
            liveCount: 1,
            deadCount: 1,
          },
        ]
      : [],
  );
  const [drafts] = React.useState(emptyAttentionDrafts());
  return (
    <div
      className="workspace-shell__content"
      style={{
        height: '100vh',
        display: 'flex',
        flexDirection: 'column',
        overflow: 'auto',
        padding: 'var(--space-4)',
      }}
    >
      <div className="global-bar" style={{ marginBottom: 'var(--space-3)' }}>
        <AttentionInbox
          items={attentionItems}
          refresh={async () => attentionItems}
          featureLabel={() => 'Recovery workspace'}
          drafts={drafts}
          setDrafts={() => {}}
          onJump={() => {}}
        />
      </div>
      <RecoveryWorkspace onNavigateToFeature={() => {}} />
    </div>
  );
}

function CompletionScene({ scene }: { scene: string }): React.ReactElement {
  const api = (window as unknown as { agentico: AgenticoApi }).agentico;
  const initialStep: CompletionStep =
    scene === 'completion-publish'
      ? 'publish'
      : scene === 'completion-delete'
        ? 'delete'
        : 'inspect';
  const isConstrained = scene === 'completion-constrained';
  return (
    <div
      style={{
        height: '100vh',
        display: 'flex',
        flexDirection: 'column',
        padding: isConstrained ? '0' : '24px',
        maxWidth: isConstrained ? '760px' : '100%',
        margin: isConstrained ? '0 auto' : undefined,
      }}
    >
      <CompletionWorkspace
        featureId="feat-electron-app"
        featureName="Electron App for Agentic Orchestrator"
        initialStep={initialStep}
        onClose={() => {}}
        preflightCompletion={(id) => api.preflightCompletion({ featureId: id })}
        getRepositoryDiff={(id, repo, filePath) =>
          api.getRepositoryDiff({ featureId: id, repo, ...(filePath ? { filePath } : {}) })
        }
        dispatchAction={(id, action, body) =>
          api.dispatchFeatureAction({
            featureId: id,
            action,
            ...(body ? { body } : {}),
          } as FeatureActionRequest)
        }
        generatePublishDescription={(id) => api.generatePublishDescription({ featureId: id })}
        openExternal={(url) => api.openExternal({ url })}
        revealPath={(id, repo) => api.revealPath({ featureId: id, repo })}
      />
    </div>
  );
}

function BackgroundScene({ scene }: { scene: string }): React.ReactElement {
  const [drafts, setDrafts] = React.useState(emptyAttentionDrafts());
  const [attentionItems, setAttentionItems] = React.useState<AttentionItem[]>([]);
  const amaRoute =
    scene === 'background-ama-expanded' || scene === 'background-ama-constrained'
      ? { id: 1, event: { target: 'ama' as const } }
      : null;
  const paletteRoute =
    scene === 'background-command-palette' || scene === 'background-ama-constrained'
      ? { id: 2, event: { target: 'palette' as const } }
      : null;

  React.useEffect(() => {
    void window.agentico.getAttention().then((snapshot) => setAttentionItems(snapshot.items));
  }, []);

  const refreshAttention = React.useCallback(async () => {
    const snapshot = await window.agentico.getAttention();
    setAttentionItems(snapshot.items);
    return snapshot.items;
  }, []);

  const route = React.useCallback((_event: AppRouteEvent) => {}, []);

  return (
    <div className="app-frame" style={{ height: '100vh' }}>
      <header className="global-bar">
        <div className="global-bar__brand">
          <span className="global-bar__mark" aria-hidden="true">
            A
          </span>
          <h1>Agentico</h1>
        </div>
        <p className="global-bar__runtime" role="status" data-tone="ready">
          <span aria-hidden="true">●</span> Runtime ready
        </p>
        <AttentionInbox
          items={attentionItems}
          refresh={refreshAttention}
          featureLabel={() => 'History and Rewind'}
          drafts={drafts}
          setDrafts={setDrafts}
          onJump={() => {}}
          openRequest={scene === 'background-ama-compact' ? { id: 1 } : null}
        />
      </header>
      {scene === 'background-ama-compact' ? (
        <div className="tab-panel" style={{ flex: 1, minHeight: 0 }}>
          <SettingsPanel />
        </div>
      ) : (
        <div className="tab-panel" style={{ flex: 1, minHeight: 0 }}>
          <header className="home-surface__header">
            <div>
              <p className="home-surface__eyebrow">Background supervision</p>
              <h1>History and Rewind</h1>
            </div>
            <button type="button" className="create-form__submit">
              New feature
            </button>
          </header>
          <section className="cockpit__actions" role="group" aria-label="Feature actions">
            <button type="button" className="cockpit__stop">
              Stop
            </button>
            <button type="button" className="cockpit__restart">
              Restart
            </button>
            <button type="button" className="cockpit__rewind-button">
              Rewind
            </button>
            <p className="cockpit__phase-status" aria-label="Current feature status">
              <code>implementing</code>
            </p>
          </section>
          {scene === 'background-close-dialog' ? <CloseDialogScene /> : null}
        </div>
      )}
      <AmaDock
        attentionItems={attentionItems}
        refreshAttention={refreshAttention}
        attentionDrafts={drafts}
        setAttentionDrafts={setDrafts}
        routeRequest={amaRoute}
      />
      <CommandPalette ready={true} routeRequest={paletteRoute} onRoute={route} />
    </div>
  );
}

function CloseDialogScene(): React.ReactElement {
  return (
    <div className="impact-dialog__backdrop" style={{ position: 'absolute' }}>
      <div
        className="impact-dialog"
        role="dialog"
        aria-modal="true"
        aria-label="Work is still running"
      >
        <h2>Work is still running</h2>
        <p>Agentico has background work that may continue without the window.</p>
        <p>
          1 feature run is stoppable. The AMA session is active. Keep Running hides the window and
          leaves work attached.
        </p>
        <div className="impact-dialog__actions">
          <button type="button">Keep Running</button>
          <button type="button" className="cockpit__stop">
            Stop Work and Quit
          </button>
          <button type="button">Cancel</button>
        </div>
      </div>
    </div>
  );
}

function UpdateAppScene(): React.ReactElement {
  const [update, setUpdate] = React.useState<Awaited<ReturnType<AgenticoApi['getUpdates']>> | null>(
    null,
  );
  const [showSettings, setShowSettings] = React.useState(false);
  React.useEffect(() => {
    void window.agentico.getUpdates().then(setUpdate);
  }, []);
  return (
    <div className="app-frame" style={{ height: '100vh' }}>
      <header className="global-bar">
        <div className="global-bar__brand">
          <span className="global-bar__mark" aria-hidden="true">
            A
          </span>
          <h1>Agentico</h1>
        </div>
        <p className="global-bar__runtime" role="status" data-tone="ready">
          <span aria-hidden="true">●</span> Runtime ready
        </p>
      </header>
      <UpdateNotice
        update={update}
        dismissedVersion={null}
        scheduling={false}
        onDismiss={() => {}}
        onOpenSettings={() => setShowSettings(true)}
        onInstallWhenIdle={async () => {
          setUpdate(await window.agentico.installUpdateWhenIdle());
        }}
      />
      <WorkspaceShell
        routeRequest={
          showSettings
            ? {
                id: 1,
                event: { target: 'settings', settingsSection: 'updates' },
              }
            : null
        }
      />
    </div>
  );
}

function SettingsUpdateScene({ scene }: { scene: string }): React.ReactElement {
  return (
    <div className="app-frame" style={{ height: '100vh' }}>
      <header className="global-bar">
        <div className="global-bar__brand">
          <span className="global-bar__mark" aria-hidden="true">
            A
          </span>
          <h1>Agentico</h1>
        </div>
        <p className="global-bar__runtime" role="status" data-tone="ready">
          <span aria-hidden="true">●</span> Runtime ready
        </p>
      </header>
      <div className="tab-panel" style={{ flex: 1, minHeight: 0 }}>
        <SettingsPanel
          routeRequest={{
            id: 1,
            event: {
              target: 'settings',
              settingsSection: scene === 'settings-diagnostics' ? 'diagnostics' : 'updates',
            },
          }}
        />
      </div>
    </div>
  );
}

function CaptureApp() {
  const scene = getScene();

  if (scene === 'update-passive-active' || scene === 'update-constrained') {
    return <UpdateAppScene />;
  }
  if (
    scene === 'settings-updates-ready' ||
    scene === 'settings-updates-deb' ||
    scene === 'settings-install-now-confirm' ||
    scene === 'settings-diagnostics'
  ) {
    return <SettingsUpdateScene scene={scene} />;
  }

  if (scene === 'archive' || scene === 'pinned' || scene === 'constrained') {
    return <ArchiveScene scene={scene} />;
  }
  if (scene === 'rewind-confirm' || scene === 'fork') {
    return <RewindScene />;
  }
  if (scene === 'repo-instrument') {
    return <RepoInstrumentScene />;
  }
  if (scene === 'run-gauge') {
    return <RunGaugeScene />;
  }
  if (scene === 'rebase-preflight' || scene === 'review-refactor' || scene === 'cycle-gate') {
    return <CycleJourneysScene scene={scene} />;
  }
  if (scene === 'bulk-preview' || scene === 'bulk-queue') {
    return <BulkPreviewScene />;
  }
  if (scene === 'recovery' || scene === 'recovery-constrained') {
    return <RecoveryScene scene={scene} />;
  }
  if (
    scene === 'completion-inspect' ||
    scene === 'completion-publish' ||
    scene === 'completion-constrained' ||
    scene === 'completion-delete'
  ) {
    return <CompletionScene scene={scene} />;
  }
  if (scene.startsWith('background-')) {
    return <BackgroundScene scene={scene} />;
  }
  return <div>Unknown scene: {scene}</div>;
}

const container = document.getElementById('root');
if (!container) throw new Error('#root missing');

const mockControls = installMockApi(getScene());
(window as typeof window & { __agenticoMock?: typeof mockControls }).__agenticoMock = mockControls;

createRoot(container).render(
  <React.StrictMode>
    <CaptureApp />
  </React.StrictMode>,
);
