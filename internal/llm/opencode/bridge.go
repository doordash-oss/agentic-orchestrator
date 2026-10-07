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

package opencode

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// The child-session bridge.
//
// OpenCode's ACP agent never forwards permission requests raised inside
// task-spawned child sessions (it returns without replying when the asking
// session is not in its ACP registry), and it never forwards question.asked
// for any session. Both are visible on the HTTP server OpenCode embeds beside
// ACP. For interactive sessions the command builder pins that server to a
// loopback port with a per-launch password; after the handshake the protocol
// follows GET /event, turns child permission requests and every question into
// control requests, and answers them through the HTTP reply endpoints.
//
// Bridged events reach the session through ParseLine, in order with stdout:
// the protocol interposes on the process's stdout and writes each event as one
// nonce-tagged line, so the session's single reader dispatches them like any
// other provider message.

const (
	serverPasswordEnvVar = "OPENCODE_SERVER_PASSWORD"
	questionToolEnvVar   = "OPENCODE_ENABLE_QUESTION_TOOL"
	serverUsername       = "opencode"
	serverHost           = "127.0.0.1"
)

// bridgeConnectTimeout bounds how long the handshake waits for the event
// stream. OpenCode's ACP agent is itself a client of the embedded server, so
// the server is up by the time session/new answers; the bound only matters
// when it is not, and the launch must then fail rather than hang.
const bridgeConnectTimeout = 10 * time.Second

// Reconnect backoff for a dropped event stream, matching the desktop's
// event-stream consumers.
const (
	bridgeBackoffInitial = 250 * time.Millisecond
	bridgeBackoffMax     = 5 * time.Second
)

// bridgeRequestTimeout bounds one HTTP request other than the event stream.
const bridgeRequestTimeout = 10 * time.Second

// withServerBridge adds the embedded HTTP server the bridge talks to: a free
// loopback port chosen now (OpenCode does not report a port it picks), a
// random server password, and the question tool. The password is registered
// with the diagnostic redaction before it is returned.
func withServerBridge(args, env []string) ([]string, []string, error) {
	port, err := freeLoopbackPort()
	if err != nil {
		return nil, nil, fmt.Errorf("allocating a loopback port for the OpenCode server: %w", err)
	}
	password, err := randomServerPassword()
	if err != nil {
		return nil, nil, fmt.Errorf("generating the OpenCode server password: %w", err)
	}
	registerLaunchSecret(password)
	args = append(args, "--port", strconv.Itoa(port), "--hostname", serverHost)
	env = append(env, serverPasswordEnvVar+"="+password, questionToolEnvVar+"=1")
	return args, env, nil
}

func freeLoopbackPort() (int, error) {
	l, err := net.Listen("tcp", net.JoinHostPort(serverHost, "0"))
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func randomServerPassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// serverEndpointFromLaunch recovers the server address and password the
// command builder chose for this launch. ok is false when the launch carries
// no server (non-interactive sessions, or a protocol built without a launch).
func serverEndpointFromLaunch(args, env []string) (serverClient, bool) {
	var host, port string
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--hostname":
			host = args[i+1]
		case "--port":
			port = args[i+1]
		}
	}
	var password string
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == serverPasswordEnvVar {
			password = v
		}
	}
	if host == "" || port == "" || password == "" {
		return serverClient{}, false
	}
	return serverClient{addr: net.JoinHostPort(host, port), password: password}, true
}

// serverClient speaks to OpenCode's embedded HTTP server with the per-launch
// credential. Errors never carry the password: it travels only in the
// Authorization header, which the HTTP client never echoes.
type serverClient struct {
	addr     string
	password string
	http     *http.Client
}

// errServerCredentialsRejected marks a 401 or 403 from the embedded server.
var errServerCredentialsRejected = errors.New("OpenCode's HTTP server rejected the Agentico server credentials")

func (c *serverClient) client() *http.Client {
	if c.http == nil {
		// Loopback traffic must never be routed through a configured proxy.
		c.http = &http.Client{Transport: &http.Transport{Proxy: nil, DisableCompression: true}}
	}
	return c.http
}

func (c *serverClient) request(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+c.addr+path, rdr)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(serverUsername, c.password)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return nil, errors.New(sanitizeDiagnostic(err.Error()))
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		return nil, fmt.Errorf("%w (HTTP %d on %s %s)", errServerCredentialsRejected, resp.StatusCode, method, path)
	}
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, fmt.Errorf("OpenCode's HTTP server answered %s %s with HTTP %d", method, path, resp.StatusCode)
	}
	return resp, nil
}

