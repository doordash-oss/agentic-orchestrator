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
	"fmt"
)

// RebuildInput is one native-session rebuild request: the durable records up
// to the transcript head, rendered into the harness's own session store
// under the pre-assigned native session id.
type RebuildInput struct {
	ConversationID  string
	NativeSessionID string
	WorkDir         string
	Records         []Record
}

// RebuildResult reports what a rebuild wrote. Resume is false when the
// selected history was empty and nothing was written.
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
