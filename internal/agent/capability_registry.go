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

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Built-in capability names a plan may declare without a probe command. The
// harness resolves each to a deterministic in-process probe so planners write
// a name rather than a shell snippet.
const (
	CapabilityAuthenticatedBrowser = "authenticated-browser"
	CapabilityDisplay              = "display"
	CapabilityDocker               = "docker"
	CapabilityNetwork              = "network"
)

// capabilityProbeTimeout bounds one built-in probe, matching the executor's
// shell-probe budget.
const capabilityProbeTimeout = 30 * time.Second

// CapabilityPolicy configures built-in probes for one server. It travels on
// the verification context so executor signatures stay stable.
type CapabilityPolicy struct {
	// RuntimeDir is the parent of the feature state dir. Browser storage
	// state lives under <RuntimeDir>/capabilities/browser/<host>/.
	RuntimeDir string
	// AllowBrowserState permits the authenticated-browser capability. Servers
	// exposed on the network default to false: a signed-in session cookie
	// must never be provisioned onto a shared remote sandbox.
	AllowBrowserState bool
	// HTTPClient performs liveness requests; nil uses a bounded default.
	HTTPClient *http.Client
	// DialContext performs network probes; nil uses net.Dialer.
	DialContext func(ctx context.Context, network, address string) (net.Conn, error)
}

type capabilityPolicyKey struct{}

// WithCapabilityPolicy attaches the server's capability policy to ctx.
func WithCapabilityPolicy(ctx context.Context, policy CapabilityPolicy) context.Context {
	return context.WithValue(ctx, capabilityPolicyKey{}, policy)
}

// CapabilityPolicyFromContext returns the attached policy or a local default.
func CapabilityPolicyFromContext(ctx context.Context) CapabilityPolicy {
	if ctx != nil {
		if policy, ok := ctx.Value(capabilityPolicyKey{}).(CapabilityPolicy); ok {
			return policy
		}
	}
	return CapabilityPolicy{AllowBrowserState: true}
}

// CapabilityProbeResult is the outcome of one built-in probe.
type CapabilityProbeResult struct {
	Available bool
	// Reason is a one-line, user-actionable explanation when unavailable.
	Reason string
	// Env carries values the capability makes available to the checked
	// item, e.g. the browser storage-state path.
	Env []string
}

var capabilityDeclRE = regexp.MustCompile(`^([a-z][a-z0-9-]*)(?:\(([^()]*)\))?$`)

// ParseCapabilityName splits "name(arg)" into its registry name and argument.
// ok is false when the text is not a registry-shaped declaration.
func ParseCapabilityName(text string) (name, arg string, ok bool) {
	m := capabilityDeclRE.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return "", "", false
	}
	return m[1], strings.TrimSpace(m[2]), true
}

// ValidateRegistryCapability checks a declaration's shape and argument so a
// planner defect surfaces at contract compile time rather than at probe time.
func ValidateRegistryCapability(name, arg string) error {
	switch name {
	case CapabilityAuthenticatedBrowser, CapabilityNetwork:
		if arg == "" {
			return fmt.Errorf("capability %q requires a host argument, e.g. %s(example.com)", name, name)
		}
		if _, _, err := splitHostPort(arg); err != nil {
			return fmt.Errorf("capability %q has an invalid host %q: %v", name, arg, err)
		}
	case CapabilityDisplay, CapabilityDocker:
		if arg != "" {
			return fmt.Errorf("capability %q takes no argument", name)
		}
	default:
		return fmt.Errorf("unknown built-in capability %q", name)
	}
	return nil
}

