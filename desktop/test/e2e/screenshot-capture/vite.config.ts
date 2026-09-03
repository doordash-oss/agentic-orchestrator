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

import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  // ConnectionShell reads this build-time global (see env.d.ts); the
  // harness has no package.json version to embed, so mock a stable one —
  // matches vitest.config.ts's renderer project.
  define: {
    __APP_VERSION__: JSON.stringify('0.1.0'),
  },
  server: {
    port: 9871,
    strictPort: true,
  },
});
