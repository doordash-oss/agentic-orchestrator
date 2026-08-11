import { expect, it } from 'vitest';
import { isMainModule } from './main-entry.mjs';

it('fails closed when a direct entry point cannot be resolved', () => {
  expect(() => isMainModule(import.meta.url, '/does/not/exist.mjs')).toThrow(/ENOENT/);
});
