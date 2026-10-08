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
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"

	"github.com/google/uuid"
)

// ResetOutcome names the outcome of a new-conversation request.
type ResetOutcome string

const (
	// ResetDone retired the current conversation and opened an empty one.
	ResetDone ResetOutcome = "reset"
	// ResetNoop found nothing to retire: no process, no launch and an empty
	// transcript.
	ResetNoop ResetOutcome = "noop"
)

// ResetResult reports a new-conversation request. PreviousConversationID is
// the conversation current when the request arrived; for a noop it equals
// State.ConversationID.
type ResetResult struct {
	Result                 ResetOutcome
	PreviousConversationID string
	State                  State
}

// resetInterruptedText is the marker a reset leaves on a turn it cut.
const resetInterruptedText = "Interrupted when a new conversation was started"

// Reset retires the current conversation and starts an empty one. An
// in-flight launch is cancelled and a live process is stopped the way End
// stops it, applying a queued settings change at exit; a cut turn is marked
// interrupted by the user in the old transcript and its open requests are
// resolved. The new conversation has a fresh id and stream epoch, generation
// 0 and no native session id. Settings and the old conversation directory
// are untouched. Every live subscription ends with ConversationReset set so
// the stream re-snapshots under the new conversation.
func (c *Coordinator) Reset() (ResetResult, error) {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.Lock()
	previousID := c.conv.ConversationID
	if c.session == nil && c.launch == nil && c.store.head() == 0 {
		st := c.stateLocked()
		c.mu.Unlock()
		return ResetResult{Result: ResetNoop, PreviousConversationID: previousID, State: st}, nil
	}
	switch {
	case c.launch != nil:
		c.cancelLaunchLocked(OutcomeInterrupted, InterruptedByUser)
		c.commitPendingAtStopLocked()
	case c.session != nil:
		sess, sessionID := c.session, c.sessionID
		c.ending = true
		// The turn-in-flight record survives the exit so the cut turns can
		// be marked from it below, exactly as a boot after End would.
		c.keepTurns = true
		c.mu.Unlock()
		_ = sess.Stop()
		c.mu.Lock()
		if c.sessionID == sessionID {
			c.applyExitLocked(true)
		}
	}
	if err := c.markResetCutLocked(); err != nil {
		log.Printf("supervisor: mark turns cut by reset: %v", err)
	}
	if err := saveTurns(c.dir, c.conv.Generation, nil); err != nil {
		c.mu.Unlock()
		return ResetResult{}, fmt.Errorf("clear supervisor turn-in-flight record: %w", err)
	}

	conv := persistedConversation{ConversationID: uuid.NewString(), StreamEpoch: randomID()}
	store, err := openTranscriptStore(filepath.Join(c.dir, conversationsDir, conv.ConversationID), conv.ConversationID, c.opts.NewID, c.opts.Now)
	if err != nil {
		c.publishStateLocked()
		c.mu.Unlock()
		return ResetResult{}, fmt.Errorf("open new supervisor conversation: %w", err)
	}
	// conversation.json is the single current-conversation pointer: the
	// atomic write is the commit point of the reset.
	if err := saveConversation(c.dir, conv); err != nil {
		_ = store.close()
		c.publishStateLocked()
		c.mu.Unlock()
		return ResetResult{}, fmt.Errorf("allocate supervisor conversation: %w", err)
	}
	old := c.store
	store.setGeneration(conv.Generation)
	c.conv = conv
	c.store = store
	c.resetProcessLocked()
	c.applyingChange = false
	c.failure = nil
	c.persistFailure = nil
	c.changeFailure = nil
	c.lifecycle = LifecycleStopped
	c.setOutcomeLocked(OutcomeNone, InterruptedByNone)
	if c.relaunchPrevious != nil {
		// A revert after a failed first launch restores settings only; the
		// old conversation's native session belongs to the old conversation.
		c.relaunchPrevious.NativeSessionID = ""
	}
	// Live subscribers are bound to the old conversation's id and epoch:
	// end them before publishing so the stream handler re-subscribes and
	// emits stream.reset under the new conversation.
	for sub := range c.subs {
		sub.conversationReset = true
		delete(c.subs, sub)
		close(sub.ch)
	}
	c.publishStateLocked()
	st := c.stateLocked()
	c.mu.Unlock()
	if err := old.close(); err != nil {
		log.Printf("supervisor: close retired transcript: %v", err)
	}
	return ResetResult{Result: ResetDone, PreviousConversationID: previousID, State: st}, nil
}

// markResetCutLocked marks every turn the turn-in-flight record still lists
// for the current generation as interrupted by the user, after resolving
// the generation's unanswered requests: the process is gone, so none of
// them can still be answered.
func (c *Coordinator) markResetCutLocked() error {
	gen := c.conv.Generation
	if gen == 0 {
		return nil
	}
	inflight, err := loadTurns(c.dir)
	if err != nil {
		return err
	}
	records, err := c.store.after(0)
	if err != nil {
		return err
	}
	for _, req := range openRequests(records, gen) {
		c.appendDisplayLocked(gen, req.rec.TurnID, req.rec.Kind, RequestData{
			RequestID:      req.data.RequestID,
			ToolName:       req.data.ToolName,
			Stage:          StageResolved,
			Outcome:        RequestInterrupted,
			Origin:         req.data.Origin,
			ChildSessionID: req.data.ChildSessionID,
		})
	}
	if inflight.Generation != gen {
		return nil
	}
	for _, turn := range inflight.TurnIDs {
		c.appendMarkerLocked(gen, turn, MarkerData{Marker: MarkerInterrupted, Text: resetInterruptedText})
	}
	return nil
}

// appendDisplayLocked commits a display-only record and publishes it.
func (c *Coordinator) appendDisplayLocked(gen int64, turnID string, kind RecordKind, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("supervisor: encode %s record: %v", kind, err)
		return
	}
	rec, _, err := c.store.appendRecord(Record{Generation: gen, TurnID: turnID, Kind: kind, Visibility: VisibilityDisplayOnly, Data: data})
	if err != nil {
		log.Printf("supervisor: append %s record: %v", kind, err)
		return
	}
	c.publishLocked(Event{Kind: EventRecord, Generation: gen, Record: &rec})
}

// commitPendingAtStopLocked applies a queued settings change once no process
// remains, the way a process exit applies it.
func (c *Coordinator) commitPendingAtStopLocked() {
	if c.pendingChange == nil {
		return
	}
	change := c.pendingChange
	previous := c.settings
	previousID := c.conv.NativeSessionID
	err := c.commitChangeLocked(change, previous)
	if err == nil {
		if err = savePendingChange(c.dir, nil); err == nil {
			c.pendingChange = nil
			if err := c.rememberChangeLocked(change.RequestID); err != nil {
				log.Printf("supervisor: remember applied change: %v", err)
			}
			c.relaunchPrevious = &relaunchSettings{Settings: previous, NativeSessionID: previousID, Kind: change.Kind}
		}
	}
	if err != nil {
		log.Printf("supervisor: apply change at reset: %v", err)
	}
	c.publishStateLocked()
}
