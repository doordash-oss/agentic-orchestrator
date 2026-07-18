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

import { installMockApi } from './mock-api';
import { ArchiveMode } from '../../../src/renderer/src/features/ArchiveMode';
import { RewindJourney } from '../../../src/renderer/src/features/RewindJourney';

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

function App() {
  const scene = getScene();

  if (scene === 'archive' || scene === 'pinned' || scene === 'constrained') {
    return <ArchiveScene scene={scene} />;
  }
  if (scene === 'rewind-confirm' || scene === 'fork') {
    return <RewindScene />;
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
