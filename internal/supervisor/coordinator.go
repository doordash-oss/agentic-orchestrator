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

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
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
)

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

	mu             sync.Mutex
	settings       Settings
	conv           persistedConversation
	store          *transcriptStore
	lifecycle      Lifecycle
	step           StartingStep
	outcome        TurnOutcome
	session        ports.SessionView
	sessionID      string
	effectiveModel string
	launch         *launchAttempt
	// turns holds the delivered turns still awaiting a result, oldest first.
	turns        []string
	turnCount    int
	pending      []*llm.ControlRequestMessage
	streamID     string
	streamChunks int
	streamCount  int
	// ending marks the current process as being stopped on purpose; its
	// exit is not a failure.
	ending    bool
	interrupt *interruptAttempt
	closed    bool
	subs      map[*Subscription]struct{}
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
	return &Coordinator{
		opts:      opts,
		dir:       dir,
		settings:  settings,
		conv:      conv,
		store:     store,
		lifecycle: LifecycleStopped,
		outcome:   OutcomeNone,
		subs:      map[*Subscription]struct{}{},
	}, nil
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
		Lifecycle:       c.lifecycle,
		LastTurnOutcome: c.outcome,
		Settings:        c.settings,
		EffectiveModel:  c.effectiveModel,
		PendingRequests: append([]*llm.ControlRequestMessage(nil), c.pending...),
		Session:         c.session,
		HeadSeq:         c.store.head(),
		StreamEpoch:     c.conv.StreamEpoch,
		WorkDir:         c.opts.WorkDir,
	}
	if c.lifecycle == LifecycleStarting {
		st.StartingStep = c.step
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

// Send commits one user message and delivers it to the harness, launching
// the process when none exists. Sends arriving while a launch is in flight
// join it and are delivered in arrival order after the handshake.
func (c *Coordinator) Send(ctx context.Context, text, clientMessageID string) (SendResult, error) {
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
		j := newJoiner(text, clientMessageID, false)
		c.launch.joiners = append(c.launch.joiners, j)
		c.mu.Unlock()
		c.opMu.Unlock()
		return j.wait(ctx)
	case c.lifecycle == LifecycleIdle && c.session != nil:
		defer c.opMu.Unlock()
		rec, _, err := c.appendUserLocked(text, clientMessageID)
		if err != nil {
			c.mu.Unlock()
			return SendResult{}, err
		}
		c.lifecycle = LifecycleRunning
		c.publishStateLocked()
		sess := c.session
		c.mu.Unlock()
		if err := sess.SendUserMessage(text); err != nil {
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
	j := newJoiner(text, clientMessageID, true)
	attempt.joiners = append(attempt.joiners, j)
	c.mu.Unlock()
	c.opMu.Unlock()
	// The launch outlives the initiating request so joiners are served even
	// if the initiator's client goes away.
	go c.runLaunch(attempt)
	return j.wait(ctx)
}

func newJoiner(text, cmid string, initiator bool) *joiner {
	return &joiner{text: text, cmid: cmid, initiator: initiator, done: make(chan joinResult, 1)}
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
	c.sessionID = SessionID(conv.ConversationID, conv.Generation)
	c.lifecycle = LifecycleStarting
	c.step = StepLaunching
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
	c.interrupt = nil
	c.step = ""
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
	obs := &generationObserver{c: c, generation: attempt.generation, handshake: make(chan struct{})}
	sess, err := c.opts.Launcher.Launch(context.Background(), LaunchRequest{
		SessionID:      attempt.sessionID,
		ConversationID: conversationID,
		Generation:     attempt.generation,
		Settings:       settings,
		WorkDir:        c.opts.WorkDir,
		PIDDir:         genDir,
		LogPath:        filepath.Join(genDir, "output.txt"),
		StderrPath:     filepath.Join(genDir, "stderr.log"),
		Observer:       obs,
		OnSpawned:      func() { c.markHandshake(attempt) },
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
	var deliveries []string
	for i, j := range attempt.joiners {
		rec, existing, err := c.appendUserLocked(j.text, j.cmid)
		if err != nil {
			results[i] = joinResult{err: err}
			continue
		}
		if !existing {
			deliveries = append(deliveries, j.text)
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
	for _, text := range deliveries {
		if err := sess.SendUserMessage(text); err != nil {
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
		c.outcome = OutcomeInterrupted
	case !clean || c.lifecycle.inTurn():
		c.outcome = OutcomeFailed
	}
	if c.interrupt != nil {
		close(c.interrupt.done)
	}
	c.resetProcessLocked()
	c.lifecycle = LifecycleStopped
	c.publishStateLocked()
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
		c.cancelLaunchLocked(OutcomeInterrupted)
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
	c.outcome = OutcomeInterrupted
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
func (c *Coordinator) cancelLaunchLocked(outcome TurnOutcome) {
	attempt := c.launch
	attempt.cancelled = true
	close(attempt.cancel)
	c.launch = nil
	c.resetProcessLocked()
	c.lifecycle = LifecycleStopped
	c.outcome = outcome
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
		c.cancelLaunchLocked(c.outcome)
		st := c.stateLocked()
		c.mu.Unlock()
		return ActionEnded, st
	case c.session != nil:
		sess, sessionID := c.session, c.sessionID
		c.ending = true
		if c.lifecycle.inTurn() {
			c.outcome = OutcomeInterrupted
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
	if msg.Init != nil && current && msg.Init.Model != "" {
		c.effectiveModel = msg.Init.Model
		c.publishStateLocked()
	}
	if msg.Type == "stream_event" {
		if current {
			c.observeStreamLocked(gen, msg)
		}
		return
	}
	if msg.Origin.Kind == llm.EventOriginTask {
		// Sub-agent output belongs to the sub-agent, not the conversation.
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
	c.appendProviderLocked(gen, kind, RequestData{
		RequestID: req.RequestID,
		ToolName:  req.Request.ToolName,
		Stage:     StageRequested,
		Outcome:   RequestPending,
		Input:     req.Request.Input,
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
	for i, p := range c.pending {
		if p.RequestID == answer.RequestID {
			toolName = p.Request.ToolName
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
		RequestID: answer.RequestID,
		ToolName:  toolName,
		Stage:     StageResolved,
		Outcome:   outcome,
		Reason:    answer.Reason,
		Answers:   answer.Answers,
	}, "")
	if current && (c.lifecycle == LifecycleWaitingPermission || c.lifecycle == LifecycleWaitingQuestion) {
		c.lifecycle = c.waitingLifecycleLocked()
	}
	c.publishStateLocked()
}

func (c *Coordinator) observeResultLocked(result *llm.ResultMessage) {
	if len(c.turns) > 0 {
		c.turns = c.turns[1:]
	}
	// Turn end clears any request the harness left unanswered.
	c.pending = nil
	c.streamID, c.streamChunks = "", 0
	switch {
	case c.interrupt != nil:
		c.outcome = OutcomeInterrupted
		close(c.interrupt.done)
		c.interrupt = nil
	case result.IsError || result.Subtype == "error":
		c.outcome = OutcomeFailed
	default:
		c.outcome = OutcomeCompleted
	}
	if len(c.turns) == 0 {
		c.lifecycle = LifecycleIdle
	} else {
		c.lifecycle = LifecycleRunning
	}
	c.publishStateLocked()
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
