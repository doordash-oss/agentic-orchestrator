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
 * Registers every IPC handler from the central registry. Each handler:
 *   1. validates the sender (main-window webContents + app origin),
 *   2. size-checks and pollution-scans the payload,
 *   3. validates arguments against the channel's request schema,
 *   4. invokes the service,
 *   5. validates the response against the channel's response schema,
 * and always resolves to a typed { ok } envelope — exceptions never cross
 * the boundary unredacted.
 */
import { buildCanonicalError, toCanonicalError, type CanonicalError } from '../shared/errors';
import { validateWithSchema } from '../shared/api/parse';
import { assertNoPrototypePollution, assertWithinByteSize } from '../shared/sanitize';
import type { z } from 'zod';
import { IPC_CHANNELS, IPC_EVENTS, type IpcMethod } from '../shared/ipcChannels';
import {
  ipcContracts,
  type IpcEnvelope,
  type SessionOutputEvent,
  type SessionOutputOpenRequest,
} from '../shared/ipc';
import { isTrustedSender, type SenderLikeEvent, type TrustedSender } from './security';

/** Service signatures derive from the validated request and response schemas. */
export type IpcServices = {
  [Method in Exclude<IpcMethod, 'openSessionOutput'>]: (
    ...args: z.output<(typeof ipcContracts)[Method]['request']>
  ) =>
    | z.input<(typeof ipcContracts)[Method]['response']>
    | Promise<z.input<(typeof ipcContracts)[Method]['response']>>;
} & {
  openSessionOutput(
    request: SessionOutputOpenRequest,
    emit: (event: SessionOutputEvent) => void,
  ): string;
};

interface IpcMainLike {
  handle(
    channel: string,
    listener: (event: SenderLikeEvent, ...args: unknown[]) => Promise<unknown>,
  ): void;
}

const UNTRUSTED: IpcEnvelope = {
  ok: false,
  error: buildCanonicalError('E_UNTRUSTED_SENDER'),
};

/**
 * The envelope's single error shape delegates canonical pass-through,
 * timeout classification, and fallback diagnostics to the shared boundary.
 */
function envelopeError(err: unknown): CanonicalError {
  return toCanonicalError(err, 'E_INTERNAL');
}

function makeHandler(
  method: IpcMethod,
  trusted: TrustedSender,
  invoke: (event: SenderLikeEvent, ...args: never[]) => unknown,
): (event: SenderLikeEvent, ...args: unknown[]) => Promise<IpcEnvelope> {
  const contract = ipcContracts[method];
  return async (event, ...args) => {
    if (!isTrustedSender(event, trusted)) {
      return UNTRUSTED;
    }
    try {
      assertWithinByteSize(JSON.stringify(args) ?? '');
      assertNoPrototypePollution(args);
      const parsedArgs = validateWithSchema(args, contract.request);
      const value = await (invoke as (e: SenderLikeEvent, ...a: unknown[]) => unknown)(
        event,
        ...parsedArgs,
      );
      return { ok: true, value: validateWithSchema(value, contract.response) };
    } catch (err) {
      return { ok: false, error: envelopeError(err) };
    }
  };
}

export function registerIpcHandlers(
  ipcMain: IpcMainLike,
  trusted: TrustedSender,
  services: IpcServices,
): void {
  for (const method of Object.keys(IPC_CHANNELS) as IpcMethod[]) {
    const invoke = (event: SenderLikeEvent, ...args: unknown[]): unknown => {
      if (method === 'openSessionOutput') {
        const request = args[0] as SessionOutputOpenRequest;
        return {
          subscriptionId: services.openSessionOutput(request, (payload) => {
            const sender = event.sender as typeof event.sender & { isDestroyed?(): boolean };
            if (sender.isDestroyed?.() !== true) {
              sender.send?.(IPC_EVENTS.sessionOutput, payload);
            }
          }),
        };
      }
      // The contract validates args before this call; the mapped type validates each service.
      return (services[method] as (...parsedArgs: unknown[]) => unknown)(...args);
    };
    ipcMain.handle(IPC_CHANNELS[method], makeHandler(method, trusted, invoke));
  }
}
