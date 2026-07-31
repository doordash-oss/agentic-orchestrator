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

// Package tuitest drives the real API-driven TUI model through bubbletea key
// messages from tests, instead of calling server.Client mutation methods
// directly. It is test support only: the shipped internal/tui package
// exposes just the read-only probes this package consumes, while the
// synchronous command pump, timeout handling, and message-type filtering
// live here where no production build needs them.
package tuitest

import (
	"context"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/doordash-oss/agentic-orchestrator/internal/server"
	"github.com/doordash-oss/agentic-orchestrator/internal/tui"
)

const (
	// cmdTimeout bounds how long one pumped command may run before its
	// result is treated as blocked. Commands that block past the deadline
	// (most notably the SSE listener, which only resolves when an event
	// fires) are abandoned: the harness drives reads explicitly via Refresh.
	cmdTimeout = 3 * time.Second
	// longCmdTimeout bounds legitimately slow commands (session stop under
	// -race, real-git worktree setup): past this bound the command is wedged
	// and the pump fails the test rather than silently dropping its result.
	longCmdTimeout = 60 * time.Second
	// maxPumpIterations bounds how many message results one key press may
	// chain. Tick/Blink messages are already dropped by type, so this is a
	// final guard against runaway self-respawning commands under -race.
	maxPumpIterations = 60
)

// AppHarness wraps tui.APIAppModel for end-to-end tests. All model mutation
// happens synchronously on the caller's goroutine, so the harness is safe
// under the race detector without locks: callers drive one key (or refresh)
// at a time.
//
// The harness intentionally never calls APIAppModel.Init: the spinner tick
// and the SSE listener it returns are timer/stream-driven and would make the
// model non-deterministic. Events still arrive — every mutation action and
// Refresh push their produced commands through the same pump a real Bubble
// Tea program would run.
type AppHarness struct {
	model tui.APIAppModel
}

// RemediationDirtyRepo mirrors tui.RemediationDirtyRepo so journey tests can
// assert the remediation diagnostics without importing tui's probe types.
type RemediationDirtyRepo = tui.RemediationDirtyRepo

// NewAppHarness cold-boots the model exactly as production does: it loads
// the full snapshot (features, runtime config, catalog, prompts, permissions,
// sessions, recovery) and eagerly projects any active child relationships.
func NewAppHarness(ctx context.Context, client tui.APIClient, opts tui.APIAppOptions) (*AppHarness, error) {
	app, err := tui.NewAPIAppModel(ctx, client, opts)
	if err != nil {
		return nil, err
	}
	return &AppHarness{model: app}, nil
}

// Resize delivers a WindowSizeMsg so the model lays out at a deterministic
// terminal size.
func (h *AppHarness) Resize(w, height int) {
	h.update(tea.WindowSizeMsg{Width: w, Height: height})
}

