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

import { expect, test } from '@playwright/test';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { openScene, shoot, skipWithoutEvidenceDir } from './evidence-capture';

const modulePath = `/@fs${path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  '../../../src/renderer/src/features/featureCommands.ts',
)}`;

const captures = [
  {
    theme: 'light',
    muted: false,
    file: 'cockpit-configuration-modal-light-theme-overrides-set-the-notifications-group-of-1440x900',
  },
  {
    theme: 'light',
    muted: true,
    file: 'cockpit-configuration-modal-light-theme-feature-muted-the-notifications-group-wi-1440x900',
  },
  {
    theme: 'dark',
    muted: false,
    file: 'cockpit-configuration-modal-dark-theme-overrides-set-the-same-group-in-the-same-1440x900',
  },
] as const;

test('cockpit notifications modal evidence', async ({ page }) => {
  skipWithoutEvidenceDir();
  for (const { theme, muted, file } of captures) {
    await openScene(page, 'feature-question-bench', theme, 1440, 900, '.cockpit');
    const outcome = await page.evaluate(
      async ({ isMuted, modulePath: commandModule }) => {
        const original = window.agentico.getFeatureConfig;
        const base = await original('abcd1234ef567890');
        Object.assign(window.agentico, {
          getFeatureConfig: async () => ({
            ...base,
            current: {
              ...base.current,
              slackConfigured: true,
              slackDefaults: {
                categories: { progress: true, needsInput: true, problems: false },
                recipientNames: ['Workspace alerts'],
              },
              slackNotifications: {
                mode: isMuted ? 'muted' : '',
                recipients: [
                  {
                    typedText: '#release-alerts',
                    kind: 'channel',
                    id: 'C123',
                    displayName: 'Release alerts',
                  },
                ],
                progress: 'off',
                needsInput: '',
                problems: '',
              },
            },
          }),
        });
        const commands = await import(/* @vite-ignore */ commandModule);
        for (
          let attempt = 0;
          attempt < 50 && commands.activeFeatureCommandTarget() === null;
          attempt += 1
        ) {
          await new Promise((resolve) => setTimeout(resolve, 100));
        }
        return commands.runFeatureCommand('feature.configuration', {
          featureId: 'abcd1234ef567890',
        });
      },
      { isMuted: muted, modulePath },
    );
    expect(outcome).toBe('executed');
    const modal = page.getByRole('dialog', { name: 'Feature configuration' });
    await expect(modal.getByRole('group', { name: 'Notifications' })).toBeVisible();
    await expect(modal.getByRole('textbox', { name: 'Recipient 1' })).toHaveValue(
      '#release-alerts',
    );
    await modal.getByRole('group', { name: 'Notifications' }).scrollIntoViewIfNeeded();
    if (muted) {
      await expect(modal.getByText('Category choices have no effect while muted.')).toBeVisible();
    } else {
      await expect(modal.getByRole('combobox', { name: 'Progress' })).toHaveValue('off');
    }
    await shoot(page, file);
  }
});
