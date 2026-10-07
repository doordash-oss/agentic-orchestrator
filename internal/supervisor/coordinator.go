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

package supervisor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/doordash-oss/agentic-orchestrator/internal/errcat"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/internal/workadmission"
)

// UserData is the payload of a user record.
type UserData struct {
	Text string `json:"text"`
}

// ContentData is the payload of assistant, tool_use and tool_result
// records: the provider's content blocks with reasoning removed.
type ContentData struct {
	Content []llm.ContentBlock `json:"content"`
}

// Request stages and outcomes carried by permission and question records.
const (
	StageRequested = "requested"
	StageResolved  = "resolved"

	RequestPending  = "pending"
	RequestAllowed  = "allowed"
	RequestDenied   = "denied"
	RequestAnswered = "answered"
	// RequestInterrupted resolves a request whose turn was cut by a server
	// shutdown or crash before it was answered.
	RequestInterrupted = "interrupted"
)

// Marker types carried by marker records.
const (
	MarkerInterrupted          = "interrupted"
	MarkerError                = "error"
	MarkerHistoryNotRestored   = "history_not_restored"
	MarkerPermissionRestricted = "permission_restricted"
	MarkerSettingsChanged      = "settings_changed"
	MarkerSettingsReverted     = "settings_reverted"
)

// MarkerData is the payload of a display-only marker record. Code is the
// catalog code of an error marker.
type MarkerData struct {
	Marker string `json:"marker"`
	Text   string `json:"text"`
	Code   string `json:"code,omitempty"`
}

// NoteData is model-only context inserted at a settings boundary.
type NoteData struct {
	Text string `json:"text"`
}

// RequestData is the payload of permission and question records. Input,
// Reason and Answers stay server-side; projection reduces them.
type RequestData struct {
	RequestID string            `json:"request_id"`
	ToolName  string            `json:"tool_name"`
	Stage     string            `json:"stage"`
	Outcome   string            `json:"outcome"`
	Input     json.RawMessage   `json:"input,omitempty"`
	Reason    string            `json:"reason,omitempty"`
	Answers   map[string]string `json:"answers,omitempty"`
	// Origin is who raised the request: RequestOriginRoot for the
	// conversation's own agent, RequestOriginChild for one of its
	// sub-agents. Records written before origins existed carry none and
	// read as root.
	Origin string `json:"origin,omitempty"`
	// ChildSessionID names the sub-agent's session for a child request.
	ChildSessionID string `json:"child_session_id,omitempty"`
}

// Request origins carried by permission and question records.
const (
	RequestOriginRoot  = "root"
	RequestOriginChild = "child"
)

// RequestOrigin maps a provider event origin to the request origin and, for
// a sub-agent, its session id.
func RequestOrigin(origin llm.EventOrigin) (string, string) {
	if origin.Kind != llm.EventOriginTask {
		return RequestOriginRoot, ""
	}
	child := origin.ChildSessionID
	if child == "" {
		child = origin.TaskID
	}
	return RequestOriginChild, child
}

const askUserQuestionTool = "AskUserQuestion"

// subscriberBuffer bounds one subscriber's undelivered events. Overflow
// drops deltas; any other overflow ends the subscription with a reset so
// the client re-snapshots instead of silently missing a record.
const subscriberBuffer = 512

// Coordinator owns the supervisor conversation. Mutations serialise under
// opMu; mu guards the read model and is the only lock the session observer
// takes, so a mutation may wait on the provider (handshake, Stop) without
// stalling the session reader.
type Coordinator struct {
	opts Options
	dir  string

	opMu sync.Mutex

	mu               sync.Mutex
	settings         Settings
	appliedChanges   map[string]bool
	pendingChange    *PendingChange
	relaunchPrevious *Settings
	applyingChange   bool
	conv             persistedConversation
	store            *transcriptStore
	lifecycle        Lifecycle
	step             StartingStep
	outcome          TurnOutcome
	interruptedBy    InterruptedBy
	failure          *LaunchFailedError
	permMode         PermissionMode
	session          ports.SessionView
	sessionID        string
	effectiveModel   string
	launch           *launchAttempt
	// turns holds the delivered turns still awaiting a result, oldest first.
	turns        []string
	turnCount    int
	pending      []*llm.ControlRequestMessage
	streamID     string
	streamChunks int
	streamCount  int
	// ending marks the current process as being stopped on purpose; its
	// exit is not a failure.
	ending bool
	// keepTurns leaves the turn-in-flight record in place when the process
	// is stopped mid-turn by End or Close, so a clean shutdown and a crash
	// take the same boot path.
	keepTurns bool
	// resumeID is the native session id the current process was resumed
	// against; empty for a fresh launch.
	resumeID string
	// fallbackMarked records that this process already reported a failed
	// resume, so the marker appears once per generation.
	fallbackMarked bool
	interrupt      *interruptAttempt
	closed         bool
	subs           map[*Subscription]struct{}
}

type launchAttempt struct {
	generation  int64
	sessionID   string
	joiners     []*joiner
	cancelled   bool
	cancel      chan struct{}
	reservation *workadmission.Reservation
}

type joiner struct {
	text      string
	hidden    string
	cmid      string
	initiator bool
	done      chan joinResult
}

type joinResult struct {
	res SendResult
	err error
}

type interruptAttempt struct {
	done chan struct{}
}

// New loads the settings, the current conversation (allocating one when
// none exists) and its transcript, and reports lifecycle stopped. It never
// launches or touches a process.
func New(opts Options) (*Coordinator, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.NewID == nil {
		opts.NewID = randomID
	}
	if opts.HandshakeTimeout <= 0 {
		opts.HandshakeTimeout = DefaultHandshakeTimeout
	}
	if opts.InterruptGrace <= 0 {
		opts.InterruptGrace = DefaultInterruptGrace
	}
	if opts.OrphanWait <= 0 {
		opts.OrphanWait = DefaultOrphanWait
	}
	if opts.SettingsUpdateTimeout <= 0 {
		opts.SettingsUpdateTimeout = DefaultSettingsUpdateTimeout
	}
	if opts.WorkDir == "" {
		opts.WorkDir = opts.StateDir
	}
	dir := filepath.Join(opts.StateDir, supervisorDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create supervisor state dir: %w", err)
	}
	settings, err := loadSettings(dir)
	if err != nil {
		return nil, err
	}
	pendingChange, err := loadPendingChange(dir)
	if err != nil {
		return nil, err
	}
	appliedChanges, err := loadAppliedChanges(dir)
	if err != nil {
		return nil, err
	}
	conv, found, err := loadConversation(dir)
	if err != nil {
		return nil, err
	}
	if !found || !validConversationID(conv.ConversationID) {
		conv = persistedConversation{ConversationID: uuid.NewString(), StreamEpoch: randomID()}
		if err := saveConversation(dir, conv); err != nil {
			return nil, fmt.Errorf("allocate supervisor conversation: %w", err)
		}
	}
	if conv.StreamEpoch == "" {
		conv.StreamEpoch = randomID()
		if err := saveConversation(dir, conv); err != nil {
			return nil, err
		}
	}
	store, err := openTranscriptStore(filepath.Join(dir, conversationsDir, conv.ConversationID), conv.ConversationID, opts.NewID, opts.Now)
	if err != nil {
		return nil, err
	}
	store.setGeneration(conv.Generation)
	c := &Coordinator{
		opts:           opts,
		dir:            dir,
		settings:       settings,
		appliedChanges: appliedChanges,
		pendingChange:  pendingChange,
		conv:           conv,
		store:          store,
		lifecycle:      LifecycleStopped,
		outcome:        OutcomeNone,
		interruptedBy:  InterruptedByNone,
		permMode:       PermissionMode{Requested: RequestedPermissionMode},
		subs:           map[*Subscription]struct{}{},
	}
	if err := c.recoverBoot(); err != nil {
		_ = store.close()
		return nil, err
	}
	return c, nil
}

