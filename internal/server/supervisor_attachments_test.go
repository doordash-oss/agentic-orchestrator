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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/supervisor"
)

// stagingSupervisor is a SupervisorService double that runs the handler's
// attachment staging into dir the way the coordinator does: a committed
// client message id deduplicates without staging, a scripted error rolls
// the staging back, and success commits it.
type stagingSupervisor struct {
	recordingSupervisor
	dir       string
	sendErr   error
	messages  []supervisor.Message
	committed map[string]supervisor.UserData
}

func (s *stagingSupervisor) SendMessage(_ context.Context, msg supervisor.Message) (supervisor.SendResult, error) {
	s.messages = append(s.messages, msg)
	record := func(data supervisor.UserData) supervisor.Record {
		raw, _ := json.Marshal(data)
		return supervisor.Record{Seq: 1, ID: "r1", ConversationID: "conv-1", Generation: 1, TurnID: "g1.t1", Kind: supervisor.KindUser,
			Visibility: supervisor.VisibilityContent, ClientMessageID: msg.ClientMessageID, Data: raw, CreatedAt: time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)}
	}
	if data, ok := s.committed[msg.ClientMessageID]; ok {
		return supervisor.SendResult{Record: record(data), Deduplicated: true}, nil
	}
	atts := msg.Attachments
	finish := func(bool) {}
	if msg.Stage != nil {
		staged, done, err := msg.Stage(s.dir)
		if err != nil {
			return supervisor.SendResult{}, err
		}
		atts, finish = staged, done
	}
	if s.sendErr != nil {
		finish(false)
		return supervisor.SendResult{}, s.sendErr
	}
	finish(true)
	data := supervisor.UserData{Text: msg.Text, Attachments: atts}
	if s.committed == nil {
		s.committed = map[string]supervisor.UserData{}
	}
	s.committed[msg.ClientMessageID] = data
	return supervisor.SendResult{Record: record(data), Launched: true}, nil
}

type attachmentFixture struct {
	t        *testing.T
	stateDir string
	dir      string
	svc      *stagingSupervisor
	handler  http.Handler
}

func newAttachmentFixture(t *testing.T) *attachmentFixture {
	t.Helper()
	stateDir := t.TempDir()
	svc := &stagingSupervisor{dir: filepath.Join(stateDir, "supervisor", "conversations", "conv-1", "attachments")}
	api := newAPIHandler(HandlerOptions{
		Runtime:               RuntimeIdentity{RuntimeDir: filepath.Dir(stateDir), StateDir: stateDir, Config: testRuntimeConfigPath},
		Config:                config.NewDefault(),
		AuthToken:             testAuthToken,
		DisableHostValidation: true,
		Supervisor:            svc,
	})
	return &attachmentFixture{t: t, stateDir: stateDir, dir: svc.dir, svc: svc, handler: api.routes()}
}

func (f *attachmentFixture) stage(kind, name string, body []byte) string {
	f.t.Helper()
	w := authedPostUpload(f.handler, apiPathUploads+"?kind="+kind+"&name="+name, body)
	if w.Code != http.StatusOK {
		f.t.Fatalf("stage %s: %d %s", name, w.Code, w.Body.String())
	}
	var resp StageUploadResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		f.t.Fatal(err)
	}
	return resp.Reference
}

func (f *attachmentFixture) send(body map[string]any) *httptest.ResponseRecorder {
	f.t.Helper()
	if _, ok := body["client_message_id"]; !ok {
		body["client_message_id"] = "cm-1"
	}
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, supervisorRequest(http.MethodPost, apiPathSupervisorMessages, string(raw)))
	return w
}

