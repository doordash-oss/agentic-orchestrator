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

import { EventEmitter } from 'node:events';
import { describe, expect, it, vi } from 'vitest';
import { FAULT_LOG_PREFIX, describeFault, installMainProcessFaultHandlers } from '../faults';

describe('main-process fault handlers', () => {
  it('claims both fault events so the host runtime never falls back to a modal', () => {
    const emitter = new EventEmitter();
    installMainProcessFaultHandlers({ process: emitter, log: vi.fn() });
    expect(emitter.listenerCount('uncaughtException')).toBe(1);
    expect(emitter.listenerCount('unhandledRejection')).toBe(1);
  });

  it('logs the stack and records a main-process crash once a recorder is bound', () => {
    const emitter = new EventEmitter();
    const log = vi.fn();
    const handlers = installMainProcessFaultHandlers({ process: emitter, log });
    const recordCrash = vi.fn();
    handlers.setRecorder({ recordCrash });

    const error = new Error('child exit listener threw');
    emitter.emit('uncaughtException', error);

    expect(log).toHaveBeenCalledWith(`${FAULT_LOG_PREFIX} uncaught exception: ${error.stack}`);
    expect(recordCrash).toHaveBeenCalledWith({
      processRole: 'main',
      category: 'uncaught exception',
      context: error.stack,
    });
  });

  it('describes non-Error rejection reasons and survives before a recorder exists', () => {
    const emitter = new EventEmitter();
    const log = vi.fn();
    installMainProcessFaultHandlers({ process: emitter, log });

    emitter.emit('unhandledRejection', { code: 'E_TEST' });

    expect(log).toHaveBeenCalledWith(`${FAULT_LOG_PREFIX} unhandled rejection: {"code":"E_TEST"}`);
  });

  it('keeps running when the recorder itself throws', () => {
    const emitter = new EventEmitter();
    const log = vi.fn();
    const handlers = installMainProcessFaultHandlers({ process: emitter, log });
    handlers.setRecorder({
      recordCrash: () => {
        throw new Error('disk full');
      },
    });

    expect(() => emitter.emit('uncaughtException', new Error('boom'))).not.toThrow();
    expect(log.mock.calls.map(([line]) => String(line))).toEqual([
      expect.stringContaining('uncaught exception: Error: boom'),
      expect.stringContaining('recording the fault failed: Error: disk full'),
    ]);
  });

  it('describeFault falls back to String() for unserialisable values', () => {
    const cyclic: Record<string, unknown> = {};
    cyclic['self'] = cyclic;
    expect(describeFault(cyclic)).toBe('[object Object]');
    expect(describeFault('plain')).toBe('plain');
  });
});
