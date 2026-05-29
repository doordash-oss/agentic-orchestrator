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

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
	"github.com/doordash-oss/agentic-orchestrator/internal/session"
	"github.com/doordash-oss/agentic-orchestrator/test/testutil/mocks"
)

func TestRunBlockingLoop_CompleteWritesMeta(t *testing.T) {
	artifactDir := t.TempDir()

	result, err := RunBlockingLoop(context.Background(), blockingLoopTestConfig(artifactDir, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
		if in.Iteration != 1 {
			t.Fatalf("Iteration = %d, want 1", in.Iteration)
		}
		writeBlockingLoopCanonical(t, in.IterationDir, "research.md", "# Research\n")
		writeHelperHandoff(t, in.IterationDir, ResearchProgressHandoffFilename, validResearchProgressHandoff("COMPLETE", "answered all questions"))
		writePhaseComplete(t, in.IterationDir)
		return BlockingLoopRunResult{Status: agentStatusSuccess}, nil
	}), nil)
	if err != nil {
		t.Fatalf("RunBlockingLoop() error = %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusSuccess {
		t.Fatalf("FinalStatus = %q, want %q", result.FinalStatus, BlockingLoopStatusSuccess)
	}
	if result.Iterations != 1 {
		t.Fatalf("Iterations = %d, want 1", result.Iterations)
	}
	if filepath.Base(result.TerminalIterationDir) != "iteration-01" {
		t.Fatalf("TerminalIterationDir = %q, want iteration-01", result.TerminalIterationDir)
	}
	if filepath.Base(result.CanonicalPath) != "research.md" {
		t.Fatalf("CanonicalPath = %q, want research.md", result.CanonicalPath)
	}
	meta := readBlockingLoopMeta(t, result.TerminalIterationDir)
	if meta.AgentStatus != agentStatusSuccess {
		t.Fatalf("AgentStatus = %q, want %q", meta.AgentStatus, agentStatusSuccess)
	}
	if meta.ReviewStatus != HelperHandoffComplete.String() {
		t.Fatalf("ReviewStatus = %q, want COMPLETE", meta.ReviewStatus)
	}
}

func TestRunBlockingLoop_ContinueSeedsPriorCanonical(t *testing.T) {
	artifactDir := t.TempDir()
	var seen []int

	result, err := RunBlockingLoop(context.Background(), blockingLoopTestConfig(artifactDir, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
		seen = append(seen, in.Iteration)
		switch in.Iteration {
		case 1:
			writeBlockingLoopCanonical(t, in.IterationDir, "research.md", "# Research\n\nfirst pass\n")
			writeHelperHandoff(t, in.IterationDir, ResearchProgressHandoffFilename, validResearchProgressHandoff("CONTINUE", "mapped subsystem A"))
		case 2:
			seeded, err := os.ReadFile(filepath.Join(in.IterationDir, "research.md"))
			if err != nil {
				t.Fatalf("read seeded canonical: %v", err)
			}
			if !strings.Contains(string(seeded), "first pass") {
				t.Fatalf("seeded canonical = %q, want prior contents", string(seeded))
			}
			writeBlockingLoopCanonical(t, in.IterationDir, "research.md", string(seeded)+"second pass\n")
			writeHelperHandoff(t, in.IterationDir, ResearchProgressHandoffFilename, validResearchProgressHandoff("COMPLETE", "mapped subsystem B"))
		default:
			t.Fatalf("unexpected iteration %d", in.Iteration)
		}
		writePhaseComplete(t, in.IterationDir)
		return BlockingLoopRunResult{Status: agentStatusSuccess}, nil
	}), nil)
	if err != nil {
		t.Fatalf("RunBlockingLoop() error = %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusSuccess || result.Iterations != 2 {
		t.Fatalf("result = %+v, want success after 2 iterations", result)
	}
	if fmt.Sprint(seen) != "[1 2]" {
		t.Fatalf("seen iterations = %v, want [1 2]", seen)
	}
}

