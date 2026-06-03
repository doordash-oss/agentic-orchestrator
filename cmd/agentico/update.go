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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/tui"
)

const (
	// githubAPIBaseURL is the GitHub REST API root for the unauthenticated
	// release lookup. The default transport honors standard proxy env vars.
	githubAPIBaseURL = "https://api.github.com"
	// updateCheckTimeout bounds the single outbound GitHub call.
	updateCheckTimeout = 15 * time.Second

	// User-facing narrative strings. The placeholder action line is a Phase 1
	// stand-in; real install-method classification lands in Phase 2.
	updateCheckingNarrative = "Checking for updates…"
	updatePlaceholderAction = "Install method: not yet detected — automatic install is coming in a future release. Re-run your original install command to upgrade for now."
	updateNotImplementedMsg = "Update execution is not yet implemented."
)

// errNoStableRelease signals that a repository has no published non-draft,
// non-pre-release release to update to. Callers map it to a clean message and
// a non-zero exit rather than a hard failure.
var errNoStableRelease = errors.New("no stable release found")

// parseUpdateArgs parses the arguments that follow the `update` subcommand.
// Only --check / -n are accepted; any other flag or extra positional token is
// rejected so the surface stays minimal and unambiguous.
func parseUpdateArgs(opts launchOptions, rest []string) (launchOptions, error) {
	opts.mode = launchModeUpdate
	for _, arg := range rest {
		switch arg {
		case "--check", "-n":
			opts.updateCheck = true
		default:
			if strings.HasPrefix(arg, "-") {
				return opts, fmt.Errorf("unknown flag: %s", arg)
			}
			return opts, fmt.Errorf("unexpected argument: %s", arg)
		}
	}
	return opts, nil
}

// updateDeps bundles the injectable dependencies of the update flow so the
// resolution logic can be exercised hermetically: tests supply a controllable
// slug and an httptest-backed (or canned) latest-release fetcher.
type updateDeps struct {
	currentVersion string
	slug           func() (string, error)
	fetchLatest    func(ctx context.Context, slug string) (string, error)
}

// runUpdate is the production updater seam wired into the router. It resolves
// the current version via the existing accessor and the latest stable release
// via the first outbound GitHub call, then prints the result.
func runUpdate(checkOnly bool, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	defer cancel()
	return runUpdateWith(ctx, checkOnly, stdout, stderr, updateDeps{
		currentVersion: tui.GetVersion(),
		slug:           moduleSlug,
		fetchLatest:    githubFetchLatestStableTag(http.DefaultClient, githubBaseURL()),
	})
}

// githubBaseURL returns the GitHub REST API root, honoring the standard
// GITHUB_API_URL environment variable (set by GitHub Actions and used to point
// at GitHub Enterprise) and falling back to the public API. The value is still
// reached over the default transport, which honors standard proxy env vars.
func githubBaseURL() string {
	if v := strings.TrimSpace(os.Getenv("GITHUB_API_URL")); v != "" {
		return v
	}
	return githubAPIBaseURL
}

// runUpdateWith implements the --check / bare-update behavior against injected
// dependencies and returns the process exit code.
func runUpdateWith(ctx context.Context, checkOnly bool, stdout, stderr io.Writer, deps updateDeps) int {
	fmt.Fprintln(stdout, updateCheckingNarrative)

	slug, err := deps.slug()
	if err != nil {
		fmt.Fprintf(stderr, "Error: could not determine the release repository: %v\n", err)
		return 1
	}

	latest, err := deps.fetchLatest(ctx, slug)
	if err != nil {
		if errors.Is(err, errNoStableRelease) {
			fmt.Fprintln(stderr, "Error: no published stable release is available to update to.")
			return 1
		}
		fmt.Fprintf(stderr, "Error: could not check for the latest release: %v\n", err)
		return 1
	}

	current := deps.currentVersion
	if sameVersion(current, latest) {
		fmt.Fprintf(stdout, "agentico is already up to date (version %s).\n", normalizeVersion(current))
		return 0
	}

	if checkOnly {
		fmt.Fprintf(stdout, "Current version: %s\n", normalizeVersion(current))
		fmt.Fprintf(stdout, "Latest version:  %s\n", normalizeVersion(latest))
		fmt.Fprintln(stdout, updatePlaceholderAction)
		return 0
	}

	// Bare `update` with an update available: print the conventional old → new
	// skeleton to stdout, then report that execution is not yet wired on
	// stderr and exit non-zero (real execution lands in Phases 2-4).
	fmt.Fprintf(stdout, "Update available: %s → %s\n", normalizeVersion(current), normalizeVersion(latest))
	fmt.Fprintln(stderr, updateNotImplementedMsg)
	return 1
}

// normalizeVersion trims surrounding whitespace and a single leading "v" so
// ldflags/build-info versions and GitHub tags compare on equal footing.
func normalizeVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// sameVersion reports whether two version strings are equal after
// normalization. A "dev" current never equals a release tag.
func sameVersion(a, b string) bool {
	return normalizeVersion(a) == normalizeVersion(b)
}

// moduleSlug derives the owner/repo GitHub slug from the binary's module path
// at runtime (e.g. github.com/doordash-oss/agentic-orchestrator →
// doordash-oss/agentic-orchestrator) rather than hardcoding it.
func moduleSlug() (string, error) {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Path == "" {
		return "", errors.New("build info unavailable; cannot determine module path")
	}
	return slugFromModulePath(info.Main.Path)
}

// slugFromModulePath extracts the owner/repo slug from a github.com module
// path, ignoring any sub-package suffix.
func slugFromModulePath(path string) (string, error) {
	const host = "github.com/"
	if !strings.HasPrefix(path, host) {
		return "", fmt.Errorf("module path %q is not a github.com repository", path)
	}
	parts := strings.Split(strings.TrimPrefix(path, host), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("module path %q has no owner/repo slug", path)
	}
	return parts[0] + "/" + parts[1], nil
}

// githubRelease is the subset of the GitHub release object this phase reads.
type githubRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// githubFetchLatestStableTag returns a fetcher that lists a repository's
// releases and yields the tag of the most recent non-draft, non-pre-release
// entry. The base URL and HTTP client are injected so the resolution logic can
// be unit-tested against an in-process httptest server. The call is
// unauthenticated; GITHUB_TOKEN consumption is deferred to a later phase.
func githubFetchLatestStableTag(client *http.Client, baseURL string) func(ctx context.Context, slug string) (string, error) {
	return func(ctx context.Context, slug string) (string, error) {
		url := fmt.Sprintf("%s/repos/%s/releases", strings.TrimRight(baseURL, "/"), slug)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", fmt.Errorf("building release request: %w", err)
		}
		req.Header.Set("Accept", "application/vnd.github+json")

		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("requesting latest release: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("github returned status %s", resp.Status)
		}

		var releases []githubRelease
		if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
			return "", fmt.Errorf("decoding releases: %w", err)
		}

		for _, rel := range releases {
			if rel.Draft || rel.Prerelease {
				continue
			}
			if tag := strings.TrimSpace(rel.TagName); tag != "" {
				return tag, nil
			}
		}
		return "", errNoStableRelease
	}
}
