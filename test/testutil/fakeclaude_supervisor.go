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

package testutil

// Markers a supervisor test puts in a user message to script the fake
// interactive Claude's next turn. A message without a marker gets a
// streamed reply.
const (
	// FakeSupervisorPermBash requests a blocking Bash permission and
	// replies once the control response arrives.
	FakeSupervisorPermBash = "SUPERVISOR_PERM_BASH"
	// FakeSupervisorPermEdit requests a blocking Edit permission.
	FakeSupervisorPermEdit = "SUPERVISOR_PERM_EDIT"
	// FakeSupervisorPermAgent requests a blocking sub-agent (Task)
	// permission.
	FakeSupervisorPermAgent = "SUPERVISOR_PERM_AGENT"
	// FakeSupervisorPermRead requests a read-only tool the supervisor
	// handler auto-allows; the request must never surface.
	FakeSupervisorPermRead = "SUPERVISOR_PERM_READ"
	// FakeSupervisorAsk asks an AskUserQuestion question.
	FakeSupervisorAsk = "SUPERVISOR_ASK"
	// FakeSupervisorHold holds a long-running tool until an interrupt
	// arrives, then reports an interrupted result without exiting.
	FakeSupervisorHold = "SUPERVISOR_HOLD"
	// FakeSupervisorStubborn holds the turn and ignores interrupts.
	FakeSupervisorStubborn = "SUPERVISOR_STUBBORN"
	// FakeSupervisorPartial commits partial assistant text and a Bash tool
	// call, then holds the turn like FakeSupervisorHold.
	FakeSupervisorPartial = "SUPERVISOR_PARTIAL"
)

// FakeSupervisorPartialText is the assistant text a FakeSupervisorPartial
// turn commits before holding.
const FakeSupervisorPartialText = "Partial answer before the cut"

// FakeSupervisorArgvFile is the file, next to the script, that each fake
// interactive Claude launch overwrites with its argv, one argument per line.
const FakeSupervisorArgvFile = "argv"

// FakeSupervisorInvocationsFile is the file, next to the script, that each
// fake interactive Claude launch appends one line to.
const FakeSupervisorInvocationsFile = "invocations"

// FakeSupervisorUserInputsFile is the file, next to the script, that the
// fake interactive Claude appends each user stdin line to verbatim, so a
// test can inspect exactly what reached the harness.
const FakeSupervisorUserInputsFile = "user_inputs"

// FakeSupervisorSystemPromptFile is the file, next to the script, that each
// fake interactive Claude launch overwrites with the value it received on
// the --append-system-prompt launch flag (empty when none was passed).
const FakeSupervisorSystemPromptFile = "system_prompt"

// fakeSupervisorLaunchPrologue records the launch, its argv and its system
// prompt, and notes the session id passed on --resume.
const fakeSupervisorLaunchPrologue = `printf 'x\n' >> "$(dirname "$0")/` + FakeSupervisorInvocationsFile + `"
printf '%s\n' "$@" > "$(dirname "$0")/` + FakeSupervisorArgvFile + `"
sysprompt=""
resume=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "--append-system-prompt" ]; then sysprompt="$arg"; fi
  if [ "$prev" = "--resume" ]; then resume="$arg"; fi
  prev="$arg"
done
printf '%s' "$sysprompt" > "$(dirname "$0")/` + FakeSupervisorSystemPromptFile + `"
`

// fakeSupervisorResumeLines read the rebuilt session file a --resume launch
// names, under the Claude configuration directory and the physical cwd's
// encoded project directory, into the reply for the first prompt.
const fakeSupervisorResumeLines = `resumed=""
session_id="s"
if [ -n "$resume" ]; then
  session_id="$resume"
  cfg="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
  enc=$(pwd -P | tr '/.' '--')
  file="$cfg/projects/$enc/$resume.jsonl"
  if [ -f "$file" ]; then
    count=$(grep -c '"role":"user","content":"' "$file")
    first=$(grep -m1 '"role":"user","content":"' "$file" | sed 's/.*"role":"user","content":"\([^"]*\)".*/\1/')
    resumed="Resumed with $count prior messages: $first"
  else
    resumed="Resume file missing: $file"
  fi
fi
`

// FakeClaudeInteractiveScriptBody returns a long-lived stream-json harness:
// it answers the initialize handshake, then serves one turn per user
// message (stream deltas, one assistant message with a message id, and a
// result) and stays alive reading stdin between turns. Markers in the user
// text script permission requests, questions and interrupt handling. Every
// user line is recorded in FakeSupervisorUserInputsFile.
func FakeClaudeInteractiveScriptBody() string {
	return FakeClaudeInteractiveScriptBodyWithPermissionMode("default")
}

