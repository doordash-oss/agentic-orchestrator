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
 * Sandboxed preload. Exposes exactly one narrow, task-specific API
 * (`window.agentico`) via the context bridge. There is no generic invoke,
 * no channel parameter accepted from the renderer, and no token, URL, node,
 * or process material in scope.
 */
import { contextBridge, ipcRenderer, webUtils } from 'electron';
import {
  buildCanonicalError,
  CANONICAL_ERROR_MESSAGE_PREFIX,
  type CanonicalError,
} from '../shared/errors';
import { type z } from 'zod';
import { IPC_CHANNELS, IPC_EVENTS, type IpcMethod } from '../shared/ipcChannels';
import {
  AppEventSchema,
  AppRouteEventSchema,
  SessionOutputEventSchema,
  SupervisorEventSchema,
  WindowFocusEventSchema,
  ServerListSnapshotSchema,
  ConnectionStateSchema,
  CREATION_ATTACHMENT_LIMIT,
  CREATION_IMAGE_FORMATS,
  CREATION_IMAGE_LIMIT,
  IpcEnvelopeSchema,
  windowPurposeFromArgv,
  type AgenticoApi,
  type AppEvent,
  type AppRouteEvent,
  type ConnectionState,
  type ServerListSnapshot,
  type CreationFileKind,
  type SessionOutputEvent,
  type SupervisorEvent,
  type WindowFocusEvent,
} from '../shared/ipc';
import { assertNoPrototypePollution } from '../shared/sanitize';

/** Invokes a fixed channel and unwraps the validated envelope, failing closed. */
async function call<T>(channel: string, ...args: unknown[]): Promise<T> {
  const raw: unknown = await ipcRenderer.invoke(channel, ...args);
  const parsed = IpcEnvelopeSchema.safeParse(raw);
  if (!parsed.success) {
    // The malformed-envelope failure is also a catalog-authored canonical:
    // it rides the same sentinel-prefixed message channel so the renderer's
    // parser recovers it like any other envelope error.
    throwCanonicalError(buildCanonicalError('E_IPC_PROTOCOL'));
  }
  if (!parsed.data.ok) {
    // The catalog owns the text, so the canonical object crosses untouched.
    // Custom Error properties do not survive the context bridge, so the
    // object also rides in the message behind a sentinel prefix; the
    // renderer's parser recovers it there.
    throwCanonicalError(parsed.data.error);
  }
  return parsed.data.value as T;
}

function throwCanonicalError(canonical: CanonicalError): never {
  const error = new Error(CANONICAL_ERROR_MESSAGE_PREFIX + JSON.stringify(canonical));
  Object.assign(error, {
    canonical,
  });
  throw error;
}

type ChannelMethods = { [K in IpcMethod]: AgenticoApi[K] };

// The table is fixed at build time; each exposed method closes over its own channel.
const methods = Object.fromEntries(
  Object.entries(IPC_CHANNELS).map(([method, channel]) => [
    method,
    (...args: unknown[]) => call(channel, ...args),
  ]),
) as ChannelMethods;

function subscribe<T>(
  eventName: string,
  schema: z.ZodType<T>,
  listener: (value: T) => void,
): () => void {
  const wrapped = (_event: unknown, payload: unknown): void => {
    try {
      assertNoPrototypePollution(payload);
    } catch {
      return; // Drop unsafe push events.
    }
    const parsed = schema.safeParse(payload);
    if (parsed.success) listener(parsed.data);
  };
  ipcRenderer.on(eventName, wrapped);
  return () => ipcRenderer.removeListener(eventName, wrapped);
}

const api = {
  ...methods,
  platform: process.platform,
  windowPurpose: windowPurposeFromArgv(process.argv),
  importDroppedCreationFiles: (kind: CreationFileKind, files: readonly File[]) => {
    const limit = kind === 'image' ? CREATION_IMAGE_LIMIT : CREATION_ATTACHMENT_LIMIT;
    const imageMimeTypes = new Set(CREATION_IMAGE_FORMATS.map((format) => format.mime));
    const paths = files.slice(0, limit).flatMap((file) => {
      if (
        kind === 'image' &&
        !imageMimeTypes.has(file.type as (typeof CREATION_IMAGE_FORMATS)[number]['mime'])
      )
        return [];
      const filePath = webUtils.getPathForFile(file);
      return filePath === '' ? [] : [filePath];
    });
    return { paths };
  },
  onServersChanged: (listener: (snapshot: ServerListSnapshot) => void) =>
    subscribe(IPC_EVENTS.serversChanged, ServerListSnapshotSchema, listener),
  onConnectionChanged: (listener: (state: ConnectionState) => void) =>
    subscribe(IPC_EVENTS.connectionChanged, ConnectionStateSchema, listener),
  onRouteRequest: (listener: (event: AppRouteEvent) => void) =>
    subscribe(IPC_EVENTS.routeRequested, AppRouteEventSchema, listener),
  onSessionOutput: (listener: (event: SessionOutputEvent) => void) =>
    subscribe(IPC_EVENTS.sessionOutput, SessionOutputEventSchema, listener),
  onSupervisorEvent: (listener: (event: SupervisorEvent) => void) =>
    subscribe(IPC_EVENTS.supervisorEvent, SupervisorEventSchema, listener),
  onWindowFocusChanged: (listener: (event: WindowFocusEvent) => void) =>
    subscribe(IPC_EVENTS.windowFocusChanged, WindowFocusEventSchema, listener),
  onAppEvent: (listener: (event: AppEvent) => void) =>
    subscribe(IPC_EVENTS.appEvent, AppEventSchema, listener),
} satisfies AgenticoApi;

contextBridge.exposeInMainWorld('agentico', api);