func TestRunBlockingLoop_QALogAccumulationWritesPhaseRoot(t *testing.T) {
	artifactDir := t.TempDir()

	cfg := blockingLoopTestConfig(artifactDir, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
		switch in.Iteration {
		case 1:
			writeBlockingLoopCanonical(t, in.IterationDir, "research.md", "# Research\n\nfirst pass\n")
			writeHelperHandoff(t, in.IterationDir, ResearchProgressHandoffFilename, validResearchProgressHandoff("CONTINUE", "mapped subsystem A"))
			writePhaseComplete(t, in.IterationDir)
			return BlockingLoopRunResult{
				Status: agentStatusSuccess,
				QALog: []ports.QAPair{
					{Question: "First question?", Answer: "First answer"},
				},
			}, nil
		case 2:
			seededQA, err := os.ReadFile(filepath.Join(in.IterationDir, "qa-answers.md"))
			if err != nil {
				t.Fatalf("read seeded qa-answers.md: %v", err)
			}
			if !strings.Contains(string(seededQA), "First question?") {
				t.Fatalf("seeded qa-answers.md = %q, want first question", string(seededQA))
			}
			writeBlockingLoopCanonical(t, in.IterationDir, "research.md", "# Research\n\nsecond pass\n")
			writeHelperHandoff(t, in.IterationDir, ResearchProgressHandoffFilename, validResearchProgressHandoff("COMPLETE", "mapped subsystem B"))
			writePhaseComplete(t, in.IterationDir)
			return BlockingLoopRunResult{
				Status: agentStatusSuccess,
				QALog: []ports.QAPair{
					{Question: "First question?", Answer: "First answer"},
					{Question: "Second question?", Answer: "Second answer", Notes: "second note"},
				},
			}, nil
		default:
			t.Fatalf("unexpected iteration %d", in.Iteration)
			return BlockingLoopRunResult{}, nil
		}
	})
	cfg.AccumulateQALog = true

	result, err := RunBlockingLoop(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("RunBlockingLoop() error = %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusSuccess || result.Iterations != 2 {
		t.Fatalf("result = %+v, want success after 2 iterations", result)
	}

	got, err := os.ReadFile(filepath.Join(artifactDir, "qa-answers.md"))
	if err != nil {
		t.Fatalf("read phase-root qa-answers.md: %v", err)
	}
	content := string(got)
	if !strings.Contains(content, "First question?") || !strings.Contains(content, "Second question?") {
		t.Fatalf("phase-root qa-answers.md missing accumulated questions:\n%s", content)
	}
	if strings.Count(content, "First question?") != 1 {
		t.Fatalf("phase-root qa-answers.md duplicated first question:\n%s", content)
	}
	if strings.Index(content, "First question?") > strings.Index(content, "Second question?") {
		t.Fatalf("phase-root qa-answers.md order is not presented order:\n%s", content)
	}
}

func TestRunBlockingLoop_QALogAccumulationExposesSeededQAPathToPrompt(t *testing.T) {
	artifactDir := t.TempDir()

	cfg := blockingLoopTestConfig(artifactDir, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
		switch in.Iteration {
		case 1:
			writeBlockingLoopCanonical(t, in.IterationDir, "research.md", "# Research\n\nfirst pass\n")
			writeHelperHandoff(t, in.IterationDir, ResearchProgressHandoffFilename, validResearchProgressHandoff("CONTINUE", "mapped subsystem A"))
			writePhaseComplete(t, in.IterationDir)
			return BlockingLoopRunResult{
				Status: agentStatusSuccess,
				QALog: []ports.QAPair{
					{Question: "First question?", Answer: "First answer"},
				},
			}, nil
		case 2:
			want := filepath.Join(in.IterationDir, "qa-answers.md")
			if in.Prompt != want {
				t.Fatalf("iteration 2 prompt = %q, want seeded QA path %q", in.Prompt, want)
			}
			writeBlockingLoopCanonical(t, in.IterationDir, "research.md", "# Research\n\nsecond pass\n")
			writeHelperHandoff(t, in.IterationDir, ResearchProgressHandoffFilename, validResearchProgressHandoff("COMPLETE", "mapped subsystem B"))
			writePhaseComplete(t, in.IterationDir)
			return BlockingLoopRunResult{Status: agentStatusSuccess}, nil
		default:
			t.Fatalf("unexpected iteration %d", in.Iteration)
			return BlockingLoopRunResult{}, nil
		}
	})
	cfg.AccumulateQALog = true
	cfg.BuildPrompt = func(in BlockingLoopPromptInput) (string, error) {
		return in.SeededQAPath, nil
	}

	result, err := RunBlockingLoop(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("RunBlockingLoop() error = %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusSuccess || result.Iterations != 2 {
		t.Fatalf("result = %+v, want success after 2 iterations", result)
	}
}

