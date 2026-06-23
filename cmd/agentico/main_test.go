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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	opencode "github.com/doordash-oss/agentic-orchestrator/internal/llm/opencode"
)

func failingLauncher(t *testing.T) tuiLauncher {
	t.Helper()
	return func(string, string, bool, []string) {
		t.Fatal("TUI launcher invoked unexpectedly")
	}
}

func writeReviewFeedback(t *testing.T, path, findings, verdict string) {
	t.Helper()
	body := strings.Join([]string{
		"## Findings",
		findings,
		"",
		"## Suggestions",
		"- (none)",
		"",
		"## Verdict",
		verdict,
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write review feedback: %v", err)
	}
}

// stubProvider is a minimal LLMProvider for testing checkRequiredProviders.
type stubProvider struct {
	name              string
	models            []string
	hasCLI            bool
	installHint       string
	readiness         llm.ProviderReadiness
	hasReadiness      bool
	version           string // VersionInfo() output; "" defaults to "1.0.0"
	minVersion        [3]int // MinVersion(); zero value {0,0,0} accepts any version
	enforceMinVersion bool   // EnforcesMinVersion(); opts into the startup version gate
}

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) MatchesModel(m string) bool {
	return slices.Contains(s.models, m)
}
func (s *stubProvider) DetectCLI() bool           { return s.hasCLI }
func (s *stubProvider) AvailableModels() []string { return s.models }
func (s *stubProvider) BuildCommand(_ llm.CommandBuildOpts) ([]string, []string, error) {
	return nil, nil, nil
}
func (s *stubProvider) NewProtocol(_ llm.ProtocolOpts) llm.Protocol { return nil }
func (s *stubProvider) InstallHint() string                         { return s.installHint }
func (s *stubProvider) VersionInfo() (string, error) {
	if s.version != "" {
		return s.version, nil
	}
	return "1.0.0", nil
}
func (s *stubProvider) MinVersion() [3]int         { return s.minVersion }
func (s *stubProvider) EnforcesMinVersion() bool   { return s.enforceMinVersion }
func (s *stubProvider) EnvVarsToExclude() []string { return nil }
func (s *stubProvider) CheckReadiness(context.Context) llm.ProviderReadiness {
	if s.hasReadiness {
		return s.readiness
	}
	return llm.ProviderReadiness{Ready: true}
}

type stubCatalogDiscoveryProvider struct {
	stubProvider
	discovered  []llm.ModelInfo
	discoverErr error
	catalog     []llm.ModelInfo
	versionInfo string
	versionErr  error
	discoveries int
	discoverFn  func(context.Context) ([]llm.ModelInfo, error)
}

func (s *stubCatalogDiscoveryProvider) DiscoverModelCatalog(ctx context.Context) ([]llm.ModelInfo, error) {
	s.discoveries++
	if s.discoverFn != nil {
		return s.discoverFn(ctx)
	}
	return s.discovered, s.discoverErr
}

func (s *stubCatalogDiscoveryProvider) SetModelCatalog(models []llm.ModelInfo) {
	s.catalog = models
}

func (s *stubCatalogDiscoveryProvider) VersionInfo() (string, error) {
	if s.versionErr != nil {
		return "", s.versionErr
	}
	if s.versionInfo != "" {
		return s.versionInfo, nil
	}
	return s.stubProvider.VersionInfo()
}

func TestDiscoverProviderCatalogsSetsCatalogAndWarnsOnFailure(t *testing.T) {
	success := &stubCatalogDiscoveryProvider{
		stubProvider: stubProvider{name: "success", hasCLI: true},
		discovered: []llm.ModelInfo{
			{ID: "live-model", DisplayName: "Live Model", ContextWindow: 123_000, Category: "capable"},
		},
	}
	failure := &stubCatalogDiscoveryProvider{
		stubProvider: stubProvider{name: "failure", hasCLI: true},
		discoverErr:  errors.New("catalog unavailable"),
	}
	plain := &stubProvider{name: "plain", hasCLI: true}

	warnings := discoverProviderCatalogs(context.Background(), []llm.LLMProvider{success, failure, plain}, t.TempDir(), nil)
	if len(success.catalog) != 1 || success.catalog[0].ID != "live-model" {
		t.Fatalf("success catalog = %+v, want live-model", success.catalog)
	}
	if len(failure.catalog) != 0 {
		t.Fatalf("failure catalog = %+v, want unchanged empty catalog", failure.catalog)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one warning", warnings)
	}
	if !strings.Contains(warnings[0], "failure") || !strings.Contains(warnings[0], "catalog unavailable") {
		t.Fatalf("warning = %q, want provider name and error", warnings[0])
	}
}

func TestDiscoverProviderCatalogsReportsDiscoveredModels(t *testing.T) {
	p := &stubCatalogDiscoveryProvider{
		stubProvider: stubProvider{name: "success", hasCLI: true},
		discovered: []llm.ModelInfo{
			{ID: "live-model", DisplayName: "Live Model", ContextWindow: 123_000, Category: "capable"},
		},
	}
	var reported []string

	warnings := discoverProviderCatalogs(context.Background(), []llm.LLMProvider{p}, "", func(provider string, model llm.ModelInfo) {
		reported = append(reported, provider+":"+model.DisplayName)
	})
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if !slices.Equal(reported, []string{"success:Live Model"}) {
		t.Fatalf("reported = %v, want discovered model", reported)
	}
}

