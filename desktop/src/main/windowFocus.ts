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
 * The main window's effective focus: the one value both the attention
 * notification gate and the renderer's supervisor unread rules read. The
 * window counts as focused only while it exists, is visible, and holds focus
 * — with the packaged-test override, when set, standing in for the ambient
 * focus flag so one hook pins both consumers.
 */
export interface WindowFocusSignalDeps {
  /** True while a main window exists and is visible. */
  isVisible(): boolean;
  /** Receives the effective value on every publish. */
  publish(focused: boolean): void;
}

export class WindowFocusSignal {
  private focused = false;
  private override: boolean | undefined;
  private readonly listeners = new Set<(focused: boolean) => void>();

  constructor(private readonly deps: WindowFocusSignalDeps) {}

  /** The ambient focus flag from the window's focus, blur, show and hide events. */
  setFocused(focused: boolean): void {
    this.focused = focused;
    this.emit();
  }

  /** Packaged-test override; `undefined` returns to the ambient flag. */
  setOverride(focused: boolean | undefined): void {
    this.override = focused;
    this.emit();
  }

  /** Visible and focused, the override applied. */
  effective(): boolean {
    return this.deps.isVisible() && (this.override ?? this.focused);
  }

  /** Main-process consumers of every publish; returns the unsubscribe. */
  subscribe(listener: (focused: boolean) => void): () => void {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  }

  /** Publishes the current effective value (visibility changes, ready, reload). */
  emit(): void {
    const focused = this.effective();
    this.deps.publish(focused);
    for (const listener of this.listeners) listener(focused);
  }
}
