// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// The apiPathUploads route lives outside the JSON mutation pipeline
// (application/octet-stream bodies far above the 64 KiB mutation cap) but
// keeps the same bearer auth, trusted-client header, and loopback-Origin
// posture as JSON mutations.

// contentTypeOctetStream is the only Content-Type the upload route accepts.
const contentTypeOctetStream = "application/octet-stream"

const (
	uploadKindImage      = "image"
	uploadKindAttachment = "attachment"
)

const (
	// maxUploadImageBytes and maxUploadAttachmentBytes are the per-kind byte
	// caps enforced via http.MaxBytesReader.
	maxUploadImageBytes      = 10 << 20
	maxUploadAttachmentBytes = 25 << 20
	// maxUploadNameLength bounds the client-supplied original file name,
	// which is retained as metadata only.
	maxUploadNameLength = 255
)

// Combined per-request limits across local paths plus upload references.
// They mirror the desktop CREATION_IMAGE_LIMIT / CREATION_ATTACHMENT_LIMIT
// caps (desktop/src/shared/ipc.ts) and are enforced server-side on feature
// create, refactor launch, and chat start.
const (
	maxFeatureImagesTotal      = 12
	maxFeatureAttachmentsTotal = 24
)

// allowedUploadImageExtensions mirrors the desktop CREATION_IMAGE_FORMATS
// allowlist (desktop/src/shared/ipc.ts); a desktop change there must be
// mirrored here.
var allowedUploadImageExtensions = map[string]bool{
	"png": true,
	"jpg": true,
	"jpeg": true,
	"gif": true,
	"webp": true,
}

const (
	// uploadStagingDirName is the state-dir subdirectory holding staged
	// upload bytes, their JSON sidecars, and durable consumption copies.
	uploadStagingDirName = "uploads"
	// uploadChatDirName keeps chat image copies aligned with the chat session
	// directory name (chatName in cmd/agentico/main.go).
	uploadChatDirName = "chat"
	// uploadOrphanTTL reaps staged uploads (and durable consumption copies)
	// that were never consumed after this age.
	uploadOrphanTTL = 24 * time.Hour
	// uploadSweepInterval is the periodic orphan-sweep cadence; the sweep
	// also runs once at server startup.
	uploadSweepInterval = time.Hour
	// consumedUploadPrefix names durable copies of consumed staged files so
	// they stay distinguishable from live staged entries.
	consumedUploadPrefix = "consumed-"
)

// uploadRefPattern constrains references to the opaque hex format the server
// itself generates, so a client reference can never escape the staging dir.
var uploadRefPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

var safeUploadExtPattern = regexp.MustCompile(`^\.[A-Za-z0-9]{1,10}$`)

var errUploadTooLarge = errors.New("upload body is too large")

// uploadReferenceError describes a failed staged-reference resolution. The
// mutation fallthrough maps it to 400 bad_request; a request that fails
// resolution consumes nothing.
type uploadReferenceError struct{ msg string }

func (e *uploadReferenceError) Error() string { return e.msg }

// stagedUploadMeta is the JSON sidecar (ref.json) for one staged upload. The
// original client-supplied name is metadata only: on-disk names derive from
// the opaque reference, never from the name.
type stagedUploadMeta struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	StagedAt   int64  `json:"staged_at"`
	ConsumedAt int64  `json:"consumed_at,omitempty"`
}

// stagedUpload is a successfully staged body: an opaque single-use reference
// plus its sidecar metadata.
type stagedUpload struct {
	ref  string
	meta stagedUploadMeta
}

// preparedUpload is a validated, still-staged reference awaiting consumption.
type preparedUpload struct {
	ref  string
	meta stagedUploadMeta
}

type uploadStore struct {
	dir string
	// mu guards the consumption transaction (resolve + claim + durable
	// copy) so concurrent mutations cannot consume the same staged
	// reference. claims holds references reserved by a still-open
	// transaction; a claim is released on rollback or replaced by a
	// tombstone on commit.
	mu     sync.Mutex
	claims map[string]struct{}
}