func TestDiscoverProviderCatalogsRunsProvidersConcurrently(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	closeRelease := func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}
	defer closeRelease()

	provider := func(name string) *stubCatalogDiscoveryProvider {
		return &stubCatalogDiscoveryProvider{
			stubProvider: stubProvider{name: name, hasCLI: true},
			discoverFn: func(ctx context.Context) ([]llm.ModelInfo, error) {
				started <- name
				select {
				case <-release:
					return []llm.ModelInfo{{ID: name + "-model", DisplayName: name + " Model"}}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			},
		}
	}
	first := provider("first")
	second := provider("second")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan []string, 1)
	go func() {
		done <- discoverProviderCatalogs(ctx, []llm.LLMProvider{first, second}, "", nil)
	}()

	seen := make(map[string]bool)
	for len(seen) < 2 {
		select {
		case name := <-started:
			seen[name] = true
		case <-time.After(250 * time.Millisecond):
			closeRelease()
			select {
			case <-done:
			case <-time.After(time.Second):
			}
			t.Fatalf("providers did not start concurrently; started = %v", seen)
		}
	}

	closeRelease()
	select {
	case warnings := <-done:
		if len(warnings) != 0 {
			t.Fatalf("warnings = %v, want none", warnings)
		}
	case <-time.After(time.Second):
		t.Fatal("discoverProviderCatalogs did not return after releasing providers")
	}
}

func TestDiscoverProviderCatalogsLoadsVersionedCache(t *testing.T) {
	cacheRoot := t.TempDir()
	cachedModels := []llm.ModelInfo{
		{ID: "cached-model", DisplayName: "Cached Model", ContextWindow: 456_000, Category: "balanced"},
	}
	if err := saveProviderCatalogCache(cacheRoot, "cached", "2.0.0", cachedModels); err != nil {
		t.Fatalf("saveProviderCatalogCache() error: %v", err)
	}
	p := &stubCatalogDiscoveryProvider{
		stubProvider: stubProvider{name: "cached", hasCLI: true},
		// Name-prefixed CLI output (the shape Claude/Codex report) must be
		// normalized to the parsed semver "2.0.0" before it is used as the cache
		// key, so this matches the pre-seeded cache entry above.
		versionInfo: "cached 2.0.0",
		discoverErr: errors.New("discovery should not run"),
	}

	warnings := discoverProviderCatalogs(context.Background(), []llm.LLMProvider{p}, cacheRoot, nil)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if p.discoveries != 0 {
		t.Fatalf("discovery calls = %d, want 0", p.discoveries)
	}
	if len(p.catalog) != 1 || p.catalog[0].ID != "cached-model" {
		t.Fatalf("catalog = %+v, want cached-model", p.catalog)
	}
}

func TestDiscoverProviderCatalogsWritesVersionedCacheOnMiss(t *testing.T) {
	cacheRoot := t.TempDir()
	discovered := []llm.ModelInfo{
		{ID: "fresh-model", DisplayName: "Fresh Model", ContextWindow: 789_000, Category: "capable"},
	}
	p := &stubCatalogDiscoveryProvider{
		stubProvider: stubProvider{name: "fresh", hasCLI: true},
		// Prefixed "name + v" CLI output (the shape Codex reports) must be
		// normalized to the parsed semver "3.0.0" before it becomes the cache key.
		versionInfo: "OpenAI Codex v3.0.0",
		discovered:  discovered,
	}

	warnings := discoverProviderCatalogs(context.Background(), []llm.LLMProvider{p}, cacheRoot, nil)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if p.discoveries != 1 {
		t.Fatalf("discovery calls = %d, want 1", p.discoveries)
	}
	cached, err := loadProviderCatalogCache(cacheRoot, "fresh", "3.0.0")
	if err != nil {
		t.Fatalf("loadProviderCatalogCache() error: %v", err)
	}
	if len(cached) != 1 || cached[0].ID != "fresh-model" {
		t.Fatalf("cached models = %+v, want fresh-model", cached)
	}
}

func TestDiscoverProviderCatalogsRefreshesCorruptVersionedCache(t *testing.T) {
	cacheRoot := t.TempDir()
	path := providerCatalogCachePath(cacheRoot, "corrupt", "4.0.0")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}
	p := &stubCatalogDiscoveryProvider{
		stubProvider: stubProvider{name: "corrupt", hasCLI: true},
		// Name-prefixed CLI output normalizes to the parsed semver "4.0.0", which
		// keys the corrupt entry written above and the refreshed entry written back.
		versionInfo: "corrupt 4.0.0",
		discovered: []llm.ModelInfo{
			{ID: "refreshed-model", DisplayName: "Refreshed Model", ContextWindow: 321_000, Category: "capable"},
		},
	}

	warnings := discoverProviderCatalogs(context.Background(), []llm.LLMProvider{p}, cacheRoot, nil)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "ignoring cached corrupt model catalog") {
		t.Fatalf("warnings = %v, want corrupt cache warning", warnings)
	}
	if p.discoveries != 1 {
		t.Fatalf("discovery calls = %d, want 1", p.discoveries)
	}
	cached, err := loadProviderCatalogCache(cacheRoot, "corrupt", "4.0.0")
	if err != nil {
		t.Fatalf("loadProviderCatalogCache() error: %v", err)
	}
	if len(cached) != 1 || cached[0].ID != "refreshed-model" {
		t.Fatalf("cached models = %+v, want refreshed-model", cached)
	}
}

// hostileVersion is a provider version string carrying both credential-like
// content and a terminal-control (OSC) escape — the shape a malformed or
// adversarial `opencode --version` could take. The cache layer must never let it
// reach a cache key, filename, persisted metadata, or any diagnostic string.
const hostileVersion = "1.17.9 sk-ant-deadbeefdeadbeefdeadbeef0123\x1b]0;pwn\x07"

