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

// Backward-compatible release credential entry point for local operators.
import { cleanupReleaseWorkspace, runReleasePreflight } from './release-preflight.mjs';

let evidence;
try {
  evidence = runReleasePreflight();
  console.log(`release credentials and local prerequisites verified for ${evidence.tag}`);
} catch (error) {
  console.error(error instanceof Error ? error.message : String(error));
  process.exitCode = 1;
} finally {
  if (evidence !== undefined) cleanupReleaseWorkspace({ evidence });
}
