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
 * Shared test helpers for seeding feature state on disk while the bundled
 * server is stopped. Extracted from journey specs to eliminate the
 * triplicated setFeatureStatus copies that drifted across lifecycle-passes,
 * bulk-resume-retry, and recovery-orphans specs. persistAppLogs and
 * evidenceDir live in ./app and are re-exported here so journey specs keep a
 * single import surface without duplicating evidence-writing logic.
 */
import fs from 'node:fs';
import path from 'node:path';

import { replaceTopLevelBlock } from './yaml';

// Re-export the canonical evidence helpers from ./app so journey specs that
// import them from ./seed keep working without a second copy of the logic.
export { persistAppLogs, evidenceDir } from './app';

/**
 * Sets a feature's status in feature.yaml. When the target status is not
 * "Failed", also clears the run-level failure record from the active run's
 * run.yaml so reconcileTerminalRunFailure does not overwrite the seeded
 * status back to Failed on the next server load.
 */
export function setFeatureStatus(stateDir: string, featureId: string, status: string): void {
  const featurePath = path.join(stateDir, featureId, 'feature.yaml');
  let yaml = fs.readFileSync(featurePath, 'utf8');
  const pattern = /^status:.*$/m;
  if (pattern.test(yaml)) {
    yaml = yaml.replace(pattern, `status: ${status}`);
  } else {
    yaml = `${yaml.endsWith('\n') ? yaml : `${yaml}\n`}status: ${status}\n`;
  }
  fs.writeFileSync(featurePath, yaml);

  if (status === 'Failed') return;
  clearRunFailures(stateDir, featureId);
}

/**
 * Removes the failure record block from the active run's run.yaml so
 * Feature.reconcileTerminalRunFailure does not revert a non-failed status
 * to StatusFailed on load.
 */
function clearRunFailures(stateDir: string, featureId: string): void {
  const featurePath = path.join(stateDir, featureId, 'feature.yaml');
  const featureYaml = fs.readFileSync(featurePath, 'utf8');
  const activeRunMatch = featureYaml.match(/^active_run:\s*(\d+)/m);
  const activeRun = activeRunMatch !== null ? activeRunMatch[1]! : '1';
  const runPath = path.join(
    stateDir,
    featureId,
    'runs',
    `run-${activeRun.padStart(3, '0')}`,
    'run.yaml',
  );
  if (!fs.existsSync(runPath)) return;
  const runYaml = fs.readFileSync(runPath, 'utf8');
  fs.writeFileSync(runPath, replaceTopLevelBlock(runYaml, 'failure', []));
}