// Type sends one KeyPressMsg per rune, mirroring how a terminal delivers
// typed text to focused inputs (wizard name/description fields).
func (h *AppHarness) Type(text string) {
	for _, r := range text {
		h.PressKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// Press sends a bare key-code message (enter, esc, tab, arrows, ...).
func (h *AppHarness) Press(code rune) {
	h.PressKey(tea.KeyPressMsg{Code: code})
}

// PressKey sends one fully populated key message and pumps the command chain
// it triggers.
func (h *AppHarness) PressKey(k tea.KeyPressMsg) {
	h.update(k)
}

// FullResyncSignal mirrors the synthetic full-resync signal the SSE stream
// sends on (re)connect, so the client re-pulls every read-model snapshot.
func FullResyncSignal() server.RefreshSignal {
	return server.RefreshSignal{
		Event:            server.SSEEventDTO{Kind: "connected"},
		SnapshotRequired: true,
	}
}

// FeatureSignal builds the resource-scoped signal an SSE feature.updated
// frame carries for one feature, so the client re-fetches that feature's
// detail snapshot.
func FeatureSignal(featureID string) server.RefreshSignal {
	return server.RefreshSignal{
		Event:    server.SSEEventDTO{Kind: "feature.updated"},
		Resource: server.ResourceDTO{Type: "feature", FeatureID: featureID},
	}
}

// RelationshipSignal builds the resource-scoped signal an SSE
// lifecycle.updated frame carries for one parent/child relationship, so the
// client re-fetches both records as one ordered bundle.
func RelationshipSignal(parentID, childID string) server.RefreshSignal {
	resource := server.ResourceDTO{
		Type:     "relationship",
		ID:       parentID + ":" + childID,
		ParentID: parentID,
		ChildID:  childID,
	}
	return server.RefreshSignal{
		Event:    server.SSEEventDTO{Kind: "lifecycle.updated", Resource: resource},
		Resource: resource,
	}
}

// RefreshRelationship synchronously runs the same refresh the SSE frame for
// one changed/closed relationship triggers — the ordered parent+child bundle
// re-pull.
func (h *AppHarness) RefreshRelationship(parentID, childID string) {
	h.pumpCmd(h.model.RefreshCmd(RelationshipSignal(parentID, childID)))
}

// RefreshRelationshipDeleted synchronously runs the refresh the
// relationship_deleted SSE frame triggers: a list re-pull plus the ordered
// eviction of both relationship records, without expected-to-fail detail
// reads.
func (h *AppHarness) RefreshRelationshipDeleted(parentID, childID string) {
	resource := server.ResourceDTO{
		Type:                "relationship",
		ID:                  parentID + ":" + childID,
		ParentID:            parentID,
		ChildID:             childID,
		RelationshipDeleted: true,
	}
	h.pumpCmd(h.model.RefreshCmd(server.RefreshSignal{
		Event:    server.SSEEventDTO{Kind: "lifecycle.updated", Resource: resource},
		Resource: resource,
	}))
}

// Refresh synchronously runs the same refresh the SSE "connected" resync
// signal triggers — a full snapshot re-pull — and additionally re-fetches
// the detail snapshots of the selected feature and its relationship partner
// so the model sees the same state it would after the corresponding SSE
// frames.
func (h *AppHarness) Refresh() {
	h.pumpCmd(h.model.RefreshCmd(FullResyncSignal()))
	selected := h.model.SelectedFeatureID()
	if selected == "" {
		return
	}
	h.pumpCmd(h.model.RefreshCmd(FeatureSignal(selected)))
	if detail, ok := h.model.FeatureDetail(selected); ok {
		if detail.Feature.ParentID != "" {
			h.pumpCmd(h.model.RefreshCmd(FeatureSignal(detail.Feature.ParentID)))
		} else if detail.Feature.ActiveChild != nil && detail.Feature.ActiveChild.ID != "" {
			h.pumpCmd(h.model.RefreshCmd(FeatureSignal(detail.Feature.ActiveChild.ID)))
		}
	}
}

// SelectedFeatureID reports the model's currently selected feature id.
func (h *AppHarness) SelectedFeatureID() string {
	return h.model.SelectedFeatureID()
}

// FeatureDetail exposes the model's cached feature detail snapshot, so tests
// can assert how a refresh changed the projected relationship state.
func (h *AppHarness) FeatureDetail(featureID string) (server.FeatureDetailResponse, bool) {
	return h.model.FeatureDetail(featureID)
}

// WizardActive reports whether the create/refactor wizard is open.
func (h *AppHarness) WizardActive() bool {
	return h.model.WizardActive()
}

// View returns the composed view exactly as the live app renders it,
// including wizard, editor, remediation, and review overlays.
func (h *AppHarness) View() string {
	return h.model.View().Content
}

// RemediationVisible reports whether the dirty-worktree remediation overlay
// is open after a blocked refactor launch or entry.
func (h *AppHarness) RemediationVisible() bool {
	return h.model.RemediationActive()
}

// RemediationRepos returns the ordered dirty-repository diagnostics carried
// by the open remediation overlay.
func (h *AppHarness) RemediationRepos() []RemediationDirtyRepo {
	return h.model.RemediationDirtyRepos()
}

// EditorOpen reports whether the config editor overlay is open.
func (h *AppHarness) EditorOpen() bool {
	return h.model.EditorOpen()
}

// ReviewOpen reports whether a review session overlay is open.
func (h *AppHarness) ReviewOpen() bool {
	return h.model.ReviewOpen()
}

// ReviewMenuOpen reports whether the review overlay's decision menu is
// currently showing (after esc from the artifact editor).
func (h *AppHarness) ReviewMenuOpen() bool {
	return h.model.ReviewMenuOpen()
}

// ReviewMenuLabels exposes the review overlay's ordered decision menu labels.
func (h *AppHarness) ReviewMenuLabels() []string {
	return h.model.ReviewMenuLabels()
}

// StatusMessage is the dashboard status line the live app would show.
func (h *AppHarness) StatusMessage() string {
	return h.model.StatusMessage()
}

// FeatureListSummaries exposes the model's cached top-level feature list
// snapshot so journey tests can assert the relationship projection the
// dashboard actually consumed.
func (h *AppHarness) FeatureListSummaries() []server.FeatureSummary {
	return h.model.FeatureListSummaries()
}

// Close releases the model's event subscription.
func (h *AppHarness) Close() {
	h.model.Close()
}

func (h *AppHarness) update(msg tea.Msg) {
	model, cmd := h.model.Update(msg)
	if app, ok := model.(tui.APIAppModel); ok {
		h.model = app
	}
	h.pumpCmd(cmd)
}

// pumpCmd executes cmd in a helper goroutine so a blocking command can be
// dropped on timeout, feeds its message back through Update, and repeats for
// the follow-up command. tea.BatchMsg fans out to every child command;
// Tick/Blink self-respawning messages are dropped by type so the pump
// terminates deterministically. A panicking command, or one that blocks past
// cmdTimeout without being the known long-lived SSE listener, fails the test
// immediately instead of decaying into a silently dropped message.
func (h *AppHarness) pumpCmd(cmd tea.Cmd) {
	for i := 0; i < maxPumpIterations && cmd != nil; i++ {
		res := runHarnessCmd(cmd)
		if res.panicValue != nil {
			panic(fmt.Sprintf("tuitest: pumped command %s panicked: %v", res.name, res.panicValue))
		}
		if res.timedOut {
			panic(fmt.Sprintf("tuitest: pumped command %s blocked for %s", res.name, res.limit))
		}
		msg := res.msg
		if msg == nil {
			return
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, child := range batch {
				if child != nil {
					h.pumpCmd(child)
				}
			}
			return
		}
		if dropHarnessMsg(msg) {
			return
		}
		model, next := h.model.Update(msg)
		if app, ok := model.(tui.APIAppModel); ok {
			h.model = app
		}
		cmd = next
	}
}

// harnessCmdResult reports how one pumped command resolved: its message, the
// recovered panic value, or a timeout past limit. name carries the command
// function's runtime symbol so failures identify the offender.
type harnessCmdResult struct {
	name       string
	msg        tea.Msg
	panicValue any
	timedOut   bool
	limit      time.Duration
}

// runHarnessCmd executes cmd and waits for its message. The known long-lived
// SSE listener is dropped once it blocks past cmdTimeout (it only resolves
// when an event fires, which a synchronous test never delivers); every other
// command may legitimately run up to longCmdTimeout, after which it is
// reported wedged. Panics surface to the pump instead of decaying into a
// silently dropped message.
func runHarnessCmd(cmd tea.Cmd) harnessCmdResult {
	name := ""
	if fn := runtime.FuncForPC(reflect.ValueOf(cmd).Pointer()); fn != nil {
		name = fn.Name()
	}
	limit := longCmdTimeout
	if strings.Contains(name, "listenForAPIEvents") {
		limit = cmdTimeout
	}
	done := make(chan harnessCmdResult, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- harnessCmdResult{name: name, panicValue: r}
			}
		}()
		done <- harnessCmdResult{name: name, msg: cmd()}
	}()
	select {
	case res := <-done:
		return res
	case <-time.After(limit):
		if limit == cmdTimeout {
			// The SSE listener's block is the one expected timeout.
			return harnessCmdResult{name: name, msg: nil, limit: limit}
		}
		return harnessCmdResult{name: name, timedOut: true, limit: limit}
	}
}

// dropHarnessMsg reports messages that must not be fed back through Update:
// quit requests (the test drives lifecycle explicitly) and the
// self-respawning spinner/textinput tick messages, which would otherwise
// chain forever through the pump. Type identification uses the type's
// reflected name so the harness stays independent of the exact bubbles
// subpackage layout.
func dropHarnessMsg(msg tea.Msg) bool {
	name := reflect.TypeOf(msg).String()
	return strings.Contains(name, "TickMsg") ||
		strings.Contains(name, "BlinkMsg") ||
		strings.Contains(name, "QuitMsg")
}
