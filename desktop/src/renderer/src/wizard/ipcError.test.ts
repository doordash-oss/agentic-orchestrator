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
import { parseIpcError } from './ipcError';

describe('parseIpcError', () => {
  it('preserves preload metadata apart from the display message', () => {
    const remediation = 'Review and reconcile the branch on GitHub, then refresh and retry.';
    const error = Object.assign(
      new Error(`publish_remote_diverged: safe display message ${remediation}`),
      { code: 'publish_remote_diverged', remediation },
    );

    expect(parseIpcError(error)).toEqual({
      code: 'publish_remote_diverged',
      message: 'safe display message',
      remediation,
    });
  });
});