// recoverBoot reconciles what the previous server process left behind. A
// surviving provider of the current generation is terminated by process
// group (never reattached), and every turn the turn-in-flight record still
// lists is marked interrupted with its unanswered requests resolved, so the
// conversation reads as paused rather than silently idle.
func (c *Coordinator) recoverBoot() error {
	gen := c.conv.Generation
	// The record is read before the orphan is terminated: whatever the dying
	// process's owner does on its way out cannot change what this boot saw.
	inflight, err := loadTurns(c.dir)
	if err != nil {
		// A corrupt record must not keep the supervisor from booting; the
		// cut turn then simply reads as ended.
		log.Printf("supervisor: read turn-in-flight record: %v", err)
		inflight = persistedTurns{}
	}
	if gen > 0 {
		sessionID := SessionID(c.conv.ConversationID, gen)
		switch session.TerminateOrphan(c.generationDir(gen), sessionID, c.opts.OrphanWait) {
		case session.OrphanTerminated:
			log.Printf("supervisor: terminated orphaned provider of %s", sessionID)
		case session.OrphanSurvived:
			log.Printf("supervisor: orphaned provider of %s outlived its termination wait", sessionID)
		case session.OrphanMismatch:
			log.Printf("supervisor: left a live process alone: its identity does not match %s", sessionID)
		}
	}
	records, err := c.store.after(0)
	if err != nil {
		return fmt.Errorf("read supervisor transcript: %w", err)
	}
	// The process is gone, so no request of this generation can still be
	// answered.
	open := openRequests(records, gen)
	resolve := func(match func(string) bool) error {
		for i := 0; i < len(open); i++ {
			req := open[i]
			if !match(req.rec.TurnID) {
				continue
			}
			open = append(open[:i], open[i+1:]...)
			i--
			if err := c.appendBootRecord(req.rec.TurnID, req.rec.Kind, RequestData{
				RequestID:      req.data.RequestID,
				ToolName:       req.data.ToolName,
				Stage:          StageResolved,
				Outcome:        RequestInterrupted,
				Origin:         req.data.Origin,
				ChildSessionID: req.data.ChildSessionID,
			}); err != nil {
				return err
			}
		}
		return nil
	}
	cut := map[string]bool{}
	if inflight.Generation == gen {
		for _, id := range inflight.TurnIDs {
			cut[id] = true
		}
	}
	if err := resolve(func(turn string) bool { return !cut[turn] }); err != nil {
		return err
	}
	if len(cut) == 0 {
		return c.applyBootChange()
	}
	for _, turn := range inflight.TurnIDs {
		if err := resolve(func(t string) bool { return t == turn }); err != nil {
			return err
		}
		if err := c.appendBootRecord(turn, KindMarker, MarkerData{Marker: MarkerInterrupted, Text: "Interrupted before restart"}); err != nil {
			return err
		}
	}
	c.outcome = OutcomeInterrupted
	c.interruptedBy = InterruptedByShutdown
	if err := saveTurns(c.dir, gen, nil); err != nil {
		return fmt.Errorf("clear supervisor turn-in-flight record: %w", err)
	}
	return c.applyBootChange()
}

func (c *Coordinator) applyBootChange() error {
	if c.pendingChange == nil {
		return nil
	}
	change := c.pendingChange
	previous := c.settings
	if err := saveSettings(c.dir, change.Target); err != nil {
		return err
	}
	if err := savePendingChange(c.dir, nil); err != nil {
		return err
	}
	c.settings = change.Target
	c.pendingChange = nil
	if err := c.rememberChangeLocked(change.RequestID); err != nil {
		return err
	}
	c.appendSettingsRecordsLocked(change, previous, false, "")
	return nil
}

func (c *Coordinator) rememberChangeLocked(id string) error {
	if id == "" {
		return nil
	}
	c.appliedChanges[id] = true
	return saveAppliedChanges(c.dir, c.appliedChanges)
}

type openRequest struct {
	rec  Record
	data RequestData
}

// openRequests returns the generation's requests still at the requested
// stage, in arrival order.
func openRequests(records []Record, gen int64) []openRequest {
	var open []openRequest
	for _, rec := range records {
		if rec.Generation != gen || (rec.Kind != KindPermission && rec.Kind != KindQuestion) {
			continue
		}
		var data RequestData
		if json.Unmarshal(rec.Data, &data) != nil {
			continue
		}
		if data.Stage == StageRequested {
			open = append(open, openRequest{rec: rec, data: data})
			continue
		}
		for i, req := range open {
			if req.data.RequestID == data.RequestID {
				open = append(open[:i], open[i+1:]...)
				break
			}
		}
	}
	return open
}

// appendBootRecord commits a display-only record at boot, before any
// subscriber exists.
func (c *Coordinator) appendBootRecord(turnID string, kind RecordKind, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, _, err := c.store.appendRecord(Record{
		Generation: c.conv.Generation,
		TurnID:     turnID,
		Kind:       kind,
		Visibility: VisibilityDisplayOnly,
		Data:       data,
	}); err != nil {
		return fmt.Errorf("append supervisor boot record: %w", err)
	}
	return nil
}

func validConversationID(id string) bool {
	_, err := uuid.Parse(id)
	return err == nil
}

func randomID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf[:])
}

// State returns the current read model.
func (c *Coordinator) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stateLocked()
}

func (c *Coordinator) stateLocked() State {
	st := State{
		ConversationID:  c.conv.ConversationID,
		Generation:      c.conv.Generation,
		SessionID:       c.sessionID,
		NativeSessionID: c.conv.NativeSessionID,
		Lifecycle:       c.lifecycle,
		LastTurnOutcome: c.outcome,
		InterruptedBy:   c.interruptedBy,
		Settings:        c.settings,
		PendingChange:   c.pendingChange,
		EffectiveModel:  c.effectiveModel,
		PermissionMode:  c.permMode,
		PendingRequests: append([]*llm.ControlRequestMessage(nil), c.pending...),
		Session:         c.session,
		HeadSeq:         c.store.head(),
		StreamEpoch:     c.conv.StreamEpoch,
		WorkDir:         c.opts.WorkDir,
	}
	if c.lifecycle == LifecycleStarting {
		st.StartingStep = c.step
	}
	if c.lifecycle == LifecycleFailed {
		st.Failure = c.failure
	}
	return st
}

// Busy reports whether the supervisor counts as active work.
func (c *Coordinator) Busy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lifecycle.Active()
}

// Transcript reads one page of the durable transcript.
func (c *Coordinator) Transcript(q PageQuery) (Page, error) {
	return c.store.page(q)
}

