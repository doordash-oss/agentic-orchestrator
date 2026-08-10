// Canonicalize electron-builder's Linux artifact names for release publishing.
import { existsSync, renameSync } from 'node:fs';
import { join } from 'node:path';

/** Rename electron-builder's AppImage output to the public release contract name. */
export function normalizeLinuxAppImage(distDir, arch) {
  if (arch !== 'x64' && arch !== 'arm64') {
    throw new Error(`unsupported Linux package architecture: ${arch}`);
  }
  const builderArch = arch === 'x64' ? 'x86_64' : arch;
  const source = join(distDir, `Agentico-${builderArch}.AppImage`);
  const destination = join(distDir, `Agentico-${arch}.AppImage`);
  if (source !== destination) {
    if (existsSync(destination)) {
      throw new Error(`refusing to overwrite existing Linux release artifact: ${destination}`);
    }
    renameSync(source, destination);
  }
  return destination;
}
