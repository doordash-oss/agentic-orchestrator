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
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
)

// uploadMutationRecorder captures the mutation requests upload references
// flow into; unset methods fail loudly via the embedded nil target.
type uploadMutationRecorder struct {
	MutationTarget
	mu          sync.Mutex
	createReq   *CreateFeatureRequest
	createErr   error
	refactorReq *RefactorFeatureRequest
	refactorErr error
	chatReq           *ChatStartRequest
	chatHiddenContext string
	chatErr           error
}

func (r *uploadMutationRecorder) CreateFeature(req CreateFeatureRequest) (CreateFeatureResponse, error) {
	copied := req
	r.mu.Lock()
	r.createReq = &copied
	r.mu.Unlock()
	if r.createErr != nil {
		return CreateFeatureResponse{Result: "failed"}, r.createErr
	}
	return CreateFeatureResponse{APIVersion: APIVersion, FeatureID: "feat-uploaded", Result: "created"}, nil
}

func (r *uploadMutationRecorder) RefactorFeature(_ string, req RefactorFeatureRequest) (RefactorFeatureResponse, error) {
	copied := req
	r.mu.Lock()
	r.refactorReq = &copied
	r.mu.Unlock()
	if r.refactorErr != nil {
		return RefactorFeatureResponse{Result: "failed"}, r.refactorErr
	}
	return RefactorFeatureResponse{APIVersion: APIVersion, FeatureID: "refactor-child", ParentID: fixtureFeatureID, Result: "created"}, nil
}

func (r *uploadMutationRecorder) StartChat(req ChatStartRequest, hiddenContext string) (ChatStartResponse, error) {
	copied := req
	r.mu.Lock()
	r.chatReq = &copied
	r.chatHiddenContext = hiddenContext
	r.mu.Unlock()
	if r.chatErr != nil {
		return ChatStartResponse{Result: "failed"}, r.chatErr
	}
	return ChatStartResponse{APIVersion: APIVersion, SessionID: fixtureSessionID, Result: "started"}, nil
}

// newUploadTestAPI builds a handler backed by a temp state dir so the upload
// staging area exists, with auth enforcement enabled.
func newUploadTestAPI(t *testing.T, target MutationTarget, withAuth bool) (*apiHandler, http.Handler, string) {
	t.Helper()
	stateDir := t.TempDir()
	opts := HandlerOptions{
		Runtime:               RuntimeIdentity{RuntimeDir: filepath.Dir(stateDir), StateDir: stateDir, Config: testRuntimeConfigPath},
		Config:                config.NewDefault(),
		Mutations:             target,
		DisableHostValidation: true,
	}
	if withAuth {
		opts.AuthToken = testAuthToken
	}
	api := newAPIHandler(opts)
	return api, api.routes(), stateDir
}

func postUpload(handler http.Handler, target string, body []byte, mutate func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentTypeOctetStream)
	req.Header.Set("X-Agentico-Client", trustedClientHeaderValue)
	if mutate != nil {
		mutate(req)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func authedPostUpload(handler http.Handler, target string, body []byte) *httptest.ResponseRecorder {
	return postUpload(handler, target, body, func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+testAuthToken)
	})
}

func stageViaAPI(t *testing.T, handler http.Handler, kind, name string, body []byte) StageUploadResponse {
	t.Helper()
	w := postUpload(handler, apiPathUploads+"?kind="+kind+"&name="+name, body, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("stage %s %s status = %d body=%s; want 200", kind, name, w.Code, w.Body.String())
	}
	var resp StageUploadResponse
	if err := json.NewDecoder(w.Result().Body).Decode(&resp); err != nil {
		t.Fatalf("decode stage response: %v", err)
	}
	return resp
}

func TestUploadRequiresBearerToken(t *testing.T) {
	t.Parallel()
	_, handler, _ := newUploadTestAPI(t, &uploadMutationRecorder{}, true)
	w := postUpload(handler, apiPathUploads+"?kind=image&name=shot.png", []byte("png-bytes"), nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d; want 401", w.Code)
	}
}

func TestUploadRequiresTrustedClientHeader(t *testing.T) {
	t.Parallel()
	_, handler, _ := newUploadTestAPI(t, &uploadMutationRecorder{}, true)
	w := postUpload(handler, apiPathUploads+"?kind=image&name=shot.png", []byte("png-bytes"), func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+testAuthToken)
		req.Header.Del("X-Agentico-Client")
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d; want 403", w.Code)
	}
	if body := decodeErrorBody(t, w); body.Error.Code != "forbidden" {
		t.Fatalf("error code = %q; want forbidden", body.Error.Code)
	}
}

