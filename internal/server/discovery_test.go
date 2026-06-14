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
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/instancelock"
)

func TestStartServerBindsLoopbackAndServesHealth(t *testing.T) {
	t.Parallel()
	runtimeDir := t.TempDir()
	identity := RuntimeIdentity{
		RuntimeDir: runtimeDir,
		StateDir:   filepath.Join(runtimeDir, "features"),
		Config:     filepath.Join(runtimeDir, "config.yaml"),
	}
	policy := NewLaunchPolicy([]string{"codex"}, true)
	srv, err := Start(context.Background(), Options{
		Runtime:      identity,
		LaunchPolicy: policy,
		StartMode:    "server",
		Owner:        instancelock.Owner{PID: os.Getpid(), StartedAt: time.Now()},
		Features:     featureListerFunc(nil),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := srv.Close(ctx); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	})

	baseURL := srv.BaseURL()
	if !strings.HasPrefix(baseURL, "http://127.0.0.1:") {
		t.Fatalf("BaseURL() = %q; want loopback dynamic port", baseURL)
	}

	resp, err := http.Get(baseURL + "/api/v1/health")
	if err != nil {
		t.Fatalf("GET /api/v1/health error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d; want 200", resp.StatusCode)
	}
	var body HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if body.APIVersion != APIVersion || body.Status != "ok" {
		t.Fatalf("health body = %+v; want api version %q and ok status", body, APIVersion)
	}
	if body.Runtime.StateDir != identity.StateDir {
		t.Fatalf("health runtime state_dir = %q; want %q", body.Runtime.StateDir, identity.StateDir)
	}
	if !launchPolicyEquivalent(body.LaunchPolicy, policy) {
		t.Fatalf("health launch_policy = %+v; want %+v", body.LaunchPolicy, policy)
	}
}

func TestPublishDiscoveryWritesOwnerOnlyAtomically(t *testing.T) {
	t.Parallel()
	runtimeDir := t.TempDir()
	rec := DiscoveryRecord{
		SchemaVersion: 1,
		APIVersion:    APIVersion,
		BaseURL:       "http://127.0.0.1:4567",
		Runtime: RuntimeIdentity{
			RuntimeDir: runtimeDir,
			StateDir:   filepath.Join(runtimeDir, "features"),
			Config:     filepath.Join(runtimeDir, "config.yaml"),
		},
		StartMode:   "server",
		PID:         os.Getpid(),
		StartedAt:   time.Now().UTC(),
		PublishedAt: time.Now().UTC(),
	}

	if err := PublishDiscovery(runtimeDir, rec); err != nil {
		t.Fatalf("PublishDiscovery() error = %v", err)
	}
	info, err := os.Stat(DiscoveryPath(runtimeDir))
	if err != nil {
		t.Fatalf("Stat(discovery) error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("discovery mode = %#o; want 0600", got)
	}
	matches, err := filepath.Glob(filepath.Join(runtimeDir, ".agentico-server-*.json.tmp"))
	if err != nil {
		t.Fatalf("Glob(tmp): %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary discovery files left behind: %v", matches)
	}

	got, err := ReadDiscovery(runtimeDir)
	if err != nil {
		t.Fatalf("ReadDiscovery() error = %v", err)
	}
	if got.BaseURL != rec.BaseURL || got.Runtime.StateDir != rec.Runtime.StateDir {
		t.Fatalf("ReadDiscovery() = %+v; want published record", got)
	}
}

func TestPrepareDiscoveryRejectsNonLoopbackWithoutProbe(t *testing.T) {
	t.Parallel()
	runtimeDir := t.TempDir()
	identity := RuntimeIdentity{
		RuntimeDir: runtimeDir,
		StateDir:   filepath.Join(runtimeDir, "features"),
		Config:     filepath.Join(runtimeDir, "config.yaml"),
	}
	policy := NewLaunchPolicy(nil, false)
	rec := DiscoveryRecord{
		SchemaVersion: 1,
		APIVersion:    APIVersion,
		BaseURL:       "http://192.0.2.20:4567",
		Runtime:       identity,
		LaunchPolicy:  policy,
		StartMode:     "server",
	}
	if err := PublishDiscovery(runtimeDir, rec); err != nil {
		t.Fatalf("PublishDiscovery() error = %v", err)
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("PrepareDiscovery probed a non-loopback discovery URL")
		return nil, errors.New("unreachable")
	})}

	decision, err := PrepareDiscovery(context.Background(), runtimeDir, identity, policy, client)
	if err != nil {
		t.Fatalf("PrepareDiscovery() error = %v", err)
	}
	if decision.AlreadyRunning || !decision.Replace {
		t.Fatalf("decision = %+v; want safe stale replacement", decision)
	}
	if !strings.Contains(decision.Reason, "non-loopback") {
		t.Fatalf("decision reason = %q; want non-loopback", decision.Reason)
	}
}

