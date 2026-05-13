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

package eval

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"sort"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	codex_proto "github.com/doordash-oss/agentic-orchestrator/internal/llm/codex"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/internal/skilldef"
	"gopkg.in/yaml.v3"
)

// evalCase represents a single skill discovery test case.
type evalCase struct {
	Name          string   `yaml:"name"`
	Prompt        string   `yaml:"prompt"`
	ExpectRead    []string `yaml:"expect_read"`
	NotExpectRead []string `yaml:"not_expect_read"`
	MaxTurns      int      `yaml:"max_turns,omitempty"` // override default of 5
}

type evalCasesFile struct {
	Cases []evalCase `yaml:"cases"`
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// skipUnlessEval skips the test if the AGENTIC_EVAL env var is not set.
func skipUnlessEval(t *testing.T) {
	t.Helper()
	if os.Getenv("AGENTIC_EVAL") == "" {
		t.Skip("skipping eval test: set AGENTIC_EVAL=1 to run")
	}
}

// unsetClaudeCode clears the CLAUDECODE env var for this test so we can spawn
// nested Claude/Codex CLI sessions from inside a Claude Code instance.
func unsetClaudeCode(t *testing.T) {
	t.Helper()
	t.Setenv("CLAUDECODE", "")
}

// reconcileSkillsDir reconciles all embedded skills to a temp directory and
// returns the skills directory path.
func reconcileSkillsDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "skills")
	if err := skilldef.ReconcileSkills(dir); err != nil {
		t.Fatalf("reconciling skills: %v", err)
	}
	return dir
}

// loadCases reads the test cases YAML file.
func loadCases(t *testing.T) []evalCase {
	t.Helper()
	data, err := os.ReadFile("testdata/cases.yaml")
	if err != nil {
		t.Fatalf("reading cases.yaml: %v", err)
	}
	var cf evalCasesFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		t.Fatalf("parsing cases.yaml: %v", err)
	}
	if len(cf.Cases) == 0 {
		t.Fatal("no test cases found in cases.yaml")
	}
	return cf.Cases
}

// readToolInput represents the JSON input for a Read tool call.
type readToolInput struct {
	FilePath string `json:"file_path"`
}

// extractReadPaths finds all Read tool calls in the tool use blocks and
// returns the set of file_path values.
func extractReadPaths(blocks []llm.ContentBlock) map[string]bool {
	paths := make(map[string]bool)
	for _, block := range blocks {
		if !block.IsToolUse() || block.Name != "Read" {
			continue
		}
		var input readToolInput
		if err := json.Unmarshal(block.Input, &input); err != nil {
			continue
		}
		if input.FilePath != "" {
			paths[input.FilePath] = true
		}
	}
	return paths
}

// skillPathsToNames converts a set of full file paths to skill names by
// matching against the skillsDir pattern: <skillsDir>/<name>/SKILL.md.
func skillPathsToNames(paths map[string]bool, skillsDir string) map[string]bool {
	names := make(map[string]bool)
	for p := range paths {
		if !strings.HasSuffix(p, "/SKILL.md") {
			continue
		}
		// Extract the skill name from the path.
		dir := filepath.Dir(p)      // .../skills/<name>
		name := filepath.Base(dir)  // <name>
		parent := filepath.Dir(dir) // .../skills
		// Verify this is under our skillsDir.
		if filepath.Clean(parent) == filepath.Clean(skillsDir) {
			names[name] = true
		}
	}
	return names
}

// ---------------------------------------------------------------------------
// Claude eval
// ---------------------------------------------------------------------------

