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

package main

import (
	"sync"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
)

func TestGitFreshnessProviderCachesProbes(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	p := newGitFreshnessProviderWithProbe(func(string) string {
		mu.Lock()
		calls++
		mu.Unlock()
		return "in sync"
	})
	repo := feature.FeatureRepo{Name: "api", WorktreePath: "/tmp/api"}
	if got := p.Freshness(nil, repo); got != serverruntime.RepoFreshnessInSync {
		t.Fatalf("first probe = %q, want in sync", got)
	}
	if got := p.Freshness(nil, repo); got != serverruntime.RepoFreshnessInSync {
		t.Fatalf("cached probe = %q, want in sync", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("probe ran %d times, want 1 (second read cached)", calls)
	}
}

func TestGitFreshnessProviderCachesPerWorktree(t *testing.T) {
	p := newGitFreshnessProviderWithProbe(func(path string) string {
		if path == "/tmp/api" {
			return "in sync"
		}
		return "local changes"
	})
	if got := p.Freshness(nil, feature.FeatureRepo{Name: "api", WorktreePath: "/tmp/api"}); got != serverruntime.RepoFreshnessInSync {
		t.Fatalf("api = %q, want in sync", got)
	}
	if got := p.Freshness(nil, feature.FeatureRepo{Name: "web", Path: "/tmp/web"}); got != serverruntime.RepoFreshnessLocalChanges {
		t.Fatalf("web = %q, want local changes", got)
	}
}

func TestGitFreshnessProviderMapsUnknownProbeResult(t *testing.T) {
	p := newGitFreshnessProviderWithProbe(func(string) string { return "unknown" })
	repo := feature.FeatureRepo{Name: "api", WorktreePath: "/tmp/api"}
	if got := p.Freshness(nil, repo); got != serverruntime.RepoFreshnessUnknown {
		t.Fatalf("freshness = %q, want unknown", got)
	}
}
