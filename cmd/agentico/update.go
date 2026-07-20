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
	"os/exec"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/buildinfo"
)

const (
	// githubAPIBaseURL is the GitHub REST API root for the unauthenticated
	// release lookup. The default transport honors standard proxy env vars.
	githubAPIBaseURL = "https://api.github.com"
	// updateCheckTimeout bounds the single outbound GitHub call.
	updateCheckTimeout = 15 * time.Second
	// desktopBridgeTimeout bounds the desktop handoff command. It never waits
	// for update work, only for the OS launcher to accept or reject the route.
	desktopBridgeTimeout = 10 * time.Second

	updateCheckingNarrative = "Checking for updates..."
	updateDesktopOpenedMsg  = "Opened Agentico Settings > Updates. Desktop updates are managed in the app."
)

// errNoStableRelease signals that a repository has no published non-draft,
// non-prerelease release with a clean vX.Y.Z tag.
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

// desktopBridge focuses or launches the registered desktop app into Settings.
// It is intentionally narrow: no download, package-manager, or filesystem
// mutation capability is available through this seam.
type desktopBridge func(ctx context.Context) error

// updateDeps bundles the injectable dependencies of the update bridge.
type updateDeps struct {
	currentVersion string
	method         func() installMethod
	slug           func() (string, error)
	fetchLatest    func(ctx context.Context, slug string) (string, error)
	openDesktop    desktopBridge
}

// runUpdate is the production updater seam wired into the router. Bare update
// first tries to hand off to the desktop Settings > Updates surface. If no
// desktop registration is available, and for --check always, it performs only a
// bounded stable-release metadata lookup and prints install-method-aware next
// steps. It never downloads, overwrites, renames, swaps, re-signs, or invokes a
// package manager.
func runUpdate(checkOnly bool, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	defer cancel()
	return runUpdateWith(ctx, checkOnly, stdout, stderr, updateDeps{
		currentVersion: buildinfo.Version(),
		method:         gatherInstallMethod,
		slug:           moduleSlug,
		fetchLatest:    githubFetchLatestStableTag(http.DefaultClient, githubBaseURL()),
		openDesktop:    openRegisteredDesktopUpdates,
	})
}

// runUpdateWith implements the --check / bare-update behavior against injected
// dependencies and returns the process exit code.
func runUpdateWith(ctx context.Context, checkOnly bool, stdout, stderr io.Writer, deps updateDeps) int {
	method := deps.method()

	if !checkOnly {
		bridgeCtx, cancel := context.WithTimeout(context.Background(), desktopBridgeTimeout)
		defer cancel()
		if deps.openDesktop != nil && os.Getenv("AGENTICO_UPDATE_DISABLE_DESKTOP_BRIDGE") != "1" {
			if err := deps.openDesktop(bridgeCtx); err == nil {
				fmt.Fprintln(stdout, updateDesktopOpenedMsg)
				return 0
			}
		}
		if method == installMethodDevBuild {
			fmt.Fprintf(stdout, "Install method:  %s\n", method.label())
			fmt.Fprintln(stdout, method.wouldDoAction())
			return 0
		}
	}

	fmt.Fprintln(stdout, updateCheckingNarrative)

	slug, err := deps.slug()
	if err != nil {
		fmt.Fprintf(stderr, "Error: could not determine the release repository: %v\n", err)
		return 1
	}

	latest, err := deps.fetchLatest(ctx, slug)
	if err != nil {
		if errors.Is(err, errNoStableRelease) {
			fmt.Fprintln(stderr, "Error: no published stable release is available.")
			return 1
		}
		fmt.Fprintf(stderr, "Error: could not check for the latest release: %v\n", err)
		return 1
	}

	printReadOnlyUpdateReport(stdout, deps.currentVersion, latest, method)
	return 0
}

