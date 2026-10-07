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

import { describe, expect, it, vi } from 'vitest';

// The tray builders are pure; the module's Electron imports only need to load.
vi.mock('electron', () => ({
  BrowserWindow: {},
  Menu: {},
  Tray: class {},
  nativeImage: {},
}));

const { buildTrayMenuTemplate, trayToolTip } = await import('../nativeCommands');

function handlers() {
  return { showWindow: vi.fn(), route: vi.fn(), quit: vi.fn() };
}

describe('tray', () => {
  it('names the supervisor lifecycle in the tooltip', () => {
    expect(trayToolTip({ attentionCount: 2, supervisorActive: true })).toBe(
      'Agentico - 2 attention - Supervisor working',
    );
    expect(trayToolTip({ attentionCount: 0, supervisorActive: false })).toBe(
      'Agentico - 0 attention - Supervisor idle',
    );
  });

  it('offers a Supervisor item that reflects the lifecycle and routes home', () => {
    const deps = handlers();
    const idle = buildTrayMenuTemplate({ attentionCount: 0, supervisorActive: false }, deps);
    const working = buildTrayMenuTemplate({ attentionCount: 0, supervisorActive: true }, deps);

    expect(idle.map((item) => item.label)).toContain('Supervisor');
    const item = working.find((entry) => entry.label === 'Supervisor (working)');
    expect(item).toBeDefined();
    item?.click?.({} as never, undefined, {} as never);
    expect(deps.route).toHaveBeenCalledWith({ target: 'home' });
    expect(JSON.stringify([...idle, ...working].map((entry) => entry.label))).not.toMatch(/AMA/);
  });
});
