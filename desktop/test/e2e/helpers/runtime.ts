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
