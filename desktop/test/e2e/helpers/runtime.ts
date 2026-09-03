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

import { processAlive, readDiscovery, waitFor } from './world';

export function tailText(text: string, lines: number): string {
  return text.split('\n').slice(-lines).join('\n');
}

export function requireDiscovery(world: Parameters<typeof readDiscovery>[0]) {
  const discovery = readDiscovery(world);
  if (discovery === null) throw new Error('expected an app-owned discovery record');
  return discovery;
}

export async function waitForNewServer(
  world: Parameters<typeof readDiscovery>[0],
  previousPid: number,
) {
  await waitFor(
    () => {
      const discovery = readDiscovery(world);
      return discovery !== null && discovery.pid !== previousPid && processAlive(discovery.pid);
    },
    `new app-owned server after pid ${previousPid}`,
    60_000,
  );
  return requireDiscovery(world);
}
