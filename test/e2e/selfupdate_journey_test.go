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

package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/selfupdate"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
)

// The selfupdate journeys boot real, deliberately built agentico binaries as
// subprocesses: a tagged lower driver installed as the copy under test, a
// tagged higher driver as its update candidate, and one untagged production
// binary proving ordinary builds stay sealed. Every journey drives the real
// production server graph through a real unix exec; nothing here runs in
// short mode, in parallel, or on hosts without real exec semantics.

const (
	selfupdateVersionLower  = "v0.1.0-selfupdate-test"
	selfupdateVersionHigher = "v0.2.0-selfupdate-test"
	selfupdateVersionProd   = "v0.9.0-selfupdate-test"
)

// selfupdateBuildDir holds the once-built test binaries for the whole test
// binary run; TestMain creates and removes it.
var selfupdateBuildDir string

// TestMain owns the shared build directory so each deliberate binary is
// built at most once per test-binary run and cleaned up afterwards.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "agentico-selfupdate-e2e-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "selfupdate e2e: create build dir: %v\n", err)
		os.Exit(1)
	}
	selfupdateBuildDir = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// selfupdateBinaries carries the paths and digests of the deliberate builds.
type selfupdateBinaries struct {
	lower        string
	higher       string
	prod         string
	lowerDigest  string
	higherDigest string
	prodDigest   string
}

type selfupdateBuiltBinary struct {
	path, digest string
}

var (
	selfupdateBuildMu sync.Mutex
	selfupdateBuilds  = make(map[string]selfupdateBuiltBinary)
)

// selfupdateBinary shares native builds across all updater journeys.
func selfupdateBinary(t *testing.T, version string, tagged bool) selfupdateBuiltBinary {
	t.Helper()
	key := fmt.Sprintf("%s-%t", version, tagged)
	selfupdateBuildMu.Lock()
	defer selfupdateBuildMu.Unlock()
	if built, ok := selfupdateBuilds[key]; ok {
		return built
	}
	path := filepath.Join(selfupdateBuildDir, "agentico-"+key)
	selfupdateGoBuild(t, selfupdateRepoRoot(t), path, version, tagged)
	digest, err := selfupdate.DigestFile(path)
	if err != nil {
		t.Fatalf("digest %s: %v", path, err)
	}
	built := selfupdateBuiltBinary{path: path, digest: digest}
	selfupdateBuilds[key] = built
	return built
}

func selfupdateTestBinaries(t *testing.T) *selfupdateBinaries {
	t.Helper()
	lower := selfupdateBinary(t, selfupdateVersionLower, true)
	higher := selfupdateBinary(t, selfupdateVersionHigher, true)
	prod := selfupdateBinary(t, selfupdateVersionProd, false)
	return &selfupdateBinaries{lower: lower.path, higher: higher.path, prod: prod.path,
		lowerDigest: lower.digest, higherDigest: higher.digest, prodDigest: prod.digest}
}

// selfupdateRepoRoot resolves the repository root; test/e2e sits two levels
// below it, so the go test working directory is the anchor.
func selfupdateRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("%s is not the repo root (no go.mod): %v", root, err)
	}
	if _, err := os.Stat(filepath.Join(root, "cmd", "agentico")); err != nil {
		t.Fatalf("%s is not the repo root (no cmd/agentico): %v", root, err)
	}
	return root
}

// selfupdateGoBuild builds ./cmd/agentico into outPath with the given
// stamped version, optionally carrying the driver build tag.
func selfupdateGoBuild(t *testing.T, root, outPath, version string, tagged bool) {
	t.Helper()
	// VCS-derived module versions vary between tagged/shallow CI checkouts.
	// These fixtures model tarball releases using only the explicit version stamp.
	args := []string{"build", "-buildvcs=false"}
	if os.Getenv("AGENTICO_E2E_RACE") == "1" {
		args = append(args, "-race")
	}
	if tagged {
		args = append(args, "-tags", "agentico_selfupdate_driver")
	}
	args = append(args,
		"-ldflags", "-X github.com/doordash-oss/agentic-orchestrator/internal/buildinfo.version="+version,
		"-o", outPath,
		"./cmd/agentico")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", outPath, err, out)
	}
}

// selfupdateJourneyGuard skips journeys that need real process exec: short
// mode never boots a subprocess, and non-darwin/linux hosts have no real
// unix exec semantics to verify.
func selfupdateJourneyGuard(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("journey boots real process exec")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skipf("journey requires real unix exec; unsupported on %s", runtime.GOOS)
	}
}

// driverProcess owns one driver subprocess for its whole — possibly
// exec-replaced — lifetime. Stderr flows through a dedicated pipe the parent
// never closes early: the old image and its exec replacement write their
// milestones to the same descriptor, which is itself the stdio-continuity
// proof. Every process is reaped by a bounded cleanup that escalates to
// SIGKILL.
type driverProcess struct {
	cmd      *exec.Cmd
	scanDone chan struct{}

	mu       sync.Mutex
	lines    []string
	waited   bool
	exitCode int
}

// startDriverProcess launches bin with an explicit environment and collects
// its stderr lines for the process's whole (possibly exec-replaced) life.
func startDriverProcess(t *testing.T, bin string, env []string, args ...string) *driverProcess {
	t.Helper()
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	// A dedicated process group makes the child's PGID its own PID, so
	// journeys can prove process-group identity survives the exec
	// replacement. Termination still signals the process alone.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stderr = stderrW
	if err := cmd.Start(); err != nil {
		_ = stderrR.Close()
		_ = stderrW.Close()
		t.Fatalf("start %s %v: %v", bin, args, err)
	}
	// The child holds its own copy of the descriptor; dropping the parent's
	// lets the scanner observe EOF exactly when the process dies.
	_ = stderrW.Close()
	p := &driverProcess{cmd: cmd, scanDone: make(chan struct{})}
	go p.scanStderr(stderrR)
	t.Cleanup(func() { p.terminate() })
	return p
}

// scanStderr collects every stderr line into a thread-safe slice.
func (p *driverProcess) scanStderr(r *os.File) {
	defer close(p.scanDone)
	defer r.Close()
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		p.mu.Lock()
		p.lines = append(p.lines, line)
		p.mu.Unlock()
	}
}

// stderrLines returns a copy of the collected stderr lines.
func (p *driverProcess) stderrLines() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.lines))
	copy(out, p.lines)
	return out
}

// stderrContains reports whether any collected line contains sub.
func (p *driverProcess) stderrContains(sub string) bool {
	for _, line := range p.stderrLines() {
		if strings.Contains(line, sub) {
			return true
		}
	}
	return false
}

// stderrTail renders the last few stderr lines for failure diagnostics.
func (p *driverProcess) stderrTail() string {
	lines := p.stderrLines()
	if len(lines) > 12 {
		lines = lines[len(lines)-12:]
	}
	return strings.Join(lines, "\n")
}

// digestTokens returns the hex tokens of every "selfupdate-driver: <label>"
// fingerprint line, in arrival order (one per process image).
func (p *driverProcess) digestTokens(label string) []string {
	prefix := "selfupdate-driver: " + label + " "
	var tokens []string
	for _, line := range p.stderrLines() {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			tokens = append(tokens, strings.TrimSpace(rest))
		}
	}
	return tokens
}

// stderrPrefixCount counts the collected stderr lines carrying prefix: one
// "selfupdate-driver: exec /path" line marks one crossed exec boundary (the
// "exec failed:" line never matches).
func (p *driverProcess) stderrPrefixCount(prefix string) int {
	n := 0
	for _, line := range p.stderrLines() {
		if strings.HasPrefix(line, prefix) {
			n++
		}
	}
	return n
}

// alive reports whether the process is still running (signal 0 probe).
func (p *driverProcess) alive() bool {
	if p.cmd.Process == nil || p.cmd.ProcessState != nil {
		return false
	}
	return p.cmd.Process.Signal(syscall.Signal(0)) == nil
}

// pgid reports the process's current process-group id. The launched child
// owns its group (Setpgid at start), so an unchanged PGID across an exec
// replacement proves process-group identity preservation.
func (p *driverProcess) pgid() (int, error) {
	if p.cmd.Process == nil {
		return 0, errors.New("process not started")
	}
	return syscall.Getpgid(p.cmd.Process.Pid)
}

