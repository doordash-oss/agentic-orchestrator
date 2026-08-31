/**
 * Bounded, redaction-first ring buffer for child-server stdio. Every line
 * is scrubbed on the way IN (bearer-like strings plus registered exact
 * secrets), so no unredacted copy is ever retained. Snapshot output feeds
 * local diagnostics only — it never crosses IPC as-is.
 */
import { redactText } from '../../shared/errors';

const REDACTED = '[redacted]';

export class RedactedLogBuffer {
  private readonly lines: string[] = [];
  private partial = '';
  private readonly secrets: string[] = [];

  constructor(
    private readonly maxLines = 200,
    secrets: readonly string[] = [],
  ) {
    for (const secret of secrets) {
      this.addSecret(secret);
    }
  }

  /** Registers an exact secret to scrub from all future lines. */
  addSecret(secret: string): void {
    if (secret.length > 0) {
      this.secrets.push(secret);
      // Discovery may reveal the credential after the child already printed
      // it. Scrub retained data immediately so a later snapshot cannot leak it.
      for (let index = 0; index < this.lines.length; index += 1) {
        this.lines[index] = this.redact(this.lines[index] ?? '');
      }
      this.partial = this.redact(this.partial);
    }
  }

  append(chunk: string): void {
    this.partial += chunk;
    let index = this.partial.indexOf('\n');
    while (index !== -1) {
      this.push(this.partial.slice(0, index));
      this.partial = this.partial.slice(index + 1);
      index = this.partial.indexOf('\n');
    }
  }

  /**
   * Scrubs one string against registered secrets (plus generic token/path
   * redaction) without retaining it. Used by server-boundary consumers that
   * ingest server-controlled text (e.g. health-payload display names) into
   * places the log buffer never sees: IPC state and settings.
   */
  scrub(line: string): string {
    return this.redact(line);
  }

  /** The redacted retained lines, oldest first. */
  snapshot(): string[] {
    const snapshot = [...this.lines];
    if (this.partial !== '') snapshot.push(this.redact(this.partial));
    return snapshot.slice(-this.maxLines);
  }

  private push(line: string): void {
    this.lines.push(this.redact(line));
    if (this.lines.length > this.maxLines) {
      this.lines.splice(0, this.lines.length - this.maxLines);
    }
  }

  private redact(line: string): string {
    let out = redactText(line);
    for (const secret of this.secrets) {
      out = out.split(secret).join(REDACTED);
    }
    return out;
  }
}
