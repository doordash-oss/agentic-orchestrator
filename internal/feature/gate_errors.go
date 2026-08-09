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

package feature

import "errors"

// ErrInvalidTransition wraps every rejected lifecycle status transition so
// callers can distinguish "the feature's state does not allow this action" from
// a malformed request.
var ErrInvalidTransition = errors.New("invalid transition")

// ErrNeedUserInputGateOpen is returned when an execution verb (start/resume)
// arrives while the feature is waiting on a need-user-input request. Answering
// the request is the only way forward; anything else would silently bypass it.
var ErrNeedUserInputGateOpen = errors.New("feature is waiting on a user input request")

// ErrPhaseFinalizing is returned when an execution verb arrives while the
// feature is inside the synchronous end-of-phase git boundary.
var ErrPhaseFinalizing = errors.New("feature is finalizing the current phase")
