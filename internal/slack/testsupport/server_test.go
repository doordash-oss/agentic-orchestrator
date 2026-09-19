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
	"encoding/json"
	"io"
	"net/http"
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