func TestPrepareDiscoveryRejectsUnsafePermissionsWithoutProbe(t *testing.T) {
	t.Parallel()
	runtimeDir := t.TempDir()
	identity := RuntimeIdentity{
		RuntimeDir: runtimeDir,
		StateDir:   filepath.Join(runtimeDir, "features"),
		Config:     filepath.Join(runtimeDir, "config.yaml"),
	}
	policy := NewLaunchPolicy(nil, false)
	if err := PublishDiscovery(runtimeDir, DiscoveryRecord{
		SchemaVersion: 1,
		APIVersion:    APIVersion,
		BaseURL:       "http://127.0.0.1:4567",
		Runtime:       identity,
		LaunchPolicy:  policy,
		StartMode:     "server",
	}); err != nil {
		t.Fatalf("PublishDiscovery() error = %v", err)
	}
	if err := os.Chmod(DiscoveryPath(runtimeDir), 0o644); err != nil {
		t.Fatalf("Chmod(discovery) error = %v", err)
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("PrepareDiscovery probed an unsafe discovery file")
		return nil, errors.New("unreachable")
	})}

	decision, err := PrepareDiscovery(context.Background(), runtimeDir, identity, policy, client)
	if err != nil {
		t.Fatalf("PrepareDiscovery() error = %v", err)
	}
	if decision.AlreadyRunning || !decision.Replace {
		t.Fatalf("decision = %+v; want unsafe metadata replacement", decision)
	}
	if !strings.Contains(decision.Reason, "unsafe discovery permissions") {
		t.Fatalf("decision reason = %q; want unsafe permissions", decision.Reason)
	}
}

func TestPrepareDiscoveryClassifiesHealthyMatchingServer(t *testing.T) {
	t.Parallel()
	runtimeDir := t.TempDir()
	identity := RuntimeIdentity{
		RuntimeDir: runtimeDir,
		StateDir:   filepath.Join(runtimeDir, "features"),
		Config:     filepath.Join(runtimeDir, "config.yaml"),
	}
	policy := NewLaunchPolicy(nil, false)
	rec := DiscoveryRecord{
		SchemaVersion: 1,
		APIVersion:    APIVersion,
		BaseURL:       "http://127.0.0.1:4567",
		Runtime:       identity,
		LaunchPolicy:  policy,
		StartMode:     "server",
	}
	if err := PublishDiscovery(runtimeDir, rec); err != nil {
		t.Fatalf("PublishDiscovery() error = %v", err)
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, HealthResponse{
			APIVersion:   APIVersion,
			Status:       "ok",
			Runtime:      identity,
			LaunchPolicy: policy,
		})
	})}

	decision, err := PrepareDiscovery(context.Background(), runtimeDir, identity, policy, client)
	if err != nil {
		t.Fatalf("PrepareDiscovery() error = %v", err)
	}
	if !decision.AlreadyRunning || decision.Replace {
		t.Fatalf("decision = %+v; want already running", decision)
	}
}

func TestPrepareDiscoveryRequiresPolicyEquivalentHealthyServer(t *testing.T) {
	t.Parallel()
	runtimeDir := t.TempDir()
	identity := RuntimeIdentity{
		RuntimeDir: runtimeDir,
		StateDir:   filepath.Join(runtimeDir, "features"),
		Config:     filepath.Join(runtimeDir, "config.yaml"),
	}
	policy := NewLaunchPolicy([]string{"codex"}, true)

	tests := []struct {
		name         string
		recordPolicy LaunchPolicy
		healthPolicy LaunchPolicy
		wantReason   string
	}{
		{
			name:         "provider restriction differs in discovery",
			recordPolicy: NewLaunchPolicy([]string{"claude"}, true),
			healthPolicy: policy,
			wantReason:   "mismatched discovery policy",
		},
		{
			name:         "dangerous skip differs in discovery",
			recordPolicy: NewLaunchPolicy([]string{"codex"}, false),
			healthPolicy: policy,
			wantReason:   "mismatched discovery policy",
		},
		{
			name:         "provider restriction differs in health",
			recordPolicy: policy,
			healthPolicy: NewLaunchPolicy([]string{"claude"}, true),
			wantReason:   "mismatched discovery policy",
		},
		{
			name:         "dangerous skip differs in health",
			recordPolicy: policy,
			healthPolicy: NewLaunchPolicy([]string{"codex"}, false),
			wantReason:   "mismatched discovery policy",
		},
		{
			name:         "unverifiable policy in existing discovery",
			recordPolicy: LaunchPolicy{},
			healthPolicy: policy,
			wantReason:   "mismatched discovery policy",
		},
		{
			name:         "unverifiable policy in health",
			recordPolicy: policy,
			healthPolicy: LaunchPolicy{},
			wantReason:   "mismatched discovery policy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := PublishDiscovery(runtimeDir, DiscoveryRecord{
				SchemaVersion: 1,
				APIVersion:    APIVersion,
				BaseURL:       "http://127.0.0.1:4567",
				Runtime:       identity,
				LaunchPolicy:  tt.recordPolicy,
				StartMode:     "server",
			}); err != nil {
				t.Fatalf("PublishDiscovery() error = %v", err)
			}
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(http.StatusOK, HealthResponse{
					APIVersion:   APIVersion,
					Status:       "ok",
					Runtime:      identity,
					LaunchPolicy: tt.healthPolicy,
				})
			})}

			decision, err := PrepareDiscovery(context.Background(), runtimeDir, identity, policy, client)
			if err != nil {
				t.Fatalf("PrepareDiscovery() error = %v", err)
			}
			if decision.AlreadyRunning || !decision.Replace {
				t.Fatalf("PrepareDiscovery() = %+v; want replacement", decision)
			}
			if !strings.Contains(decision.Reason, tt.wantReason) {
				t.Fatalf("PrepareDiscovery() reason = %q; want %q", decision.Reason, tt.wantReason)
			}
		})
	}
}