// hostileSecret is the credential-like substring of hostileVersion; assertions
// check it never surfaces in an error, a file path, or a cache artifact.
const hostileSecret = "sk-ant-deadbeefdeadbeefdeadbeef0123"

// unparseableHostileVersion carries credential-like and terminal-control content
// but contains NO recognizable semver, so the shared version parser cannot
// extract a cache key from it. The startup path must reject it, warn generically,
// and never echo it — distinct from hostileVersion, which begins with a valid
// semver the parser can salvage.
const unparseableHostileVersion = hostileSecret + "\x1b]0;pwn\x07"

// assertNoCacheLeak walks cacheRoot and fails if any directory entry name or
// file body contains the credential-like secret or the OSC terminal-control
// bytes from a rejected version.
func assertNoCacheLeak(t *testing.T, cacheRoot string) {
	t.Helper()
	err := filepath.WalkDir(cacheRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.Contains(path, hostileSecret) || strings.ContainsRune(path, '\x1b') {
			t.Fatalf("cache path leaked credential/control content: %q", path)
		}
		if d.IsDir() {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(body), hostileSecret) || strings.ContainsRune(string(body), '\x1b') {
			t.Fatalf("cache file %q leaked credential/control content", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk cache root: %v", err)
	}
}

func assertDiagnosticClean(t *testing.T, where string, msg string) {
	t.Helper()
	if strings.Contains(msg, hostileSecret) {
		t.Fatalf("%s leaked credential-like content: %q", where, msg)
	}
	if strings.ContainsRune(msg, '\x1b') {
		t.Fatalf("%s leaked terminal-control bytes: %q", where, msg)
	}
}

// TestCacheableVersion classifies version strings: a bounded, version-shaped
// token is cacheable; anything carrying whitespace, control bytes, trailing
// credential-like content, an unexpected shape, or excessive length is not.
func TestCacheableVersion(t *testing.T) {
	cacheable := []string{"1.17.9", "0.116.0", "2.1.81", "v1.2.3", "1.2.3-rc.1", "1.2.3+build.5"}
	for _, v := range cacheable {
		if !cacheableVersion(v) {
			t.Errorf("cacheableVersion(%q) = false, want true", v)
		}
	}
	uncacheable := []string{
		"",
		"   ",
		"provider 2.0.0",                    // name prefix + space
		hostileVersion,                      // credential + control content
		hostileSecret,                       // bare credential-shaped token, no semver
		"1.17.9 extra",                      // trailing junk
		"1.17.9\x1b]0;x\x07",                // trailing terminal control
		"1.17.9\n2.0.0",                     // embedded newline
		"sk-ant-" + strings.Repeat("a", 80), // overlong
	}
	for _, v := range uncacheable {
		if cacheableVersion(v) {
			t.Errorf("cacheableVersion(%q) = true, want false", v)
		}
	}
}

// TestSaveProviderCatalogCache_RejectsUncacheableVersionWithoutLeak is the
// cache-WRITE diagnostic guarantee the phase requires: a hostile version is
// refused, the returned error never echoes the credential-like or terminal-
// control content, and no cache artifact carrying that content is written.
func TestSaveProviderCatalogCache_RejectsUncacheableVersionWithoutLeak(t *testing.T) {
	cacheRoot := t.TempDir()
	models := []llm.ModelInfo{{ID: "m", DisplayName: "M", ContextWindow: 1000, Category: "balanced"}}

	err := saveProviderCatalogCache(cacheRoot, "opencode", hostileVersion, models)
	if err == nil {
		t.Fatal("saveProviderCatalogCache() = nil, want refusal for uncacheable version")
	}
	assertDiagnosticClean(t, "saveProviderCatalogCache error", err.Error())
	assertNoCacheLeak(t, cacheRoot)
}

// TestLoadProviderCatalogCache_RejectsUncacheableVersionWithoutLeak is the
// cache-READ diagnostic guarantee: a hostile version is refused before a cache
// path is built from it, the returned error never echoes the credential-like or
// terminal-control content, and no path carrying that content is created.
func TestLoadProviderCatalogCache_RejectsUncacheableVersionWithoutLeak(t *testing.T) {
	cacheRoot := t.TempDir()

	_, err := loadProviderCatalogCache(cacheRoot, "opencode", hostileVersion)
	if err == nil {
		t.Fatal("loadProviderCatalogCache() error = nil, want refusal for uncacheable version")
	}
	assertDiagnosticClean(t, "loadProviderCatalogCache error", err.Error())
	assertNoCacheLeak(t, cacheRoot)
}

// TestDiscoverProviderCatalogs_RejectsUnparseableVersionWithoutLeak proves the
// startup orchestration rejects a provider version it cannot parse into a semver
// before using it as a cache key or in a diagnostic: discovery still runs and the
// catalog is populated, exactly one ordered warning is emitted, that warning never
// echoes the credential-like or terminal-control content, and nothing is cached.
func TestDiscoverProviderCatalogs_RejectsUnparseableVersionWithoutLeak(t *testing.T) {
	cacheRoot := t.TempDir()
	p := &stubCatalogDiscoveryProvider{
		stubProvider: stubProvider{name: "opencode", hasCLI: true},
		versionInfo:  unparseableHostileVersion,
		discovered: []llm.ModelInfo{
			{ID: "live-model", DisplayName: "Live Model", ContextWindow: 200_000, Category: "capable"},
		},
	}

	warnings := discoverProviderCatalogs(context.Background(), []llm.LLMProvider{p}, cacheRoot, nil)
	if p.discoveries != 1 {
		t.Fatalf("discovery calls = %d, want 1 (discovery still runs without caching)", p.discoveries)
	}
	if len(p.catalog) != 1 || p.catalog[0].ID != "live-model" {
		t.Fatalf("catalog = %+v, want discovered live-model", p.catalog)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one unrecognized-version warning", warnings)
	}
	assertDiagnosticClean(t, "startup warning", warnings[0])
	assertNoCacheLeak(t, cacheRoot)
}

// TestDiscoverProviderCatalogs_PrefixedVersionNormalizesToCacheKey proves the
// startup path normalizes human-readable, name- or "v"-prefixed CLI version
// output (the shape Claude and Codex report from VersionInfo) through the shared
// semver parser before using it as a cache key: a cache miss writes the catalog
// under the parsed token and a second startup at the same version installs that
// cache without re-running discovery. Without normalization these valid versions
// are rejected and re-discovered (uncached) on every startup.
func TestDiscoverProviderCatalogs_PrefixedVersionNormalizesToCacheKey(t *testing.T) {
	cases := []struct {
		name        string
		versionInfo string
		wantKey     string
	}{
		{"claude name prefix", "claude 2.1.112", "2.1.112"},
		{"codex name and v prefix", "OpenAI Codex v0.120.0", "0.120.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cacheRoot := t.TempDir()
			discovered := []llm.ModelInfo{
				{ID: "live-model", DisplayName: "Live Model", ContextWindow: 200_000, Category: "capable"},
			}

			// First startup: cache miss → discovery runs and persists under the
			// parsed token, not the raw prefixed output.
			p1 := &stubCatalogDiscoveryProvider{
				stubProvider: stubProvider{name: "prov", hasCLI: true},
				versionInfo:  tc.versionInfo,
				discovered:   discovered,
			}
			if warnings := discoverProviderCatalogs(context.Background(), []llm.LLMProvider{p1}, cacheRoot, nil); len(warnings) != 0 {
				t.Fatalf("first-startup warnings = %v, want none", warnings)
			}
			if p1.discoveries != 1 {
				t.Fatalf("discovery calls = %d, want 1 on cache miss", p1.discoveries)
			}
			cached, err := loadProviderCatalogCache(cacheRoot, "prov", tc.wantKey)
			if err != nil {
				t.Fatalf("loadProviderCatalogCache(%q) error: %v, want catalog cached under parsed token", tc.wantKey, err)
			}
			if len(cached) != 1 || cached[0].ID != "live-model" {
				t.Fatalf("cached = %+v, want live-model", cached)
			}

			// Second startup at the same version: cache hit → discovery does NOT run.
			p2 := &stubCatalogDiscoveryProvider{
				stubProvider: stubProvider{name: "prov", hasCLI: true},
				versionInfo:  tc.versionInfo,
				discoverErr:  errors.New("discovery should not run on cache hit"),
			}
			if warnings := discoverProviderCatalogs(context.Background(), []llm.LLMProvider{p2}, cacheRoot, nil); len(warnings) != 0 {
				t.Fatalf("second-startup warnings = %v, want none", warnings)
			}
			if p2.discoveries != 0 {
				t.Fatalf("second-startup discovery calls = %d, want 0 (version-keyed cache hit)", p2.discoveries)
			}
			if len(p2.catalog) != 1 || p2.catalog[0].ID != "live-model" {
				t.Fatalf("second-startup catalog = %+v, want cached live-model", p2.catalog)
			}
		})
	}
}

// TestDiscoverProviderCatalogs_NormalizesHostileVersionLineWithoutLeak proves the
// startup path salvages a hostile version LINE that still begins with a valid
// semver: the shared parser extracts the clean token "1.17.9" and drops the
// trailing credential-like and terminal-control content, so the catalog is cached
// under the clean key with no leak and no warning.
func TestDiscoverProviderCatalogs_NormalizesHostileVersionLineWithoutLeak(t *testing.T) {
	cacheRoot := t.TempDir()
	p := &stubCatalogDiscoveryProvider{
		stubProvider: stubProvider{name: "opencode", hasCLI: true},
		versionInfo:  hostileVersion, // "1.17.9 " + credential + OSC escape
		discovered: []llm.ModelInfo{
			{ID: "live-model", DisplayName: "Live Model", ContextWindow: 200_000, Category: "capable"},
		},
	}

	warnings := discoverProviderCatalogs(context.Background(), []llm.LLMProvider{p}, cacheRoot, nil)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none (hostile line normalizes to a clean semver)", warnings)
	}
	if p.discoveries != 1 {
		t.Fatalf("discovery calls = %d, want 1 on cache miss", p.discoveries)
	}
	// Cached under the parsed token "1.17.9", never the raw hostile line.
	cached, err := loadProviderCatalogCache(cacheRoot, "opencode", "1.17.9")
	if err != nil {
		t.Fatalf("loadProviderCatalogCache(\"1.17.9\") error: %v, want catalog cached under parsed token", err)
	}
	if len(cached) != 1 || cached[0].ID != "live-model" {
		t.Fatalf("cached = %+v, want live-model", cached)
	}
	assertNoCacheLeak(t, cacheRoot)
}

