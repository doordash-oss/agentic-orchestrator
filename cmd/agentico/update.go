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
	// updateInstallTimeout bounds the `go install` subprocess. It is far more
	// generous than the check timeout because a cold module cache must download
	// and compile the whole dependency graph, but it is still bounded so a
	// stalled install cannot hang forever. It is rooted at a fresh context, not
	// derived from the 15s check context, so the install gets its full budget.
	updateInstallTimeout = 5 * time.Minute

	// User-facing narrative strings.
	updateCheckingNarrative = "Checking for updates…"
	updateNotImplementedMsg = "Update execution is not yet implemented."

	// updateGoToolchainMissingMsg is printed to stderr when the go-install
	// branch cannot find the Go toolchain on PATH. The existing binary is left
	// untouched.
	updateGoToolchainMissingMsg = "Error: the Go toolchain was not found on PATH. " +
		"Install Go or add it to PATH, then run `agentico update` again."

	// updateFromSourceGuidance is printed to stderr when a development build
	// refuses to self-update. It points the user back at their source checkout
	// rather than touching the binary or the network.
	updateFromSourceGuidance = "agentico was built from source; development builds are not updated automatically.\n" +
		"Update from source instead: pull the latest changes and rebuild (for example, `git pull` then `make install`)."
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

// commandRunner runs an external command and returns its combined stdout+stderr
// output. It is a function value mirroring the repo's CommandRunner convention,
// injected so the go-install subprocess is faked in unit tests and no real
// `go install` ever runs in the unit suite.
type commandRunner func(ctx context.Context, name string, args, env []string) ([]byte, error)

// updateDeps bundles the injectable dependencies of the update flow so the
// resolution logic can be exercised hermetically: tests supply a controllable
// install-method detector, slug, an httptest-backed (or canned) latest-release
// fetcher, and — for the go-install execution branch — a main-package resolver,
// a running-binary-directory resolver, and a command runner.
type updateDeps struct {
	currentVersion string
	// method detects how the running binary was installed. It is consulted
	// before any network call so a development build can be refused offline.
	method      func() installMethod
	slug        func() (string, error)
	fetchLatest func(ctx context.Context, slug string) (string, error)

	// mainPackage derives the command's own main-package import path from build
	// info at runtime (the package to `go install`, not the bare module path).
	mainPackage func() (string, error)
	// binaryDir resolves the symlink-resolved directory of the running binary so
	// the re-install can force GOBIN there and replace the binary in place.
	binaryDir func() string
	// runInstall is the injectable command-runner seam for the `go install`
	// subprocess.
	runInstall commandRunner
}