func newUploadStore(stateDir string) *uploadStore {
	if strings.TrimSpace(stateDir) == "" {
		return nil
	}
	return &uploadStore{dir: filepath.Join(stateDir, uploadStagingDirName), claims: map[string]struct{}{}}
}

func (s *uploadStore) dataPath(ref string) string { return filepath.Join(s.dir, ref) }
func (s *uploadStore) metaPath(ref string) string { return filepath.Join(s.dir, ref+".json") }

// newUploadReference returns an opaque, unguessable reference; only the hex
// encoding of it ever becomes a file name.
func newUploadReference() (string, error) {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(entropy[:]), nil
}

func (s *uploadStore) writeMeta(ref string, meta stagedUploadMeta) error {
	payload, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return os.WriteFile(s.metaPath(ref), payload, 0o600)
}

func (s *uploadStore) readMeta(ref string) (stagedUploadMeta, error) {
	var meta stagedUploadMeta
	payload, err := os.ReadFile(s.metaPath(ref))
	if err != nil {
		return meta, err
	}
	if err := json.Unmarshal(payload, &meta); err != nil {
		return meta, err
	}
	return meta, nil
}

// stage streams the request body into a staging file under an opaque
// reference, capped per kind by http.MaxBytesReader. Oversized bodies fail
// with errUploadTooLarge; partial files never survive an error.
func (s *uploadStore) stage(kind, name string, limit int64, w http.ResponseWriter, r *http.Request) (stagedUpload, error) {
	ref, err := newUploadReference()
	if err != nil {
		return stagedUpload{}, err
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return stagedUpload{}, err
	}
	dataPath := s.dataPath(ref)
	f, err := os.OpenFile(dataPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return stagedUpload{}, err
	}
	body := http.MaxBytesReader(w, r.Body, limit)
	defer body.Close()
	size, err := io.Copy(f, body)
	closeErr := f.Close()
	if err != nil {
		_ = os.Remove(dataPath)
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return stagedUpload{}, errUploadTooLarge
		}
		return stagedUpload{}, err
	}
	if closeErr != nil {
		_ = os.Remove(dataPath)
		return stagedUpload{}, closeErr
	}
	meta := stagedUploadMeta{Kind: kind, Name: name, Size: size, StagedAt: time.Now().UTC().Unix()}
	if err := s.writeMeta(ref, meta); err != nil {
		_ = os.Remove(dataPath)
		return stagedUpload{}, err
	}
	return stagedUpload{ref: ref, meta: meta}, nil
}

// resolve validates one reference without consuming it: well-formed,
// currently staged (not consumed or reaped), and of the expected kind.
func (s *uploadStore) resolve(ref, wantKind string) (preparedUpload, error) {
	if !uploadRefPattern.MatchString(ref) {
		return preparedUpload{}, &uploadReferenceError{msg: fmt.Sprintf("upload reference %q has an invalid format", ref)}
	}
	meta, err := s.readMeta(ref)
	if err != nil || meta.ConsumedAt != 0 {
		return preparedUpload{}, &uploadReferenceError{msg: fmt.Sprintf("upload reference %q is unknown, expired, or already consumed", ref)}
	}
	if _, err := os.Stat(s.dataPath(ref)); err != nil {
		return preparedUpload{}, &uploadReferenceError{msg: fmt.Sprintf("upload reference %q is unknown, expired, or already consumed", ref)}
	}
	if meta.Kind != wantKind {
		return preparedUpload{}, &uploadReferenceError{msg: fmt.Sprintf("upload reference %q has kind %q, not %q", ref, meta.Kind, wantKind)}
	}
	return preparedUpload{ref: ref, meta: meta}, nil
}

