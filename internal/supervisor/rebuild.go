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
	"encoding/json"
	"fmt"
)

// RebuildInput is one native-session rebuild request: the durable records up
// to the transcript head, rendered into the harness's own session store
// under the pre-assigned native session id. A HistorySeeder gets no native
// id and writes its seed under ConversationDir instead.
type RebuildInput struct {
	ConversationID  string
	NativeSessionID string
	WorkDir         string
	// ConversationDir is the conversation's durable directory, where a
	// HistorySeeder writes its seed file.
	ConversationDir string
	// Model and Effort are the conversation's committed settings, for
	// formats that record them per turn. Empty Effort means the default.
	Model   string
	Effort  string
	Records []Record
}

// RebuildResult reports what a rebuild wrote. Resume is false when the
// selected history was empty and nothing was written. For a HistorySeeder,
// Path is the seed file and SessionID is empty.
type RebuildResult struct {
	Resume    bool
	SessionID string
	Path      string
}

// Converter rebuilds one harness's native session from the durable
// transcript so a relaunched process resumes with the conversation's
// history. Each harness's undocumented session format stays inside its own
// converter.
type Converter interface {
	Harness() string
	Rebuild(ctx context.Context, in RebuildInput) (RebuildResult, error)
}

// NativeCompactionCapture reads a harness-owned compacted baseline after a
// compaction boundary. The caller bounds ctx and records the returned payload
// without interpreting the native format.
type NativeCompactionCapture interface {
	CaptureCompaction(ctx context.Context, nativeSessionID string) (json.RawMessage, error)
}

// HarnessAssignedIDs is implemented by converters whose harness mints its
// own session id and cannot resume under a caller-chosen one. For those the
// coordinator pre-assigns nothing: it adopts the id the harness reports
// after a launch that did not resume, and rebuilds under that id later.
type HarnessAssignedIDs interface {
	HarnessAssignsSessionID() bool
}

// HistorySeeder is implemented by converters whose harness cannot resume a
// rebuilt native session and instead receives the history as a seed before
// the first prompt. For those the coordinator mints and adopts no native id
// (the conversation's native id stays empty), never resumes, and passes the
// rebuild result's Path to the launch as the seed file.
type HistorySeeder interface {
	SeedsHistory() bool
}

// ConversionError reports records a converter cannot represent. It is
// deterministic, unlike an I/O failure: the launch degrades to a fresh
// session instead of failing.
type ConversionError struct {
	Seq    int64
	Reason string
}

func (e *ConversionError) Error() string {
	if e.Seq > 0 {
		return fmt.Sprintf("supervisor history conversion: record %d: %s", e.Seq, e.Reason)
	}
	return "supervisor history conversion: " + e.Reason
}