// RunRegistryCapabilityProbe executes the built-in probe for name(arg).
func RunRegistryCapabilityProbe(ctx context.Context, name, arg string) CapabilityProbeResult {
	if err := ValidateRegistryCapability(name, arg); err != nil {
		return CapabilityProbeResult{Reason: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, capabilityProbeTimeout)
	defer cancel()
	policy := CapabilityPolicyFromContext(ctx)
	switch name {
	case CapabilityAuthenticatedBrowser:
		return probeAuthenticatedBrowser(ctx, policy, arg)
	case CapabilityDisplay:
		return probeDisplay()
	case CapabilityDocker:
		return probeDocker(ctx, policy)
	case CapabilityNetwork:
		return probeNetwork(ctx, policy, arg)
	}
	return CapabilityProbeResult{Reason: fmt.Sprintf("unknown built-in capability %q", name)}
}

// BrowserStatePath is the harness-owned location of Playwright storage state
// for host under runtimeDir.
func BrowserStatePath(runtimeDir, host string) string {
	return filepath.Join(runtimeDir, "capabilities", "browser", browserStateDirName(host), "storageState.json")
}

// BrowserStateEnvName returns the environment variable that carries the
// storage-state path for host, e.g. AGENTICO_BROWSER_STATE_SLACK_COM.
func BrowserStateEnvName(host string) string {
	upper := strings.ToUpper(host)
	var b strings.Builder
	for _, r := range upper {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return "AGENTICO_BROWSER_STATE_" + b.String()
}

func browserStateDirName(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-' {
			return r
		}
		return '_'
	}, host)
}

// playwrightStorageState is the subset of Playwright's storageState.json the
// probe reads.
type playwrightStorageState struct {
	Cookies []playwrightCookie `json:"cookies"`
}

// playwrightCookie is one saved cookie. Value is used only to build the
// liveness request and is never logged or persisted by the probe.
type playwrightCookie struct {
	Name    string  `json:"name"`
	Value   string  `json:"value"`
	Domain  string  `json:"domain"`
	Expires float64 `json:"expires"`
}

// browserLiveness maps a host to the authenticated landing URL and the
// sign-in redirect patterns that prove a session is not live.
type browserLiveness struct {
	URL    string
	SignIn *regexp.Regexp
}

var browserLivenessByHost = map[string]browserLiveness{
	"slack.com": {URL: "https://app.slack.com/block-kit-builder", SignIn: regexp.MustCompile(`(?i)signin|workspace-signin|sso|ssb/redirect`)},
}

var genericSignInRE = regexp.MustCompile(`(?i)login|signin|sign-in|sso|/auth`)

func livenessForHost(host string) browserLiveness {
	lower := strings.ToLower(host)
	for known, liveness := range browserLivenessByHost {
		if lower == known || strings.HasSuffix(lower, "."+known) {
			return liveness
		}
	}
	return browserLiveness{URL: "https://" + host + "/", SignIn: genericSignInRE}
}

func probeAuthenticatedBrowser(ctx context.Context, policy CapabilityPolicy, host string) CapabilityProbeResult {
	if !policy.AllowBrowserState {
		return CapabilityProbeResult{Reason: "authenticated browser state is not permitted on this server (remote servers deny signed-in browser sessions by default)"}
	}
	if strings.TrimSpace(policy.RuntimeDir) == "" {
		return CapabilityProbeResult{Reason: "authenticated browser state has no runtime directory configured"}
	}
	statePath := BrowserStatePath(policy.RuntimeDir, host)
	data, err := os.ReadFile(statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return CapabilityProbeResult{Reason: fmt.Sprintf("no signed-in browser state for %s; sign in through Agentico to create %s", host, statePath)}
		}
		return CapabilityProbeResult{Reason: fmt.Sprintf("reading browser state %s: %v", statePath, err)}
	}
	var state playwrightStorageState
	if err := json.Unmarshal(data, &state); err != nil {
		return CapabilityProbeResult{Reason: fmt.Sprintf("browser state %s is not Playwright storage state: %v", statePath, err)}
	}
	now := time.Now()
	matched, live := 0, 0
	for _, cookie := range state.Cookies {
		domain := strings.TrimPrefix(strings.ToLower(cookie.Domain), ".")
		if domain != strings.ToLower(host) && !strings.HasSuffix(domain, "."+strings.ToLower(host)) && !strings.HasSuffix(strings.ToLower(host), "."+domain) {
			continue
		}
		matched++
		if cookie.Expires <= 0 || time.Unix(int64(cookie.Expires), 0).After(now) {
			live++
		}
	}
	if matched == 0 {
		return CapabilityProbeResult{Reason: fmt.Sprintf("browser state %s holds no cookies for %s", statePath, host)}
	}
	if live == 0 {
		return CapabilityProbeResult{Reason: fmt.Sprintf("every %s cookie in %s has expired; sign in again", host, statePath)}
	}
	if reason := checkBrowserLiveness(ctx, policy, host, state); reason != "" {
		return CapabilityProbeResult{Reason: reason}
	}
	return CapabilityProbeResult{Available: true, Env: []string{BrowserStateEnvName(host) + "=" + statePath}}
}

