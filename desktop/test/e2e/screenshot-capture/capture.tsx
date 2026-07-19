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
import { CycleJourneys } from '../../../src/renderer/src/features/CycleJourneys';
import { BulkPreviewPanel } from '../../../src/renderer/src/features/BulkPreviewPanel';
import { RecoveryWorkspace } from '../../../src/renderer/src/features/RecoveryWorkspace';
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

function App() {
  const scene = getScene();

  if (scene === 'archive' || scene === 'pinned' || scene === 'constrained') {
    return <ArchiveScene scene={scene} />;
  }
  if (scene === 'rewind-confirm' || scene === 'fork') {
    return <RewindScene />;
  }
  if (scene === 'repo-instrument') {
    return <RepoInstrumentScene />;
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
  return <div>Unknown scene: {scene}</div>;
}

const container = document.getElementById('root');
if (!container) throw new Error('#root missing');

installMockApi(getScene());

createRoot(container).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
