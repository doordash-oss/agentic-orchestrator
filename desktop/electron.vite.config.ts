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
