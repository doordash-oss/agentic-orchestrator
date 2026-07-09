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
	"io"
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
	owner := instancelock.Owner{
		PID:       os.Getpid(),
		StartedAt: time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC),
		StateDir:  identity.StateDir,
		Config:    identity.Config,
	}
	srv, err := Start(context.Background(), Options{
		Runtime:      identity,
		LaunchPolicy: policy,
		StartMode:    "server",
		Owner:        owner,
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
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read health body: %v", err)
	}
	assertTopLevelJSONOwnerOmitsPrivatePaths(t, data, "health owner")
	var body HealthResponse
	if err := json.Unmarshal(data, &body); err != nil {
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
	if !body.StartedAt.Equal(srv.StartedAt()) {
		t.Fatalf("health started_at = %s; want server started_at %s", body.StartedAt, srv.StartedAt())
	}
	wantOwner := OwnerDTOFromInstanceOwner(owner)
	if body.Owner != wantOwner {
		t.Fatalf("health owner = %+v; want %+v", body.Owner, wantOwner)
	}
	if srv.EventEpoch() == "" {
		t.Fatal("EventEpoch() is empty; discovery cannot publish stream epoch")
	}
}

func TestPublishDiscoveryWritesOwnerOnlyAtomically(t *testing.T) {
	t.Parallel()
	runtimeDir := t.TempDir()
	owner := instancelock.Owner{
		PID:       os.Getpid(),
		StartedAt: time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC),
		StateDir:  filepath.Join(runtimeDir, "features"),
		Config:    filepath.Join(runtimeDir, "config.yaml"),
		Version:   "test-version",
	}
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
		Owner:       OwnerDTOFromInstanceOwner(owner),
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
	data, err := os.ReadFile(DiscoveryPath(runtimeDir))
	if err != nil {
		t.Fatalf("ReadFile(discovery) error = %v", err)
	}
	assertTopLevelJSONOwnerOmitsPrivatePaths(t, data, "discovery owner")

	got, err := ReadDiscovery(runtimeDir)
	if err != nil {
		t.Fatalf("ReadDiscovery() error = %v", err)
	}
	if got.BaseURL != rec.BaseURL || got.Runtime.StateDir != rec.Runtime.StateDir {
		t.Fatalf("ReadDiscovery() = %+v; want published record", got)
	}
}

func TestEnsureAuthTokenCreatesOwnerOnlyStableToken(t *testing.T) {
	t.Parallel()

	runtimeDir := t.TempDir()
	first, err := EnsureAuthToken(runtimeDir)
	if err != nil {
		t.Fatalf("EnsureAuthToken(first) error = %v", err)
	}
	if first == "" {
		t.Fatal("EnsureAuthToken(first) returned empty token")
	}
	info, err := os.Stat(AuthTokenPath(runtimeDir))
	if err != nil {
		t.Fatalf("stat token: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("token mode = %#o; want 0600", got)
	}
	second, err := EnsureAuthToken(runtimeDir)
	if err != nil {
		t.Fatalf("EnsureAuthToken(second) error = %v", err)
	}
	if second != first {
		t.Fatalf("EnsureAuthToken second = %q, want stable first token", second)
	}
}

func assertTopLevelJSONOwnerOmitsPrivatePaths(t testing.TB, data []byte, label string) {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("unmarshal %s JSON %s: %v", label, data, err)
	}
	owner, ok := body["owner"].(map[string]any)
	if !ok {
		t.Fatalf("%s = %v; want object", label, body["owner"])
	}
	assertOwnerMapOmitsPrivatePaths(t, owner, label)
}

func assertOwnerMapOmitsPrivatePaths(t testing.TB, owner map[string]any, label string) {
	t.Helper()
	for _, key := range []string{"state_dir", "config_path"} {
		if _, ok := owner[key]; ok {
			t.Fatalf("%s contains %q: %+v", label, key, owner)
		}
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
		AuthToken:     "test-token",
		Runtime:       identity,
		LaunchPolicy:  policy,
		StartMode:     "server",
	}
	if err := PublishDiscovery(runtimeDir, rec); err != nil {
		t.Fatalf("PublishDiscovery() error = %v", err)
	}
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("discovery health Authorization = %q, want bearer token", got)
		}
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

