/**
 * The verified-package manifest written by scripts/verify-package.mjs. The
 * journeys never re-derive package layout: they launch exactly the
 * unpacked_app path the inspection gate proved.
 */
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

export const desktopDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../..');

export interface PackageVerification {
  verified_at: string;
  host: { os: string; arch: string };
  artifacts: { target: string; path: string }[];
  unpacked_app: string;
  identity: {
    desktop_version: string;
    api_version: string;
    schema_version: number;
    server_version: string;
    server_revision: string;
    os: string;
    arch: string;
    built_at: string;
  };
}

export function readVerification(): PackageVerification | null {
  const manifest = path.join(desktopDir, 'dist', 'package-verification.json');
  try {
    return JSON.parse(fs.readFileSync(manifest, 'utf8')) as PackageVerification;
  } catch {
    return null;
  }
}

/**
 * The executable the journeys launch; throws when global-setup did not run.
 * AGENTICO_E2E_EXECUTABLE points a run at a different install of the same
 * verified build (extracted AppImage payload, dpkg-installed deb) — the
 * journeys' identity assertions still cross-check it against the manifest.
 */
export function packagedExecutable(): string {
  const override = process.env['AGENTICO_E2E_EXECUTABLE'];
  if (override !== undefined && override !== '') {
    if (!fs.existsSync(override)) {
      throw new Error(`AGENTICO_E2E_EXECUTABLE does not exist: ${override}`);
    }
    return override;
  }
  const verification = readVerification();
  if (verification === null || !fs.existsSync(verification.unpacked_app)) {
    throw new Error(
      'no verified package found — run `npm run package:verify` (global-setup does this automatically)',
    );
  }
  return verification.unpacked_app;
}

/**
 * The packaged resources directory (bundled server + build identity),
 * derived from the verified executable path per platform layout.
 */
export function packagedResourcesDir(executablePath: string): string {
  if (process.platform === 'darwin') {
    // .../Agentico.app/Contents/MacOS/Agentico → .../Contents/Resources
    return path.join(path.dirname(path.dirname(executablePath)), 'Resources');
  }
  // .../linux-unpacked/agentico → .../linux-unpacked/resources
  return path.join(path.dirname(executablePath), 'resources');
}

/** The bundled Go server binary inside the verified package. */
export function bundledServerBinary(executablePath: string): string {
  return path.join(packagedResourcesDir(executablePath), 'bin', 'agentico');
}
