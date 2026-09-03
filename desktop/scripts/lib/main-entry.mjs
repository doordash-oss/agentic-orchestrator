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

// Resolve Node's argv entry point and ESM module URL through symlinks before comparing them.
import { realpathSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

/** Return whether the supplied ESM module is the script Node was asked to execute. */
export function isMainModule(moduleUrl, entryPoint = process.argv[1]) {
  if (typeof entryPoint !== 'string' || entryPoint === '') return false;
  return realpathSync(entryPoint) === realpathSync(fileURLToPath(moduleUrl));
}
