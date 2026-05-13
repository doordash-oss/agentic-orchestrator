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

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/guidelinedef"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	codex_proto "github.com/doordash-oss/agentic-orchestrator/internal/llm/codex"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"gopkg.in/yaml.v3"
)

// guidelineEvalCase represents a single guideline discovery test case.
type guidelineEvalCase struct {
	Name          string   `yaml:"name"`
	Prompt        string   `yaml:"prompt"`
	Language      string   `yaml:"language"`
	ExpectRead    []string `yaml:"expect_read"`
	NotExpectRead []string `yaml:"not_expect_read"`
	MaxTurns      int      `yaml:"max_turns,omitempty"`
}

type guidelineEvalCasesFile struct {
	Cases []guidelineEvalCase `yaml:"cases"`
}

// reconcileGuidelinesDir reconciles all embedded guidelines to a temp directory
// and returns the guidelines directory path.
func reconcileGuidelinesDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "guidelines")
	if err := guidelinedef.ReconcileGuidelines(dir); err != nil {
		t.Fatalf("reconciling guidelines: %v", err)
	}
	return dir
}

// loadGuidelineCases reads the guideline test cases YAML file.
func loadGuidelineCases(t *testing.T) []guidelineEvalCase {
	t.Helper()
	data, err := os.ReadFile("testdata/guideline_cases.yaml")
	if err != nil {
		t.Fatalf("reading guideline_cases.yaml: %v", err)
	}
	var cf guidelineEvalCasesFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		t.Fatalf("parsing guideline_cases.yaml: %v", err)
	}
	if len(cf.Cases) == 0 {
		t.Fatal("no test cases found in guideline_cases.yaml")
	}
	return cf.Cases
}

