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
import {
  isVerifyingPhase,
  verificationCounts,
  verificationSymbol,
  verificationTone,
} from './verificationModel';

describe('verificationModel', () => {
  it('maps harness states onto display tones', () => {
    expect(verificationTone('passed')).toBe('passed');
    expect(verificationTone('running')).toBe('running');
    expect(verificationTone('pending')).toBe('pending');
    expect(verificationTone('')).toBe('pending');
    expect(verificationTone('failed')).toBe('failed');
    expect(verificationTone('blocked')).toBe('failed');
    expect(verificationTone('inherited_failure')).toBe('failed');
    expect(verificationTone('waived')).toBe('neutral');
    expect(verificationTone('not_run')).toBe('neutral');
    expect(verificationTone('pending_human')).toBe('neutral');
  });

  it('renders the glyph vocabulary', () => {
    expect(verificationSymbol('passed')).toBe('✓');
    expect(verificationSymbol('running')).toBe('⟳');
    expect(verificationSymbol('pending')).toBe('·');
    expect(verificationSymbol('failed')).toBe('✕');
    expect(verificationSymbol('waived')).toBe('•');
  });

  it('counts done as any terminal verdict and failures across failure tones', () => {
    const counts = verificationCounts([
      { name: 'a', state: 'passed' },
      { name: 'b', state: 'failed' },
      { name: 'c', state: 'blocked' },
      { name: 'd', state: 'running' },
      { name: 'e', state: 'pending' },
      { name: 'f', state: 'waived' },
    ]);
    // running + pending are not done; the other four have a verdict.
    expect(counts).toEqual({ total: 6, done: 4, failed: 2 });
  });

  it('is verifying only when the phase status is verifying and items exist', () => {
    const items = [{ name: 'go test', state: 'running' }];
    expect(isVerifyingPhase('verifying', items)).toBe(true);
    expect(isVerifyingPhase('Verifying', items)).toBe(true);
    expect(isVerifyingPhase('verifying', [])).toBe(false);
    expect(isVerifyingPhase('verifying', undefined)).toBe(false);
    expect(isVerifyingPhase('implementing', items)).toBe(false);
    expect(isVerifyingPhase(undefined, items)).toBe(false);
  });
});
