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

import { describe, expect, it } from 'vitest';
import { SUPERVISOR_INSPIRATIONS, greetingFor, inspirationFor } from './supervisorWelcome';

describe('greetingFor', () => {
  it('follows the local clock', () => {
    expect(greetingFor(new Date(2026, 9, 8, 8, 0))).toBe('Good morning.');
    expect(greetingFor(new Date(2026, 9, 8, 13, 0))).toBe('Good afternoon.');
    expect(greetingFor(new Date(2026, 9, 8, 20, 0))).toBe('Good evening.');
    expect(greetingFor(new Date(2026, 9, 8, 2, 0))).toBe('Good evening.');
  });
});

describe('inspirationFor', () => {
  it('is stable within a day and rotates across days', () => {
    const morning = new Date(2026, 9, 8, 8, 0);
    const night = new Date(2026, 9, 8, 23, 0);
    const tomorrow = new Date(2026, 9, 9, 8, 0);
    expect(inspirationFor(morning)).toBe(inspirationFor(night));
    expect(inspirationFor(tomorrow)).not.toBe(inspirationFor(morning));
  });

  it('always returns a line from the catalogue', () => {
    for (let day = 0; day < 400; day += 1) {
      const date = new Date(2026, 0, 1 + day, 12, 0);
      expect(SUPERVISOR_INSPIRATIONS).toContain(inspirationFor(date));
    }
  });
});