// TestDiscoverProviderCatalogs_OpenCodeDiscoversAndCaches proves the real
// OpenCode provider participates in the shared startup discovery + version-keyed
// cache path: a cache miss runs CLI discovery and persists the result, and a
// second startup at the same CLI version installs the cached catalog without
// invoking the CLI again. The runner is faked so the test needs no real OpenCode
// binary, credentials, or network.
func TestDiscoverProviderCatalogs_OpenCodeDiscoversAndCaches(t *testing.T) {
	cacheRoot := t.TempDir()
	var verboseCalls int
	verbose := "anthropic/claude-sonnet-4-5\n" +
		`{ "id": "claude-sonnet-4-5", "providerID": "anthropic", "name": "Claude Sonnet 4.5", "status": "active", "limit": { "context": 200000 } }` + "\n"
	runner := func(_ context.Context, name string, args []string, _ []string) ([]byte, error) {
		if name != "opencode" {
			t.Fatalf("ran %q, want opencode", name)
		}
		switch strings.Join(args, " ") {
		case "--version":
			return []byte("1.17.9\n"), nil
		case "models --verbose --refresh":
			verboseCalls++
			return []byte(verbose), nil
		default:
			return nil, fmt.Errorf("unexpected argv %q", strings.Join(args, " "))
		}
	}

	const wantID = "anthropic/claude-sonnet-4-5[200K]"

	// First startup: cache miss → discovery runs, catalog populated and cached.
	p1 := opencode.NewWithRunner(runner)
	if warnings := discoverProviderCatalogs(context.Background(), []llm.LLMProvider{p1}, cacheRoot, nil); len(warnings) != 0 {
		t.Fatalf("first-startup warnings = %v, want none", warnings)
	}
	if verboseCalls != 1 {
		t.Fatalf("discovery ran %d times, want 1 on cache miss", verboseCalls)
	}
	if cat := p1.ModelCatalog(); len(cat) != 1 || cat[0].ID != wantID {
		t.Fatalf("first-startup catalog = %+v, want discovered %q", cat, wantID)
	}

	// Second startup at the same version: cache hit → discovery does NOT run.
	p2 := opencode.NewWithRunner(runner)
	if warnings := discoverProviderCatalogs(context.Background(), []llm.LLMProvider{p2}, cacheRoot, nil); len(warnings) != 0 {
		t.Fatalf("second-startup warnings = %v, want none", warnings)
	}
	if verboseCalls != 1 {
		t.Fatalf("discovery ran %d times total, want 1 (version-keyed cache hit skips CLI)", verboseCalls)
	}
	if cat := p2.ModelCatalog(); len(cat) != 1 || cat[0].ID != wantID {
		t.Fatalf("second-startup catalog = %+v, want cached %q", cat, wantID)
	}
}