// extractReadPathSet finds all Read tool calls in the tool use blocks and
// returns the set of file_path values. Reuses the same JSON structure as
// the skill discovery tests.
func extractReadPathSet(blocks []llm.ContentBlock) map[string]bool {
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

// guidelineSubPaths converts relative subcategory paths to absolute paths
// under the given guidelinesDir and language.
func guidelineSubPaths(guidelinesDir, language string, relativePaths []string) []string {
	abs := make([]string, len(relativePaths))
	for i, rel := range relativePaths {
		abs[i] = filepath.Join(guidelinesDir, language, rel)
	}
	return abs
}

// readGuidelineFingerprint reads a guideline subcategory file from disk and
// returns a fingerprint line suitable for content-based detection.
func readGuidelineFingerprint(guidelinesDir, language, relPath string) string {
	absPath := filepath.Join(guidelinesDir, language, relPath)
	data, err := os.ReadFile(absPath)
	if err != nil {
		return ""
	}
	return extractFingerprint(string(data))
}

// TestGuidelineDiscovery_Claude verifies that Claude agents read guideline
// subcategory files (not just the top-level index.md) when given the
// updated preamble instructions.
func TestGuidelineDiscovery_Claude(t *testing.T) {
	skipUnlessEval(t)
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not available")
	}
	unsetClaudeCode(t)

	guidelinesDir := reconcileGuidelinesDir(t)
	preamble := guidelinedef.BuildPreamble(guidelinesDir)
	if preamble == "" {
		t.Fatal("BuildPreamble returned empty string")
	}

	cases := loadGuidelineCases(t)

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			maxTurns := tc.MaxTurns
			if maxTurns == 0 {
				maxTurns = 3
			}

			command := []string{
				"claude",
				"--print",
				"--model", "sonnet",
				"--output-format", "stream-json",
				"--verbose",
				"--dangerously-skip-permissions",
				"--append-system-prompt", preamble,
				"--add-dir", guidelinesDir,
				"--max-turns", strconv.Itoa(maxTurns),
				"--", tc.Prompt,
			}

			workDir := t.TempDir()
			logPath := filepath.Join(workDir, "session.jsonl")

			sess := session.NewSession("eval-guideline-"+tc.Name, "eval", feature.PhaseResearch)

			logFile, err := os.Create(logPath)
			if err != nil {
				t.Fatalf("creating log file: %v", err)
			}
			sess.SetLogFile(logFile)
			t.Cleanup(func() { logFile.Close() })

			go func() {
				for range sess.AttachCh() {
				}
			}()

			if err := sess.Start(command, workDir, nil, func(msg llm.SDKMessage) {}); err != nil {
				t.Fatalf("starting claude session: %v", err)
			}
			sess.CloseStdin()

			select {
			case <-sess.Done():
			case <-time.After(180 * time.Second):
				t.Fatal("claude session did not complete within 180s")
			}

			// Extract all Read tool call paths.
			blocks := sess.MessageLog().ToolUseBlocks()
			readPaths := extractReadPathSet(blocks)

			t.Logf("Read tool calls (%d total):", len(readPaths))
			for p := range readPaths {
				t.Logf("  %s", p)
			}

			// Check that index.md itself was read (sanity check).
			topLevel := filepath.Join(guidelinesDir, tc.Language, "index.md")
			if !readPaths[topLevel] {
				t.Errorf("agent did not read top-level %s", topLevel)
			}

			// Assert expected subcategory files were read.
			for _, absPath := range guidelineSubPaths(guidelinesDir, tc.Language, tc.ExpectRead) {
				if !readPaths[absPath] {
					t.Errorf("expected agent to read %s but it did not", absPath)
				}
			}

			// Assert unexpected subcategory files were not read.
			for _, absPath := range guidelineSubPaths(guidelinesDir, tc.Language, tc.NotExpectRead) {
				if readPaths[absPath] {
					t.Errorf("agent read %s which was not expected", absPath)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Codex eval
// ---------------------------------------------------------------------------

// TestGuidelineDiscovery_Codex verifies that Codex agents discover and read
// guideline subcategory files. Codex doesn't emit Read tool_use blocks, so we
// check assistant text and tool_progress for evidence of file paths or content.
func TestGuidelineDiscovery_Codex(t *testing.T) {
	skipUnlessEval(t)
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex CLI not available")
	}
	unsetClaudeCode(t)

	guidelinesDir := reconcileGuidelinesDir(t)
	preamble := guidelinedef.BuildPreamble(guidelinesDir)
	if preamble == "" {
		t.Fatal("BuildPreamble returned empty string")
	}

	cases := loadGuidelineCases(t)

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			workDir := t.TempDir()
			logPath := filepath.Join(workDir, "session.jsonl")

			command := []string{"codex", "app-server"}

			proto := codex_proto.NewProtocol(llm.ProtocolOpts{
				Model:         "gpt-5.4-mini",
				WorkDir:       workDir,
				SystemPrompt:  preamble,
				InitialPrompt: tc.Prompt,
				DSP:           true,
				WritableRoots: []string{workDir, guidelinesDir},
			})

			eventCh := make(chan interface{}, 100)
			sm := session.NewManager(eventCh)
			t.Cleanup(func() { sm.Shutdown() })

			sess, err := sm.StartSession(
				"eval-codex-guideline-"+tc.Name,
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
			// Check assistant text and tool_progress for evidence.
			assistantText := sess.MessageLog().AssistantText()
			t.Logf("Assistant text length: %d bytes", len(assistantText))
			t.Logf("Assistant text: %s", truncateStr(assistantText, 500))
			t.Logf("Total messages: %d", sess.MessageLog().Len())

			allMsgs := sess.MessageLog().Messages()
			var progressText strings.Builder
			for _, msg := range allMsgs {
				if msg.ToolProgress != nil {
					progressText.WriteString(msg.ToolProgress.Data)
					progressText.WriteByte('\n')
				}
			}
			combined := assistantText + "\n" + progressText.String()

			for _, rel := range tc.ExpectRead {
				absPath := filepath.Join(guidelinesDir, tc.Language, rel)
				foundPath := strings.Contains(combined, absPath)
				fingerprint := readGuidelineFingerprint(guidelinesDir, tc.Language, rel)
				foundContent := fingerprint != "" && strings.Contains(combined, fingerprint)

				if !foundPath && !foundContent {
					t.Errorf("expected codex to discover guideline %q but found no evidence (path or content) in output", rel)
				} else {
					t.Logf("Found evidence of guideline %q discovery (path=%v, content=%v)", rel, foundPath, foundContent)
				}
			}

			for _, rel := range tc.NotExpectRead {
				fingerprint := readGuidelineFingerprint(guidelinesDir, tc.Language, rel)
				if fingerprint != "" && strings.Contains(combined, fingerprint) {
					t.Errorf("codex appears to have read unexpected guideline %q (found body content in output)", rel)
				}
			}
		})
	}
}
