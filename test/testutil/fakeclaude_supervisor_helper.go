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

// Markers a helper journey puts in a supervisor user message to make the
// helper-driving fake Claude operate Agentico through the real
// `agentico api` helper.
const (
	// FakeHelperOperate creates a feature, posts its config and starts it.
	FakeHelperOperate = "SUPERVISOR_HELPER_OPERATE"
	// FakeHelperAnswer reads the pending prompts and answers the first
	// ask-user question.
	FakeHelperAnswer = "SUPERVISOR_HELPER_ANSWER"
)

// Files, next to the script, the helper-driving fake reads or writes.
const (
	// FakeHelperFeatureConfigFile holds the JSON body the operate turn
	// posts to the created feature's config route; the journey writes it.
	FakeHelperFeatureConfigFile = "feature_config.json"
	// FakeHelperCommandsFile records every helper command the supervisor
	// ran, one per line.
	FakeHelperCommandsFile = "helper_commands"
	// FakeHelperPhaseInvocationsFile gets one line per phase-worker launch.
	FakeHelperPhaseInvocationsFile = "phase_invocations"
	// FakeHelperPhaseAnswersFile records every control response a phase
	// worker received for its question.
	FakeHelperPhaseAnswersFile = "phase_answers"
	// FakeHelperEmptyRuntimeDir is the empty directory the misconfigured
	// body points the helper's runtime-directory variable at.
	FakeHelperEmptyRuntimeDir = "empty-runtime"
)

// FakeHelperFeatureName is the name the operate turn creates its feature
// with.
const FakeHelperFeatureName = "helper-journey"

// FakeHelperPhaseQuestion and FakeHelperPhaseOption are the question a
// phase worker asks and the first option the supervisor answers with.
const (
	FakeHelperPhaseQuestion = "Which approach should the feature take?"
	FakeHelperPhaseOption   = "simple"
)

// FakeClaudeSupervisorHelperScriptBody returns a scripted `claude` CLI for a
// real built server. It answers auth, version and model-catalog probes, then
// splits by launch: the supervisor child (the only launch carrying the
// runtime-directory variable and the Agentico supervisor system prompt) is a
// stream-json harness whose marker turns run the real helper from
// $AGENTICO_BIN; every other launch is a phase worker that asks one
// AskUserQuestion question and parks.
//
// Each helper call mirrors a real Bash tool call: an assistant tool_use
// block carrying the command, a can_use_tool permission request the
// supervisor handler must allow, the command run through sh, and a user
// tool_result block carrying its combined output.
func FakeClaudeSupervisorHelperScriptBody() string {
	return fakeHelperScript("")
}

// FakeClaudeSupervisorHelperMisconfiguredScriptBody is the helper-driving
// body with the helper's runtime-directory variable pointed at an empty
// directory, so every helper call fails discovery. Its operate turn only
// attempts the feature create.
func FakeClaudeSupervisorHelperMisconfiguredScriptBody() string {
	return fakeHelperScript(`override_runtime="$dir/` + FakeHelperEmptyRuntimeDir + `"
mkdir -p "$override_runtime"
`)
}

