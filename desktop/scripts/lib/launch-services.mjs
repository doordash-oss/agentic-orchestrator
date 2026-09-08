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

/**
 * macOS LaunchServices hygiene for throwaway Agentico.app copies.
 *
 * Every app copy that runs registers itself as an `agentico:` URL handler,
 * and deleting the bundle does not undo that: LaunchServices keeps the dead
 * record and may still route `open agentico://…` (a bearer-token link) to it.
 * Anything that creates a temporary bundle must unregister it before removing
 * it. Everything here is best effort and a no-op off macOS.
 */
import { execFileSync } from 'node:child_process';
import { existsSync } from 'node:fs';

export const LSREGISTER =
  '/System/Library/Frameworks/CoreServices.framework/Versions/A/Frameworks/LaunchServices.framework/Versions/A/Support/lsregister';

export const AGENTICO_SCHEME = 'agentico:';

/** The one bundle that should keep its `agentico:` claim. */
export const INSTALLED_APP = '/Applications/Agentico.app';

export function lsregisterAvailable(platform = process.platform) {
  return platform === 'darwin' && existsSync(LSREGISTER);
}

/**
 * Unregisters one app bundle. Returns true when lsregister ran and exited 0;
 * false otherwise (not macOS, binary missing, or lsregister refused). Never
 * throws — teardown must not fail because of registry hygiene.
 */
export function unregisterAppBundle(bundlePath, options = {}) {
  const { platform = process.platform, exec = execFileSync } = options;
  if (!lsregisterAvailable(platform)) return false;
  try {
    exec(LSREGISTER, ['-u', bundlePath], { stdio: 'ignore', timeout: 30_000 });
    return true;
  } catch {
    return false;
  }
}

/**
 * Parses `lsregister -dump` into the bundles that claim the given scheme.
 * Records are separated by dashed rules; only `path:` and `claimed schemes:`
 * are read. Returns unique bundle paths in dump order.
 */
export function parseSchemeClaimants(dumpText, scheme = AGENTICO_SCHEME) {
  const claimants = [];
  const seen = new Set();
  for (const record of dumpText.split(/^-{20,}\s*$/m)) {
    const pathMatch = record.match(/^path:\s+(.+?)(?:\s+\(0x[0-9a-f]+\))?\s*$/m);
    const schemesMatch = record.match(/^claimed schemes:\s+(.+)$/m);
    if (pathMatch === null || schemesMatch === null) continue;
    const schemes = schemesMatch[1].split(/[\s,]+/).filter((token) => token !== '');
    if (!schemes.includes(scheme)) continue;
    const bundlePath = pathMatch[1].trim();
    if (seen.has(bundlePath)) continue;
    seen.add(bundlePath);
    claimants.push(bundlePath);
  }
  return claimants;
}

/**
 * Decides whether a claimant is stale. Only the installed app keeps its
 * claim; every other claimant is either a dev Electron shell, a build or test
 * copy, or a bundle that no longer exists. Returns a short reason, or null
 * when the claimant should be left alone.
 */
export function stalenessReason(bundlePath, options = {}) {
  const { installedApp = INSTALLED_APP, exists = existsSync } = options;
  if (bundlePath === installedApp) return null;
  if (!exists(bundlePath)) return 'bundle no longer exists';
  if (/\/node_modules\/electron\//.test(bundlePath)) return 'dev Electron shell';
  if (/\/(?:var\/folders|tmp)\//.test(bundlePath)) return 'temporary directory';
  return `not the installed app (${installedApp})`;
}
