/// <reference types="vite/client" />
import type { AgenticoApi } from '../../shared/ipc';

declare global {
  /** Injected at build time from package.json. */
  const __APP_VERSION__: string;

  interface Window {
    /** The only bridge to the main process (see src/preload/index.ts). */
    agentico: AgenticoApi;
  }
}

export {};
