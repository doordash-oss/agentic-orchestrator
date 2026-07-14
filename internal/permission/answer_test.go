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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// Test fixture literals shared across this package's test files.
const (
	testPermID    = "perm-1"
	testSessionID = "sess-1"
	testFeatureID = "feat-1"
	testRepoA     = "repo-a"
	testMyRepo    = "my-repo"
	testLsLa      = "ls -la"
	testRmRfRoot  = "rm -rf /"
	testNpmTest   = "npm test"

	testNpmTestCoverage = "npm test --coverage"
	testGoTestDotDotDot = "go test ./..."
	testFilePath        = "/path/to/file"
	patternEditFilePath = "Edit(/path/to/file)"

	testGoTestBashInput     = `{"command":"go test ./internal/permission"}`
	testJSONNpmTestCoverage = `{"command":"npm test --coverage"}`
	testJSONLsLa            = `{"command":"ls -la"}`
)

func TestAnswerServiceAllowRememberPersistsBeforeAnswerAndAudits(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cache := NewCache(NewStore(filepath.Join(dir, "permissions")))
	audit := NewAuditSink(filepath.Join(dir, "permissions"))
	service := NewAnswerService(cache, audit)
	var calls []answerCall

	result, err := service.Answer(AnswerRequest{
		RequestID:        testPermID,
		SessionID:        testSessionID,
		FeatureID:        testFeatureID,
		ToolName:         toolNameBash,
		ToolInput:        testGoTestBashInput,
		Decision:         DecisionAllowRemember,
		RememberPattern:  patternBashGoTest,
		RememberScope:    testRepoA,
		RememberScopeSet: true,
	}, appendAnswerCall(&calls))
	if err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
	if len(calls) != 1 || calls[0] != (answerCall{requestID: testPermID, allow: true}) {
		t.Fatalf("answer calls = %+v, want one allow", calls)
	}
	if !result.Persisted || !result.Answered || result.AlreadyExisted || result.AuditPath == "" || result.AuditWarning != "" {
		t.Fatalf("result = %+v, want persisted answered audited", result)
	}
	rules, err := NewStore(filepath.Join(dir, "permissions")).Load(scopeFor(testRepoA))
	if err != nil {
		t.Fatalf("Load(repo-a) error = %v", err)
	}
	if len(rules) != 1 || rules[0].ToolPattern != patternBashGoTest || rules[0].Effect != DecisionAllow || rules[0].RepoName != testRepoA {
		t.Fatalf("rules = %+v, want remembered allow", rules)
	}
	event := readSingleAuditEvent(t, result.AuditPath)
	if event.Result != rememberAuditResultSuccess || !event.Persisted || !event.Answered {
		t.Fatalf("audit event = %+v, want successful persisted+answered", event)
	}
}

func TestAnswerServiceAllowRememberPersistenceFailureDoesNotAnswer(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	blockingFile := filepath.Join(dir, "permissions")
	if err := os.WriteFile(blockingFile, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}
	cache := NewCache(NewStore(blockingFile))
	audit := NewAuditSink(filepath.Join(dir, "audit"))
	service := NewAnswerService(cache, audit)
	var calls []answerCall

	result, err := service.Answer(AnswerRequest{
		RequestID:        testPermID,
		SessionID:        testSessionID,
		FeatureID:        testFeatureID,
		ToolName:         toolNameBash,
		ToolInput:        testGoTestBashInput,
		Decision:         DecisionAllowRemember,
		RememberPattern:  patternBashGoTest,
		RememberScope:    testRepoA,
		RememberScopeSet: true,
	}, appendAnswerCall(&calls))
	if err == nil {
		t.Fatal("Answer() error = nil, want persistence failure")
	}
	if len(calls) != 0 {
		t.Fatalf("answer calls = %+v, want none", calls)
	}
	if result.Answered || result.Persisted {
		t.Fatalf("result = %+v, want not persisted or answered", result)
	}
	event := readSingleAuditEvent(t, filepath.Join(dir, "audit", rememberAuditFile))
	if event.Result != rememberAuditResultPersistFailed || event.Answered || event.Persisted || event.Error == "" {
		t.Fatalf("audit event = %+v, want persist_failed without answer", event)
	}
}

