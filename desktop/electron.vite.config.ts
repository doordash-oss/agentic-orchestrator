import { defineConfig } from 'electron-vite';
import react from '@vitejs/plugin-react';

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
  },
});