func TestRunBlockingLoop_QALogDisabledWritesNoQAFile(t *testing.T) {
	artifactDir := t.TempDir()

	result, err := RunBlockingLoop(context.Background(), blockingLoopTestConfig(artifactDir, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
		writeBlockingLoopCanonical(t, in.IterationDir, "research.md", "# Research\n")
		writeHelperHandoff(t, in.IterationDir, ResearchProgressHandoffFilename, validResearchProgressHandoff("COMPLETE", "answered all questions"))
		writePhaseComplete(t, in.IterationDir)
		return BlockingLoopRunResult{
			Status: agentStatusSuccess,
			QALog: []ports.QAPair{
				{Question: "Should not persist?", Answer: "No"},
			},
		}, nil
	}), nil)
	if err != nil {
		t.Fatalf("RunBlockingLoop() error = %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusSuccess {
		t.Fatalf("FinalStatus = %q, want %q", result.FinalStatus, BlockingLoopStatusSuccess)
	}
	if _, err := os.Stat(filepath.Join(artifactDir, "qa-answers.md")); !os.IsNotExist(err) {
		t.Fatalf("phase-root qa-answers.md stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(filepath.Join(artifactDir, "iteration-01", "qa-answers.md")); !os.IsNotExist(err) {
		t.Fatalf("iteration qa-answers.md stat err = %v, want not exist", err)
	}
}

func TestRunBlockingLoop_QALogResumeRehydratesFromForwardedQA(t *testing.T) {
	artifactDir := t.TempDir()
	am := NewArtifactManager(artifactDir)
	iter1, err := am.CreateIterationDir(1)
	if err != nil {
		t.Fatalf("CreateIterationDir(1): %v", err)
	}
	writeBlockingLoopCanonical(t, iter1, "research.md", "# Research\n\ncompleted iteration 1\n")
	writeHelperHandoff(t, iter1, ResearchProgressHandoffFilename, validResearchProgressHandoff("CONTINUE", "completed iteration 1"))
	preservedAnswer := "Preserved answer line 1\n\nPreserved answer line 2"
	if _, err := WriteQAFile([]ports.QAPair{{Question: "Preserved question?", Answer: preservedAnswer}}, iter1); err != nil {
		t.Fatalf("WriteQAFile(iter1): %v", err)
	}
	if err := am.WriteMeta(iter1, IterationMeta{
		Iteration:    1,
		StartedAt:    time.Now(),
		AgentStatus:  agentStatusSuccess,
		ReviewStatus: HelperHandoffContinue.String(),
		MadeProgress: true,
	}); err != nil {
		t.Fatalf("WriteMeta(iter1): %v", err)
	}
	iter2, err := am.CreateIterationDir(2)
	if err != nil {
		t.Fatalf("CreateIterationDir(2): %v", err)
	}
	if _, err := WriteQAFile([]ports.QAPair{{Question: "Stale interrupted question?", Answer: "drop me"}}, iter2); err != nil {
		t.Fatalf("WriteQAFile(iter2): %v", err)
	}

	cfg := blockingLoopTestConfig(artifactDir, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
		if in.Iteration != 2 {
			t.Fatalf("Iteration = %d, want replay of iteration 2", in.Iteration)
		}
		seededQA, err := os.ReadFile(filepath.Join(in.IterationDir, "qa-answers.md"))
		if err != nil {
			t.Fatalf("read seeded qa-answers.md: %v", err)
		}
		if strings.Contains(string(seededQA), "Stale interrupted question?") || !strings.Contains(string(seededQA), "Preserved question?") || !strings.Contains(string(seededQA), preservedAnswer) {
			t.Fatalf("seeded qa-answers.md = %q, want prior completed QA only", string(seededQA))
		}
		writeBlockingLoopCanonical(t, in.IterationDir, "research.md", "# Research\n\ncompleted iteration 2\n")
		writeHelperHandoff(t, in.IterationDir, ResearchProgressHandoffFilename, validResearchProgressHandoff("COMPLETE", "completed iteration 2"))
		writePhaseComplete(t, in.IterationDir)
		return BlockingLoopRunResult{
			Status: agentStatusSuccess,
			QALog: []ports.QAPair{
				{Question: "New question?", Answer: "New answer"},
			},
		}, nil
	})
	cfg.AccumulateQALog = true

	result, err := RunBlockingLoop(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("RunBlockingLoop() error = %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusSuccess || result.Iterations != 2 {
		t.Fatalf("result = %+v, want success at replayed iteration 2", result)
	}
	got, err := os.ReadFile(filepath.Join(artifactDir, "qa-answers.md"))
	if err != nil {
		t.Fatalf("read phase-root qa-answers.md: %v", err)
	}
	content := string(got)
	if !strings.Contains(content, "Preserved question?") || !strings.Contains(content, "New question?") {
		t.Fatalf("phase-root qa-answers.md missing resumed union:\n%s", content)
	}
	if !strings.Contains(content, preservedAnswer) {
		t.Fatalf("phase-root qa-answers.md lost multiline answer:\n%s", content)
	}
	if strings.Contains(content, "Stale interrupted question?") {
		t.Fatalf("phase-root qa-answers.md included stale interrupted QA:\n%s", content)
	}
}

func TestRunBlockingLoopForcedHandoffBehaviorEvidence(t *testing.T) {
	withHandoffPollInterval(t, 2*time.Millisecond)

	root := t.TempDir()
	cases := []blockingLoopEvidenceCase{
		{
			role:             "inquire",
			phase:            feature.PhaseInquire,
			spec:             InquirerRoleSpec(),
			canonicalName:    "inquiry.md",
			handoffName:      InquireProgressHandoffFilename,
			parse:            ParseInquireProgressHandoffMd,
			fingerprint:      InquireProgressHandoffFingerprint,
			handoffBody:      validInquireProgressHandoff,
			iteration1Detail: "captured forced inquire requirement",
			iteration2Detail: "captured final inquire requirement",
		},
		{
			role:             "design",
			phase:            feature.PhaseDesign,
			spec:             DesignerRoleSpec(),
			canonicalName:    "design.md",
			handoffName:      DesignProgressHandoffFilename,
			parse:            ParseDesignProgressHandoffMd,
			fingerprint:      DesignProgressHandoffFingerprint,
			handoffBody:      validDesignProgressHandoff,
			iteration1Detail: "selected forced design boundary",
			iteration2Detail: "selected final design boundary",
		},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			t.Log(runForcedBlockingLoopEvidence(t, root, tc))
		})
	}

	t.Log(runBlockingLoopNoProgressSafetyEvidence(t, root))
}