func TestAnswerServiceAllowRememberDuplicateSkipsAuditButAnswers(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cache := NewCache(NewStore(filepath.Join(dir, "permissions")))
	auditDir := filepath.Join(dir, "audit")
	service := NewAnswerService(cache, NewAuditSink(auditDir))
	req := AnswerRequest{
		RequestID:        testPermID,
		SessionID:        testSessionID,
		FeatureID:        testFeatureID,
		ToolName:         toolNameBash,
		ToolInput:        testGoTestBashInput,
		Decision:         DecisionAllowRemember,
		RememberPattern:  patternBashGoTest,
		RememberScope:    testRepoA,
		RememberScopeSet: true,
	}
	if _, err := service.Answer(req, func(string, bool, string) error { return nil }); err != nil {
		t.Fatalf("Answer(first) error = %v", err)
	}

	var calls []answerCall
	req.RequestID = "perm-2"
	result, err := service.Answer(req, appendAnswerCall(&calls))
	if err != nil {
		t.Fatalf("Answer(duplicate) error = %v", err)
	}
	if len(calls) != 1 || !calls[0].allow {
		t.Fatalf("answer calls = %+v, want duplicate answered", calls)
	}
	if !result.AlreadyExisted || result.AuditPath != "" {
		t.Fatalf("result = %+v, want already_existed with skipped audit", result)
	}
	data, err := os.ReadFile(filepath.Join(auditDir, rememberAuditFile))
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	if lines := strings.Split(strings.TrimSpace(string(data)), "\n"); len(lines) != 1 {
		t.Fatalf("audit lines = %d, want only first remember audited", len(lines))
	}
}

func TestAnswerServiceAllowRememberAnswerFailureKeepsRuleAndAudits(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cache := NewCache(NewStore(filepath.Join(dir, "permissions")))
	audit := NewAuditSink(filepath.Join(dir, "audit"))
	service := NewAnswerService(cache, audit)
	answerErr := errors.New("session closed with token=answer-secret")

	result, err := service.Answer(AnswerRequest{
		RequestID:        testPermID,
		SessionID:        testSessionID,
		FeatureID:        testFeatureID,
		ToolName:         toolNameBash,
		ToolInput:        testGoTestBashInput,
		Decision:         DecisionAllowRemember,
		RememberPattern:  patternBashGoTest,
		RememberScope:    testRepoA,
		RememberScopeSet: true,
	}, func(string, bool, string) error {
		return answerErr
	})
	if err == nil {
		t.Fatal("Answer() error = nil, want answer failure")
	}
	if !result.Persisted || result.Answered {
		t.Fatalf("result = %+v, want persisted but not answered", result)
	}
	if _, ok := cache.Check(toolNameBash, testGoTestBashInput, testRepoA); !ok {
		t.Fatal("remembered rule missing after answer failure")
	}
	event := readSingleAuditEvent(t, result.AuditPath)
	if event.Result != rememberAuditResultAnswerFailed || !event.Persisted || event.Answered {
		t.Fatalf("audit event = %+v, want answer_failed after persistence", event)
	}
	if strings.Contains(event.Error, "answer-secret") {
		t.Fatalf("audit error was not redacted: %q", event.Error)
	}
}

func TestAnswerServiceAuditFailureIsWarningOnly(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	blockingFile := filepath.Join(dir, "audit")
	if err := os.WriteFile(blockingFile, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}
	service := NewAnswerService(NewCache(NewStore(filepath.Join(dir, "permissions"))), NewAuditSink(blockingFile))
	var calls []answerCall

	result, err := service.Answer(AnswerRequest{
		RequestID:        testPermID,
		SessionID:        testSessionID,
		FeatureID:        testFeatureID,
		ToolName:         toolNameBash,
		ToolInput:        testGoTestBashInput,
		Decision:         DecisionAllowRemember,
		RememberPattern:  patternBashGoTest,
		RememberScope:    testRepoA,
		RememberScopeSet: true,
	}, appendAnswerCall(&calls))
	if err != nil {
		t.Fatalf("Answer() error = %v, want audit warning only", err)
	}
	if len(calls) != 1 || !calls[0].allow {
		t.Fatalf("answer calls = %+v, want answered", calls)
	}
	if result.AuditWarning == "" || result.AuditPath != "" || !result.Persisted || !result.Answered {
		t.Fatalf("result = %+v, want audit warning with persisted answer", result)
	}
}

func TestAnswerServiceAllowOnceAndDenyDoNotRemember(t *testing.T) {
	t.Parallel()

	service := NewAnswerService(NewCache(nil), NewAuditSink(t.TempDir()))
	for _, tc := range []struct {
		name       string
		decision   string
		wantAllow  bool
		wantReason string
	}{
		{name: "allow once", decision: DecisionAllowOnce, wantAllow: true},
		{name: DecisionDeny, decision: DecisionDeny, wantAllow: false, wantReason: "denied by user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []answerCall
			result, err := service.Answer(AnswerRequest{
				RequestID: testPermID,
				Decision:  tc.decision,
			}, appendAnswerCall(&calls))
			if err != nil {
				t.Fatalf("Answer() error = %v", err)
			}
			if len(calls) != 1 || calls[0] != (answerCall{requestID: testPermID, allow: tc.wantAllow, reason: tc.wantReason}) {
				t.Fatalf("answer calls = %+v, want allow=%t reason=%q", calls, tc.wantAllow, tc.wantReason)
			}
			if !result.Answered || result.Persisted || result.AuditPath != "" {
				t.Fatalf("result = %+v, want answered without remember/audit", result)
			}
		})
	}
}

