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
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/autoreview"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil"
)

// stubHandler returns a fixed decision/error for every request.
type stubHandler struct {
	decision ports.PermissionDecision
	err      error
	calls    int
}

func (s *stubHandler) CanUseTool(req ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	s.calls++
	return s.decision, s.err
}

func bashReq(input string) ports.ToolPermissionRequest {
	return ports.ToolPermissionRequest{ToolName: "Bash", Input: input}
}

func TestDecoratorReturnsExistingAllow(t *testing.T) {
	inner := &stubHandler{decision: ports.PermissionDecision{Behavior: "allow"}}
	d := &autoReviewPermissionDecorator{inner: inner, reviewer: autoreview.Reviewer{Provider: fakeAllowProvider(t), Model: "haiku[200K]"}}
	got, err := d.CanUseTool(bashReq(`{"command":"go test ./..."}`))
	if err != nil || got.Behavior != "allow" || inner.calls != 1 {
		t.Fatalf("existing allow not returned: got %+v err %v calls %d", got, err, inner.calls)
	}
}

func TestDecoratorReturnsExistingDeny(t *testing.T) {
	inner := &stubHandler{decision: ports.PermissionDecision{Behavior: "deny", Reason: "nope"}}
	d := &autoReviewPermissionDecorator{inner: inner, reviewer: autoreview.Reviewer{Provider: fakeAllowProvider(t), Model: "haiku[200K]"}}
	got, err := d.CanUseTool(bashReq(`{"command":"go test ./..."}`))
	if err != nil || got.Behavior != "deny" {
		t.Fatalf("existing deny not returned: got %+v err %v", got, err)
	}
}

func TestDecoratorReturnsInnerError(t *testing.T) {
	want := errors.New("boom")
	inner := &stubHandler{err: want}
	d := &autoReviewPermissionDecorator{inner: inner, reviewer: autoreview.Reviewer{Provider: fakeAllowProvider(t), Model: "haiku[200K]"}}
	_, err := d.CanUseTool(bashReq(`{"command":"go test ./..."}`))
	if !errors.Is(err, want) {
		t.Fatalf("inner error not returned: got %v", err)
	}
}

func TestDecoratorNonBashDefers(t *testing.T) {
	inner := &stubHandler{}
	d := &autoReviewPermissionDecorator{inner: inner, reviewer: autoreview.Reviewer{Provider: fakeAllowProvider(t), Model: "haiku[200K]"}}
	got, err := d.CanUseTool(ports.ToolPermissionRequest{ToolName: "Read", Input: `{}`})
	if err != nil || got.Behavior != "" {
		t.Fatalf("non-Bash should defer: got %+v err %v", got, err)
	}
}

