import React from 'react';
import { createRoot } from 'react-dom/client';
import App from './App';
import SettingsWindow from './SettingsWindow';

// Packaged fonts (offline-safe); system fallbacks are declared in tokens.css.
import '@fontsource/barlow-condensed/500.css';
import '@fontsource/barlow-condensed/600.css';
import '@fontsource/atkinson-hyperlegible/400.css';
import '@fontsource/atkinson-hyperlegible/700.css';
import '@fontsource/ibm-plex-mono/400.css';
import '@fontsource/ibm-plex-mono/500.css';

import './styles/tokens.css';
import './styles/app.css';

// Set before first paint so platform-conditional CSS (Bench material,
// traffic-light drag region) never flashes the wrong variant.
document.documentElement.dataset['platform'] = window.agentico.platform;

const container = document.getElementById('root');
if (container === null) {
  throw new Error('Renderer bootstrap failed: #root missing.');
}

// One entry point, two roots: the window's purpose arrives as a constructor
// argument through the preload, so the right root mounts on the first render.
const Root = window.agentico.windowPurpose === 'settings' ? SettingsWindow : App;

createRoot(container).render(
  <React.StrictMode>
    <Root />
  </React.StrictMode>,
);
