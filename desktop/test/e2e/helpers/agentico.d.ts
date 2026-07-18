/**
 * Packaged journeys execute against the same narrow preload contract as the
 * renderer. This makes test and screenshot code fail typechecking when IPC
 * methods drift instead of maintaining a permissive shadow surface.
 */
import type { AgenticoApi } from '../../../src/shared/ipc';

declare global {
  interface Window {
    agentico: AgenticoApi;
  }
}

export {};
