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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
				func(string, string, bool, []string) { launchCalled = true },
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
		func(string, string, bool, []string) { launchCalled = true },
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

func TestRunUpdateWithBareNamesDetectedMethod(t *testing.T) {
	for _, tt := range []struct {
		name      string
		method    installMethod
		wantLabel string
	}{
		{name: "go-install", method: installMethodGoInstall, wantLabel: "go install"},
		{name: "tarball", method: installMethodTarball, wantLabel: "release tarball"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runUpdateWith(context.Background(), false, &stdout, &stderr, updateDeps{
				currentVersion: "1.2.3",
				method:         fixedMethod(tt.method),
				slug:           okSlug,
				fetchLatest:    fakeFetch("v2.0.0", nil),
			})
			if code != 1 {
				t.Fatalf("code = %d, want 1", code)
			}
			if !strings.Contains(stdout.String(), "→") {
				t.Errorf("stdout missing old → new skeleton\nfull:\n%s", stdout.String())
			}
			errOut := stderr.String()
			if !strings.Contains(errOut, updateNotImplementedMsg) {
				t.Errorf("stderr missing not-yet-wired stub\nfull:\n%s", errOut)
			}
			if !strings.Contains(errOut, tt.wantLabel) {
				t.Errorf("stderr must name the detected method %q\nfull:\n%s", tt.wantLabel, errOut)
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
