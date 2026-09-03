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

import React from 'react';
import { createRoot } from 'react-dom/client';
import App from './App';
import SettingsWindow from './SettingsWindow';

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