// UpdateSettings commits a harness choice. It is accepted only while no
// process exists.
func (c *Coordinator) UpdateSettings(s Settings) (State, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.Lock()
	locked := c.lifecycle != LifecycleStopped && c.lifecycle != LifecycleFailed
	c.mu.Unlock()
	if locked {
		return State{}, ErrSettingsLocked
	}
	if c.opts.Catalog == nil {
		return State{}, &SettingsInvalidError{Reason: "no model catalog is available"}
	}
	if err := c.opts.Catalog.ValidateSettings(s); err != nil {
		var invalid *SettingsInvalidError
		if !errors.As(err, &invalid) {
			err = &SettingsInvalidError{Reason: err.Error()}
		}
		return State{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := saveSettings(c.dir, s); err != nil {
		return State{}, fmt.Errorf("persist supervisor settings: %w", err)
	}
	c.settings = s
	c.publishStateLocked()
	return c.stateLocked(), nil
}

// ChangeSettings validates and merges a versioned request. A busy process
// retains one durable target; an idle process is retired before commit.
func (c *Coordinator) ChangeSettings(req SettingsChange) (State, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if req.RequestID == "" {
		return State{}, &SettingsInvalidError{Reason: "request_id is required"}
	}
	if c.appliedChanges[req.RequestID] {
		return c.stateLocked(), nil
	}
	if c.pendingChange != nil {
		if c.pendingChange.RequestID == req.RequestID {
			return c.stateLocked(), nil
		}
	}
	if req.ExpectedGeneration != c.conv.Generation {
		return State{}, &StaleGenerationError{Current: c.conv.Generation}
	}
	if c.pendingChange != nil {
		return State{}, &ChangePendingError{RequestID: c.pendingChange.RequestID}
	}
	target := c.settings
	if req.Harness != nil {
		target.Harness = *req.Harness
	}
	if req.Model != nil {
		target.Model = *req.Model
	}
	if req.Effort != nil {
		target.Effort = *req.Effort
	}
	if c.opts.Catalog == nil {
		return State{}, &SettingsInvalidError{Reason: "no model catalog is available"}
	}
	if err := c.opts.Catalog.ValidateSettings(target); err != nil {
		return State{}, &SettingsInvalidError{Reason: err.Error()}
	}
	if target == c.settings {
		if err := c.rememberChangeLocked(req.RequestID); err != nil {
			return State{}, err
		}
		return c.stateLocked(), nil
	}
	if c.lifecycle != LifecycleStopped && c.lifecycle != LifecycleFailed && target.Harness != c.settings.Harness {
		return State{}, ErrSettingsLocked
	}
	kind := "effort"
	if target.Model != c.settings.Model || target.Harness != c.settings.Harness {
		kind = "model"
	}
	change := &PendingChange{RequestID: req.RequestID, Kind: kind, Target: target, RequestedAt: c.opts.Now().UTC()}
	if c.lifecycle == LifecycleStopped || c.lifecycle == LifecycleFailed {
		previous := c.settings
		if err := saveSettings(c.dir, target); err != nil {
			return State{}, err
		}
		c.settings = target
		if err := c.rememberChangeLocked(req.RequestID); err != nil {
			return State{}, err
		}
		if previous.Complete() {
			c.appendSettingsRecordsLocked(change, previous, false, "")
		}
		c.publishStateLocked()
		return c.stateLocked(), nil
	}
	if c.lifecycle == LifecycleIdle {
		c.mu.Unlock()
		err := c.applyChange(change)
		c.mu.Lock()
		if err != nil {
			return State{}, err
		}
		return c.stateLocked(), nil
	}
	if err := savePendingChange(c.dir, change); err != nil {
		return State{}, err
	}
	c.pendingChange = change
	c.publishStateLocked()
	return c.stateLocked(), nil
}

type inPlaceSettingsSession interface {
	ApplySettings(context.Context, string, string) error
}

func (c *Coordinator) applyChange(change *PendingChange) error {
	c.mu.Lock()
	sess := c.session
	harness := c.settings.Harness
	effortChanged := change.Target.Effort != c.settings.Effort
	c.mu.Unlock()
	updater, capable := sess.(inPlaceSettingsSession)
	if capable && (harness == "codex" || (harness == "opencode" && !effortChanged)) {
		c.mu.Lock()
		previous := c.settings
		c.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), c.opts.SettingsUpdateTimeout)
		err := updater.ApplySettings(ctx, change.Target.Model, change.Target.Effort)
		cancel()
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.session == nil && c.pendingChange == nil {
			c.applyingChange = false
			c.publishStateLocked()
			return nil
		}
		if err != nil {
			c.pendingChange = nil
			if persistErr := savePendingChange(c.dir, nil); persistErr != nil {
				return persistErr
			}
			if persistErr := c.rememberChangeLocked(change.RequestID); persistErr != nil {
				return persistErr
			}
			c.appendSettingsRecordsLocked(change, previous, true, harnessDisplayName(harness)+": "+err.Error())
			c.lifecycle = LifecycleIdle
			c.publishStateLocked()
			return nil
		}
		if err := saveSettings(c.dir, change.Target); err != nil {
			return err
		}
		if err := savePendingChange(c.dir, nil); err != nil {
			return err
		}
		c.settings = change.Target
		if err := c.rememberChangeLocked(change.RequestID); err != nil {
			return err
		}
		c.pendingChange = nil
		c.applyingChange = false
		c.lifecycle = LifecycleIdle
		c.appendSettingsRecordsLocked(change, previous, false, "")
		c.publishStateLocked()
		return nil
	}
	return c.applyRelaunchChange(change)
}

func (c *Coordinator) CancelPendingChange(id string) (State, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pendingChange == nil || c.pendingChange.RequestID != id {
		return State{}, ErrPendingChangeNotFound
	}
	if err := savePendingChange(c.dir, nil); err != nil {
		return State{}, err
	}
	c.pendingChange = nil
	if c.applyingChange && len(c.turns) == 0 {
		c.applyingChange = false
		if c.session != nil {
			c.lifecycle = LifecycleIdle
		}
	}
	c.publishStateLocked()
	return c.stateLocked(), nil
}