func TestDecoratorIneligibleCommandDefers(t *testing.T) {
	inner := &stubHandler{}
	d := &autoReviewPermissionDecorator{inner: inner, reviewer: autoreview.Reviewer{Provider: fakeAllowProvider(t), Model: "haiku[200K]"}}
	got, err := d.CanUseTool(bashReq(`{"command":"rm -rf /"}`))
	if err != nil || got.Behavior != "" {
		t.Fatalf("ineligible command should defer: got %+v err %v", got, err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner should have been asked once, got %d", inner.calls)
	}
}

func TestDecoratorEligibleAllowApproves(t *testing.T) {
	inner := &stubHandler{}
	d := &autoReviewPermissionDecorator{inner: inner, reviewer: autoreview.Reviewer{Provider: fakeAllowProvider(t), Model: "haiku[200K]"}}
	got, err := d.CanUseTool(bashReq(`{"command":"go test ./..."}`))
	if err != nil || got.Behavior != "allow" {
		t.Fatalf("eligible ALLOW should approve: got %+v err %v", got, err)
	}
	got2, err := d.CanUseTool(bashReq(`{"command":"git status --short"}`))
	if err != nil || got2.Behavior != "allow" {
		t.Fatalf("eligible ALLOW should approve git status: got %+v err %v", got2, err)
	}
}

func TestDecoratorDeferReturnsEmpty(t *testing.T) {
	inner := &stubHandler{}
	d := &autoReviewPermissionDecorator{inner: inner, reviewer: autoreview.Reviewer{Provider: fakeDeferProvider(t), Model: "haiku[200K]"}}
	got, err := d.CanUseTool(bashReq(`{"command":"go test ./..."}`))
	if err != nil || got.Behavior != "" {
		t.Fatalf("DEFER should defer to human: got %+v err %v", got, err)
	}
}

func TestDecoratorReviewerFailureDefers(t *testing.T) {
	inner := &stubHandler{}
	d := &autoReviewPermissionDecorator{inner: inner, reviewer: autoreview.Reviewer{Provider: fakeMalformedProvider(t), Model: "haiku[200K]"}}
	got, err := d.CanUseTool(bashReq(`{"command":"go test ./..."}`))
	if err != nil || got.Behavior != "" {
		t.Fatalf("reviewer failure should defer: got %+v err %v", got, err)
	}
}

func TestDecoratorNoReviewerDefers(t *testing.T) {
	inner := &stubHandler{}
	d := &autoReviewPermissionDecorator{inner: inner, reviewer: autoreview.Reviewer{}}
	got, err := d.CanUseTool(bashReq(`{"command":"go test ./..."}`))
	if err != nil || got.Behavior != "" {
		t.Fatalf("no reviewer should defer: got %+v err %v", got, err)
	}
}

func TestDecoratorIneligibleVariantsDefer(t *testing.T) {
	inner := &stubHandler{}
	d := &autoReviewPermissionDecorator{inner: inner, reviewer: autoreview.Reviewer{Provider: fakeAllowProvider(t), Model: "haiku[200K]"}}
	// The structural guardrail replaces exact-string matching. These variants
	// defer because of structural or policy rejection, not whitespace tolerance.
	for _, in := range []string{
		`{"command":"go test ./... && echo done"}`,   // compound with ineligible segment
		`{"command":"go test ./../"}`,                // parent escape
		`{"command":"rm -rf /"}`,                     // not in policy
		`{"command":"go test -exec ./runner ./..."}`, // hazardous flag
	} {
		got, err := d.CanUseTool(bashReq(in))
		if err != nil || got.Behavior != "" {
			t.Errorf("variant %s should defer, got %+v err %v", in, got, err)
		}
	}
}

func TestDecoratorEligibleVariantsApprove(t *testing.T) {
	inner := &stubHandler{}
	d := &autoReviewPermissionDecorator{inner: inner, reviewer: autoreview.Reviewer{Provider: fakeAllowProvider(t), Model: "haiku[200K]"}}
	// The structural guardrail recognizes these commands as eligible.
	for _, in := range []string{
		`{"command":"go test -v ./..."}`,                  // safe flag
		`{"command":"go test ./... 2>/dev/null"}`,         // accepted redirect
		`{"command":"go build ./... && go test ./..."}`,   // eligible compound
		`{"command":"git --no-pager diff --no-textconv"}`, // git with --no-pager and --no-textconv
		`{"command":"cargo test"}`,                        // Rust test
		`{"command":"npm test"}`,                          // JS test
		`{"command":"make test"}`,                         // Make target
		`{"command":"pytest"}`,                            // Python test
		`{"command":"go test -run TestFoo ./..."}`,        // value flag with separate value
	} {
		got, err := d.CanUseTool(bashReq(in))
		if err != nil || got.Behavior != "allow" {
			t.Errorf("variant %s should be eligible+approved, got %+v err %v", in, got, err)
		}
	}
}

// fakeAllowProvider returns a FakeClaudeProvider whose script emits ALLOW.
func fakeAllowProvider(t *testing.T) llm.LLMProvider {
	t.Helper()
	return testutil.FakeClaudeProvider{Script: testutil.WriteFakeClaudeScript(t, testutil.FakeClaudeAllowScriptBody())}
}

// fakeDeferProvider returns a FakeClaudeProvider whose script emits DEFER.
func fakeDeferProvider(t *testing.T) llm.LLMProvider {
	t.Helper()
	return testutil.FakeClaudeProvider{Script: testutil.WriteFakeClaudeScript(t, testutil.FakeClaudeDeferScriptBody())}
}

// fakeMalformedProvider returns a FakeClaudeProvider whose script emits prose.
func fakeMalformedProvider(t *testing.T) llm.LLMProvider {
	t.Helper()
	return testutil.FakeClaudeProvider{Script: testutil.WriteFakeClaudeScript(t, testutil.FakeClaudeMalformedScriptBody())}
}

// newFakeRegistryForAutoReview creates a registry with a FakeClaudeProvider
// whose CheckBareAuth returns true (unlike the real claude.Provider, which
// requires CheckReadiness to cache bareAuthOK). The script is unused because
// these tests verify snapshot/restore behavior, not classification.
func newFakeRegistryForAutoReview() *llm.Registry {
	reg := llm.NewRegistry()
	reg.Register(testutil.FakeClaudeProvider{})
	return reg
}

// TestDecorateWithAutoReviewSnapshot verifies that the snapshot returned by
// decorateWithAutoReview is used on crash-resume rather than the current
// workspace config. This ensures a workspace edit between crash and resume
// does not change the resumed session's reviewer policy. The snapshot must
// also capture the resolved reviewer identity so crash-resume can restore
// the same reviewer even if the provider/catalog state changed.
func TestDecorateWithAutoReviewSnapshot(t *testing.T) {
	dir := t.TempDir()
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(nil, store, dir)
	pr.Registry = newFakeRegistryForAutoReview()
	pr.Config = &config.Config{Defaults: config.DefaultsConfig{
		AutomaticReviewEnabled: true,
		Models:                 config.ModelConfig{AutomaticReview: ""},
	}}

	original := permission.Guarded(&permission.AutoApproveHandler{})
	composed := permission.WrapGeneralPhaseHandlerWithSafeCreate(original, nil)

	// First call: no snapshot in opts → reads from config, resolves reviewer,
	// returns snapshot with resolved reviewer identity.
	opts := BuildSessionOpts{}
	handler1, snap1 := pr.decorateWithAutoReview(composed, original, &opts, dir, nil)
	if snap1.Enabled == nil || !*snap1.Enabled {
		t.Fatalf("first call: expected enabled=true from config")
	}
	if handler1 == composed {
		t.Fatalf("first call: expected decorated handler when enabled")
	}
	if snap1.ReviewerProvider != "claude" {
		t.Fatalf("first call: expected ReviewerProvider=claude, got %q", snap1.ReviewerProvider)
	}
	if snap1.ReviewerModel == "" {
		t.Fatalf("first call: expected non-empty ReviewerModel")
	}

	// Simulate workspace edit: disable auto-review in the config.
	pr.Config.Defaults.AutomaticReviewEnabled = false

	// Second call with snapshot: should use the snapshot, not the edited config.
	// The reviewer identity is restored from the snapshot, not re-resolved.
	opts2 := BuildSessionOpts{
		AutoReview: snap1,
	}
	handler2, snap2 := pr.decorateWithAutoReview(composed, original, &opts2, dir, nil)
	if snap2.Enabled == nil || !*snap2.Enabled {
		t.Fatalf("second call: expected enabled=true from snapshot, not edited config")
	}
	if handler2 == composed {
		t.Fatalf("second call: expected decorated handler from snapshot")
	}
	if snap2.Model != snap1.Model {
		t.Fatalf("second call: model = %q, want %q (snapshot)", snap2.Model, snap1.Model)
	}
	if snap2.ReviewerProvider != snap1.ReviewerProvider || snap2.ReviewerModel != snap1.ReviewerModel {
		t.Fatalf("second call: reviewer identity = (%q,%q), want (%q,%q) (snapshot)",
			snap2.ReviewerProvider, snap2.ReviewerModel, snap1.ReviewerProvider, snap1.ReviewerModel)
	}

	// Third call without snapshot: should read the edited config (disabled).
	opts3 := BuildSessionOpts{}
	handler3, snap3 := pr.decorateWithAutoReview(composed, original, &opts3, dir, nil)
	if snap3.Enabled != nil && *snap3.Enabled {
		t.Fatalf("third call: expected enabled=false from edited config")
	}
	if handler3 != composed {
		t.Fatalf("third call: expected undecorated handler when disabled")
	}
}

// TestCrashResumeRetainsReviewerAcrossProviderChange verifies that a
// crash-resume session retains the original session's resolved reviewer even
// when the provider is no longer available in the registry. The reviewer is
// restored from the snapshot's identity fields, not re-resolved. When the
// provider is gone, RestoreReviewer returns an empty Reviewer so the decorator
// defers to the human prompt — matching the original session's behavior had
// the reviewer failed, but preserving the intent that the logical session does
// not gain or lose a reviewer due to environment changes.
func TestCrashResumeRetainsReviewerAcrossProviderChange(t *testing.T) {
	dir := t.TempDir()
	store := feature.NewStore(dir)
	pr := NewPhaseRunner(nil, store, dir)
	pr.Registry = newFakeRegistryForAutoReview()
	pr.Config = &config.Config{Defaults: config.DefaultsConfig{
		AutomaticReviewEnabled: true,
		Models:                 config.ModelConfig{AutomaticReview: ""},
	}}

	original := permission.Guarded(&permission.AcceptEditsHandler{})
	composed := permission.WrapGeneralPhaseHandlerWithSafeCreate(original, nil)

	// Build a session: resolves a claude reviewer and snapshots it.
	opts := BuildSessionOpts{}
	_, snap := pr.decorateWithAutoReview(composed, original, &opts, dir, nil)
	if snap.ReviewerProvider != "claude" || snap.ReviewerModel == "" {
		t.Fatalf("expected resolved claude reviewer in snapshot, got (%q,%q)",
			snap.ReviewerProvider, snap.ReviewerModel)
	}

	// Simulate crash-resume with the provider removed from the registry.
	// The snapshot's identity is used to attempt restoration; since the
	// provider is gone, RestoreReviewer returns empty and the decorator
	// defers (no crash, no panic, no new reviewer).
	emptyReg := llm.NewRegistry()
	pr.Registry = emptyReg

	opts2 := BuildSessionOpts{AutoReview: snap}
	handler2, snap2 := pr.decorateWithAutoReview(composed, original, &opts2, dir, nil)
	if snap2.ReviewerProvider != snap.ReviewerProvider || snap2.ReviewerModel != snap.ReviewerModel {
		t.Fatalf("resume should preserve snapshot identity, got (%q,%q) want (%q,%q)",
			snap2.ReviewerProvider, snap2.ReviewerModel, snap.ReviewerProvider, snap.ReviewerModel)
	}
	// The decorator was created (enabled=true) but with an empty reviewer,
	// so exact-command Bash defers to the human prompt.
	got, err := handler2.CanUseTool(bashReq(`{"command":"go test ./..."}`))
	if err != nil || got.Behavior != "" {
		t.Fatalf("resume with missing provider should defer: got %+v err %v", got, err)
	}

	// Simulate crash-resume with the provider available again. The
	// snapshot's model id is used as-is — the decorator is created with
	// the restored reviewer, matching the original session's resolved
	// reviewer rather than re-resolving against the current catalog.
	pr.Registry = newFakeRegistryForAutoReview()
	opts3 := BuildSessionOpts{AutoReview: snap}
	handler3, snap3 := pr.decorateWithAutoReview(composed, original, &opts3, dir, nil)
	if handler3 == composed {
		t.Fatalf("resume with provider available should decorate (enabled=true from snapshot)")
	}
	if snap3.ReviewerProvider != snap.ReviewerProvider || snap3.ReviewerModel != snap.ReviewerModel {
		t.Fatalf("resume should preserve snapshot identity, got (%q,%q) want (%q,%q)",
			snap3.ReviewerProvider, snap3.ReviewerModel, snap.ReviewerProvider, snap.ReviewerModel)
	}
}

// --- Integration-style decorator tests (moved from E2E) ---
// These exercise decorateHandlerWithAutoReview through realistic composed
// handlers, verifying the full default-off-to-enabled journey including
// edge cases. They were previously in the E2E package calling the exported
// DecorateWithAutoReview; the composition helper is now unexported and these
// tests live alongside the code they exercise.

// composedGeneralHandler mirrors BuildSession's output for a general-phase
// handler: CreateWithinRoots(SizeGuard(AcceptEdits)). AcceptEdits defers Bash
// (empty decision), so eligible Bash reaches the decorator. Returns the
// composed handler and the original (pre-safe-create) handler.
func composedGeneralHandler() (ports.PermissionHandler, ports.PermissionHandler) {
	original := permission.Guarded(&permission.AcceptEditsHandler{})
	return permission.WrapGeneralPhaseHandlerWithSafeCreate(original, nil), original
}

// agentFakeRegistry creates a Registry with a single FakeClaudeProvider
// running a script built from the given body.
func agentFakeRegistry(t *testing.T, scriptBody string) *llm.Registry {
	t.Helper()
	return testutil.NewFakeClaudeRegistry(t, testutil.WriteFakeClaudeScript(t, scriptBody))
}

// denyBashHandler denies Bash and defers everything else, standing in for a
// deny-wins cached decision.
type denyBashHandler struct{}

func (denyBashHandler) CanUseTool(req ports.ToolPermissionRequest) (ports.PermissionDecision, error) {
	if req.ToolName == "Bash" {
		return ports.PermissionDecision{Behavior: "deny", Reason: "cached deny"}, nil
	}
	return ports.PermissionDecision{}, nil
}

func TestIntegrationDefaultOffDefersExactCommand(t *testing.T) {
	composed, original := composedGeneralHandler()
	handler := decorateHandlerWithAutoReview(composed, original, false, autoreview.Reviewer{}, "", nil)
	got, err := handler.CanUseTool(bashReq(`{"command":"go test ./..."}`))
	if err != nil || got.Behavior != "" {
		t.Fatalf("default-off should defer to human: got %+v err %v", got, err)
	}
}

func TestIntegrationEnabledAllowApprovesBothExactCommands(t *testing.T) {
	reg := agentFakeRegistry(t, testutil.FakeClaudeAllowScriptBody())
	reviewer, ok := autoreview.ResolveReviewer(reg, "")
	if !ok {
		t.Fatalf("ResolveReviewer = false, want true")
	}
	composed, original := composedGeneralHandler()
	handler := decorateHandlerWithAutoReview(composed, original, true, reviewer, "", nil)
	for _, cmd := range []string{"go test ./...", "git status --short"} {
		got, err := handler.CanUseTool(bashReq(`{"command":"` + cmd + `"}`))
		if err != nil || got.Behavior != "allow" {
			t.Errorf("enabled+ALLOW for %q = %+v err %v, want allow", cmd, got, err)
		}
	}
}

func TestIntegrationEnabledDeferPreservesHumanPrompt(t *testing.T) {
	reg := agentFakeRegistry(t, testutil.FakeClaudeDeferScriptBody())
	reviewer, ok := autoreview.ResolveReviewer(reg, "")
	if !ok {
		t.Fatalf("ResolveReviewer = false, want true")
	}
	composed, original := composedGeneralHandler()
	handler := decorateHandlerWithAutoReview(composed, original, true, reviewer, "", nil)
	got, err := handler.CanUseTool(bashReq(`{"command":"go test ./..."}`))
	if err != nil || got.Behavior != "" {
		t.Fatalf("enabled+DEFER should defer: got %+v err %v", got, err)
	}
}

func TestIntegrationCloseVariantsDeferWithoutModelCall(t *testing.T) {
	reg := agentFakeRegistry(t, testutil.FakeClaudeAllowScriptBody())
	reviewer, ok := autoreview.ResolveReviewer(reg, "")
	if !ok {
		t.Fatalf("ResolveReviewer = false, want true")
	}
	composed, original := composedGeneralHandler()
	handler := decorateHandlerWithAutoReview(composed, original, true, reviewer, "", nil)
	// The structural guardrail rejects these for structural or policy reasons.
	// Using an ALLOW provider verifies zero reviewer calls: if the guardrail
	// incorrectly passed the command, the decorator would return "allow".
	for _, cmd := range []string{
		"go test ./../",
		"go test ./... && echo done",
		"rm -rf /",
		"go test -exec ./runner ./...",
		"go list -export -toolexec=./runner ./...",
		"go list -export -toolexec ./runner ./...",
		// Quoted hazardous flags must defer — quoting does not change flag semantics
		"go test '-exec' ./runner ./...",
		"eslint '--plugin' evil .",
		"protoc '--plugin=./evil' foo.proto",
		"pytest '-p' myplugin",
		"mocha '--require' foo",
		// External wrapper paths must defer as direct scripts
		"/tmp/gradlew test",
		"../gradlew test",
		"./untrusted/gradlew test",
		// Sensitive bare basenames must defer
		"prettier --write .env",
		"gcc -o .git/hooks/pre-commit main.c",
		"go build -o .git/hooks/pre-commit ./cmd/app",
		"go build -o .claude/settings.json ./cmd/app",
		// Git sensitive pathspecs must defer
		"git status /etc/passwd",
		"git --no-pager show --no-textconv HEAD:.env",
		// Helper/plugin flags must defer
		"gcc -plugin foo.so main.c",
		"javac -processor foo Main.java",
		"pytest --cov-config=evil.ini",
		"pytest --cov-config evil.ini",
		"javac -cp .:/tmp/processor.jar Main.java",
		"javac -classpath=.:/tmp/processor.jar Main.java",
		"javac -sourcepath src:/tmp/src Main.java",
		"javac -bootclasspath .:/tmp/rt.jar Main.java",
		"javac -extdirs lib:/tmp/ext Main.java",
		"javac -endorseddirs lib:/tmp/endorsed Main.java",
		// Compiler helper/search-path forms must defer
		"kotlinc -Xplugin=./evil.jar main.kt",
		"kotlinc -cp .:/tmp/lib.jar main.kt",
		"kotlinc -classpath=.:/tmp/lib.jar main.kt",
		"gcc -B ./toolchain main.c",
		// Runner --flag=value bypasses must defer
		"make --file=/tmp/evil.mk test",
		"just --justfile=/tmp/evil.just test",
		"task --taskfile=/tmp/evil.yml test",
		"./gradlew --init-script=/tmp/evil.gradle test",
		// Runner flags without a recognized target must not invoke defaults
		"make --silent",
		"make -j4",
		"make -j 4",
		"just --quiet",
		"task --silent",
		"./gradlew --quiet",
		"./gradlew -x test",
		"./mvnw --quiet",
		"./mvnw -T 2",
		// Runner variable overrides must defer before reviewer invocation
		"make test SHELL=./foo-test",
		"just test shell=./foo-test",
		"task test SHELL=./foo-test",
		// Prohibited target components are case-insensitive.
		"make test-Deploy",
		"just lint-Release",
		"task build-Publish",
		"./gradlew test-Release",
		"./mvnw verify-Deploy",
		"npm run test-Publish",
		"pnpm run lint-Release",
		"yarn run build-Deploy",
		// Execution-capable assignment variables must defer
		"GOFLAGS=-toolexec=./runner go test ./...",
		"CFLAGS=-B./toolchain gcc main.c",
		// Git diff without --no-textconv must defer
		"git --no-pager diff",
		// air (live-reload daemon) must defer
		"air",
		// Mixed-fragment word concatenation bypasses must defer
		"prettier --write .''env",
		"go test -e''xec ./runner ./...",
		// Compiler strict mode: attached hazardous flags must defer
		"gcc -B./toolchain main.c",
		"gcc -Wl,--plugin,evil.so main.c",
		"gcc -Wl,-plugin,./evil.so main.c",
		// CMake cache variables that select executables or loaded code must defer
		"cmake -D CMAKE_C_COMPILER=./runner -S . -B build",
		"cmake -DCMAKE_C_COMPILER=./runner -S . -B build",
		"cmake -D CMAKE_PROJECT_TOP_LEVEL_INCLUDES=./evil.cmake -S . -B build",
		"cmake -DCMAKE_PROJECT_TOP_LEVEL_INCLUDES=./evil.cmake -S . -B build",
		// go vet strict mode: -vettool must defer
		"go vet -vettool=./runner ./...",
		// Bazel strict mode: = forms must defer
		"bazel build --override_repository=repo=/tmp/evil //target",
		"bazel build delete-all",
		// Buf strict mode: = forms must defer
		"buf generate --template=evil.yaml",
		// Protoc strict mode: unknown plugin output must defer
		"protoc --evil_out=. foo.proto",
		// Git --show-signature invokes GPG helper — must defer
		"git --no-pager log --no-textconv --show-signature",
		// Inline value skipping: safe =value flag must not consume next arg
		"go test -run=Test -exec=/tmp/runner ./...",
		"bazel test --jobs=1 --override_repository=repo=/tmp/evil //...",
		// Response-file indirection must defer
		"gcc @options main.c",
		"clang-tidy @params src/main.cpp",
		"cppcheck @options src/",
		// Go nested pass-through flags must defer
		"go build -ldflags '-linkmode=external -extld=./runner' ./...",
		// Go pass-through flags (-gcflags, -asmflags, -gccgoflags) and compiler
		// selection (-compiler) must defer — their values bypass the policy
		"go build -gcflags '-B' ./...",
		"go test -gcflags '-B' ./...",
		"go build -asmflags '-I' ./...",
		"go test -asmflags '-I' ./...",
		"go test -gccgoflags '-B./toolchain' ./...",
		"go build -compiler gccgo ./...",
		"go test -compiler gccgo ./...",
		"go vet -compiler gccgo ./...",
		// Code-loading tools in strict tier: plugin/helper flags must defer
		"pylint --load-plugins=evil src/",
		"pylint --init-hook x src/",
		"pylint -f evil.EvilReporter src/",
		"pylint -f=evil.EvilReporter src/",
		"pylint -fevil.EvilReporter src/",
		"pylint --output-format evil.EvilReporter src/",
		"pylint --output-format=evil.EvilReporter src/",
		"pylint --format=evil.EvilReporter src/",
		"clang-tidy --load=./evil.so src/main.cpp",
		"clang-tidy --extra-arg=-fplugin src/main.cpp",
		"clang-tidy '--config={ExtraArgsBefore: [-Xclang, -load, -Xclang, ./evil.so]}' src/main.cpp",
		"clang-tidy '--config={ExtraArgs: [-Xclang, -load, -Xclang, ./evil.so]}' src/main.cpp",
		"clang-tidy --config '{ExtraArgsBefore: [-Xclang, -load, -Xclang, ./evil.so]}' src/main.cpp",
		"clang-tidy --config-file=evil.yaml src/main.cpp",
		"clang-tidy --config-file evil.yaml src/main.cpp",
		"clang-tidy --fix src/main.cpp",
		"mypy --python-executable=./evil src/",
		"mypy --python-executable ./evil src/",
		"cppcheck --addon=./evil.py src/",
		"cppcheck --library=evil.cfg src/",
		"javac -J-javaagent:./evil.jar Main.java",
		"kotlinc -J-javaagent:./evil.jar main.kt",
		"ktlint --ruleset=./evil.jar src/main.kt",
		"ktlint --ruleset ./evil.jar src/main.kt",
		"ktlint -R ./evil.jar src/main.kt",
		// Buf input operands must be root-bounded and non-sensitive
		"buf lint /etc/passwd",
		"buf generate /tmp/external",
		"buf lint .env",
		// Git symbolic-ref mutating forms must defer
		"git symbolic-ref HEAD refs/heads/other",
		"git symbolic-ref -d HEAD",
		"git symbolic-ref --delete HEAD",
		// Cargo --config override and unverified external subcommand must defer
		"cargo check --config build.rustc-wrapper=./runner",
		"cargo test-unit",
		// Code-loading and executable-selection flags must defer
		"eslint --parser ./evil.js .",
		"eslint --format ./evil.js .",
		"eslint --format=./evil.js .",
		"eslint -f ./evil.js .",
		"eslint -f=./evil.js .",
		"eslint -f./evil.js .",
		"mocha --reporter ./evil.js",
		"vitest --reporter ./evil.js",
		"mockgen -exec_only ./runner",
		// Embedded file-backed style selectors must be interpreted as paths.
		"clang-format --style=file:/tmp/evil-format main.cpp",
		"clang-format --style file:/tmp/evil-format main.cpp",
		"clang-format -style=file:/tmp/evil-format main.cpp",
		"clang-format -style file:/tmp/evil-format main.cpp",
		"clang-format --style=file:.env main.cpp",
		// Multi-mode and destructive-clean commands must defer.
		"ruff server",
		"ruff clean",
		"golangci-lint cache clean",
		"jest --clearCache",
		"pytest --cache-clear",
		// Destructive target components override development verbs everywhere.
		"make test-remove",
		"make test-uninstall",
		"make test-destroy",
		"make test-delete",
		"just test-remove",
		"just test-uninstall",
		"task test-delete",
		"task test-destroy",
		"bazel test //ops:test-remove",
		"bazel build //ops:build-destroy",
		"./gradlew test-remove",
		"./gradlew test-uninstall",
		"./mvnw test-delete",
		"./mvnw test-destroy",
		"npm run test-remove",
		"npm run test-uninstall",
		"pnpm run test-delete",
		"yarn run test-destroy",
		// Bazel opaque pass-through options must defer
		"bazel test --test_arg=delete-all //target",
		"bazel test --test_env=LD_PRELOAD=./evil.so //target",
		"bazel test --config=repo_defined //target",
		"bazel build --disk_cache=grpc://external.example //target",
		"bazel build --repository_cache=/tmp/cache //target",
		"bazel build --copt=-fplugin=./evil.so //target",
		"bazel build --copt -fplugin=./evil.so //target",
		"bazel build --linkopt=--plugin=evil.so //target",
		"bazel build --python_path=./runner //target",
		"bazel build --action_env=LD_PRELOAD=./evil.so //target",
		"bazel build --define=FOO=bar //target",
		"bazel build --features=evil //target",
		// Secret-bearing attached macro flags must defer before reviewer invocation.
		"gcc -DPASSWORD=hunter2 -c main.c",
		"gcc -DAPI_KEY=hunter2 -c main.c",
		"cppcheck -DPASSWORD=hunter2 src/",
		// NUL and control bytes must defer
		"go test \x00 ./...",
		"go test \x01 ./...",
	} {
		got, err := handler.CanUseTool(bashReq(`{"command":"` + cmd + `"}`))
		if err != nil || got.Behavior != "" {
			t.Errorf("variant %q should defer, got %+v err %v", cmd, got, err)
		}
	}
}

func TestIntegrationCompilerExecutableSelectorsDeferWithoutModelCall(t *testing.T) {
	reg := agentFakeRegistry(t, testutil.FakeClaudeAllowScriptBody())
	reviewer, ok := autoreview.ResolveReviewer(reg, "")
	if !ok {
		t.Fatalf("ResolveReviewer = false, want true")
	}
	composed, original := composedGeneralHandler()
	handler := decorateHandlerWithAutoReview(composed, original, true, reviewer, "", nil)
	for _, cmd := range []string{
		"clang -flto=thin -fuse-ld=lld -fthinlto-distributor=./runner main.c",
		"clang -fuse-ld=./runner main.c",
		"clang -flto=thin -c main.c",
	} {
		got, err := handler.CanUseTool(bashReq(`{"command":"` + cmd + `"}`))
		if err != nil || got.Behavior != "" {
			t.Errorf("compiler selector %q should defer, got %+v err %v", cmd, got, err)
		}
	}
}

func TestIntegrationCompilerPassThroughOutputsDeferWithoutModelCall(t *testing.T) {
	reg := agentFakeRegistry(t, testutil.FakeClaudeAllowScriptBody())
	reviewer, ok := autoreview.ResolveReviewer(reg, "")
	if !ok {
		t.Fatalf("ResolveReviewer = false, want true")
	}
	composed, original := composedGeneralHandler()
	handler := decorateHandlerWithAutoReview(composed, original, true, reviewer, "", nil)
	for _, cmd := range []string{
		"gcc -Wp,-MD,/tmp/deps main.c",
		"gcc -Wa,-o,/tmp/asm.o main.c",
		"gcc -Wl,-Map,/tmp/link.map main.c",
	} {
		got, err := handler.CanUseTool(bashReq(`{"command":"` + cmd + `"}`))
		if err != nil || got.Behavior != "" {
			t.Errorf("compiler pass-through %q should defer, got %+v err %v", cmd, got, err)
		}
	}
}

func TestIntegrationCMakeNativePassThroughDeferWithoutModelCall(t *testing.T) {
	reg := agentFakeRegistry(t, testutil.FakeClaudeAllowScriptBody())
	reviewer, ok := autoreview.ResolveReviewer(reg, "")
	if !ok {
		t.Fatalf("ResolveReviewer = false, want true")
	}
	composed, original := composedGeneralHandler()
	handler := decorateHandlerWithAutoReview(composed, original, true, reviewer, "", nil)
	for _, cmd := range []string{
		"cmake --build build -- SHELL=./runner",
		"cmake --build build -- clean",
		"cmake --build build -- install",
	} {
		got, err := handler.CanUseTool(bashReq(`{"command":"` + cmd + `"}`))
		if err != nil || got.Behavior != "" {
			t.Errorf("cmake native pass-through %q should defer, got %+v err %v", cmd, got, err)
		}
	}
}

func TestIntegrationCMakePresetsDeferWithoutModelCall(t *testing.T) {
	reg := agentFakeRegistry(t, testutil.FakeClaudeAllowScriptBody())
	reviewer, ok := autoreview.ResolveReviewer(reg, "")
	if !ok {
		t.Fatalf("ResolveReviewer = false, want true")
	}
	composed, original := composedGeneralHandler()
	handler := decorateHandlerWithAutoReview(composed, original, true, reviewer, "", nil)
	for _, cmd := range []string{
		"cmake --preset evil-compiler",
		"cmake --preset=evil-include",
	} {
		got, err := handler.CanUseTool(bashReq(`{"command":"` + cmd + `"}`))
		if err != nil || got.Behavior != "" {
			t.Errorf("cmake preset %q should defer, got %+v err %v", cmd, got, err)
		}
	}
}

func TestIntegrationPackageScriptPassThroughDefersWithoutModelCall(t *testing.T) {
	reg := agentFakeRegistry(t, testutil.FakeClaudeAllowScriptBody())
	reviewer, ok := autoreview.ResolveReviewer(reg, "")
	if !ok {
		t.Fatalf("ResolveReviewer = false, want true")
	}
	composed, original := composedGeneralHandler()
	handler := decorateHandlerWithAutoReview(composed, original, true, reviewer, "", nil)
	for _, cmd := range []string{
		"npm test -- --silent",
		"pnpm test -- --quiet",
		"yarn test -- --verbose",
		"npm run test -- --silent",
		"pnpm run test -- --quiet",
		"yarn run test -- --verbose",
	} {
		got, err := handler.CanUseTool(bashReq(`{"command":"` + cmd + `"}`))
		if err != nil || got.Behavior != "" {
			t.Errorf("package script pass-through %q should defer, got %+v err %v", cmd, got, err)
		}
	}
}

func TestIntegrationBazelProhibitedLabelsDeferWithoutModelCall(t *testing.T) {
	reg := agentFakeRegistry(t, testutil.FakeClaudeAllowScriptBody())
	reviewer, ok := autoreview.ResolveReviewer(reg, "")
	if !ok {
		t.Fatalf("ResolveReviewer = false, want true")
	}
	composed, original := composedGeneralHandler()
	handler := decorateHandlerWithAutoReview(composed, original, true, reviewer, "", nil)
	for _, cmd := range []string{
		"bazel build //:deploy",
		"bazel build //tools:install",
		"bazel test //ops:release",
	} {
		got, err := handler.CanUseTool(bashReq(`{"command":"` + cmd + `"}`))
		if err != nil || got.Behavior != "" {
			t.Errorf("bazel prohibited label %q should defer, got %+v err %v", cmd, got, err)
		}
	}
}

func TestIntegrationSymlinkEscapesDeferWithoutModelCall(t *testing.T) {
	workDir := t.TempDir()
	externalDir := t.TempDir()
	if err := os.Symlink(externalDir, filepath.Join(workDir, "escape")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	externalFile := filepath.Join(externalDir, "victim.go")
	if err := os.WriteFile(externalFile, []byte("package external\n"), 0o600); err != nil {
		t.Fatalf("write external file: %v", err)
	}
	if err := os.Symlink(externalFile, filepath.Join(workDir, "victim.go")); err != nil {
		t.Skipf("file symlink unavailable: %v", err)
	}
	subdir := filepath.Join(workDir, "subdir")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatalf("create subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "changed.go"), []byte("package root\n"), 0o600); err != nil {
		t.Fatalf("write root changed file: %v", err)
	}
	if err := os.Symlink(externalFile, filepath.Join(subdir, "changed.go")); err != nil {
		t.Skipf("changed-directory symlink unavailable: %v", err)
	}

	reg := agentFakeRegistry(t, testutil.FakeClaudeAllowScriptBody())
	reviewer, ok := autoreview.ResolveReviewer(reg, "")
	if !ok {
		t.Fatalf("ResolveReviewer = false, want true")
	}
	composed, original := composedGeneralHandler()
	handler := decorateHandlerWithAutoReview(composed, original, true, reviewer, workDir, []string{workDir})
	for _, cmd := range []string{
		"cd escape && make test",
		"go test ./escape/...",
		"go build -o escape/app ./...",
		"gofmt -w victim.go",
		"prettier --write escape",
		"go build -o victim.go ./...",
		"cd subdir && gofmt -w changed.go",
	} {
		got, err := handler.CanUseTool(bashReq(`{"command":"` + cmd + `"}`))
		if err != nil || got.Behavior != "" {
			t.Errorf("symlink escape %q should defer, got %+v err %v", cmd, got, err)
		}
	}
}

func TestIntegrationMissingHaikuDefers(t *testing.T) {
	reg := llm.NewRegistry() // no claude
	reviewer, _ := autoreview.ResolveReviewer(reg, "")
	composed, original := composedGeneralHandler()
	handler := decorateHandlerWithAutoReview(composed, original, true, reviewer, "", nil)
	got, err := handler.CanUseTool(bashReq(`{"command":"go test ./..."}`))
	if err != nil || got.Behavior != "" {
		t.Fatalf("missing Haiku should defer: got %+v err %v", got, err)
	}
}

func TestIntegrationExplicitNonClaudeModelNotSubstituted(t *testing.T) {
	reg := agentFakeRegistry(t, testutil.FakeClaudeAllowScriptBody())
	reviewer, _ := autoreview.ResolveReviewer(reg, "sonnet-99")
	if reviewer.Provider != nil {
		t.Fatalf("unresolvable model should not produce a reviewer")
	}
	composed, original := composedGeneralHandler()
	handler := decorateHandlerWithAutoReview(composed, original, true, reviewer, "", nil)
	got, err := handler.CanUseTool(bashReq(`{"command":"go test ./..."}`))
	if err != nil || got.Behavior != "" {
		t.Fatalf("unresolvable explicit model should defer (no substitution): got %+v err %v", got, err)
	}
}

func TestIntegrationTimeoutDefers(t *testing.T) {
	reg := agentFakeRegistry(t, testutil.FakeClaudeSleepScriptBody())
	reviewer, ok := autoreview.ResolveReviewer(reg, "")
	if !ok {
		t.Fatalf("ResolveReviewer = false, want true")
	}
	composed, original := composedGeneralHandler()
	handler := decorateHandlerWithAutoReview(composed, original, true, reviewer, "", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	req := bashReq(`{"command":"go test ./..."}`)
	req.Ctx = ctx
	got, err := handler.CanUseTool(req)
	cancel()
	if err != nil || got.Behavior != "" {
		t.Fatalf("timeout should defer: got %+v err %v", got, err)
	}
}

func TestIntegrationMalformedOutputDefers(t *testing.T) {
	reg := agentFakeRegistry(t, testutil.FakeClaudeMalformedScriptBody())
	reviewer, ok := autoreview.ResolveReviewer(reg, "")
	if !ok {
		t.Fatalf("ResolveReviewer = false, want true")
	}
	composed, original := composedGeneralHandler()
	handler := decorateHandlerWithAutoReview(composed, original, true, reviewer, "", nil)
	got, err := handler.CanUseTool(bashReq(`{"command":"go test ./..."}`))
	if err != nil || got.Behavior != "" {
		t.Fatalf("malformed output should defer: got %+v err %v", got, err)
	}
}

func TestIntegrationProviderFailureDefers(t *testing.T) {
	reg := agentFakeRegistry(t, testutil.FakeClaudeExitScriptBody())
	reviewer, ok := autoreview.ResolveReviewer(reg, "")
	if !ok {
		t.Fatalf("ResolveReviewer = false, want true")
	}
	composed, original := composedGeneralHandler()
	handler := decorateHandlerWithAutoReview(composed, original, true, reviewer, "", nil)
	got, err := handler.CanUseTool(bashReq(`{"command":"go test ./..."}`))
	if err != nil || got.Behavior != "" {
		t.Fatalf("provider failure should defer: got %+v err %v", got, err)
	}
}

func TestIntegrationExistingAllowRemainsAuthoritative(t *testing.T) {
	original := permission.Guarded(&permission.AutoApproveHandler{})
	composed := permission.WrapGeneralPhaseHandlerWithSafeCreate(original, nil)
	reg := agentFakeRegistry(t, testutil.FakeClaudeDeferScriptBody())
	reviewer, _ := autoreview.ResolveReviewer(reg, "")
	handler := decorateHandlerWithAutoReview(composed, original, true, reviewer, "", nil)
	got, err := handler.CanUseTool(bashReq(`{"command":"go test ./..."}`))
	if err != nil || got.Behavior != "allow" {
		t.Fatalf("existing allow should win without reviewer: got %+v err %v", got, err)
	}
}

func TestIntegrationDirectDenyRemainsAuthoritative(t *testing.T) {
	denyInner := permission.Guarded(&denyBashHandler{})
	original := denyInner
	composed := permission.WrapGeneralPhaseHandlerWithSafeCreate(original, nil)
	reg := agentFakeRegistry(t, testutil.FakeClaudeAllowScriptBody())
	reviewer, _ := autoreview.ResolveReviewer(reg, "")
	handler := decorateHandlerWithAutoReview(composed, original, true, reviewer, "", nil)
	got, err := handler.CanUseTool(bashReq(`{"command":"go test ./..."}`))
	if err != nil || got.Behavior != "deny" {
		t.Fatalf("existing deny should win without reviewer: got %+v err %v", got, err)
	}
}

func TestIntegrationNonBashRequestUnchanged(t *testing.T) {
	composed, original := composedGeneralHandler()
	reg := agentFakeRegistry(t, testutil.FakeClaudeAllowScriptBody())
	reviewer, _ := autoreview.ResolveReviewer(reg, "")
	handler := decorateHandlerWithAutoReview(composed, original, true, reviewer, "", nil)
	got, err := handler.CanUseTool(ports.ToolPermissionRequest{ToolName: "Read", Input: `{}`})
	if err != nil || got.Behavior != "allow" {
		t.Fatalf("non-Bash read should be allowed by AcceptEdits: got %+v err %v", got, err)
	}
}

func TestIntegrationPreservesOriginalCallbackInput(t *testing.T) {
	reg := agentFakeRegistry(t, testutil.FakeClaudeAllowScriptBody())
	reviewer, ok := autoreview.ResolveReviewer(reg, "")
	if !ok {
		t.Fatalf("ResolveReviewer = false, want true")
	}
	composed, original := composedGeneralHandler()
	handler := decorateHandlerWithAutoReview(composed, original, true, reviewer, "", nil)
	originalInput := `{"command":"go test ./..."}`
	req := ports.ToolPermissionRequest{ToolName: "Bash", Input: originalInput}
	got, err := handler.CanUseTool(req)
	if err != nil || got.Behavior != "allow" {
		t.Fatalf("expected auto-approve: got %+v err %v", got, err)
	}
	if req.Input != originalInput {
		t.Errorf("request Input was modified: got %q, want %q", req.Input, originalInput)
	}
}
