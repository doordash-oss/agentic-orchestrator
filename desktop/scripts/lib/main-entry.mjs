// Resolve Node's argv entry point and ESM module URL through symlinks before comparing them.
import { realpathSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

/** Return whether the supplied ESM module is the script Node was asked to execute. */
export function isMainModule(moduleUrl, entryPoint = process.argv[1]) {
  if (typeof entryPoint !== 'string' || entryPoint === '') return false;
  return realpathSync(entryPoint) === realpathSync(fileURLToPath(moduleUrl));
}