// safeUploadExtension keeps only simple dot-extensions from the original
// client name for durable copy names; anything else yields none.
func safeUploadExtension(name string) string {
	ext := filepath.Ext(name)
	if safeUploadExtPattern.MatchString(ext) {
		return ext
	}
	return ""
}

// copyInto durably copies staged bytes to destDir under a name derived from
// the claim ID and the opaque reference (never the client name), so each
// transaction gets an isolated handoff path even if another request
// referenced the same upload.
func (s *uploadStore) copyInto(p preparedUpload, claimID, destDir string) (string, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(destDir, consumedUploadPrefix+claimID+"-"+p.ref+safeUploadExtension(p.meta.Name))
	src, err := os.Open(s.dataPath(p.ref))
	if err != nil {
		return "", err
	}
	defer src.Close()
	dst, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil {
		_ = os.Remove(dest)
		return "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(dest)
		return "", closeErr
	}
	return dest, nil
}

// sweep removes expired entries: never-consumed staged files (data plus
// sidecar) aged past the TTL, and durable consumption copies past the TTL.
// Consumed reference tombstones (sidecars marked consumed) are preserved so
// reused references keep failing with a client error rather than re-staging.
func (s *uploadStore) sweep(now time.Time) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".json") {
			ref := strings.TrimSuffix(name, ".json")
			if !uploadRefPattern.MatchString(ref) {
				continue
			}
			// Sidecars are reaped together with their data file (handled by
			// the data-file branch); an orphaned sidecar with no data file
			// ages out by its modification time unless it is a tombstone.
			if _, err := os.Stat(s.dataPath(ref)); err == nil {
				continue
			}
			if meta, err := s.readMeta(ref); err == nil && meta.ConsumedAt != 0 {
				continue
			}
			s.removeByModTime(entry, now)
			continue
		}
		if uploadRefPattern.MatchString(name) {
			meta, err := s.readMeta(name)
			stagedAt := time.Time{}
			if err == nil {
				stagedAt = time.Unix(meta.StagedAt, 0)
			} else {
				info, infoErr := entry.Info()
				if infoErr != nil {
					continue
				}
				stagedAt = info.ModTime()
			}
			if now.Sub(stagedAt) <= uploadOrphanTTL {
				continue
			}
			// Consumed staged bytes are deleted at commit time; the consumed
			// tombstone sidecar is never reaped so a reused reference keeps
			// failing with a client error.
			_ = os.Remove(s.dataPath(name))
			if err != nil || meta.ConsumedAt == 0 {
				_ = os.Remove(s.metaPath(name))
			}
			continue
		}
		// Durable consumption copies and foreign files age out by mtime.
		s.removeByModTime(entry, now)
	}
}

func (s *uploadStore) removeByModTime(entry os.DirEntry, now time.Time) {
	info, err := entry.Info()
	if err != nil {
		return
	}
	if now.Sub(info.ModTime()) > uploadOrphanTTL {
		_ = os.Remove(filepath.Join(s.dir, entry.Name()))
	}
}

// sweepLoop sweeps once at startup and then hourly until ctx is cancelled
// (wired to the server lifetime context, so shutdown stops it cleanly).
func (s *uploadStore) sweepLoop(ctx context.Context) {
	s.sweep(time.Now())
	ticker := time.NewTicker(uploadSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.sweep(now)
		}
	}
}

// consumedUploads is the result of one store-owned consumption transaction:
// every reference was validated and claimed, and its staged bytes were
// copied to a durable per-claim location before the transaction returned.
// rollback releases the claims and removes the copies (a failed request
// consumes nothing); commit deletes each staged source and tombstones its
// sidecar so a reused reference fails.
type consumedUploads struct {
	store           *uploadStore
	prepared        []preparedUpload
	copies          []string
	imagePaths      []string
	attachmentPaths []string
}