func printReadOnlyUpdateReport(stdout io.Writer, current, latest string, method installMethod) {
	if cmp, ordered := compareReleaseVersions(current, latest); sameVersion(current, latest) || (ordered && cmp >= 0) {
		if ordered && cmp > 0 {
			fmt.Fprintf(stdout, "agentico is already newer than the latest stable release (current %s, latest %s).\n",
				normalizeVersion(current), normalizeVersion(latest))
		} else {
			fmt.Fprintf(stdout, "agentico is already up to date (version %s).\n", normalizeVersion(current))
		}
	} else {
		fmt.Fprintf(stdout, "Current version: %s\n", normalizeVersion(current))
		fmt.Fprintf(stdout, "Latest version:  %s\n", normalizeVersion(latest))
	}
	fmt.Fprintf(stdout, "Install method:  %s\n", method.label())
	fmt.Fprintln(stdout, method.wouldDoAction())
}

// openRegisteredDesktopUpdates asks the OS to open the registered Agentico
// desktop app. Electron's single-instance lock focuses an existing app; a cold
// launch receives the same route.
func openRegisteredDesktopUpdates(ctx context.Context) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.CommandContext(ctx, "open", "-b", "com.doordash.agentico", "--args", "--agentico-route=updates").Run()
	case "linux":
		return exec.CommandContext(ctx, "xdg-open", "agentico://updates").Run()
	default:
		return fmt.Errorf("desktop bridge unsupported on %s", runtime.GOOS)
	}
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

// githubRelease is the subset of the GitHub release object this command reads.
type githubRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// githubBaseURL returns the GitHub REST API root, honoring the standard
// GITHUB_API_URL environment variable and falling back to the public API.
func githubBaseURL() string {
	if v := strings.TrimSpace(os.Getenv("GITHUB_API_URL")); v != "" {
		return v
	}
	return githubAPIBaseURL
}

func listReleases(ctx context.Context, client *http.Client, baseURL, slug string, authenticated bool) ([]githubRelease, error) {
	url := fmt.Sprintf("%s/repos/%s/releases", strings.TrimRight(baseURL, "/"), slug)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if authenticated {
		authorizeGitHub(req)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("requesting releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if msg := rateLimitMessage(resp, githubToken() != ""); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, fmt.Errorf("github returned status %s", resp.Status)
	}

	var releases []githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return nil, fmt.Errorf("decoding releases: %w", err)
	}
	return releases, nil
}

// githubFetchLatestStableTag returns a fetcher that lists a repository's
// releases and yields the first published stable SemVer tag. Drafts,
// prereleases, and malformed tags are ignored so --check never advertises an
// update the desktop feed would reject.
func githubFetchLatestStableTag(client *http.Client, baseURL string) func(ctx context.Context, slug string) (string, error) {
	return func(ctx context.Context, slug string) (string, error) {
		releases, err := listReleases(ctx, client, baseURL, slug, true)
		if err != nil {
			return "", err
		}
		for _, rel := range releases {
			if rel.Draft || rel.Prerelease {
				continue
			}
			tag := strings.TrimSpace(rel.TagName)
			if tag != "" && isReleaseVersion(tag) {
				return tag, nil
			}
		}
		return "", errNoStableRelease
	}
}

// rateLimitMessage returns a clear, actionable error message when resp is a
// GitHub primary rate-limit rejection.
func rateLimitMessage(resp *http.Response, tokenSet bool) string {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return ""
	}
	if resp.Header.Get("X-RateLimit-Remaining") != "0" {
		return ""
	}
	var when string
	if reset := resp.Header.Get("X-RateLimit-Reset"); reset != "" {
		if secs, err := strconv.ParseInt(reset, 10, 64); err == nil {
			when = fmt.Sprintf(" Try again after %s.", time.Unix(secs, 0).Format("15:04 MST"))
		}
	}
	if tokenSet {
		return "GitHub API rate limit exceeded for the authenticated token." + when
	}
	return "GitHub API rate limit exceeded (unauthenticated requests are capped at 60/hour per IP). " +
		"Set GITHUB_TOKEN to a personal access token to raise the limit to 5000/hour." + when
}

// githubToken returns the GITHUB_TOKEN environment value, trimmed, or "".
func githubToken() string {
	return strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
}

// authorizeGitHub adds an Authorization: Bearer header when a GITHUB_TOKEN is
// set so private/Enterprise repositories and higher rate limits work for the
// metadata-only check.
func authorizeGitHub(req *http.Request) {
	if tok := githubToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
}