type blockingLoopEvidenceCase struct {
	role             string
	phase            feature.Phase
	spec             RoleSpec
	canonicalName    string
	handoffName      string
	parse            func(string) (*ParsedHelperHandoff, error)
	fingerprint      func(string) (string, error)
	handoffBody      func(string, string) string
	iteration1Detail string
	iteration2Detail string
}

func runForcedBlockingLoopEvidence(t *testing.T, root string, tc blockingLoopEvidenceCase) string {
	t.Helper()

	featureID := "forced-handoff-" + tc.role
	artifactDir := filepath.Join(root, tc.role)
	stateDir := filepath.Join(root, "state", tc.role)
	workDir := filepath.Join(root, "work", tc.role)
	observeDir := filepath.Join(root, "observe", tc.role)
	for _, dir := range []string{artifactDir, stateDir, workDir, filepath.Join(observeDir, featureID)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", dir, err)
		}
	}

	f := &feature.Feature{
		ID:            featureID,
		Name:          "Forced Handoff " + tc.role,
		Slug:          featureID,
		Description:   "blocking loop forced handoff evidence",
		Status:        feature.StatusInquiring,
		CurrentPhase:  tc.phase,
		TraceID:       "trace-" + featureID,
		ActiveRun:     1,
		RunCount:      1,
		SchemaVersion: feature.SchemaVersionCurrent,
	}

	writeErrs := make(chan error, 2)
	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, _ []string, _ string, _ []string, opts ...*ports.SessionOpts) (ports.SessionHandle, error) {
		iter := 0
		if len(opts) > 0 && opts[0] != nil {
			iter = opts[0].Iteration
		}
		iterDir := filepath.Join(artifactDir, fmt.Sprintf("iteration-%02d", iter))
		sess := session.NewSession(id, featureID, phase)
		sess.SetProviderName("codex")
		sess.SetIteration(iter)
		appendEvidenceQAPair(t, sess, fmt.Sprintf("%s question %d?", tc.role, iter), fmt.Sprintf("%s answer %d line 1\n\n%s answer %d line 2", tc.role, iter, tc.role, iter))

		switch iter {
		case 1:
			sess.SetLatestUsage(&llm.Usage{ContextTotalTokens: 80_000, ContextWindow: 200_000})
			sink := attachCaptureSink(sess)
			go func() {
				select {
				case <-sink.done:
					err := writeBlockingLoopEvidenceFiles(iterDir, tc.canonicalName, "# "+tc.role+"\n\niteration-01 draft\n", tc.handoffName, tc.handoffBody("CONTINUE", tc.iteration1Detail))
					writeErrs <- err
					if err != nil {
						sess.SendStatus(agentStatusFailed)
						return
					}
					sess.SendStatus(agentStatusSuccess)
				case <-time.After(2 * time.Second):
					writeErrs <- fmt.Errorf("%s iteration-01 did not receive context handoff message", tc.role)
					sess.SendStatus(agentStatusFailed)
				}
			}()
		case 2:
			seededCanonical, err := os.ReadFile(filepath.Join(iterDir, tc.canonicalName))
			if err != nil {
				t.Fatalf("%s iteration-02 seeded canonical read: %v", tc.role, err)
			}
			seededQA, err := os.ReadFile(filepath.Join(iterDir, "qa-answers.md"))
			if err != nil {
				t.Fatalf("%s iteration-02 seeded qa read: %v", tc.role, err)
			}
			if !strings.Contains(string(seededCanonical), "iteration-01 draft") || !strings.Contains(string(seededQA), tc.role+" answer 1 line 2") {
				t.Fatalf("%s iteration-02 seed mismatch:\ncanonical:\n%s\nqa:\n%s", tc.role, seededCanonical, seededQA)
			}
			sess.SetLatestUsage(&llm.Usage{ContextTotalTokens: 10_000, ContextWindow: 200_000})
			go func() {
				err := writeBlockingLoopEvidenceFiles(iterDir, tc.canonicalName, string(seededCanonical)+"iteration-02 final\n", tc.handoffName, tc.handoffBody("COMPLETE", tc.iteration2Detail))
				writeErrs <- err
				if err != nil {
					sess.SendStatus(agentStatusFailed)
					return
				}
				sess.SendStatus(agentStatusSuccess)
			}()
		default:
			return nil, fmt.Errorf("unexpected %s iteration %d", tc.role, iter)
		}
		return sess, nil
	}

	cfg := BlockingLoopConfig{
		Label:                       tc.role,
		Feature:                     f,
		FeatureID:                   f.ID,
		Phase:                       tc.phase,
		Role:                        tc.spec.Role,
		Spec:                        tc.spec,
		ArtifactDir:                 artifactDir,
		StateDir:                    stateDir,
		WorkDir:                     workDir,
		SkillsDir:                   "skills",
		GuidelinesDir:               "guidelines",
		Model:                       "gpt-5.3-codex",
		BuildSession:                evidenceBuildSession,
		Observer:                    observe.New(true, observeDir, false, "", false, "agentic"),
		HandoffFilename:             tc.handoffName,
		ParseHandoff:                tc.parse,
		Fingerprint:                 tc.fingerprint,
		CanonicalSelector:           SelectNewestNonExcludedMarkdown,
		MaxConsecNoProgress:         3,
		MaxConsecFailures:           1,
		MaxConsecProtocolViolations: 3,
		AccumulateQALog:             true,
		SessionIDBase:               featureID,
		TelemetryRole:               tc.role,
		InitialPrompt:               "forced handoff evidence",
	}

	result, err := RunBlockingLoop(context.Background(), cfg, sm)
	if err != nil {
		t.Fatalf("RunBlockingLoop(%s): %v", tc.role, err)
	}
	for i := 0; i < result.Iterations; i++ {
		select {
		case err := <-writeErrs:
			if err != nil {
				t.Fatalf("%s session write error: %v", tc.role, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s timed out waiting for session write result %d/%d", tc.role, i+1, result.Iterations)
		}
	}
	if result.FinalStatus != BlockingLoopStatusSuccess || result.Iterations != 2 {
		t.Fatalf("%s result = %+v, want success after two iterations", tc.role, result)
	}

	iter1 := filepath.Join(artifactDir, "iteration-01")
	iter2 := filepath.Join(artifactDir, "iteration-02")
	meta1 := readBlockingLoopMeta(t, iter1)
	meta2 := readBlockingLoopMeta(t, iter2)
	if meta1.ReviewStatus != HelperHandoffContinue.String() || meta2.ReviewStatus != HelperHandoffComplete.String() {
		t.Fatalf("%s meta review statuses = %q/%q, want CONTINUE/COMPLETE", tc.role, meta1.ReviewStatus, meta2.ReviewStatus)
	}
	if meta1.Context == nil || !meta1.Context.HandoffTriggered || meta1.Context.ThresholdTokens != 80_000 {
		t.Fatalf("%s iteration-01 context meta = %+v, want handoff threshold", tc.role, meta1.Context)
	}
	qaBytes, err := os.ReadFile(filepath.Join(artifactDir, "qa-answers.md"))
	if err != nil {
		t.Fatalf("%s read phase-root qa-answers.md: %v", tc.role, err)
	}
	qa := string(qaBytes)
	if !strings.Contains(qa, tc.role+" question 1?") || !strings.Contains(qa, tc.role+" question 2?") || !strings.Contains(qa, tc.role+" answer 1 line 2") {
		t.Fatalf("%s phase-root qa-answers.md missing union:\n%s", tc.role, qa)
	}

	events := filterEventsByType(readObserveEvents(t, observeDir, f.ID), "context.handoff_triggered")
	if len(events) != 1 {
		t.Fatalf("%s context.handoff_triggered events = %d, want 1", tc.role, len(events))
	}
	if events[0].Phase != tc.role {
		t.Fatalf("%s event phase = %q, want %q", tc.role, events[0].Phase, tc.role)
	}
	if got := events[0].Data["threshold_tokens"]; got != float64(80_000) {
		t.Fatalf("%s threshold_tokens = %v, want 80000", tc.role, got)
	}
	eventJSON, err := json.Marshal(events[0])
	if err != nil {
		t.Fatalf("%s marshal event: %v", tc.role, err)
	}

	return fmt.Sprintf(`%s forced-handoff transcript
- event log: %s
- iteration-01: handoff=CONTINUE seeded_iteration_02=false context.handoff_triggered=true threshold_tokens=%d canonical=%s
- iteration-02: seeded_iteration_02=true handoff=COMPLETE canonical=%s
- phase-root qa-answers.md union:
%s`, tc.role, eventJSON, meta1.Context.ThresholdTokens, filepath.Join(iter1, tc.canonicalName), result.CanonicalPath, qa)
}

func runBlockingLoopNoProgressSafetyEvidence(t *testing.T, root string) string {
	t.Helper()

	artifactDir := filepath.Join(root, "safety-rail")
	calls := 0
	cfg := BlockingLoopConfig{
		Label:                       "inquire",
		FeatureID:                   "forced-handoff-safety-rail",
		ArtifactDir:                 artifactDir,
		HandoffFilename:             InquireProgressHandoffFilename,
		ParseHandoff:                ParseInquireProgressHandoffMd,
		Fingerprint:                 InquireProgressHandoffFingerprint,
		CanonicalSelector:           SelectNewestNonExcludedMarkdown,
		MaxConsecNoProgress:         1,
		MaxConsecFailures:           3,
		MaxConsecProtocolViolations: 3,
		RunSession: func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
			calls++
			writeBlockingLoopCanonical(t, in.IterationDir, "inquiry.md", "# Inquiry\n\nsame producer output\n")
			writeHelperHandoff(t, in.IterationDir, InquireProgressHandoffFilename, validInquireProgressHandoff("CONTINUE", "same clarified requirement"))
			writePhaseComplete(t, in.IterationDir)
			return BlockingLoopRunResult{Status: agentStatusSuccess}, nil
		},
	}

	result, err := RunBlockingLoop(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("RunBlockingLoop(no-progress safety): %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusSafetyRail || result.Iterations != 2 {
		t.Fatalf("safety rail result = %+v, want safety_rail after two iterations", result)
	}
	if calls != 2 {
		t.Fatalf("safety rail calls = %d, want 2", calls)
	}
	meta := readBlockingLoopMeta(t, filepath.Join(artifactDir, "iteration-02"))
	if meta.MadeProgress {
		t.Fatalf("safety rail iteration-02 MadeProgress = true, want false")
	}

	return fmt.Sprintf(`no-progress safety rail transcript
- repeated producer handoff: %s
- iteration-01: handoff=CONTINUE made_progress=true
- iteration-02: handoff=CONTINUE made_progress=false
- terminal: final_status=%s iterations=%d last_error=%q`, filepath.Join(artifactDir, "iteration-02", InquireProgressHandoffFilename), result.FinalStatus, result.Iterations, result.LastError)
}

func evidenceBuildSession(opts BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
	return []string{"mock-agent"}, nil, &ports.SessionOpts{
		PIDDir:                        opts.PIDDir,
		PermHandler:                   opts.PermHandler,
		ProviderName:                  "codex",
		ContextHandoffThresholdTokens: 80_000,
	}, nil
}

func appendEvidenceQAPair(t testing.TB, sess *session.Session, question, answer string) {
	t.Helper()
	sess.SetStdinForTest(newCaptureSink())
	questions, err := json.Marshal([]map[string]string{{"question": question}})
	if err != nil {
		t.Fatalf("marshal AskUserQuestion payload: %v", err)
	}
	if err := sess.RespondToAskUser("req-"+strings.ReplaceAll(question, " ", "-"), questions, map[string]string{question: answer}, nil); err != nil {
		t.Fatalf("RespondToAskUser(%q): %v", question, err)
	}
}

func writeBlockingLoopEvidenceFiles(iterDir, canonicalName, canonicalBody, handoffName, handoffBody string) error {
	if err := os.MkdirAll(iterDir, 0o755); err != nil {
		return fmt.Errorf("mkdir iteration dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(iterDir, canonicalName), []byte(canonicalBody), 0o644); err != nil {
		return fmt.Errorf("write canonical: %w", err)
	}
	if err := os.WriteFile(filepath.Join(iterDir, handoffName), []byte(handoffBody), 0o644); err != nil {
		return fmt.Errorf("write handoff: %w", err)
	}
	if err := os.WriteFile(filepath.Join(iterDir, PhaseCompleteFile), []byte("complete\n"), 0o644); err != nil {
		return fmt.Errorf("write phase_complete: %w", err)
	}
	return nil
}

func TestRunBlockingLoopSafetyRails(t *testing.T) {
	t.Run("no progress", func(t *testing.T) {
		artifactDir := t.TempDir()
		calls := 0
		cfg := blockingLoopTestConfig(artifactDir, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
			calls++
			writeBlockingLoopCanonical(t, in.IterationDir, "research.md", "# Research\n")
			writeHelperHandoff(t, in.IterationDir, ResearchProgressHandoffFilename, validResearchProgressHandoff("CONTINUE", "same finding"))
			writePhaseComplete(t, in.IterationDir)
			return BlockingLoopRunResult{Status: agentStatusSuccess}, nil
		})
		cfg.MaxConsecNoProgress = 1

		result, err := RunBlockingLoop(context.Background(), cfg, nil)
		if err != nil {
			t.Fatalf("RunBlockingLoop() error = %v", err)
		}
		if result.FinalStatus != BlockingLoopStatusSafetyRail {
			t.Fatalf("FinalStatus = %q, want %q", result.FinalStatus, BlockingLoopStatusSafetyRail)
		}
		if calls != 2 {
			t.Fatalf("calls = %d, want 2", calls)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		artifactDir := t.TempDir()
		calls := 0
		cfg := blockingLoopTestConfig(artifactDir, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
			calls++
			writeBlockingLoopCanonical(t, in.IterationDir, "research.md", "# Research\n")
			writeHelperHandoff(t, in.IterationDir, ResearchProgressHandoffFilename, validResearchProgressHandoff("INVALID", "same finding"))
			writePhaseComplete(t, in.IterationDir)
			return BlockingLoopRunResult{Status: agentStatusSuccess}, nil
		})
		cfg.MaxConsecProtocolViolations = 2

		result, err := RunBlockingLoop(context.Background(), cfg, nil)
		if err != nil {
			t.Fatalf("RunBlockingLoop() error = %v", err)
		}
		if result.FinalStatus != BlockingLoopStatusProtocolViolation {
			t.Fatalf("FinalStatus = %q, want %q", result.FinalStatus, BlockingLoopStatusProtocolViolation)
		}
		if calls != 2 {
			t.Fatalf("calls = %d, want 2", calls)
		}
	})

	t.Run("session failures", func(t *testing.T) {
		artifactDir := t.TempDir()
		calls := 0
		cfg := blockingLoopTestConfig(artifactDir, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
			calls++
			return BlockingLoopRunResult{Status: agentStatusFailed}, nil
		})
		cfg.MaxConsecFailures = 2

		result, err := RunBlockingLoop(context.Background(), cfg, nil)
		if err != nil {
			t.Fatalf("RunBlockingLoop() error = %v", err)
		}
		if result.FinalStatus != BlockingLoopStatusSafetyRail {
			t.Fatalf("FinalStatus = %q, want %q", result.FinalStatus, BlockingLoopStatusSafetyRail)
		}
		if calls != 2 {
			t.Fatalf("calls = %d, want 2", calls)
		}
	})
}