func TestPrepareDiscoveryRequiresHealthStartedAtToMatchRecord(t *testing.T) {
	t.Parallel()
	runtimeDir := t.TempDir()
	identity := RuntimeIdentity{
		RuntimeDir: runtimeDir,
		StateDir:   filepath.Join(runtimeDir, "features"),
		Config:     filepath.Join(runtimeDir, "config.yaml"),
	}
	policy := NewLaunchPolicy([]string{"codex"}, false)
	recordStartedAt := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	rec := DiscoveryRecord{
		SchemaVersion: 1,
		APIVersion:    APIVersion,
		BaseURL:       "http://127.0.0.1:4567",
		Runtime:       identity,
		LaunchPolicy:  policy,
		StartMode:     "server",
		PID:           1111,
		StartedAt:     recordStartedAt,
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
			StartedAt:    recordStartedAt.Add(time.Minute),
		})
	})}

	decision, err := PrepareDiscovery(context.Background(), runtimeDir, identity, policy, client)
	if err != nil {
		t.Fatalf("PrepareDiscovery() error = %v", err)
	}
	if decision.AlreadyRunning || !decision.Replace {
		t.Fatalf("decision = %+v; want replacement for health/record mismatch", decision)
	}
	if !strings.Contains(decision.Reason, "mismatched discovery health") {
		t.Fatalf("decision reason = %q; want mismatched discovery health", decision.Reason)
	}
}

func TestPrepareDiscoveryRequiresHealthOwnerToMatchRecord(t *testing.T) {
	t.Parallel()
	runtimeDir := t.TempDir()
	identity := RuntimeIdentity{
		RuntimeDir: runtimeDir,
		StateDir:   filepath.Join(runtimeDir, "features"),
		Config:     filepath.Join(runtimeDir, "config.yaml"),
	}
	policy := NewLaunchPolicy([]string{"codex"}, false)
	startedAt := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	recordOwner := OwnerDTO{
		PID:       1111,
		PGID:      2222,
		StartedAt: startedAt,
		Version:   "record-owner",
	}
	healthOwner := recordOwner
	healthOwner.PID = 3333
	healthOwner.Version = "health-owner"
	rec := DiscoveryRecord{
		SchemaVersion: 1,
		APIVersion:    APIVersion,
		BaseURL:       "http://127.0.0.1:4567",
		Runtime:       identity,
		LaunchPolicy:  policy,
		StartMode:     "server",
		PID:           recordOwner.PID,
		PGID:          recordOwner.PGID,
		StartedAt:     startedAt,
		Owner:         recordOwner,
	}
	if err := PublishDiscovery(runtimeDir, rec); err != nil {
		t.Fatalf("PublishDiscovery() error = %v", err)
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, struct {
			APIVersion   string          `json:"api_version"`
			Status       string          `json:"status"`
			Runtime      RuntimeIdentity `json:"runtime"`
			LaunchPolicy LaunchPolicy    `json:"launch_policy"`
			StartedAt    time.Time       `json:"started_at"`
			Owner        OwnerDTO        `json:"owner"`
		}{
			APIVersion:   APIVersion,
			Status:       "ok",
			Runtime:      identity,
			LaunchPolicy: policy,
			StartedAt:    startedAt,
			Owner:        healthOwner,
		})
	})}

	decision, err := PrepareDiscovery(context.Background(), runtimeDir, identity, policy, client)
	if err != nil {
		t.Fatalf("PrepareDiscovery() error = %v", err)
	}
	if decision.AlreadyRunning || !decision.Replace {
		t.Fatalf("decision = %+v; want replacement for owner metadata mismatch", decision)
	}
	if !strings.Contains(decision.Reason, "mismatched discovery owner") {
		t.Fatalf("decision reason = %q; want mismatched discovery owner", decision.Reason)
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

func TestPrepareDiscoveryReportsDeadPortDiagnostic(t *testing.T) {
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
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connect: connection refused")
	})}

	decision, err := PrepareDiscovery(context.Background(), runtimeDir, identity, policy, client)
	if err != nil {
		t.Fatalf("PrepareDiscovery() error = %v", err)
	}
	if decision.AlreadyRunning || !decision.Replace {
		t.Fatalf("decision = %+v; want replacement for dead discovery port", decision)
	}
	if !strings.Contains(decision.Reason, "dead discovery port") {
		t.Fatalf("decision reason = %q; want dead discovery port", decision.Reason)
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
