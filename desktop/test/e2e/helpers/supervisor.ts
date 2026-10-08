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
 * Supervisor-page locators and the main-process test hooks the supervisor
 * lifecycle journeys share: the composer and model chip, the provider stub's
 * invocation log, the captured OS notifications, the pinned focus signal and
 * the tray's native-command state.
 */
import fs from 'node:fs';
import { expect, type Locator, type Page } from '@playwright/test';
import type { AppHandle } from './app';
import { waitFor, type JourneyWorld } from './world';

/** The pinned first sidebar row; exact so a feature row naming the supervisor never matches. */
export function supervisorRow(page: Page): Locator {
  return page.getByRole('option', { name: 'Supervisor', exact: true });
}

export function supervisorPage(page: Page): Locator {
  return page.getByRole('region', { name: 'Supervisor', exact: true });
}

export function supervisorComposer(page: Page): Locator {
  return page.getByRole('textbox', { name: 'Message the supervisor' });
}

function supervisorSendButton(page: Page): Locator {
  return supervisorPage(page)
    .locator('.supervisor-page__dock')
    .getByRole('button', { name: 'Send', exact: true });
}

/** Commits Claude · Haiku through the harness-and-model chip. */
export async function chooseSupervisorModel(page: Page): Promise<void> {
  await page.getByTestId('supervisor-model-chip').click();
  const popover = page.getByRole('region', { name: 'Harness and model' });
  await expect(popover).toBeVisible();
  // Catalogues may use the short fixture label or the canonical context label.
  const model = popover.getByRole('group', { name: 'Claude' }).getByRole('radio', {
    name: /^(?:Claude )?Haiku(?: \(200K\))?$/,
  });
  const label = (
    await model.locator('..').locator('.supervisor-chip__option-name').innerText()
  ).trim();
  await model.locator('..').click();
  await expect(page.getByTestId('supervisor-model-chip')).toHaveAccessibleName(
    `Claude ${label} · Default`,
  );
  await page.keyboard.press('Escape');
  await expect(popover).toHaveCount(0);
  await expect(supervisorComposer(page)).toHaveAttribute('placeholder', 'Message the supervisor');
}

export async function sendSupervisorMessage(page: Page, text: string): Promise<void> {
  await supervisorComposer(page).fill(text);
  await expect(supervisorSendButton(page)).toBeEnabled();
  await supervisorSendButton(page).click();
}

export function readProviderLog(world: JourneyWorld): string {
  try {
    return fs.readFileSync(world.providerInvocationLog, 'utf8');
  } catch {
    return '';
  }
}

export async function waitForProviderLog(world: JourneyWorld, needle: string): Promise<void> {
  await waitFor(
    () => readProviderLog(world).includes(needle),
    `provider log to contain ${needle}`,
    30_000,
  );
}

export interface CapturedNotification {
  title: string;
  body: string;
}

/** Replaces the Electron notification surface with an in-memory capture. */
export async function installNotificationCapture(handle: AppHandle): Promise<void> {
  await handle.app.evaluate(({ Notification }) => {
    const global = globalThis as typeof globalThis & {
      __agenticoNotifications?: Array<{ title: string; body: string; click?: () => void }>;
    };
    global.__agenticoNotifications = [];
    Notification.isSupported = () => true;
    Notification.prototype.on = function patchedOn(
      this: { __agenticoClick?: () => void },
      event: string,
      listener: () => void,
    ) {
      if (event === 'click') this.__agenticoClick = listener;
      return this;
    } as typeof Notification.prototype.on;
    Notification.prototype.show = function patchedShow(this: {
      title?: string;
      body?: string;
      __agenticoClick?: () => void;
    }) {
      global.__agenticoNotifications?.push({
        title: this.title ?? '',
        body: this.body ?? '',
        click: this.__agenticoClick,
      });
      return this;
    } as typeof Notification.prototype.show;
  });
}

export async function capturedNotifications(handle: AppHandle): Promise<CapturedNotification[]> {
  return handle.app.evaluate(() => {
    const global = globalThis as typeof globalThis & {
      __agenticoNotifications?: Array<{ title: string; body: string }>;
    };
    return (global.__agenticoNotifications ?? []).map(({ title, body }) => ({ title, body }));
  });
}

/** Fires the click listener of the captured notification at `index`. */
export async function activateNotification(handle: AppHandle, index: number): Promise<void> {
  await handle.app.evaluate((_electron, notificationIndex) => {
    const global = globalThis as typeof globalThis & {
      __agenticoNotifications?: Array<{ click?: () => void }>;
    };
    const notification = global.__agenticoNotifications?.[notificationIndex];
    if (notification?.click === undefined) {
      throw new Error(`notification ${notificationIndex} has no click listener`);
    }
    notification.click();
  }, index);
}

/**
 * Pins the main window's effective focus through the packaged-test override
 * (it still requires the window to be visible), so another window on a
 * shared display cannot rewrite the scenario.
 */
export async function setMainWindowFocusOverride(
  handle: AppHandle,
  focused: boolean,
): Promise<void> {
  await handle.app.evaluate((_electron, value) => {
    const global = globalThis as typeof globalThis & {
      __agenticoSetMainWindowAttentionFocusOverride?: (focused: boolean) => void;
    };
    if (global.__agenticoSetMainWindowAttentionFocusOverride === undefined) {
      throw new Error('the focus override test hook is not installed');
    }
    global.__agenticoSetMainWindowAttentionFocusOverride(value);
  }, focused);
}

/** Hides the main window without triggering a background refresh. */
export async function hideMainWindow(handle: AppHandle): Promise<void> {
  await handle.app.evaluate(({ BrowserWindow }) => {
    const window = BrowserWindow.getAllWindows()[0];
    if (window === undefined) throw new Error('main window missing');
    window.hide();
  });
}

export async function mainWindowVisible(handle: AppHandle): Promise<boolean> {
  return handle.app.evaluate(({ BrowserWindow }) => {
    const window = BrowserWindow.getAllWindows()[0];
    return window !== undefined && window.isVisible();
  });
}

export type SupervisorGrade = 'working' | 'waiting' | 'idle';

/** The tray's lifecycle grade, as the packaged-test native-command state reports it. */
export async function trayGrade(handle: AppHandle): Promise<SupervisorGrade | null> {
  return handle.app.evaluate(() => {
    const global = globalThis as typeof globalThis & {
      __agenticoNativeCommandState?: { supervisorGrade: SupervisorGrade };
    };
    return global.__agenticoNativeCommandState?.supervisorGrade ?? null;
  });
}
