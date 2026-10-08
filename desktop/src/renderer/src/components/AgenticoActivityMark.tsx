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

import { useLayoutEffect, useRef } from 'react';
import { usePrefersReducedMotion } from '../hooks';

export type AgenticoActivityState = 'working' | 'complete' | 'resting';

// The app monogram's two strokes, scaled from its 1024-unit viewBox to 24.
// Cubics keep the same topology through the orbit and the final checkmark.
const MONOGRAM = [
  'M 7.73 18.98 C 9.15 14.61 10.58 10.23 12 5.86',
  'M 16.27 18.98 C 14.85 14.61 13.42 10.23 12 5.86',
] as const;
const CHECK = ['M 5.5 12.5 C 7 14 8.5 15.5 10 17', 'M 10 17 C 13 13.67 16 10.33 19 7'];

// Open into an orbit, wind inward, cross in an S, then unfurl with the
// opposite handedness. The inward tips leave air between the two strokes
// even at 26px; the monogram returns once per complete phrase.
const FLOW_POSES = [
  [MONOGRAM[0], MONOGRAM[1]],
  ['M 5.5 16 C 3.5 10 7 4.5 13.5 5', 'M 18.5 8 C 20.5 14 17 19.5 10.5 19'],
  ['M 7 5 C 22 1 23 22 12 15', 'M 17 19 C 2 23 1 2 12 9'],
  ['M 19 8 C 23 23 2 23 9 12', 'M 5 16 C 1 1 22 1 15 12'],
  ['M 5 9 C 18 2 6 22 19 15', 'M 5 15 C 18 22 6 2 19 9'],
  ['M 17 5 C 2 1 1 22 12 15', 'M 7 19 C 22 23 23 2 12 9'],
  ['M 5 8 C 1 23 22 23 15 12', 'M 19 16 C 23 1 2 1 9 12'],
  ['M 17 18.5 C 11 20.5 4.5 17 5 10.5', 'M 7 5.5 C 13 3.5 19.5 7 19 13.5'],
];
const pathValue = (d: string) => `path("${d}")`;

// Sample a closed Catmull–Rom curve through the choreography once, not on
// every render. Continuous tangents carry the strokes through each pose and
// across the loop seam, avoiding a stop/start at each change of direction.
const FLOW_FRAMES = [0, 1].map((stroke) => {
  const poses = FLOW_POSES.map((pair) => pair[stroke]!.match(/-?\d+(?:\.\d+)?/g)!.map(Number));
  const frames: Keyframe[] = [];
  for (let segment = 0; segment < poses.length; segment++) {
    const before = poses[(segment + poses.length - 1) % poses.length]!;
    const start = poses[segment]!;
    const end = poses[(segment + 1) % poses.length]!;
    const after = poses[(segment + 2) % poses.length]!;
    for (let sample = 0; sample < 16; sample++) {
      const t = sample / 16;
      const coordinates = start.map((p, axis) => {
        const a = before[axis]!;
        const b = end[axis]!;
        const c = after[axis]!;
        return (
          0.5 *
          (2 * p +
            (b - a) * t +
            (2 * a - 5 * p + 4 * b - c) * t * t +
            (3 * p - a - 3 * b + c) * t * t * t)
        ).toFixed(3);
      });
      frames.push({
        d: pathValue(`M ${coordinates.slice(0, 2).join(' ')} C ${coordinates.slice(2).join(' ')}`),
      });
    }
  }
  frames.push(frames[0]!);
  return frames;
});

/** The same two paths live through work and completion; historical ticks stay still. */
export function AgenticoActivityMark({ state }: { state: AgenticoActivityState }) {
  const svg = useRef<SVGSVGElement>(null);
  const previous = useRef<AgenticoActivityState | null>(null);
  const pose = useRef<string[]>([]);
  const reducedMotion = usePrefersReducedMotion();

  useLayoutEffect(() => {
    const paths = [...(svg.current?.querySelectorAll('path') ?? [])];
    const wasWorking = previous.current === 'working';
    previous.current = state;
    const animations: Animation[] = [];

    paths.forEach((path, index) => {
      const target = pathValue(state === 'complete' ? CHECK[index]! : MONOGRAM[index]!);
      path.style.d = target;
      if (reducedMotion || typeof path.animate !== 'function') return;

      if (state === 'working') {
        animations.push(
          path.animate(FLOW_FRAMES[index]!, { duration: 7200, iterations: Infinity }),
        );
      } else if (state === 'complete' && wasWorking) {
        // Capture happens in the previous effect's cleanup, before cancelling
        // the orbit. Any point in the cycle can resolve without a snap to A.
        animations.push(
          path.animate([{ d: pose.current[index] ?? target }, { d: target }], {
            duration: 640,
            easing: 'cubic-bezier(0.22, 1, 0.36, 1)',
          }),
        );
      }
    });

    return () => {
      pose.current = paths.map((path) => getComputedStyle(path).d);
      animations.forEach((animation) => animation.cancel());
    };
  }, [state, reducedMotion]);

  return (
    <svg
      ref={svg}
      className="agentico-activity-mark"
      data-state={state}
      viewBox="0 0 24 24"
      width="26"
      height="26"
      fill="none"
      strokeWidth="3.5"
      strokeLinecap="round"
      aria-hidden="true"
      focusable="false"
    >
      <path d={MONOGRAM[0]} />
      <path d={MONOGRAM[1]} />
    </svg>
  );
}