func TestUploadRejectsNonOctetStreamBody(t *testing.T) {
	t.Parallel()
	_, handler, _ := newUploadTestAPI(t, &uploadMutationRecorder{}, false)
	w := postUpload(handler, apiPathUploads+"?kind=image&name=shot.png", []byte("{}"), func(req *http.Request) {
		req.Header.Set("Content-Type", contentTypeJSON)
	})
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d; want 415", w.Code)
	}
	if body := decodeErrorBody(t, w); body.Error.Code != "unsupported_media_type" {
		t.Fatalf("error code = %q; want unsupported_media_type", body.Error.Code)
	}
}

func TestUploadEnforcesPerKindSizeCaps(t *testing.T) {
	t.Parallel()
	_, handler, _ := newUploadTestAPI(t, &uploadMutationRecorder{}, false)
	oversizedImage := make([]byte, maxUploadImageBytes+1)
	w := postUpload(handler, apiPathUploads+"?kind=image&name=big.png", oversizedImage, nil)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("image status = %d; want 413", w.Code)
	}
	if body := decodeErrorBody(t, w); body.Error.Code != "request_too_large" {
		t.Fatalf("image error code = %q; want request_too_large", body.Error.Code)
	}
	oversizedAttachment := make([]byte, maxUploadAttachmentBytes+1)
	w = postUpload(handler, apiPathUploads+"?kind=attachment&name=big.bin", oversizedAttachment, nil)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("attachment status = %d; want 413", w.Code)
	}
}

func TestUploadEnforcesByteCapWhileStreaming(t *testing.T) {
	t.Parallel()
	_, handler, stateDir := newUploadTestAPI(t, &uploadMutationRecorder{}, false)
	// Under-report Content-Length so the fast-path check passes and the
	// MaxBytesReader streaming cap has to catch it.
	body := make([]byte, maxUploadImageBytes+1)
	w := postUpload(handler, apiPathUploads+"?kind=image&name=big.png", body, func(req *http.Request) {
		req.ContentLength = 5
	})
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d; want 413", w.Code)
	}
	entries, err := os.ReadDir(filepath.Join(stateDir, uploadStagingDirName))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read staging dir: %v", err)
		}
		return
	}
	if len(entries) != 0 {
		t.Fatalf("staging dir entries = %v; want none after a rejected body", entries)
	}
}

func TestUploadImageKindRequiresImageExtension(t *testing.T) {
	t.Parallel()
	_, handler, _ := newUploadTestAPI(t, &uploadMutationRecorder{}, false)
	w := postUpload(handler, apiPathUploads+"?kind=image&name=payload.sh", []byte("bytes"), nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", w.Code)
	}
	if body := decodeErrorBody(t, w); body.Error.Code != string(errcat.BadRequest) {
		t.Fatalf("error code = %q; want bad_request", body.Error.Code)
	}
}

