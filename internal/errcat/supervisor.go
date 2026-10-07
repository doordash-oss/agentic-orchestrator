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

package errcat

// Supervisor conversation codes. The supervisor REST namespace refuses a
// request with one of these before any provider work starts, except
// SupervisorLaunchFailed, which reports a launch or handshake that failed
// after the request was admitted.
const (
	// SupervisorSettingsLocked refuses a settings change while a supervisor
	// process exists.
	SupervisorSettingsLocked Code = "supervisor_settings_locked"
	// SupervisorSettingsInvalid refuses a harness, model or effort the
	// model catalog does not offer for the supervisor.
	SupervisorSettingsInvalid Code = "supervisor_settings_invalid"
	// SettingsRequired refuses a send before a harness and model are chosen.
	SettingsRequired Code = "settings_required"
	// TurnActive refuses a send while the supervisor is in a turn.
	TurnActive Code = "turn_active"
	// SupervisorLaunchFailed reports that the supervisor process failed to
	// launch or complete its handshake; no user message was committed.
	SupervisorLaunchFailed Code = "supervisor_launch_failed"
	ChangePending          Code = "change_pending"
	StaleGeneration        Code = "stale_generation"
	PendingChangeNotFound  Code = "pending_change_not_found"
)