func (f *attachmentFixture) copies() []string {
	f.t.Helper()
	entries, err := os.ReadDir(f.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func (f *attachmentFixture) assertConsumable(refs map[string]string) {
	f.t.Helper()
	store := newUploadStore(f.stateDir)
	for ref, kind := range refs {
		if _, err := store.resolve(ref, kind); err != nil {
			f.t.Fatalf("ref %s is no longer consumable: %v", ref, err)
		}
	}
}

func writeLocal(t *testing.T, name string, body []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSupervisorMessage_AttachmentsFromLocalPathsAndUploadsAreCopiedIntoTheConversation(t *testing.T) {
	f := newAttachmentFixture(t)
	localImage := writeLocal(t, "local.png", []byte("local-png"))
	localFile := writeLocal(t, "notes.txt", []byte("local notes"))
	imageRef := f.stage(uploadKindImage, "remote.jpg", []byte("remote-jpg"))
	fileRef := f.stage(uploadKindAttachment, "spec.pdf", []byte("remote pdf bytes"))

	w := f.send(map[string]any{
		"text": "look", "images": []string{localImage}, "image_uploads": []string{imageRef},
		"attachments": []string{localFile}, "attachment_uploads": []string{fileRef},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("send = %d %s", w.Code, w.Body.String())
	}
	var resp SupervisorMessageResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	type want struct {
		kind SupervisorAttachmentKind
		name string
		ext  string
		body string
	}
	wants := []want{
		{SupervisorAttachmentKind(supervisor.AttachmentKindImage), "local.png", ".png", "local-png"},
		{SupervisorAttachmentKind(supervisor.AttachmentKindImage), "remote.jpg", ".jpg", "remote-jpg"},
		{SupervisorAttachmentKind(supervisor.AttachmentKindFile), "notes.txt", ".txt", "local notes"},
		{SupervisorAttachmentKind(supervisor.AttachmentKindFile), "spec.pdf", ".pdf", "remote pdf bytes"},
	}
	got := resp.Record.Attachments
	if len(got) != len(wants) {
		t.Fatalf("record attachments = %+v", got)
	}
	for i, w := range wants {
		a := got[i]
		if a.Kind != w.kind || a.Name != w.name || a.Size != int64(len(w.body)) || filepath.Dir(a.Path) != f.dir || filepath.Ext(a.Path) != w.ext {
			t.Fatalf("attachment %d = %+v, want %+v under %s", i, a, w, f.dir)
		}
		body, err := os.ReadFile(a.Path)
		if err != nil || string(body) != w.body {
			t.Fatalf("copy %s = %q, %v", a.Path, body, err)
		}
	}
	if len(f.copies()) != 4 {
		t.Fatalf("copies = %v", f.copies())
	}
	// The originals are untouched and the staged sources are gone.
	for path, body := range map[string]string{localImage: "local-png", localFile: "local notes"} {
		if b, err := os.ReadFile(path); err != nil || string(b) != body {
			t.Fatalf("original %s = %q, %v", path, b, err)
		}
	}
	for _, ref := range []string{imageRef, fileRef} {
		if _, err := os.Stat(filepath.Join(f.stateDir, uploadStagingDirName, ref)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("staged source %s survived: %v", ref, err)
		}
	}
	// The coordinator saw the identity before staging, in harness order.
	msg := f.svc.messages[0]
	if len(msg.Attachments) != 4 || msg.Attachments[1].Name != "remote.jpg" || msg.Attachments[3].Size != int64(len("remote pdf bytes")) {
		t.Fatalf("message descriptors = %+v", msg.Attachments)
	}

	// An identical resend deduplicates without consuming anything, even
	// though the refs are already consumed.
	w = f.send(map[string]any{
		"text": "look", "images": []string{localImage}, "image_uploads": []string{imageRef},
		"attachments": []string{localFile}, "attachment_uploads": []string{fileRef},
	})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"deduplicated":true`) {
		t.Fatalf("resend = %d %s", w.Code, w.Body.String())
	}
	if len(f.copies()) != 4 {
		t.Fatalf("resend left extra copies: %v", f.copies())
	}
	// A consumed ref under a new client message id is the upload-reference error.
	w = f.send(map[string]any{"text": "again", "client_message_id": "cm-2", "image_uploads": []string{imageRef}})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "already consumed") {
		t.Fatalf("reused ref = %d %s", w.Code, w.Body.String())
	}
	if len(f.copies()) != 4 {
		t.Fatalf("reused ref left copies: %v", f.copies())
	}
}

func TestSupervisorMessage_BlankTextNeedsAnAttachment(t *testing.T) {
	f := newAttachmentFixture(t)
	if w := f.send(map[string]any{"text": "  "}); w.Code != http.StatusBadRequest {
		t.Fatalf("blank text without attachments = %d", w.Code)
	}
	w := f.send(map[string]any{"text": "", "attachments": []string{writeLocal(t, "a.log", []byte("x"))}})
	if w.Code != http.StatusOK {
		t.Fatalf("blank text with attachment = %d %s", w.Code, w.Body.String())
	}
	var resp SupervisorMessageResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Record.Attachments) != 1 || resp.Record.Attachments[0].Name != "a.log" {
		t.Fatalf("attachments = %+v", resp.Record.Attachments)
	}
}

func TestSupervisorMessage_RefusedOrFailedSendLeavesNoCopiesAndRefsConsumable(t *testing.T) {
	for name, err := range map[string]error{
		"settings_required": supervisor.ErrSettingsRequired,
		"turn_active":       supervisor.ErrTurnActive,
		"launch_failed":     &supervisor.LaunchFailedError{Err: errors.New("boom")},
	} {
		t.Run(name, func(t *testing.T) {
			f := newAttachmentFixture(t)
			f.svc.sendErr = err
			imageRef := f.stage(uploadKindImage, "shot.png", []byte("png"))
			fileRef := f.stage(uploadKindAttachment, "spec.pdf", []byte("pdf"))
			w := f.send(map[string]any{
				"text": "hi", "images": []string{writeLocal(t, "local.png", []byte("l"))},
				"image_uploads": []string{imageRef}, "attachment_uploads": []string{fileRef},
			})
			if w.Code < 400 {
				t.Fatalf("send = %d", w.Code)
			}
			if c := f.copies(); len(c) != 0 {
				t.Fatalf("copies left behind: %v", c)
			}
			f.assertConsumable(map[string]string{imageRef: uploadKindImage, fileRef: uploadKindAttachment})
		})
	}
}

func TestSupervisorMessage_AttachmentValidationHappensBeforeAnyCopy(t *testing.T) {
	f := newAttachmentFixture(t)
	imageRef := f.stage(uploadKindImage, "shot.png", []byte("png"))
	big := filepath.Join(t.TempDir(), "big.png")
	fh, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := fh.Truncate(maxUploadImageBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = fh.Close()
	thirteen := make([]string, 13)
	for i := range thirteen {
		thirteen[i] = writeLocal(t, fmt.Sprintf("i%d.png", i), []byte("p"))
	}
	for name, tc := range map[string]struct {
		body map[string]any
		diag string
	}{
		"oversized local image": {map[string]any{"text": "hi", "images": []string{big}, "image_uploads": []string{imageRef}}, "10 MiB"},
		"13th image":            {map[string]any{"text": "hi", "images": thirteen[:12], "image_uploads": []string{imageRef}}, "too many images"},
		"13 local images":       {map[string]any{"text": "hi", "images": thirteen}, ""},
		"relative path":         {map[string]any{"text": "hi", "attachments": []string{"notes.txt"}}, "absolute"},
		"missing path":          {map[string]any{"text": "hi", "attachments": []string{"/definitely/not/here.txt"}}, "regular file"},
		"directory":             {map[string]any{"text": "hi", "attachments": []string{t.TempDir()}}, "regular file"},
		"image extension":       {map[string]any{"text": "hi", "images": []string{writeLocal(t, "x.bmp", []byte("b"))}}, "png, jpg"},
		"unknown ref":           {map[string]any{"text": "hi", "image_uploads": []string{strings.Repeat("ab", 16)}}, "unknown, expired, or already consumed"},
		"malformed ref":         {map[string]any{"text": "hi", "image_uploads": []string{"../x"}}, "invalid format"},
		"wrong kind ref":        {map[string]any{"text": "hi", "attachment_uploads": []string{imageRef}}, "has kind"},
		"duplicate ref":         {map[string]any{"text": "hi", "image_uploads": []string{imageRef, imageRef}}, "more than once"},
	} {
		t.Run(name, func(t *testing.T) {
			w := f.send(map[string]any(tc.body))
			if w.Code != http.StatusBadRequest || !bytes.Contains(w.Body.Bytes(), []byte(`"code":"bad_request"`)) || !strings.Contains(w.Body.String(), tc.diag) {
				t.Fatalf("send = %d %s, want bad_request naming %q", w.Code, w.Body.String(), tc.diag)
			}
		})
	}
	if len(f.svc.messages) != 0 {
		t.Fatalf("an invalid request reached the coordinator: %+v", f.svc.messages)
	}
	if c := f.copies(); len(c) != 0 {
		t.Fatalf("copies made: %v", c)
	}
	f.assertConsumable(map[string]string{imageRef: uploadKindImage})
}
