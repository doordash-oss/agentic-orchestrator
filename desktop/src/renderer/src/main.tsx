import React from 'react';
import { createRoot } from 'react-dom/client';
import App from './App';

// Packaged fonts (offline-safe); system fallbacks are declared in tokens.css.
import '@fontsource/space-grotesk/500.css';
import '@fontsource/space-grotesk/700.css';
import '@fontsource/ibm-plex-sans/400.css';
import '@fontsource/ibm-plex-sans/500.css';
import '@fontsource/ibm-plex-sans/600.css';
import '@fontsource/ibm-plex-mono/400.css';
import '@fontsource/ibm-plex-mono/500.css';

import './styles/tokens.css';
import './styles/app.css';

const container = document.getElementById('root');
if (container === null) {
  throw new Error('Renderer bootstrap failed: #root missing.');
}
createRoot(container).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