// consume runs one atomic consumption transaction under the store mutex:
// the request is deduplicated, every reference is validated and claimed (so
// no concurrent transaction can resolve it), and staged bytes are copied
// into destDir under per-claim handoff names (the staging dir itself is the
// feature launch handoff; the chat dir for chat images). Any failure
// releases every claim and removes prior copies, so a rejected request
// consumes nothing. The durable copies are what the existing async copy
// pipeline (feature setup image/attachment tasks) or the chat prompt
// embedding consume; deleting the staged source at commit time satisfies
// the copy-then-delete single-use rule because the copies are durable.
func (s *uploadStore) consume(imageRefs, attachmentRefs []string, destDir string) (*consumedUploads, error) {
	if len(imageRefs) == 0 && len(attachmentRefs) == 0 {
		return nil, nil
	}
	if s == nil {
		return nil, errors.New("upload service is unavailable")
	}
	if destDir == "" {
		destDir = s.dir
	}
	claimID, err := newUploadReference()
	if err != nil {
		return nil, fmt.Errorf("begin upload consumption claim: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]struct{}{}
	consumed := &consumedUploads{store: s}
	resolve := func(ref, wantKind string) error {
		if _, dup := seen[ref]; dup {
			return &uploadReferenceError{msg: fmt.Sprintf("upload reference %q is listed more than once", ref)}
		}
		seen[ref] = struct{}{}
		if _, claimed := s.claims[ref]; claimed {
			return &uploadReferenceError{msg: fmt.Sprintf("upload reference %q is unknown, expired, or already consumed", ref)}
		}
		p, err := s.resolve(ref, wantKind)
		if err != nil {
			return err
		}
		// Claim the reference now so another transaction interleaving with
		// the copy loop below still sees it as consumed.
		s.claims[ref] = struct{}{}
		consumed.prepared = append(consumed.prepared, p)
		return nil
	}
	for _, ref := range imageRefs {
		if err := resolve(ref, uploadKindImage); err != nil {
			consumed.releaseClaims()
			return nil, err
		}
	}
	for _, ref := range attachmentRefs {
		if err := resolve(ref, uploadKindAttachment); err != nil {
			consumed.releaseClaims()
			return nil, err
		}
	}
	for _, p := range consumed.prepared {
		copyPath, err := s.copyInto(p, claimID, destDir)
		if err != nil {
			for _, prior := range consumed.copies {
				_ = os.Remove(prior)
			}
			consumed.releaseClaims()
			return nil, fmt.Errorf("consume upload reference %q: %w", p.ref, err)
		}
		consumed.copies = append(consumed.copies, copyPath)
		if p.meta.Kind == uploadKindImage {
			consumed.imagePaths = append(consumed.imagePaths, copyPath)
		} else {
			consumed.attachmentPaths = append(consumed.attachmentPaths, copyPath)
		}
	}
	return consumed, nil
}

// consumeUploadRefs wraps the store-owned consumption transaction for the
// handler's upload store.
func (h *apiHandler) consumeUploadRefs(imageRefs, attachmentRefs []string, destDir string) (*consumedUploads, error) {
	return h.uploads.consume(imageRefs, attachmentRefs, destDir)
}

// releaseClaims drops every claim this transaction holds. Callers must hold
// the store mutex.
func (c *consumedUploads) releaseClaims() {
	for _, p := range c.prepared {
		delete(c.store.claims, p.ref)
	}
}

// rollback removes the durable copies produced at consume time and releases
// the claims; the staged sources stay intact so the caller can retry the
// request unchanged.
func (c *consumedUploads) rollback() {
	if c == nil {
		return
	}
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	for _, copyPath := range c.copies {
		_ = os.Remove(copyPath)
	}
	c.releaseClaims()
}

// commit deletes each consumed staged source and tombstones its sidecar so
// the reference is single-use: a reused reference fails resolution. The
// claims are released because the tombstones supersede them.
func (c *consumedUploads) commit() {
	if c == nil {
		return
	}
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	for _, p := range c.prepared {
		_ = os.Remove(c.store.dataPath(p.ref))
		meta := p.meta
		meta.ConsumedAt = time.Now().UTC().Unix()
		_ = c.store.writeMeta(p.ref, meta)
	}
	c.releaseClaims()
}

// handleUploadsRoute serves POST /api/v1/uploads.
func (h *apiHandler) handleUploadsRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if !h.requireTrustedUpload(w, r) {
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	var limit int64
	switch kind {
	case uploadKindImage:
		limit = maxUploadImageBytes
	case uploadKindAttachment:
		limit = maxUploadAttachmentBytes
	default:
		writeAPIError(w, http.StatusBadRequest, errCodeBadRequest, "kind must be image or attachment", nil)
		return
	}
	if name == "" {
		writeAPIError(w, http.StatusBadRequest, errCodeBadRequest, "name is required", nil)
		return
	}
	if len(name) > maxUploadNameLength {
		writeAPIError(w, http.StatusBadRequest, errCodeBadRequest, fmt.Sprintf("name exceeds the %d character limit", maxUploadNameLength), nil)
		return
	}
	if kind == uploadKindImage && !allowedUploadImageExtensions[strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))] {
		writeAPIError(w, http.StatusBadRequest, errCodeBadRequest, "image uploads require a png, jpg, jpeg, gif, or webp file name extension", nil)
		return
	}
	if r.ContentLength > limit {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "request_too_large", "upload body is too large", nil)
		return
	}
	staged, err := h.uploads.stage(kind, name, limit, w, r)
	if errors.Is(err, errUploadTooLarge) {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "request_too_large", "upload body is too large", nil)
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "stage upload", nil)
		return
	}
	writeJSON(w, http.StatusOK, StageUploadResponse{
		APIVersion: APIVersion,
		Reference:  staged.ref,
		Kind:       staged.meta.Kind,
		Name:       staged.meta.Name,
		Size:       staged.meta.Size,
	})
}