func TestRunBlockingLoop_ContinueWithoutFixedCap(t *testing.T) {
	artifactDir := t.TempDir()
	calls := 0

	result, err := RunBlockingLoop(context.Background(), blockingLoopTestConfig(artifactDir, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
		calls++
		writeBlockingLoopCanonical(t, in.IterationDir, "research.md", fmt.Sprintf("# Research\n\niteration %d\n", in.Iteration))
		state := "CONTINUE"
		if in.Iteration == 6 {
			state = "COMPLETE"
		}
		writeHelperHandoff(t, in.IterationDir, ResearchProgressHandoffFilename, validResearchProgressHandoff(state, fmt.Sprintf("finding %d", in.Iteration)))
		writePhaseComplete(t, in.IterationDir)
		return BlockingLoopRunResult{Status: agentStatusSuccess}, nil
	}), nil)
	if err != nil {
		t.Fatalf("RunBlockingLoop() error = %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusSuccess || result.Iterations != 6 {
		t.Fatalf("result = %+v, want success after 6 iterations", result)
	}
	if calls != 6 {
		t.Fatalf("calls = %d, want 6", calls)
	}
}

func TestRunBlockingLoop_ResumeReplaysIncompleteIteration(t *testing.T) {
	artifactDir := t.TempDir()
	am := NewArtifactManager(artifactDir)
	iter1, err := am.CreateIterationDir(1)
	if err != nil {
		t.Fatalf("CreateIterationDir(1): %v", err)
	}
	writeBlockingLoopCanonical(t, iter1, "research.md", "# Research\n\ncompleted iteration 1\n")
	writeHelperHandoff(t, iter1, ResearchProgressHandoffFilename, validResearchProgressHandoff("CONTINUE", "completed iteration 1"))
	if err := am.WriteMeta(iter1, IterationMeta{
		Iteration:    1,
		StartedAt:    time.Now(),
		AgentStatus:  agentStatusSuccess,
		ReviewStatus: HelperHandoffContinue.String(),
		MadeProgress: true,
	}); err != nil {
		t.Fatalf("WriteMeta(iter1): %v", err)
	}
	iter2, err := am.CreateIterationDir(2)
	if err != nil {
		t.Fatalf("CreateIterationDir(2): %v", err)
	}
	writeBlockingLoopCanonical(t, iter2, "research.md", "# Research\n\nstale interrupted partial\n")

	var seen []int
	result, err := RunBlockingLoop(context.Background(), blockingLoopTestConfig(artifactDir, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
		seen = append(seen, in.Iteration)
		if in.Iteration != 2 {
			t.Fatalf("Iteration = %d, want replay of iteration 2", in.Iteration)
		}
		seeded, err := os.ReadFile(filepath.Join(in.IterationDir, "research.md"))
		if err != nil {
			t.Fatalf("read seeded canonical: %v", err)
		}
		if strings.Contains(string(seeded), "stale interrupted partial") || !strings.Contains(string(seeded), "completed iteration 1") {
			t.Fatalf("seeded canonical = %q, want replay from completed iteration 1 only", string(seeded))
		}
		writeBlockingLoopCanonical(t, in.IterationDir, "research.md", string(seeded)+"completed iteration 2\n")
		writeHelperHandoff(t, in.IterationDir, ResearchProgressHandoffFilename, validResearchProgressHandoff("COMPLETE", "completed iteration 2"))
		writePhaseComplete(t, in.IterationDir)
		return BlockingLoopRunResult{Status: agentStatusSuccess}, nil
	}), nil)
	if err != nil {
		t.Fatalf("RunBlockingLoop() error = %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusSuccess || result.Iterations != 2 {
		t.Fatalf("result = %+v, want success at replayed iteration 2", result)
	}
	if fmt.Sprint(seen) != "[2]" {
		t.Fatalf("seen iterations = %v, want [2]", seen)
	}
}

