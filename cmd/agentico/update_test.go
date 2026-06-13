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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// failingUpdater returns an updater seam that fails the test if invoked. Use
// it in launch-path tests that must never reach the update seam.
func failingUpdater(t *testing.T) updater {
	t.Helper()
	return func(bool, io.Writer, io.Writer) int {
		t.Fatal("update seam invoked on a non-update launch path")
		return 1
	}
}

func failingServerLauncher(t *testing.T) serverLauncher {
	t.Helper()
	return func(string, string, bool, []string) int {
		t.Fatal("server seam invoked on a non-server launch path")
		return 1
	}
}

// --- Task 1: parser surface for `update` ------------------------------------

func TestParseLaunchArgsUpdateSurface(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantMode  launchMode
		wantCheck bool
		wantErr   string // empty means no error expected
	}{
		{name: "bare update", args: []string{"update"}, wantMode: launchModeUpdate, wantCheck: false},
		{name: "update --check", args: []string{"update", "--check"}, wantMode: launchModeUpdate, wantCheck: true},
		{name: "update -n", args: []string{"update", "-n"}, wantMode: launchModeUpdate, wantCheck: true},
		{name: "update --bogus", args: []string{"update", "--bogus"}, wantErr: "unknown flag: --bogus"},
		{name: "update with retained launch flag --config", args: []string{"update", "--config", "/tmp/x"}, wantErr: "unknown flag: --config"},
		{name: "update with --state-dir", args: []string{"update", "--state-dir", "/tmp/x"}, wantErr: "unknown flag: --state-dir"},
		{name: "update with --providers", args: []string{"update", "--providers", "claude"}, wantErr: "unknown flag: --providers"},
		{name: "update with --dangerously-skip-permissions", args: []string{"update", "--dangerously-skip-permissions"}, wantErr: "unknown flag: --dangerously-skip-permissions"},
		{name: "update with extra positional", args: []string{"update", "extra"}, wantErr: "unexpected argument: extra"},
		{name: "update --check then extra positional", args: []string{"update", "--check", "extra"}, wantErr: "unexpected argument: extra"},
		{name: "update not first arg", args: []string{"--config", "/tmp/x", "update"}, wantErr: "unknown command: update"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := parseLaunchArgs(tt.args)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("parseLaunchArgs(%v) error = nil, want %q", tt.args, tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Fatalf("parseLaunchArgs(%v) error = %q, want %q", tt.args, err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseLaunchArgs(%v) unexpected error: %v", tt.args, err)
			}
			if opts.mode != tt.wantMode {
				t.Errorf("mode = %v, want %v", opts.mode, tt.wantMode)
			}
			if opts.updateCheck != tt.wantCheck {
				t.Errorf("updateCheck = %v, want %v", opts.updateCheck, tt.wantCheck)
			}
		})
	}
}

// --check / -n outside update context must still reject as an unknown flag.
func TestParseLaunchArgsCheckFlagOutsideUpdateRejects(t *testing.T) {
	for _, arg := range []string{"--check", "-n"} {
		t.Run(arg, func(t *testing.T) {
			_, err := parseLaunchArgs([]string{arg})
			if err == nil || err.Error() != "unknown flag: "+arg {
				t.Fatalf("parseLaunchArgs([%q]) error = %v, want unknown flag", arg, err)
			}
		})
	}
}

// --- Task 1: router dispatch through the injectable seam --------------------

func TestRunArgsDispatchesUpdateToSeam(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantCheck bool
	}{
		{name: "bare update", args: []string{"update"}, wantCheck: false},
		{name: "update --check", args: []string{"update", "--check"}, wantCheck: true},
		{name: "update -n", args: []string{"update", "-n"}, wantCheck: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			var gotCheck, updateCalled, launchCalled bool
			code := runArgs(tt.args, &stdout, &stderr,
				func(string, string, bool, []string, bool) { launchCalled = true },
				failingServerLauncher(t),
				func(checkOnly bool, _, _ io.Writer) int {
					updateCalled = true
					gotCheck = checkOnly
					return 7 // arbitrary known exit code the router must propagate verbatim
				},
			)
			if !updateCalled {
				t.Fatal("update seam was not invoked")
			}
			if launchCalled {
				t.Fatal("TUI launcher was invoked on the update path; it must not be")
			}
			if gotCheck != tt.wantCheck {
				t.Errorf("checkOnly = %v, want %v", gotCheck, tt.wantCheck)
			}
			if code != 7 {
				t.Errorf("runArgs returned %d, want the seam's exit code 7", code)
			}
		})
	}
}

func TestRunArgsDefaultStillLaunchesTUINotUpdate(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var launchCalled, updateCalled bool
	code := runArgs(nil, &stdout, &stderr,
		func(string, string, bool, []string, bool) { launchCalled = true },
		failingServerLauncher(t),
		func(bool, io.Writer, io.Writer) int { updateCalled = true; return 1 },
	)
	if !launchCalled {
		t.Fatal("default mode must launch the TUI")
	}
	if updateCalled {
		t.Fatal("default mode must not invoke the update seam")
	}
	if code != 0 {
		t.Errorf("code = %d, want 0", code)
	}
}

func TestGithubBaseURL(t *testing.T) {
	t.Run("defaults to public api", func(t *testing.T) {
		t.Setenv("GITHUB_API_URL", "")
		if got := githubBaseURL(); got != githubAPIBaseURL {
			t.Errorf("githubBaseURL() = %q, want %q", got, githubAPIBaseURL)
		}
	})
	t.Run("honors GITHUB_API_URL override", func(t *testing.T) {
		t.Setenv("GITHUB_API_URL", "https://ghe.example.com/api/v3")
		if got := githubBaseURL(); got != "https://ghe.example.com/api/v3" {
			t.Errorf("githubBaseURL() = %q, want override", got)
		}
	})
	t.Run("trims surrounding whitespace", func(t *testing.T) {
		t.Setenv("GITHUB_API_URL", "  https://ghe.example.com/api/v3  ")
		if got := githubBaseURL(); got != "https://ghe.example.com/api/v3" {
			t.Errorf("githubBaseURL() = %q, want trimmed override", got)
		}
	})
}

// --- Task 2: version normalization & slug derivation ------------------------

