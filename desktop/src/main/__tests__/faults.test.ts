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
import {
  FAULT_LOG_PREFIX,
  TOLERATED_LOG_PREFIX,
  describeFault,
  installMainProcessFaultHandlers,
  isSocketTeardownRace,
} from '../faults';

/** The exact shape Node throws from its TCP read callback on a destroyed socket. */
function socketTeardownRace(frames?: readonly string[]): RangeError {
  const error = new RangeError(
    'The value of "err" is out of range. It must be a negative integer. Received 617',
  );
  Object.assign(error, { code: 'ERR_OUT_OF_RANGE' });
  const stack = frames ?? [
    '    at Object.getSystemErrorName (node:util:455:11)',
    '    at new ErrnoException (node:internal/errors:733:23)',
    '    at TCP.onStreamRead (node:internal/stream_base_commons:216:20)',
  ];
  error.stack = [`RangeError [ERR_OUT_OF_RANGE]: ${error.message}`, ...stack].join('\n');
  return error;
}

describe('main-process fault handlers', () => {
  it('tolerates the Node socket teardown race without recording a crash', () => {
    const emitter = new EventEmitter();
    const log = vi.fn();
    const handlers = installMainProcessFaultHandlers({ process: emitter, log });
    const recordCrash = vi.fn();
    handlers.setRecorder({ recordCrash });

    const error = socketTeardownRace();
    emitter.emit('uncaughtException', error);

    expect(log).toHaveBeenCalledTimes(1);
    const [line] = log.mock.calls[0] as [string];
    expect(line.startsWith(`${TOLERATED_LOG_PREFIX} socket teardown race: `)).toBe(true);
    expect(line.startsWith(FAULT_LOG_PREFIX)).toBe(false);
    expect(recordCrash).not.toHaveBeenCalled();
  });

  it('still records a range error that an application data handler threw', () => {
    const emitter = new EventEmitter();
    const log = vi.fn();
    const handlers = installMainProcessFaultHandlers({ process: emitter, log });
    const recordCrash = vi.fn();
    handlers.setRecorder({ recordCrash });

    emitter.emit(
      'uncaughtException',
      socketTeardownRace([
        '    at Object.getSystemErrorName (node:util:455:11)',
        '    at parseFrame (/app/out/main/index.js:10:5)',
        '    at TCP.onStreamRead (node:internal/stream_base_commons:216:20)',
      ]),
    );

    expect(recordCrash).toHaveBeenCalledTimes(1);
    expect(String(log.mock.calls[0]?.[0])).toContain(`${FAULT_LOG_PREFIX} uncaught exception`);
  });

  it('isSocketTeardownRace rejects every nearby shape', () => {
    expect(isSocketTeardownRace(socketTeardownRace())).toBe(true);
    expect(isSocketTeardownRace(new RangeError('The value of "err" is out of range.'))).toBe(false);
    const otherCode = socketTeardownRace();
    Object.assign(otherCode, { code: 'ERR_BUFFER_OUT_OF_BOUNDS' });
    expect(isSocketTeardownRace(otherCode)).toBe(false);
    const otherMessage = socketTeardownRace();
    otherMessage.message = 'The value of "offset" is out of range.';
    expect(isSocketTeardownRace(otherMessage)).toBe(false);
    expect(isSocketTeardownRace(socketTeardownRace([]))).toBe(false);
    expect(
      isSocketTeardownRace(
        socketTeardownRace(['    at TCP.onStreamRead (node:internal/stream_base_commons:216:20)']),
      ),
    ).toBe(false);
    expect(isSocketTeardownRace(new TypeError('not a range error'))).toBe(false);
    expect(isSocketTeardownRace('string')).toBe(false);
  });

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
