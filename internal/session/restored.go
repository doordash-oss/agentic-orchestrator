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
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm/askuser"
)

// errRestoredStdinClosed is the write error a session recovered from a PID
// file reports for every stdin write: the provider process survived the
// manager restart, but this manager owns no pipe to it.
var errRestoredStdinClosed = errors.New("session stdin is closed")

// restoredProtocol is the protocol assigned to sessions restored from PID
// files after an abrupt server exit. The provider process keeps running
// without this manager's pipes, so every stdin write fails with the same
// error the pre-restart session reported, Interrupt reports ErrNotSupported
// so Session.Interrupt falls back to the recovered process-group signal,
// and no handshake was observed by this manager, so SessionID and
// TranscriptPath report empty.
type restoredProtocol struct{}

func (restoredProtocol) SetStdin(io.Writer) {}

func (restoredProtocol) Handshake(context.Context) error {
	return errRestoredStdinClosed
}

func (restoredProtocol) ParseLine(line []byte) ([]llm.SDKMessage, error) {
	var msg llm.SDKMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		msg = llm.SDKMessage{
			Type: "assistant",
			Assistant: &llm.AssistantMessage{
				Message: llm.ConversationMsg{
					Role:    "assistant",
					Content: []llm.ContentBlock{{Type: "text", Text: string(line)}},
				},
			},
		}
	}
	return []llm.SDKMessage{msg}, nil
}

func (restoredProtocol) SendUserMessage(string) error {
	return errRestoredStdinClosed
}

func (restoredProtocol) RespondToControl(string, bool, json.RawMessage, string) error {
	return errRestoredStdinClosed
}

func (restoredProtocol) RespondToHook(string) error {
	return errRestoredStdinClosed
}

func (restoredProtocol) RespondToAskUser(string, askuser.Resolved) error {
	return errRestoredStdinClosed
}

func (restoredProtocol) Interrupt() error {
	return llm.ErrNotSupported
}

func (restoredProtocol) SessionID() string      { return "" }
func (restoredProtocol) TranscriptPath() string { return "" }
func (restoredProtocol) Close() error           { return nil }