// exited reports whether the process already terminated: the stderr pipe
// EOFs exactly when the (possibly exec-replaced) process's last writer
// disappears, so this is a reliable death signal even before Wait.
func (p *driverProcess) exited() bool {
	select {
	case <-p.scanDone:
		return true
	default:
		return false
	}
}

// pid returns the process id; a successful exec preserves it.
func (p *driverProcess) pid() int {
	return p.cmd.Process.Pid
}

// waitBounded waits for process exit within timeout, escalating to SIGKILL,
// and drains the stderr scanner so late lines are collected first. Only the
// first call waits; later calls return the recorded exit code.
func (p *driverProcess) waitBounded(timeout time.Duration) int {
	p.mu.Lock()
	waited := p.waited
	p.mu.Unlock()
	if waited {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.exitCode
	}
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(timeout):
		_ = p.cmd.Process.Kill()
		<-done
	}
	select {
	case <-p.scanDone:
	case <-time.After(5 * time.Second):
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.waited = true
	p.exitCode = 0
	if p.cmd.ProcessState != nil {
		p.exitCode = p.cmd.ProcessState.ExitCode()
	}
	return p.exitCode
}

// terminate signals SIGTERM when the process still runs and returns the
// bounded exit code.
func (p *driverProcess) terminate() int {
	if p.alive() {
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
	}
	return p.waitBounded(15 * time.Second)
}

// selfupdateJourney owns one isolated journey fixture: a temp home for the
// central registry, an isolated runtime (config/state under one dir, so
// discovery and the auth token live beside it), an installed copy of the
// binary under test, and the trigger file.
type selfupdateJourney struct {
	t           *testing.T
	bins        *selfupdateBinaries
	home        string
	runtimeDir  string
	stateDir    string
	configPath  string
	installPath string
	triggerPath string
}

// newSelfupdateJourney builds the isolated fixture with an installed copy of
// sourceBin — never the repo's own build output path — under a temp dir.
func newSelfupdateJourney(t *testing.T, sourceBin string) *selfupdateJourney {
	t.Helper()
	bins := selfupdateTestBinaries(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	j := &selfupdateJourney{
		t:           t,
		bins:        bins,
		home:        filepath.Join(root, "home"),
		runtimeDir:  filepath.Join(root, "runtime"),
		stateDir:    filepath.Join(root, "runtime", "features"),
		configPath:  filepath.Join(root, "runtime", "config.yaml"),
		installPath: filepath.Join(root, "install", "agentico"),
		triggerPath: filepath.Join(root, "trigger"),
	}
	for _, dir := range []string{j.home, filepath.Dir(j.installPath)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
	}
	selfupdateCopyBinary(t, sourceBin, j.installPath)
	return j
}

// selfupdateCopyBinary copies a built binary to dst with executable
// permissions; the installed copy under test is never the build output.
func selfupdateCopyBinary(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open %s: %v", src, err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatalf("create %s: %v", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatalf("copy %s to %s: %v", src, dst, err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close %s: %v", dst, err)
	}
	if err := os.Chmod(dst, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", dst, err)
	}
}

// selfupdateStartOptions shapes one installed-driver launch.
type selfupdateStartOptions struct {
	listen      string
	failAt      string
	prepareOnly bool
	barrier     string
	barrierFile string
}

// start launches the installed driver copy with the journey's candidate and
// trigger file. The missing config file is deliberate: the server boots
// setup-capable without any provider CLI.
func (j *selfupdateJourney) start(opts selfupdateStartOptions) *driverProcess {
	args := []string{
		"selfupdate-driver",
		"--config", j.configPath,
		"--state-dir", j.stateDir,
		"--candidate", j.bins.higher,
		"--trigger-file", j.triggerPath}
	if opts.listen != "" {
		args = append(args, "--listen", opts.listen)
	}
	if opts.failAt != "" {
		args = append(args, "--fail-at", opts.failAt)
	}
	if opts.prepareOnly {
		args = append(args, "--prepare-only")
	}
	if opts.barrier != "" {
		args = append(args, "--barrier", opts.barrier, "--barrier-file", opts.barrierFile)
	}
	return startDriverProcess(j.t, j.installPath, selfupdateDriverEnv(j.home), args...)
}

// launch runs the installed binary with explicit arguments (secondary
// runtimes, competing launches, production-boundary probes).
func (j *selfupdateJourney) launch(args ...string) *driverProcess {
	return startDriverProcess(j.t, j.installPath, selfupdateDriverEnv(j.home), args...)
}

// launchEnv runs the installed binary with an explicit environment.
func (j *selfupdateJourney) launchEnv(env []string, args ...string) *driverProcess {
	return startDriverProcess(j.t, j.installPath, env, args...)
}

// selfupdateDriverEnv isolates a subprocess: only the temp HOME (the central
// registry resolves from it) plus a PATH passthrough.
func selfupdateDriverEnv(home string) []string {
	// A sanitized PATH keeps the journey hermetic: the developer machine's
	// real provider CLIs (claude/codex/opencode) must not be detected, or
	// startup blocks on live model discovery before the server ever
	// listens. With no providers on PATH the server boots setup-capable,
	// which is the deterministic journey shape.
	return []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
}

// selfupdateHTTPClient bounds every ordinary JSON probe.
var selfupdateHTTPClient = &http.Client{Timeout: time.Second}

// selfupdateStreamClient serves long-lived SSE requests; per-request
// contexts bound each stream instead of a client timeout.
var selfupdateStreamClient = &http.Client{}