func (c *Coordinator) applyRelaunchChange(change *PendingChange) error {
	c.mu.Lock()
	previous := c.settings
	sess := c.session
	sessionID := c.sessionID
	if c.pendingChange != nil && c.pendingChange.RequestID == change.RequestID {
		if err := savePendingChange(c.dir, nil); err != nil {
			c.mu.Unlock()
			return err
		}
		c.pendingChange = nil
	}
	c.applyingChange = true
	c.ending = true
	c.mu.Unlock()
	if sess != nil {
		_ = sess.Stop()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sessionID == sessionID && sess != nil {
		c.resetProcessLocked()
	}
	if err := saveSettings(c.dir, change.Target); err != nil {
		return err
	}
	if err := savePendingChange(c.dir, nil); err != nil {
		return err
	}
	c.settings = change.Target
	if err := c.rememberChangeLocked(change.RequestID); err != nil {
		return err
	}
	c.pendingChange = nil
	c.relaunchPrevious = &previous
	c.applyingChange = false
	c.lifecycle = LifecycleStopped
	c.appendSettingsRecordsLocked(change, previous, false, "")
	c.publishStateLocked()
	return nil
}

func (c *Coordinator) appendSettingsRecordsLocked(change *PendingChange, previous Settings, reverted bool, reason string) {
	if change == nil {
		return
	}
	marker := MarkerSettingsChanged
	text := ""
	if reverted {
		marker = MarkerSettingsReverted
		text = "Couldn't apply " + change.Target.Model + " — still using " + c.settings.Model
		if reason != "" {
			text += ": " + reason
		}
	} else if change.Kind == "model" {
		text = "Model changed to " + change.Target.Model
	} else {
		text = "Effort changed to " + displayEffort(change.Target.Effort)
	}
	c.appendMarkerLocked(c.conv.Generation, "", MarkerData{Marker: marker, Text: text})
	if reverted {
		return
	}
	if change.Kind == "model" && change.Target.Effort != previous.Effort {
		c.appendMarkerLocked(c.conv.Generation, "", MarkerData{Marker: MarkerSettingsChanged, Text: "Effort changed to " + displayEffort(change.Target.Effort)})
	}
	note := NoteData{Text: "Agentico note: The user changed " + change.Kind + " at this point. Current model: " + change.Target.Model + "; effort: " + change.Target.Effort + "."}
	data, _ := json.Marshal(note)
	rec, _, err := c.store.appendRecord(Record{Generation: c.conv.Generation, Kind: KindNote, Visibility: VisibilityModelOnly, Data: data})
	if err == nil {
		c.publishLocked(Event{Kind: EventRecord, Generation: c.conv.Generation, Record: &rec})
	}
}

func displayEffort(effort string) string {
	if effort == "" {
		return "Default"
	}
	return effort
}

// Send commits one user message and delivers it to the harness, launching
// the process when none exists. Sends arriving while a launch is in flight
// join it and are delivered in arrival order after the handshake. Hidden
// context, when present, reaches the harness ahead of the visible text; the
// committed user record holds only the visible text.
func (c *Coordinator) Send(ctx context.Context, text, hiddenContext, clientMessageID string) (SendResult, error) {
	c.opMu.Lock()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		c.opMu.Unlock()
		return SendResult{}, ErrClosed
	}
	if rec, ok := c.store.lookupClientMessage(clientMessageID); ok {
		c.mu.Unlock()
		c.opMu.Unlock()
		return SendResult{Record: rec}, nil
	}
	if !c.settings.Complete() {
		c.mu.Unlock()
		c.opMu.Unlock()
		return SendResult{}, ErrSettingsRequired
	}
	switch {
	case c.lifecycle == LifecycleStarting && c.launch != nil:
		j := newJoiner(text, hiddenContext, clientMessageID, false)
		c.launch.joiners = append(c.launch.joiners, j)
		c.mu.Unlock()
		c.opMu.Unlock()
		return j.wait(ctx)
	case c.lifecycle == LifecycleIdle && c.session != nil:
		defer c.opMu.Unlock()
		if !canDeliver(c.session, hiddenContext) {
			c.mu.Unlock()
			return SendResult{}, ErrHiddenContextUnsupported
		}
		rec, _, err := c.appendUserLocked(text, clientMessageID)
		if err != nil {
			c.mu.Unlock()
			return SendResult{}, err
		}
		c.lifecycle = LifecycleRunning
		c.publishStateLocked()
		sess := c.session
		c.mu.Unlock()
		if err := deliver(sess, text, hiddenContext); err != nil {
			return SendResult{}, fmt.Errorf("deliver supervisor message: %w", err)
		}
		return SendResult{Record: rec}, nil
	case c.lifecycle.inTurn():
		c.mu.Unlock()
		c.opMu.Unlock()
		return SendResult{}, ErrTurnActive
	}
	attempt, err := c.beginLaunchLocked()
	if err != nil {
		c.mu.Unlock()
		c.opMu.Unlock()
		return SendResult{}, err
	}
	j := newJoiner(text, hiddenContext, clientMessageID, true)
	attempt.joiners = append(attempt.joiners, j)
	c.mu.Unlock()
	c.opMu.Unlock()
	// The launch outlives the initiating request so joiners are served even
	// if the initiator's client goes away.
	go c.runLaunch(attempt)
	return j.wait(ctx)
}

func newJoiner(text, hidden, cmid string, initiator bool) *joiner {
	return &joiner{text: text, hidden: hidden, cmid: cmid, initiator: initiator, done: make(chan joinResult, 1)}
}

// canDeliver reports whether the session can carry the message's hidden
// context; a message without any is always deliverable.
func canDeliver(sess ports.SessionView, hiddenContext string) bool {
	if hiddenContext == "" {
		return true
	}
	_, ok := sess.(ports.HiddenContextSender)
	return ok
}

// deliver sends one user message, routing hidden context through the
// session's hidden-context send so the provider sees it ahead of the
// visible text.
func deliver(sess ports.SessionView, text, hiddenContext string) error {
	if sender, ok := sess.(ports.HiddenContextSender); ok && hiddenContext != "" {
		return sender.SendUserMessageWithHiddenContext(text, hiddenContext)
	}
	return sess.SendUserMessage(text)
}

func (j *joiner) wait(ctx context.Context) (SendResult, error) {
	select {
	case r := <-j.done:
		return r.res, r.err
	case <-ctx.Done():
		return SendResult{}, ctx.Err()
	}
}

// beginLaunchLocked opens a new generation: it reserves work admission for
// the launch window, advances and persists the generation, moves the
// transcript fence and reports starting.
func (c *Coordinator) beginLaunchLocked() (*launchAttempt, error) {
	var reservation *workadmission.Reservation
	if c.opts.Admission != nil {
		res, err := c.opts.Admission.Acquire(workadmission.CategorySupervisor)
		if err != nil {
			return nil, err
		}
		reservation = res
	}
	conv := c.conv
	conv.Generation++
	if err := saveConversation(c.dir, conv); err != nil {
		reservation.Release()
		return nil, fmt.Errorf("persist supervisor generation: %w", err)
	}
	c.conv = conv
	c.store.setGeneration(conv.Generation)
	c.resetProcessLocked()
	c.failure = nil
	c.sessionID = SessionID(conv.ConversationID, conv.Generation)
	c.lifecycle = LifecycleStarting
	c.step = StepLaunching
	if c.rebuildsLocked() {
		c.step = StepRebuilding
	}
	attempt := &launchAttempt{
		generation:  conv.Generation,
		sessionID:   c.sessionID,
		cancel:      make(chan struct{}),
		reservation: reservation,
	}
	c.launch = attempt
	c.publishStateLocked()
	return attempt, nil
}

// resetProcessLocked clears every per-process field.
func (c *Coordinator) resetProcessLocked() {
	c.session = nil
	c.sessionID = ""
	c.effectiveModel = ""
	c.turns = nil
	c.turnCount = 0
	c.pending = nil
	c.streamID = ""
	c.streamChunks = 0
	c.streamCount = 0
	c.ending = false
	c.keepTurns = false
	c.resumeID = ""
	c.fallbackMarked = false
	c.interrupt = nil
	c.step = ""
	c.permMode = PermissionMode{Requested: RequestedPermissionMode}
}

// converterLocked returns the native-session converter for the chosen
// harness; nil means the harness launches fresh every generation.
func (c *Coordinator) converterLocked() Converter {
	return c.opts.Converters[c.settings.Harness]
}

// harnessAssignsIDLocked reports whether the chosen harness mints its own
// native session id, which the coordinator adopts instead of pre-assigning.
func (c *Coordinator) harnessAssignsIDLocked() bool {
	if c.seedsHistoryLocked() {
		return false
	}
	assigned, ok := c.converterLocked().(HarnessAssignedIDs)
	return ok && assigned.HarnessAssignsSessionID()
}

// seedsHistoryLocked reports whether the chosen harness receives history as
// a seed file instead of resuming a native session; such a harness has no
// durable native id at all.
func (c *Coordinator) seedsHistoryLocked() bool {
	seeder, ok := c.converterLocked().(HistorySeeder)
	return ok && seeder.SeedsHistory()
}