func fakeHelperScript(prelude string) string {
	return `dir=$(cd "$(dirname "$0")" && pwd)
case "$1" in
auth)
  printf '{"loggedIn":true,"authMethod":"api_key","apiProvider":"fake"}\n'
  exit 0
  ;;
--version)
  printf '2.1.81 (Claude Code)\n'
  exit 0
  ;;
-p)
  IFS= read -r _req
  printf '%s\n' '{"type":"control_response","response":{"request_id":"agentico-model-catalog","subtype":"success","response":{"models":[{"value":"claude-sonnet-4-20250514","resolvedModel":"claude-sonnet-4-20250514","displayName":"Sonnet 4"}]}}}'
  while read -r _line; do :; done
  exit 0
  ;;
esac
sysprompt=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "--append-system-prompt" ]; then sysprompt="$arg"; fi
  prev="$arg"
done
supervisor=0
if [ -n "$AGENTICO_RUNTIME_DIR" ]; then
  case "$sysprompt" in *"Agentico supervisor"*) supervisor=1 ;; esac
fi

if [ "$supervisor" != 1 ]; then
  # Phase worker: ask one question after the first prompt, record the
  # answer, then park until stdin closes.
  printf 'x\n' >> "$dir/` + FakeHelperPhaseInvocationsFile + `"
  asked=0
  while IFS= read -r line; do
    case "$line" in
      *'"subtype":"initialize"'*)
        printf '%s\n' '{"type":"control_response","response":{"subtype":"success","request_id":"agentic-init-1"}}'
        printf '%s\n' '{"type":"system","subtype":"init","session_id":"phase","model":"claude-sonnet-4-20250514"}'
        ;;
      *'"control_response"'*)
        printf '%s\n' "$line" >> "$dir/` + FakeHelperPhaseAnswersFile + `"
        printf '%s\n' '{"type":"assistant","message":{"id":"phase_msg_2","role":"assistant","content":[{"type":"text","text":"Thanks, continuing."}]}}'
        ;;
      *'"type":"user"'*)
        if [ "$asked" = 0 ]; then
          asked=1
          printf '%s\n' '{"type":"assistant","message":{"id":"phase_msg_1","role":"assistant","content":[{"type":"text","text":"I need one decision first."}]}}'
          printf '%s\n' '{"type":"control_request","request_id":"phase_ask_1","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"` + FakeHelperPhaseQuestion + `","header":"Approach","options":[{"label":"` + FakeHelperPhaseOption + `","description":"smallest change"},{"label":"thorough","description":"broader change"}],"multiSelect":false}]}}}'
        fi
        ;;
    esac
  done
  exit 0
fi

# Supervisor harness.
printf 'x\n' >> "$dir/` + FakeSupervisorInvocationsFile + `"
printf '%s' "$sysprompt" > "$dir/` + FakeSupervisorSystemPromptFile + `"
override_runtime=""
` + prelude + `turn=0
n=0

# esc prints its argument as the body of a JSON string: control characters
# other than newline dropped, backslashes and quotes escaped, newlines
# joined as \n.
esc() {
  printf '%s' "$1" | tr -d '\000-\011\013-\037' | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' | awk 'NR>1{printf "%s","\\n"} {printf "%s",$0}'
}

# final streams and commits one assistant text reply, then ends the turn.
final() {
  text=$(esc "$1")
  printf '%s\n' "{\"type\":\"stream_event\",\"event\":{\"type\":\"message_start\",\"message\":{\"id\":\"msg_final_$turn\"}}}"
  printf '%s\n' "{\"type\":\"stream_event\",\"event\":{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"$text\"}}}"
  printf '%s\n' "{\"type\":\"assistant\",\"message\":{\"id\":\"msg_final_$turn\",\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"$text\"}]}}"
  printf '%s\n' '{"type":"result","subtype":"success"}'
}

# helper runs one helper command the way the Bash tool does and leaves its
# combined output in $dir/helper_out.
helper() {
  n=$((n+1))
  tool_id="toolu_helper_$n"
  cmd_json=$(esc "$1")
  printf '%s\n' "{\"type\":\"assistant\",\"message\":{\"id\":\"msg_tool_$n\",\"role\":\"assistant\",\"content\":[{\"type\":\"tool_use\",\"id\":\"$tool_id\",\"name\":\"Bash\",\"input\":{\"command\":\"$cmd_json\"}}]}}"
  printf '%s\n' "{\"type\":\"control_request\",\"request_id\":\"perm_helper_$n\",\"request\":{\"subtype\":\"can_use_tool\",\"tool_name\":\"Bash\",\"input\":{\"command\":\"$cmd_json\"}}}"
  decision=""
  while IFS= read -r resp; do
    case "$resp" in
      *'"control_response"'*) case "$resp" in *"\"perm_helper_$n\""*) decision="$resp"; break ;; esac ;;
    esac
  done
  printf '%s\n' "$1" >> "$dir/` + FakeHelperCommandsFile + `"
  case "$decision" in
    *'"behavior":"allow"'*)
      if [ -n "$override_runtime" ]; then
        AGENTICO_RUNTIME_DIR="$override_runtime" sh -c "$1" > "$dir/helper_out" 2>&1
        rc=$?
      else
        sh -c "$1" > "$dir/helper_out" 2>&1
        rc=$?
      fi
      ;;
    *)
      printf 'permission not granted\n' > "$dir/helper_out"
      rc=1
      ;;
  esac
  is_error=false
  if [ "$rc" != 0 ]; then is_error=true; fi
  out_json=$(esc "$(cat "$dir/helper_out")")
  printf '%s\n' "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"tool_result\",\"tool_use_id\":\"$tool_id\",\"content\":\"$out_json\",\"is_error\":$is_error}]}}"
}

operate() {
  helper "\"\$AGENTICO_BIN\" api POST /api/v1/features '{\"name\":\"` + FakeHelperFeatureName + `\"}'"
  if [ -n "$override_runtime" ]; then
    final "The helper could not reach Agentico."
    return
  fi
  fid=$(tr -d '\n' < "$dir/helper_out" | grep -o '"feature_id":"[^"]*"' | head -n 1 | sed 's/^"feature_id":"//; s/"$//')
  cfg=$(tr -d '\n' < "$dir/` + FakeHelperFeatureConfigFile + `")
  helper "\"\$AGENTICO_BIN\" api POST /api/v1/features/$fid/config '$cfg'"
  helper "\"\$AGENTICO_BIN\" api POST /api/v1/features/$fid/actions/start '{}'"
  final "Created, configured and started the feature."
}

answer() {
  helper "\"\$AGENTICO_BIN\" api GET /api/v1/prompts"
  asks=$(tr -d '\n' < "$dir/helper_out" | sed -e 's/.*"ask_user_questions":\[//' -e 's/],"help_queue".*//')
  rid=$(printf '%s' "$asks" | grep -o '"request_id":"[^"]*"' | head -n 1 | sed 's/^"request_id":"//; s/"$//')
  sid=$(printf '%s' "$asks" | grep -o '"session_id":"[^"]*"' | head -n 1 | sed 's/^"session_id":"//; s/"$//')
  question=$(printf '%s' "$asks" | grep -o '"question":"[^"]*"' | head -n 1 | sed 's/^"question":"//; s/"$//')
  label=$(printf '%s' "$asks" | grep -o '"label":"[^"]*"' | head -n 1 | sed 's/^"label":"//; s/"$//')
  helper "\"\$AGENTICO_BIN\" api POST /api/v1/prompts/ask-user/answer '{\"request_id\":\"$rid\",\"session_id\":\"$sid\",\"answers\":{\"$question\":\"$label\"}}'"
  final "Answered the pending question."
}

while IFS= read -r line; do
  case "$line" in
    *'"subtype":"initialize"'*)
      printf '%s\n' '{"type":"control_response","response":{"subtype":"success","request_id":"agentic-init-1"}}'
      printf '%s\n' '{"type":"system","subtype":"init","session_id":"s","model":"claude-sonnet-4-20250514"}'
      ;;
    *'"type":"user"'*)
      turn=$((turn+1))
      printf '%s\n' "$line" >> "$dir/` + FakeSupervisorUserInputsFile + `"
      case "$line" in
        *` + FakeHelperOperate + `*) operate ;;
        *` + FakeHelperAnswer + `*) answer ;;
        *) final "Ready." ;;
      esac
      ;;
  esac
done
`
}