// requireTrustedUpload enforces the same bearer-adjacent posture as
// requireTrustedMutation — trusted local client header and loopback-only
// Origin — for the octet-stream upload route, requiring the octet-stream
// content type instead of a JSON body bound by MaxMutationBodyBytes.
func (h *apiHandler) requireTrustedUpload(w http.ResponseWriter, r *http.Request) bool {
	if h.uploads == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "unavailable", "upload service unavailable", nil)
		return false
	}
	if r.Header.Get("X-Agentico-Client") != trustedClientHeaderValue {
		writeAPIError(w, http.StatusForbidden, "forbidden", "trusted local client header is required", nil)
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" && !isLoopbackOrigin(origin) {
		writeAPIError(w, http.StatusForbidden, "forbidden", "browser origin is not trusted", nil)
		return false
	}
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	if !strings.HasPrefix(ct, contentTypeOctetStream) {
		writeAPIError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "octet-stream body is required", nil)
		return false
	}
	return true
}

// validateCombinedUploadCounts enforces the combined local-path +
// upload-reference caps (12 images, 24 attachments) so a caller cannot bypass
// the per-array limits by splitting across the two forms.
func validateCombinedUploadCounts(w http.ResponseWriter, images, imageUploads, attachments, attachmentUploads int) bool {
	if images+imageUploads > maxFeatureImagesTotal {
		writeAPIError(w, http.StatusBadRequest, errCodeBadRequest,
			fmt.Sprintf("too many images: images + image_uploads must be at most %d total", maxFeatureImagesTotal), nil)
		return false
	}
	if attachments+attachmentUploads > maxFeatureAttachmentsTotal {
		writeAPIError(w, http.StatusBadRequest, errCodeBadRequest,
			fmt.Sprintf("too many attachments: attachments + attachment_uploads must be at most %d total", maxFeatureAttachmentsTotal), nil)
		return false
	}
	return true
}
