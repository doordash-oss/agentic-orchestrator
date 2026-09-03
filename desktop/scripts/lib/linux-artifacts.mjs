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
