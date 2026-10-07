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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/agent"
	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	serverruntime "github.com/doordash-oss/agentic-orchestrator/internal/server"
)

const (
	cliSubcommandAPI = "api"

	apiPathPrefix         = "/api/v1/"
	apiDefaultTimeout     = 30 * time.Second
	apiTrustedClientValue = "local"
	apiContentTypeJSON    = "application/json"
	apiSSEHeartbeatEvent  = "heartbeat"
)

// apiOptions is one parsed `agentico api` invocation.
type apiOptions struct {
	help       bool
	runtimeDir string
	method     string
	path       string
	body       string
	hasBody    bool
	timeout    time.Duration
	hasTimeout bool
	after      string
	stream     bool
}

// apiMethods are the HTTP methods the helper sends.
var apiMethods = map[string]bool{
	http.MethodGet: true, http.MethodPost: true, http.MethodPut: true, http.MethodPatch: true, http.MethodDelete: true,
}

// parseAPIArgs parses `agentico api` arguments. Flags may appear before or
// after the positionals, in `--flag value` or `--flag=value` form; a bare
// `--` ends flag parsing so a body may begin with "-". Every check here
// runs before any network call.
func parseAPIArgs(opts launchOptions, args []string) (launchOptions, error) {
	api := &opts.api
	var positionals []string
	flagsDone := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if flagsDone || !strings.HasPrefix(arg, "-") {
			positionals = append(positionals, arg)
			continue
		}
		if arg == "--" {
			flagsDone = true
			continue
		}
		if arg == "--help" || arg == "-h" {
			api.help = true
			return opts, nil
		}
		name, value, hasValue := strings.Cut(arg, "=")
		switch name {
		case "--runtime-dir", "--timeout", "--after":
		default:
			return opts, fmt.Errorf("unknown api flag: %s", arg)
		}
		if !hasValue {
			if i+1 >= len(args) {
				return opts, fmt.Errorf("%s requires a value", name)
			}
			i++
			value = args[i]
		}
		switch name {
		case "--runtime-dir":
			if strings.TrimSpace(value) == "" {
				return opts, fmt.Errorf("--runtime-dir requires a value")
			}
			api.runtimeDir = value
		case "--timeout":
			d, err := time.ParseDuration(value)
			if err != nil || d <= 0 {
				return opts, fmt.Errorf("invalid --timeout %q: expected a positive duration such as 30s or 2m", value)
			}
			api.timeout, api.hasTimeout = d, true
		case "--after":
			if _, err := strconv.ParseUint(value, 10, 64); err != nil {
				return opts, fmt.Errorf("invalid --after %q: expected a non-negative event cursor", value)
			}
			api.after = value
		}
	}
	if len(positionals) < 2 {
		return opts, fmt.Errorf("api requires METHOD and PATH, e.g. 'agentico api GET /api/v1/features'")
	}
	if len(positionals) > 3 {
		return opts, fmt.Errorf("api accepts at most METHOD, PATH and one JSON body; quote the body as a single argument")
	}
	api.method = strings.ToUpper(positionals[0])
	if !apiMethods[api.method] {
		return opts, fmt.Errorf("unsupported method %q: expected GET, POST, PUT, PATCH or DELETE", positionals[0])
	}
	api.path = positionals[1]
	if !strings.HasPrefix(api.path, apiPathPrefix) {
		return opts, fmt.Errorf("path %q must start with %s", api.path, apiPathPrefix)
	}
	if len(positionals) == 3 {
		api.body, api.hasBody = positionals[2], true
		if api.method == http.MethodGet {
			return opts, fmt.Errorf("GET requests take no body")
		}
		if !json.Valid([]byte(api.body)) {
			return opts, fmt.Errorf("request body is not valid JSON")
		}
	}
	api.stream = isAPIStreamPath(api.path)
	if api.stream {
		if api.method != http.MethodGet {
			return opts, fmt.Errorf("stream paths accept only GET")
		}
		if !api.hasTimeout {
			return opts, fmt.Errorf("stream path %s requires --timeout <duration>, e.g. --timeout 30s", api.path)
		}
	} else if api.after != "" {
		return opts, fmt.Errorf("--after applies only to stream paths")
	}
	if !api.hasTimeout {
		api.timeout = apiDefaultTimeout
	}
	return opts, nil
}

// isAPIStreamPath reports whether path (query string allowed) names one of
// the server's SSE streams.
func isAPIStreamPath(path string) bool {
	path, _, _ = strings.Cut(path, "?")
	switch path {
	case "/api/v1/events", "/api/v1/supervisor/events":
		return true
	}
	id, ok := strings.CutPrefix(path, "/api/v1/sessions/")
	if !ok {
		return false
	}
	id, ok = strings.CutSuffix(id, "/output/stream")
	return ok && id != "" && !strings.Contains(id, "/")
}

// resolveAPIRuntimeDir picks the runtime directory whose discovery file the
// helper trusts: the flag, then the variable the supervisor launch exports,
// then the default home runtime directory.
func resolveAPIRuntimeDir(flag string) string {
	if flag != "" {
		return flag
	}
	if env := strings.TrimSpace(os.Getenv(agent.RuntimeDirEnv)); env != "" {
		return env
	}
	return pickRuntimeParent()
}