// FakeClaudeInteractiveScriptBodyWithPermissionMode is the interactive
// harness reporting permissionMode in its init message, as a policy that
// restricts the requested mode would make the real CLI do. A --resume
// launch answers its first prompt with "Resumed with <n> prior messages:
// <first prior prompt>" read from the rebuilt session file.
func FakeClaudeInteractiveScriptBodyWithPermissionMode(permissionMode string) string {
	return fakeSupervisorLaunchPrologue + fakeSupervisorResumeLines + `turn=0
mode=""
permission_mode="` + permissionMode + `"
reply() {
  if [ -n "$resumed" ]; then
    text="$resumed"
    resumed=""
    printf '%s\n' "{\"type\":\"stream_event\",\"event\":{\"type\":\"message_start\",\"message\":{\"id\":\"msg_$turn\"}}}"
    printf '%s\n' "{\"type\":\"assistant\",\"message\":{\"id\":\"msg_$turn\",\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"$text\"}]}}"
    printf '%s\n' '{"type":"result","subtype":"success"}'
    return
  fi
  printf '%s\n' "{\"type\":\"stream_event\",\"event\":{\"type\":\"message_start\",\"message\":{\"id\":\"msg_$turn\"}}}"
  printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello "}}}'
  printf '%s\n' "{\"type\":\"stream_event\",\"event\":{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"from turn $turn\"}}}"
  printf '%s\n' "{\"type\":\"assistant\",\"message\":{\"id\":\"msg_$turn\",\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"Hello from turn $turn\"}]}}"
  printf '%s\n' '{"type":"result","subtype":"success"}'
}
partial() {
  printf '%s\n' "{\"type\":\"assistant\",\"message\":{\"id\":\"msg_$turn\",\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"` + FakeSupervisorPartialText + `\"}]}}"
  printf '%s\n' "{\"type\":\"assistant\",\"message\":{\"id\":\"msg_tool_$turn\",\"role\":\"assistant\",\"content\":[{\"type\":\"tool_use\",\"id\":\"toolu_partial_$turn\",\"name\":\"Bash\",\"input\":{\"command\":\"sleep 600\"}}]}}"
}
request() {
  printf '%s\n' "{\"type\":\"control_request\",\"request_id\":\"req_$turn\",\"request\":{\"subtype\":\"can_use_tool\",\"tool_name\":\"$1\",\"input\":$2}}"
}
while IFS= read -r line; do
  case "$line" in
    *'"subtype":"initialize"'*)
      printf '%s\n' '{"type":"control_response","response":{"subtype":"success","request_id":"init"}}'
      printf '%s\n' "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"$session_id\",\"model\":\"haiku[200K]\",\"permissionMode\":\"$permission_mode\"}"
      ;;
    *'"subtype":"interrupt"'*)
      if [ "$mode" = hold ]; then
        mode=""
        printf '%s\n' '{"type":"result","subtype":"error_during_execution","is_error":true}'
      fi
      ;;
    *'"control_response"'*)
      if [ "$mode" = wait ]; then
        mode=""
        reply
      fi
      ;;
    *'"type":"user"'*)
      turn=$((turn+1))
      printf '%s\n' "$line" >> "$(dirname "$0")/` + FakeSupervisorUserInputsFile + `"
      case "$line" in
        *` + FakeSupervisorHold + `*) mode=hold ;;
        *` + FakeSupervisorPartial + `*) mode=hold; partial ;;
        *` + FakeSupervisorStubborn + `*) mode=stubborn ;;
        *` + FakeSupervisorPermBash + `*) mode=wait; request Bash '{"command":"make test"}' ;;
        *` + FakeSupervisorPermEdit + `*) mode=wait; request Edit '{"file_path":"main.go","old_string":"a","new_string":"b"}' ;;
        *` + FakeSupervisorPermAgent + `*) mode=wait; request Task '{"description":"explore","prompt":"look around"}' ;;
        *` + FakeSupervisorPermRead + `*) mode=wait; request Read '{"file_path":"README.md"}' ;;
        *` + FakeSupervisorAsk + `*) mode=wait; request AskUserQuestion '{"questions":[{"question":"Which branch?","header":"Branch","options":[{"label":"main","description":"default"},{"label":"dev","description":"work"}],"multiSelect":false}]}' ;;
        *) reply ;;
      esac
      ;;
  esac
done
`
}

// FakeClaudeLaunchFailureScriptBody returns a harness that exits before
// answering the handshake.
func FakeClaudeLaunchFailureScriptBody() string {
	return `printf 'x\n' >> "$(dirname "$0")/` + FakeSupervisorInvocationsFile + `"
exit 3
`
}