// postJSON sends one JSON request and discards the response body.
func (c *serverClient) postJSON(ctx context.Context, path string, body any) error {
	ctx, cancel := context.WithTimeout(ctx, bridgeRequestTimeout)
	defer cancel()
	resp, err := c.request(ctx, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

// getJSON decodes one JSON response into out.
func (c *serverClient) getJSON(ctx context.Context, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, bridgeRequestTimeout)
	defer cancel()
	resp, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

// bridgedEventTypes are the event-stream types the bridge forwards; every
// other type (heartbeats, message and session updates) is dropped.
var bridgedEventTypes = map[string]bool{
	"permission.asked":   true,
	"permission.replied": true,
	"question.asked":     true,
	"question.replied":   true,
	"question.rejected":  true,
}

// bridgeLinePrefix opens every line the bridge writes into the stdout stream.
var bridgeLinePrefix = []byte(`{"agenticoBridge":`)

// Bridge line kinds.
const (
	bridgeLineEvent          = "event"
	bridgeLineReconcileBegin = "reconcile_begin"
	bridgeLineReconcileEnd   = "reconcile_end"
)

// bridgeLine is one bridged record in the stdout stream. Nonce proves the
// line came from this protocol's bridge, not from OpenCode's stdout.
type bridgeLine struct {
	Nonce       string          `json:"agenticoBridge"`
	Kind        string          `json:"kind"`
	Seq         int             `json:"seq,omitempty"`
	Type        string          `json:"type,omitempty"`
	Properties  json.RawMessage `json:"properties,omitempty"`
	Permissions []string        `json:"permissions,omitempty"`
	Questions   []string        `json:"questions,omitempty"`
}

// serverBridge owns the event-stream follower and the merged stdout stream.
type serverBridge struct {
	client serverClient
	nonce  string
	logf   func(string, ...interface{})

	ctx    context.Context
	cancel context.CancelFunc

	attached atomic.Bool
	writeMu  sync.Mutex
	sink     *io.PipeWriter
	closed   atomic.Bool
}

func newServerBridge(client serverClient, logf func(string, ...interface{})) *serverBridge {
	nonce, _ := randomServerPassword()
	ctx, cancel := context.WithCancel(context.Background())
	return &serverBridge{client: client, nonce: nonce, logf: logf, ctx: ctx, cancel: cancel}
}

// interposedStdout is the merged stream the session reads.
type interposedStdout struct {
	*io.PipeReader
	stdout io.ReadCloser
}

func (s *interposedStdout) Close() error {
	_ = s.PipeReader.Close()
	return s.stdout.Close()
}

// interpose merges stdout with the bridge's lines. Whole lines are copied
// under the write lock so a bridged line never splits a stdout line. The
// merged stream ends when stdout ends, which also stops the bridge.
func (b *serverBridge) interpose(stdout io.ReadCloser) io.ReadCloser {
	pr, pw := io.Pipe()
	b.sink = pw
	b.attached.Store(true)
	go func() {
		r := bufio.NewReaderSize(stdout, 64<<10)
		for {
			line, err := r.ReadBytes('\n')
			if len(line) > 0 && b.write(line) != nil {
				break
			}
			if err != nil {
				break
			}
		}
		b.stop()
	}()
	return &interposedStdout{PipeReader: pr, stdout: stdout}
}

func (b *serverBridge) write(line []byte) error {
	if b.closed.Load() {
		return io.ErrClosedPipe
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	_, err := b.sink.Write(line)
	return err
}

// stop ends the bridge and the merged stream. It does not take the write
// lock: closing the pipe is what releases a writer blocked on a reader that
// has gone away.
func (b *serverBridge) stop() {
	if b.closed.Swap(true) {
		return
	}
	b.cancel()
	if b.sink != nil {
		_ = b.sink.Close()
	}
}

func (b *serverBridge) emit(line bridgeLine) error {
	line.Nonce = b.nonce
	raw, err := json.Marshal(line)
	if err != nil {
		return err
	}
	return b.write(append(raw, '\n'))
}

// start opens the event stream and hands it to the follower. It fails when
// the server cannot be reached or rejects the credentials before the
// handshake deadline, so the launch fails instead of running without the
// bridge.
func (b *serverBridge) start(ctx context.Context) error {
	if !b.attached.Load() {
		return errors.New("OpenCode child-session bridge requires the session to read stdout through the protocol")
	}
	ctx, cancel := context.WithTimeout(ctx, bridgeConnectTimeout)
	defer cancel()
	delay := 50 * time.Millisecond
	var lastErr error
	for {
		resp, err := b.openEvents(ctx)
		if err == nil {
			go b.follow(resp)
			return nil
		}
		if errors.Is(err, errServerCredentialsRejected) {
			return err
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return fmt.Errorf("OpenCode's HTTP server on %s did not open its event stream before the handshake deadline: %v", b.client.addr, lastErr)
		case <-time.After(delay):
		}
		delay = min(delay*2, 500*time.Millisecond)
	}
}

// openEvents issues GET /event. The stream lives on the bridge's context;
// connectCtx bounds only the wait for the response headers.
func (b *serverBridge) openEvents(connectCtx context.Context) (*http.Response, error) {
	reqCtx, reqCancel := context.WithCancel(b.ctx)
	stop := context.AfterFunc(connectCtx, reqCancel)
	resp, err := b.client.request(reqCtx, http.MethodGet, "/event", nil)
	if !stop() || err != nil {
		reqCancel()
		if resp != nil {
			resp.Body.Close()
		}
		if err == nil {
			err = connectCtx.Err()
		}
		return nil, err
	}
	return &http.Response{StatusCode: resp.StatusCode, Body: &cancelOnClose{ReadCloser: resp.Body, cancel: reqCancel}}, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	c.cancel()
	return c.ReadCloser.Close()
}

// follow consumes the event stream for the life of the process. Before each
// stream it reconciles against the pending lists, so a request raised while
// no stream was open still reaches the session.
func (b *serverBridge) follow(resp *http.Response) {
	seq := 0
	for {
		seq++
		if b.reconcile(seq) != nil && b.ctx.Err() != nil {
			resp.Body.Close()
			return
		}
		b.consume(resp.Body)
		resp.Body.Close()
		if b.ctx.Err() != nil {
			return
		}
		if resp = b.reconnect(); resp == nil {
			return
		}
	}
}

func (b *serverBridge) reconnect() *http.Response {
	delay := bridgeBackoffInitial
	for {
		select {
		case <-b.ctx.Done():
			return nil
		case <-time.After(delay):
		}
		ctx, cancel := context.WithTimeout(b.ctx, bridgeRequestTimeout)
		resp, err := b.openEvents(ctx)
		cancel()
		if err == nil {
			return resp
		}
		b.logf("[opencode] event stream reconnect failed: %v", err)
		delay = min(delay*2, bridgeBackoffMax)
	}
}

// reconcile replays what is still pending. The begin and end markers let
// ParseLine tell a held request that vanished from the lists (answered while
// no stream was open) from one asked after the lists were read.
func (b *serverBridge) reconcile(seq int) error {
	if err := b.emit(bridgeLine{Kind: bridgeLineReconcileBegin, Seq: seq}); err != nil {
		return err
	}
	end := bridgeLine{Kind: bridgeLineReconcileEnd, Seq: seq}
	for _, list := range []struct {
		path, kind string
		ids        *[]string
	}{
		{"/permission", "permission.asked", &end.Permissions},
		{"/question", "question.asked", &end.Questions},
	} {
		var pending []json.RawMessage
		if err := b.client.getJSON(b.ctx, list.path, &pending); err != nil {
			b.logf("[opencode] reconcile %s failed: %v", list.path, err)
			return err
		}
		for _, item := range pending {
			var head struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(item, &head) != nil || head.ID == "" {
				continue
			}
			*list.ids = append(*list.ids, head.ID)
			if err := b.emit(bridgeLine{Kind: bridgeLineEvent, Type: list.kind, Properties: item}); err != nil {
				return err
			}
		}
	}
	return b.emit(end)
}

// consume forwards the bridged events of one stream until it ends.
func (b *serverBridge) consume(body io.Reader) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	var data strings.Builder
	flush := func() bool {
		if data.Len() == 0 {
			return true
		}
		raw := data.String()
		data.Reset()
		var ev struct {
			Type       string          `json:"type"`
			Properties json.RawMessage `json:"properties"`
		}
		if json.Unmarshal([]byte(raw), &ev) != nil || !bridgedEventTypes[ev.Type] {
			return true
		}
		return b.emit(bridgeLine{Kind: bridgeLineEvent, Type: ev.Type, Properties: ev.Properties}) == nil
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if !flush() {
				return
			}
			continue
		}
		if payload, ok := strings.CutPrefix(line, "data:"); ok {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(payload, " "))
		}
	}
	flush()
}

// InterposeStdout implements llm.StdoutInterposer. Sessions without the
// bridge read stdout unchanged.
func (p *Protocol) InterposeStdout(stdout io.ReadCloser) io.ReadCloser {
	if p.bridge == nil {
		return stdout
	}
	return p.bridge.interpose(stdout)
}

var _ llm.StdoutInterposer = (*Protocol)(nil)
