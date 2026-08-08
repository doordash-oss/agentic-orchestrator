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