// runAPI performs one authenticated request against this machine's server.
// A 2xx body goes to stdout verbatim (exit 0); any other status writes the
// server's envelope to stdout (exit 1); discovery and transport failures
// render a catalog error on stderr (exit 1). Headers and the token are never
// written anywhere.
func runAPI(opts apiOptions, stdout, stderr io.Writer) int {
	if opts.help {
		printAPIUsage(stdout)
		return 0
	}
	runtimeDir := resolveAPIRuntimeDir(opts.runtimeDir)
	dirParams := errcat.WithParams(errcat.RuntimeDirParams{RuntimeDir: runtimeDir})
	rec, err := serverruntime.ReadTrustedDiscovery(runtimeDir)
	if err != nil {
		code := errcat.DiscoveryUntrusted
		if errors.Is(err, serverruntime.ErrDiscoveryMissing) {
			code = errcat.DiscoveryMissing
		}
		renderError(stderr, code, dirParams, errcat.WithDiagnostics(err.Error()))
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	var body io.Reader
	if opts.hasBody {
		body = strings.NewReader(opts.body)
	}
	req, err := http.NewRequestWithContext(ctx, opts.method, strings.TrimRight(rec.BaseURL, "/")+opts.path, body)
	if err != nil {
		renderError(stderr, errcat.InvalidUsage, errcat.WithParams(errcat.UsageParams{Reason: fmt.Sprintf("invalid request path %q", opts.path)}))
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+rec.AuthToken)
	req.Header.Set("X-Agentico-Client", apiTrustedClientValue)
	if opts.stream {
		req.Header.Set("Accept", "text/event-stream")
		if opts.after != "" {
			// Every SSE route reads Last-Event-ID as its resume cursor, so
			// the helper never rewrites the caller's query string.
			req.Header.Set("Last-Event-ID", opts.after)
		}
	} else {
		req.Header.Set("Accept", apiContentTypeJSON)
	}
	if opts.hasBody {
		req.Header.Set("Content-Type", apiContentTypeJSON)
	}

	resp, err := newAPIHTTPClient().Do(req)
	if err != nil {
		renderError(stderr, errcat.ServerUnreachable, dirParams, errcat.WithDiagnostics(err.Error()))
		return 1
	}
	defer resp.Body.Close()
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	if opts.stream && ok {
		return copySSEData(ctx, resp.Body, stdout, stderr, dirParams)
	}
	if _, err := io.Copy(stdout, resp.Body); err != nil {
		renderError(stderr, errcat.ServerUnreachable, dirParams, errcat.WithDiagnostics("reading response: "+err.Error()))
		return 1
	}
	if !ok {
		return 1
	}
	return 0
}

// newAPIHTTPClient returns a client that never follows redirects and never
// routes through an environment proxy, so the bearer token only ever
// travels to the base URL the server itself published.
func newAPIHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// copySSEData prints one stdout line per SSE event carrying its data
// payload, skipping comments, heartbeats and data-less events. Each line is
// a single write so a pipe reader sees it immediately. The stream ends with
// exit 0 at the timeout or when the server closes it.
func copySSEData(ctx context.Context, body io.Reader, stdout, stderr io.Writer, dirParams errcat.Option) int {
	reader := bufio.NewReader(body)
	var event string
	var data []string
	for {
		line, err := reader.ReadString('\n')
		if err == nil || line != "" {
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if len(data) > 0 && event != apiSSEHeartbeatEvent {
					// Multi-line data joins with a space rather than the
					// spec's newline: one event stays one line, and JSON
					// payloads keep their meaning.
					if _, werr := io.WriteString(stdout, strings.Join(data, " ")+"\n"); werr != nil {
						return 1
					}
					if f, ok := stdout.(interface{ Flush() error }); ok {
						_ = f.Flush()
					}
				}
				event, data = "", nil
			case strings.HasPrefix(line, ":"):
			default:
				field, value, _ := strings.Cut(line, ":")
				value = strings.TrimPrefix(value, " ")
				switch field {
				case "event":
					event = value
				case "data":
					data = append(data, value)
				}
			}
		}
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return 0
			}
			renderError(stderr, errcat.ServerUnreachable, dirParams, errcat.WithDiagnostics("reading event stream: "+err.Error()))
			return 1
		}
	}
}

func printAPIUsage(w io.Writer) {
	_, _ = fmt.Fprintf(w, `Usage: agentico api [--runtime-dir <dir>] [--timeout <duration>] [--after <cursor>] METHOD /api/v1/<path>[?query] [json]

Performs one authenticated REST call against this machine's Agentico server.
The helper reads the server's owner-only discovery file, adds the bearer
token and trusted-client header itself, and never prints headers or the
token, so shell callers never handle the credential.

  METHOD                  GET, POST, PUT, PATCH or DELETE (case-insensitive)
  /api/v1/<path>          Absolute API path; a query string is allowed
  json                    Optional request body; must be valid JSON; not allowed on GET

Flags (before or after the positionals; '--' ends flag parsing):
  --runtime-dir <dir>     Runtime directory holding the discovery file. Default:
                          $%s, else ~/.agentic-orchestrator
  --timeout <duration>    Request timeout (default 30s). Required for stream paths,
                          where it bounds how long events are printed
  --after <cursor>        Stream paths only: resume after this event cursor

Output: a 2xx response body is written to stdout verbatim (exit 0); any other
status writes the server's error envelope to stdout (exit 1). Discovery or
connection failures render an error on stderr (exit 1).

Stream paths (/api/v1/events, /api/v1/supervisor/events,
/api/v1/sessions/<id>/output/stream) print one line per event carrying its
data payload, skip heartbeats, and exit 0 when the timeout elapses or the
server closes the stream.
`, agent.RuntimeDirEnv)
}