// rebuildsLocked reports whether the next launch rebuilds native history:
// a converter exists and, for a harness that assigns its own ids, an id
// was already adopted from an earlier launch. A seeding harness rebuilds
// only when the transcript holds history to seed.
func (c *Coordinator) rebuildsLocked() bool {
	if c.converterLocked() == nil {
		return false
	}
	if c.seedsHistoryLocked() {
		return c.store.hasContent()
	}
	return !c.harnessAssignsIDLocked() || c.conv.NativeSessionID != ""
}

// setOutcomeLocked records how the latest turn ended and who cut it.
func (c *Coordinator) setOutcomeLocked(outcome TurnOutcome, by InterruptedBy) {
	c.outcome = outcome
	c.interruptedBy = by
}

// persistTurnsLocked rewrites the turn-in-flight record from the turns
// still awaiting a result.
func (c *Coordinator) persistTurnsLocked() {
	if err := saveTurns(c.dir, c.conv.Generation, c.turns); err != nil {
		log.Printf("supervisor: persist turn-in-flight record: %v", err)
	}
}

func (c *Coordinator) generationDir(gen int64) string {
	return filepath.Join(c.dir, conversationsDir, c.conv.ConversationID, generationsDirName, strconv.FormatInt(gen, 10))
}

func (c *Coordinator) runLaunch(attempt *launchAttempt) {
	c.mu.Lock()
	settings := c.settings
	conversationID := c.conv.ConversationID
	genDir := c.generationDir(attempt.generation)
	c.mu.Unlock()
	if err := os.MkdirAll(genDir, 0o755); err != nil {
		c.failLaunch(attempt, fmt.Errorf("prepare generation dir: %w", err))
		return
	}
	resumeID, seedPath, err := c.rebuildNative(attempt)
	if err != nil {
		c.failLaunch(attempt, err)
		return
	}
	obs := &generationObserver{c: c, generation: attempt.generation, handshake: make(chan struct{})}
	sess, err := c.opts.Launcher.Launch(context.Background(), LaunchRequest{
		SessionID:       attempt.sessionID,
		ConversationID:  conversationID,
		Generation:      attempt.generation,
		Settings:        settings,
		ResumeSessionID: resumeID,
		SeedHistoryPath: seedPath,
		WorkDir:         c.opts.WorkDir,
		PIDDir:          genDir,
		LogPath:         filepath.Join(genDir, "output.txt"),
		StderrPath:      filepath.Join(genDir, "stderr.log"),
		Observer:        obs,
		OnSpawned:       func() { c.markHandshake(attempt) },
	})
	if err != nil {
		c.failLaunch(attempt, err)
		return
	}
	timer := time.NewTimer(c.opts.HandshakeTimeout)
	defer timer.Stop()
	select {
	case <-obs.handshake:
	case <-sess.Done():
		c.failLaunch(attempt, errors.New("the harness exited before completing its handshake"))
		return
	case <-attempt.cancel:
		_ = sess.Stop()
		c.failLaunch(attempt, errors.New("the launch was cancelled"))
		return
	case <-timer.C:
		_ = sess.Stop()
		c.failLaunch(attempt, fmt.Errorf("the harness did not answer its handshake within %s", c.opts.HandshakeTimeout))
		return
	}
	c.completeLaunch(attempt, sess)
}

// rebuildNative renders the transcript into the harness's native session
// under the conversation's stable native id and returns the id to resume,
// or "" for a fresh launch. For a seeding harness it mints no id and
// returns the seed file instead. A conversion error degrades to a fresh
// launch with a visible marker; any other error fails the launch.
func (c *Coordinator) rebuildNative(attempt *launchAttempt) (resumeID, seedPath string, err error) {
	c.mu.Lock()
	converter := c.converterLocked()
	if converter == nil || c.launch != attempt || !c.rebuildsLocked() {
		c.mu.Unlock()
		return "", "", nil
	}
	seeds := c.seedsHistoryLocked()
	nativeID := c.conv.NativeSessionID
	if seeds {
		nativeID = ""
	} else if nativeID == "" {
		conv := c.conv
		conv.NativeSessionID = uuid.NewString()
		if err := saveConversation(c.dir, conv); err != nil {
			c.mu.Unlock()
			return "", "", fmt.Errorf("persist native session id: %w", err)
		}
		c.conv = conv
		nativeID = conv.NativeSessionID
	}
	conversationID := c.conv.ConversationID
	settings := c.settings
	c.mu.Unlock()
	records, err := c.store.after(0)
	if err != nil {
		return "", "", fmt.Errorf("read transcript for rebuild: %w", err)
	}
	res, err := converter.Rebuild(context.Background(), RebuildInput{
		ConversationID:  conversationID,
		NativeSessionID: nativeID,
		WorkDir:         c.opts.WorkDir,
		ConversationDir: c.store.dir,
		Model:           settings.Model,
		Effort:          settings.Effort,
		Records:         records,
	})
	var conversion *ConversionError
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case errors.As(err, &conversion):
		log.Printf("supervisor: %v; launching without history", err)
		if c.launch == attempt {
			c.appendMarkerLocked(attempt.generation, "", MarkerData{
				Marker: MarkerHistoryNotRestored,
				Text:   "History could not be restored for this session; starting without it",
			})
		}
		res = RebuildResult{}
	case err != nil:
		return "", "", fmt.Errorf("rebuild %s session history: %w", converter.Harness(), err)
	}
	if c.launch == attempt && c.step == StepRebuilding {
		c.step = StepLaunching
		c.publishStateLocked()
	}
	if !res.Resume {
		return "", "", nil
	}
	if seeds {
		return "", res.Path, nil
	}
	if c.launch == attempt {
		c.resumeID = res.SessionID
	}
	return res.SessionID, "", nil
}

// appendMarkerLocked commits a display-only marker for the generation.
func (c *Coordinator) appendMarkerLocked(gen int64, turnID string, marker MarkerData) {
	data, err := json.Marshal(marker)
	if err != nil {
		log.Printf("supervisor: encode %s marker: %v", marker.Marker, err)
		return
	}
	rec, _, err := c.store.appendRecord(Record{
		Generation: gen,
		TurnID:     turnID,
		Kind:       KindMarker,
		Visibility: VisibilityDisplayOnly,
		Data:       data,
	})
	if err != nil {
		log.Printf("supervisor: append %s marker: %v", marker.Marker, err)
		return
	}
	c.publishLocked(Event{Kind: EventRecord, Generation: gen, Record: &rec})
}

func (c *Coordinator) markHandshake(attempt *launchAttempt) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.launch == attempt && c.step != StepHandshake {
		c.step = StepHandshake
		c.publishStateLocked()
	}
}

func (c *Coordinator) completeLaunch(attempt *launchAttempt, sess ports.SessionView) {
	c.mu.Lock()
	if attempt.cancelled || c.launch != attempt {
		c.mu.Unlock()
		_ = sess.Stop()
		c.failLaunch(attempt, errors.New("the launch was cancelled"))
		return
	}
	c.session = sess
	c.launch = nil
	c.step = ""
	results := make([]joinResult, len(attempt.joiners))
	var deliveries []*joiner
	for i, j := range attempt.joiners {
		if _, ok := c.store.lookupClientMessage(j.cmid); !ok && !canDeliver(sess, j.hidden) {
			results[i] = joinResult{err: ErrHiddenContextUnsupported}
			continue
		}
		rec, existing, err := c.appendUserLocked(j.text, j.cmid)
		if err != nil {
			results[i] = joinResult{err: err}
			continue
		}
		if !existing {
			deliveries = append(deliveries, j)
		}
		results[i] = joinResult{res: SendResult{Record: rec, Launched: j.initiator}}
	}
	if len(c.turns) > 0 {
		c.lifecycle = LifecycleRunning
	} else {
		c.lifecycle = LifecycleIdle
	}
	c.publishStateLocked()
	sessionID := c.sessionID
	c.mu.Unlock()
	// The registered session is detector-visible through the lifecycle now,
	// so the launch reservation can settle.
	attempt.reservation.Release()
	go c.watchExit(sess, sessionID)
	for _, j := range deliveries {
		if err := deliver(sess, j.text, j.hidden); err != nil {
			// The exit watcher reports the dead process.
			log.Printf("supervisor: deliver message to %s: %v", sessionID, err)
			break
		}
	}
	for i, j := range attempt.joiners {
		j.done <- results[i]
	}
}

