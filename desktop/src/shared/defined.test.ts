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
import { defined, definedNonEmpty } from './defined';

describe('defined', () => {
  it('drops undefined and retains null, false, zero and empty string', () => {
    expect(defined('x', undefined)).toEqual({});
    expect(defined('x', null)).toEqual({ x: null });
    expect(defined('x', false)).toEqual({ x: false });
    expect(defined('x', 0)).toEqual({ x: 0 });
    expect(defined('x', '')).toEqual({ x: '' });
  });

  it('drops undefined and empty string only when explicitly requested', () => {
    expect(definedNonEmpty('x', undefined)).toEqual({});
    expect(definedNonEmpty('x', '')).toEqual({});
    expect(definedNonEmpty('x', null)).toEqual({ x: null });
    expect(definedNonEmpty('x', 0)).toEqual({ x: 0 });
  });
});
