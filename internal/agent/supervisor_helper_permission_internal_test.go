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
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/autoreview"
	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

const (
	bareHelperCommand     = `"$AGENTICO_BIN" api POST /api/v1/features '{"name":"my feature","description":"a b"}'`
	compoundHelperCommand = `"$AGENTICO_BIN" api GET /api/v1/features | jq .`
	helperShapeReasonPart = "agentico api must be invoked as a single bare command"
)

func helperBashReq(command string) ports.ToolPermissionRequest {
	return bashReq(`{"command":` + strconv.Quote(command) + `}`)
}

// The supervisor handler decides a bare helper call itself, so neither the
// skip-permissions decorator's auto-approval nor automatic review acts on it:
// the reviewer is never consulted, no review status is appended, and no
// auto-approve offer rides on the decision.
func TestSupervisorBareHelperCallIsDecidedBeforeDecorators(t *testing.T) {
	for _, reviewEnabled := range []bool{true, false} {
		t.Run("review_enabled="+strconv.FormatBool(reviewEnabled), func(t *testing.T) {
			var classifyCalls, statusAppends, skipChecks atomic.Int32
			reviewer := &autoReviewPermissionDecorator{
				inner:    &permission.SupervisorHandler{},
				enabled:  func() bool { return reviewEnabled },
				reviewer: autoreview.Reviewer{Provider: fakeAllowProvider(t), Model: "haiku[200K]"},
				classify: func(context.Context, autoreview.Reviewer, autoreview.ClassifyRequest) (autoreview.Decision, bool) {
					classifyCalls.Add(1)
					return autoreview.Allow, true
				},
				appendStatus: func(string) error {
					statusAppends.Add(1)
					return nil
				},
			}
			handler := &skipPermissionsDecorator{inner: reviewer, skip: func() bool {
				skipChecks.Add(1)
				return false
			}}

			got, err := handler.CanUseTool(helperBashReq(bareHelperCommand))
			if err != nil || got.Behavior != permission.DecisionAllow {
				t.Fatalf("bare helper call = %+v, %v; want allow", got, err)
			}
			if got.AutoApproveOffer != nil || got.AutomaticReview != nil || got.Reason != "" {
				t.Fatalf("bare helper call carries decorator output: %+v", got)
			}
			if n := classifyCalls.Load(); n != 0 {
				t.Fatalf("reviewer classifications = %d, want 0", n)
			}
			if n := statusAppends.Load(); n != 0 {
				t.Fatalf("review status appends = %d, want 0", n)
			}
			if n := skipChecks.Load(); n != 1 {
				t.Fatalf("skip-mode checks = %d, want 1 (live mode selection is unchanged)", n)
			}
		})
	}
}

// A compound helper command is still undecided by the supervisor handler, so
// the decorators compose exactly as before: review disabled leaves the human
// deferral (with its auto-approve offer) and the shape reason intact; review
// enabled consults the reviewer; skip-permissions still auto-approves.
func TestSupervisorCompoundHelperCallStillReachesDecorators(t *testing.T) {
	var classifyCalls atomic.Int32
	reviewEnabled, skip := false, false
	reviewer := &autoReviewPermissionDecorator{
		inner:    &permission.SupervisorHandler{},
		enabled:  func() bool { return reviewEnabled },
		reviewer: autoreview.Reviewer{Provider: fakeDeferProvider(t), Model: "haiku[200K]"},
		classify: func(context.Context, autoreview.Reviewer, autoreview.ClassifyRequest) (autoreview.Decision, bool) {
			classifyCalls.Add(1)
			return autoreview.Defer, true
		},
		appendStatus: func(string) error { return nil },
	}
	handler := &skipPermissionsDecorator{inner: reviewer, skip: func() bool { return skip }}
	req := helperBashReq(compoundHelperCommand)

	got, err := handler.CanUseTool(req)
	if err != nil || got.Behavior != "" || got.AutoApproveOffer == nil || !strings.Contains(got.Reason, helperShapeReasonPart) {
		t.Fatalf("review off: %+v, %v; want deferral with offer and shape reason", got, err)
	}

	reviewEnabled = true
	got, err = handler.CanUseTool(req)
	if err != nil || got.Behavior != "" || classifyCalls.Load() != 1 || !strings.Contains(got.Reason, helperShapeReasonPart) {
		t.Fatalf("review on: %+v, %v, classifications=%d; want reviewed deferral with shape reason", got, err, classifyCalls.Load())
	}

	skip = true
	got, err = handler.CanUseTool(req)
	if err != nil || got.Behavior != permission.DecisionAllow || classifyCalls.Load() != 1 {
		t.Fatalf("skip-permissions: %+v, %v, classifications=%d; want allow without review", got, err, classifyCalls.Load())
	}
}

// Through the real BuildSession composition (safe-create wrapper, automatic
// review decorator, skip-permissions decorator) with automatic review at its
// default (off), a bare helper call is a plain allow with no auto-approve
// offer, while other Bash still reaches the review decorator and carries one.
func TestBuildSessionSupervisorAllowsBareHelperCallWithoutReview(t *testing.T) {
	dir := t.TempDir()
	provider := &captureProvider{name: "capture", model: "model-a", contextWindow: 200_000}
	pr := NewPhaseRunner(nil, feature.NewStore(dir), dir)
	pr.Registry = newRegistryWithCaptureProvider(provider)
	pr.Config = &config.Config{}
	_, _, opts, err := pr.BuildSession(BuildSessionOpts{Model: "model-a", WorkDir: t.TempDir(), PermHandler: &permission.SupervisorHandler{}, Interactive: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		`"$AGENTICO_BIN" api GET /api/v1/features`,
		`/Applications/Agentico.app/Contents/Resources/bin/agentico api GET /api/v1/health`,
		bareHelperCommand,
	} {
		got, err := opts.PermHandler.CanUseTool(helperBashReq(command))
		if err != nil || got.Behavior != permission.DecisionAllow || got.AutoApproveOffer != nil || got.AutomaticReview != nil {
			t.Fatalf("composed handler on %q = %+v, %v; want a plain allow", command, got, err)
		}
	}
	got, err := opts.PermHandler.CanUseTool(helperBashReq(compoundHelperCommand))
	if err != nil || got.Behavior != "" || got.AutoApproveOffer == nil || !strings.Contains(got.Reason, helperShapeReasonPart) {
		t.Fatalf("composed handler on compound helper call = %+v, %v; want deferral with offer and shape reason", got, err)
	}
	got, err = opts.PermHandler.CanUseTool(helperBashReq(`"$AGENTICO_BIN" validate-artifacts --dir /tmp/x`))
	if err != nil || got.Behavior != "" || got.Reason != "" {
		t.Fatalf("composed handler on validate-artifacts = %+v, %v; want plain deferral", got, err)
	}
}