func (c *Coordinator) failLaunch(attempt *launchAttempt, cause error) {
	c.mu.Lock()
	if c.launch == attempt {
		c.launch = nil
		c.resetProcessLocked()
		c.failure = &LaunchFailedError{Err: cause}
		if c.relaunchPrevious != nil {
			previous := *c.relaunchPrevious
			failed := &PendingChange{Kind: "model", Target: c.settings}
			if failed.Target.Model == previous.Model {
				failed.Kind = "effort"
			}
			if err := saveSettings(c.dir, previous); err == nil {
				c.settings = previous
				c.appendSettingsRecordsLocked(failed, previous, true, cause.Error())
			} else {
				log.Printf("supervisor: restore settings after failed relaunch: %v", err)
			}
			c.relaunchPrevious = nil
		}
		// The user record was never committed; the marker is the
		// transcript's durable trace of the failed attempt.
		c.appendMarkerLocked(attempt.generation, "", MarkerData{
			Marker: MarkerError,
			Text:   cause.Error(),
			Code:   string(errcat.SupervisorLaunchFailed),
		})
		if c.pendingChange != nil {
			change := c.pendingChange
			previous := c.settings
			err := saveSettings(c.dir, change.Target)
			if err == nil {
				if err = savePendingChange(c.dir, nil); err == nil {
					c.settings = change.Target
					c.pendingChange = nil
					if err = c.rememberChangeLocked(change.RequestID); err == nil {
						c.appendSettingsRecordsLocked(change, previous, false, "")
					}
				}
			}
			if err != nil {
				log.Printf("supervisor: apply change after failed launch: %v", err)
			}
		}
		c.lifecycle = LifecycleFailed
		c.publishStateLocked()
	}
	c.mu.Unlock()
	attempt.reservation.Release()
	for _, j := range attempt.joiners {
		j.done <- joinResult{err: &LaunchFailedError{Err: cause}}
	}
}

// appendUserLocked commits a user record on a new turn, or returns the
// record already committed for the client message id.
func (c *Coordinator) appendUserLocked(text, cmid string) (Record, bool, error) {
	if rec, ok := c.store.lookupClientMessage(cmid); ok {
		return rec, true, nil
	}
	data, err := json.Marshal(UserData{Text: text})
	if err != nil {
		return Record{}, false, err
	}
	c.turnCount++
	turnID := fmt.Sprintf("g%d.t%d", c.conv.Generation, c.turnCount)
	rec, existing, err := c.store.appendRecord(Record{
		Generation:      c.conv.Generation,
		TurnID:          turnID,
		Kind:            KindUser,
		Visibility:      VisibilityContent,
		ClientMessageID: cmid,
		Data:            data,
	})
	if err != nil || existing {
		return rec, existing, err
	}
	c.turns = append(c.turns, turnID)
	c.persistTurnsLocked()
	c.publishLocked(Event{Kind: EventRecord, Generation: rec.Generation, Record: &rec})
	return rec, false, nil
}

func (c *Coordinator) watchExit(sess ports.SessionView, sessionID string) {
	<-sess.Done()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sessionID != sessionID {
		return
	}
	c.applyExitLocked(sess.Status() != ports.SessionFailed)
}

// applyExitLocked records the current process's exit and reports stopped.
func (c *Coordinator) applyExitLocked(clean bool) {
	switch {
	case c.ending:
		// End, shutdown and interrupt termination set the outcome themselves.
	case c.interrupt != nil:
		c.setOutcomeLocked(OutcomeInterrupted, InterruptedByUser)
	case !clean || c.lifecycle.inTurn():
		c.setOutcomeLocked(OutcomeFailed, InterruptedByNone)
	}
	if c.interrupt != nil {
		close(c.interrupt.done)
	}
	keep := c.keepTurns
	c.resetProcessLocked()
	if !keep {
		c.persistTurnsLocked()
	}
	c.lifecycle = LifecycleStopped
	if c.pendingChange != nil {
		change := c.pendingChange
		previous := c.settings
		err := saveSettings(c.dir, change.Target)
		if err == nil {
			if err = savePendingChange(c.dir, nil); err == nil {
				c.settings = change.Target
				if err := c.rememberChangeLocked(change.RequestID); err != nil {
					log.Printf("supervisor: remember applied change: %v", err)
				}
				c.pendingChange = nil
				c.relaunchPrevious = &previous
				c.appendSettingsRecordsLocked(change, previous, false, "")
			}
		}
		if err != nil {
			log.Printf("supervisor: apply change after exit: %v", err)
		}
	}
	if !c.applyingChange {
		c.publishStateLocked()
	}
}

// Interrupt asks the harness to interrupt the current turn and returns at
// once. The lifecycle returns to idle only when the turn result or process
// exit is observed; after the grace the process is terminated.
func (c *Coordinator) Interrupt() (ActionResult, State) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.Lock()
	switch {
	case c.lifecycle == LifecycleStarting && c.launch != nil:
		c.cancelLaunchLocked(OutcomeInterrupted, InterruptedByUser)
	case c.lifecycle.inTurn() && c.session != nil && c.interrupt == nil:
		attempt := &interruptAttempt{done: make(chan struct{})}
		c.interrupt = attempt
		c.pending = nil
		c.publishStateLocked()
		sess, sessionID := c.session, c.sessionID
		c.mu.Unlock()
		if err := sess.Interrupt(); err != nil {
			log.Printf("supervisor: interrupt %s: %v", sessionID, err)
		}
		go c.interruptGrace(attempt, sess, sessionID)
		return ActionAccepted, c.State()
	}
	st := c.stateLocked()
	c.mu.Unlock()
	return ActionAccepted, st
}

func (c *Coordinator) interruptGrace(attempt *interruptAttempt, sess ports.SessionView, sessionID string) {
	timer := time.NewTimer(c.opts.InterruptGrace)
	defer timer.Stop()
	select {
	case <-attempt.done:
		return
	case <-sess.Done():
		return
	case <-timer.C:
	}
	c.mu.Lock()
	if c.interrupt != attempt || c.sessionID != sessionID {
		c.mu.Unlock()
		return
	}
	c.ending = true
	c.setOutcomeLocked(OutcomeInterrupted, InterruptedByUser)
	c.mu.Unlock()
	_ = sess.Stop()
	c.mu.Lock()
	if c.sessionID == sessionID {
		c.applyExitLocked(false)
	}
	c.mu.Unlock()
}