func TestAnswerServiceRejectsInvalidRememberRequests(t *testing.T) {
	t.Parallel()

	service := NewAnswerService(NewCache(nil), nil)
	for _, tc := range []struct {
		name string
		req  AnswerRequest
	}{
		{name: "unknown decision", req: AnswerRequest{RequestID: testPermID, Decision: DecisionAllow}},
		{name: "missing cache", req: AnswerRequest{RequestID: testPermID, Decision: DecisionAllowRemember, RememberPattern: patternBashGoTest, RememberScopeSet: true}},
		{name: "missing pattern", req: AnswerRequest{RequestID: testPermID, Decision: DecisionAllowRemember, RememberScopeSet: true}},
		{name: "missing scope", req: AnswerRequest{RequestID: testPermID, Decision: DecisionAllowRemember, RememberPattern: patternBashGoTest}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []answerCall
			_, err := service.Answer(tc.req, appendAnswerCall(&calls))
			if err == nil {
				t.Fatal("Answer() error = nil, want validation error")
			}
			if len(calls) != 0 {
				t.Fatalf("answer calls = %+v, want none", calls)
			}
		})
	}
}

type answerCall struct {
	requestID string
	allow     bool
	reason    string
}

func appendAnswerCall(calls *[]answerCall) AnswerFunc {
	return func(requestID string, allow bool, reason string) error {
		*calls = append(*calls, answerCall{requestID: requestID, allow: allow, reason: reason})
		return nil
	}
}

func TestAuditLogWritesBoundedJSONLEvent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sink := NewAuditSink(dir)
	wantTime := time.Unix(100, 0).UTC()
	event := RememberAuditEvent{
		Time:         wantTime,
		SessionID:    testSessionID,
		RequestID:    testPermID,
		FeatureID:    testFeatureID,
		ToolName:     toolNameBash,
		Decision:     DecisionAllowRemember,
		Pattern:      patternBashGoTest,
		Scope:        testRepoA,
		InputSummary: strings.Repeat("x", 500),
		Result:       rememberAuditResultSuccess,
		Persisted:    true,
		Answered:     true,
	}
	result, err := sink.Append(event)
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("audit lines = %d, want 1", len(lines))
	}
	var got RememberAuditEvent
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("audit line is not JSON: %v", err)
	}
	if !got.Time.Equal(wantTime) {
		t.Fatalf("Time = %v, want %v", got.Time, wantTime)
	}
	if got.SessionID != testSessionID || got.RequestID != testPermID || got.FeatureID != testFeatureID {
		t.Fatalf("audit identifiers = session %q request %q feature %q, want sess-1 perm-1 feat-1", got.SessionID, got.RequestID, got.FeatureID)
	}
	if got.ToolName != toolNameBash || got.Decision != DecisionAllowRemember {
		t.Fatalf("audit decision = tool %q decision %q, want Bash allow_remember", got.ToolName, got.Decision)
	}
	if got.Pattern != patternBashGoTest || got.Scope != testRepoA || got.Result != rememberAuditResultSuccess {
		t.Fatalf("audit remembered fields = pattern %q scope %q result %q, want remembered success", got.Pattern, got.Scope, got.Result)
	}
	if got.InputSummary == "" {
		t.Fatal("InputSummary is empty, want bounded summary")
	}
	if len(got.InputSummary) > 240 {
		t.Fatalf("input summary length = %d, want bounded", len(got.InputSummary))
	}
	if !got.Persisted || !got.Answered {
		t.Fatalf("audit booleans = persisted %t answered %t, want true true", got.Persisted, got.Answered)
	}
}

func TestAuditLogSanitizesInputSummary(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sink := NewAuditSink(dir)
	event := RememberAuditEvent{
		Decision:     DecisionAllowRemember,
		Pattern:      `Bash(echo token=pattern-secret *)`,
		Scope:        testRepoA,
		InputSummary: ` {"Authorization":"Bearer json-auth-secret","password":"json-secret","api_key":"json-key"} Authorization: Bearer bearer-secret token=plain-secret api_key="quoted-secret" echo private-token ` + "\x00",
		Result:       rememberAuditResultSuccess,
		Error:        "failed with secret=error-secret",
	}
	result, err := sink.Append(event)
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	got := readSingleAuditEvent(t, result.Path)
	for _, forbidden := range []string{"json-auth-secret", "json-secret", "json-key", "bearer-secret", "plain-secret", "quoted-secret", "private-token", "\x00"} {
		if strings.Contains(got.InputSummary, forbidden) || strings.Contains(got.Pattern, forbidden) || strings.Contains(got.Error, forbidden) {
			t.Fatalf("audit event = %+v, contains forbidden %q", got, forbidden)
		}
	}
	if count := strings.Count(got.InputSummary, "[redacted]"); count < 4 {
		t.Fatalf("InputSummary = %q, want credential redactions", got.InputSummary)
	}
	if strings.Contains(got.Pattern, "pattern-secret") || strings.Contains(got.Error, "error-secret") {
		t.Fatalf("audit pattern/error not redacted: pattern=%q error=%q", got.Pattern, got.Error)
	}
}

