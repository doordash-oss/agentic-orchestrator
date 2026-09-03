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
	"errors"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

func TestDetectResumeRejectionProviderPatterns(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		detail   string
	}{
		{name: "claude", provider: "claude", detail: "Error: No conversation found for session abc"},
		{name: "codex", provider: "codex", detail: `thread/resume JSON-RPC error: thread not found`},
		{name: "opencode", provider: "opencode", detail: "opencode session/load failed: session not found"},
		// The repo's own generated failure string carries no guaranteed
		// second keyword: "session/load" alone must establish the rejection
		// so the fresh-session fallback still dispatches.
		{name: "opencode keywordless rpc detail", provider: "opencode", detail: `opencode session/load failed for session "ses_x": Unknown session`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sess := newResumeRejectionSession(test.provider, test.detail)
			got := detectResumeRejection(sess, time.Second)
			if !got.Rejected || got.Reason == "" {
				t.Errorf("detectResumeRejection() = %#v, want rejected verdict", got)
			}
		})
	}
}

func TestDetectResumeRejectionRequiresEarlyIdleFailureAndPattern(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*resumeRejectionTestSession)
		elapsed time.Duration
	}{
		{
			name: "ordinary early crash",
			mutate: func(sess *resumeRejectionTestSession) {
				sess.errorDetail = "segmentation fault"
			},
			elapsed: time.Second,
		},
		{
			name: "productive init",
			mutate: func(sess *resumeRejectionTestSession) {
				sess.errorDetail = "No conversation found"
				sess.MessageLog().Append(llm.SDKMessage{Type: "system", Init: &llm.SystemInitMessage{SessionID: "prior"}})
			},
			elapsed: time.Second,
		},
		{
			name: "outside window",
			mutate: func(sess *resumeRejectionTestSession) {
				sess.errorDetail = "No conversation found"
			},
			elapsed: resumeEstablishmentWindow + time.Second,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sess := newResumeRejectionSession("claude", "")
			test.mutate(sess)
			if got := detectResumeRejection(sess, test.elapsed); got.Rejected {
				t.Errorf("detectResumeRejection() = %#v, want ordinary failure", got)
			}
		})
	}
}

func TestDetectResumeStartRejection(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		err      error
		elapsed  time.Duration
		want     bool
	}{
		{
			name:     "claude handshake rejection",
			provider: "claude",
			err:      errors.New("handshake failed: no conversation found"),
			elapsed:  time.Second,
			want:     true,
		},
		{
			name:     "codex handshake rejection",
			provider: "codex",
			err:      errors.New("thread/resume JSON-RPC error: thread not found"),
			elapsed:  time.Second,
			want:     true,
		},
		{
			name:     "opencode handshake rejection",
			provider: "opencode",
			err:      errors.New("session/load failed: session expired"),
			elapsed:  time.Second,
			want:     true,
		},
		{
			name:     "opencode handshake rejection without keyword",
			provider: "opencode",
			err:      errors.New(`opencode session/load failed for session "ses_x": Unknown session`),
			elapsed:  time.Second,
			want:     true,
		},
		{
			name:     "ordinary startup failure",
			provider: "codex",
			err:      errors.New("executable not found"),
			elapsed:  time.Second,
			want:     false,
		},
		{
			name:     "late matching failure",
			provider: "claude",
			err:      errors.New("no conversation found"),
			elapsed:  resumeEstablishmentWindow + time.Second,
			want:     false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := detectResumeStartRejection(test.provider, test.err, test.elapsed)
			if got.Rejected != test.want {
				t.Errorf("detectResumeStartRejection() = %#v, want rejected=%v", got, test.want)
			}
		})
	}
}

type resumeRejectionTestSession struct {
	*utilityTestSession
	providerName string
	errorDetail  string
}

func (s *resumeRejectionTestSession) ProviderName() string { return s.providerName }
func (s *resumeRejectionTestSession) ErrorDetail() string  { return s.errorDetail }

func newResumeRejectionSession(provider, detail string) *resumeRejectionTestSession {
	return &resumeRejectionTestSession{
		utilityTestSession: newUtilityTestSession(),
		providerName:       provider,
		errorDetail:        detail,
	}
}
