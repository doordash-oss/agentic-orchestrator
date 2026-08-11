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

// testDiscoveryBaseURL is a fake loopback base_url used across discovery
// record fixtures in this file.
const testDiscoveryBaseURL = "http://127.0.0.1:4567"

// startModeServer is the DiscoveryRecord.StartMode value used across
// discovery record fixtures in this file.
const startModeServer = "server"

// newDiscoveryRecord builds a DiscoveryRecord fixture with the fields common
// to nearly every discovery test: schema/API version, the shared loopback
// base_url, and startModeServer. Callers can override BaseURL or set any of
// the remaining fields (AuthToken, PID, StartedAt, Owner, ...) directly on
// the returned value.
func newDiscoveryRecord(runtime RuntimeIdentity, policy LaunchPolicy) DiscoveryRecord {
	return DiscoveryRecord{
		SchemaVersion: 1,
		APIVersion:    APIVersion,
		BaseURL:       testDiscoveryBaseURL,
		Runtime:       runtime,
		LaunchPolicy:  policy,
		StartMode:     startModeServer,
	}
}

func TestStartServerBindsLoopbackAndServesHealth(t *testing.T) {
	t.Parallel()
	runtimeDir := t.TempDir()
	authToken, err := EnsureAuthToken(runtimeDir)
	if err != nil {
		t.Fatalf("EnsureAuthToken() error = %v", err)
	}
	identity := RuntimeIdentity{
		RuntimeDir: runtimeDir,
		StateDir:   filepath.Join(runtimeDir, "features"),
		Config:     filepath.Join(runtimeDir, "config.yaml"),
	}
	policy := NewLaunchPolicy([]string{providerCodex}, true)
	owner := instancelock.Owner{
		PID:       os.Getpid(),
		StartedAt: time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC),
		StateDir:  identity.StateDir,
		Config:    identity.Config,
	}
	srv, err := Start(context.Background(), Options{
		Runtime:      identity,
		LaunchPolicy: policy,
		StartMode:    startModeServer,
		Owner:        owner,
		AuthToken:    authToken,
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

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, baseURL+"/api/v1/health", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+authToken)
	resp, err := http.DefaultClient.Do(req)
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
	wantOwner := OwnerFromInstanceOwner(owner)
	if body.Owner != wantOwner {
		t.Fatalf("health owner = %+v; want %+v", body.Owner, wantOwner)
	}
	if srv.EventEpoch() == "" {
		t.Fatal("EventEpoch() is empty; discovery cannot publish stream epoch")
	}
	if srv.srv.WriteTimeout != 0 {
		t.Fatalf("server WriteTimeout = %s, want 0 so SSE streams are not killed by the shared server timeout", srv.srv.WriteTimeout)
	}
}

func TestStartRejectsMissingAuthToken(t *testing.T) {
	t.Parallel()

	srv, err := Start(context.Background(), Options{})
	if err == nil {
		_ = srv.Close(context.Background())
		t.Fatal("Start() error = nil, want missing auth token error")
	}
	if !strings.Contains(err.Error(), "auth token") {
		t.Fatalf("Start() error = %v, want auth token error", err)
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
	rec := newDiscoveryRecord(RuntimeIdentity{
		RuntimeDir: runtimeDir,
		StateDir:   filepath.Join(runtimeDir, "features"),
		Config:     filepath.Join(runtimeDir, "config.yaml"),
	}, LaunchPolicy{})
	rec.PID = os.Getpid()
	rec.StartedAt = time.Now().UTC()
	rec.PublishedAt = time.Now().UTC()
	rec.Owner = OwnerFromInstanceOwner(owner)

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
	rec := newDiscoveryRecord(identity, policy)
	rec.BaseURL = "http://192.0.2.20:4567"
	if err := PublishDiscovery(runtimeDir, rec); err != nil {
		t.Fatalf("PublishDiscovery() error = %v", err)
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("PrepareDiscovery probed a non-loopback discovery URL")
		return nil, errors.New("unreachable")
	})}

	decision, err := PrepareDiscovery(context.Background(), runtimeDir, identity, policy, client, false)
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