func TestPrepareDiscoveryReplacesHealthyWrongRuntime(t *testing.T) {
	t.Parallel()
	runtimeDir := t.TempDir()
	identity := RuntimeIdentity{
		RuntimeDir: runtimeDir,
		StateDir:   filepath.Join(runtimeDir, "features"),
		Config:     filepath.Join(runtimeDir, "config.yaml"),
	}
	policy := NewLaunchPolicy(nil, false)
	rec := DiscoveryRecord{
		SchemaVersion: 1,
		APIVersion:    APIVersion,
		BaseURL:       "http://127.0.0.1:4567",
		Runtime:       identity,
		LaunchPolicy:  policy,
		StartMode:     "server",
	}
	if err := PublishDiscovery(runtimeDir, rec); err != nil {
		t.Fatalf("PublishDiscovery() error = %v", err)
	}
	otherRuntime := identity
	otherRuntime.StateDir = filepath.Join(runtimeDir, "other-features")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, HealthResponse{
			APIVersion:   APIVersion,
			Status:       "ok",
			Runtime:      otherRuntime,
			LaunchPolicy: policy,
		})
	})}

	decision, err := PrepareDiscovery(context.Background(), runtimeDir, identity, policy, client)
	if err != nil {
		t.Fatalf("PrepareDiscovery() error = %v", err)
	}
	if decision.AlreadyRunning || !decision.Replace {
		t.Fatalf("decision = %+v; want replacement for wrong health runtime", decision)
	}
}

func TestPrepareDiscoveryReplacesStaleAndMismatchedRecords(t *testing.T) {
	t.Parallel()
	runtimeDir := t.TempDir()
	identity := RuntimeIdentity{
		RuntimeDir: runtimeDir,
		StateDir:   filepath.Join(runtimeDir, "features"),
		Config:     filepath.Join(runtimeDir, "config.yaml"),
	}
	policy := NewLaunchPolicy(nil, false)
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}

	if err := PublishDiscovery(runtimeDir, DiscoveryRecord{
		SchemaVersion: 1,
		APIVersion:    APIVersion,
		BaseURL:       "http://127.0.0.1:4567",
		Runtime:       identity,
		LaunchPolicy:  policy,
		StartMode:     "server",
	}); err != nil {
		t.Fatalf("PublishDiscovery(stale) error = %v", err)
	}
	decision, err := PrepareDiscovery(context.Background(), runtimeDir, identity, policy, client)
	if err != nil {
		t.Fatalf("PrepareDiscovery(stale) error = %v", err)
	}
	if decision.AlreadyRunning || !decision.Replace {
		t.Fatalf("stale decision = %+v; want replacement", decision)
	}

	otherRuntime := identity
	otherRuntime.StateDir = filepath.Join(runtimeDir, "other-features")
	if err := PublishDiscovery(runtimeDir, DiscoveryRecord{
		SchemaVersion: 1,
		APIVersion:    APIVersion,
		BaseURL:       "http://127.0.0.1:4567",
		Runtime:       otherRuntime,
		LaunchPolicy:  policy,
		StartMode:     "server",
	}); err != nil {
		t.Fatalf("PublishDiscovery(mismatch) error = %v", err)
	}
	decision, err = PrepareDiscovery(context.Background(), runtimeDir, identity, policy, client)
	if err != nil {
		t.Fatalf("PrepareDiscovery(mismatch) error = %v", err)
	}
	if decision.AlreadyRunning || !decision.Replace {
		t.Fatalf("mismatch decision = %+v; want replacement", decision)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