func checkBrowserLiveness(ctx context.Context, policy CapabilityPolicy, host string, state playwrightStorageState) string {
	liveness := livenessForHost(host)
	client := policy.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, liveness.URL, nil)
	if err != nil {
		return fmt.Sprintf("building liveness request for %s: %v", host, err)
	}
	target, _ := url.Parse(liveness.URL)
	for _, cookie := range state.Cookies {
		domain := strings.TrimPrefix(strings.ToLower(cookie.Domain), ".")
		if target != nil && (strings.EqualFold(target.Hostname(), domain) || strings.HasSuffix(strings.ToLower(target.Hostname()), "."+domain)) {
			req.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Sprintf("liveness request to %s failed: %v", host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Sprintf("%s rejected the saved session (HTTP %d); sign in again", host, resp.StatusCode)
	}
	final := liveness.URL
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
	}
	if final != liveness.URL && liveness.SignIn.MatchString(final) {
		return fmt.Sprintf("%s redirected to sign-in (%s); the saved session is no longer live", host, redactURL(final))
	}
	return ""
}

func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<redacted>"
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func probeDisplay() CapabilityProbeResult {
	if strings.TrimSpace(os.Getenv("DISPLAY")) != "" || strings.TrimSpace(os.Getenv("WAYLAND_DISPLAY")) != "" {
		return CapabilityProbeResult{Available: true}
	}
	if _, err := os.Stat("/System/Library/CoreServices/WindowServer"); err == nil {
		return CapabilityProbeResult{Available: true}
	}
	return CapabilityProbeResult{Reason: "no display: DISPLAY and WAYLAND_DISPLAY are unset"}
}

func probeDocker(ctx context.Context, policy CapabilityPolicy) CapabilityProbeResult {
	socket := strings.TrimSpace(os.Getenv("DOCKER_HOST"))
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	u, err := url.Parse(socket)
	if err != nil {
		return CapabilityProbeResult{Reason: fmt.Sprintf("invalid DOCKER_HOST %q", socket)}
	}
	network, address := "tcp", u.Host
	if u.Scheme == "unix" {
		network, address = "unix", u.Path
	}
	conn, err := dial(ctx, policy, network, address)
	if err != nil {
		return CapabilityProbeResult{Reason: fmt.Sprintf("docker daemon unreachable at %s: %v", socket, err)}
	}
	_ = conn.Close()
	return CapabilityProbeResult{Available: true}
}

func probeNetwork(ctx context.Context, policy CapabilityPolicy, hostPort string) CapabilityProbeResult {
	host, port, _ := splitHostPort(hostPort)
	if port == "" {
		port = "443"
	}
	conn, err := dial(ctx, policy, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return CapabilityProbeResult{Reason: fmt.Sprintf("cannot reach %s: %v", net.JoinHostPort(host, port), err)}
	}
	_ = conn.Close()
	return CapabilityProbeResult{Available: true}
}

func dial(ctx context.Context, policy CapabilityPolicy, network, address string) (net.Conn, error) {
	if policy.DialContext != nil {
		return policy.DialContext(ctx, network, address)
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return dialer.DialContext(ctx, network, address)
}

func splitHostPort(text string) (host, port string, err error) {
	text = strings.TrimSpace(text)
	if strings.Contains(text, "://") || strings.ContainsAny(text, "/ \t") {
		return "", "", errors.New("expected host or host:port")
	}
	if h, p, splitErr := net.SplitHostPort(text); splitErr == nil {
		host, port = h, p
	} else {
		host = text
	}
	if host == "" {
		return "", "", errors.New("empty host")
	}
	return host, port, nil
}
