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

import { act, render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { dispatchMediaChange, matchMediaState } from '../test/setup';
import { AgenticoActivityMark } from './AgenticoActivityMark';

const animate = vi.fn((_frames: Keyframe[], _options: KeyframeAnimationOptions) => ({
  cancel: vi.fn(),
}));
const originalAnimate = SVGElement.prototype.animate;

afterEach(() => {
  SVGElement.prototype.animate = originalAnimate;
  animate.mockClear();
  matchMediaState.reducedMotion = false;
  vi.restoreAllMocks();
});

function installAnimation() {
  SVGElement.prototype.animate = animate as unknown as typeof SVGElement.prototype.animate;
}

describe('AgenticoActivityMark', () => {
  it('keeps the orbit through rerenders and resolves from its current pose', () => {
    installAnimation();
    const pose = 'path("M 5 6 C 8 4 14 4 18 8")';
    vi.spyOn(window, 'getComputedStyle').mockReturnValue({ d: pose } as CSSStyleDeclaration);
    const { container, rerender, unmount } = render(<AgenticoActivityMark state="working" />);
    const paths = [...container.querySelectorAll('path')];
    expect(animate).toHaveBeenCalledTimes(2);
    const orbits = animate.mock.results.map((result) => result.value);
    rerender(<AgenticoActivityMark state="working" />);
    expect(animate).toHaveBeenCalledTimes(2);
    rerender(<AgenticoActivityMark state="complete" />);
    expect([...container.querySelectorAll('path')]).toEqual(paths);
    expect(animate).toHaveBeenCalledTimes(4);
    expect(animate.mock.calls[2]?.[0]).toEqual([
      { d: pose },
      { d: 'path("M 5.5 12.5 C 7 14 8.5 15.5 10 17")' },
    ]);
    orbits.forEach((animation) => expect(animation.cancel).toHaveBeenCalledOnce());
    unmount();
    animate.mock.results.forEach((result) => expect(result.value.cancel).toHaveBeenCalledOnce());
  });

  it('renders historical completion without replaying the animation', () => {
    installAnimation();
    const { container } = render(<AgenticoActivityMark state="complete" />);
    expect(animate).not.toHaveBeenCalled();
    expect(container.querySelector('path')?.style.d).toContain('5.5 12.5');
  });

  it('stops when reduced motion is enabled, and never animates the finish', () => {
    installAnimation();
    const { rerender } = render(<AgenticoActivityMark state="working" />);
    const orbits = animate.mock.results.map((result) => result.value);
    act(() => dispatchMediaChange('(prefers-reduced-motion: reduce)', true));
    orbits.forEach((animation) => expect(animation.cancel).toHaveBeenCalledOnce());
    rerender(<AgenticoActivityMark state="complete" />);
    expect(animate).toHaveBeenCalledTimes(2);
  });

  it('stops on a non-successful outcome and can resume working', () => {
    installAnimation();
    const { container, rerender } = render(<AgenticoActivityMark state="working" />);
    rerender(<AgenticoActivityMark state="resting" />);
    expect(animate).toHaveBeenCalledTimes(2);
    expect(container.querySelector('path')?.style.d).toContain('7.73 18.98');
    rerender(<AgenticoActivityMark state="working" />);
    expect(animate).toHaveBeenCalledTimes(4);
  });
});