// TestPrepareDiscoveryNetworkBindAcceptsAdvertisedBaseURL pins the
// policy-aware base-URL gate: a network-bound server accepts a pre-existing
// discovery record whose base_url is its advertised (non-loopback) address
// and classifies a healthy match as already running, while the loopback
// policy keeps rejecting it.
func TestPrepareDiscoveryNetworkBindAcceptsAdvertisedBaseURL(t *testing.T) {
	t.Parallel()
	runtimeDir := t.TempDir()
	identity := RuntimeIdentity{
		RuntimeDir: runtimeDir,
		StateDir:   filepath.Join(runtimeDir, "features"),
		Config:     filepath.Join(runtimeDir, "config.yaml"),
	}
	policy := NewLaunchPolicy(nil, false)
	rec := newDiscoveryRecord(identity, policy)
	rec.BaseURL = "http://10.9.8.7:4567"
	rec.AuthToken = testAuthToken
	if err := PublishDiscovery(runtimeDir, rec); err != nil {
		t.Fatalf("PublishDiscovery() error = %v", err)
	}
	probed := false
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		probed = true
		return jsonResponse(HealthResponse{
			APIVersion:   APIVersion,
			Status:       "ok",
			Runtime:      identity,
			LaunchPolicy: policy,
		})
	})}

	decision, err := PrepareDiscovery(context.Background(), runtimeDir, identity, policy, client, true)
	if err != nil {
		t.Fatalf("PrepareDiscovery(network) error = %v", err)
	}
	if !decision.AlreadyRunning || decision.Replace {
		t.Fatalf("network decision = %+v; want already running", decision)
	}
	if !probed {
		t.Fatal("network-bind PrepareDiscovery skipped the health probe on its advertised URL")
	}

	// The same record stays rejected under the loopback policy.
	decision, err = PrepareDiscovery(context.Background(), runtimeDir, identity, policy, client, false)
	if err != nil {
		t.Fatalf("PrepareDiscovery(loopback) error = %v", err)
	}
	if !decision.Replace || !strings.Contains(decision.Reason, "non-loopback") {
		t.Fatalf("loopback decision = %+v; want non-loopback replacement", decision)
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
	if err := PublishDiscovery(runtimeDir, newDiscoveryRecord(identity, policy)); err != nil {
		t.Fatalf("PublishDiscovery() error = %v", err)
	}
	if err := os.Chmod(DiscoveryPath(runtimeDir), 0o644); err != nil {
		t.Fatalf("Chmod(discovery) error = %v", err)
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("PrepareDiscovery probed an unsafe discovery file")
		return nil, errors.New("unreachable")
	})}

	decision, err := PrepareDiscovery(context.Background(), runtimeDir, identity, policy, client, false)
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
	rec := newDiscoveryRecord(identity, policy)
	rec.AuthToken = testAuthToken
	if err := PublishDiscovery(runtimeDir, rec); err != nil {
		t.Fatalf("PublishDiscovery() error = %v", err)
	}
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("discovery health Authorization = %q, want bearer token", got)
		}
		return jsonResponse(HealthResponse{
			APIVersion:   APIVersion,
			Status:       "ok",
			Runtime:      identity,
			LaunchPolicy: policy,
		})
	})}

	decision, err := PrepareDiscovery(context.Background(), runtimeDir, identity, policy, client, false)
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
	policy := NewLaunchPolicy([]string{providerCodex}, false)
	recordStartedAt := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	rec := newDiscoveryRecord(identity, policy)
	rec.PID = 1111
	rec.StartedAt = recordStartedAt
	if err := PublishDiscovery(runtimeDir, rec); err != nil {
		t.Fatalf("PublishDiscovery() error = %v", err)
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(HealthResponse{
			APIVersion:   APIVersion,
			Status:       "ok",
			Runtime:      identity,
			LaunchPolicy: policy,
			StartedAt:    recordStartedAt.Add(time.Minute),
		})
	})}

	decision, err := PrepareDiscovery(context.Background(), runtimeDir, identity, policy, client, false)
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
	policy := NewLaunchPolicy([]string{providerCodex}, false)
	startedAt := time.Date(2026, 6, 14, 8, 0, 0, 0, time.UTC)
	recordOwner := Owner{
		PID:       1111,
		PGID:      2222,
		StartedAt: startedAt,
		Version:   "record-owner",
	}
	healthOwner := recordOwner
	healthOwner.PID = 3333
	healthOwner.Version = "health-owner"
	rec := newDiscoveryRecord(identity, policy)
	rec.PID = recordOwner.PID
	rec.PGID = recordOwner.PGID
	rec.StartedAt = startedAt
	rec.Owner = recordOwner
	if err := PublishDiscovery(runtimeDir, rec); err != nil {
		t.Fatalf("PublishDiscovery() error = %v", err)
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(struct {
			APIVersion   string          `json:"api_version"`
			Status       string          `json:"status"`
			Runtime      RuntimeIdentity `json:"runtime"`
			LaunchPolicy LaunchPolicy    `json:"launch_policy"`
			StartedAt    time.Time       `json:"started_at"`
			Owner        Owner           `json:"owner"`
		}{
			APIVersion:   APIVersion,
			Status:       "ok",
			Runtime:      identity,
			LaunchPolicy: policy,
			StartedAt:    startedAt,
			Owner:        healthOwner,
		})
	})}

	decision, err := PrepareDiscovery(context.Background(), runtimeDir, identity, policy, client, false)
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
	policy := NewLaunchPolicy([]string{providerCodex}, true)

	tests := []struct {
		name         string
		recordPolicy LaunchPolicy
		healthPolicy LaunchPolicy
		wantReason   string
	}{
		{
			name:         "provider restriction differs in discovery",
			recordPolicy: NewLaunchPolicy([]string{providerClaude}, true),
			healthPolicy: policy,
			wantReason:   reasonMismatchedDiscoveryPolicy,
		},
		{
			name:         "dangerous skip differs in discovery",
			recordPolicy: NewLaunchPolicy([]string{providerCodex}, false),
			healthPolicy: policy,
			wantReason:   reasonMismatchedDiscoveryPolicy,
		},
		{
			name:         "provider restriction differs in health",
			recordPolicy: policy,
			healthPolicy: NewLaunchPolicy([]string{providerClaude}, true),
			wantReason:   reasonMismatchedDiscoveryPolicy,
		},
		{
			name:         "dangerous skip differs in health",
			recordPolicy: policy,
			healthPolicy: NewLaunchPolicy([]string{providerCodex}, false),
			wantReason:   reasonMismatchedDiscoveryPolicy,
		},
		{
			name:         "unverifiable policy in existing discovery",
			recordPolicy: LaunchPolicy{},
			healthPolicy: policy,
			wantReason:   reasonMismatchedDiscoveryPolicy,
		},
		{
			name:         "unverifiable policy in health",
			recordPolicy: policy,
			healthPolicy: LaunchPolicy{},
			wantReason:   reasonMismatchedDiscoveryPolicy,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := PublishDiscovery(runtimeDir, newDiscoveryRecord(identity, tt.recordPolicy)); err != nil {
				t.Fatalf("PublishDiscovery() error = %v", err)
			}
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(HealthResponse{
					APIVersion:   APIVersion,
					Status:       "ok",
					Runtime:      identity,
					LaunchPolicy: tt.healthPolicy,
				})
			})}

			decision, err := PrepareDiscovery(context.Background(), runtimeDir, identity, policy, client, false)
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
	rec := newDiscoveryRecord(identity, policy)
	if err := PublishDiscovery(runtimeDir, rec); err != nil {
		t.Fatalf("PublishDiscovery() error = %v", err)
	}
	otherRuntime := identity
	otherRuntime.StateDir = filepath.Join(runtimeDir, "other-features")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(HealthResponse{
			APIVersion:   APIVersion,
			Status:       "ok",
			Runtime:      otherRuntime,
			LaunchPolicy: policy,
		})
	})}

	decision, err := PrepareDiscovery(context.Background(), runtimeDir, identity, policy, client, false)
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
	if err := PublishDiscovery(runtimeDir, newDiscoveryRecord(identity, policy)); err != nil {
		t.Fatalf("PublishDiscovery() error = %v", err)
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connect: connection refused")
	})}

	decision, err := PrepareDiscovery(context.Background(), runtimeDir, identity, policy, client, false)
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

	if err := PublishDiscovery(runtimeDir, newDiscoveryRecord(identity, policy)); err != nil {
		t.Fatalf("PublishDiscovery(stale) error = %v", err)
	}
	decision, err := PrepareDiscovery(context.Background(), runtimeDir, identity, policy, client, false)
	if err != nil {
		t.Fatalf("PrepareDiscovery(stale) error = %v", err)
	}
	if decision.AlreadyRunning || !decision.Replace {
		t.Fatalf("stale decision = %+v; want replacement", decision)
	}

	otherRuntime := identity
	otherRuntime.StateDir = filepath.Join(runtimeDir, "other-features")
	if err := PublishDiscovery(runtimeDir, newDiscoveryRecord(otherRuntime, policy)); err != nil {
		t.Fatalf("PublishDiscovery(mismatch) error = %v", err)
	}
	decision, err = PrepareDiscovery(context.Background(), runtimeDir, identity, policy, client, false)
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