func TestRunBlockingLoop_InterruptedInFlightIterationLeavesNoMeta(t *testing.T) {
	artifactDir := t.TempDir()
	store := mocks.NewMockFeatureStore()
	current := &feature.Feature{ID: "feat-research", Status: feature.StatusResearching}
	store.LoadFn = func(id string) (*feature.Feature, error) {
		if id != current.ID {
			t.Fatalf("Load(%q), want %q", id, current.ID)
		}
		clone := *current
		return &clone, nil
	}

	cfg := blockingLoopTestConfig(artifactDir, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
		writeBlockingLoopCanonical(t, in.IterationDir, "research.md", "# Research\n\npartial interrupted work\n")
		current.Status = feature.StatusInterrupted
		return BlockingLoopRunResult{Status: agentStatusFailed}, nil
	})
	cfg.Feature = current
	cfg.FeatureStore = store

	result, err := RunBlockingLoop(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("RunBlockingLoop() error = %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusInterrupted {
		t.Fatalf("FinalStatus = %q, want %q", result.FinalStatus, BlockingLoopStatusInterrupted)
	}
	if result.Iterations != 0 {
		t.Fatalf("Iterations = %d, want 0 completed iterations", result.Iterations)
	}
	metaPath := filepath.Join(artifactDir, "iteration-01", "meta.yaml")
	if _, err := os.Stat(metaPath); !os.IsNotExist(err) {
		t.Fatalf("Stat(%q) error = %v, want not exist", metaPath, err)
	}
}

