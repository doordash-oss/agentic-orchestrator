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
import { requiresLocalServerError, SafeErrorException } from '../shared/errors';

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
    throw new SafeErrorException(requiresLocalServerError());
  }
}