func TestSkillDiscovery_Claude(t *testing.T) {
	skipUnlessEval(t)
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not available")
	}
	unsetClaudeCode(t)

	skillsDir := reconcileSkillsDir(t)
	preamble := skilldef.BuildPreamble(skillsDir, allSkillNames(t))
	if preamble == "" {
		t.Fatal("BuildPreamble returned empty string")
	}

	cases := loadCases(t)

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			maxTurns := tc.MaxTurns
			if maxTurns == 0 {
				maxTurns = 5
			}

			// Build claude --print command with skill preamble in system prompt.
			command := []string{
				"claude",
				"--print",
				"--model", "sonnet",
				"--output-format", "stream-json",
				"--verbose",
				"--dangerously-skip-permissions",
				"--append-system-prompt", preamble,
				"--add-dir", skillsDir,
				"--max-turns", strconv.Itoa(maxTurns),
				"--", tc.Prompt,
			}

			workDir := t.TempDir()
			logPath := filepath.Join(workDir, "session.jsonl")

			// Create session. No protocol needed — the session's fallback
			// JSON unmarshal handles Claude's stream-json output in print mode.
			sess := session.NewSession("eval-claude-"+tc.Name, "eval", feature.PhaseResearch)

			// Set up log file for debugging.
			logFile, err := os.Create(logPath)
			if err != nil {
				t.Fatalf("creating log file: %v", err)
			}
			sess.SetLogFile(logFile)
			t.Cleanup(func() { logFile.Close() })

			// Drain the attach channel to prevent blocking.
			go func() {
				for range sess.AttachCh() {
				}
			}()

			if err := sess.Start(command, workDir, nil, func(msg llm.SDKMessage) {}); err != nil {
				t.Fatalf("starting claude session: %v", err)
			}
			// Close stdin so claude --print doesn't hang waiting for input.
			sess.CloseStdin()

			// Wait for completion with timeout.
			select {
			case <-sess.Done():
			case <-time.After(180 * time.Second):
				t.Fatal("claude session did not complete within 180s")
			}

			// Extract tool use blocks and find Read calls.
			blocks := sess.MessageLog().ToolUseBlocks()
			readPaths := extractReadPaths(blocks)
			readSkills := skillPathsToNames(readPaths, skillsDir)

			t.Logf("Read tool calls (%d total):", len(readPaths))
			for p := range readPaths {
				t.Logf("  %s", p)
			}
			t.Logf("Discovered skills: %v", mapKeys(readSkills))

			// Assert expected skills were read.
			for _, expected := range tc.ExpectRead {
				if !readSkills[expected] {
					t.Errorf("expected agent to read skill %q but it did not", expected)
				}
			}

			// Assert unexpected skills were not read.
			for _, unexpected := range tc.NotExpectRead {
				if readSkills[unexpected] {
					t.Errorf("agent read skill %q which was not expected", unexpected)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Codex eval
// ---------------------------------------------------------------------------

func TestSkillDiscovery_Codex(t *testing.T) {
	skipUnlessEval(t)
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI not available")
	}
	unsetClaudeCode(t)

	skillsDir := reconcileSkillsDir(t)
	preamble := skilldef.BuildPreamble(skillsDir, allSkillNames(t))
	if preamble == "" {
		t.Fatal("BuildPreamble returned empty string")
	}

	cases := loadCases(t)

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			workDir := t.TempDir()
			logPath := filepath.Join(workDir, "session.jsonl")

			// Use app-server mode (interactive JSON-RPC) — matches production.
			// System prompt goes via developer_instructions in turn/start.
			command := []string{"codex", "app-server"}

			proto := codex_proto.NewProtocol(llm.ProtocolOpts{
				Model:         "gpt-5.4-mini",
				WorkDir:       workDir,
				SystemPrompt:  preamble,
				InitialPrompt: tc.Prompt,
				DSP:           true, // skip approvals
				WritableRoots: []string{workDir, skillsDir},
			})

			eventCh := make(chan interface{}, 100)
			sm := session.NewManager(eventCh)
			t.Cleanup(func() { sm.Shutdown() })

			sess, err := sm.StartSession(
				"eval-codex-"+tc.Name,
				"eval",
				feature.PhaseResearch,
				command,
				workDir,
				nil,
				&session.SessionOpts{
					Protocol:    proto,
					PermHandler: &session.AutoApproveHandler{},
					LogPath:     logPath,
				},
			)
			if err != nil {
				t.Fatalf("starting codex session: %v", err)
			}

			// app-server stays alive after turn completion. Watch both the
			// event channel and poll the message log for a result message,
			// then stop the session.
			turnDone := make(chan struct{})
			go func() {
				defer close(turnDone)
				for evt := range eventCh {
					if sdkEvt, ok := evt.(session.SDKEventMsg); ok {
						if sdkEvt.Message.Result != nil {
							return
						}
					}
				}
			}()

			select {
			case <-turnDone:
				t.Log("Codex turn completed, stopping session")
			case <-sess.Done():
				t.Log("Codex session exited on its own")
			case <-time.After(180 * time.Second):
				t.Log("Codex session timed out at 180s, stopping")
			}
			_ = sess.Stop()
			<-sess.Done()

			// Codex doesn't emit Read tool_use blocks (file reads are implicit).
			// Check assistant text and tool_progress for skill discovery evidence.
			assistantText := sess.MessageLog().AssistantText()
			t.Logf("Assistant text length: %d bytes", len(assistantText))
			t.Logf("Assistant text: %s", truncateStr(assistantText, 500))
			t.Logf("Total messages: %d", sess.MessageLog().Len())

			// Also check tool_progress data — if Codex ran cat/read on a
			// skill file, the content appears in progress messages.
			allMsgs := sess.MessageLog().Messages()
			var progressText strings.Builder
			for _, msg := range allMsgs {
				if msg.ToolProgress != nil {
					progressText.WriteString(msg.ToolProgress.Data)
					progressText.WriteByte('\n')
				}
			}
			combined := assistantText + "\n" + progressText.String()

			for _, expected := range tc.ExpectRead {
				skillPath := filepath.Join(skillsDir, expected, "SKILL.md")
				// Check for the skill path in assistant text or tool progress.
				foundPath := strings.Contains(combined, skillPath)
				// Check for skill body content (agent read and reflected it).
				body, _ := skilldef.ReadBody(expected)
				fingerprint := extractFingerprint(body)
				foundContent := fingerprint != "" && strings.Contains(combined, fingerprint)

				if !foundPath && !foundContent {
					t.Errorf("expected codex to discover skill %q but found no evidence (path or content) in output", expected)
				} else {
					t.Logf("Found evidence of skill %q discovery (path=%v, content=%v)", expected, foundPath, foundContent)
				}
			}

			for _, unexpected := range tc.NotExpectRead {
				body, bodyErr := skilldef.ReadBody(unexpected)
				if bodyErr != nil {
					continue
				}
				fingerprint := extractFingerprint(body)
				if fingerprint != "" && strings.Contains(combined, fingerprint) {
					t.Errorf("codex appears to have read unexpected skill %q (found body content in output)", unexpected)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Utilities
// ---------------------------------------------------------------------------

// extractFingerprint returns the first non-heading, non-empty line from a
// skill body that is long enough to be a reliable fingerprint (>30 chars).
func extractFingerprint(body string) string {
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || len(line) < 30 {
			continue
		}
		return line
	}
	return ""
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func mapKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// allSkillNames returns all embedded skill names sorted alphabetically.
// Used by eval tests that need to build a preamble with all skills.
func allSkillNames(t *testing.T) []string {
	t.Helper()
	defs, err := skilldef.ParseEmbedded()
	if err != nil {
		t.Fatalf("parsing embedded skills: %v", err)
	}
	names := make([]string, 0, len(defs))
	for name := range defs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
