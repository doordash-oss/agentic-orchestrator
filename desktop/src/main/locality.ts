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
 * Main-process locality guards. Every local-filesystem service boundary
 * (native pickers, clipboard capture, repository file walk, submit-time
 * path payloads) reads the active connection's kind from the runtime
 * gateway only — never from an IPC payload — through an injected
 * LocalitySource. A remote connection refuses fast with the distinct
 * E_REQUIRES_LOCAL_SERVER error before any filesystem or network work; a
 * null signal (transitional/not-ready states) is deliberately treated like
 * local so local behavior stays byte-for-byte unchanged.
 */
import { CanonicalErrorException, requiresLocalServerError } from '../shared/errors';

export type ConnectionLocality = 'local' | 'remote' | null;

/** Reads the gateway-owned locality of the active connection at call time. */
export type LocalitySource = () => ConnectionLocality;

/** Local-permissive default for deps a test fixture does not care about. */
export function alwaysLocal(): ConnectionLocality {
  return 'local';
}

/** Refuses local-filesystem work while the active connection is remote. */
export function assertLocalConnection(locality: LocalitySource): void {
  if (locality() === 'remote') {
    throw new CanonicalErrorException(requiresLocalServerError());
  }
}
