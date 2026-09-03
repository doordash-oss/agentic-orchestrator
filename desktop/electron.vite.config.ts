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

import { createRequire } from 'node:module';
import { defineConfig } from 'electron-vite';
import react from '@vitejs/plugin-react';

const pkg = createRequire(import.meta.url)('./package.json') as { version: string };
const rendererVersion = process.env.AGENTICO_DESKTOP_VERSION?.trim() || pkg.version;

// Everything (including zod and shared modules) is bundled into each target so
// the packaged app ships no runtime node_modules.
export default defineConfig({
  main: {
    build: {
      rollupOptions: {
        input: { index: 'src/main/index.ts' },
      },
    },
  },
  preload: {
    build: {
      rollupOptions: {
        input: { index: 'src/preload/index.ts' },
        output: {
          // Sandboxed preload scripts must be CommonJS.
          format: 'cjs',
          entryFileNames: '[name].cjs',
        },
      },
    },
  },
  renderer: {
    plugins: [react()],
    define: {
      __APP_VERSION__: JSON.stringify(rendererVersion),
    },
  },
});
