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
 * Generic, owner-only, recoverable storage for keyed unsaved text drafts.
 *
 * The review-editor draft store delegates to this implementation,
 * supplying its key shape, file name, draft cap, error-code prefix, and
 * Zod schemas.
 *
 * A corrupt or incompatible file is moved aside; recovery always starts
 * empty rather than implying that draft text survived.
 */
import fs from 'node:fs';
import path from 'node:path';
import { redactText, SafeErrorException, safeError } from '../shared/errors';
import {
  assertNoPrototypePollution,
  assertWithinByteSize,
  MAX_PAYLOAD_BYTES,
} from '../shared/sanitize';

interface SafeParseSuccess<T> {
  success: true;
  data: T;
}

interface SafeParseFailure {
  success: false;
}

type SafeParseResult<T> = SafeParseSuccess<T> | SafeParseFailure;

interface DraftSchema<T> {
  safeParse(value: unknown): SafeParseResult<T>;
  parse(value: unknown): T;
}

export interface KeyedDraftStoreConfig<
  Draft extends { savedAt: string },
  Key,
  Lookup,
  SaveRequest extends { text: string },
> {
  fileName: string;
  maxDrafts: number;
  errorCodePrefix: string;
  maxBytes: number;
  saveRequestSchema: DraftSchema<SaveRequest>;
  storeSchema: DraftSchema<{ schemaVersion: number; drafts: Draft[] }>;
  keysMatch: (draft: Draft, key: Key) => boolean;
  partialMatch: (draft: Draft, lookup: Lookup) => boolean;
  hasRevision: (lookup: Lookup) => boolean;
  recoveryLabel: string;
}

export interface KeyedDraftStoreOptions {
  warn?: (message: string) => void;
  now?: () => Date;
}

export class KeyedDraftStore<
  Draft extends { savedAt: string },
  Key,
  Lookup,
  SaveRequest extends { text: string },
> {
  private readonly file: string;
  private readonly warn: (message: string) => void;
  private readonly now: () => Date;
  private drafts: Draft[];

  constructor(
    private readonly dir: string,
    private readonly config: KeyedDraftStoreConfig<Draft, Key, Lookup, SaveRequest>,
    options: KeyedDraftStoreOptions = {},
  ) {
    this.file = path.join(dir, config.fileName);
    this.warn = options.warn ?? ((message) => console.warn(message));
    this.now = options.now ?? (() => new Date());
    this.drafts = this.loadStore();
  }

  load(key: Lookup): Draft | null {
    const matches = this.config.hasRevision(key)
      ? (draft: Draft) => this.config.keysMatch(draft, key as unknown as Key)
      : (draft: Draft) => this.config.partialMatch(draft, key);
    const found = this.drafts
      .filter(matches)
      .sort((left, right) => right.savedAt.localeCompare(left.savedAt))[0];
    return found === undefined ? null : { ...found };
  }

  save(request: SaveRequest): Draft {
    const parsed = this.config.saveRequestSchema.safeParse(request);
    if (!parsed.success) {
      throw new SafeErrorException(
        safeError(
          `E_INVALID_${this.config.errorCodePrefix}`,
          'The local draft did not match the supported schema.',
        ),
      );
    }
    assertWithinByteSize(parsed.data.text, this.config.maxBytes);
    const draft = { ...parsed.data, savedAt: this.now().toISOString() } as unknown as Draft;
    const index = this.drafts.findIndex((candidate) =>
      this.config.keysMatch(candidate, draft as unknown as Key),
    );
    if (index === -1) {
      if (this.drafts.length >= this.config.maxDrafts) {
        throw new SafeErrorException(
          safeError(
            `E_${this.config.errorCodePrefix}_LIMIT`,
            'Too many recoverable drafts are stored locally. Discard an older draft before saving another.',
          ),
        );
      }
      this.drafts = [...this.drafts, draft];
    } else {
      this.drafts = this.drafts.map((candidate, candidateIndex) =>
        candidateIndex === index ? draft : candidate,
      );
    }
    this.persist();
    return { ...draft };
  }

  discard(key: Key): boolean {
    const next = this.drafts.filter((draft) => !this.config.keysMatch(draft, key));
    if (next.length === this.drafts.length) {
      return false;
    }
    this.drafts = next;
    this.persist();
    return true;
  }

  /**
   * Renames every draft matching `match` through `transform` and persists.
   * Idempotent when the match predicate no longer holds for the transformed
   * drafts; returns how many entries were re-keyed.
   */
  rekey(match: (draft: Draft) => boolean, transform: (draft: Draft) => Draft): number {
    let changed = 0;
    this.drafts = this.drafts.map((draft) => {
      if (!match(draft)) {
        return draft;
      }
      changed += 1;
      return transform(draft);
    });
    if (changed > 0) {
      this.persist();
    }
    return changed;
  }

  private loadStore(): Draft[] {
    let raw: string;
    try {
      raw = fs.readFileSync(this.file, 'utf8');
    } catch (err) {
      if ((err as NodeJS.ErrnoException).code === 'ENOENT') {
        return [];
      }
      this.recover(`${this.config.recoveryLabel} was unreadable`);
      return [];
    }

    try {
      assertWithinByteSize(raw, MAX_PAYLOAD_BYTES);
      const data: unknown = JSON.parse(raw);
      assertNoPrototypePollution(data);
      const parsed = this.config.storeSchema.safeParse(data);
      if (!parsed.success) {
        this.recover(`${this.config.recoveryLabel} did not match the supported schema version`);
        return [];
      }
      return parsed.data.drafts;
    } catch {
      this.recover(`${this.config.recoveryLabel} was corrupt or truncated`);
      return [];
    }
  }

  private recover(reason: string): void {
    try {
      let counter = 1;
      let backup = `${this.file}.bak-${counter}`;
      while (fs.existsSync(backup)) {
        counter += 1;
        backup = `${this.file}.bak-${counter}`;
      }
      fs.renameSync(this.file, backup);
      this.warn(
        redactText(
          `Recovered ${this.config.recoveryLabel}: ${reason}; the previous file was saved as ` +
            `${this.config.fileName}.bak-${counter} and no local draft was restored.`,
        ),
      );
    } catch {
      this.warn(
        redactText(
          `Recovered ${this.config.recoveryLabel}: ${reason}; no local draft was restored.`,
        ),
      );
    }
  }

  /** Atomic replace: temp file in the same directory, 0600, fsync, rename. */
  private persist(): void {
    const store = this.config.storeSchema.parse({ schemaVersion: 1, drafts: this.drafts });
    fs.mkdirSync(this.dir, { recursive: true, mode: 0o700 });
    fs.chmodSync(this.dir, 0o700);
    const temp = `${this.file}.tmp-${process.pid}`;
    const payload = `${JSON.stringify(store, null, 2)}\n`;
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
