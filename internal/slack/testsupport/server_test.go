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

package testsupport

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestServerScriptsRecordsAndFallsBack(t *testing.T) {
	server := New(t)
	server.Script("chat.postMessage",
		Response{Body: map[string]any{"ok": true, "ts": "1.0"}},
		Response{Status: http.StatusTooManyRequests, Body: map[string]any{"ok": false}},
	)

	body, _ := json.Marshal(map[string]any{"channel": "C123", "text": "hello"})
	req, err := http.NewRequest(http.MethodPost, server.URL()+"chat.postMessage", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	for range 2 {
		resp, err := http.DefaultClient.Do(req.Clone(t.Context()))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	requests := server.Requests("chat.postMessage")
	if len(requests) != 2 || !requests[0].BearerPresent ||
		requests[0].Fields["channel"] != "C123" {
		t.Fatalf("Requests(chat.postMessage) = %#v", requests)
	}

	resp, err := http.Post(server.URL()+"unscripted", "application/x-www-form-urlencoded", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var fallback map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&fallback); err != nil {
		t.Fatal(err)
	}
	if fallback["ok"] != false || fallback["error"] != "unknown_method" {
		t.Fatalf("unscripted response = %#v; want unknown_method", fallback)
	}
}

func TestServerDefaultResponderAnswersUnscriptedCalls(t *testing.T) {
	server := NewServer()
	defer server.Close()

	var calls atomic.Int64
	server.SetDefault(func(method string, request Request) Response {
		calls.Add(1)
		if method != "chat.postMessage" {
			return Response{Body: map[string]any{"ok": false, "error": "unexpected " + method}}
		}
		return Response{Body: map[string]any{
			"ok": true, "ts": "1234.00000" + strconv.Itoa(int(calls.Load())),
			"channel": request.Fields["channel"],
		}}
	})

	_, _ = http.Post(server.URL()+"chat.postMessage", "application/x-www-form-urlencoded",
		strings.NewReader("channel=C1&text=hi"))
	_, _ = http.Post(server.URL()+"chat.postMessage", "application/x-www-form-urlencoded",
		strings.NewReader("channel=C1&text=again"))

	if got := calls.Load(); got != 2 {
		t.Fatalf("default responder calls = %d; want 2", got)
	}
	requests := server.Requests("chat.postMessage")
	if len(requests) != 2 {
		t.Fatalf("recorded posts = %d; want 2", len(requests))
	}

	// A scripted response still takes precedence over the default.
	server.Script("chat.postMessage", Response{Body: map[string]any{"ok": true, "ts": "9.9"}})
	_, _ = http.Post(server.URL()+"chat.postMessage", "application/x-www-form-urlencoded",
		strings.NewReader("channel=C1&text=scripted"))
	if got := calls.Load(); got != 2 {
		t.Fatalf("scripted response bypassed the default: responder calls = %d", got)
	}
}

func TestServerExternalUploadDefaultsAndRawMetadata(t *testing.T) {
	server := New(t)

	resp, err := http.Post(
		server.URL()+"files.getUploadURLExternal",
		"application/x-www-form-urlencoded",
		strings.NewReader("filename=phase-plan.md&length=12"),
	)
	if err != nil {
		t.Fatal(err)
	}
	var uploadURL struct {
		OK        bool   `json:"ok"`
		UploadURL string `json:"upload_url"`
		FileID    string `json:"file_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&uploadURL); err != nil {
		_ = resp.Body.Close()
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if !uploadURL.OK || uploadURL.FileID != "F00000001" ||
		uploadURL.UploadURL != strings.TrimSuffix(server.URL(), "/api/")+"/upload/F00000001" {
		t.Fatalf("getUploadURLExternal response = %#v; want local upload URL and file ID", uploadURL)
	}

	payload := []byte("hello upload")
	req, err := http.NewRequest(http.MethodPost, uploadURL.UploadURL, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("raw upload status = %d; want 200", resp.StatusCode)
	}

	files, _ := json.Marshal([]map[string]string{{
		"id": uploadURL.FileID, "title": "Phase 3 plan",
	}})
	resp, err = http.Post(
		server.URL()+"files.completeUploadExternal",
		"application/x-www-form-urlencoded",
		strings.NewReader(url.Values{
			"files":      {string(files)},
			"channel_id": {"C12345678"},
			"thread_ts":  {"1.0"},
		}.Encode()),
	)
	if err != nil {
		t.Fatal(err)
	}
	var completion map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&completion); err != nil {
		_ = resp.Body.Close()
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if completion["ok"] != true {
		t.Fatalf("completeUploadExternal response = %#v; want ok", completion)
	}

	requests := server.AllRequests()
	if len(requests) != 3 {
		t.Fatalf("AllRequests() = %#v; want three upload steps", requests)
	}
	digest := sha256.Sum256(payload)
	raw := requests[1]
	if raw.Method != http.MethodPost || raw.Path != "/upload/F00000001" ||
		raw.BearerPresent || raw.ContentType != "application/octet-stream" ||
		raw.ContentLength != int64(len(payload)) || raw.Digest != fmt.Sprintf("%x", digest) {
		t.Fatalf("raw request = %#v; want recorded content metadata", raw)
	}
}

func TestServerExternalUploadStepsAreScriptedIndependently(t *testing.T) {
	server := New(t)
	server.Script("files.getUploadURLExternal", Response{
		Status: http.StatusCreated,
		Body: map[string]any{
			"ok": true, "upload_url": strings.TrimSuffix(server.URL(), "/api/") + "/arbitrary-upload-target",
			"file_id": "FSCRIPTED",
		},
	})
	server.Script("upload", Response{Status: http.StatusAccepted, Body: "accepted"})
	server.Script("files.completeUploadExternal", Response{
		Status: http.StatusNoContent,
	})

	responses := make([]*http.Response, 0, 3)
	for _, request := range []struct {
		target      string
		contentType string
		body        io.Reader
	}{
		{server.URL() + "files.getUploadURLExternal", "application/x-www-form-urlencoded", nil},
		{strings.TrimSuffix(server.URL(), "/api/") + "/arbitrary-upload-target", "application/octet-stream", strings.NewReader("bytes")},
		{server.URL() + "files.completeUploadExternal", "application/x-www-form-urlencoded", nil},
	} {
		resp, err := http.Post(request.target, request.contentType, request.body)
		if err != nil {
			t.Fatal(err)
		}
		responses = append(responses, resp)
		t.Cleanup(func() { _ = resp.Body.Close() })
	}
	if responses[0].StatusCode != http.StatusCreated ||
		responses[1].StatusCode != http.StatusAccepted ||
		responses[2].StatusCode != http.StatusNoContent {
		t.Fatalf("scripted statuses = %d, %d, %d; want 201, 202, 204",
			responses[0].StatusCode, responses[1].StatusCode, responses[2].StatusCode)
	}
}

func TestServerSeededThreadsAndScriptsCoexistPerMethod(t *testing.T) {
	server := New(t)
	server.SetOwnUserID("UAGENTICO")
	server.SeedThread("C123", "10.0", []Message{
		{TS: "10.0", ThreadTS: "10.0", User: "UROOT", Text: "root"},
		{TS: "11.0", ThreadTS: "10.0", User: "U1", Text: "first"},
	})
	server.Script("conversations.replies", Response{
		Status: http.StatusServiceUnavailable,
	})
	server.Script("reactions.add", Response{
		Body: map[string]any{"ok": false, "error": "scripted_failure"},
	})

	postForm := func(method string, values url.Values) *http.Response {
		t.Helper()
		resp, err := http.Post(
			server.URL()+method,
			"application/x-www-form-urlencoded",
			strings.NewReader(values.Encode()),
		)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	if got := postForm("conversations.replies", url.Values{
		"channel": {"C123"}, "ts": {"10.0"}, "oldest": {"10.0"},
		"inclusive": {"true"}, "limit": {"100"},
	}).StatusCode; got != http.StatusServiceUnavailable {
		t.Fatalf("scripted conversations.replies status = %d; want 503", got)
	}
	scriptedReaction := postForm("reactions.add", url.Values{
		"channel": {"C123"}, "timestamp": {"11.0"}, "name": {"white_check_mark"},
	})
	var envelope map[string]any
	if err := json.NewDecoder(scriptedReaction.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["error"] != "scripted_failure" {
		t.Fatalf("scripted reactions.add response = %#v; want scripted failure", envelope)
	}

	reactionResponse := postForm("reactions.add", url.Values{
		"channel": {"C123"}, "timestamp": {"11.0"}, "name": {"white_check_mark"},
	})
	if err := json.NewDecoder(reactionResponse.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["ok"] != true {
		t.Fatalf("seeded reactions.add response = %#v; want ok", envelope)
	}
	repliesResponse := postForm("conversations.replies", url.Values{
		"channel": {"C123"}, "ts": {"10.0"}, "oldest": {"11.0"},
		"inclusive": {"true"}, "limit": {"100"},
	})
	var replies struct {
		OK       bool      `json:"ok"`
		Messages []Message `json:"messages"`
	}
	if err := json.NewDecoder(repliesResponse.Body).Decode(&replies); err != nil {
		t.Fatal(err)
	}
	if !replies.OK || len(replies.Messages) != 1 ||
		len(replies.Messages[0].Reactions) != 1 ||
		replies.Messages[0].Reactions[0].Users[0] != "UAGENTICO" {
		t.Fatalf("seeded conversations.replies response = %#v; want reflected reaction", replies)
	}
}