func TestAuditLogDefaultsTimeAndAppendsJSONLines(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sink := NewAuditSink(dir)
	before := time.Now().UTC()
	first, err := sink.Append(RememberAuditEvent{Decision: DecisionAllowRemember, Scope: testRepoA, Result: rememberAuditResultSuccess})
	if err != nil {
		t.Fatalf("Append(first) error = %v", err)
	}
	if _, err := sink.Append(RememberAuditEvent{Decision: DecisionAllowRemember, Scope: testRepoA, Result: "failed", Error: "boom"}); err != nil {
		t.Fatalf("Append(second) error = %v", err)
	}
	after := time.Now().UTC()

	data, err := os.ReadFile(first.Path)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("audit lines = %d, want 2", len(lines))
	}
	var got RememberAuditEvent
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("first audit line is not JSON: %v", err)
	}
	if got.Time.Before(before) || got.Time.After(after) {
		t.Fatalf("default Time = %v, want between %v and %v", got.Time, before, after)
	}
	info, err := os.Stat(first.Path)
	if err != nil {
		t.Fatalf("stat audit log: %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Fatalf("audit log mode = %#o, want 0600", gotMode)
	}
}

func TestAuditLogHardensExistingFileMode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, rememberAuditFile)
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatalf("write audit log: %v", err)
	}
	if _, err := NewAuditSink(dir).Append(RememberAuditEvent{Decision: DecisionAllowRemember, Scope: testRepoA, Result: rememberAuditResultSuccess}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat audit log: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("audit log mode = %#o, want 0600", got)
	}
}

func TestAuditLogConcurrentAppendsRemainJSONLines(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sink := NewAuditSink(dir)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := sink.Append(RememberAuditEvent{Decision: DecisionAllowRemember, Scope: testRepoA, Result: rememberAuditResultSuccess}); err != nil {
				t.Errorf("Append() error = %v", err)
			}
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(filepath.Join(dir, rememberAuditFile))
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 20 {
		t.Fatalf("audit lines = %d, want 20", len(lines))
	}
	for _, line := range lines {
		var event RememberAuditEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("audit line is not JSON: %v\nline: %s", err, line)
		}
	}
}

func TestAuditLogNoopSinkDoesNotCreateFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if result, err := (*AuditSink)(nil).Append(RememberAuditEvent{Decision: DecisionAllowRemember}); err != nil || result.Path != "" {
		t.Fatalf("nil Append() = (%+v, %v), want zero nil", result, err)
	}
	if result, err := NewAuditSink("").Append(RememberAuditEvent{Decision: DecisionAllowRemember}); err != nil || result.Path != "" {
		t.Fatalf("empty Append() = (%+v, %v), want zero nil", result, err)
	}
	if _, err := os.Stat(filepath.Join(dir, rememberAuditFile)); !os.IsNotExist(err) {
		t.Fatalf("audit file stat error = %v, want not exist", err)
	}
}

func TestAuditLogTruncatesWithoutSplittingUTF8(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sink := NewAuditSink(dir)
	result, err := sink.Append(RememberAuditEvent{
		Decision:     DecisionAllowRemember,
		Scope:        testRepoA,
		InputSummary: strings.Repeat("界", 200),
		Result:       rememberAuditResultSuccess,
	})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	got := readSingleAuditEvent(t, result.Path)
	if len(got.InputSummary) > maxAuditInputSummary {
		t.Fatalf("input summary length = %d, want <= %d", len(got.InputSummary), maxAuditInputSummary)
	}
	if !utf8.ValidString(got.InputSummary) {
		t.Fatalf("InputSummary is not valid UTF-8: %q", got.InputSummary)
	}
	if !strings.HasSuffix(got.InputSummary, "...") {
		t.Fatalf("InputSummary = %q, want ellipsis suffix", got.InputSummary)
	}
}

func readSingleAuditEvent(t *testing.T, path string) RememberAuditEvent {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("audit lines = %d, want 1", len(lines))
	}
	var got RememberAuditEvent
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("audit line is not JSON: %v", err)
	}
	return got
}
