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

package testutil

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/github"
)

// FakeGitHubAPI is an httptest-backed GitHub API double. Install it and
// register handlers on Mux (REST paths like "/repos/o/r/pulls/1/comments",
// GraphQL at "/graphql"). Every request is logged as
// "METHOD /path?query BODY" so tests can count invocations.
type FakeGitHubAPI struct {
	Mux *http.ServeMux
	URL string

	mu       sync.Mutex
	requests []string
}

// InstallFakeGitHubAPI starts the server and routes internal/github
// clients at it for the duration of the test. Not safe for t.Parallel()
// (process-global override).
func InstallFakeGitHubAPI(t testing.TB) *FakeGitHubAPI {
	t.Helper()
	f := &FakeGitHubAPI{Mux: http.NewServeMux()}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		f.mu.Unlock()
		f.Mux.ServeHTTP(w, r)
	}))
	f.URL = server.URL
	restore := github.OverrideForTest(server.URL, "test-token")
	t.Cleanup(func() {
		restore()
		server.Close()
	})
	return f
}

// HandleJSON registers a handler answering with a fixed status and body.
func (f *FakeGitHubAPI) HandleJSON(pattern string, status int, body string) {
	f.Mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

// Requests returns the logged request lines in arrival order.
func (f *FakeGitHubAPI) Requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

// RequestCount counts logged requests containing substr.
func (f *FakeGitHubAPI) RequestCount(substr string) int {
	count := 0
	for _, line := range f.Requests() {
		if strings.Contains(line, substr) {
			count++
		}
	}
	return count
}
