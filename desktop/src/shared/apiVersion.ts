/**
 * API schema-version compatibility gate. Every server response envelope
 * declares `api_version`; anything outside the supported major fails closed.
 */
import { SafeErrorException, safeError } from './errors';

/** The API major version this build of the desktop app speaks. */
export const SUPPORTED_API_VERSION = 'v1';

export function isCompatibleApiVersion(version: string): boolean {
  return version === SUPPORTED_API_VERSION || version.startsWith(`${SUPPORTED_API_VERSION}.`);
}

export function assertCompatibleApiVersion(version: string): void {
  if (!isCompatibleApiVersion(version)) {
    throw new SafeErrorException(
      safeError(
        'E_API_VERSION_INCOMPATIBLE',
        `The server speaks an unsupported API version; this app requires ${SUPPORTED_API_VERSION}.`,
        'Update the Agentico desktop app and the agentico server to matching releases.',
      ),
    );
  }
}