// TestDiscoverProviderCatalogs_OpenCodeFallsBackOnDiscoveryFailure proves the
// plan's degrade-to-fallback contract end-to-end: when a ready, version-eligible
// OpenCode's live discovery fails, the shared startup path emits an ordered
// "using built-in fallback" warning AND the provider still surfaces a non-empty
// curated catalog through CatalogProvider/AvailableModels — so a discovery
// failure never leaves setup/config consumers with an empty OpenCode model list.
// The runner is faked so the test needs no real OpenCode binary, credentials, or
// network.
func TestDiscoverProviderCatalogs_OpenCodeFallsBackOnDiscoveryFailure(t *testing.T) {
	runner := func(_ context.Context, name string, args []string, _ []string) ([]byte, error) {
		if name != "opencode" {
			t.Fatalf("ran %q, want opencode", name)
		}
		switch strings.Join(args, " ") {
		case "--version":
			return []byte("1.17.9\n"), nil
		case "models --verbose --refresh", "models --verbose", "models":
			return nil, errors.New("discovery boom")
		default:
			return nil, fmt.Errorf("unexpected argv %q", strings.Join(args, " "))
		}
	}

	p := opencode.NewWithRunner(runner)
	warnings := discoverProviderCatalogs(context.Background(), []llm.LLMProvider{p}, t.TempDir(), nil)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "opencode") || !strings.Contains(warnings[0], "built-in fallback") {
		t.Fatalf("warnings = %v, want one opencode built-in-fallback warning", warnings)
	}

	// Despite the discovery failure, the provider degrades to its curated
	// fallback so the registry's model lists and routing never see it as empty.
	if cat := p.ModelCatalog(); len(cat) == 0 {
		t.Fatal("ModelCatalog() = empty after discovery failure, want the built-in fallback")
	}
	if models := p.AvailableModels(); len(models) == 0 {
		t.Fatal("AvailableModels() = empty after discovery failure, want the built-in fallback")
	}
}

func TestRunArgsLaunchesTUIByDefault(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var launched bool
	wantParent := pickRuntimeParent(os.Stat)
	code := runArgs(nil, &stdout, &stderr, func(configPath, stateDir string, dangerouslySkipPerms bool, enabledProviders []string) {
		launched = true
		if configPath != filepath.Join(wantParent, defaultConfigBasename) {
			t.Errorf("configPath = %q, want default", configPath)
		}
		if stateDir != filepath.Join(wantParent, defaultStateBasename) {
			t.Errorf("stateDir = %q, want default", stateDir)
		}
		if dangerouslySkipPerms {
			t.Error("dangerouslySkipPerms = true, want false")
		}
		if enabledProviders != nil {
			t.Errorf("enabledProviders = %v, want nil", enabledProviders)
		}
	}, failingUpdater(t))
	if code != 0 {
		t.Errorf("runArgs() code = %d, want 0", code)
	}
	if !launched {
		t.Fatal("launcher was not called")
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout = %q stderr = %q, want both empty", stdout.String(), stderr.String())
	}
}

func TestRunArgsPassesRetainedLaunchFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var gotConfig, gotState string
	var gotDangerouslySkipPerms bool
	var gotProviders []string
	code := runArgs(
		[]string{"--config", "/tmp/agentic-config.yaml", "--state-dir", "/tmp/agentic-features", "--providers", "codex, claude", "--dangerously-skip-permissions"},
		&stdout,
		&stderr,
		func(configPath, stateDir string, dangerouslySkipPerms bool, enabledProviders []string) {
			gotConfig = configPath
			gotState = stateDir
			gotDangerouslySkipPerms = dangerouslySkipPerms
			gotProviders = enabledProviders
		},
		failingUpdater(t),
	)
	if code != 0 {
		t.Errorf("runArgs() code = %d, want 0", code)
	}
	if gotConfig != "/tmp/agentic-config.yaml" {
		t.Errorf("configPath = %q, want custom path", gotConfig)
	}
	if gotState != "/tmp/agentic-features" {
		t.Errorf("stateDir = %q, want custom path", gotState)
	}
	if !gotDangerouslySkipPerms {
		t.Error("dangerouslySkipPerms = false, want true")
	}
	if !slices.Equal(gotProviders, []string{"codex", " claude"}) {
		t.Errorf("enabledProviders = %v, want raw CSV split", gotProviders)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout = %q stderr = %q, want both empty", stdout.String(), stderr.String())
	}
}

