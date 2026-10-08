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

const { buildTrayMenuTemplate, supervisorGradeLabel, trayToolTip } =
  await import('../nativeCommands');
const { supervisorGrade } = await import('../quitCoordinator');

function handlers() {
  return { showWindow: vi.fn(), route: vi.fn(), quit: vi.fn() };
}

describe('tray', () => {
  it('names the supervisor lifecycle grade in the tooltip', () => {
    expect(trayToolTip({ attentionCount: 2, supervisorGrade: 'working' })).toBe(
      'Agentico - 2 attention - Supervisor working',
    );
    expect(trayToolTip({ attentionCount: 1, supervisorGrade: 'waiting' })).toBe(
      'Agentico - 1 attention - Supervisor waiting for you',
    );
    expect(trayToolTip({ attentionCount: 0, supervisorGrade: 'idle' })).toBe(
      'Agentico - 0 attention - Supervisor idle',
    );
  });

  it('maps each lifecycle onto its tray grade', () => {
    const grades = Object.fromEntries(
      (
        [
          'stopped',
          'starting',
          'idle',
          'running',
          'waiting_permission',
          'waiting_question',
          'failed',
        ] as const
      ).map((lifecycle) => [lifecycle, supervisorGradeLabel(supervisorGrade(lifecycle))]),
    );
    expect(grades).toEqual({
      stopped: 'Supervisor idle',
      starting: 'Supervisor working',
      idle: 'Supervisor idle',
      running: 'Supervisor working',
      waiting_permission: 'Supervisor waiting for you',
      waiting_question: 'Supervisor waiting for you',
      failed: 'Supervisor idle',
    });
  });

  it('offers a Supervisor item that reads the grade and routes home', () => {
    const deps = handlers();
    const labels = (['working', 'waiting', 'idle'] as const).map((supervisorGrade) =>
      buildTrayMenuTemplate({ attentionCount: 0, supervisorGrade }, deps).map((item) => item.label),
    );
    expect(labels[0]).toContain('Supervisor working');
    expect(labels[1]).toContain('Supervisor waiting for you');
    expect(labels[2]).toContain('Supervisor idle');

    const waiting = buildTrayMenuTemplate({ attentionCount: 0, supervisorGrade: 'waiting' }, deps);
    const item = waiting.find((entry) => entry.label === 'Supervisor waiting for you');
    expect(item).toBeDefined();
    item?.click?.({} as never, undefined, {} as never);
    expect(deps.route).toHaveBeenCalledWith({ target: 'home' });
    expect(JSON.stringify(labels)).not.toMatch(/AMA/);
  });
});
