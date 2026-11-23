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

package session

import (
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// ErrExitedBeforeHandshake reports a provider process that exited before its
// protocol handshake completed.
var ErrExitedBeforeHandshake = ports.ErrProviderExitedBeforeHandshake

const (
	stderrTailBytes = 4096
	stderrDrainWait = 200 * time.Millisecond
	// The error keeps a few readable lines; the log keeps the whole tail.
	stderrErrorLines   = 5
	stderrErrorLineLen = 200
	minLaunchSecretLen = 8
)

var launchSecretEnvName = regexp.MustCompile(`(?i)(KEY|SECRET|TOKEN|PASSWORD|PASSWD|CREDENTIAL|AUTH)`)

// launchSecretValues returns the credential-looking values of a child's
// launch environment.
func launchSecretValues(env []string) []string {
	var vals []string
	for _, kv := range env {
		name, val, ok := strings.Cut(kv, "=")
		if ok && len(val) >= minLaunchSecretLen && launchSecretEnvName.MatchString(name) {
			vals = append(vals, val)
		}
	}
	return vals
}

// stderrTail keeps the last stderrTailBytes a child wrote to stderr.
type stderrTail struct {
	mu   sync.Mutex
	buf  []byte
	done chan struct{}
}

func newStderrTail(r io.ReadCloser) *stderrTail {
	t := &stderrTail{done: make(chan struct{})}
	go func() {
		defer close(t.done)
		defer func() { _ = r.Close() }()
		chunk := make([]byte, 1024)
		for {
			n, err := r.Read(chunk)
			if n > 0 {
				t.mu.Lock()
				t.buf = append(t.buf, chunk[:n]...)
				if over := len(t.buf) - stderrTailBytes; over > 0 {
					t.buf = append(t.buf[:0], t.buf[over:]...)
				}
				t.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	return t
}

func (t *stderrTail) String() string {
	select {
	case <-t.done:
	case <-time.After(stderrDrainWait):
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

func readFileTail(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	if info, err := f.Stat(); err == nil && info.Size() > stderrTailBytes {
		_, _ = f.Seek(info.Size()-stderrTailBytes, io.SeekStart)
	}
	b, _ := io.ReadAll(io.LimitReader(f, stderrTailBytes))
	return string(b)
}

// redactProviderText scrubs launch credentials, provider-known secrets and
// common credential shapes, and drops control characters.
func redactProviderText(text string, secrets []string, protocol llm.Protocol) string {
	for _, v := range secrets {
		text = strings.ReplaceAll(text, v, "[redacted]")
	}
	if sanitizer, ok := protocol.(llm.DiagnosticSanitizer); ok {
		text = sanitizer.SanitizeDiagnostic(text)
	}
	text = permission.RedactTelemetryText(text)
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, text)
}

// lastLines keeps the final n non-empty lines, each truncated to width runes.
func lastLines(text string, n, width int) string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if r := []rune(line); len(r) > width {
				line = string(r[:width]) + "..."
			}
			lines = append(lines, line)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// earlyExitError describes a child that exited mid-handshake: its exit status
// and the redacted tail of its stderr.
func (s *Session) earlyExitError() error {
	s.mu.Lock()
	proc, tail, path, secrets, protocol := s.process, s.stderrTail, s.stderrPath, s.launchSecrets, s.protocol
	s.mu.Unlock()
	status := "exit status unknown"
	if proc != nil && proc.ProcessState != nil {
		status = proc.ProcessState.String()
	}
	stderr := ""
	switch {
	case path != "":
		stderr = readFileTail(path)
	case tail != nil:
		stderr = tail.String()
	}
	stderr = strings.TrimSpace(redactProviderText(stderr, secrets, protocol))
	if stderr == "" {
		return fmt.Errorf("%w: %s", ErrExitedBeforeHandshake, status)
	}
	log.Printf("session %s: provider exited before the handshake (%s); stderr:\n%s", s.id, status, stderr)
	return fmt.Errorf("%w: %s; stderr: %s", ErrExitedBeforeHandshake, status, lastLines(stderr, stderrErrorLines, stderrErrorLineLen))
}
