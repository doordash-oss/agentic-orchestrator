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

package permission

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const rememberAuditFile = "remember-audit.jsonl"
const maxAuditInputSummary = 240

var auditRedactionPatterns = []struct {
	re          *regexp.Regexp
	replacement string
}{
	{
		re:          regexp.MustCompile(`(?i)\b(authorization\s*:\s*(?:bearer|basic)\s+)[^\s"',;]+`),
		replacement: `${1}[redacted]`,
	},
	{
		re:          regexp.MustCompile(`(?i)\b((?:"|')?(?:authorization|api[_-]?key|access[_-]?token|refresh[_-]?token|auth[_-]?token|id[_-]?token|token|secret|password|passwd|pwd)\b(?:"|')?\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;&}]+)`),
		replacement: `${1}[redacted]`,
	},
	{
		re:          regexp.MustCompile(`(?i)\b(private-token|ghp_[A-Za-z0-9_]{16,}|github_pat_[A-Za-z0-9_]{20,}|sk-[A-Za-z0-9_-]{16,}|xox[baprs]-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{16})\b`),
		replacement: `[redacted]`,
	},
}

var rememberAuditMu sync.Mutex

type RememberAuditEvent struct {
	Time         time.Time `json:"time"`
	SessionID    string    `json:"session_id,omitempty"`
	RequestID    string    `json:"request_id,omitempty"`
	FeatureID    string    `json:"feature_id,omitempty"`
	ToolName     string    `json:"tool_name,omitempty"`
	Decision     string    `json:"decision"`
	Pattern      string    `json:"pattern,omitempty"`
	Scope        string    `json:"scope"`
	InputSummary string    `json:"input_summary,omitempty"`
	Result       string    `json:"result"`
	Error        string    `json:"error,omitempty"`
	Persisted    bool      `json:"persisted"`
	Answered     bool      `json:"answered"`
}

type AuditAppendResult struct {
	Path string
}

type AuditSink struct {
	baseDir string
}

func NewAuditSink(baseDir string) *AuditSink {
	return &AuditSink{baseDir: baseDir}
}

func (s *AuditSink) Append(event RememberAuditEvent) (AuditAppendResult, error) {
	if s == nil || s.baseDir == "" {
		return AuditAppendResult{}, nil
	}
	rememberAuditMu.Lock()
	defer rememberAuditMu.Unlock()

	if event.Time.IsZero() {
		event.Time = time.Now().UTC()
	}
	event.Pattern = sanitizeAuditField(event.Pattern)
	event.Error = sanitizeAuditField(event.Error)
	event.InputSummary = sanitizeAuditInputSummary(event.InputSummary, maxAuditInputSummary)
	if err := os.MkdirAll(s.baseDir, 0o700); err != nil {
		return AuditAppendResult{}, fmt.Errorf("creating permission audit dir: %w", err)
	}
	if err := os.Chmod(s.baseDir, 0o700); err != nil {
		return AuditAppendResult{}, fmt.Errorf("chmod permission audit dir: %w", err)
	}
	path := filepath.Join(s.baseDir, rememberAuditFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return AuditAppendResult{}, fmt.Errorf("opening permission audit log: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return AuditAppendResult{}, fmt.Errorf("chmod permission audit log: %w", err)
	}
	data, err := json.Marshal(event)
	if err != nil {
		_ = f.Close()
		return AuditAppendResult{}, fmt.Errorf("marshaling permission audit event: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return AuditAppendResult{}, fmt.Errorf("writing permission audit event: %w", err)
	}
	if err := f.Close(); err != nil {
		return AuditAppendResult{}, fmt.Errorf("closing permission audit log: %w", err)
	}
	return AuditAppendResult{Path: path}, nil
}

func sanitizeAuditInputSummary(s string, limit int) string {
	return boundAuditText(sanitizeAuditField(s), limit)
}

func sanitizeAuditField(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\x00", ""))
	for _, pattern := range auditRedactionPatterns {
		s = pattern.re.ReplaceAllString(s, pattern.replacement)
	}
	return s
}

// RedactTelemetryText applies the same credential vocabulary used by the
// permission audit log. Telemetry callers add their own path scrubbing and
// size bounds after this shared secret-redaction step.
func RedactTelemetryText(s string) string {
	return sanitizeAuditField(s)
}

func boundAuditText(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	const suffix = "..."
	if limit <= len(suffix) {
		return truncateValidUTF8(s, limit)
	}
	return truncateValidUTF8(s, limit-len(suffix)) + suffix
}

func truncateValidUTF8(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	for limit > 0 && !utf8.ValidString(s[:limit]) {
		limit--
	}
	return s[:limit]
}