func TestUploadAttachmentKindAcceptsArbitraryBytesAndName(t *testing.T) {
	t.Parallel()
	_, handler, stateDir := newUploadTestAPI(t, &uploadMutationRecorder{}, false)
	payload := []byte{0x00, 0xFF, 0x13, 0x37}
	resp := stageViaAPI(t, handler, uploadKindAttachment, "payload.sh", payload)
	stagedPath := filepath.Join(stateDir, uploadStagingDirName, resp.Reference)
	got, err := os.ReadFile(stagedPath)
	if err != nil {
		t.Fatalf("read staged file: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("staged bytes mismatch: %v want %v", got, payload)
	}
}

func TestUploadStagesBytesUnderOpaqueSingleUseReference(t *testing.T) {
	t.Parallel()
	target := &uploadMutationRecorder{}
	api, handler, stateDir := newUploadTestAPI(t, target, false)
	payload := []byte("the-image-bytes")
	resp := stageViaAPI(t, handler, uploadKindImage, "Diagram.PNG", payload)
	if resp.APIVersion != APIVersion {
		t.Fatalf("api_version = %q; want %q", resp.APIVersion, APIVersion)
	}
	if !uploadRefPattern.MatchString(resp.Reference) {
		t.Fatalf("reference = %q; want 32-hex opaque id", resp.Reference)
	}
	if resp.Kind != uploadKindImage || resp.Name != "Diagram.PNG" || resp.Size != int64(len(payload)) {
		t.Fatalf("stage response = %+v; want kind/name/size echoed", resp)
	}
	stagedPath := filepath.Join(stateDir, uploadStagingDirName, resp.Reference)
	staged, err := os.ReadFile(stagedPath)
	if err != nil || !bytes.Equal(staged, payload) {
		t.Fatalf("staged file = %v, %v; want upload bytes", staged, err)
	}
	meta, err := api.uploads.readMeta(resp.Reference)
	if err != nil || meta.Kind != uploadKindImage || meta.Name != "Diagram.PNG" {
		t.Fatalf("sidecar = %+v, %v; want image kind and original name", meta, err)
	}

	// Consuming the reference once succeeds and deletes the staged file.
	w := postTrustedJSON(handler, apiPathFeatures, map[string]any{
		"name":          "with image",
		"image_uploads": []string{resp.Reference},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s; want 201", w.Code, w.Body.String())
	}
	if _, err := os.Stat(stagedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged file after consume err = %v; want deleted", err)
	}
	meta, err = api.uploads.readMeta(resp.Reference)
	if err != nil || meta.ConsumedAt == 0 {
		t.Fatalf("sidecar after consume = %+v, %v; want consumed tombstone", meta, err)
	}

	// A second consumption of the same reference fails without mutating.
	target.createReq = nil
	w = postTrustedJSON(handler, apiPathFeatures, map[string]any{
		"name":          "with image again",
		"image_uploads": []string{resp.Reference},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("second consume status = %d; want 400", w.Code)
	}
	if target.createReq != nil {
		t.Fatal("CreateFeature must not be called for a consumed reference")
	}
}

func TestUploadOrphanSweep(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	store := newUploadStore(stateDir)
	if store == nil {
		t.Fatal("newUploadStore returned nil")
	}
	if api, _, _ := newUploadTestAPI(t, &uploadMutationRecorder{}, false); api.uploads == nil {
		t.Fatal("handler wiring lost the upload store")
	}
	// Stage one entry and backdate it past the TTL; keep one fresh.
	stageAt := func(name string, stagedAt time.Time) string {
		payload := []byte(name)
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, apiPathUploads, bytes.NewReader(payload))
		staged, err := store.stage(uploadKindAttachment, name, maxUploadAttachmentBytes, w, req)
		if err != nil {
			t.Fatalf("stage %s: %v", name, err)
		}
		if !stagedAt.IsZero() {
			meta, err := store.readMeta(staged.ref)
			if err != nil {
				t.Fatalf("read meta %s: %v", name, err)
			}
			meta.StagedAt = stagedAt.Unix()
			if err := store.writeMeta(staged.ref, meta); err != nil {
				t.Fatalf("rewrite meta %s: %v", name, err)
			}
		}
		return staged.ref
	}
	old := 50 * time.Hour
	oldRef := stageAt("old.bin", time.Now().Add(-old))
	freshRef := stageAt("fresh.bin", time.Time{})
	consumedRef := stageAt("consumed.bin", time.Now().Add(-old))
	// Consume one reference so its sidecar becomes an old tombstone.
	consumed := &consumedUploads{store: store}
	p, err := store.resolve(consumedRef, uploadKindAttachment)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	consumed.prepared = append(consumed.prepared, p)
	consumed.commit()
	// An old durable consumption copy must age out by modtime.
	handoffPath := filepath.Join(stateDir, uploadStagingDirName, consumedUploadPrefix+"leftover")
	if err := os.WriteFile(handoffPath, []byte("copy"), 0o600); err != nil {
		t.Fatalf("write handoff: %v", err)
	}
	if err := os.Chtimes(handoffPath, time.Now().Add(-old), time.Now().Add(-old)); err != nil {
		t.Fatalf("backdate handoff: %v", err)
	}

	store.sweep(time.Now())

	if _, err := os.Stat(store.dataPath(oldRef)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old staged data err = %v; want reaped", err)
	}
	if _, err := os.Stat(store.metaPath(oldRef)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old staged sidecar err = %v; want reaped", err)
	}
	if _, err := os.Stat(store.dataPath(freshRef)); err != nil {
		t.Fatalf("fresh staged data = %v; want preserved", err)
	}
	if _, err := os.Stat(store.metaPath(freshRef)); err != nil {
		t.Fatalf("fresh staged sidecar = %v; want preserved", err)
	}
	meta, err := store.readMeta(consumedRef)
	if err != nil || meta.ConsumedAt == 0 {
		t.Fatalf("consumed tombstone = %+v, %v; want preserved", meta, err)
	}
	if _, err := os.Stat(handoffPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old handoff err = %v; want reaped", err)
	}
}

func TestCreateFeatureConsumesImageAndAttachmentRefs(t *testing.T) {
	t.Parallel()
	target := &uploadMutationRecorder{}
	_, handler, stateDir := newUploadTestAPI(t, target, false)
	image := stageViaAPI(t, handler, uploadKindImage, "Shot.png", []byte("image-bytes"))
	attachment := stageViaAPI(t, handler, uploadKindAttachment, "spec.pdf", []byte("pdf-bytes"))

	w := postTrustedJSON(handler, apiPathFeatures, map[string]any{
		"name":               "with uploads",
		"images":             []string{"/local/one.png"},
		"image_uploads":      []string{image.Reference},
		"attachment_uploads": []string{attachment.Reference},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s; want 201", w.Code, w.Body.String())
	}
	if target.createReq == nil {
		t.Fatal("CreateFeature was not called")
	}
	if len(target.createReq.Images) != 2 || target.createReq.Images[0] != "/local/one.png" {
		t.Fatalf("images = %v; want local path first then the resolved copy", target.createReq.Images)
	}
	imageCopy := target.createReq.Images[1]
	if filepath.Dir(imageCopy) != filepath.Join(stateDir, uploadStagingDirName) {
		t.Fatalf("image copy dir = %q; want staging dir", imageCopy)
	}
	got, err := os.ReadFile(imageCopy)
	if err != nil || !bytes.Equal(got, []byte("image-bytes")) {
		t.Fatalf("image copy = %v, %v; want staged bytes", got, err)
	}
	if len(target.createReq.Attachments) != 1 {
		t.Fatalf("attachments = %v; want exactly the resolved copy", target.createReq.Attachments)
	}
	got, err = os.ReadFile(target.createReq.Attachments[0])
	if err != nil || !bytes.Equal(got, []byte("pdf-bytes")) {
		t.Fatalf("attachment copy = %v, %v; want staged bytes", got, err)
	}
	// Consumed references are single-use.
	if _, err := os.Stat(filepath.Join(stateDir, uploadStagingDirName, image.Reference)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged image after consume err = %v; want deleted", err)
	}
}

func TestRefactorConsumesAttachmentRefs(t *testing.T) {
	t.Parallel()
	target := &uploadMutationRecorder{}
	_, handler, stateDir := newUploadTestAPI(t, target, false)
	attachment := stageViaAPI(t, handler, uploadKindAttachment, "notes.txt", []byte("notes"))

	w := postTrustedJSON(handler, apiPathFeatures+"/"+fixtureFeatureID+"/actions/refactor", map[string]any{
		"name":               "refactor child",
		"attachment_uploads": []string{attachment.Reference},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("refactor status = %d body=%s; want 201", w.Code, w.Body.String())
	}
	if target.refactorReq == nil {
		t.Fatal("RefactorFeature was not called")
	}
	if len(target.refactorReq.Attachments) != 1 {
		t.Fatalf("attachments = %v; want exactly the resolved copy", target.refactorReq.Attachments)
	}
	got, err := os.ReadFile(target.refactorReq.Attachments[0])
	if err != nil || !bytes.Equal(got, []byte("notes")) {
		t.Fatalf("attachment copy = %v, %v; want staged bytes", got, err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, uploadStagingDirName, attachment.Reference)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged attachment after consume err = %v; want deleted", err)
	}
}

func TestChatStartResolvesImageRefsIntoChatDir(t *testing.T) {
	t.Parallel()
	target := &uploadMutationRecorder{}
	_, handler, stateDir := newUploadTestAPI(t, target, false)
	image := stageViaAPI(t, handler, uploadKindImage, "shot.png", []byte("chat-image"))

	w := postTrustedJSON(handler, "/api/v1/prompts/chat/start", map[string]any{
		"message":       "what is this?",
		"image_uploads": []string{image.Reference},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("chat start status = %d body=%s; want 200", w.Code, w.Body.String())
	}
	if target.chatReq == nil {
		t.Fatal("StartChat was not called")
	}
	if len(target.chatReq.Images) != 1 {
		t.Fatalf("chat images = %v; want exactly the copied image", target.chatReq.Images)
	}
	chatCopy := target.chatReq.Images[0]
	if filepath.Dir(chatCopy) != filepath.Join(stateDir, uploadChatDirName) {
		t.Fatalf("chat image copy dir = %q; want the chat session dir", filepath.Dir(chatCopy))
	}
	got, err := os.ReadFile(chatCopy)
	if err != nil || !bytes.Equal(got, []byte("chat-image")) {
		t.Fatalf("chat image copy = %v, %v; want staged bytes", got, err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, uploadStagingDirName, image.Reference)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged image after consume err = %v; want deleted", err)
	}
}

func TestCreateFeatureEnforcesCombinedUploadCounts(t *testing.T) {
	t.Parallel()
	target := &uploadMutationRecorder{}
	api, handler, _ := newUploadTestAPI(t, target, false)
	localImages := make([]string, maxFeatureImagesTotal)
	for i := range localImages {
		localImages[i] = "/local/img.png"
	}
	image := stageViaAPI(t, handler, uploadKindImage, "extra.png", []byte("x"))
	w := postTrustedJSON(handler, apiPathFeatures, map[string]any{
		"name":          "too many images",
		"images":        localImages,
		"image_uploads": []string{image.Reference},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", w.Code)
	}
	if body := decodeErrorBody(t, w); body.Error.Code != string(errcat.BadRequest) || !strings.Contains(body.Error.Diagnostics, "12") {
		t.Fatalf("error = %+v; want bad_request naming the 12-image limit", body.Error)
	}
	if target.createReq != nil {
		t.Fatal("CreateFeature must not be called when the combined limit trips")
	}
	// The rejected request consumed nothing.
	if _, err := api.uploads.resolve(image.Reference, uploadKindImage); err != nil {
		t.Fatalf("reference after rejected request = %v; want still staged", err)
	}

	localAttachments := make([]string, maxFeatureAttachmentsTotal)
	for i := range localAttachments {
		localAttachments[i] = "/local/att.txt"
	}
	attachment := stageViaAPI(t, handler, uploadKindAttachment, "extra.txt", []byte("x"))
	w = postTrustedJSON(handler, apiPathFeatures, map[string]any{
		"name":               "too many attachments",
		"attachments":        localAttachments,
		"attachment_uploads": []string{attachment.Reference},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", w.Code)
	}
	if body := decodeErrorBody(t, w); body.Error.Code != string(errcat.BadRequest) || !strings.Contains(body.Error.Diagnostics, "24") {
		t.Fatalf("error = %+v; want bad_request naming the 24-attachment limit", body.Error)
	}
}

func TestRefactorEnforcesCombinedUploadCounts(t *testing.T) {
	t.Parallel()
	target := &uploadMutationRecorder{}
	_, handler, _ := newUploadTestAPI(t, target, false)
	image := stageViaAPI(t, handler, uploadKindImage, "extra.png", []byte("x"))
	w := postTrustedJSON(handler, apiPathFeatures+"/"+fixtureFeatureID+"/actions/refactor", map[string]any{
		"name":          "too many",
		"images":        make([]string, maxFeatureImagesTotal),
		"image_uploads": []string{image.Reference},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", w.Code)
	}
	if target.refactorReq != nil {
		t.Fatal("RefactorFeature must not be called when the combined limit trips")
	}
}

func TestUploadRefsAreKindCheckedOnConsume(t *testing.T) {
	t.Parallel()
	target := &uploadMutationRecorder{}
	api, handler, _ := newUploadTestAPI(t, target, false)
	attachment := stageViaAPI(t, handler, uploadKindAttachment, "notes.txt", []byte("notes"))
	w := postTrustedJSON(handler, apiPathFeatures, map[string]any{
		"name":          "kind mixup",
		"image_uploads": []string{attachment.Reference},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", w.Code)
	}
	if target.createReq != nil {
		t.Fatal("CreateFeature must not be called for a kind mismatch")
	}
	if _, err := api.uploads.resolve(attachment.Reference, uploadKindAttachment); err != nil {
		t.Fatalf("reference after kind mismatch = %v; want still staged", err)
	}
}

func TestUploadUnknownRefFailsWithoutPartialConsumption(t *testing.T) {
	t.Parallel()
	target := &uploadMutationRecorder{}
	api, handler, _ := newUploadTestAPI(t, target, false)
	image := stageViaAPI(t, handler, uploadKindImage, "real.png", []byte("image-bytes"))
	w := postTrustedJSON(handler, apiPathFeatures, map[string]any{
		"name":          "mixed refs",
		"image_uploads": []string{image.Reference, "0123456789abcdef0123456789abcdef"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", w.Code)
	}
	if target.createReq != nil {
		t.Fatal("CreateFeature must not be called when any reference is unknown")
	}
	if _, err := api.uploads.resolve(image.Reference, uploadKindImage); err != nil {
		t.Fatalf("valid reference after failed request = %v; want still staged", err)
	}
	// A malformed reference is a client error as well.
	w = postTrustedJSON(handler, apiPathFeatures, map[string]any{
		"name":          "malformed ref",
		"image_uploads": []string{"../../etc/passwd"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed ref status = %d; want 400", w.Code)
	}
}

func TestFailedMutationConsumesNothing(t *testing.T) {
	t.Parallel()
	target := &uploadMutationRecorder{createErr: errors.New("creation failed")}
	_, handler, stateDir := newUploadTestAPI(t, target, false)
	image := stageViaAPI(t, handler, uploadKindImage, "retry.png", []byte("retry-bytes"))
	w := postTrustedJSON(handler, apiPathFeatures, map[string]any{
		"name":          "will fail",
		"image_uploads": []string{image.Reference},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s; want 400", w.Code, w.Body.String())
	}
	if _, err := newUploadStore(stateDir).resolve(image.Reference, uploadKindImage); err != nil {
		t.Fatalf("reference after failed mutation = %v; want still staged for retry", err)
	}
	entries, err := os.ReadDir(filepath.Join(stateDir, uploadStagingDirName))
	if err != nil {
		t.Fatalf("read staging dir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("staging dir entries = %v; want only the staged file and its sidecar", entries)
	}
}

func TestUploadPreflightAccepted(t *testing.T) {
	t.Parallel()
	_, handler, _ := newUploadTestAPI(t, &uploadMutationRecorder{}, false)
	req := httptest.NewRequest(http.MethodOptions, apiPathUploads, nil)
	req.Header.Set("Origin", "http://127.0.0.1:9000")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type, X-Agentico-Client")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d body=%s; want 204", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:9000" {
		t.Fatalf("Access-Control-Allow-Origin = %q; want loopback origin", got)
	}
}

// TestDuplicateUploadRefInOneRequestRejected covers create, refactor, and
// chat: listing the same reference twice in one request is a client error
// that consumes nothing.
func TestDuplicateUploadRefInOneRequestRejected(t *testing.T) {
	t.Parallel()
	t.Run("create", func(t *testing.T) {
		t.Parallel()
		target := &uploadMutationRecorder{}
		api, handler, _ := newUploadTestAPI(t, target, false)
		image := stageViaAPI(t, handler, uploadKindImage, "dup.png", []byte("dup"))
		w := postTrustedJSON(handler, apiPathFeatures, map[string]any{
			"name":          "dup ref",
			"image_uploads": []string{image.Reference, image.Reference},
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d body=%s; want 400", w.Code, w.Body.String())
		}
		if target.createReq != nil {
			t.Fatal("CreateFeature must not be called for a duplicated reference")
		}
		if _, err := api.uploads.resolve(image.Reference, uploadKindImage); err != nil {
			t.Fatalf("reference after rejection = %v; want still staged", err)
		}
	})
	t.Run("refactor", func(t *testing.T) {
		t.Parallel()
		target := &uploadMutationRecorder{}
		api, handler, _ := newUploadTestAPI(t, target, false)
		attachment := stageViaAPI(t, handler, uploadKindAttachment, "dup.txt", []byte("dup"))
		w := postTrustedJSON(handler, apiPathFeatures+"/"+fixtureFeatureID+"/actions/refactor", map[string]any{
			"name":               "dup ref",
			"attachment_uploads": []string{attachment.Reference, attachment.Reference},
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d body=%s; want 400", w.Code, w.Body.String())
		}
		if target.refactorReq != nil {
			t.Fatal("RefactorFeature must not be called for a duplicated reference")
		}
		if _, err := api.uploads.resolve(attachment.Reference, uploadKindAttachment); err != nil {
			t.Fatalf("reference after rejection = %v; want still staged", err)
		}
	})
	t.Run("chat", func(t *testing.T) {
		t.Parallel()
		target := &uploadMutationRecorder{}
		api, handler, _ := newUploadTestAPI(t, target, false)
		image := stageViaAPI(t, handler, uploadKindImage, "dup.png", []byte("dup"))
		w := postTrustedJSON(handler, "/api/v1/prompts/chat/start", map[string]any{
			"message":       "dup ref",
			"image_uploads": []string{image.Reference, image.Reference},
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d body=%s; want 400", w.Code, w.Body.String())
		}
		if target.chatReq != nil {
			t.Fatal("StartChat must not be called for a duplicated reference")
		}
		if _, err := api.uploads.resolve(image.Reference, uploadKindImage); err != nil {
			t.Fatalf("reference after rejection = %v; want still staged", err)
		}
	})
	t.Run("across kinds", func(t *testing.T) {
		t.Parallel()
		target := &uploadMutationRecorder{}
		api, handler, _ := newUploadTestAPI(t, target, false)
		image := stageViaAPI(t, handler, uploadKindImage, "dup.png", []byte("dup"))
		w := postTrustedJSON(handler, apiPathFeatures, map[string]any{
			"name":               "dup across kinds",
			"image_uploads":      []string{image.Reference},
			"attachment_uploads": []string{image.Reference},
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d body=%s; want 400", w.Code, w.Body.String())
		}
		if target.createReq != nil {
			t.Fatal("CreateFeature must not be called for a duplicated reference")
		}
		if _, err := api.uploads.resolve(image.Reference, uploadKindImage); err != nil {
			t.Fatalf("reference after rejection = %v; want still staged", err)
		}
	})
}

// TestConcurrentUploadConsumers covers create, refactor, and chat: racing
// requests for the same staged reference yield exactly one winner; losers
// fail with 400 and nothing is partially consumed.
func TestConcurrentUploadConsumers(t *testing.T) {
	t.Parallel()
	const racers = 8
	run := func(t *testing.T, path string, body func(ref string) map[string]any, wantOK int) {
		t.Helper()
		target := &uploadMutationRecorder{}
		_, handler, stateDir := newUploadTestAPI(t, target, false)
		image := stageViaAPI(t, handler, uploadKindImage, "race.png", []byte("race-bytes"))
		codes := make(chan int, racers)
		var wg sync.WaitGroup
		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				w := postTrustedJSON(handler, path, body(image.Reference))
				codes <- w.Code
			}()
		}
		wg.Wait()
		close(codes)
		successes, rejects := 0, 0
		for code := range codes {
			switch code {
			case wantOK:
				successes++
			case http.StatusBadRequest:
				rejects++
			default:
				t.Fatalf("unexpected status %d; want exactly one %d and rest 400", code, wantOK)
			}
		}
		if successes != 1 || rejects != racers-1 {
			t.Fatalf("outcomes = %d successes, %d rejects; want 1 and %d", successes, rejects, racers-1)
		}
		// The winner consumed the single-use reference exactly once.
		if _, err := os.Stat(filepath.Join(stateDir, uploadStagingDirName, image.Reference)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("staged file after race err = %v; want deleted", err)
		}
		if _, err := newUploadStore(stateDir).resolve(image.Reference, uploadKindImage); err == nil {
			t.Fatal("resolve after race: want the reference to stay consumed")
		}
	}
	t.Run("create", func(t *testing.T) {
		t.Parallel()
		run(t, apiPathFeatures, func(ref string) map[string]any {
			return map[string]any{"name": "racing create", "image_uploads": []string{ref}}
		}, http.StatusCreated)
	})
	t.Run("refactor", func(t *testing.T) {
		t.Parallel()
		run(t, apiPathFeatures+"/"+fixtureFeatureID+"/actions/refactor", func(ref string) map[string]any {
			return map[string]any{"name": "racing refactor", "image_uploads": []string{ref}}
		}, http.StatusCreated)
	})
	t.Run("chat", func(t *testing.T) {
		t.Parallel()
		run(t, "/api/v1/prompts/chat/start", func(ref string) map[string]any {
			return map[string]any{"message": "racing chat", "image_uploads": []string{ref}}
		}, http.StatusOK)
	})
}
