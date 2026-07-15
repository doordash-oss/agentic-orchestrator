/**
 * Shared narrow client for the authoritative server's authenticated REST
 * surface. Main-process services route every request through here so the
 * success check, structured-error parsing, redaction, and fail-closed
 * fallback stay identical across services instead of drifting per call site.
 * Services stay in charge of their own domain vocabulary: each supplies its
 * remedy-by-code table (and whether 409 `not_ready` target issues fold into
 * the remediation).
 */
import { redactText, SafeErrorException, safeError } from '../shared/errors';
import { ServerErrorResponseSchema, ServerErrorWithIssuesSchema } from '../shared/api/parse';
import type { ApiRequestInit, HttpResult } from './gateway/runtimeGateway';

/** The authenticated transport surface the runtime gateway provides. */
export interface ServerTransport {
  apiRequest(path: string, init?: ApiRequestInit): Promise<HttpResult>;
}

/** How a service maps structured server rejections to safe errors. */
export interface ServerErrorMapping {
  /** Concrete, safe next steps per structured server error code. */
  remedyByCode: Readonly<Record<string, string>>;
  /**
   * When true, error bodies are parsed with the `target.issues` extension
   * (409 `not_ready` rejections) and the redacted issue messages are folded
   * into the remediation so the caller can show why. When false, `target`
   * payloads are ignored entirely.
   */
  foldTargetIssues?: boolean;
}

/**
 * Performs one authenticated request and returns the raw success body for
 * the caller to schema-validate. Non-2xx responses throw the mapped
 * SafeErrorException; nothing from the wire crosses unredacted.
 */
export async function serverRequest(
  transport: ServerTransport,
  path: string,
  init: ApiRequestInit | undefined,
  mapping: ServerErrorMapping,
): Promise<unknown> {
  const result = await transport.apiRequest(path, init);
  if (result.status >= 200 && result.status < 300) {
    return result.body;
  }
  throw mapServerError(result, mapping);
}

/** Maps a structured server error body into a SafeError, failing closed. */
export function mapServerError(
  result: HttpResult,
  mapping: ServerErrorMapping,
): SafeErrorException {
  if (mapping.foldTargetIssues === true) {
    const parsed = ServerErrorWithIssuesSchema.safeParse(result.body);
    if (parsed.success) {
      const { code, message, target } = parsed.data.error;
      const issues = target?.issues ?? [];
      const issueText = issues.map((issue) => redactText(issue.message)).join(' ');
      const remedy =
        issueText !== ''
          ? `${issueText} ${mapping.remedyByCode[code] ?? ''}`.trim()
          : mapping.remedyByCode[code];
      return new SafeErrorException(safeError(code, redactText(message), remedy));
    }
  } else {
    const parsed = ServerErrorResponseSchema.safeParse(result.body);
    if (parsed.success) {
      const { code, message } = parsed.data.error;
      return new SafeErrorException(
        safeError(code, redactText(message), mapping.remedyByCode[code]),
      );
    }
  }
  return new SafeErrorException(
    safeError(
      `E_HTTP_${result.status}`,
      'The runtime rejected the request.',
      'Retry; if this persists, restart the runtime and check its log.',
    ),
  );
}