// cancelLaunchLocked abandons the in-flight launch; its goroutine stops the
// process once the launcher returns and fails the joiners.
func (c *Coordinator) cancelLaunchLocked(outcome TurnOutcome, by InterruptedBy) {
	attempt := c.launch
	attempt.cancelled = true
	close(attempt.cancel)
	c.launch = nil
	c.resetProcessLocked()
	c.lifecycle = LifecycleStopped
	c.setOutcomeLocked(outcome, by)
	c.publishStateLocked()
}

// End stops the process, keeping the transcript and settings.
func (c *Coordinator) End() (ActionResult, State) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	return c.endLocked()
}

func (c *Coordinator) endLocked() (ActionResult, State) {
	c.mu.Lock()
	switch {
	case c.launch != nil:
		c.cancelLaunchLocked(c.outcome, c.interruptedBy)
		st := c.stateLocked()
		c.mu.Unlock()
		return ActionEnded, st
	case c.session != nil:
		sess, sessionID := c.session, c.sessionID
		c.ending = true
		if c.lifecycle.inTurn() {
			c.setOutcomeLocked(OutcomeInterrupted, InterruptedByUser)
			c.keepTurns = true
		}
		c.mu.Unlock()
		_ = sess.Stop()
		c.mu.Lock()
		if c.sessionID == sessionID {
			c.applyExitLocked(true)
		}
		st := c.stateLocked()
		c.mu.Unlock()
		return ActionEnded, st
	}
	st := c.stateLocked()
	c.mu.Unlock()
	return ActionNotActive, st
}

// Close ends the process and refuses every later send. Server shutdown
// calls it before the session manager shuts down.
func (c *Coordinator) Close() error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.endLocked()
	c.mu.Lock()
	for sub := range c.subs {
		delete(c.subs, sub)
		close(sub.ch)
	}
	c.mu.Unlock()
	return c.store.close()
}

// generationObserver routes one generation's session output into the
// coordinator. Any provider output completes the launch handshake.
type generationObserver struct {
	c          *Coordinator
	generation int64
	once       sync.Once
	handshake  chan struct{}
}

func (o *generationObserver) ObserveSessionMessage(sessionID string, msg llm.SDKMessage) {
	o.once.Do(func() { close(o.handshake) })
	o.c.observeMessage(o.generation, sessionID, msg)
}

func (o *generationObserver) ObserveControlAnswer(sessionID string, answer ports.ControlAnswer) {
	o.c.observeAnswer(o.generation, sessionID, answer)
}

func (c *Coordinator) currentTurnLocked() string {
	if len(c.turns) == 0 {
		return ""
	}
	return c.turns[0]
}

func (c *Coordinator) observeMessage(gen int64, sessionID string, msg llm.SDKMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	current := sessionID == c.sessionID && gen == c.conv.Generation
	if msg.Init != nil && current {
		c.observeInitLocked(gen, msg.Init)
	}
	if msg.Type == "stream_event" {
		if current {
			c.observeStreamLocked(gen, msg)
		}
		return
	}
	if msg.Origin.Kind == llm.EventOriginTask && msg.ControlRequest == nil {
		// Sub-agent output belongs to the sub-agent, not the conversation;
		// only the permissions and questions it raises reach the user.
		return
	}
	switch {
	case msg.Assistant != nil && msg.Subtype != "partial":
		var text, tools []llm.ContentBlock
		for _, block := range msg.Assistant.Message.Content {
			switch {
			case block.IsText():
				text = append(text, block)
			case block.IsToolUse():
				tools = append(tools, block)
			}
		}
		if len(text) > 0 {
			streamID := msg.Assistant.Message.ID
			if streamID == "" && current {
				streamID = c.streamID
			}
			c.appendProviderLocked(gen, KindAssistant, ContentData{Content: text}, streamID)
			if current {
				c.streamID, c.streamChunks = "", 0
			}
		}
		if len(tools) > 0 {
			c.appendProviderLocked(gen, KindToolUse, ContentData{Content: tools}, "")
		}
	case msg.User != nil && !msg.LocallyAppended:
		var results []llm.ContentBlock
		for _, block := range msg.User.Message.Content {
			if block.IsToolResult() {
				results = append(results, block)
			}
		}
		if len(results) > 0 {
			c.appendProviderLocked(gen, KindToolResult, ContentData{Content: results}, "")
		}
	case msg.ControlRequest != nil && current:
		c.surfaceRequestLocked(gen, msg.ControlRequest)
	case msg.Result != nil && current:
		c.observeResultLocked(msg.Result)
	}
}

// observeInitLocked records what the harness reports at startup: the
// effective model, the effective permission mode (marking a mode a policy
// restricted, once per generation), and whether it resumed the pre-assigned
// native session.
func (c *Coordinator) observeInitLocked(gen int64, init *llm.SystemInitMessage) {
	if init.Model != "" {
		c.effectiveModel = init.Model
	}
	if init.PermissionMode != "" && c.permMode.Effective == "" {
		c.permMode.Effective = init.PermissionMode
		if init.PermissionMode != c.permMode.Requested {
			c.permMode.RestrictedByPolicy = true
			c.appendMarkerLocked(gen, c.currentTurnLocked(), MarkerData{
				Marker: MarkerPermissionRestricted,
				Text: fmt.Sprintf("Permission mode restricted by policy: the supervisor runs in %s mode instead of %s",
					init.PermissionMode, c.permMode.Requested),
			})
		}
	}
	if c.harnessAssignsIDLocked() && init.SessionID != "" {
		c.adoptNativeIDLocked(gen, init)
	} else if c.resumeID != "" && init.SessionID != "" && init.SessionID != c.resumeID {
		// The harness chose its own id; later output still belongs to this
		// generation, so the launch stands.
		log.Printf("supervisor: harness resumed as session %s, not the pre-assigned %s", init.SessionID, c.resumeID)
		c.resumeID = init.SessionID
	}
	c.publishStateLocked()
}

// adoptNativeIDLocked applies the id rule for a harness that mints its own
// session ids: after a launch that did not resume (first launch, empty
// history, or a resume the harness could not read) the reported id becomes
// the conversation's native id, so the next rebuild writes under it.
func (c *Coordinator) adoptNativeIDLocked(gen int64, init *llm.SystemInitMessage) {
	fallback := init.ResumeOutcome == llm.ResumeOutcomeFallback
	if fallback && !c.fallbackMarked {
		c.fallbackMarked = true
		c.appendMarkerLocked(gen, c.currentTurnLocked(), MarkerData{
			Marker: MarkerHistoryNotRestored,
			Text:   harnessDisplayName(c.settings.Harness) + " could not read the restored thread; continuing on a fresh thread",
		})
	}
	if c.resumeID != "" && !fallback {
		return
	}
	c.resumeID = ""
	if init.SessionID == c.conv.NativeSessionID {
		return
	}
	conv := c.conv
	conv.NativeSessionID = init.SessionID
	if err := saveConversation(c.dir, conv); err != nil {
		log.Printf("supervisor: persist adopted native session id: %v", err)
		return
	}
	c.conv = conv
}

func harnessDisplayName(harness string) string {
	switch harness {
	case "codex":
		return "Codex"
	case "claude":
		return "Claude"
	case "opencode":
		return "OpenCode"
	}
	return harness
}

func (c *Coordinator) observeStreamLocked(gen int64, msg llm.SDKMessage) {
	if msg.StreamMessageID != "" {
		c.streamID, c.streamChunks = msg.StreamMessageID, 0
	}
	if msg.StreamDeltaText == "" {
		return
	}
	turnID := c.currentTurnLocked()
	if c.streamID == "" {
		c.streamCount++
		c.streamID = fmt.Sprintf("%s.s%d", turnID, c.streamCount)
	}
	delta := Delta{TurnID: turnID, StreamMessageID: c.streamID, ChunkIndex: c.streamChunks, Text: msg.StreamDeltaText}
	c.streamChunks++
	c.publishLocked(Event{Kind: EventDelta, Generation: gen, Delta: &delta})
}

