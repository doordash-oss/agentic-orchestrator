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
 * Geometry maths for the floating AMA panel. Kept out of the component so the
 * clamp — the one invariant the panel must never violate, at restore, after a
 * drag or resize, and while the window is resized around it — is testable on
 * its own.
 */
import { AMA_PANEL_MIN_HEIGHT, AMA_PANEL_MIN_WIDTH, type AmaGeometry } from '../../../shared/ipc';

export interface Viewport {
  width: number;
  height: number;
}

/** The panel's resizable sides; corners are the two-axis combinations. */
export type ResizeEdge = 'n' | 's' | 'e' | 'w' | 'ne' | 'nw' | 'se' | 'sw';

export const RESIZE_EDGES: readonly ResizeEdge[] = ['n', 's', 'e', 'w', 'ne', 'nw', 'se', 'sw'];

function bound(value: number, min: number, max: number): number {
  return Math.round(Math.min(Math.max(value, min), Math.max(min, max)));
}

/**
 * Fits `geometry` entirely inside `viewport`, shrinking below the usable
 * minimum only when the window itself is smaller than that minimum.
 */
export function clampAmaGeometry(geometry: AmaGeometry, viewport: Viewport): AmaGeometry {
  const availableWidth = Math.max(1, Math.floor(viewport.width));
  const availableHeight = Math.max(1, Math.floor(viewport.height));
  const width = bound(
    geometry.width,
    Math.min(AMA_PANEL_MIN_WIDTH, availableWidth),
    availableWidth,
  );
  const height = bound(
    geometry.height,
    Math.min(AMA_PANEL_MIN_HEIGHT, availableHeight),
    availableHeight,
  );
  return {
    width,
    height,
    right: bound(geometry.right, 0, availableWidth - width),
    bottom: bound(geometry.bottom, 0, availableHeight - height),
  };
}

/** The panel moved by a pointer delta, still anchored to the bottom-right. */
export function dragAmaGeometry(
  start: AmaGeometry,
  delta: { x: number; y: number },
  viewport: Viewport,
): AmaGeometry {
  return clampAmaGeometry(
    { ...start, right: start.right - delta.x, bottom: start.bottom - delta.y },
    viewport,
  );
}

/**
 * The panel resized from one edge or corner. Trailing/bottom edges move the
 * anchor with the pointer; leading/top edges grow the panel away from it.
 */
export function resizeAmaGeometry(
  start: AmaGeometry,
  edge: ResizeEdge,
  delta: { x: number; y: number },
  viewport: Viewport,
): AmaGeometry {
  let { right, bottom, width, height } = start;
  if (edge.includes('e')) {
    right = start.right - delta.x;
    width = start.width + delta.x;
  }
  if (edge.includes('w')) {
    width = start.width - delta.x;
  }
  if (edge.includes('s')) {
    bottom = start.bottom - delta.y;
    height = start.height + delta.y;
  }
  if (edge.includes('n')) {
    height = start.height - delta.y;
  }
  return clampAmaGeometry({ right, bottom, width, height }, viewport);
}
