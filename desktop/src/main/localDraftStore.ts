/**
 * Owner-only, recoverable storage for unsaved review-editor text.
 *
 * This is deliberately separate from presentation settings and contains no
 * server review-session state. A corrupt or incompatible file is moved aside;
 * recovery always starts empty rather than implying that draft text survived.
 */
import fs from 'node:fs';
import path from 'node:path';
import { redactText, SafeErrorException, safeError } from '../shared/errors';
import {
  assertNoPrototypePollution,
  assertWithinByteSize,
  MAX_PAYLOAD_BYTES,
} from '../shared/sanitize';
import {
  LocalReviewDraftSaveRequestSchema,
  LocalReviewDraftStoreSchema,
  MAX_LOCAL_REVIEW_DRAFT_BYTES,
  type LocalReviewDraft,
  type LocalReviewDraftSaveRequest,
  type LocalReviewDraftLookupRequest,
  type ReviewDraftKey,
} from '../shared/ipc';

const FILE_NAME = 'review-local-drafts.json';

export interface LocalDraftStoreOptions {
  warn?: (message: string) => void;
  now?: () => Date;
}

export class LocalDraftStore {
  private readonly file: string;
  private readonly warn: (message: string) => void;
  private readonly now: () => Date;
  private drafts: LocalReviewDraft[];

  constructor(
    private readonly dir: string,
    options: LocalDraftStoreOptions = {},
  ) {
    this.file = path.join(dir, FILE_NAME);
    this.warn = options.warn ?? ((message) => console.warn(message));
    this.now = options.now ?? (() => new Date());
    this.drafts = this.loadStore();
  }

  load(key: LocalReviewDraftLookupRequest): LocalReviewDraft | null {
    const matches =
      key.baseDraftRevision === undefined
        ? (draft: LocalReviewDraft) => resourceMatches(draft, key)
        : (draft: LocalReviewDraft) => keysMatch(draft, key as ReviewDraftKey);
    const found = this.drafts
      .filter(matches)
      .sort((left, right) => right.savedAt.localeCompare(left.savedAt))[0];
    return found === undefined ? null : { ...found };
  }

  save(request: LocalReviewDraftSaveRequest): LocalReviewDraft {
    const parsed = LocalReviewDraftSaveRequestSchema.safeParse(request);
    if (!parsed.success) {
      throw new SafeErrorException(
        safeError('E_INVALID_LOCAL_DRAFT', 'The local draft did not match the supported schema.'),
      );
    }
    assertWithinByteSize(parsed.data.text, MAX_LOCAL_REVIEW_DRAFT_BYTES);
    const draft: LocalReviewDraft = { ...parsed.data, savedAt: this.now().toISOString() };
    const index = this.drafts.findIndex((candidate) => keysMatch(candidate, draft));
    if (index === -1) {
      if (this.drafts.length >= 20) {
        throw new SafeErrorException(
          safeError(
            'E_LOCAL_DRAFT_LIMIT',
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

  discard(key: ReviewDraftKey): boolean {
    const next = this.drafts.filter((draft) => !keysMatch(draft, key));
    if (next.length === this.drafts.length) {
      return false;
    }
    this.drafts = next;
    this.persist();
    return true;
  }

  private loadStore(): LocalReviewDraft[] {
    let raw: string;
    try {
      raw = fs.readFileSync(this.file, 'utf8');
    } catch (err) {
      if ((err as NodeJS.ErrnoException).code === 'ENOENT') {
        return [];
      }
      this.recover('local draft store was unreadable');
      return [];
    }

    try {
      assertWithinByteSize(raw, MAX_PAYLOAD_BYTES);
      const data: unknown = JSON.parse(raw);
      assertNoPrototypePollution(data);
      const parsed = LocalReviewDraftStoreSchema.safeParse(data);
      if (!parsed.success) {
        this.recover('local draft store did not match the supported schema version');
        return [];
      }
      return parsed.data.drafts;
    } catch {
      this.recover('local draft store was corrupt or truncated');
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
          `Recovered local review drafts: ${reason}; the previous file was saved as ` +
            `${FILE_NAME}.bak-${counter} and no local draft was restored.`,
        ),
      );
    } catch {
      this.warn(
        redactText(`Recovered local review drafts: ${reason}; no local draft was restored.`),
      );
    }
  }

  /** Atomic replace: temp file in the same directory, 0600, fsync, rename. */
  private persist(): void {
    const store = LocalReviewDraftStoreSchema.parse({ schemaVersion: 1, drafts: this.drafts });
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

function keysMatch(draft: ReviewDraftKey, key: ReviewDraftKey): boolean {
  return (
    draft.runtimeId === key.runtimeId &&
    draft.featureId === key.featureId &&
    draft.reviewId === key.reviewId &&
    draft.baseDraftRevision === key.baseDraftRevision
  );
}

function resourceMatches(draft: ReviewDraftKey, key: LocalReviewDraftLookupRequest): boolean {
  return (
    draft.runtimeId === key.runtimeId &&
    draft.featureId === key.featureId &&
    draft.reviewId === key.reviewId
  );
}
