/**
 * OS-encrypted store for remote-server bearer tokens, keyed by the 32-hex-char
 * serverKey (`serverKeyForBaseUrl`). Tokens are encrypted with Electron's
 * safeStorage before they touch disk; the on-disk JSON file only ever holds
 * base64 ciphertext blobs. Every token that passes through the store is
 * registered with the log-redaction secret registry immediately upon receipt,
 * before any other use.
 *
 * Re-paste semantics: when a blob cannot be decrypted (safeStorage
 * unavailable, decryption throws, or the file is corrupt) `load()` reports
 * `re-paste-required` and NEVER deletes the entry — a later successful save
 * overwrites the blob.
 */
import fs from 'node:fs';
import path from 'node:path';
import { z } from 'zod';

export interface SafeStorageLike {
  isEncryptionAvailable(): boolean;
  encryptString(plain: string): Buffer;
  decryptString(encrypted: Buffer): string;
}

export interface RemoteTokenStoreDeps {
  safeStorage: SafeStorageLike;
  registerSecret: (secret: string) => void;
}

const FILE_VERSION = 1;
const MAX_FILE_BYTES = 1024 * 1024;

const ServerKeySchema = z.string().regex(/^[0-9a-f]{32}$/);

const FileSchema = z.object({
  version: z.literal(FILE_VERSION),
  tokens: z.record(ServerKeySchema, z.string()),
});

type FileDoc = z.infer<typeof FileSchema>;

export type SaveResult = { status: 'saved' } | { status: 'unavailable' };

export type LoadResult =
  { status: 'ok'; token: string } | { status: 'absent' } | { status: 're-paste-required' };

export class RemoteTokenStore {
  constructor(
    private readonly file: string,
    private readonly deps: RemoteTokenStoreDeps,
  ) {}

  save(serverKey: string, token: string): SaveResult {
    // Register for log redaction immediately upon receipt, before any other
    // use of the token.
    this.deps.registerSecret(token);
    if (!this.deps.safeStorage.isEncryptionAvailable()) {
      return { status: 'unavailable' };
    }
    let encrypted: Buffer;
    try {
      encrypted = this.deps.safeStorage.encryptString(token);
    } catch {
      return { status: 'unavailable' };
    }
    const read = this.readFile();
    const doc: FileDoc =
      read === 'corrupt' ? { version: FILE_VERSION, tokens: {} as Record<string, string> } : read;
    doc.tokens[serverKey] = encrypted.toString('base64');
    this.persist(doc);
    return { status: 'saved' };
  }

  load(serverKey: string): LoadResult {
    const doc = this.readFile();
    if (doc === 'corrupt') {
      return { status: 're-paste-required' };
    }
    const blob = doc.tokens[serverKey];
    if (blob === undefined) {
      return { status: 'absent' };
    }
    if (!this.deps.safeStorage.isEncryptionAvailable()) {
      return { status: 're-paste-required' };
    }
    let decoded: Buffer;
    try {
      decoded = Buffer.from(blob, 'base64');
    } catch {
      return { status: 're-paste-required' };
    }
    let token: string;
    try {
      token = this.deps.safeStorage.decryptString(decoded);
    } catch {
      return { status: 're-paste-required' };
    }
    if (token === '') {
      return { status: 're-paste-required' };
    }
    // Register for log redaction before the token leaves the store.
    this.deps.registerSecret(token);
    return { status: 'ok', token };
  }

  remove(serverKey: string): void {
    const doc = this.readFile();
    if (doc === 'corrupt') {
      return;
    }
    if (!(serverKey in doc.tokens)) {
      return;
    }
    delete doc.tokens[serverKey];
    this.persist(doc);
  }

  // --- persistence ----------------------------------------------------------

  /**
   * Returns the parsed doc, an empty doc when the file is missing, or the
   * 'corrupt' marker when the file exists but cannot be read or parsed.
   */
  private readFile(): FileDoc | 'corrupt' {
    let raw: string;
    try {
      raw = fs.readFileSync(this.file, 'utf8');
    } catch (err) {
      if ((err as NodeJS.ErrnoException).code === 'ENOENT') {
        return { version: FILE_VERSION, tokens: {} };
      }
      return 'corrupt';
    }
    try {
      if (Buffer.byteLength(raw, 'utf8') > MAX_FILE_BYTES) {
        return 'corrupt';
      }
      const parsed = FileSchema.safeParse(JSON.parse(raw));
      return parsed.success ? parsed.data : 'corrupt';
    } catch {
      return 'corrupt';
    }
  }

  /** Atomic replace: temp file in the same directory, 0600, fsync, rename. */
  private persist(doc: FileDoc): void {
    const dir = path.dirname(this.file);
    fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
    fs.chmodSync(dir, 0o700);
    const temp = `${this.file}.tmp-${process.pid}`;
    const payload = `${JSON.stringify(doc, null, 2)}\n`;
    const fd = fs.openSync(temp, 'w', 0o600);
    try {
      fs.writeFileSync(fd, payload, 'utf8');
      fs.fchmodSync(fd, 0o600);
      fs.fsyncSync(fd);
    } finally {
      fs.closeSync(fd);
    }
    fs.renameSync(temp, this.file);
  }
}