func TestNormalizeVersion(t *testing.T) {
	cases := map[string]string{
		"v1.2.3":   "1.2.3",
		" v1.2.3 ": "1.2.3",
		"1.2.3":    "1.2.3",
		"dev":      "dev",
		"  dev  ":  "dev",
		"v0.0.0":   "0.0.0",
	}
	for in, want := range cases {
		if got := normalizeVersion(in); got != want {
			t.Errorf("normalizeVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSameVersion(t *testing.T) {
	if !sameVersion("1.2.3", "v1.2.3") {
		t.Error("1.2.3 and v1.2.3 should be equal after normalization")
	}
	if sameVersion("dev", "v1.2.3") {
		t.Error("dev must never equal a release tag")
	}
	if sameVersion("1.2.3", "v1.2.4") {
		t.Error("different versions must not be equal")
	}
}

func TestSlugFromModulePath(t *testing.T) {
	tests := []struct {
		path    string
		want    string
		wantErr bool
	}{
		{path: "github.com/doordash-oss/agentic-orchestrator", want: "doordash-oss/agentic-orchestrator"},
		{path: "github.com/doordash-oss/agentic-orchestrator/cmd/agentico", want: "doordash-oss/agentic-orchestrator"},
		{path: "example.com/foo/bar", wantErr: true},
		{path: "github.com/onlyowner", wantErr: true},
		{path: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got, err := slugFromModulePath(tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("slugFromModulePath(%q) error = nil, want error", tt.path)
				}
				return
			}
			if err != nil {
				t.Fatalf("slugFromModulePath(%q) unexpected error: %v", tt.path, err)
			}
			if got != tt.want {
				t.Errorf("slugFromModulePath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// --- Task 2: real GitHub fetch logic against an in-process httptest server ---

// releasesServer returns an httptest server serving the GitHub releases list
// endpoint for the given slug with the supplied release payload.
func releasesServer(t *testing.T, wantSlug string, status int, releases []map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/repos/" + wantSlug + "/releases"
		if r.URL.Path != wantPath {
			t.Errorf("request path = %q, want %q", r.URL.Path, wantPath)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(releases)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGithubFetchLatestStableTag(t *testing.T) {
	const slug = "doordash-oss/agentic-orchestrator"

	t.Run("returns latest stable tag", func(t *testing.T) {
		srv := releasesServer(t, slug, http.StatusOK, []map[string]any{
			{"tag_name": "v2.0.0"},
			{"tag_name": "v1.0.0"},
		})
		fetch := githubFetchLatestStableTag(srv.Client(), srv.URL)
		got, err := fetch(context.Background(), slug)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "v2.0.0" {
			t.Errorf("tag = %q, want v2.0.0", got)
		}
	})

	t.Run("skips drafts and pre-releases", func(t *testing.T) {
		srv := releasesServer(t, slug, http.StatusOK, []map[string]any{
			{"tag_name": "v3.0.0-rc1", "prerelease": true},
			{"tag_name": "v2.5.0", "draft": true},
			{"tag_name": "v2.0.0"},
		})
		fetch := githubFetchLatestStableTag(srv.Client(), srv.URL)
		got, err := fetch(context.Background(), slug)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "v2.0.0" {
			t.Errorf("tag = %q, want v2.0.0 (drafts/pre-releases skipped)", got)
		}
	})

	t.Run("no stable release", func(t *testing.T) {
		srv := releasesServer(t, slug, http.StatusOK, []map[string]any{
			{"tag_name": "v3.0.0-rc1", "prerelease": true},
		})
		fetch := githubFetchLatestStableTag(srv.Client(), srv.URL)
		_, err := fetch(context.Background(), slug)
		if !errors.Is(err, errNoStableRelease) {
			t.Fatalf("error = %v, want errNoStableRelease", err)
		}
	})

	t.Run("empty release list", func(t *testing.T) {
		srv := releasesServer(t, slug, http.StatusOK, []map[string]any{})
		fetch := githubFetchLatestStableTag(srv.Client(), srv.URL)
		_, err := fetch(context.Background(), slug)
		if !errors.Is(err, errNoStableRelease) {
			t.Fatalf("error = %v, want errNoStableRelease", err)
		}
	})

	t.Run("non-200 response", func(t *testing.T) {
		srv := releasesServer(t, slug, http.StatusInternalServerError, nil)
		fetch := githubFetchLatestStableTag(srv.Client(), srv.URL)
		_, err := fetch(context.Background(), slug)
		if err == nil {
			t.Fatal("expected error on non-200 response")
		}
	})

	t.Run("network failure", func(t *testing.T) {
		// Point at a closed server to force a transport error.
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()
		fetch := githubFetchLatestStableTag(http.DefaultClient, url)
		_, err := fetch(context.Background(), slug)
		if err == nil {
			t.Fatal("expected error on network failure")
		}
	})
}

// --- Task 2: end-to-end --check / bare update behavior (hermetic) -----------

// fakeFetch returns a fetchLatest function that always yields tag/err.
func fakeFetch(tag string, err error) func(context.Context, string) (string, error) {
	return func(context.Context, string) (string, error) { return tag, err }
}

func okSlug() (string, error) { return "doordash-oss/agentic-orchestrator", nil }

// fixedMethod returns a detector that always reports m. The non-dev branches of
// runUpdateWith reach the network, so most tests pin a concrete non-dev method.
func fixedMethod(m installMethod) func() installMethod {
	return func() installMethod { return m }
}

// failingSlugFn / failingFetchFn fail the test if invoked. They guard the
// classify-first control flow: a dev build must never reach the slug resolver
// or the latest-release fetcher.
func failingSlugFn(t *testing.T) func() (string, error) {
	t.Helper()
	return func() (string, error) {
		t.Fatal("slug resolver called on a network-free (dev-build) path")
		return "", nil
	}
}

func failingFetchFn(t *testing.T) func(context.Context, string) (string, error) {
	t.Helper()
	return func(context.Context, string) (string, error) {
		t.Fatal("fetchLatest called on a network-free (dev-build) path")
		return "", nil
	}
}

func TestRunUpdateWith(t *testing.T) {
	t.Run("already up to date short-circuits, exits 0", func(t *testing.T) {
		for _, checkOnly := range []bool{true, false} {
			var stdout, stderr bytes.Buffer
			code := runUpdateWith(context.Background(), checkOnly, &stdout, &stderr, updateDeps{
				currentVersion: "2.0.0",
				method:         fixedMethod(installMethodTarball),
				slug:           okSlug,
				fetchLatest:    fakeFetch("v2.0.0", nil),
			})
			if code != 0 {
				t.Fatalf("checkOnly=%v code = %d, want 0", checkOnly, code)
			}
			out := stdout.String()
			if !strings.Contains(out, "up to date") {
				t.Errorf("checkOnly=%v stdout missing up-to-date message\nfull:\n%s", checkOnly, out)
			}
			if strings.Contains(out, "Would update") {
				t.Errorf("checkOnly=%v up-to-date path must not print a would-do action", checkOnly)
			}
		}
	})

	t.Run("dev version string with non-dev method still prints latest, check exits 0", func(t *testing.T) {
		// The injected method decides the branch; the version string only
		// decides up-to-date. A "dev" version on a go-install method must not
		// claim up to date and must still resolve the latest release.
		var stdout, stderr bytes.Buffer
		code := runUpdateWith(context.Background(), true, &stdout, &stderr, updateDeps{
			currentVersion: "dev",
			method:         fixedMethod(installMethodGoInstall),
			slug:           okSlug,
			fetchLatest:    fakeFetch("v2.0.0", nil),
		})
		if code != 0 {
			t.Fatalf("code = %d, want 0", code)
		}
		out := stdout.String()
		if strings.Contains(out, "up to date") {
			t.Errorf("dev must never claim up to date\nfull:\n%s", out)
		}
		if !strings.Contains(out, "2.0.0") {
			t.Errorf("dev --check must still print latest\nfull:\n%s", out)
		}
	})

	t.Run("fetch error exits 1 with stderr message", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runUpdateWith(context.Background(), true, &stdout, &stderr, updateDeps{
			currentVersion: "1.2.3",
			method:         fixedMethod(installMethodTarball),
			slug:           okSlug,
			fetchLatest:    fakeFetch("", errors.New("boom")),
		})
		if code != 1 {
			t.Fatalf("code = %d, want 1", code)
		}
		if stderr.Len() == 0 {
			t.Error("expected an error message on stderr")
		}
	})

	t.Run("no stable release exits 1 with clean message", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runUpdateWith(context.Background(), true, &stdout, &stderr, updateDeps{
			currentVersion: "1.2.3",
			method:         fixedMethod(installMethodTarball),
			slug:           okSlug,
			fetchLatest:    fakeFetch("", errNoStableRelease),
		})
		if code != 1 {
			t.Fatalf("code = %d, want 1", code)
		}
		if stderr.Len() == 0 {
			t.Error("expected a no-release message on stderr")
		}
	})

	t.Run("underivable slug exits 1", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		fetchCalled := false
		code := runUpdateWith(context.Background(), true, &stdout, &stderr, updateDeps{
			currentVersion: "1.2.3",
			method:         fixedMethod(installMethodTarball),
			slug:           func() (string, error) { return "", errors.New("no module path") },
			fetchLatest: func(context.Context, string) (string, error) {
				fetchCalled = true
				return "", nil
			},
		})
		if code != 1 {
			t.Fatalf("code = %d, want 1", code)
		}
		if fetchCalled {
			t.Error("fetch must not run when the slug cannot be derived")
		}
		if stderr.Len() == 0 {
			t.Error("expected a slug error on stderr")
		}
	})
}

// --- Task 2: dev-build refusal (network-free) -------------------------------

func TestRunUpdateWithDevBuildBareRefuses(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runUpdateWith(context.Background(), false, &stdout, &stderr, updateDeps{
		currentVersion: "dev",
		method:         fixedMethod(installMethodDevBuild),
		slug:           failingSlugFn(t),  // must not be reached
		fetchLatest:    failingFetchFn(t), // must not be reached
	})
	if code == 0 {
		t.Fatalf("bare dev-build update must exit non-zero, got %d", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("bare dev-build refusal must keep stdout empty, got %q", stdout.String())
	}
	errOut := stderr.String()
	for _, want := range []string{"built from source", "Update from source"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing guidance %q\nfull:\n%s", want, errOut)
		}
	}
}

func TestRunUpdateWithDevBuildCheckReports(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runUpdateWith(context.Background(), true, &stdout, &stderr, updateDeps{
		currentVersion: "dev",
		method:         fixedMethod(installMethodDevBuild),
		slug:           failingSlugFn(t),  // must not be reached
		fetchLatest:    failingFetchFn(t), // must not be reached
	})
	if code != 0 {
		t.Fatalf("dev-build --check must exit 0 (non-destructive report), got %d\nstderr: %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"Current version", "dev", installMethodDevBuild.label(), installMethodDevBuild.wouldDoAction()} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q\nfull:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Latest version") {
		t.Errorf("dev-build --check must not print a latest version (no network)\nfull:\n%s", out)
	}
	if stderr.Len() != 0 {
		t.Errorf("dev-build --check stderr = %q, want empty", stderr.String())
	}
}

// --- Task 3: real method + would-do-action reporting for non-dev branches ---

func TestRunUpdateWithCheckReportsMethodAndAction(t *testing.T) {
	tests := []struct {
		name       string
		method     installMethod
		wantLabel  string
		wantAction string
	}{
		{
			name:       "go-install",
			method:     installMethodGoInstall,
			wantLabel:  "go install",
			wantAction: "re-running the Go install",
		},
		{
			name:       "tarball",
			method:     installMethodTarball,
			wantLabel:  "release tarball",
			wantAction: "downloading the latest release and replacing the binary",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runUpdateWith(context.Background(), true, &stdout, &stderr, updateDeps{
				currentVersion: "1.2.3",
				method:         fixedMethod(tt.method),
				slug:           okSlug,
				fetchLatest:    fakeFetch("v2.0.0", nil),
			})
			if code != 0 {
				t.Fatalf("code = %d, want 0\nstderr: %s", code, stderr.String())
			}
			out := stdout.String()
			for _, want := range []string{"Current version", "1.2.3", "Latest version", "2.0.0", tt.wantLabel, tt.wantAction} {
				if !strings.Contains(out, want) {
					t.Errorf("stdout missing %q\nfull:\n%s", want, out)
				}
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

// Exercises the real fetch path end-to-end through runUpdateWith and httptest,
// confirming the whole resolution chain stays hermetic.
func TestRunUpdateWithRealFetchHermetic(t *testing.T) {
	const slug = "doordash-oss/agentic-orchestrator"
	srv := releasesServer(t, slug, http.StatusOK, []map[string]any{{"tag_name": "v9.9.9"}})
	var stdout, stderr bytes.Buffer
	code := runUpdateWith(context.Background(), true, &stdout, &stderr, updateDeps{
		currentVersion: "1.0.0",
		method:         fixedMethod(installMethodTarball),
		slug:           func() (string, error) { return slug, nil },
		fetchLatest:    githubFetchLatestStableTag(srv.Client(), srv.URL),
	})
	if code != 0 {
		t.Fatalf("code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "9.9.9") {
		t.Errorf("stdout missing resolved latest tag\nfull:\n%s", stdout.String())
	}
}

// Guards that the default production wiring resolves an owner/repo slug from
// the test binary's own module path (github.com/doordash-oss/...), proving the
// slug is derived at runtime rather than hardcoded.
func TestModuleSlugDerivedFromBuildInfo(t *testing.T) {
	slug, err := moduleSlug()
	if err != nil {
		t.Fatalf("moduleSlug() error: %v", err)
	}
	if slug != "doordash-oss/agentic-orchestrator" {
		t.Errorf("moduleSlug() = %q, want doordash-oss/agentic-orchestrator", slug)
	}
}

// --- Phase 3: real go-install re-install behind the runner seam -------------

const testMainPkg = "github.com/doordash-oss/agentic-orchestrator/cmd/agentico"

// recordedInstall captures the single invocation a fake runner saw so a test can
// assert the command name, argument slice, and the GOBIN-augmented environment.
type recordedInstall struct {
	called bool
	name   string
	args   []string
	env    []string
}

// fakeRunner returns a commandRunner that records its invocation into rec and
// then yields the supplied combined output and error — so no real `go install`
// ever runs in the unit suite.
func fakeRunner(rec *recordedInstall, out []byte, err error) commandRunner {
	return func(_ context.Context, name string, args, env []string) ([]byte, error) {
		rec.called = true
		rec.name = name
		rec.args = append([]string(nil), args...)
		rec.env = append([]string(nil), env...)
		return out, err
	}
}

// failingRunner returns a commandRunner that fails the test if invoked. It
// guards control-flow paths that must never reach the subprocess (e.g. the
// main-package derivation failing first).
func failingRunner(t *testing.T) commandRunner {
	t.Helper()
	return func(context.Context, string, []string, []string) ([]byte, error) {
		t.Fatal("install runner invoked on a path that must not reach the subprocess")
		return nil, nil
	}
}

// lastEnvValue returns the last value bound to key in an os/exec-style env
// slice (last write wins, matching exec semantics), and whether key was present.
func lastEnvValue(env []string, key string) (string, bool) {
	prefix := key + "="
	val, ok := "", false
	for _, kv := range env {
		if after, found := strings.CutPrefix(kv, prefix); found {
			val, ok = after, true
		}
	}
	return val, ok
}

// goInstallDeps builds an updateDeps wired for the bare go-install branch with a
// fake runner, an injected main-package path, and a fixed resolved binary dir.
func goInstallDeps(rec *recordedInstall, out []byte, runErr error, binDir string) updateDeps {
	return updateDeps{
		currentVersion: "1.2.3",
		method:         fixedMethod(installMethodGoInstall),
		slug:           okSlug,
		fetchLatest:    fakeFetch("v2.0.0", nil),
		mainPackage:    func() (string, error) { return testMainPkg, nil },
		binaryDir:      func() string { return binDir },
		runInstall:     fakeRunner(rec, out, runErr),
	}
}

// --- Homebrew branch: delegate to `brew upgrade` (Codex dispatcher model) ----

// homebrewDeps wires the bare Homebrew branch with a fake brew runner and an
// available update.
func homebrewDeps(rec *recordedInstall, out []byte, runErr error) updateDeps {
	return updateDeps{
		currentVersion: "1.2.3",
		method:         fixedMethod(installMethodHomebrew),
		slug:           okSlug,
		fetchLatest:    fakeFetch("v2.0.0", nil),
		runBrew:        fakeRunner(rec, out, runErr),
	}
}

// Bare homebrew update delegates to `brew upgrade agentico` through the runBrew
// seam and exits 0 without swapping the binary itself.
func TestRunUpdateWithHomebrewSuccess(t *testing.T) {
	var rec recordedInstall
	var stdout, stderr bytes.Buffer

	code := runUpdateWith(context.Background(), false, &stdout, &stderr,
		homebrewDeps(&rec, []byte("==> Upgrading agentico\nPouring agentico"), nil))

	if code != 0 {
		t.Fatalf("code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if !rec.called {
		t.Fatal("brew runner was not invoked")
	}
	if rec.name != "brew" {
		t.Errorf("command name = %q, want %q", rec.name, "brew")
	}
	wantArgs := []string{"upgrade", "agentico"}
	if len(rec.args) != len(wantArgs) {
		t.Fatalf("args = %v, want %v", rec.args, wantArgs)
	}
	for i := range wantArgs {
		if rec.args[i] != wantArgs[i] {
			t.Errorf("args[%d] = %q, want %q", i, rec.args[i], wantArgs[i])
		}
	}
	out := stdout.String()
	for _, want := range []string{"brew upgrade agentico", "Homebrew", "Upgrading agentico"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q\nfull:\n%s", want, out)
		}
	}
}

// --check on a homebrew install reports the method without invoking brew (the
// runner must never be reached on the check path).
func TestRunUpdateWithHomebrewCheckDoesNotRunBrew(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runUpdateWith(context.Background(), true, &stdout, &stderr, updateDeps{
		currentVersion: "1.2.3",
		method:         fixedMethod(installMethodHomebrew),
		slug:           okSlug,
		fetchLatest:    fakeFetch("v2.0.0", nil),
		runBrew:        failingRunner(t), // must not be invoked in --check mode
	})
	if code != 0 {
		t.Fatalf("code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"Current version", "Latest version", "2.0.0", installMethodHomebrew.label(), "brew upgrade agentico"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q\nfull:\n%s", want, out)
		}
	}
}

// A missing brew binary is reported with guidance and a non-zero exit.
func TestRunUpdateWithHomebrewBrewMissing(t *testing.T) {
	var rec recordedInstall
	var stdout, stderr bytes.Buffer
	code := runUpdateWith(context.Background(), false, &stdout, &stderr,
		homebrewDeps(&rec, []byte("command not found: brew"), exec.ErrNotFound))
	if code != 1 {
		t.Fatalf("code = %d, want 1\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "not found on PATH") {
		t.Errorf("stderr missing brew-not-found guidance\nfull:\n%s", stderr.String())
	}
}

// Bare homebrew update refreshes the tap with `brew update` before running
// `brew upgrade agentico`, so a stale local tap clone cannot leave the old
// version installed. The two brew invocations must run in that order.
func TestRunUpdateWithHomebrewRefreshesTapBeforeUpgrade(t *testing.T) {
	var calls [][]string
	var stdout, stderr bytes.Buffer

	deps := updateDeps{
		currentVersion: "1.2.3",
		method:         fixedMethod(installMethodHomebrew),
		slug:           okSlug,
		fetchLatest:    fakeFetch("v2.0.0", nil),
		runBrew: func(_ context.Context, _ string, args, _ []string) ([]byte, error) {
			calls = append(calls, append([]string(nil), args...))
			return nil, nil
		},
	}

	code := runUpdateWith(context.Background(), false, &stdout, &stderr, deps)
	if code != 0 {
		t.Fatalf("code = %d, want 0\nstderr: %s", code, stderr.String())
	}

	wantCalls := [][]string{
		{"update"},
		{"upgrade", "agentico"},
	}
	if len(calls) != len(wantCalls) {
		t.Fatalf("brew invocations = %v, want %v", calls, wantCalls)
	}
	for i := range wantCalls {
		if !equalArgs(calls[i], wantCalls[i]) {
			t.Errorf("call[%d] = %v, want %v", i, calls[i], wantCalls[i])
		}
	}
}

// equalArgs reports whether two argument slices are element-wise equal.
func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A non-zero `brew upgrade` exit is reported as a failure.
func TestRunUpdateWithHomebrewFailure(t *testing.T) {
	var rec recordedInstall
	var stdout, stderr bytes.Buffer
	code := runUpdateWith(context.Background(), false, &stdout, &stderr,
		homebrewDeps(&rec, []byte("Error: brew blew up"), errors.New("exit status 1")))
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "failed") {
		t.Errorf("stderr missing failure message\nfull:\n%s", stderr.String())
	}
}

// Task 1: bare go-install with an update available re-runs `go install` of the
// command's own main package pinned to the resolved tag, augments GOBIN to the
// running binary's directory, prints the old → new transition, and exits 0.
func TestRunUpdateWithGoInstallSuccess(t *testing.T) {
	const binDir = "/home/user/go/bin"
	var rec recordedInstall
	var stdout, stderr bytes.Buffer

	code := runUpdateWith(context.Background(), false, &stdout, &stderr,
		goInstallDeps(&rec, []byte("ok"), nil, binDir))

	if code != 0 {
		t.Fatalf("code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if !rec.called {
		t.Fatal("install runner was not invoked")
	}
	if rec.name != "go" {
		t.Errorf("command name = %q, want %q", rec.name, "go")
	}
	wantArgs := []string{"install", testMainPkg + "@v2.0.0"}
	if len(rec.args) != len(wantArgs) {
		t.Fatalf("args = %v, want %v", rec.args, wantArgs)
	}
	for i := range wantArgs {
		if rec.args[i] != wantArgs[i] {
			t.Fatalf("args = %v, want %v (pinned at the resolved tag)", rec.args, wantArgs)
		}
	}
	if got, ok := lastEnvValue(rec.env, "GOBIN"); !ok || got != binDir {
		t.Errorf("GOBIN = %q (present=%v), want %q (running binary's dir)", got, ok, binDir)
	}
	if !strings.Contains(stdout.String(), "1.2.3 → 2.0.0") {
		t.Errorf("stdout missing old → new transition\nfull:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), updateCheckingNarrative) {
		t.Errorf("stdout missing the %q narrative\nfull:\n%s", updateCheckingNarrative, stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty on success", stderr.String())
	}
}

// When the running binary's directory cannot be resolved, GOBIN is not forced
// and the toolchain default applies — the install still runs.
func TestRunUpdateWithGoInstallNoBinaryDirOmitsGOBIN(t *testing.T) {
	var rec recordedInstall
	var stdout, stderr bytes.Buffer

	code := runUpdateWith(context.Background(), false, &stdout, &stderr,
		goInstallDeps(&rec, []byte("ok"), nil, ""))

	if code != 0 {
		t.Fatalf("code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if _, ok := lastEnvValue(rec.env, "GOBIN"); ok {
		t.Errorf("GOBIN must not be forced when the binary dir is unresolved; env = %v", rec.env)
	}
}

// The main-package derivation failing short-circuits before any subprocess runs.
func TestRunUpdateWithGoInstallMainPackageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	deps := updateDeps{
		currentVersion: "1.2.3",
		method:         fixedMethod(installMethodGoInstall),
		slug:           okSlug,
		fetchLatest:    fakeFetch("v2.0.0", nil),
		mainPackage:    func() (string, error) { return "", errors.New("no main package") },
		binaryDir:      func() string { return "/home/user/go/bin" },
		runInstall:     failingRunner(t),
	}
	code := runUpdateWith(context.Background(), false, &stdout, &stderr, deps)
	if code == 0 {
		t.Fatal("main-package derivation failure must exit non-zero")
	}
	if stderr.Len() == 0 {
		t.Error("expected an error message on stderr")
	}
	if strings.Contains(stdout.String(), "→") {
		t.Errorf("no old → new success line may be printed on failure\nfull:\n%s", stdout.String())
	}
}

// mainPackagePath derives the command's own main-package import path from the
// test binary's build info — proving the package is derived at runtime (not the
// bare module path) and is testable like moduleSlug.
func TestMainPackagePathDerivedFromBuildInfo(t *testing.T) {
	pkg, err := mainPackagePath()
	if err != nil {
		t.Fatalf("mainPackagePath() error: %v", err)
	}
	if pkg != testMainPkg {
		t.Errorf("mainPackagePath() = %q, want %q", pkg, testMainPkg)
	}
}

// --- Phase 3 / Task 2: clear failure surfacing through the runner seam ------

// Toolchain absent (the runner surfaces exec.ErrNotFound): a clear
// toolchain-not-found message to stderr, non-zero exit, and no success line.
func TestRunUpdateWithGoInstallToolchainMissing(t *testing.T) {
	var rec recordedInstall
	var stdout, stderr bytes.Buffer

	notFound := &exec.Error{Name: "go", Err: exec.ErrNotFound}
	code := runUpdateWith(context.Background(), false, &stdout, &stderr,
		goInstallDeps(&rec, nil, notFound, "/home/user/go/bin"))

	if code == 0 {
		t.Fatal("toolchain-absent path must exit non-zero")
	}
	errOut := stderr.String()
	if !strings.Contains(errOut, "Go toolchain") || !strings.Contains(errOut, "PATH") {
		t.Errorf("stderr missing a clear toolchain-not-found message\nfull:\n%s", errOut)
	}
	if strings.Contains(stdout.String(), "→") {
		t.Errorf("no old → new success line may be printed when the toolchain is absent\nfull:\n%s", stdout.String())
	}
}

// `go install` exits non-zero: the captured combined output is wrapped into the
// stderr error message and the command exits non-zero.
func TestRunUpdateWithGoInstallExitsNonZero(t *testing.T) {
	var rec recordedInstall
	var stdout, stderr bytes.Buffer

	const failOutput = "go: downloading module: 404 Not Found"
	code := runUpdateWith(context.Background(), false, &stdout, &stderr,
		goInstallDeps(&rec, []byte(failOutput), errors.New("exit status 1"), "/home/user/go/bin"))

	if code == 0 {
		t.Fatal("install failure must exit non-zero")
	}
	errOut := stderr.String()
	if !strings.Contains(errOut, failOutput) {
		t.Errorf("stderr must include the captured combined output\nfull:\n%s", errOut)
	}
	if strings.Contains(stdout.String(), "→") {
		t.Errorf("no old → new success line may be printed on install failure\nfull:\n%s", stdout.String())
	}
}

// The install context times out (the runner surfaces context.DeadlineExceeded):
// a clear timeout error including the captured output, and a non-zero exit.
func TestRunUpdateWithGoInstallTimeout(t *testing.T) {
	var rec recordedInstall
	var stdout, stderr bytes.Buffer

	const partial = "go: downloading a very large dependency graph"
	timeoutErr := fmt.Errorf("running go install: %w", context.DeadlineExceeded)
	code := runUpdateWith(context.Background(), false, &stdout, &stderr,
		goInstallDeps(&rec, []byte(partial), timeoutErr, "/home/user/go/bin"))

	if code == 0 {
		t.Fatal("install timeout must exit non-zero")
	}
	errOut := stderr.String()
	if !strings.Contains(strings.ToLower(errOut), "timed out") && !strings.Contains(strings.ToLower(errOut), "timeout") {
		t.Errorf("stderr missing a clear timeout error\nfull:\n%s", errOut)
	}
	if !strings.Contains(errOut, partial) {
		t.Errorf("stderr must include the captured output on timeout\nfull:\n%s", errOut)
	}
	if strings.Contains(stdout.String(), "→") {
		t.Errorf("no old → new success line may be printed on timeout\nfull:\n%s", stdout.String())
	}
}

// --- Phase 4: tarball update — fetch, verify, extract, atomic swap ----------

// makeTarGz builds an in-memory .tar.gz containing the given name→content files
// as regular 0755 entries, fabricating a goreleaser-style release archive
// (typically with an inner "agentico" binary alongside LICENSE/README).
func makeTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("writing tar header for %q: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("writing tar body for %q: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar writer: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("closing gzip writer: %v", err)
	}
	return buf.Bytes()
}

// sha256Hex returns the lowercase hex SHA-256 of b, matching checksums.txt.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// tarballServer is an httptest server that serves a single release's listing
// (with assets whose download URLs point back at itself) plus the asset bytes,
// recording the Authorization header seen on every request so a test can assert
// the unauthenticated default and the GITHUB_TOKEN-authenticated behavior.
type tarballServer struct {
	srv         *httptest.Server
	authHeaders []string
}

// newTarballServer wires a hermetic release+asset server. slug/tag identify the
// release; archiveName is the archive asset's name; archive and checksums are
// the served asset bytes. A checksums.txt asset is always advertised.
func newTarballServer(t *testing.T, slug, tag, archiveName string, archive, checksums []byte) *tarballServer {
	t.Helper()
	ts := &tarballServer{}
	releasesPath := "/repos/" + slug + "/releases"
	const archivePath = "/dl/archive"
	const checksumsPath = "/dl/checksums.txt"
	ts.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts.authHeaders = append(ts.authHeaders, r.Header.Get("Authorization"))
		base := "http://" + r.Host
		switch r.URL.Path {
		case releasesPath:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"tag_name": tag,
					"assets": []map[string]any{
						{"name": archiveName, "browser_download_url": base + archivePath},
						{"name": "checksums.txt", "browser_download_url": base + checksumsPath},
					},
				},
			})
		case archivePath:
			_, _ = w.Write(archive)
		case checksumsPath:
			_, _ = w.Write(checksums)
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.srv.Close)
	return ts
}

// sawAuthHeader reports whether any recorded request carried the given exact
// Authorization header value.
func (ts *tarballServer) sawAuthHeader(value string) bool {
	return slices.Contains(ts.authHeaders, value)
}

// sawAnyAuthHeader reports whether any recorded request carried a non-empty
// Authorization header.
func (ts *tarballServer) sawAnyAuthHeader() bool {
	for _, h := range ts.authHeaders {
		if h != "" {
			return true
		}
	}
	return false
}

const (
	testTarballGOOS   = "linux"
	testTarballGOARCH = "amd64"
)

// writeTargetBinary creates a stand-in installed binary under a fresh temp dir
// with the given content and returns its path and pre-swap FileInfo.
func writeTargetBinary(t *testing.T, content string) (string, os.FileInfo) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "agentico")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("writing target binary: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat target binary: %v", err)
	}
	return path, info
}

// Success path through runUpdateWith: the matching archive and checksums.txt
// assets are resolved from the release, downloaded, the archive SHA-256 is
// verified, the inner agentico binary is extracted and atomically swapped into
// the target, the old → new transition is printed, and the process exits 0. The
// target is replaced via rename (a new file identity), preserving any open
// handle to the original — the non-destructive swap guarantee.
func TestRunUpdateWithTarballSuccess(t *testing.T) {
	const slug = "doordash-oss/agentic-orchestrator"
	const newContent = "NEW-AGENTICO-BINARY-BYTES"
	archiveName := fmt.Sprintf("agentic-orchestrator_2.0.0_%s_%s.tar.gz", testTarballGOOS, testTarballGOARCH)
	archive := makeTarGz(t, map[string]string{"agentico": newContent, "LICENSE": "Apache-2.0"})
	checksums := fmt.Appendf(nil, "%s  %s\n%s  some-other-asset.tar.gz\n", sha256Hex(archive), archiveName, sha256Hex([]byte("other")))

	srv := newTarballServer(t, slug, "v2.0.0", archiveName, archive, checksums)
	target, origInfo := writeTargetBinary(t, "OLD-AGENTICO-BINARY")

	var stdout, stderr bytes.Buffer
	code := runUpdateWith(context.Background(), false, &stdout, &stderr, updateDeps{
		currentVersion: "1.0.0",
		method:         fixedMethod(installMethodTarball),
		slug:           func() (string, error) { return slug, nil },
		fetchLatest:    fakeFetch("v2.0.0", nil),
		runTarball:     newTarballUpdater(srv.srv.Client(), srv.srv.URL, target, testTarballGOOS, testTarballGOARCH),
	})

	t.Logf("exit=%d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	if code != 0 {
		t.Fatalf("code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "1.0.0 → 2.0.0") {
		t.Errorf("stdout missing old → new transition\nfull:\n%s", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty on success", stderr.String())
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading swapped target: %v", err)
	}
	if string(got) != newContent {
		t.Errorf("target content = %q, want %q", got, newContent)
	}
	newInfo, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat swapped target: %v", err)
	}
	if os.SameFile(origInfo, newInfo) {
		t.Error("swap must replace the target via rename (new file identity), preserving the original inode for any process holding it open")
	}
	if newInfo.Mode().Perm()&0o100 == 0 {
		t.Errorf("swapped target is not executable: mode %v", newInfo.Mode())
	}
}

// Checksum mismatch is non-destructive: the swap aborts before touching the
// binary, a clear error reaches stderr, the process exits non-zero, no old → new
// line is printed, and the original binary is byte-for-byte unchanged.
func TestRunUpdateWithTarballChecksumMismatch(t *testing.T) {
	const slug = "doordash-oss/agentic-orchestrator"
	archiveName := fmt.Sprintf("agentic-orchestrator_2.0.0_%s_%s.tar.gz", testTarballGOOS, testTarballGOARCH)
	archive := makeTarGz(t, map[string]string{"agentico": "NEW"})
	// A digest that does not match the archive bytes.
	checksums := fmt.Appendf(nil, "%s  %s\n", sha256Hex([]byte("WRONG")), archiveName)

	srv := newTarballServer(t, slug, "v2.0.0", archiveName, archive, checksums)
	const original = "ORIGINAL-UNTOUCHED"
	target, origInfo := writeTargetBinary(t, original)

	var stdout, stderr bytes.Buffer
	code := runUpdateWith(context.Background(), false, &stdout, &stderr, updateDeps{
		currentVersion: "1.0.0",
		method:         fixedMethod(installMethodTarball),
		slug:           func() (string, error) { return slug, nil },
		fetchLatest:    fakeFetch("v2.0.0", nil),
		runTarball:     newTarballUpdater(srv.srv.Client(), srv.srv.URL, target, testTarballGOOS, testTarballGOARCH),
	})

	t.Logf("exit=%d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	if code == 0 {
		t.Fatal("checksum mismatch must exit non-zero")
	}
	if strings.Contains(stdout.String(), "→") {
		t.Errorf("no old → new line may print on checksum mismatch\nfull:\n%s", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Error("expected a clear checksum error on stderr")
	}
	if !strings.Contains(strings.ToLower(stderr.String()), "checksum") {
		t.Errorf("the error must name the checksum so the user can diagnose it\nfull:\n%s", stderr.String())
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading target: %v", err)
	}
	if string(got) != original {
		t.Errorf("target content = %q, want it left untouched as %q", got, original)
	}
	newInfo, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if !os.SameFile(origInfo, newInfo) {
		t.Error("the original binary file must be left untouched (same identity) on a failed swap")
	}
}

// Missing per-OS/arch asset is non-destructive: when the release carries no
// archive matching the running platform, the update errors clearly, exits
// non-zero, prints no transition, and leaves the binary unchanged.
func TestRunUpdateWithTarballMissingAsset(t *testing.T) {
	const slug = "doordash-oss/agentic-orchestrator"
	// The only published archive targets darwin/arm64, not the linux/amd64 we ask for.
	archiveName := "agentic-orchestrator_2.0.0_darwin_arm64.tar.gz"
	archive := makeTarGz(t, map[string]string{"agentico": "NEW"})
	checksums := fmt.Appendf(nil, "%s  %s\n", sha256Hex(archive), archiveName)

	srv := newTarballServer(t, slug, "v2.0.0", archiveName, archive, checksums)
	const original = "ORIGINAL-UNTOUCHED"
	target, origInfo := writeTargetBinary(t, original)

	var stdout, stderr bytes.Buffer
	code := runUpdateWith(context.Background(), false, &stdout, &stderr, updateDeps{
		currentVersion: "1.0.0",
		method:         fixedMethod(installMethodTarball),
		slug:           func() (string, error) { return slug, nil },
		fetchLatest:    fakeFetch("v2.0.0", nil),
		runTarball:     newTarballUpdater(srv.srv.Client(), srv.srv.URL, target, testTarballGOOS, testTarballGOARCH),
	})

	if code == 0 {
		t.Fatal("missing per-platform asset must exit non-zero")
	}
	if strings.Contains(stdout.String(), "→") {
		t.Errorf("no old → new line may print when the asset is missing\nfull:\n%s", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Error("expected a clear missing-asset error on stderr")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading target: %v", err)
	}
	if string(got) != original {
		t.Errorf("target content = %q, want it left untouched as %q", got, original)
	}
	newInfo, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat target: %v", err)
	}
	if !os.SameFile(origInfo, newInfo) {
		t.Error("the original binary file must be left untouched on a missing-asset error")
	}
}

// The tarball flow is unauthenticated by default and adds an Authorization:
// Bearer header to every GitHub request only when GITHUB_TOKEN is set.
func TestTarballUpdaterGithubTokenAuth(t *testing.T) {
	const slug = "doordash-oss/agentic-orchestrator"
	archiveName := fmt.Sprintf("agentic-orchestrator_2.0.0_%s_%s.tar.gz", testTarballGOOS, testTarballGOARCH)
	archive := makeTarGz(t, map[string]string{"agentico": "NEW"})
	checksums := fmt.Appendf(nil, "%s  %s\n", sha256Hex(archive), archiveName)

	runOnce := func(t *testing.T) *tarballServer {
		t.Helper()
		srv := newTarballServer(t, slug, "v2.0.0", archiveName, archive, checksums)
		target, _ := writeTargetBinary(t, "OLD")
		update := newTarballUpdater(srv.srv.Client(), srv.srv.URL, target, testTarballGOOS, testTarballGOARCH)
		if err := update(context.Background(), slug, "v2.0.0"); err != nil {
			t.Fatalf("tarball update failed: %v", err)
		}
		return srv
	}

	t.Run("unauthenticated by default", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		srv := runOnce(t)
		if srv.sawAnyAuthHeader() {
			t.Errorf("no Authorization header expected without GITHUB_TOKEN; saw %v", srv.authHeaders)
		}
	})

	t.Run("adds Bearer token when GITHUB_TOKEN set", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "secret-token")
		srv := runOnce(t)
		if !srv.sawAuthHeader("Bearer secret-token") {
			t.Errorf("expected Bearer token on requests; saw %v", srv.authHeaders)
		}
	})
}

// --- Phase 4: tarball update helper units -----------------------------------

func TestProjectNameFromSlug(t *testing.T) {
	cases := map[string]string{
		"doordash-oss/agentic-orchestrator": "agentic-orchestrator",
		"owner/repo":                        "repo",
		"single":                            "single",
	}
	for in, want := range cases {
		if got := projectNameFromSlug(in); got != want {
			t.Errorf("projectNameFromSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestChecksumFor(t *testing.T) {
	checksums := []byte("aaa  first.tar.gz\nbbb  second.tar.gz\n\nccc  dist/third.tar.gz\n")
	t.Run("exact name match", func(t *testing.T) {
		got, ok := checksumFor(checksums, "second.tar.gz")
		if !ok || got != "bbb" {
			t.Errorf("checksumFor(second) = %q,%v want bbb,true", got, ok)
		}
	})
	t.Run("matches on base name", func(t *testing.T) {
		got, ok := checksumFor(checksums, "third.tar.gz")
		if !ok || got != "ccc" {
			t.Errorf("checksumFor(third) = %q,%v want ccc,true", got, ok)
		}
	})
	t.Run("missing entry", func(t *testing.T) {
		if _, ok := checksumFor(checksums, "absent.tar.gz"); ok {
			t.Error("checksumFor(absent) ok = true, want false")
		}
	})
}

func TestVerifyChecksum(t *testing.T) {
	archive := []byte("the-archive-bytes")
	name := "agentic.tar.gz"
	good := fmt.Appendf(nil, "%s  %s\n", sha256Hex(archive), name)
	t.Run("match", func(t *testing.T) {
		if err := verifyChecksum(archive, good, name); err != nil {
			t.Errorf("verifyChecksum match error: %v", err)
		}
	})
	t.Run("case-insensitive digest", func(t *testing.T) {
		upper := fmt.Appendf(nil, "%s  %s\n", strings.ToUpper(sha256Hex(archive)), name)
		if err := verifyChecksum(archive, upper, name); err != nil {
			t.Errorf("verifyChecksum should accept upper-case digest: %v", err)
		}
	})
	t.Run("mismatch", func(t *testing.T) {
		bad := fmt.Appendf(nil, "%s  %s\n", sha256Hex([]byte("other")), name)
		if err := verifyChecksum(archive, bad, name); err == nil {
			t.Error("verifyChecksum mismatch error = nil, want error")
		}
	})
	t.Run("no entry for archive", func(t *testing.T) {
		if err := verifyChecksum(archive, []byte("zzz  unrelated.tar.gz\n"), name); err == nil {
			t.Error("verifyChecksum with no matching entry error = nil, want error")
		}
	})
}

func TestExtractAgenticoBinary(t *testing.T) {
	t.Run("extracts inner agentico binary", func(t *testing.T) {
		const want = "BINARY-CONTENT"
		archive := makeTarGz(t, map[string]string{"LICENSE": "x", "agentico": want, "README.md": "y"})
		got, err := extractAgenticoBinary(archive)
		if err != nil {
			t.Fatalf("extractAgenticoBinary error: %v", err)
		}
		if string(got) != want {
			t.Errorf("extracted content = %q, want %q", got, want)
		}
	})
	t.Run("errors when agentico entry absent", func(t *testing.T) {
		archive := makeTarGz(t, map[string]string{"LICENSE": "x", "README.md": "y"})
		if _, err := extractAgenticoBinary(archive); err == nil {
			t.Error("expected error when archive lacks an agentico entry")
		}
	})
	t.Run("errors on non-gzip input", func(t *testing.T) {
		if _, err := extractAgenticoBinary([]byte("not a gzip stream")); err == nil {
			t.Error("expected error on a corrupt archive")
		}
	})
}

// swapBinary writes a temp file in the target directory and renames it over the
// target: the swapped target has new content, an executable mode, and a new
// file identity (rename), so the original inode survives for any open handle.
func TestSwapBinaryAtomicRename(t *testing.T) {
	const newContent = "REPLACEMENT"
	target, origInfo := writeTargetBinary(t, "ORIGINAL")

	// Hold the original file open to model the running process; reading from the
	// handle after the swap must still return the original bytes.
	orig, err := os.Open(target)
	if err != nil {
		t.Fatalf("opening original: %v", err)
	}
	defer orig.Close()

	if err := swapBinary(target, []byte(newContent)); err != nil {
		t.Fatalf("swapBinary error: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading swapped target: %v", err)
	}
	if string(got) != newContent {
		t.Errorf("target content = %q, want %q", got, newContent)
	}
	newInfo, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat swapped target: %v", err)
	}
	if os.SameFile(origInfo, newInfo) {
		t.Error("swapBinary must rename a fresh file over the target (new identity)")
	}
	if newInfo.Mode().Perm()&0o100 == 0 {
		t.Errorf("swapped target not executable: %v", newInfo.Mode())
	}
	fromHandle, err := io.ReadAll(orig)
	if err != nil {
		t.Fatalf("reading original handle: %v", err)
	}
	if string(fromHandle) != "ORIGINAL" {
		t.Errorf("original handle content = %q, want it preserved as %q", fromHandle, "ORIGINAL")
	}
}

// swapBinary leaves nothing behind when the target directory is unwritable: the
// temp-file creation fails and the (absent) target is not created.
func TestSwapBinaryFailsClosedOnBadDir(t *testing.T) {
	target := filepath.Join(t.TempDir(), "no-such-subdir", "agentico")
	if err := swapBinary(target, []byte("X")); err == nil {
		t.Fatal("swapBinary into a nonexistent directory must error")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("target must not exist after a failed swap; stat err = %v", err)
	}
}

// --- Phase 5: tarball robustness — pre-flight writability + macOS re-sign ----

// failingTarball returns a tarballUpdater that fails the test if invoked. It
// guards the pre-flight: an unwritable target must abort before the download
// seam (which issues the GitHub requests and mutates the binary) is ever
// reached.
func failingTarball(t *testing.T) tarballUpdater {
	t.Helper()
	return func(context.Context, string, string) error {
		t.Fatal("tarball download seam invoked after a pre-flight that must have aborted")
		return nil
	}
}

// newTarballSuccessDeps wires a hermetic release server, a writable temp target
// binary, and the base updateDeps for a successful tarball swap through
// runUpdateWith (current 1.0.0 → latest 2.0.0). Callers set the Phase-5 seams
// (goos, binaryPath, runCodesign, checkWritable) on the returned deps before
// invoking runUpdateWith. The returned target is the swapped-in-place path.
func newTarballSuccessDeps(t *testing.T) (updateDeps, string) {
	t.Helper()
	const slug = "doordash-oss/agentic-orchestrator"
	archiveName := fmt.Sprintf("agentic-orchestrator_2.0.0_%s_%s.tar.gz", testTarballGOOS, testTarballGOARCH)
	archive := makeTarGz(t, map[string]string{"agentico": "NEW-AGENTICO-BINARY-BYTES"})
	checksums := fmt.Appendf(nil, "%s  %s\n", sha256Hex(archive), archiveName)
	srv := newTarballServer(t, slug, "v2.0.0", archiveName, archive, checksums)
	target, _ := writeTargetBinary(t, "OLD-AGENTICO-BINARY")
	deps := updateDeps{
		currentVersion: "1.0.0",
		method:         fixedMethod(installMethodTarball),
		slug:           func() (string, error) { return slug, nil },
		fetchLatest:    fakeFetch("v2.0.0", nil),
		runTarball:     newTarballUpdater(srv.srv.Client(), srv.srv.URL, target, testTarballGOOS, testTarballGOARCH),
	}
	return deps, target
}

// --- Task 1: pre-flight writability check -----------------------------------

// checkDirWritable performs the swap's own temp-create + rename probe: a
// writable directory succeeds, a nonexistent one fails, and an empty path (the
// binary directory could not be resolved) is reported as an error — all
// independent of the test process's uid.
func TestCheckDirWritable(t *testing.T) {
	t.Run("writable directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := checkDirWritable(dir); err != nil {
			t.Errorf("checkDirWritable(writable temp dir) = %v, want nil", err)
		}
		// Inspect the same directory that was probed: the temp-create + remove
		// must leave nothing behind.
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("reading probed dir: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("probe must leave no temp entry behind; found %d", len(entries))
		}
	})
	t.Run("nonexistent directory", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "no-such-subdir")
		if err := checkDirWritable(dir); err == nil {
			t.Error("checkDirWritable(nonexistent dir) = nil, want error")
		}
	})
	t.Run("empty path", func(t *testing.T) {
		if err := checkDirWritable(""); err == nil {
			t.Error("checkDirWritable(\"\") = nil, want error")
		}
	})
}

// On the real tarball update path, when an update IS warranted but the target
// directory is unwritable, the command aborts before any download: it exits
// non-zero, prints stderr guidance naming the directory and pointing at elevated
// privileges (sudo), prints no old → new line, and never reaches the download
// seam. The pre-flight runs only after the version check confirms an update is
// needed, so the fetch does run here. The verdict is driven by an injected
// check, so it is deterministic regardless of the test process's uid.
func TestRunUpdateWithTarballPreflightUnwritableAborts(t *testing.T) {
	const dir = "/usr/local/bin"
	var checkedDir string
	var fetchCalled bool
	deps := updateDeps{
		currentVersion: "1.0.0",
		method:         fixedMethod(installMethodTarball),
		slug:           okSlug,
		// An update is available (2.0.0 > 1.0.0), so the version check falls
		// through to the pre-flight, which then aborts on the unwritable dir.
		fetchLatest: func(context.Context, string) (string, error) {
			fetchCalled = true
			return "v2.0.0", nil
		},
		binaryPath: dir + "/agentico",
		checkWritable: func(d string) error {
			checkedDir = d
			return errors.New("permission denied")
		},
		runTarball: failingTarball(t), // the download/swap seam must never be reached
	}

	var stdout, stderr bytes.Buffer
	code := runUpdateWith(context.Background(), false, &stdout, &stderr, deps)

	t.Logf("exit=%d fetchCalled=%v\nstdout:\n%s\nstderr:\n%s", code, fetchCalled, stdout.String(), stderr.String())
	if code == 0 {
		t.Fatalf("unwritable target must exit non-zero, got %d", code)
	}
	if !fetchCalled {
		t.Error("the version check (and its fetch) must run before the pre-flight, so an up-to-date binary is not nagged about writability")
	}
	if checkedDir != dir {
		t.Errorf("pre-flight probed %q, want the binary's directory %q", checkedDir, dir)
	}
	errOut := stderr.String()
	if !strings.Contains(errOut, dir) {
		t.Errorf("stderr must name the unwritable directory %q\nfull:\n%s", dir, errOut)
	}
	if !strings.Contains(strings.ToLower(errOut), "sudo") {
		t.Errorf("stderr must point the user at elevated privileges (sudo)\nfull:\n%s", errOut)
	}
	if strings.Contains(errOut, "could not update from the release tarball") {
		t.Errorf("the pre-flight message must be distinct from the generic tarball-failure error\nfull:\n%s", errOut)
	}
	if strings.Contains(stdout.String(), "→") {
		t.Errorf("no old → new line may print when the pre-flight aborts\nfull:\n%s", stdout.String())
	}
}

// A writable target directory lets the flow proceed unchanged into the existing
// fetch → verify → swap path: the swap happens, the old → new line prints, and
// the command exits 0.
func TestRunUpdateWithTarballPreflightWritableProceeds(t *testing.T) {
	deps, target := newTarballSuccessDeps(t)
	var checked bool
	deps.binaryPath = target
	deps.checkWritable = func(string) error { checked = true; return nil }

	var stdout, stderr bytes.Buffer
	code := runUpdateWith(context.Background(), false, &stdout, &stderr, deps)

	if code != 0 {
		t.Fatalf("code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if !checked {
		t.Error("the pre-flight writability check must run on the tarball path")
	}
	if !strings.Contains(stdout.String(), "1.0.0 → 2.0.0") {
		t.Errorf("a writable target must proceed to the swap and print the transition\nfull:\n%s", stdout.String())
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading swapped target: %v", err)
	}
	if string(got) != "NEW-AGENTICO-BINARY-BYTES" {
		t.Errorf("target content = %q, want the swapped-in bytes", got)
	}
}

// --- Task 2: best-effort macOS ad-hoc re-sign -------------------------------

// After a successful darwin swap, codesign -s - is invoked against the resolved
// binary path through the runner seam, mirroring the system-install step; the
// success line still prints, no warning is emitted, and the command exits 0.
func TestRunUpdateWithTarballResignsDarwin(t *testing.T) {
	deps, target := newTarballSuccessDeps(t)
	var rec recordedInstall
	deps.goos = "darwin"
	deps.binaryPath = target
	deps.runCodesign = fakeRunner(&rec, nil, nil)

	var stdout, stderr bytes.Buffer
	code := runUpdateWith(context.Background(), false, &stdout, &stderr, deps)

	t.Logf("exit=%d codesign=%v %v\nstdout:\n%s\nstderr:\n%s", code, rec.name, rec.args, stdout.String(), stderr.String())
	if code != 0 {
		t.Fatalf("code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if !rec.called {
		t.Fatal("codesign runner was not invoked after a darwin swap")
	}
	if rec.name != "codesign" {
		t.Errorf("command name = %q, want %q", rec.name, "codesign")
	}
	wantArgs := []string{"-s", "-", target}
	if !slices.Equal(rec.args, wantArgs) {
		t.Errorf("codesign args = %v, want %v (mirroring the system-install step)", rec.args, wantArgs)
	}
	if !strings.Contains(stdout.String(), "1.0.0 → 2.0.0") {
		t.Errorf("stdout missing old → new transition\nfull:\n%s", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("a successful re-sign must emit no warning; stderr = %q", stderr.String())
	}
}

// A missing codesign tool is non-fatal: a stderr warning is printed, the old →
// new success line still prints on stdout, and the command exits 0 (the swap is
// not undone).
func TestRunUpdateWithTarballResignMissingToolNonFatal(t *testing.T) {
	deps, target := newTarballSuccessDeps(t)
	var rec recordedInstall
	deps.goos = "darwin"
	deps.binaryPath = target
	deps.runCodesign = fakeRunner(&rec, nil, &exec.Error{Name: "codesign", Err: exec.ErrNotFound})

	var stdout, stderr bytes.Buffer
	code := runUpdateWith(context.Background(), false, &stdout, &stderr, deps)

	t.Logf("exit=%d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	if code != 0 {
		t.Fatalf("a missing codesign tool must stay non-fatal (exit 0), got %d\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "1.0.0 → 2.0.0") {
		t.Errorf("the success line must still print when re-sign is skipped\nfull:\n%s", stdout.String())
	}
	if !strings.Contains(strings.ToLower(stderr.String()), "warning") {
		t.Errorf("a missing codesign tool must emit a non-fatal warning\nfull:\n%s", stderr.String())
	}
}

// A codesign invocation failure (non-zero exit) is non-fatal: a stderr warning
// is printed, the success line still prints, and the command exits 0 — the swap
// is not rolled back.
func TestRunUpdateWithTarballResignFailureNonFatal(t *testing.T) {
	deps, target := newTarballSuccessDeps(t)
	var rec recordedInstall
	deps.goos = "darwin"
	deps.binaryPath = target
	deps.runCodesign = fakeRunner(&rec, []byte("codesign: some signing error"), errors.New("exit status 1"))

	var stdout, stderr bytes.Buffer
	code := runUpdateWith(context.Background(), false, &stdout, &stderr, deps)

	t.Logf("exit=%d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	if code != 0 {
		t.Fatalf("a codesign failure must stay non-fatal (exit 0), got %d\nstderr: %s", code, stderr.String())
	}
	if !rec.called {
		t.Fatal("codesign runner was not invoked")
	}
	if !strings.Contains(stdout.String(), "1.0.0 → 2.0.0") {
		t.Errorf("the success line must still print on a re-sign failure\nfull:\n%s", stdout.String())
	}
	if !strings.Contains(strings.ToLower(stderr.String()), "warning") {
		t.Errorf("a codesign failure must emit a non-fatal warning\nfull:\n%s", stderr.String())
	}
}

// Off darwin the re-sign is a literal no-op: the runner is never invoked, no
// warning prints, the success line prints, and the command exits 0.
func TestRunUpdateWithTarballNoResignOffDarwin(t *testing.T) {
	deps, target := newTarballSuccessDeps(t)
	deps.goos = "linux"
	deps.binaryPath = target
	deps.runCodesign = failingRunner(t) // must never be invoked off darwin

	var stdout, stderr bytes.Buffer
	code := runUpdateWith(context.Background(), false, &stdout, &stderr, deps)

	if code != 0 {
		t.Fatalf("code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "1.0.0 → 2.0.0") {
		t.Errorf("stdout missing old → new transition\nfull:\n%s", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("off-darwin must emit no re-sign warning; stderr = %q", stderr.String())
	}
}

// --- downgrade guard --------------------------------------------------------

// When the running version is newer than the latest published release, a bare
// tarball update must NOT downgrade: it reports "already up to date" (noting the
// running version is ahead), exits 0, and never reaches the download/swap seam.
func TestRunUpdateWithTarballDoesNotDowngrade(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runUpdateWith(context.Background(), false, &stdout, &stderr, updateDeps{
		currentVersion: "2.0.0",
		method:         fixedMethod(installMethodTarball),
		slug:           okSlug,
		fetchLatest:    fakeFetch("v1.0.0", nil),
		runTarball:     failingTarball(t), // the swap must never run when no update is warranted
	})

	t.Logf("exit=%d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	if code != 0 {
		t.Fatalf("a current version newer than latest must exit 0 (no downgrade), got %d", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "already up to date") || !strings.Contains(out, "newer than") {
		t.Errorf("expected an up-to-date/ahead message, got:\n%s", out)
	}
	if strings.Contains(out, "→") {
		t.Errorf("no old → new transition may print when no update is warranted\n%s", out)
	}
}

// Numerically-equal versions that differ only in formatting (leading v) are
// treated as up to date — the swap does not run.
func TestRunUpdateWithTarballEqualVersionNoUpdate(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runUpdateWith(context.Background(), false, &stdout, &stderr, updateDeps{
		currentVersion: "v2.0.0",
		method:         fixedMethod(installMethodTarball),
		slug:           okSlug,
		fetchLatest:    fakeFetch("2.0.0", nil),
		runTarball:     failingTarball(t),
	})
	if code != 0 {
		t.Fatalf("equal versions must exit 0, got %d\nstderr:%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "already up to date") {
		t.Errorf("expected already-up-to-date message, got:\n%s", stdout.String())
	}
}

// --- forgiving asset matching -----------------------------------------------

func TestMatchArchiveAsset(t *testing.T) {
	const project = "agentic-orchestrator"
	tests := []struct {
		name         string
		goos, goarch string
		want         bool
	}{
		{"agentic-orchestrator_2.0.0_linux_amd64.tar.gz", "linux", "amd64", true},
		{"agentic-orchestrator_2.0.0_linux_x86_64.tar.gz", "linux", "amd64", true},    // arch alias
		{"agentic-orchestrator_2.0.0_darwin_aarch64.tar.gz", "darwin", "arm64", true}, // arch alias
		{"agentic-orchestrator_2.0.0_macos_arm64.tar.gz", "darwin", "arm64", true},    // os alias
		{"AGENTIC-ORCHESTRATOR_2.0.0_LINUX_AMD64.TAR.GZ", "linux", "amd64", true},     // case-insensitive
		{"agentic-orchestrator_2.0.0_linux_amd64.tgz", "linux", "amd64", true},        // .tgz
		{"agentic-orchestrator_2.0.0_linux_arm64.tar.gz", "linux", "amd64", false},    // arch mismatch
		{"agentic-orchestrator_2.0.0_windows_amd64.tar.gz", "linux", "amd64", false},  // os mismatch
		{"agentic-orchestrator_2.0.0_linux_amd64.zip", "linux", "amd64", false},       // zip not matched
		{"other-project_2.0.0_linux_amd64.tar.gz", "linux", "amd64", false},           // wrong project
		{"checksums.txt", "linux", "amd64", false},                                    // no prefix
		{"agentic-orchestrator_linux_amd64.tar.gz", "linux", "amd64", false},          // too few fields
	}
	for _, tt := range tests {
		if got := matchArchiveAsset(tt.name, project, tt.goos, tt.goarch); got != tt.want {
			t.Errorf("matchArchiveAsset(%q, %s/%s) = %v, want %v", tt.name, tt.goos, tt.goarch, got, tt.want)
		}
	}
}

// End-to-end: an archive named with an arch alias (x86_64 for amd64) still
// resolves and swaps successfully.
func TestRunUpdateWithTarballMatchesAliasArchive(t *testing.T) {
	const slug = "doordash-oss/agentic-orchestrator"
	const newContent = "NEW-AGENTICO-ALIAS"
	// linux/amd64 binary published under the x86_64 alias name.
	archiveName := "agentic-orchestrator_2.0.0_linux_x86_64.tar.gz"
	archive := makeTarGz(t, map[string]string{"agentico": newContent})
	checksums := fmt.Appendf(nil, "%s  %s\n", sha256Hex(archive), archiveName)

	srv := newTarballServer(t, slug, "v2.0.0", archiveName, archive, checksums)
	target, _ := writeTargetBinary(t, "OLD")

	var stdout, stderr bytes.Buffer
	code := runUpdateWith(context.Background(), false, &stdout, &stderr, updateDeps{
		currentVersion: "1.0.0",
		method:         fixedMethod(installMethodTarball),
		slug:           func() (string, error) { return slug, nil },
		fetchLatest:    fakeFetch("v2.0.0", nil),
		runTarball:     newTarballUpdater(srv.srv.Client(), srv.srv.URL, target, "linux", "amd64"),
	})

	if code != 0 {
		t.Fatalf("alias-named archive must resolve and swap, got %d\nstderr:%s", code, stderr.String())
	}
	if got, _ := os.ReadFile(target); string(got) != newContent {
		t.Errorf("target content = %q, want %q", got, newContent)
	}
}

// --- mode preservation ------------------------------------------------------

// swapBinary preserves the existing binary's permission bits across the swap,
// guaranteeing the result stays executable by its owner.
func TestSwapBinaryPreservesMode(t *testing.T) {
	tests := []struct {
		orig os.FileMode
		want os.FileMode
	}{
		{0o755, 0o755},
		{0o750, 0o750},
		{0o640, 0o740}, // no owner-exec bit -> swap guarantees 0o100
	}
	for _, tt := range tests {
		dir := t.TempDir()
		target := filepath.Join(dir, "agentico")
		if err := os.WriteFile(target, []byte("OLD"), tt.orig); err != nil {
			t.Fatalf("writing target: %v", err)
		}
		// WriteFile is subject to umask; normalize the starting mode explicitly.
		if err := os.Chmod(target, tt.orig); err != nil {
			t.Fatalf("chmod target: %v", err)
		}
		if err := swapBinary(target, []byte("NEW")); err != nil {
			t.Fatalf("swapBinary: %v", err)
		}
		info, err := os.Stat(target)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.Mode().Perm() != tt.want {
			t.Errorf("orig %v -> swapped %v, want %v", tt.orig, info.Mode().Perm(), tt.want)
		}
		if got, _ := os.ReadFile(target); string(got) != "NEW" {
			t.Errorf("content = %q, want NEW", got)
		}
	}
}

// --- pre-flight ordering relative to the version check ----------------------

// Regression guard for the reorder: a binary AHEAD of the latest release in an
// unwritable directory must report "already up to date" and exit 0 — the
// version check runs before the pre-flight, so no spurious "use sudo" error and
// no swap. (Before the reorder, the pre-flight fired first and produced a
// misleading non-zero sudo error.)
func TestRunUpdateWithTarballAheadInUnwritableDirReportsUpToDate(t *testing.T) {
	var checkWritableCalled bool
	var stdout, stderr bytes.Buffer
	code := runUpdateWith(context.Background(), false, &stdout, &stderr, updateDeps{
		currentVersion: "2.0.0",
		method:         fixedMethod(installMethodTarball),
		slug:           okSlug,
		fetchLatest:    fakeFetch("v1.0.0", nil), // latest is OLDER than current
		binaryPath:     "/usr/local/bin/agentico",
		checkWritable:  func(string) error { checkWritableCalled = true; return errors.New("permission denied") },
		runTarball:     failingTarball(t),
	})

	t.Logf("exit=%d checkWritableCalled=%v\nstdout:\n%s\nstderr:\n%s", code, checkWritableCalled, stdout.String(), stderr.String())
	if code != 0 {
		t.Fatalf("an ahead binary must exit 0 even in an unwritable dir, got %d\nstderr:%s", code, stderr.String())
	}
	if checkWritableCalled {
		t.Error("the writability pre-flight must NOT run when no update is warranted (no spurious sudo error)")
	}
	if !strings.Contains(stdout.String(), "already up to date") {
		t.Errorf("expected an up-to-date report, got:\n%s", stdout.String())
	}
	if strings.Contains(strings.ToLower(stderr.String()), "sudo") {
		t.Errorf("no sudo/writability error may print when no update is warranted\nstderr:\n%s", stderr.String())
	}
}

// In --check mode the downgrade guard still fires: a binary newer than the
// latest release reports up-to-date and exits 0 without printing "Would update".
func TestRunUpdateWithDoesNotDowngradeCheckMode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runUpdateWith(context.Background(), true /*checkOnly*/, &stdout, &stderr, updateDeps{
		currentVersion: "2.0.0",
		method:         fixedMethod(installMethodTarball),
		slug:           okSlug,
		fetchLatest:    fakeFetch("v1.0.0", nil),
		runTarball:     failingTarball(t),
	})
	if code != 0 {
		t.Fatalf("check mode with an ahead version must exit 0, got %d", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "already up to date") || !strings.Contains(out, "newer than") {
		t.Errorf("check mode must report up-to-date/ahead, got:\n%s", out)
	}
	if strings.Contains(out, "Would update") {
		t.Errorf("check mode must not claim an update is available for an ahead binary\n%s", out)
	}
}

// --check mode never triggers the writability pre-flight, even on a tarball
// install in an unwritable directory: it reports the would-do action and exits 0.
func TestRunUpdateWithCheckModeSkipsPreflight(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runUpdateWith(context.Background(), true /*checkOnly*/, &stdout, &stderr, updateDeps{
		currentVersion: "1.0.0",
		method:         fixedMethod(installMethodTarball),
		slug:           okSlug,
		fetchLatest:    fakeFetch("v2.0.0", nil), // an update IS available
		binaryPath:     "/usr/local/bin/agentico",
		checkWritable: func(string) error {
			t.Error("pre-flight must not run in --check mode")
			return errors.New("permission denied")
		},
		runTarball: failingTarball(t),
	})
	if code != 0 {
		t.Fatalf("--check must exit 0 regardless of writability, got %d\nstderr:%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Would update") {
		t.Errorf("--check must report the would-do action, got:\n%s", stdout.String())
	}
}

// --- rate-limit handling on the check path ----------------------------------

func TestRateLimitMessage(t *testing.T) {
	resp := func(status int, remaining, reset string) *http.Response {
		h := http.Header{}
		if remaining != "" {
			h.Set("X-RateLimit-Remaining", remaining)
		}
		if reset != "" {
			h.Set("X-RateLimit-Reset", reset)
		}
		return &http.Response{StatusCode: status, Header: h}
	}

	t.Run("unauthenticated 403 points at GITHUB_TOKEN and the reset time", func(t *testing.T) {
		msg := rateLimitMessage(resp(http.StatusForbidden, "0", "1780506343"), false)
		if !strings.Contains(msg, "GITHUB_TOKEN") || !strings.Contains(msg, "60/hour") {
			t.Errorf("message must name GITHUB_TOKEN and the 60/hour limit, got %q", msg)
		}
		if !strings.Contains(msg, "Try again after") {
			t.Errorf("message must include the reset time, got %q", msg)
		}
	})
	t.Run("429 is also treated as rate limiting", func(t *testing.T) {
		if rateLimitMessage(resp(http.StatusTooManyRequests, "0", ""), false) == "" {
			t.Error("429 with remaining 0 must be reported as rate limiting")
		}
	})
	t.Run("authenticated token gets a token-specific message, no GITHUB_TOKEN hint", func(t *testing.T) {
		msg := rateLimitMessage(resp(http.StatusForbidden, "0", ""), true)
		if msg == "" || strings.Contains(msg, "GITHUB_TOKEN") {
			t.Errorf("an authenticated rate-limit must not suggest setting GITHUB_TOKEN, got %q", msg)
		}
	})
	t.Run("403 with remaining left is not a rate limit", func(t *testing.T) {
		if got := rateLimitMessage(resp(http.StatusForbidden, "12", ""), false); got != "" {
			t.Errorf("a 403 with quota remaining is some other error, got %q", got)
		}
	})
	t.Run("non-error status is not a rate limit", func(t *testing.T) {
		if got := rateLimitMessage(resp(http.StatusOK, "0", ""), false); got != "" {
			t.Errorf("a 200 is never a rate limit, got %q", got)
		}
	})
	t.Run("missing reset header omits the retry hint", func(t *testing.T) {
		if got := rateLimitMessage(resp(http.StatusForbidden, "0", ""), false); strings.Contains(got, "Try again") {
			t.Errorf("no reset header means no retry hint, got %q", got)
		}
	})
}

// The latest-release check honors GITHUB_TOKEN: it sends an Authorization
// header when the token is set (lifting the unauthenticated rate limit) and
// none when it is unset.
func TestGithubFetchLatestStableTagUsesGithubToken(t *testing.T) {
	const slug = "doordash-oss/agentic-orchestrator"
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{{"tag_name": "v1.0.0"}})
	}))
	t.Cleanup(srv.Close)

	t.Run("sends Bearer token when GITHUB_TOKEN is set", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "secret-token")
		gotAuth = "unset"
		fetch := githubFetchLatestStableTag(srv.Client(), srv.URL)
		if _, err := fetch(context.Background(), slug); err != nil {
			t.Fatalf("fetch: %v", err)
		}
		if gotAuth != "Bearer secret-token" {
			t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer secret-token")
		}
	})
	t.Run("no Authorization header without GITHUB_TOKEN", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		gotAuth = "unset"
		fetch := githubFetchLatestStableTag(srv.Client(), srv.URL)
		if _, err := fetch(context.Background(), slug); err != nil {
			t.Fatalf("fetch: %v", err)
		}
		if gotAuth != "" {
			t.Errorf("Authorization = %q, want empty without GITHUB_TOKEN", gotAuth)
		}
	})
}
