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

// One-off cleanup for machines polluted by throwaway Agentico.app copies that
// registered themselves as `agentico:` URL handlers (old E2E installs, build
// output in worktrees, dev Electron shells). Lists every claimant other than
// /Applications/Agentico.app; `--apply` unregisters them. Dry run by default.
//
//   npm run e2e:cleanup-handlers            # report only
//   npm run e2e:cleanup-handlers -- --apply # unregister
//
// Manual only — never wire this into a test, build, or teardown step. Rewriting
// LaunchServices URL-handler claims is a pattern endpoint-security tools flag
// as scheme hijacking; run it knowingly, once, on a polluted machine.
import { execFileSync } from 'node:child_process';
import {
  AGENTICO_SCHEME,
  INSTALLED_APP,
  LSREGISTER,
  lsregisterAvailable,
  parseSchemeClaimants,
  stalenessReason,
  unregisterAppBundle,
} from './lib/launch-services.mjs';

const apply = process.argv.includes('--apply');

if (!lsregisterAvailable()) {
  console.log('lsregister is only available on macOS; nothing to do.');
  process.exit(0);
}

const dump = execFileSync(LSREGISTER, ['-dump'], {
  encoding: 'utf8',
  maxBuffer: 512 * 1024 * 1024,
});
const claimants = parseSchemeClaimants(dump, AGENTICO_SCHEME);
const stale = claimants
  .map((bundlePath) => ({ bundlePath, reason: stalenessReason(bundlePath) }))
  .filter((entry) => entry.reason !== null);

console.log(`${claimants.length} bundle(s) claim ${AGENTICO_SCHEME}; ${stale.length} stale.`);
if (claimants.includes(INSTALLED_APP)) {
  console.log(`keep   ${INSTALLED_APP}`);
} else {
  console.log(`note   ${INSTALLED_APP} is not registered as a handler on this machine`);
}

let failed = 0;
for (const { bundlePath, reason } of stale) {
  if (!apply) {
    console.log(`stale  ${bundlePath}  (${reason})`);
    continue;
  }
  const ok = unregisterAppBundle(bundlePath);
  if (!ok) failed += 1;
  console.log(`${ok ? 'removed' : 'FAILED '} ${bundlePath}  (${reason})`);
}

if (!apply && stale.length > 0) {
  console.log('\nDry run. Re-run with --apply to unregister the stale claimants.');
}
if (failed > 0) {
  console.log(
    `\n${failed} claimant(s) could not be unregistered individually. Rebuild the ` +
      `database as a last resort:\n  ${LSREGISTER} -kill -r -domain local -domain system -domain user`,
  );
  process.exit(1);
}