func TestRunArgsValidateArtifactsCatchesMalformedYAML(t *testing.T) {
	iterDir := t.TempDir()
	writeReviewFeedback(t, filepath.Join(iterDir, "review-feedback.md"), "- **High**: needs work", "CHANGES_REQUESTED")
	if err := os.WriteFile(filepath.Join(iterDir, "verification-report.yaml"), []byte(strings.Join([]string{
		"version: 2",
		"additional_checks:",
		"  - name: Source comparison spot-check",
		"    command: manual: compare translated README against English source",
		"    mode: manual",
		"    status: failed",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write malformed report: %v", err)
	}

	var stdout, stderr bytes.Buffer
	var launchedTUI, updateCalled bool
	code := runArgs(
		[]string{"validate-artifacts", "--phase", "review", "--role", string(agent.RoleFinalReviewer), "--dir", iterDir},
		&stdout,
		&stderr,
		func(string, string, bool, []string) {
			launchedTUI = true
		},
		func(bool, io.Writer, io.Writer) int {
			updateCalled = true
			return 0
		},
	)
	if code != 1 {
		t.Fatalf("runArgs() code = %d, want validation failure 1", code)
	}
	if launchedTUI || updateCalled {
		t.Fatalf("validate-artifacts launched unrelated path: tui=%v update=%v", launchedTUI, updateCalled)
	}
	if got := stderr.String(); !strings.Contains(got, "verification-report.yaml") || !strings.Contains(got, "unparseable") {
		t.Fatalf("stderr = %q, want verification-report.yaml unparseable violation", got)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty on validation failure", stdout.String())
	}
}

func TestRunArgsValidateArtifactsAcceptsValidArtifacts(t *testing.T) {
	iterDir := t.TempDir()
	writeReviewFeedback(t, filepath.Join(iterDir, "review-feedback.md"), "- (none)", "APPROVED")
	if err := os.WriteFile(filepath.Join(iterDir, "verification-report.yaml"), []byte("version: 1\nrequired_checks: []\n"), 0o644); err != nil {
		t.Fatalf("write verification report: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := runArgs(
		[]string{"validate-artifacts", "--phase", "review", "--role", string(agent.RoleFinalReviewer), "--dir", iterDir},
		&stdout,
		&stderr,
		failingLauncher(t),
		failingUpdater(t),
	)
	if code != 0 {
		t.Fatalf("runArgs() code = %d; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "artifacts OK") {
		t.Fatalf("stdout = %q, want OK confirmation", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestParseLaunchArgsRejectsRemovedSurface(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "run alias", args: []string{"run"}, wantErr: "unknown command: run"},
		{name: "feature list", args: []string{"feature", "list"}, wantErr: "unknown command: feature"},
		{name: "feature create", args: []string{"feature", "create", "--name", "x"}, wantErr: "unknown command: feature"},
		{name: "refresh models", args: []string{"--refresh-models"}, wantErr: "unknown flag: --refresh-models"},
		{name: "feature create flag at top level", args: []string{"--name", "x"}, wantErr: "unknown flag: --name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseLaunchArgs(tt.args)
			if err == nil {
				t.Fatalf("parseLaunchArgs(%v) error = nil, want %q", tt.args, tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Fatalf("parseLaunchArgs(%v) error = %q, want %q", tt.args, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestProviderFxModules_NilReturnsDefaultSet(t *testing.T) {
	modules := providerFxModules(nil, false)
	if len(modules) != 2 {
		t.Errorf("expected 2 modules for nil input, got %d", len(modules))
	}
}

func TestProviderFxModules_SingleProvider(t *testing.T) {
	for _, name := range []string{"claude", "codex"} {
		t.Run(name, func(t *testing.T) {
			modules := providerFxModules([]string{name}, false)
			if len(modules) != 1 {
				t.Errorf("expected 1 module for %q, got %d", name, len(modules))
			}
		})
	}
}

func TestProviderFxModules_BothProviders(t *testing.T) {
	modules := providerFxModules([]string{"claude", "codex"}, false)
	if len(modules) != 2 {
		t.Errorf("expected 2 modules, got %d", len(modules))
	}
}

func TestProviderFxModules_TrimsWhitespace(t *testing.T) {
	modules := providerFxModules([]string{" claude ", " codex "}, false)
	if len(modules) != 2 {
		t.Errorf("expected 2 modules after trimming, got %d", len(modules))
	}
}

func TestProviderFxModules_UnknownSkipped(t *testing.T) {
	modules := providerFxModules([]string{"claude", "bogus"}, false)
	if len(modules) != 1 {
		t.Errorf("expected 1 module (bogus skipped), got %d", len(modules))
	}
}

func TestRemapUnresolvableModels(t *testing.T) {
	t.Run("replaces_unresolvable_with_fallback", func(t *testing.T) {
		r := llm.NewRegistry()
		r.Register(&stubProvider{name: "codex", models: []string{"gpt-5.4"}, hasCLI: true})

		cfg := config.NewDefault() // has claude models: opus, sonnet, etc.
		remapUnresolvableModels(cfg, r)

		// All fields should now be "gpt-5.4" since codex is the only provider
		m := cfg.Defaults.Models
		for _, field := range []string{m.Research, m.Planning, m.Implementation, m.Review, m.Utilities, m.KBBuild} {
			if field != "gpt-5.4" {
				t.Errorf("expected gpt-5.4, got %q", field)
			}
		}
	})

	t.Run("keeps_resolvable_models", func(t *testing.T) {
		r := llm.NewRegistry()
		r.Register(&stubProvider{name: "claude", models: []string{"opus[1M]", "sonnet[200K]"}, hasCLI: true})

		cfg := config.NewDefault()
		remapUnresolvableModels(cfg, r)

		// canonical Claude defaults should stay; gpt-5.4[272K] (review) should become opus[1M].
		if cfg.Defaults.Models.Research != "sonnet[200K]" {
			t.Errorf("research should stay sonnet[200K], got %q", cfg.Defaults.Models.Research)
		}
		if cfg.Defaults.Models.Utilities != "sonnet[200K]" {
			t.Errorf("utilities should stay sonnet[200K], got %q", cfg.Defaults.Models.Utilities)
		}
	})
}

func TestCheckRequiredProviders_AllDetected(t *testing.T) {
	r := llm.NewRegistry()
	r.Register(&stubProvider{name: "claude", models: []string{"opus"}, hasCLI: true, installHint: "install claude"})
	r.Register(&stubProvider{name: "codex", models: []string{"gpt-5.4"}, hasCLI: true, installHint: "install codex"})

	detected, warnings, notices, filtered, err := checkRequiredProviders(context.Background(), r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(notices) != 0 {
		t.Fatalf("expected no startup notices, got %v", notices)
	}
	if filtered {
		t.Fatal("filtered = true, want false")
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got %v", warnings)
	}
	if len(detected) != 2 {
		t.Errorf("expected 2 detected providers, got %d", len(detected))
	}
}

func TestCheckRequiredProviders_OneDetected(t *testing.T) {
	r := llm.NewRegistry()
	r.Register(&stubProvider{name: "claude", models: []string{"opus"}, hasCLI: true, installHint: "install claude"})
	r.Register(&stubProvider{name: "codex", models: []string{"gpt-5.4"}, hasCLI: false, installHint: "install codex"})

	detected, warnings, notices, filtered, err := checkRequiredProviders(context.Background(), r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !filtered {
		t.Fatal("filtered = false, want true when a registered provider is unavailable")
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no immediate warnings, got %d: %v", len(warnings), warnings)
	}
	if len(notices) != 1 {
		t.Fatalf("expected 1 startup notice, got %d: %v", len(notices), notices)
	}
	if !strings.Contains(notices[0], "Provider codex CLI was not found") || !strings.Contains(notices[0], "Starting with claude only") {
		t.Fatalf("startup notice = %q, want missing codex/start claude", notices[0])
	}
	if len(detected) != 1 {
		t.Errorf("expected 1 detected provider, got %d", len(detected))
	}
}

func TestPickRuntimeParent(t *testing.T) {
	newParent := config.ExpandHome(defaultRuntimeParent)
	legacyParent := config.ExpandHome(legacyRuntimeParent)

	makeStat := func(present map[string]bool) func(string) (os.FileInfo, error) {
		return func(p string) (os.FileInfo, error) {
			if present[p] {
				return fakeDirInfo{}, nil
			}
			return nil, &fs.PathError{Op: "stat", Path: p, Err: fs.ErrNotExist}
		}
	}

	tests := []struct {
		name    string
		present map[string]bool
		want    string
	}{
		{
			name:    "fresh install — neither parent exists, prefer new namespace",
			present: map[string]bool{},
			want:    newParent,
		},
		{
			name:    "new namespace exists, legacy absent",
			present: map[string]bool{newParent: true},
			want:    newParent,
		},
		{
			name:    "legacy parent exists, new absent — recover in place",
			present: map[string]bool{legacyParent: true},
			want:    legacyParent,
		},
		{
			name:    "both exist — new namespace wins",
			present: map[string]bool{newParent: true, legacyParent: true},
			want:    newParent,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pickRuntimeParent(makeStat(tt.present))
			if got != tt.want {
				t.Errorf("pickRuntimeParent = %q, want %q", got, tt.want)
			}
		})
	}
}

type fakeDirInfo struct{}

func (fakeDirInfo) Name() string       { return "" }
func (fakeDirInfo) Size() int64        { return 0 }
func (fakeDirInfo) Mode() fs.FileMode  { return fs.ModeDir | 0o755 }
func (fakeDirInfo) ModTime() time.Time { return time.Time{} }
func (fakeDirInfo) IsDir() bool        { return true }
func (fakeDirInfo) Sys() any           { return nil }

func TestPrintUsageAdvertisesRenamedDefaults(t *testing.T) {
	var b bytes.Buffer
	printUsage(&b)
	out := b.String()
	for _, want := range []string{
		"Agentic Orchestrator",
		"agentico [flags]",
		"~/.agentic-orchestrator/config.yaml",
		"~/.agentic-orchestrator/features",
		"~/.agentic-workflow/", // legacy-recovery callout retained
	} {
		if !strings.Contains(out, want) {
			t.Errorf("printUsage missing %q\nfull:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Agentic Workflow Orchestrator") {
		t.Errorf("printUsage still advertises legacy product title:\n%s", out)
	}
	if strings.Contains(out, "Commands:") {
		t.Errorf("printUsage must not introduce a Commands section:\n%s", out)
	}
}

func TestCheckRequiredProviders_NoneDetected(t *testing.T) {
	r := llm.NewRegistry()
	r.Register(&stubProvider{name: "claude", models: []string{"opus"}, hasCLI: false, installHint: "install claude"})
	r.Register(&stubProvider{name: "codex", models: []string{"gpt-5.4"}, hasCLI: false, installHint: "install codex"})

	_, _, _, _, err := checkRequiredProviders(context.Background(), r)
	if err == nil {
		t.Fatal("expected error when no providers detected")
	}
}

func TestCheckRequiredProviders_UnreadyProviderWarnsAndFiltersRegistry(t *testing.T) {
	r := llm.NewRegistry()
	r.Register(&stubProvider{name: "claude", models: []string{"opus"}, hasCLI: true, installHint: "install claude", hasReadiness: true, readiness: llm.ProviderReadiness{Ready: true}})
	r.Register(&stubProvider{name: "codex", models: []string{"gpt-5.4"}, hasCLI: true, installHint: "install codex", readiness: llm.ProviderReadiness{
		Ready:  false,
		Detail: "not logged in",
		Remedy: "run 'codex login'",
	}, hasReadiness: true})

	detected, warnings, notices, filtered, err := checkRequiredProviders(context.Background(), r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !filtered {
		t.Fatal("filtered = false, want true")
	}
	if len(detected) != 1 || detected[0].Name() != "claude" {
		t.Fatalf("detected = %v, want only claude", providerNames(detected))
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want no immediate warnings", warnings)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "Provider codex is not configured") || !strings.Contains(notices[0], "codex login") || !strings.Contains(notices[0], "Starting with claude only") {
		t.Fatalf("notices = %v, want codex startup notice", notices)
	}
	if _, _, err := r.ResolveModel("codex:gpt-5.4"); err == nil {
		t.Fatal("ResolveModel(codex:gpt-5.4) succeeded after codex was filtered")
	}
	if got := r.AvailableModels(); !slices.Equal(got, []string{"opus"}) {
		t.Fatalf("AvailableModels() = %v, want [opus]", got)
	}
}

func TestCheckRequiredProviders_NoneReadyReportsProviderSpecificFix(t *testing.T) {
	r := llm.NewRegistry()
	r.Register(&stubProvider{name: "claude", models: []string{"opus"}, hasCLI: true, installHint: "install claude", readiness: llm.ProviderReadiness{
		Ready:  false,
		Detail: "not authenticated",
		Remedy: "run 'claude auth login'",
	}, hasReadiness: true})
	r.Register(&stubProvider{name: "codex", models: []string{"gpt-5.4"}, hasCLI: false, installHint: "install codex"})

	_, _, _, filtered, err := checkRequiredProviders(context.Background(), r)
	if err == nil {
		t.Fatal("expected error when no providers are ready")
	}
	if !filtered {
		t.Fatal("filtered = false, want true")
	}
	msg := err.Error()
	for _, want := range []string{
		"No ready AI coding assistant providers detected",
		"claude",
		"not authenticated",
		"claude auth login",
		"Missing provider CLIs",
		"codex",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error missing %q:\n%s", want, msg)
		}
	}
}

func providerNames(providers []llm.LLMProvider) []string {
	names := make([]string, 0, len(providers))
	for _, p := range providers {
		names = append(names, p.Name())
	}
	return names
}

func TestShowProviderStartupNoticesWritesBeforeTUIWithoutForcedDelayInTests(t *testing.T) {
	var out bytes.Buffer
	showProviderStartupNotices(&out, []string{"Provider codex is not configured: not logged in. Starting with claude only."}, 0)
	if got := out.String(); got != "Provider codex is not configured: not logged in. Starting with claude only.\n" {
		t.Fatalf("startup notice output = %q", got)
	}
}

// TestOverrideColorProfileForcesANSI256InAppleTerminal guards the workaround
// for macOS Terminal.app: when TERM_PROGRAM=Apple_Terminal we force the
// bubbletea renderer to ANSI256, overriding the user's COLORTERM env var
// (commonly set to "truecolor" by Neovim/tmux tutorials). Terminal.app
// silently drops 24-bit escapes, so without the override the TUI renders as
// default foreground.
func TestOverrideColorProfileForcesANSI256InAppleTerminal(t *testing.T) {
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")
	t.Setenv("COLORTERM", "truecolor") // user lie that we're overriding

	profile, ok := overrideColorProfile()
	if !ok {
		t.Fatal("expected override to be applied for Apple_Terminal")
	}
	if profile != colorprofile.ANSI256 {
		t.Errorf("profile = %v, want ANSI256", profile)
	}
}

// TestOverrideColorProfileLeavesOtherTerminalsAlone ensures iTerm2 / WezTerm /
// Ghostty / etc. continue receiving full 24-bit color via bubbletea's normal
// auto-detection.
func TestOverrideColorProfileLeavesOtherTerminalsAlone(t *testing.T) {
	for _, term := range []string{"iTerm.app", "WezTerm", "ghostty", "vscode", ""} {
		t.Run(term, func(t *testing.T) {
			t.Setenv("TERM_PROGRAM", term)
			if _, ok := overrideColorProfile(); ok {
				t.Errorf("TERM_PROGRAM=%q should not trigger an override", term)
			}
		})
	}
}
