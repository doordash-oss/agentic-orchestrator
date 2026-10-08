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

import { vi } from 'vitest';

/** Every visible direct child of a laid-out scroll container is this tall. */
export const ROW_HEIGHT_PX = 50;
/** The laid-out scroll container's viewport height. */
export const VIEWPORT_HEIGHT_PX = 400;

function rect(top: number, height: number): DOMRect {
  return {
    top,
    bottom: top + height,
    left: 0,
    right: 0,
    width: 0,
    height,
    x: 0,
    y: top,
    toJSON: () => ({}),
  };
}

function rowHeight(element: Element): number {
  return element instanceof HTMLElement && element.hidden ? 0 : ROW_HEIGHT_PX;
}

/**
 * Gives jsdom a deterministic block layout for scroll containers matched by
 * `isContainer`: every visible direct child is one fixed-height row stacked
 * in DOM order, offset by the container's `scrollTop`, and the container
 * reports a fixed viewport with a scroll height covering its rows. Returns
 * the restore function.
 */
export function installTranscriptLayout(isContainer: (element: Element) => boolean): () => void {
  const contentHeight = (container: Element): number =>
    [...container.children].reduce((total, child) => total + rowHeight(child), 0);
  const spies = [
    vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (
      this: Element,
    ) {
      if (isContainer(this)) return rect(0, VIEWPORT_HEIGHT_PX);
      const container = this.parentElement;
      if (container === null || !isContainer(container)) return rect(0, 0);
      let top = -container.scrollTop;
      for (
        let sibling = container.firstElementChild;
        sibling !== null && sibling !== this;
        sibling = sibling.nextElementSibling
      ) {
        top += rowHeight(sibling);
      }
      return rect(top, rowHeight(this));
    }),
    vi.spyOn(Element.prototype, 'scrollHeight', 'get').mockImplementation(function (this: Element) {
      return isContainer(this) ? Math.max(contentHeight(this), VIEWPORT_HEIGHT_PX + 1) : 0;
    }),
    vi.spyOn(Element.prototype, 'clientHeight', 'get').mockImplementation(function (this: Element) {
      return isContainer(this) ? VIEWPORT_HEIGHT_PX : 0;
    }),
  ];
  return () => {
    for (const spy of spies) spy.mockRestore();
  };
}

/** The viewport offset of `row` inside its laid-out container. */
export function viewportOffset(row: Element): number {
  return row.getBoundingClientRect().top;
}
