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

package github_test

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/github"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

func TestReviewThreadMapReturnsUnresolvedThreadsOnly(t *testing.T) {
	fake := testutil.InstallFakeGitHubAPI(t)
	fake.Mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "reviewThreads") {
			t.Errorf("query = %s; want reviewThreads query", body)
		}
		fmt.Fprint(w, `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
			{"id":"PRRT_abc","isResolved":false,"comments":{"nodes":[{"databaseId":100}]}},
			{"id":"PRRT_def","isResolved":true,"comments":{"nodes":[{"databaseId":200}]}}
		]}}}}}`)
	})

	client, _ := github.ForHost("github.com")
	threads, err := client.ReviewThreadMap("acme", "widgets", 7)
	if err != nil {
		t.Fatalf("ReviewThreadMap() error = %v", err)
	}
	if len(threads) != 1 || threads[100] != "PRRT_abc" {
		t.Fatalf("threads = %v; want only unresolved 100→PRRT_abc", threads)
	}
}

func TestResolveReviewThreadSendsMutation(t *testing.T) {
	fake := testutil.InstallFakeGitHubAPI(t)
	fake.Mux.HandleFunc("/graphql", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "resolveReviewThread") || !strings.Contains(string(body), "PRRT_abc") {
			t.Errorf("mutation body = %s; want resolveReviewThread for PRRT_abc", body)
		}
		fmt.Fprint(w, `{"data":{"resolveReviewThread":{"thread":{"isResolved":true}}}}`)
	})

	client, _ := github.ForHost("github.com")
	if err := client.ResolveReviewThread("PRRT_abc"); err != nil {
		t.Fatalf("ResolveReviewThread() error = %v", err)
	}
}