// getHealth fetches and parses /api/v1/health (unauthenticated) once.
func (j *selfupdateJourney) getHealth(baseURL string) (server.HealthResponse, error) {
	var health server.HealthResponse
	resp, err := selfupdateHTTPClient.Get(baseURL + "/api/v1/health")
	if err != nil {
		return health, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return health, fmt.Errorf("health status = %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return health, err
	}
	return health, nil
}

// waitHealthy polls /api/v1/health until the server reports the wanted
// version, failing fast with the driver's stderr when the process dies
// before ever serving that version.
func (j *selfupdateJourney) waitHealthy(p *driverProcess, baseURL, version string, timeout time.Duration) server.HealthResponse {
	j.t.Helper()
	h, ok := j.observeHealthy(p, baseURL, version, timeout)
	if !ok {
		j.t.Fatalf("server at %s never became healthy as %q; driver stderr tail:\n%s", baseURL, version, p.stderrTail())
	}
	return h
}

// observeHealthy polls health without failing when the process exits first:
// the confirm injection, for one, aborts serving within milliseconds of the
// listener coming up, so a healthy observation is best-effort there.
func (j *selfupdateJourney) observeHealthy(p *driverProcess, baseURL, version string, timeout time.Duration) (server.HealthResponse, bool) {
	j.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		h, err := j.getHealth(baseURL)
		if err == nil && h.Status == "ok" && h.Owner.Version == version {
			return h, true
		}
		if p.exited() {
			return server.HealthResponse{}, false
		}
		if time.Now().After(deadline) {
			return server.HealthResponse{}, false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitDiscovery polls the runtime's discovery record until it is present.
func (j *selfupdateJourney) waitDiscovery(p *driverProcess, timeout time.Duration) server.DiscoveryRecord {
	j.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if rec, err := server.ReadDiscovery(j.runtimeDir); err == nil {
			return rec
		}
		if time.Now().After(deadline) {
			j.t.Fatalf("discovery record never appeared under %s; driver stderr tail:\n%s", j.runtimeDir, p.stderrTail())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitDiscoveryEpochChange polls until the discovery record carries a new
// non-empty epoch: the replacement image republishes discovery before it
// serves on.
func (j *selfupdateJourney) waitDiscoveryEpochChange(p *driverProcess, oldEpoch string, timeout time.Duration) server.DiscoveryRecord {
	j.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if rec, err := server.ReadDiscovery(j.runtimeDir); err == nil && rec.Epoch != "" && rec.Epoch != oldEpoch {
			return rec
		}
		if time.Now().After(deadline) {
			j.t.Fatalf("discovery epoch never changed from %q; driver stderr tail:\n%s", oldEpoch, p.stderrTail())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// featuresStatus performs the authenticated features list probe.
func (j *selfupdateJourney) featuresStatus(baseURL, token string) int {
	j.t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/api/v1/features", nil)
	if err != nil {
		j.t.Fatalf("new features request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := selfupdateHTTPClient.Do(req)
	if err != nil {
		j.t.Fatalf("features request: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// receiptPath is the durable receipt location for the installed copy.
func (j *selfupdateJourney) receiptPath() string {
	return selfupdate.ReceiptPath(j.installPath)
}

// waitUntil bounds observations of asynchronous process and filesystem state.
func waitUntil(t *testing.T, timeout time.Duration, description string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (p *driverProcess) waitStderr(t *testing.T, text string) {
	t.Helper()
	waitUntil(t, 10*time.Second, "stderr containing "+text, func() bool { return p.stderrContains(text) })
}

func (j *selfupdateJourney) waitForReceipt(timeout time.Duration, description string, match func(selfupdate.Receipt) bool) selfupdate.Receipt {
	j.t.Helper()
	var receipt selfupdate.Receipt
	waitUntil(j.t, timeout, description+" at "+j.receiptPath(), func() bool {
		r, err := selfupdate.ReadReceipt(j.receiptPath())
		if err != nil || !match(r) {
			return false
		}
		receipt = r
		return true
	})
	return receipt
}

func (j *selfupdateJourney) waitReceipt(timeout time.Duration) selfupdate.Receipt {
	j.t.Helper()
	return j.waitForReceipt(timeout, "receipt", func(selfupdate.Receipt) bool { return true })
}

func (j *selfupdateJourney) waitReceiptOutcome(want selfupdate.Outcome, timeout time.Duration) selfupdate.Receipt {
	j.t.Helper()
	return j.waitForReceipt(timeout, "receipt outcome "+string(want), func(r selfupdate.Receipt) bool { return r.Outcome == want })
}

func (j *selfupdateJourney) waitReceiptResolution(want selfupdate.Resolution, timeout time.Duration) selfupdate.Receipt {
	j.t.Helper()
	return j.waitForReceipt(timeout, "receipt resolution "+string(want), func(r selfupdate.Receipt) bool { return r.Resolution == want })
}

// waitReceiptRolledBack polls until the receipt is durably rolled back.
func (j *selfupdateJourney) waitReceiptRolledBack(timeout time.Duration) selfupdate.Receipt {
	j.t.Helper()
	return j.waitReceiptOutcome(selfupdate.OutcomeRolledBack, timeout)
}

// requireSuppressedTarget asserts the exact failed target version of a
// rolled-back receipt is durably suppressed for the installed executable
// while any other version stays eligible.
func requireSuppressedTarget(t *testing.T, j *selfupdateJourney, r selfupdate.Receipt) {
	t.Helper()
	lookup, err := selfupdate.LookupSuppression(j.installPath, r.ToVersion)
	if err != nil {
		t.Fatalf("lookup suppression for %s: %v", r.ToVersion, err)
	}
	if !lookup.Suppressed {
		t.Fatalf("target version %s is not suppressed after an actual rollback", r.ToVersion)
	}
	if lookup.Target.TransactionID != r.TransactionID {
		t.Fatalf("suppression records transaction %q; want the rolled-back %q", lookup.Target.TransactionID, r.TransactionID)
	}
	other := selfupdateVersionProd
	if other == r.ToVersion {
		other = selfupdateVersionLower
	}
	lookup, err = selfupdate.LookupSuppression(j.installPath, other)
	if err != nil {
		t.Fatalf("lookup suppression for %s: %v", other, err)
	}
	if lookup.Suppressed {
		t.Fatalf("version %s must not be suppressed by the rollback of %s", other, r.ToVersion)
	}
}

// requireNoSuppression asserts no version is suppressed for the installed
// executable: installation failures never suppress their target.
func requireNoSuppression(t *testing.T, j *selfupdateJourney, target string) {
	t.Helper()
	if lookup, err := selfupdate.LookupSuppression(j.installPath, target); err != nil {
		t.Fatalf("lookup suppression for %s: %v", target, err)
	} else if lookup.Suppressed {
		t.Fatalf("version %s is suppressed; an installation failure must never suppress its target", target)
	}
	targets, err := selfupdate.SuppressedVersions(j.installPath)
	if err != nil {
		t.Fatalf("read suppression store: %v", err)
	}
	if len(targets) != 0 {
		t.Fatalf("suppression store = %+v; want empty", targets)
	}
}

// waitPathGone polls until path no longer exists.
func (j *selfupdateJourney) waitPathGone(path, what string, timeout time.Duration) {
	j.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			j.t.Fatalf("%s still present at %s", what, path)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// installDigest digests the installed copy under test.
func (j *selfupdateJourney) installDigest() string {
	j.t.Helper()
	d, err := selfupdate.DigestFile(j.installPath)
	if err != nil {
		j.t.Fatalf("digest installed copy: %v", err)
	}
	return d
}

// trigger creates the trigger file the driver polls for.
func (j *selfupdateJourney) trigger() {
	j.t.Helper()
	if err := os.WriteFile(j.triggerPath, []byte("selfupdate-e2e-trigger"), 0o644); err != nil {
		j.t.Fatalf("write trigger file: %v", err)
	}
}

// selfupdateFreePort reserves an ephemeral port on host and releases it.
func selfupdateFreePort(t *testing.T, host string) int {
	t.Helper()
	l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatalf("probe free port on %s: %v", host, err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// openSelfUpdateStream opens one SSE stream and reports its termination (EOF
// or error) on the returned channel. The reader goroutine keeps reading the
// same connection across the exec boundary until the server closes it.
func openSelfUpdateStream(t *testing.T, rawURL string) <-chan error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("new stream request: %v", err)
	}
	resp, err := selfupdateStreamClient.Do(req)
	if err != nil {
		t.Fatalf("open stream %s: %v", rawURL, err)
	}
	terminated := make(chan error, 1)
	go func() {
		defer resp.Body.Close()
		buf := make([]byte, 4096)
		for {
			if _, err := resp.Body.Read(buf); err != nil {
				terminated <- err
				return
			}
		}
	}()
	return terminated
}

// selfupdateStreamResetEpoch reconnects carrying the pre-handoff epoch
// cursor (the after cursor is required for the epoch comparison to run) and
// returns the epoch carried by the resulting stream.reset event.
func selfupdateStreamResetEpoch(t *testing.T, baseURL, token, epoch string) string {
	t.Helper()
	u := baseURL + "/api/v1/events?access_token=" + url.QueryEscape(token) +
		"&epoch=" + url.QueryEscape(epoch) + "&after=1"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		t.Fatalf("new reconnect request: %v", err)
	}
	resp, err := selfupdateStreamClient.Do(req)
	if err != nil {
		t.Fatalf("reconnect stream: %v", err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 8192)
	var acc string
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			acc += string(buf[:n])
			if e, ok := selfupdateParseResetEpoch(acc); ok {
				return e
			}
		}
		if err != nil {
			t.Fatalf("stream.reset never arrived before the stream ended; received:\n%s", acc)
		}
		if ctx.Err() != nil {
			t.Fatalf("stream.reset never arrived within 10s; received:\n%s", acc)
		}
	}
}

// selfupdateParseResetEpoch extracts the epoch from a stream.reset frame.
func selfupdateParseResetEpoch(acc string) (string, bool) {
	for _, block := range strings.Split(acc, "\n\n") {
		if !strings.Contains(block, "event: stream.reset") {
			continue
		}
		for _, line := range strings.Split(block, "\n") {
			if data, ok := strings.CutPrefix(line, "data: "); ok {
				var evt struct {
					Epoch string `json:"epoch"`
				}
				if err := json.Unmarshal([]byte(data), &evt); err == nil && evt.Epoch != "" {
					return evt.Epoch, true
				}
			}
		}
	}
	return "", false
}

// TestSelfUpdateJourneyFixedPort proves the full atomic replacement journey
// on a fixed loopback endpoint: the installed lower driver prepares a
// durable transaction, drains (terminating an open event stream), commits,
// and really execs onto the installed path; the higher image rebinds the
// same endpoint with the same PID, token, and runtime identity but a new
// epoch, confirms the receipt, cleans up, and serves until SIGTERM.
func TestSelfUpdateJourneyFixedPort(t *testing.T) {
	selfupdateJourneyGuard(t)
	bins := selfupdateTestBinaries(t)
	j := newSelfupdateJourney(t, bins.lower)
	port := selfupdateFreePort(t, "127.0.0.1")
	baseURL := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	p := j.start(selfupdateStartOptions{listen: "127.0.0.1:" + strconv.Itoa(port)})

	pre := j.waitHealthy(p, baseURL, selfupdateVersionLower, 30*time.Second)
	if pre.Owner.PID != p.pid() {
		t.Fatalf("pre-health owner pid = %d; want driver pid %d", pre.Owner.PID, p.pid())
	}
	disc1 := j.waitDiscovery(p, 20*time.Second)
	if disc1.BaseURL != baseURL {
		t.Fatalf("discovery base_url = %q; want %q", disc1.BaseURL, baseURL)
	}
	if disc1.Epoch == "" || disc1.AuthToken == "" {
		t.Fatalf("discovery missing epoch/token: %+v", disc1)
	}
	if disc1.Owner.Version != selfupdateVersionLower || disc1.PID != p.pid() {
		t.Fatalf("discovery owner = %+v pid = %d; want version %q pid %d", disc1.Owner, disc1.PID, selfupdateVersionLower, p.pid())
	}
	token := disc1.AuthToken
	epoch1 := disc1.Epoch
	if code := j.featuresStatus(baseURL, token); code != http.StatusOK {
		t.Fatalf("authenticated features status = %d; want 200", code)
	}

	// One pre-handoff SSE stream: the drain must terminate it, proving open
	// event streams cannot indefinitely prevent the exec handoff.
	streamURL := baseURL + "/api/v1/events?access_token=" + url.QueryEscape(token) + "&heartbeat_ms=200"
	terminated := openSelfUpdateStream(t, streamURL)

	j.trigger()
	// Preparing and syncing binary copies precedes draining and has its own budget.
	j.waitReceipt(45 * time.Second)

	// Bounded generously: a healthy drain closes the stream promptly; a
	// stalled one only closes it at process death, which the later
	// healthy-version wait diagnoses with the driver's stderr.
	select {
	case err := <-terminated:
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			t.Fatalf("SSE client expired instead of observing server shutdown: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("pre-handoff SSE stream never terminated after trigger (stderr tail:\n%s)", p.stderrTail())
	}

	post := j.waitHealthy(p, baseURL, selfupdateVersionHigher, 30*time.Second)
	if post.Owner.PID != pre.Owner.PID {
		t.Fatalf("post-health owner pid = %d; want unchanged %d (exec preserves pid)", post.Owner.PID, pre.Owner.PID)
	}
	if post.Runtime != pre.Runtime {
		t.Fatalf("runtime identity changed across exec: %+v -> %+v", pre.Runtime, post.Runtime)
	}

	disc2 := j.waitDiscoveryEpochChange(p, epoch1, 20*time.Second)
	if disc2.AuthToken != token {
		t.Fatalf("auth token changed across exec: %q -> %q", token, disc2.AuthToken)
	}
	if disc2.PID != p.pid() || disc2.Owner.PID != p.pid() {
		t.Fatalf("discovery pid changed across exec: pid=%d owner=%d; want %d", disc2.PID, disc2.Owner.PID, p.pid())
	}
	if disc2.Owner.Version != selfupdateVersionHigher {
		t.Fatalf("discovery owner version = %q; want %q", disc2.Owner.Version, selfupdateVersionHigher)
	}
	if code := j.featuresStatus(baseURL, token); code != http.StatusOK {
		t.Fatalf("post-exec authenticated features status = %d; want 200", code)
	}

	receipt := j.waitReceiptOutcome(selfupdate.OutcomeConfirmed, 20*time.Second)
	if receipt.Phase != selfupdate.PhaseReplacementComplete {
		t.Fatalf("receipt phase = %q; want %q", receipt.Phase, selfupdate.PhaseReplacementComplete)
	}
	if receipt.FromVersion != selfupdateVersionLower || receipt.ToVersion != selfupdateVersionHigher {
		t.Fatalf("receipt versions = %q -> %q; want %q -> %q", receipt.FromVersion, receipt.ToVersion, selfupdateVersionLower, selfupdateVersionHigher)
	}
	if receipt.Bind.Port != port {
		t.Fatalf("receipt bind port = %d; want %d", receipt.Bind.Port, port)
	}
	j.waitPathGone(filepath.Join(selfupdate.LeaseDir(j.installPath), "tx-"+receipt.TransactionID), "transaction dir", 10*time.Second)
	if _, err := os.Stat(selfupdate.LeasePath(j.installPath)); err != nil {
		t.Fatalf("lease file must never be unlinked: %v", err)
	}
	if _, err := os.Stat(selfupdate.OwnershipRecordPath(j.installPath)); err != nil {
		t.Fatalf("ownership record must never be unlinked: %v", err)
	}
	if got := j.installDigest(); got != j.bins.higherDigest {
		t.Fatalf("installed digest = %s; want higher binary digest %s", got, j.bins.higherDigest)
	}

	argsDigests := p.digestTokens("args-digest")
	if len(argsDigests) != 2 || argsDigests[0] != argsDigests[1] {
		t.Fatalf("args-digest fingerprints = %v; want exactly two (one per image) and equal", argsDigests)
	}
	envDigests := p.digestTokens("env-digest")
	if len(envDigests) != 2 || envDigests[0] != envDigests[1] {
		t.Fatalf("env-digest fingerprints = %v; want exactly two (one per image) and equal", envDigests)
	}

	if resetEpoch := selfupdateStreamResetEpoch(t, baseURL, token, epoch1); resetEpoch != disc2.Epoch {
		t.Fatalf("stream.reset epoch = %q; want new discovery epoch %q", resetEpoch, disc2.Epoch)
	}
	if !p.alive() {
		t.Fatal("driver process died before SIGTERM")
	}
	if code := p.terminate(); code != 0 {
		t.Fatalf("SIGTERM exit code = %d; want 0 (stderr tail:\n%s)", code, p.stderrTail())
	}
	registryEntry := server.RegistryEntryPath(server.RegistryDir(filepath.Join(j.home, ".agentic-orchestrator")), j.runtimeDir)
	j.waitPathGone(registryEntry, "registry entry", 10*time.Second)
}

// TestSelfUpdateJourneyDefaultEphemeral proves the default ephemeral
// listener survives the handoff: the replacement image returns on the exact
// port the old image was assigned, even though the preserved argument
// vector still requests an ephemeral port.
func TestSelfUpdateJourneyDefaultEphemeral(t *testing.T) {
	selfupdateJourneyGuard(t)
	bins := selfupdateTestBinaries(t)
	j := newSelfupdateJourney(t, bins.lower)
	p := j.start(selfupdateStartOptions{})

	disc1 := j.waitDiscovery(p, 20*time.Second)
	u, err := url.Parse(disc1.BaseURL)
	if err != nil {
		t.Fatalf("parse discovery base_url %q: %v", disc1.BaseURL, err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("discovery base_url %q carries no port", disc1.BaseURL)
	}
	baseURL := disc1.BaseURL
	pre := j.waitHealthy(p, baseURL, selfupdateVersionLower, 30*time.Second)
	if pre.Owner.PID != p.pid() {
		t.Fatalf("pre-health owner pid = %d; want driver pid %d", pre.Owner.PID, p.pid())
	}
	epoch1 := disc1.Epoch

	j.trigger()

	post := j.waitHealthy(p, baseURL, selfupdateVersionHigher, 30*time.Second)
	if post.Owner.PID != p.pid() {
		t.Fatalf("post-health owner pid = %d; want unchanged %d", post.Owner.PID, p.pid())
	}
	disc2 := j.waitDiscoveryEpochChange(p, epoch1, 20*time.Second)
	if disc2.AuthToken != disc1.AuthToken {
		t.Fatalf("auth token changed across exec: %q -> %q", disc1.AuthToken, disc2.AuthToken)
	}
	if disc2.BaseURL != baseURL {
		t.Fatalf("discovery base_url moved ports: %q -> %q", baseURL, disc2.BaseURL)
	}
	receipt := j.waitReceiptOutcome(selfupdate.OutcomeConfirmed, 20*time.Second)
	if receipt.Phase != selfupdate.PhaseReplacementComplete {
		t.Fatalf("receipt phase = %q; want %q", receipt.Phase, selfupdate.PhaseReplacementComplete)
	}
	if receipt.Bind.Port != port {
		t.Fatalf("receipt bind port = %d; want originally assigned port %d", receipt.Bind.Port, port)
	}
	if got := j.installDigest(); got != j.bins.higherDigest {
		t.Fatalf("installed digest = %s; want higher binary digest %s", got, j.bins.higherDigest)
	}
	if code := p.terminate(); code != 0 {
		t.Fatalf("SIGTERM exit code = %d; want 0 (stderr tail:\n%s)", code, p.stderrTail())
	}
}

// TestSelfUpdateJourneyWildcard proves wildcard bind semantics survive the
// handoff: discovery advertises a concrete interface host (never the
// wildcard form), loopback clients reach the same port throughout, and the
// replacement image rebinds the recorded wildcard address on the same port.
// A second loopback-family journey exercises the IPv6 loopback bind where
// the host supports it.
func TestSelfUpdateJourneyWildcard(t *testing.T) {
	selfupdateJourneyGuard(t)
	bins := selfupdateTestBinaries(t)
	j := newSelfupdateJourney(t, bins.lower)
	port := selfupdateFreePort(t, "0.0.0.0")
	loopbackURL := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	p := j.start(selfupdateStartOptions{listen: "0.0.0.0:" + strconv.Itoa(port)})

	disc1 := j.waitDiscovery(p, 20*time.Second)
	advertised, err := url.Parse(disc1.BaseURL)
	if err != nil {
		t.Fatalf("parse advertised base_url %q: %v", disc1.BaseURL, err)
	}
	advertisedHost := advertised.Hostname()
	if advertisedHost == "" || advertisedHost == "0.0.0.0" || advertisedHost == "::" {
		t.Fatalf("discovery advertises a wildcard host: %q", disc1.BaseURL)
	}
	// Explicit platform capability handling: some hosts (notably macOS with
	// its application firewall blocking inbound connections to unsigned
	// binaries on non-loopback interfaces) cannot reach the advertised
	// interface address even though the wildcard bind itself is healthy via
	// loopback. The required wildcard journey still runs in that case —
	// bind semantics, same-port rebinding, and discovery advertisement are
	// all still asserted; only direct client access via the advertised host
	// is conditioned on a bounded probe of the actual driver process.
	advertisedReachable := false
	probeDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(probeDeadline) {
		if _, err := j.getHealth(disc1.BaseURL); err == nil {
			advertisedReachable = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	var pre server.HealthResponse
	if advertisedReachable {
		pre = j.waitHealthy(p, disc1.BaseURL, selfupdateVersionLower, 30*time.Second)
	} else {
		t.Logf("advertised host %s is not reachable on this host (firewall/capability); asserting wildcard bind via loopback", advertisedHost)
		pre = j.waitHealthy(p, loopbackURL, selfupdateVersionLower, 30*time.Second)
	}
	if _, err := j.getHealth(loopbackURL); err != nil {
		t.Fatalf("loopback health via 127.0.0.1:%d: %v", port, err)
	}
	if pre.Owner.PID != p.pid() {
		t.Fatalf("pre-health owner pid = %d; want driver pid %d", pre.Owner.PID, p.pid())
	}
	epoch1 := disc1.Epoch

	j.trigger()

	post := j.waitHealthy(p, loopbackURL, selfupdateVersionHigher, 30*time.Second)
	if post.Owner.PID != p.pid() {
		t.Fatalf("post-health owner pid = %d; want unchanged %d", post.Owner.PID, p.pid())
	}
	if advertisedReachable {
		if _, err := j.getHealth(disc1.BaseURL); err != nil {
			t.Fatalf("advertised-URL health after exec: %v", err)
		}
	}
	disc2 := j.waitDiscoveryEpochChange(p, epoch1, 20*time.Second)
	if disc2.AuthToken != disc1.AuthToken {
		t.Fatalf("auth token changed across exec: %q -> %q", disc1.AuthToken, disc2.AuthToken)
	}
	receipt := j.waitReceiptOutcome(selfupdate.OutcomeConfirmed, 20*time.Second)
	if receipt.Phase != selfupdate.PhaseReplacementComplete {
		t.Fatalf("receipt phase = %q; want %q", receipt.Phase, selfupdate.PhaseReplacementComplete)
	}
	if receipt.Bind.Port != port {
		t.Fatalf("receipt bind port = %d; want %d", receipt.Bind.Port, port)
	}
	if code := p.terminate(); code != 0 {
		t.Fatalf("SIGTERM exit code = %d; want 0 (stderr tail:\n%s)", code, p.stderrTail())
	}

	t.Run("ipv6 loopback", func(t *testing.T) {
		selfupdateJourneyGuard(t)
		probe, err := net.Listen("tcp", "[::1]:0")
		if err != nil {
			t.Skipf("host cannot bind IPv6 loopback: %v", err)
		}
		_ = probe.Close()
		bins := selfupdateTestBinaries(t)
		j6 := newSelfupdateJourney(t, bins.lower)
		port6 := selfupdateFreePort(t, "::1")
		baseURL6 := "http://" + net.JoinHostPort("::1", strconv.Itoa(port6))
		p6 := j6.start(selfupdateStartOptions{listen: "[::1]:" + strconv.Itoa(port6)})
		pre6 := j6.waitHealthy(p6, baseURL6, selfupdateVersionLower, 30*time.Second)
		if pre6.Owner.PID != p6.pid() {
			t.Fatalf("pre-health owner pid = %d; want driver pid %d", pre6.Owner.PID, p6.pid())
		}
		j6.trigger()
		post6 := j6.waitHealthy(p6, baseURL6, selfupdateVersionHigher, 30*time.Second)
		if post6.Owner.PID != p6.pid() {
			t.Fatalf("post-health owner pid = %d; want unchanged %d", post6.Owner.PID, p6.pid())
		}
		receipt6 := j6.waitReceiptOutcome(selfupdate.OutcomeConfirmed, 20*time.Second)
		if receipt6.Phase != selfupdate.PhaseReplacementComplete {
			t.Fatalf("receipt phase = %q; want %q", receipt6.Phase, selfupdate.PhaseReplacementComplete)
		}
		if receipt6.Bind.Port != port6 {
			t.Fatalf("receipt bind port = %d; want %d", receipt6.Bind.Port, port6)
		}
		if code := p6.terminate(); code != 0 {
			t.Fatalf("SIGTERM exit code = %d; want 0 (stderr tail:\n%s)", code, p6.stderrTail())
		}
	})
}

// TestSelfUpdateJourneyPrepareOnly proves --prepare-only stops after the
// durable preparation barrier: the process exits zero with a pending
// backup-ready receipt, untouched installed bytes, an owner-only rollback
// backup, retained staging and transaction dir, and no exec ever happening.
func TestSelfUpdateJourneyPrepareOnly(t *testing.T) {
	selfupdateJourneyGuard(t)
	bins := selfupdateTestBinaries(t)
	j := newSelfupdateJourney(t, bins.lower)
	port := selfupdateFreePort(t, "127.0.0.1")
	baseURL := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	p := j.start(selfupdateStartOptions{listen: "127.0.0.1:" + strconv.Itoa(port), prepareOnly: true})
	j.waitHealthy(p, baseURL, selfupdateVersionLower, 30*time.Second)

	j.trigger()

	if code := p.waitBounded(30 * time.Second); code != 0 {
		t.Fatalf("prepare-only exit code = %d; want 0 (stderr tail:\n%s)", code, p.stderrTail())
	}
	if !p.stderrContains("selfupdate-driver: prepared ") {
		t.Fatalf("no prepared milestone in stderr:\n%s", p.stderrTail())
	}
	if p.stderrContains("selfupdate-driver: exec /") {
		t.Fatal("prepare-only unexpectedly crossed the exec boundary")
	}
	receipt := j.waitReceipt(10 * time.Second)
	if receipt.Outcome != selfupdate.OutcomePending || receipt.Phase != selfupdate.PhaseBackupReady {
		t.Fatalf("receipt = %q/%q; want pending/backup-ready", receipt.Outcome, receipt.Phase)
	}
	if receipt.FromVersion != selfupdateVersionLower || receipt.ToVersion != selfupdateVersionHigher {
		t.Fatalf("receipt versions = %q -> %q", receipt.FromVersion, receipt.ToVersion)
	}
	if got := j.installDigest(); got != j.bins.lowerDigest {
		t.Fatalf("installed digest = %s; want untouched lower digest %s", got, j.bins.lowerDigest)
	}
	info, err := os.Stat(receipt.BackupPath)
	if err != nil {
		t.Fatalf("rollback backup missing: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %o; want 0600", info.Mode().Perm())
	}
	if _, err := os.Stat(receipt.StagingPath); err != nil {
		t.Fatalf("staged candidate missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(selfupdate.LeaseDir(j.installPath), "tx-"+receipt.TransactionID)); err != nil {
		t.Fatalf("transaction dir missing: %v", err)
	}
}

// TestSelfUpdateJourneyFailureInjection proves failure containment and
// recovery at every injection point: preparation failures leave no receipt
// and untouched bytes; an aborted drain settles an installation failure while
// the old build keeps serving; aborted shutdowns after serving resources
// closed restart the unchanged build through the guarded recovery path; and
// every failure at or after the handoff boundary funnels through the
// production recovery boundary, restoring the previous build to service with
// a truthful rolled_back receipt and exact-target suppression.
func TestSelfUpdateJourneyFailureInjection(t *testing.T) {
	selfupdateJourneyGuard(t)

	waitExitNonZero := func(t *testing.T, p *driverProcess) {
		t.Helper()
		if code := p.waitBounded(30 * time.Second); code == 0 {
			t.Fatalf("driver exited 0 despite injected failure (stderr tail:\n%s)", p.stderrTail())
		}
	}
	requireInstalledDigest := func(t *testing.T, j *selfupdateJourney, want string) {
		t.Helper()
		if got := j.installDigest(); got != want {
			t.Fatalf("installed digest = %s; want %s", got, want)
		}
	}
	requireNoReceipt := func(t *testing.T, j *selfupdateJourney) {
		t.Helper()
		if _, err := os.Stat(j.receiptPath()); !os.IsNotExist(err) {
			t.Fatalf("receipt unexpectedly present at %s (err = %v)", j.receiptPath(), err)
		}
	}

	// Preparation failures: Begin aborts before any receipt exists and the
	// installed bytes never change.
	for _, point := range []string{"backup-sync", "receipt-write"} {
		t.Run(point, func(t *testing.T) {
			selfupdateJourneyGuard(t)
			bins := selfupdateTestBinaries(t)
			j := newSelfupdateJourney(t, bins.lower)
			p := j.start(selfupdateStartOptions{failAt: point})
			j.trigger()
			waitExitNonZero(t, p)
			if !p.stderrContains("selfupdate-driver: begin failed:") {
				t.Fatalf("no begin-failed milestone in stderr:\n%s", p.stderrTail())
			}
			requireNoReceipt(t, j)
			requireInstalledDigest(t, j, j.bins.lowerDigest)
		})
	}

	// Drain failure while the old runtime is still usable: the installation
	// is settled as an installation failure before any serving resource
	// closed and the unchanged build keeps serving. The target is never
	// installed or executed, nothing is suppressed, and SIGTERM exits zero.
	t.Run("drain", func(t *testing.T) {
		selfupdateJourneyGuard(t)
		bins := selfupdateTestBinaries(t)
		j := newSelfupdateJourney(t, bins.lower)
		port := selfupdateFreePort(t, "127.0.0.1")
		baseURL := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		p := j.start(selfupdateStartOptions{failAt: "drain", listen: "127.0.0.1:" + strconv.Itoa(port)})
		j.waitHealthy(p, baseURL, selfupdateVersionLower, 30*time.Second)

		j.trigger()

		r := j.waitReceiptResolution(selfupdate.ResolutionAbandonedPreReplacement, 20*time.Second)
		if r.Outcome != selfupdate.OutcomePending || r.Phase != selfupdate.PhaseBackupReady {
			t.Fatalf("receipt = %q/%q; want pending/backup-ready", r.Outcome, r.Phase)
		}
		if !r.InstallFailed {
			t.Fatal("abandoned drain receipt must record the installation failure")
		}
		if r.RecoveryKind != "" || r.RecoveryAttemptID != "" {
			t.Fatalf("drain receipt records recovery metadata %q/%q; want none (no restart, no restore)", r.RecoveryKind, r.RecoveryAttemptID)
		}
		if r.Error == "" {
			t.Fatal("receipt carries no sanitized error")
		}
		if got := j.installDigest(); got != bins.lowerDigest {
			t.Fatalf("installed digest = %s; want untouched lower digest %s", got, bins.lowerDigest)
		}
		requireNoSuppression(t, j, selfupdateVersionHigher)
		if !p.alive() {
			t.Fatal("drain-aborted driver died before SIGTERM")
		}
		if code := p.terminate(); code != 0 {
			t.Fatalf("SIGTERM exit code = %d; want 0 (stderr tail:\n%s)", code, p.stderrTail())
		}
		if !p.stderrContains("selfupdate-driver: drain failed (injected)") {
			t.Fatalf("no injected drain milestone in stderr:\n%s", p.stderrTail())
		}
		if !p.stderrContains("selfupdate-driver: installation aborted pre-replacement") {
			t.Fatalf("no aborted-installation milestone in stderr:\n%s", p.stderrTail())
		}
	})

	// Shutdown failures after serving resources closed: the unchanged build
	// is re-executed through the guarded recovery path — an installation
	// failure under durable restart tracking, never a rollback. The process
	// keeps its pid, rebinds the same endpoint, serves the lower version
	// again, and the pending receipt is settled as abandoned on its boot.
	for _, point := range []string{"server-close", "shutdown-mixed", "stop"} {
		t.Run(point, func(t *testing.T) {
			selfupdateJourneyGuard(t)
			bins := selfupdateTestBinaries(t)
			j := newSelfupdateJourney(t, bins.lower)
			port := selfupdateFreePort(t, "127.0.0.1")
			baseURL := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
			p := j.start(selfupdateStartOptions{failAt: point, listen: "127.0.0.1:" + strconv.Itoa(port)})
			pre := j.waitHealthy(p, baseURL, selfupdateVersionLower, 30*time.Second)

			j.trigger()

			// The durable restart settlement is the unambiguous marker: wait for
			// it before any live-server assertion (the old server may still be
			// serving when the journey begins).
			r := j.waitReceiptResolution(selfupdate.ResolutionAbandonedPreReplacement, 45*time.Second)
			post := j.waitHealthy(p, baseURL, selfupdateVersionLower, 30*time.Second)
			if pre.Owner.PID != p.pid() || post.Owner.PID != p.pid() {
				t.Fatalf("owner pid moved across the restart: pre=%d post=%d pid=%d", pre.Owner.PID, post.Owner.PID, p.pid())
			}
			if r.Outcome != selfupdate.OutcomePending || r.Phase != selfupdate.PhaseBackupReady {
				t.Fatalf("receipt = %q/%q; want pending/backup-ready", r.Outcome, r.Phase)
			}
			if r.RecoveryKind != selfupdate.RecoveryKindRestart || r.RecoveryAttemptID == "" {
				t.Fatalf("receipt recovery = %q/%q; want restart kind with attempt id", r.RecoveryKind, r.RecoveryAttemptID)
			}
			if !r.InstallFailed {
				t.Fatal("restarted installation must be recorded as an installation failure")
			}
			if got := j.installDigest(); got != bins.lowerDigest {
				t.Fatalf("installed digest = %s; want untouched lower digest %s", got, bins.lowerDigest)
			}
			requireNoSuppression(t, j, selfupdateVersionHigher)
			if code := p.terminate(); code != 0 {
				t.Fatalf("SIGTERM exit code = %d; want 0 (stderr tail:\n%s)", code, p.stderrTail())
			}
			if n := p.stderrPrefixCount("selfupdate-driver: exec /"); n != 0 {
				t.Fatalf("exec milestones = %d; want 0 (the target is never executed)", n)
			}
			if p.stderrContains("selfupdate-driver: committed") {
				t.Fatal("restart journey must never reach commit")
			}
			if !p.stderrContains("selfupdate-driver: restarting unchanged build after aborted shutdown") {
				t.Fatalf("no restart milestone in stderr:\n%s", p.stderrTail())
			}
			wantMilestone := map[string]string{
				"server-close":   "selfupdate-driver: server shutdown failed",
				"shutdown-mixed": "selfupdate-driver: server shutdown failed",
				"stop":           "selfupdate-driver: stop failed (injected)",
			}[point]
			if !p.stderrContains(wantMilestone) {
				t.Fatalf("no %s milestone in stderr:\n%s", point, p.stderrTail())
			}
		})
	}

	// Handoff failures on the old image (post-rename, exec-error): the
	// candidate is committed but the target is never invoked, so the
	// production recovery boundary restores the previous build and execs it
	// with the chain guard: same pid, same endpoint, lower version serving
	// again, rolled_back receipt with restore metadata, exact-target
	// suppression, and the canonical update_rolled_back warning.
	for _, point := range []string{"post-rename", "exec-error"} {
		t.Run(point, func(t *testing.T) {
			selfupdateJourneyGuard(t)
			bins := selfupdateTestBinaries(t)
			j := newSelfupdateJourney(t, bins.lower)
			port := selfupdateFreePort(t, "127.0.0.1")
			baseURL := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
			p := j.start(selfupdateStartOptions{failAt: point, listen: "127.0.0.1:" + strconv.Itoa(port)})
			j.waitHealthy(p, baseURL, selfupdateVersionLower, 30*time.Second)

			j.trigger()

			// post-rename never invokes the target (0 exec milestones);
			// exec-error crosses the boundary print once before the
			// injected failure.
			wantExecCrossings := map[string]int{"post-rename": 0, "exec-error": 1}[point]
			assertRestoredService(t, j, p, baseURL, wantExecCrossings)
			if !p.stderrContains("selfupdate-driver: committed") {
				t.Fatalf("no committed milestone in stderr:\n%s", p.stderrTail())
			}
			wantMilestone := map[string]string{
				"post-rename": "selfupdate-driver: post-rename failure (injected)",
				"exec-error":  "selfupdate-driver: exec failed:",
			}[point]
			if !p.stderrContains(wantMilestone) {
				t.Fatalf("no %s milestone in stderr:\n%s", point, p.stderrTail())
			}
		})
	}

	// Target startup failures: the old image really execs the new build and
	// the adopted image fails during bootstrap (cooperative startup deadline
	// expiry), so the production recovery boundary restores the previous
	// build: same end state as the handoff failures, with exactly one exec
	// crossing onto the target.
	for _, point := range []string{"target-startup", "startup-deadline"} {
		t.Run(point, func(t *testing.T) {
			selfupdateJourneyGuard(t)
			bins := selfupdateTestBinaries(t)
			j := newSelfupdateJourney(t, bins.lower)
			port := selfupdateFreePort(t, "127.0.0.1")
			baseURL := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
			p := j.start(selfupdateStartOptions{failAt: point, listen: "127.0.0.1:" + strconv.Itoa(port)})
			j.waitHealthy(p, baseURL, selfupdateVersionLower, 30*time.Second)

			j.trigger()

			assertRestoredService(t, j, p, baseURL, 1)
		})
	}

	// Adopted-image failures (discovery publication, self-health wait,
	// confirmation persistence): the production recovery boundary tears the
	// partially started target down, restores the previous build, and execs
	// it with the chain guard. A confirm failure never persists
	// confirmation: the receipt ends rolled_back.
	for _, point := range []string{"discovery", "health", "confirm"} {
		t.Run(point, func(t *testing.T) {
			selfupdateJourneyGuard(t)
			bins := selfupdateTestBinaries(t)
			j := newSelfupdateJourney(t, bins.lower)
			port := selfupdateFreePort(t, "127.0.0.1")
			baseURL := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
			p := j.start(selfupdateStartOptions{failAt: point, listen: "127.0.0.1:" + strconv.Itoa(port)})
			j.waitHealthy(p, baseURL, selfupdateVersionLower, 30*time.Second)

			j.trigger()

			if point == "confirm" {
				// Best-effort: the confirm abort can land within
				// milliseconds of the listener coming up, so a short window
				// observes the adopted image without stalling the journey
				// when it is missed.
				if h, ok := j.observeHealthy(p, baseURL, selfupdateVersionHigher, 5*time.Second); ok && h.Owner.PID != p.pid() {
					t.Fatalf("adopted owner pid = %d; want preserved %d", h.Owner.PID, p.pid())
				}
			}
			assertRestoredService(t, j, p, baseURL, 1)
			if point == "confirm" {
				// The production confirm path renders the injection into the
				// recovery reason: the rolled-back receipt carries it as the
				// sanitized failure, and confirmation was never persisted.
				r := j.waitReceiptRolledBack(5 * time.Second)
				if !strings.Contains(r.Error, "confirm") {
					t.Fatalf("rolled-back receipt error = %q; want the confirm failure recorded", r.Error)
				}
			}
		})
	}
}

// assertRestoredService asserts the recovered end state of one restore
// journey: the receipt is durably rolled_back with restore metadata (the
// unambiguous recovery marker — the old server may still be serving when the
// journey begins, so the receipt is awaited first), then the previous build
// serves again with the preserved pid on the same endpoint, the installed
// bytes are the previous build, exactly wantExecCrossings exec boundaries
// were crossed onto the target, exact-target suppression records the failed
// version, the recovered build refuses a chained update journey and reports
// the canonical update_rolled_back warning, and SIGTERM exits zero.
func assertRestoredService(t *testing.T, j *selfupdateJourney, p *driverProcess, baseURL string, wantExecCrossings int) {
	t.Helper()
	r := j.waitReceiptRolledBack(45 * time.Second)
	if r.Phase != selfupdate.PhaseRollbackAttempted {
		t.Fatalf("receipt phase = %q; want %q", r.Phase, selfupdate.PhaseRollbackAttempted)
	}
	if r.RecoveryKind != selfupdate.RecoveryKindRestore || r.RecoveryAttemptID == "" {
		t.Fatalf("receipt recovery = %q/%q; want restore kind with attempt id", r.RecoveryKind, r.RecoveryAttemptID)
	}
	if r.RestorePath == "" || r.RestoreID == nil {
		t.Fatalf("receipt restore metadata missing: path=%q id=%v", r.RestorePath, r.RestoreID)
	}
	if r.FromVersion != selfupdateVersionLower || r.ToVersion != selfupdateVersionHigher {
		t.Fatalf("receipt versions = %q -> %q", r.FromVersion, r.ToVersion)
	}
	post := j.waitHealthy(p, baseURL, selfupdateVersionLower, 30*time.Second)
	if post.Owner.PID != p.pid() {
		t.Fatalf("recovered owner pid = %d; want preserved %d", post.Owner.PID, p.pid())
	}
	if got := j.installDigest(); got != j.bins.lowerDigest {
		t.Fatalf("installed digest = %s; want restored lower digest %s", got, j.bins.lowerDigest)
	}
	if code := p.terminate(); code != 0 {
		t.Fatalf("SIGTERM exit code = %d; want 0 (stderr tail:\n%s)", code, p.stderrTail())
	}
	if n := p.stderrPrefixCount("selfupdate-driver: exec /"); n != wantExecCrossings {
		t.Fatalf("exec milestones = %d; want %d (stderr tail:\n%s)", n, wantExecCrossings, p.stderrTail())
	}
	if !p.stderrContains("selfupdate-driver: refusing update journey in a recovery chain; fresh consent required") {
		t.Fatalf("recovered build did not refuse the chained journey:\n%s", p.stderrTail())
	}
	if !p.stderrContains("warning[update_rolled_back]") {
		t.Fatalf("no canonical update_rolled_back warning in stderr:\n%s", p.stderrTail())
	}
	requireSuppressedTarget(t, j, r)
}

// TestSelfUpdateOwnershipAcrossJourneys proves binary-scoped update
// ownership across the whole journey lifecycle: exactly one owner per
// installed binary, a secondary runtime serving without ownership,
// same-runtime launches refused by the instance lock, competing launches
// refused during a live handoff, and ownership surviving the exec with the
// confirmed transaction bound to the preserved pid.
func TestSelfUpdateOwnershipAcrossJourneys(t *testing.T) {
	selfupdateJourneyGuard(t)
	bins := selfupdateTestBinaries(t)
	j := newSelfupdateJourney(t, bins.lower)
	port := selfupdateFreePort(t, "127.0.0.1")
	baseURL := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	p := j.start(selfupdateStartOptions{listen: "127.0.0.1:" + strconv.Itoa(port)})
	j.waitHealthy(p, baseURL, selfupdateVersionLower, 30*time.Second)

	// (a) The serving lower driver owns updates for this installed binary.
	rec, err := selfupdate.ReadOwnershipRecord(j.installPath)
	if err != nil {
		t.Fatalf("read ownership record: %v", err)
	}
	if rec.PID != p.pid() {
		t.Fatalf("ownership record pid = %d; want serving driver pid %d", rec.PID, p.pid())
	}
	if rec.ExecutableDigest != j.bins.lowerDigest {
		t.Fatalf("ownership record digest = %s; want lower digest %s", rec.ExecutableDigest, j.bins.lowerDigest)
	}

	// (b) A second driver on the SAME installed binary with its own runtime
	// serves normally but never takes over update ownership.
	runtime2 := filepath.Join(filepath.Dir(j.runtimeDir), "runtime2")
	port2 := selfupdateFreePort(t, "127.0.0.1")
	baseURL2 := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port2))
	second := j.launch(
		"selfupdate-driver",
		"--config", filepath.Join(runtime2, "config.yaml"),
		"--state-dir", filepath.Join(runtime2, "features"),
		"--listen", "127.0.0.1:"+strconv.Itoa(port2))
	j.waitHealthy(second, baseURL2, selfupdateVersionLower, 30*time.Second)
	rec, err = selfupdate.ReadOwnershipRecord(j.installPath)
	if err != nil {
		t.Fatalf("re-read ownership record: %v", err)
	}
	if rec.PID != p.pid() {
		t.Fatalf("secondary runtime stole update ownership: record pid = %d; want %d", rec.PID, p.pid())
	}

	// (c) The secondary runtime shuts down ordinarily.
	if code := second.terminate(); code != 0 {
		t.Fatalf("second driver SIGTERM exit = %d; want 0 (stderr tail:\n%s)", code, second.stderrTail())
	}

	// (d) A competing launch on the SAME runtime is refused by the instance
	// lock while the owner serves.
	sameRuntime := j.launch(
		"selfupdate-driver",
		"--config", j.configPath,
		"--state-dir", j.stateDir)
	if code := sameRuntime.waitBounded(15 * time.Second); code == 0 {
		t.Fatalf("same-runtime competitor exited 0 (stderr:\n%s)", sameRuntime.stderrTail())
	}
	if !sameRuntime.stderrContains("already running for state dir") {
		t.Fatalf("same-runtime competitor rejected without the instance-lock message:\n%s", sameRuntime.stderrTail())
	}

	// (e) During a live handoff (pending receipt, lease held across the
	// exec), a same-runtime competing launch is rejected with either the
	// live-handoff refusal or the instance-lock busy message. The window
	// between pending and confirmed is short, so retries tolerate the race.
	j.trigger()
	pending := j.waitReceipt(20 * time.Second)
	if pending.Outcome != selfupdate.OutcomePending {
		t.Logf("journey completed before the pending snapshot was observed (outcome %q); the rejection assertions below still apply while the server lives", pending.Outcome)
	}
	rejected := false
	for attempt := 0; attempt < 3 && !rejected; attempt++ {
		racer := j.launch(
			"selfupdate-driver",
			"--config", j.configPath,
			"--state-dir", j.stateDir)
		code := racer.waitBounded(15 * time.Second)
		liveHandoff := racer.stderrContains("update handoff in progress")
		lockBusy := racer.stderrContains("already running for state dir")
		if code != 0 && (liveHandoff || lockBusy) {
			rejected = true
			break
		}
		if r, rerr := selfupdate.ReadReceipt(j.receiptPath()); rerr == nil && r.Outcome == selfupdate.OutcomeConfirmed && attempt < 2 {
			t.Logf("attempt %d raced the completed journey (exit %d, handoff-rejection=%v lock-busy=%v); retrying", attempt+1, code, liveHandoff, lockBusy)
			continue
		}
		t.Fatalf("competing launch during the handoff was not properly rejected (exit %d, handoff-rejection=%v lock-busy=%v):\n%s", code, liveHandoff, lockBusy, racer.stderrTail())
	}
	if !rejected {
		t.Fatal("no competing launch observed a handoff or instance-lock rejection")
	}

	// (f) After confirmation, ownership still names the exec-preserved pid
	// and the confirmed transaction.
	j.waitHealthy(p, baseURL, selfupdateVersionHigher, 30*time.Second)
	confirmed := j.waitReceiptOutcome(selfupdate.OutcomeConfirmed, 30*time.Second)
	rec, err = selfupdate.ReadOwnershipRecord(j.installPath)
	if err != nil {
		t.Fatalf("read ownership record after confirmation: %v", err)
	}
	if rec.PID != p.pid() {
		t.Fatalf("ownership pid after exec = %d; want preserved %d", rec.PID, p.pid())
	}
	if rec.TransactionID != confirmed.TransactionID {
		t.Fatalf("ownership transaction = %q; want confirmed %q", rec.TransactionID, confirmed.TransactionID)
	}
	if got := j.installDigest(); got != j.bins.higherDigest {
		t.Fatalf("installed digest = %s; want higher digest %s", got, j.bins.higherDigest)
	}
	if code := p.terminate(); code != 0 {
		t.Fatalf("SIGTERM exit code = %d; want 0 (stderr tail:\n%s)", code, p.stderrTail())
	}
}

// TestSelfUpdateProductionBoundary proves ordinary builds stay sealed: the
// untagged binary rejects the driver subcommand outright, and an invalid
// handoff environment entry fails the server launch closed without ever
// activating anything.
func TestSelfUpdateProductionBoundary(t *testing.T) {
	selfupdateJourneyGuard(t)

	t.Run("driver subcommand rejected", func(t *testing.T) {
		selfupdateJourneyGuard(t)
		bins := selfupdateTestBinaries(t)
		j := newSelfupdateJourney(t, bins.prod)
		p := j.launch(
			"selfupdate-driver",
			"--config", j.configPath,
			"--state-dir", j.stateDir,
			"--candidate", bins.higher,
			"--trigger-file", j.triggerPath)
		if code := p.waitBounded(15 * time.Second); code == 0 {
			t.Fatalf("production binary accepted the driver subcommand (exit 0):\n%s", p.stderrTail())
		}
		if !p.stderrContains("unknown command: selfupdate-driver") {
			t.Fatalf("production binary did not reject the driver subcommand as an unknown command:\n%s", p.stderrTail())
		}
		if _, err := os.Stat(j.receiptPath()); !os.IsNotExist(err) {
			t.Fatalf("production binary produced a receipt at %s (err = %v)", j.receiptPath(), err)
		}
		if got := j.installDigest(); got != bins.prodDigest {
			t.Fatalf("installed bytes changed: %s; want %s", got, bins.prodDigest)
		}
	})

	t.Run("invalid handoff env fails closed in server mode", func(t *testing.T) {
		selfupdateJourneyGuard(t)
		bins := selfupdateTestBinaries(t)
		j := newSelfupdateJourney(t, bins.prod)
		env := append(selfupdateDriverEnv(j.home),
			selfupdate.HandoffEnvVar+`={"json":"garbage"}`)
		p := j.launchEnv(env, "server", "--config", j.configPath, "--state-dir", j.stateDir)
		if code := p.waitBounded(15 * time.Second); code == 0 {
			t.Fatalf("production binary accepted an invalid handoff entry (exit 0):\n%s", p.stderrTail())
		}
		if !p.stderrContains("adopting update handoff") {
			t.Fatalf("launch did not fail closed on the invalid handoff entry:\n%s", p.stderrTail())
		}
		if _, err := os.Stat(j.receiptPath()); !os.IsNotExist(err) {
			t.Fatalf("invalid handoff env produced a receipt at %s (err = %v)", j.receiptPath(), err)
		}
		if entries, err := os.ReadDir(selfupdate.LeaseDir(j.installPath)); err == nil {
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "tx-") {
					t.Fatalf("lease dir carries transaction residue %s", e.Name())
				}
			}
		}
		if got := j.installDigest(); got != bins.prodDigest {
			t.Fatalf("installed bytes changed: %s; want %s", got, bins.prodDigest)
		}
	})
}
