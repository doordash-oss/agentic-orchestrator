import { afterEach, expect, it, vi } from 'vitest';

afterEach(() => {
  vi.unstubAllEnvs();
  vi.resetModules();
});

it('compiles the release-stamped desktop version into the renderer', async () => {
  vi.stubEnv('AGENTICO_DESKTOP_VERSION', '0.149.1');
  vi.resetModules();

  const { default: config } = await import('../../../electron.vite.config');
  if (typeof config !== 'object' || config === null) {
    throw new Error('electron-vite config did not resolve to an object');
  }
  const renderer = config.renderer as { define?: Record<string, string> } | undefined;

  expect(renderer?.define?.__APP_VERSION__).toBe(JSON.stringify('0.149.1'));
});
