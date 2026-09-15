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
 * Shared server-identity request fencing for main-process services. One
 * capture-call-compare policy lives here so clone, create, initialize,
 * source update, and setup cannot drift into subtly different notions of
 * "the same server", which is what makes a discard trustworthy: the
 * renderer sees the same guarantee no matter which service it called.
 * Services keep their own DTO mapping; only the fence is shared.
 */
import { buildCanonicalError, CanonicalErrorException } from '../shared/errors';
import type { ApiRequestInit } from './gateway/runtimeGateway';
import { serverRequest, type ServerTransport } from './serverClient';

/** Identity of the connected server, captured for request fencing. */
export interface ServerIdentity {
  serverKey: string | null;
  generation: number;
}

export type ServerIdentitySource = () => ServerIdentity;

/**
 * The identity of a service wired without an identity source: such a
 * service is deliberately unfenced (tests and callers that own no
 * connection), and because both captures return this same constant every
 * comparison passes.
 */
const UNFENCED_IDENTITY: ServerIdentity = { serverKey: null, generation: 0 };

/** Reads the current identity, or the unfenced constant when none is wired. */
export function captureServerIdentity(source: ServerIdentitySource | undefined): ServerIdentity {
  if (source === undefined) return UNFENCED_IDENTITY;
  return source();
}

/**
 * Rejects a sequence whose server changed since `before` was captured.
 *
 * Absent identity (`serverKey === null`, i.e. no connection is currently
 * reporting one) counts as a distinct identity rather than a wildcard: a
 * response that started or ended outside a proven connection cannot be
 * attributed to the server now attached, and an unattributable result must
 * never authorize state on it. Treating null as "matches anything" would
 * silently widen the fence exactly during connect/disconnect races — the
 * window the fence exists for — so the stricter reading wins and the
 * renderer reconciles from a fresh authoritative snapshot instead.
 */
export function assertSameServer(
  before: ServerIdentity,
  source: ServerIdentitySource | undefined,
): void {
  const after = captureServerIdentity(source);
  if (before.serverKey !== after.serverKey || before.generation !== after.generation) {
    throw new CanonicalErrorException(buildCanonicalError('E_SERVER_SWITCHED'));
  }
}

/**
 * Runs one transport call fenced by server identity and connection
 * generation: the identity is captured before the request and compared
 * after the response, so a switch A → B → A discards late replies from the
 * old connection rather than applying them to the new server.
 */
export async function fencedServerRequest(
  transport: ServerTransport,
  source: ServerIdentitySource | undefined,
  path: string,
  init?: ApiRequestInit,
): Promise<unknown> {
  const before = captureServerIdentity(source);
  const body = await serverRequest(transport, path, init);
  assertSameServer(before, source);
  return body;
}