// appendProviderLocked commits provider output tagged with its generation;
// the store rejects output from a retired generation.
func (c *Coordinator) appendProviderLocked(gen int64, kind RecordKind, payload any, streamID string) {
	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("supervisor: encode %s record: %v", kind, err)
		return
	}
	visibility := VisibilityContent
	if kind == KindPermission || kind == KindQuestion {
		visibility = VisibilityDisplayOnly
	}
	turnID := ""
	if gen == c.conv.Generation {
		turnID = c.currentTurnLocked()
	}
	rec, _, err := c.store.appendRecord(Record{
		Generation:      gen,
		TurnID:          turnID,
		Kind:            kind,
		Visibility:      visibility,
		StreamMessageID: streamID,
		Data:            data,
	})
	if errors.Is(err, ErrRetiredGeneration) {
		return
	}
	if err != nil {
		log.Printf("supervisor: append %s record: %v", kind, err)
		return
	}
	c.publishLocked(Event{Kind: EventRecord, Generation: gen, Record: &rec})
}

func (c *Coordinator) surfaceRequestLocked(gen int64, req *llm.ControlRequestMessage) {
	for _, p := range c.pending {
		if p.RequestID == req.RequestID {
			return
		}
	}
	c.pending = append(c.pending, req)
	kind := KindPermission
	if req.Request.ToolName == askUserQuestionTool {
		kind = KindQuestion
	}
	origin, child := RequestOrigin(req.Origin)
	c.appendProviderLocked(gen, kind, RequestData{
		RequestID:      req.RequestID,
		ToolName:       req.Request.ToolName,
		Stage:          StageRequested,
		Outcome:        RequestPending,
		Input:          req.Request.Input,
		Origin:         origin,
		ChildSessionID: child,
	}, "")
	c.publishLocked(Event{Kind: EventRequest, Generation: gen, Request: req, Session: c.session})
	if c.lifecycle.inTurn() {
		c.lifecycle = c.waitingLifecycleLocked()
	}
	c.publishStateLocked()
}

// waitingLifecycleLocked derives the waiting state from the newest pending
// request.
func (c *Coordinator) waitingLifecycleLocked() Lifecycle {
	if len(c.pending) == 0 {
		return LifecycleRunning
	}
	if c.pending[len(c.pending)-1].Request.ToolName == askUserQuestionTool {
		return LifecycleWaitingQuestion
	}
	return LifecycleWaitingPermission
}

func (c *Coordinator) observeAnswer(gen int64, sessionID string, answer ports.ControlAnswer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	current := sessionID == c.sessionID && gen == c.conv.Generation
	toolName := answer.ToolName
	origin, child := RequestOriginRoot, ""
	for i, p := range c.pending {
		if p.RequestID == answer.RequestID {
			toolName = p.Request.ToolName
			origin, child = RequestOrigin(p.Origin)
			c.pending = append(c.pending[:i:i], c.pending[i+1:]...)
			break
		}
	}
	kind, outcome := KindPermission, RequestDenied
	switch {
	case toolName == askUserQuestionTool:
		kind, outcome = KindQuestion, RequestAnswered
	case answer.Allowed:
		outcome = RequestAllowed
	}
	c.appendProviderLocked(gen, kind, RequestData{
		RequestID:      answer.RequestID,
		ToolName:       toolName,
		Stage:          StageResolved,
		Outcome:        outcome,
		Reason:         answer.Reason,
		Answers:        answer.Answers,
		Origin:         origin,
		ChildSessionID: child,
	}, "")
	if current && (c.lifecycle == LifecycleWaitingPermission || c.lifecycle == LifecycleWaitingQuestion) {
		c.lifecycle = c.waitingLifecycleLocked()
	}
	c.publishStateLocked()
}

func (c *Coordinator) observeResultLocked(result *llm.ResultMessage) {
	if c.relaunchPrevious != nil && !result.IsError && result.Subtype != "error" {
		c.relaunchPrevious = nil
	}
	if len(c.turns) > 0 {
		c.turns = c.turns[1:]
		c.persistTurnsLocked()
	}
	// Turn end clears any request the harness left unanswered.
	c.pending = nil
	c.streamID, c.streamChunks = "", 0
	switch {
	case c.interrupt != nil:
		c.setOutcomeLocked(OutcomeInterrupted, InterruptedByUser)
		close(c.interrupt.done)
		c.interrupt = nil
	case result.IsError || result.Subtype == "error":
		c.setOutcomeLocked(OutcomeFailed, InterruptedByNone)
	default:
		c.setOutcomeLocked(OutcomeCompleted, InterruptedByNone)
	}
	if len(c.turns) == 0 {
		if c.pendingChange != nil {
			c.applyingChange = true
			go c.applyPendingChange()
			return
		}
		c.lifecycle = LifecycleIdle
	} else {
		c.lifecycle = LifecycleRunning
	}
	c.publishStateLocked()
}

func (c *Coordinator) applyPendingChange() {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.Lock()
	change := c.pendingChange
	c.mu.Unlock()
	if change != nil {
		_ = c.applyChange(change)
	}
}

// Subscription is one live event consumer. Replay holds the committed
// records after the subscriber's cursor; live events follow on Events.
type Subscription struct {
	ch       chan Event
	Replay   []Record
	State    State
	Reset    bool
	overflow bool
}

// Events returns the live event channel; it closes on unsubscribe,
// coordinator close, or overflow.
func (s *Subscription) Events() <-chan Event { return s.ch }

// Overflowed reports whether the subscription ended because the consumer
// fell behind; the client must re-snapshot.
func (s *Subscription) Overflowed() bool { return s.overflow }

// Subscribe registers a live consumer resuming after the given record seq.
// A cursor beyond the head or a stale epoch returns a subscription with
// Reset set and no live channel registration.
func (c *Coordinator) Subscribe(after int64, hasAfter bool, epoch string) (*Subscription, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sub := &Subscription{ch: make(chan Event, subscriberBuffer), State: c.stateLocked()}
	head := sub.State.HeadSeq
	if (epoch != "" && epoch != c.conv.StreamEpoch) || (hasAfter && after > head) || after < 0 {
		sub.Reset = true
		close(sub.ch)
		return sub, nil
	}
	if hasAfter {
		replay, err := c.store.after(after)
		if err != nil {
			return nil, err
		}
		sub.Replay = replay
	}
	if c.closed {
		close(sub.ch)
		return sub, nil
	}
	c.subs[sub] = struct{}{}
	return sub, nil
}

// Unsubscribe removes a consumer.
func (c *Coordinator) Unsubscribe(sub *Subscription) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.subs[sub]; ok {
		delete(c.subs, sub)
		close(sub.ch)
	}
}

func (c *Coordinator) publishStateLocked() {
	st := c.stateLocked()
	c.publishLocked(Event{Kind: EventState, Generation: st.Generation, State: &st})
}

func (c *Coordinator) publishLocked(ev Event) {
	for sub := range c.subs {
		select {
		case sub.ch <- ev:
		default:
			if ev.Kind == EventDelta {
				continue
			}
			sub.overflow = true
			delete(c.subs, sub)
			close(sub.ch)
		}
	}
}