// runUpdate is the production updater seam wired into the router. It resolves
// the current version via the existing accessor and the latest stable release
// via the first outbound GitHub call, then prints the result.
func runUpdate(checkOnly bool, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	defer cancel()
	return runUpdateWith(ctx, checkOnly, stdout, stderr, updateDeps{
		currentVersion: tui.GetVersion(),
		method:         gatherInstallMethod,
		slug:           moduleSlug,
		fetchLatest:    githubFetchLatestStableTag(http.DefaultClient, githubBaseURL()),
		mainPackage:    mainPackagePath,
		binaryDir:      resolveBinaryDir,
		runInstall:     execGoInstall,
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
	// Classify the install method before any network call. A development build
	// is refused entirely offline — no GitHub request, no binary touched.
	method := deps.method()
	if method == installMethodDevBuild {
		return reportDevBuild(checkOnly, stdout, stderr, deps.currentVersion)
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
		fmt.Fprintf(stdout, "Install method:  %s\n", method.label())
		fmt.Fprintln(stdout, method.wouldDoAction())
		return 0
	}

	// Bare `update` with an update available: dispatch on the detected install
	// method. The go-install branch performs a real in-place re-install; the
	// tarball branch is still the not-yet-implemented placeholder (Phase 4).
	if method == installMethodGoInstall {
		return performGoInstall(stdout, stderr, deps, current, latest)
	}

	// Print the conventional old → new skeleton to stdout, then report that
	// execution for the detected method is not yet wired on stderr and exit
	// non-zero. Naming the method proves the classifier ran on this branch.
	fmt.Fprintf(stdout, "Update available: %s → %s\n", normalizeVersion(current), normalizeVersion(latest))
	fmt.Fprintf(stderr, "%s (detected install method: %s)\n", updateNotImplementedMsg, method.label())
	return 1
}

// performGoInstall re-runs the Go toolchain install of the command's own main
// package, pinned to the resolved latest stable tag, so the toolchain-managed
// binary is replaced in place. It is reached only in bare (non --check) mode
// when the detected install method is go-install and an update is available.
//
// The subprocess runs under its own generous, bounded context (distinct from
// the 15s check timeout) through the injectable runner seam, with an
// inherited-plus-augmented environment that forces GOBIN to the running
// binary's resolved directory so the binary that is actually running is the one
// replaced — no stray second copy in the default Go bin dir. On success it
// prints the old → new transition to stdout and returns 0. Every failure is
// surfaced to stderr with a non-zero exit and no success transition; a failed
// or partial `go install` does not replace the running binary.
func performGoInstall(stdout, stderr io.Writer, deps updateDeps, current, latest string) int {
	pkg, err := deps.mainPackage()
	if err != nil {
		fmt.Fprintf(stderr, "Error: could not determine the package to install: %v\n", err)
		return 1
	}

	// Pin to the exact resolved tag (not @latest): the report is truthful by
	// construction and the install fails loudly if the proxy has not indexed the
	// tag rather than silently installing an older version.
	args := []string{"install", pkg + "@" + latest}

	ctx, cancel := context.WithTimeout(context.Background(), updateInstallTimeout)
	defer cancel()

	out, err := deps.runInstall(ctx, "go", args, goInstallEnv(deps.binaryDir))
	if err != nil {
		return reportGoInstallFailure(ctx, stderr, err, out)
	}

	fmt.Fprintf(stdout, "Updated agentico %s → %s\n", normalizeVersion(current), normalizeVersion(latest))
	return 0
}

// goInstallEnv returns the environment for the `go install` subprocess: the
// inherited process environment augmented so GOBIN points at the running
// binary's resolved directory, guaranteeing the re-install replaces the binary
// in place. os/exec honors the last value for a duplicated key, so appending
// overrides any inherited GOBIN. When the binary's directory cannot be resolved
// (resolver nil or empty), GOBIN is left untouched and the toolchain default
// applies.
func goInstallEnv(binaryDir func() string) []string {
	env := os.Environ()
	if binaryDir == nil {
		return env
	}
	if dir := binaryDir(); dir != "" {
		env = append(env, "GOBIN="+dir)
	}
	return env
}

// reportGoInstallFailure maps a failed `go install` to a clear stderr message
// and a non-zero exit, distinguishing a missing toolchain, a timeout, and a
// generic install error. The captured combined output is included for the
// timeout and generic cases so the user sees what the toolchain reported.
func reportGoInstallFailure(ctx context.Context, stderr io.Writer, err error, out []byte) int {
	switch {
	case errors.Is(err, exec.ErrNotFound):
		fmt.Fprintln(stderr, updateGoToolchainMissingMsg)
	case errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded:
		fmt.Fprintf(stderr, "Error: the Go install timed out after %s and was aborted; the existing binary is unchanged.\n", updateInstallTimeout)
		writeCapturedOutput(stderr, out)
	default:
		fmt.Fprintf(stderr, "Error: `go install` failed: %v\n", err)
		writeCapturedOutput(stderr, out)
	}
	return 1
}

// writeCapturedOutput appends the trimmed combined output of the subprocess to
// stderr under a label, when there is any.
func writeCapturedOutput(stderr io.Writer, out []byte) {
	if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
		fmt.Fprintf(stderr, "go install output:\n%s\n", trimmed)
	}
}

// execGoInstall is the production command-runner seam: it runs the command
// under ctx with the augmented environment and returns the combined
// stdout+stderr output. Tests swap in a fake so no real install runs.
func execGoInstall(ctx context.Context, name string, args, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	return cmd.CombinedOutput()
}

// mainPackagePath derives the command's own main-package import path from the
// binary's build info at runtime — the package path (module path plus the
// command subdirectory, e.g.
// github.com/doordash-oss/agentic-orchestrator/cmd/agentico), not the bare
// module path, which has no main package and so cannot be `go install`ed. It
// mirrors moduleSlug's runtime derivation.
func mainPackagePath() (string, error) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", errors.New("build info unavailable; cannot determine the package to install")
	}
	path := strings.TrimSpace(info.Path)
	if path == "" || path == "command-line-arguments" {
		return "", errors.New("build info has no main package path; cannot determine the package to install")
	}
	return path, nil
}

// reportDevBuild handles the development-build case for both modes without any
// network call or binary mutation. In --check mode it reports the current
// version, the detected method, and the would-do action to stdout and exits 0
// (a non-destructive report). In bare mode it prints update-from-source
// guidance to stderr and exits non-zero (the refusal — the first real action
// the update command takes).
func reportDevBuild(checkOnly bool, stdout, stderr io.Writer, current string) int {
	if checkOnly {
		fmt.Fprintf(stdout, "Current version: %s\n", normalizeVersion(current))
		fmt.Fprintf(stdout, "Install method:  %s\n", installMethodDevBuild.label())
		fmt.Fprintln(stdout, installMethodDevBuild.wouldDoAction())
		return 0
	}
	fmt.Fprintln(stderr, updateFromSourceGuidance)
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
