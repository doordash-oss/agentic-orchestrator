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
	"os"
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
	return func(string, string, bool, []string, bool, string, string) int {
		t.Fatal("server seam invoked on a non-server launch path")
		return 1
	}
}

func TestParseLaunchArgsUpdateSurface(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantMode  launchMode
		wantCheck bool
		wantErr   string
	}{
		{name: "bare update", args: []string{"update"}, wantMode: launchModeUpdate},
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
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("parseLaunchArgs(%v) error = %v, want %q", tt.args, err, tt.wantErr)
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

func TestRunArgsDispatchesUpdateToSeam(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantCheck bool
	}{
		{name: "bare update", args: []string{"update"}},
		{name: "update --check", args: []string{"update", "--check"}, wantCheck: true},
		{name: "update -n", args: []string{"update", "-n"}, wantCheck: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			var gotCheck, updateCalled, launchCalled bool
			code := runArgs(tt.args, &stdout, &stderr,
				func(string, string, bool, []string, bool, string, string) int {
					launchCalled = true
					return 0
				},
				func(checkOnly bool, _, _ io.Writer) int {
					updateCalled = true
					gotCheck = checkOnly
					return 7
				},
			)
			if !updateCalled {
				t.Fatal("update seam was not invoked")
			}
			if launchCalled {
				t.Fatal("server launcher was invoked on the update path")
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

func TestRunArgsDefaultLaunchesDesktopNotServerOrUpdate(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var desktopCalled, serverCalled, updateCalled bool
	code := runArgsWithDesktop(nil, &stdout, &stderr,
		func() error { desktopCalled = true; return nil },
		func(string, string, bool, []string, bool, string, string) int {
			serverCalled = true
			return 0
		},
		func(bool, io.Writer, io.Writer) int { updateCalled = true; return 1 },
	)
	if !desktopCalled {
		t.Fatal("default mode must launch the desktop app")
	}
	if serverCalled {
		t.Fatal("default mode must not launch the server")
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

func TestNormalizeVersionAndSlug(t *testing.T) {
	if got := normalizeVersion(" v1.2.3 "); got != "1.2.3" {
		t.Errorf("normalizeVersion() = %q, want 1.2.3", got)
	}
	if !sameVersion("1.2.3", "v1.2.3") {
		t.Error("1.2.3 and v1.2.3 should be equal after normalization")
	}
	tests := []struct {
		path    string
		want    string
		wantErr bool
	}{
		{path: "github.com/doordash-oss/agentic-orchestrator", want: "doordash-oss/agentic-orchestrator"},
		{path: "github.com/doordash-oss/agentic-orchestrator/cmd/agentico", want: "doordash-oss/agentic-orchestrator"},
		{path: "example.com/foo/bar", wantErr: true},
		{path: "github.com/onlyowner", wantErr: true},
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
			if err != nil || got != tt.want {
				t.Fatalf("slugFromModulePath(%q) = %q,%v want %q,nil", tt.path, got, err, tt.want)
			}
		})
	}
}

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

	t.Run("returns latest stable semver tag", func(t *testing.T) {
		srv := releasesServer(t, slug, http.StatusOK, []map[string]any{
			{"tag_name": "v2.0.0"},
			{"tag_name": "v1.0.0"},
		})
		fetch := githubFetchLatestStableTag(srv.Client(), srv.URL)
		got, err := fetch(context.Background(), slug)
		if err != nil || got != "v2.0.0" {
			t.Fatalf("fetch() = %q,%v want v2.0.0,nil", got, err)
		}
	})

	t.Run("skips drafts prereleases and malformed stable entries", func(t *testing.T) {
		srv := releasesServer(t, slug, http.StatusOK, []map[string]any{
			{"tag_name": "v3.0.0-rc1", "prerelease": true},
			{"tag_name": "v2.5.0", "draft": true},
			{"tag_name": "latest"},
			{"tag_name": "v2.0.0"},
		})
		fetch := githubFetchLatestStableTag(srv.Client(), srv.URL)
		got, err := fetch(context.Background(), slug)
		if err != nil || got != "v2.0.0" {
			t.Fatalf("fetch() = %q,%v want v2.0.0,nil", got, err)
		}
	})

	t.Run("no stable release", func(t *testing.T) {
		srv := releasesServer(t, slug, http.StatusOK, []map[string]any{
			{"tag_name": "v3.0.0-rc1", "prerelease": true},
			{"tag_name": "latest"},
		})
		fetch := githubFetchLatestStableTag(srv.Client(), srv.URL)
		_, err := fetch(context.Background(), slug)
		if !errors.Is(err, errNoStableRelease) {
			t.Fatalf("error = %v, want errNoStableRelease", err)
		}
	})

	t.Run("non-200 response", func(t *testing.T) {
		srv := releasesServer(t, slug, http.StatusInternalServerError, nil)
		fetch := githubFetchLatestStableTag(srv.Client(), srv.URL)
		if _, err := fetch(context.Background(), slug); err == nil {
			t.Fatal("expected error on non-200 response")
		}
	})
}

func fakeFetch(tag string, err error) func(context.Context, string) (string, error) {
	return func(context.Context, string) (string, error) { return tag, err }
}

func okSlug() (string, error) { return "doordash-oss/agentic-orchestrator", nil }

func fixedMethod(m installMethod) func() installMethod {
	return func() installMethod { return m }
}

func TestRunUpdateWithOpensDesktopOnBareUpdate(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var opened bool
	code := runUpdateWith(context.Background(), false, &stdout, &stderr, updateDeps{
		currentVersion: "1.0.0",
		method:         fixedMethod(installMethodTarball),
		slug:           func() (string, error) { t.Fatal("slug must not run after desktop handoff"); return "", nil },
		fetchLatest:    fakeFetch("", nil),
		openDesktop: func(context.Context) error {
			opened = true
			return nil
		},
	})
	if code != 0 || !opened {
		t.Fatalf("runUpdateWith() code=%d opened=%v, want success desktop handoff", code, opened)
	}
	if !strings.Contains(stdout.String(), "Settings > Updates") {
		t.Fatalf("stdout = %q, want desktop handoff message", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunUpdateWithBareFallbackPrintsGuidanceOnly(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runUpdateWith(context.Background(), false, &stdout, &stderr, updateDeps{
		currentVersion: "1.2.3",
		method:         fixedMethod(installMethodHomebrew),
		slug:           okSlug,
		fetchLatest:    fakeFetch("v2.0.0", nil),
		openDesktop:    func(context.Context) error { return errors.New("not registered") },
	})
	if code != 0 {
		t.Fatalf("code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"Checking for updates", "Current version", "Latest version", "homebrew", "brew update && brew upgrade agentico"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q\nfull:\n%s", want, out)
		}
	}
	for _, forbidden := range []string{"Updated agentico", "Downloading", "replacing the binary", "go install output"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("stdout contains mutating update language %q\nfull:\n%s", forbidden, out)
		}
	}
}

func TestRunUpdateWithBareDevBuildGuidanceIsOffline(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runUpdateWith(context.Background(), false, &stdout, &stderr, updateDeps{
		currentVersion: "dev",
		method:         fixedMethod(installMethodDevBuild),
		slug:           func() (string, error) { t.Fatal("dev-build bare guidance must not resolve a slug"); return "", nil },
		fetchLatest: func(context.Context, string) (string, error) {
			t.Fatal("dev-build bare guidance must not check the network")
			return "", nil
		},
		openDesktop: func(context.Context) error { return errors.New("not registered") },
	})
	if code != 0 {
		t.Fatalf("code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "development build") || !strings.Contains(out, "Pull the source checkout and rebuild") {
		t.Fatalf("stdout = %q, want source guidance", out)
	}
	if strings.Contains(out, "Checking for updates") {
		t.Fatalf("stdout = %q, dev-build bare guidance must stay offline", out)
	}
}

func TestRunUpdateWithCheckNeverOpensDesktop(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runUpdateWith(context.Background(), true, &stdout, &stderr, updateDeps{
		currentVersion: "2.0.0",
		method:         fixedMethod(installMethodTarball),
		slug:           okSlug,
		fetchLatest:    fakeFetch("v2.0.0", nil),
		openDesktop: func(context.Context) error {
			t.Fatal("--check must not open the desktop app")
			return nil
		},
	})
	if code != 0 {
		t.Fatalf("code = %d, want 0\nstderr: %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "already up to date") || !strings.Contains(out, "GitHub Releases") {
		t.Fatalf("stdout = %q, want read-only status and guidance", out)
	}
}

func TestRunUpdateWithReportsLookupErrors(t *testing.T) {
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
	if !strings.HasPrefix(stderr.String(), "error[update_check_failed]: Update check failed\n") {
		t.Errorf("stderr = %q, want the update check failure heading", stderr.String())
	}
	if !strings.Contains(stderr.String(), "no published stable release") {
		t.Errorf("stderr = %q, want no stable release message", stderr.String())
	}
}

func TestRunUpdateWithReportsSlugAndFetchErrors(t *testing.T) {
	cases := []struct {
		name        string
		slug        func() (string, error)
		fetch       func(context.Context, string) (string, error)
		wantSummary string
	}{
		{
			name:        "slug resolution failure",
			slug:        func() (string, error) { return "", errors.New("build info unavailable") },
			fetch:       fakeFetch("", nil),
			wantSummary: "could not determine the release repository: build info unavailable",
		},
		{
			name:        "fetch failure",
			slug:        okSlug,
			fetch:       fakeFetch("", errors.New("network unreachable")),
			wantSummary: "could not check for the latest release: network unreachable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runUpdateWith(context.Background(), true, &stdout, &stderr, updateDeps{
				currentVersion: "1.2.3",
				method:         fixedMethod(installMethodTarball),
				slug:           tc.slug,
				fetchLatest:    tc.fetch,
			})
			if code != 1 {
				t.Fatalf("code = %d, want 1", code)
			}
			if !strings.HasPrefix(stderr.String(), "error[update_check_failed]: Update check failed\n") {
				t.Fatalf("stderr = %q, want the update check failure heading", stderr.String())
			}
			if !strings.Contains(stderr.String(), "  "+tc.wantSummary) {
				t.Fatalf("stderr = %q, want summary %q", stderr.String(), tc.wantSummary)
			}
		})
	}
}

func TestUpdateSourceContainsNoMutationMachinery(t *testing.T) {
	body, err := os.ReadFile("update.go")
	if err != nil {
		t.Fatalf("ReadFile(update.go): %v", err)
	}
	source := string(body)
	for _, forbidden := range []string{
		"swapBinary",
		"tarballUpdater",
		"runInstall",
		"runTarball",
		"runBrew",
		"codesign",
		"brew upgrade",
		"go install output",
		"archive/tar",
		"compress/gzip",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("update.go still contains self-replacement machinery %q", forbidden)
		}
	}
}