func TestRunBlockingLoop_SessionManagerShutdownLeavesNoMeta(t *testing.T) {
	artifactDir := t.TempDir()
	sm := mocks.NewMockSessionManager()
	sm.StartSessionFn = func(id, featureID string, phase feature.Phase, command []string, workdir string, env []string, opts ...*ports.SessionOpts) (ports.SessionHandle, error) {
		return nil, ports.ErrSessionShuttingDown
	}

	cfg := blockingLoopTestConfig(artifactDir, nil)
	cfg.RunSession = nil
	cfg.Phase = feature.PhaseResearch
	cfg.Spec = ResearcherRoleSpec()
	cfg.StateDir = t.TempDir()
	cfg.BuildSession = func(BuildSessionOpts) ([]string, []string, *ports.SessionOpts, error) {
		return []string{"agent"}, nil, &ports.SessionOpts{}, nil
	}

	result, err := RunBlockingLoop(context.Background(), cfg, sm)
	if err != nil {
		t.Fatalf("RunBlockingLoop() error = %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusInterrupted {
		t.Fatalf("FinalStatus = %q, want %q", result.FinalStatus, BlockingLoopStatusInterrupted)
	}
	metaPath := filepath.Join(artifactDir, "iteration-01", "meta.yaml")
	if _, err := os.Stat(metaPath); !os.IsNotExist(err) {
		t.Fatalf("Stat(%q) error = %v, want not exist", metaPath, err)
	}
}

func TestRunBlockingLoop_ShutdownFailedStatusLeavesNoMeta(t *testing.T) {
	artifactDir := t.TempDir()
	sm := mocks.NewMockSessionManager()
	sm.ShuttingDownVal = true

	cfg := blockingLoopTestConfig(artifactDir, func(_ context.Context, in BlockingLoopRunInput) (BlockingLoopRunResult, error) {
		writeBlockingLoopCanonical(t, in.IterationDir, "research.md", "# Research\n\npartial interrupted work\n")
		return BlockingLoopRunResult{Status: agentStatusFailed}, nil
	})

	result, err := RunBlockingLoop(context.Background(), cfg, sm)
	if err != nil {
		t.Fatalf("RunBlockingLoop() error = %v", err)
	}
	if result.FinalStatus != BlockingLoopStatusInterrupted {
		t.Fatalf("FinalStatus = %q, want %q", result.FinalStatus, BlockingLoopStatusInterrupted)
	}
	metaPath := filepath.Join(artifactDir, "iteration-01", "meta.yaml")
	if _, err := os.Stat(metaPath); !os.IsNotExist(err) {
		t.Fatalf("Stat(%q) error = %v, want not exist", metaPath, err)
	}
}

func TestBlockingLoopContextHandoffRoleUsesSkillName(t *testing.T) {
	cfg := normalizeBlockingLoopConfig(BlockingLoopConfig{
		Label:         "research",
		TelemetryRole: "research",
		Spec:          ResearcherRoleSpec(),
	})

	if got := blockingLoopContextHandoffRole(cfg); got != "research-codebase" {
		t.Fatalf("blockingLoopContextHandoffRole() = %q, want research-codebase", got)
	}
}

func blockingLoopTestConfig(artifactDir string, run func(context.Context, BlockingLoopRunInput) (BlockingLoopRunResult, error)) BlockingLoopConfig {
	return BlockingLoopConfig{
		Label:                       "research",
		FeatureID:                   "feat-research",
		ArtifactDir:                 artifactDir,
		HandoffFilename:             ResearchProgressHandoffFilename,
		ParseHandoff:                ParseResearchProgressHandoffMd,
		Fingerprint:                 ResearchProgressHandoffFingerprint,
		CanonicalSelector:           SelectNewestNonExcludedMarkdown,
		MaxConsecNoProgress:         3,
		MaxConsecFailures:           3,
		MaxConsecProtocolViolations: 3,
		RunSession:                  run,
	}
}

func writeBlockingLoopCanonical(t testing.TB, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", filepath.Join(dir, name), err)
	}
}

func writePhaseComplete(t testing.TB, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, PhaseCompleteFile), []byte("done\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(phase_complete): %v", err)
	}
}

func readBlockingLoopMeta(t testing.TB, iterDir string) IterationMeta {
	t.Helper()
	meta, err := NewArtifactManager(filepath.Dir(iterDir)).ReadMeta(iterDir)
	if err != nil {
		t.Fatalf("ReadMeta(%q): %v", iterDir, err)
	}
	return meta
}
