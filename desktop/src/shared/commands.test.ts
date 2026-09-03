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
 * The catalogue's own guarantees: palette visibility, the fifteen feature
 * verbs and their order, the shortcut regression (every pre-existing
 * accelerator still bound to its own command), and the single enablement rule
 * the Feature menu and the ⌘K palette share. The menu-side half of parity
 * lives in main/__tests__/menuTemplate.test.ts, next to the builder.
 */
import { describe, expect, it } from 'vitest';
import {
  COMMAND_CATALOGUE,
  FEATURE_COMMAND_IDS,
  NO_ACTIVE_FEATURE_REASON,
  UNAVAILABLE_ACTION_REASON,
  commandById,
  displayAccelerator,
  featureActionId,
  featureCommandEnablement,
  featureCommandState,
  isFeatureCommandId,
  type CommandId,
} from './commands';
/** The catalogue ids the palette actually builds an entry for. */
function paletteEntryIds(): string[] {
  return COMMAND_CATALOGUE.filter(
    (command) => command.group === 'feature' || command.paletteVisible === true,
  ).map((command) => command.id);
}

describe('command catalogue parity', () => {
  it('gives every palette-visible entry a palette entry', () => {
    const entries = new Set(paletteEntryIds());
    for (const command of COMMAND_CATALOGUE) {
      const expected = command.group === 'feature' || command.paletteVisible === true;
      expect(entries.has(command.id), `${command.id} palette visibility drifted`).toBe(expected);
    }
    // The deliberate menu-only exclusions stay out of the palette.
    for (const id of ['global.show', 'global.quit', 'global.close-window'] as const) {
      expect(entries.has(id)).toBe(false);
    }
  });

  it('lists exactly the fifteen feature verbs in catalogue order', () => {
    expect(FEATURE_COMMAND_IDS).toEqual([
      'feature.start',
      'feature.pause-stop',
      'feature.resume',
      'feature.retry',
      'feature.restart',
      'feature.rewind',
      'feature.publish',
      'feature.merge',
      'feature.mark-done',
      'feature.cleanup',
      'feature.rebase',
      'feature.refactor',
      'feature.review-feedback',
      'feature.configuration',
      'feature.delete',
    ]);
    expect(FEATURE_COMMAND_IDS.map((id) => commandById(id).label)).toEqual([
      'Start',
      'Stop',
      'Resume',
      'Retry',
      'Restart',
      'Rewind',
      'Publish',
      'Merge',
      'Mark done',
      'Clean up',
      'Rebase',
      'Refactor',
      'Address review feedback',
      'Configuration',
      'Delete',
    ]);
  });

  it('maps every feature command onto its server action, except the local editor', () => {
    expect(featureActionId('feature.pause-stop')).toBe('pause-stop');
    expect(featureActionId('feature.review-feedback')).toBe('review-feedback');
    expect(featureActionId('feature.configuration')).toBeNull();
    expect(isFeatureCommandId('feature.rewind')).toBe(true);
    expect(isFeatureCommandId('global.home')).toBe(false);
  });
});

describe('accelerator regression', () => {
  // Every accelerator that existed before the menu bar grew, plus ⌘N —
  // asserted as binding-to-command, not merely present somewhere.
  const EXPECTED: ReadonlyArray<[CommandId, string]> = [
    ['global.show', 'CommandOrControl+Shift+0'],
    ['global.palette', 'CommandOrControl+K'],
    ['global.help', 'CommandOrControl+/'],
    ['global.home', 'CommandOrControl+1'],
    ['global.settings', 'CommandOrControl+,'],
    ['global.attention', 'CommandOrControl+Shift+A'],
    ['global.ama', 'CommandOrControl+Shift+M'],
    ['global.bulk', 'CommandOrControl+Shift+B'],
    ['global.quit', 'CommandOrControl+Q'],
    ['global.close-window', 'CommandOrControl+W'],
    ['global.toggle-sidebar', 'Command+Control+S'],
    ['global.new-feature', 'CommandOrControl+N'],
  ];

  it('keeps every accelerator bound to its own command', () => {
    for (const [id, accelerator] of EXPECTED) {
      expect(commandById(id).accelerator, `${id} lost its accelerator`).toBe(accelerator);
    }
  });

  it('binds no accelerator twice', () => {
    const bound = COMMAND_CATALOGUE.filter((command) => command.accelerator !== undefined).map(
      (command) => command.accelerator,
    );
    expect(new Set(bound).size).toBe(bound.length);
  });

  it('renders macOS modifiers as symbols and other platforms as words', () => {
    // macOS always shows Control ahead of Command, however the accelerator is written.
    expect(displayAccelerator('Command+Control+S', 'MacIntel')).toBe('⌃ ⌘ S');
    expect(displayAccelerator('Command+Shift+Alt+A', 'MacIntel')).toBe('⌥ ⇧ ⌘ A');
    expect(displayAccelerator('CommandOrControl+N', 'MacIntel')).toBe('⌘ N');
    expect(displayAccelerator('CommandOrControl+N', 'Linux x86_64')).toBe('Ctrl N');
  });
});

describe('featureCommandState', () => {
  const actions = [
    { id: 'start', enabled: true, disabledReasons: [] },
    {
      id: 'pause-stop',
      enabled: false,
      disabledReasons: [{ code: 'not_running', message: 'nothing is running' }],
    },
  ];

  it('disables the whole group with the no-active-feature reason when nothing is selected', () => {
    for (const id of FEATURE_COMMAND_IDS) {
      expect(featureCommandState(id, null, { hasSelection: false })).toEqual({
        enabled: false,
        reason: NO_ACTIVE_FEATURE_REASON,
      });
    }
  });

  it('disables without a reason while a selected feature has no snapshot yet', () => {
    expect(featureCommandState('feature.start', null, { hasSelection: true })).toEqual({
      enabled: false,
    });
  });

  it('mirrors the action catalogue and carries its first disabled reason', () => {
    expect(featureCommandState('feature.start', actions, { hasSelection: true })).toEqual({
      enabled: true,
    });
    expect(featureCommandState('feature.pause-stop', actions, { hasSelection: true })).toEqual({
      enabled: false,
      reason: 'nothing is running',
    });
    // A verb the server never offered for this feature is unavailable, not absent.
    expect(featureCommandState('feature.merge', actions, { hasSelection: true })).toEqual({
      enabled: false,
      reason: UNAVAILABLE_ACTION_REASON,
    });
  });

  it('enables Configuration whenever a feature is selected', () => {
    expect(featureCommandState('feature.configuration', actions, { hasSelection: true })).toEqual({
      enabled: true,
    });
    expect(featureCommandState('feature.configuration', null, { hasSelection: true })).toEqual({
      enabled: false,
    });
  });

  it('builds the pushed enabled map over every verb', () => {
    const map = featureCommandEnablement(actions, { hasSelection: true });
    expect(Object.keys(map).sort()).toEqual([...FEATURE_COMMAND_IDS].sort());
    expect(map['feature.start']).toBe(true);
    expect(map['feature.pause-stop']).toBe(false);
    expect(map['feature.configuration']).toBe(true);
  });
});
