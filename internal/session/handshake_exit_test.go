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
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

type sanitizingHandshakeProtocol struct {
	*blockingHandshakeProtocol
}

func (sanitizingHandshakeProtocol) SanitizeDiagnostic(text string) string {
	return strings.ReplaceAll(text, "provider-held-secret", "[redacted]")
}

func TestStartSessionFailsFastWhenProviderExitsBeforeHandshake(t *testing.T) {
	const launchSecret = "launch-env-secret-value"
	for _, tc := range []struct {
		name          string
		captureStderr bool
		script        string
		env           []string
		sanitize      bool
		want          []string
		absent        []string
	}{
		{
			name:   "stderr kept in memory",
			script: "echo sandbox setup failed >&2; exit 3",
			want:   []string{"exit status 3", "sandbox setup failed"},
		},
		{
			name:          "stderr captured to file",
			captureStderr: true,
			script:        "echo sandbox setup failed >&2; exit 3",
			want:          []string{"exit status 3", "sandbox setup failed"},
		},
		{
			name:     "secrets in stderr are redacted",
			script:   `echo "server password $SERVER_PASSWORD rejected" >&2; echo "token=abcdef0123456789 provider-held-secret" >&2; exit 1`,
			env:      []string{"SERVER_PASSWORD=" + launchSecret},
			sanitize: true,
			want:     []string{"exit status 1", "server password [redacted] rejected", "token=[redacted]"},
			absent:   []string{launchSecret, "abcdef0123456789", "provider-held-secret"},
		},
		{
			name:   "error keeps only the last lines",
			script: "for i in 1 2 3 4 5 6 7 8; do echo line-$i >&2; done; exit 2",
			want:   []string{"line-4", "line-8"},
			absent: []string{"line-3"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eventCh := make(chan interface{}, 64)
			sm := NewManager(eventCh)
			defer sm.Shutdown()
			go func() {
				for range eventCh {
				}
			}()

			blocking := &blockingHandshakeProtocol{started: make(chan struct{}), release: make(chan struct{})}
			opts := &SessionOpts{Protocol: blocking, CodexHandshakeTimeout: time.Minute}
			if tc.sanitize {
				opts.Protocol = sanitizingHandshakeProtocol{blocking}
			}
			if tc.captureStderr {
				opts.StderrPath = filepath.Join(t.TempDir(), "stderr.log")
			}
			start := time.Now()
			_, err := sm.StartSession("early-exit", "feat-1", feature.PhaseDesign,
				[]string{"sh", "-c", tc.script}, t.TempDir(), tc.env, opts)
			if !errors.Is(err, ErrExitedBeforeHandshake) {
				t.Fatalf("StartSession() error = %v, want ErrExitedBeforeHandshake", err)
			}
			if elapsed := time.Since(start); elapsed > 10*time.Second {
				t.Fatalf("StartSession() took %s, want it to return once the child exits", elapsed)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("StartSession() error = %q, want it to contain %q", err, want)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(err.Error(), absent) {
					t.Errorf("StartSession() error = %q, must not contain %q", err, absent)
				}
			}
		})
	}
}
