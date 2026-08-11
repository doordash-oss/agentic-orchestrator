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

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// WriteScript writes a bash script to dir/name with 0o755 permissions.
// Prepends #!/bin/bash automatically. Returns the absolute path.
func WriteScript(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/bash\n"+content), 0o755); err != nil {
		t.Fatalf("writing script %s: %v", name, err)
	}
	return path
}

// JSONL protocol constants — emit via echo in bash scripts.
const (
	JSONLInit    = `echo '{"type":"system","subtype":"init","session_id":"mock","model":"test"}'`
	JSONLSuccess = `echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"<agentico-outcome>{\"status\":\"success\"}</agentico-outcome>"}]}}'
echo '{"type":"result","subtype":"success","session_id":"mock","total_cost_usd":0.001,"stop_reason":"end_turn"}'`
	JSONLRetry = `echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"<agentico-outcome>{\"status\":\"retry\"}</agentico-outcome>"}]}}'
echo '{"type":"result","subtype":"success","session_id":"mock","total_cost_usd":0.001,"stop_reason":"end_turn"}'`
)

// JSONLAssistant returns a shell echo command emitting an assistant text message.
func JSONLAssistant(text string) string {
	// Escape single quotes for shell embedding, then JSON string metacharacters
	// (a raw newline inside a JSON string breaks the whole JSONL line).
	escaped := strings.ReplaceAll(text, `'`, `'\''`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	escaped = strings.ReplaceAll(escaped, "\n", `\n`)
	return fmt.Sprintf(`echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"%s"}]}}'`, escaped)
}

// JSONLResult returns a shell echo command emitting a result message with embedded text.
func JSONLResult(text string) string {
	escaped := strings.ReplaceAll(text, `"`, `\"`)
	return fmt.Sprintf(`echo '{"type":"result","subtype":"success","session_id":"mock","total_cost_usd":0.001,"result":"%s"}'`, escaped)
}

// JSONLError returns a shell echo for an API error result message.
func JSONLError(errMsg string) string {
	escaped := strings.ReplaceAll(errMsg, `"`, `\"`)
	return fmt.Sprintf(`echo '{"type":"result","subtype":"error","error":"%s"}'`, escaped)
}

func rewriteExistingReportRowsPassed(dirExpr, summary string) string {
	return fmt.Sprintf(`awk -v dir="%s" -v summary=%q '
    /^[[:space:]]*-[[:space:]]*item_id:/ { mode = "" }
    /^[[:space:]]*mode:[[:space:]]*/ { mode = tolower($2) }
    function write_artifact(indent, root, file, body, path) {
      system("mkdir -p \"" dir "/" root "\"")
      path = dir "/" root "/" file
      print body > path
      close(path)
      print indent "evidence:"
      print indent "    summary: " summary
      print indent "    primary: " root "/" file
    }
    /^[[:space:]]*status:[[:space:]]*not_run[[:space:]]*$/ {
      indent = substr($0, 1, index($0, "status") - 1)
      print indent "status: passed"
      if (mode == "visual") {
        write_artifact(indent, "screenshots", "mock-visual-artifact.txt", "mock visual artifact")
        next
      }
      if (mode == "behavioral") {
        write_artifact(indent, "behaviors", "mock-behavioral-artifact.txt", "mock behavioral artifact")
        next
      }
      print indent "evidence:"
      print indent "    exit_code: 0"
      print indent "    summary: " summary
      next
    }
    { print }
  ' "%s/verification-report.yaml" > "%s/verification-report.yaml.tmp" && mv "%s/verification-report.yaml.tmp" "%s/verification-report.yaml"`, dirExpr, summary, dirExpr, dirExpr, dirExpr, dirExpr)
}

// WriteImplementSuccessArtifacts returns a shell snippet that writes the two
// artifacts a passing implement iteration must produce, in the order the
// harness expects:
//
//  1. verification-report.yaml in the latest iteration-* dir under
//     artifactDir/<repoSubdir?>. If the harness pre-seeded a report from a
//     testing contract, preserve the contract metadata and mark any not_run
//     rows passed with structured mock evidence. If no report exists, write a
//     no-check report sufficient for mock agents where the contract is empty.
//  2. progress.md in artifactDir (the same path the harness configures —
//     iter-relative wouldn't be picked up by ParseProgressMd) with the four
//     mandatory sections and `## Iteration State: SUCCESS`.
//
// Use this in mock agents that should drive the implement loop to a
// review_passed outcome. The progress.md is a minimal valid skeleton; tests
// that need to exercise specific deferral/handoff behaviour should write
// their own progress.md instead.
func WriteImplementSuccessArtifacts(artifactDir string) string {
	return writeImplArtifacts(artifactDir, "SUCCESS", "")
}

// WriteImplementRetryArtifacts returns a shell snippet for a RETRY iteration:
// same structure as success, but `## Iteration State: RETRY`. The harness
// loops back to a fresh implement iteration without invoking the review
// gate.
func WriteImplementRetryArtifacts(artifactDir string) string {
	return writeImplArtifacts(artifactDir, "RETRY", "")
}

// WriteImplementMalformedProgress returns a shell snippet that writes a
// malformed progress.md while keeping verification-report.yaml parseable. Use
// it to exercise the "artifact present but contract-invalid" branch.
func WriteImplementMalformedProgress(artifactDir string) string {
	return fmt.Sprintf(`for _d in "%s"/iteration-*; do :; done
mkdir -p "$_d"
cat > "%s/progress.md" <<PROGRESS_EOF
# Iteration Progress

## Iteration Handoff

### Completed this iteration
- malformed progress

## Verification Report

- **Path**: $_d/verification-report.yaml
- **Summary**: 0 passed, 0 failed, 0 blocked, 0 not_run
PROGRESS_EOF
if [ ! -f "$_d/verification-report.yaml" ]; then
  printf 'version: 1\nrequired_checks: []\n' > "$_d/verification-report.yaml"
fi`, artifactDir, artifactDir)
}

// WriteMultiRepoImplementSuccess returns a shell snippet that writes the
// minimal valid implement-iteration handoff (progress.md and
// verification-report.yaml) for every fresh
// `<repo>/iteration-*` directory under artifactBase. Each repo's progress.md
// lives at artifactBase/<repo>/progress.md (the harness reads it from the
// per-repo ArtifactDir); the verification report lives inside each iteration
// directory.
//
// Use this in multi-repo orchestrator tests where one mock agent script
// services every repo.
func WriteMultiRepoImplementSuccess(artifactBase string) string {
	return fmt.Sprintf(`for d in "%s"/*/iteration-*; do
  _repo_dir=$(dirname "$d")
  cat > "$_repo_dir/progress.md" <<PROGRESS_EOF
# Iteration Progress

## Iteration Handoff

### Completed this iteration
- mock work

### Remaining from the plan

### Where I stopped


### Gotchas / blockers / in-flight decisions

## Deferrals

%syaml
deferrals: []
closed_deferrals: []
%s

## Verification Report

- **Path**: $d/verification-report.yaml
- **Summary**: 0 passed, 0 failed, 0 blocked, 0 not_run

## Iteration State

SUCCESS
PROGRESS_EOF
  if [ -f "$d/verification-report.yaml" ]; then
    %s
  else
    printf 'version: 1\nrequired_checks: []\n' > "$d/verification-report.yaml"
  fi
done`, artifactBase, "~~~", "~~~", rewriteExistingReportRowsPassed("$d", "mock agent reported success for this contract check"))
}

// WriteImplementHandoffFiles writes the minimal valid progress.md +
// verification-report.yaml pair directly via os.WriteFile (no shell). Use
// from Go test setup paths that don't drive a mock agent script — for
// example, when seeding a resume scenario whose harness receipt already
// exists but the agent never runs.
//
// progressDir is where progress.md lives (typically the implement loop's
// ArtifactDir). iterDir is the per-iteration dir where
// verification-report.yaml lives. state must be "SUCCESS" or "RETRY".
func WriteImplementHandoffFiles(t *testing.T, progressDir, iterDir, state string) {
	t.Helper()
	if err := os.MkdirAll(progressDir, 0o755); err != nil {
		t.Fatalf("mkdir progress dir: %v", err)
	}
	if err := os.MkdirAll(iterDir, 0o755); err != nil {
		t.Fatalf("mkdir iter dir: %v", err)
	}
	progress := fmt.Sprintf(`# Iteration Progress

## Iteration Handoff

### Completed this iteration
- mock work

### Remaining from the plan

### Where I stopped


### Gotchas / blockers / in-flight decisions

## Deferrals

`+"```yaml"+`
deferrals: []
closed_deferrals: []
`+"```"+`

## Verification Report

- **Path**: %s/verification-report.yaml
- **Summary**: 0 passed, 0 failed, 0 blocked, 0 not_run

## Iteration State

%s
`, iterDir, state)
	if err := os.WriteFile(filepath.Join(progressDir, "progress.md"), []byte(progress), 0o644); err != nil {
		t.Fatalf("write progress.md: %v", err)
	}
	report := "version: 1\nrequired_checks: []\n"
	if err := os.WriteFile(filepath.Join(iterDir, "verification-report.yaml"), []byte(report), 0o644); err != nil {
		t.Fatalf("write verification-report.yaml: %v", err)
	}
}

// WriteImplementProgressMd returns a shell snippet that writes only the
// minimal valid progress.md for the latest iteration (alongside whatever
// verification-report.yaml the test writes itself). Use this for tests
// that exercise specific verification-report content (e.g. invalid reports,
// missing checks) but still need a valid handoff so the parser passes.
func WriteImplementProgressMd(artifactDir, state string) string {
	return fmt.Sprintf(`for _d in "%s"/iteration-*; do :; done
mkdir -p "$_d"
cat > "%s/progress.md" <<PROGRESS_EOF
# Iteration Progress

## Iteration Handoff

### Completed this iteration
- mock work

### Remaining from the plan

### Where I stopped


### Gotchas / blockers / in-flight decisions

## Deferrals

%syaml
deferrals: []
closed_deferrals: []
%s

## Verification Report

- **Path**: $_d/verification-report.yaml
- **Summary**: 0 passed, 0 failed, 0 blocked, 0 not_run

## Iteration State

%s
PROGRESS_EOF`, artifactDir, artifactDir, "~~~", "~~~", state)
}

func writeImplArtifacts(artifactDir, state, stateNote string) string {
	verifBody := "version: 1\nrequired_checks: []\n"
	// Build progress.md inline with shell-side variable interpolation
	// for the iteration dir path. We use a non-quoted heredoc so $_d
	// expands; tilde fences around the YAML block avoid shell command
	// substitution while still satisfying the progress parser.
	//
	// Important: verification-report.yaml is written ONLY if it doesn't
	// already exist on disk. The harness pre-seeds a contract-backed stub
	// when a testing contract is bound (WriteVerificationReportStubFromContract),
	// and tests that assert against that v2 report would lose it if the
	// helper overwrote with the v1 minimal placeholder. Tests that DO
	// want a custom report should write it before this helper runs.
	return fmt.Sprintf(`for _d in "%s"/iteration-*; do :; done
mkdir -p "$_d"
cat > "%s/progress.md" <<PROGRESS_EOF
# Iteration Progress

## Iteration Handoff

### Completed this iteration
- mock work

### Remaining from the plan

### Where I stopped


### Gotchas / blockers / in-flight decisions

## Deferrals

%syaml
deferrals: []
closed_deferrals: []
%s

## Verification Report

- **Path**: $_d/verification-report.yaml
- **Summary**: 0 passed, 0 failed, 0 blocked, 0 not_run
- **Notes**: mock agent

## Iteration State

%s
%s
PROGRESS_EOF
if [ -f "$_d/verification-report.yaml" ]; then
  %s
else
cat > "$_d/verification-report.yaml" <<'VR_EOF'
%s
VR_EOF
fi`, artifactDir, artifactDir, "~~~", "~~~", state, stateNote, rewriteExistingReportRowsPassed("$_d", "mock agent reported success for this contract check"), verifBody)
}

// WritePhasePlanSuccessArtifacts returns a shell snippet that writes the
// plan.md artifact required from a successful phase-planning session.
func WritePhasePlanSuccessArtifacts(phasePlanDir, planText string) string {
	planPath := filepath.Join(phasePlanDir, "plan.md")
	return fmt.Sprintf(`mkdir -p %q
cat > %q <<'PHASE_PLAN_EOF'
%s
PHASE_PLAN_EOF`, phasePlanDir, planPath, planText)
}

// StructuredReviewFeedback returns a structured review-feedback.md body that
// satisfies the file-based handoff schema (## Findings / ## Suggestions /
// ## Verdict). findings and suggestions are the prose bodies for those
// sections; pass empty strings to fall back to the canonical `- (none)`
// placeholder. verdict must be either "APPROVED" or "CHANGES_REQUESTED".
//
// Use this helper in test mocks so the body the mock writes is parseable by
// ParseReviewFeedback.
func StructuredReviewFeedback(findings, suggestions, verdict string) string {
	if verdict != "APPROVED" && verdict != "CHANGES_REQUESTED" {
		panic(fmt.Sprintf("StructuredReviewFeedback: verdict must be APPROVED or CHANGES_REQUESTED, got %q", verdict))
	}
	var b strings.Builder
	b.WriteString("## Findings\n")
	if strings.TrimSpace(findings) == "" {
		b.WriteString("- (none)\n\n")
	} else {
		fmt.Fprintf(&b, "%s\n\n", strings.TrimRight(findings, "\n"))
	}
	b.WriteString("## Suggestions\n")
	if strings.TrimSpace(suggestions) == "" {
		b.WriteString("- (none)\n\n")
	} else {
		fmt.Fprintf(&b, "%s\n\n", strings.TrimRight(suggestions, "\n"))
	}
	fmt.Fprintf(&b, "## Verdict\n%s\n", verdict)
	return b.String()
}

// StructuredReviewFeedbackWithScope is StructuredReviewFeedback plus a
// `## Review Scope` section between `## Suggestions` and `## Verdict`.
// scope must be "targeted" or "full"; justification must be non-empty.
// Use this for test mocks that simulate implementation-review or
// final-review axis feedback.
func StructuredReviewFeedbackWithScope(findings, suggestions, verdict, scope, justification string) string {
	if verdict != "APPROVED" && verdict != "CHANGES_REQUESTED" {
		panic(fmt.Sprintf("StructuredReviewFeedbackWithScope: verdict must be APPROVED or CHANGES_REQUESTED, got %q", verdict))
	}
	if scope != "targeted" && scope != "full" {
		panic(fmt.Sprintf("StructuredReviewFeedbackWithScope: scope must be targeted or full, got %q", scope))
	}
	var b strings.Builder
	b.WriteString("## Findings\n")
	if strings.TrimSpace(findings) == "" {
		b.WriteString("- (none)\n\n")
	} else {
		fmt.Fprintf(&b, "%s\n\n", strings.TrimRight(findings, "\n"))
	}
	b.WriteString("## Suggestions\n")
	if strings.TrimSpace(suggestions) == "" {
		b.WriteString("- (none)\n\n")
	} else {
		fmt.Fprintf(&b, "%s\n\n", strings.TrimRight(suggestions, "\n"))
	}
	fmt.Fprintf(&b, "## Review Scope\n%s\n%s\n\n", scope, justification)
	fmt.Fprintf(&b, "## Verdict\n%s\n", verdict)
	return b.String()
}

// WriteReviewApproved returns a shell snippet that writes a minimal APPROVED
// review-feedback.md to the latest iteration-* directory under artifactDir.
// Must be emitted BEFORE the JSONL success result so the helper's parser
// sees the file when the turn ends.
func WriteReviewApproved(artifactDir string) string {
	return writeIterationReviewFeedbackInLatestIter(artifactDir, StructuredReviewFeedbackWithScope("", "", "APPROVED", "full", "Round 1 — no prior context exists."))
}

// WriteReviewChangesRequested returns a shell snippet that writes a
// CHANGES_REQUESTED review-feedback.md (with the supplied findings prose)
// to the latest iteration-* directory under artifactDir.
func WriteReviewChangesRequested(artifactDir, findings string) string {
	return writeIterationReviewFeedbackInLatestIter(artifactDir, StructuredReviewFeedbackWithScope(findings, "", "CHANGES_REQUESTED", "full", "Round 1 — no prior context exists."))
}

// WriteFinalReviewApproved writes APPROVED review feedback for the latest
// Final Review iteration.
func WriteFinalReviewApproved(artifactDir string) string {
	return MarkLatestVerificationReportPassed(artifactDir) + "\n" + WriteReviewApproved(artifactDir)
}

// WriteFinalReviewChangesRequested writes CHANGES_REQUESTED review feedback
// for the latest Final Review iteration.
func WriteFinalReviewChangesRequested(artifactDir, findings string) string {
	return WriteReviewChangesRequested(artifactDir, findings)
}

// MarkLatestVerificationReportPassed rewrites not_run rows in the latest
// iteration's verification report to passed with structured command evidence.
func MarkLatestVerificationReportPassed(artifactDir string) string {
	return fmt.Sprintf(`for _d in "%s"/iteration-*; do :; done
if [ -f "$_d/verification-report.yaml" ]; then
  %s
fi`, artifactDir, rewriteExistingReportRowsPassed("$_d", "mock final review reported success for this contract check"))
}

// WriteReviewFeedback writes the supplied structured body verbatim into the
// latest iteration-* dir under artifactDir. Use this when a test needs full
// control over Findings / Suggestions / Sticky Approval / Verdict.
func WriteReviewFeedback(artifactDir, body string) string {
	return writeIterationReviewFeedbackInLatestIter(artifactDir, body)
}

// WriteReviewApprovedInDir writes a minimal APPROVED review-feedback.md
// directly to dir (no iteration-* lookup).
func WriteReviewApprovedInDir(dir string) string {
	return writeReviewFeedbackInExactDir(dir, StructuredReviewFeedbackWithScope("", "", "APPROVED", "full", "Round 1 — no prior context exists."))
}

// WriteReviewChangesRequestedInDir writes a CHANGES_REQUESTED
// review-feedback.md directly to dir.
func WriteReviewChangesRequestedInDir(dir, findings string) string {
	return writeReviewFeedbackInExactDir(dir, StructuredReviewFeedbackWithScope(findings, "", "CHANGES_REQUESTED", "full", "Round 1 — no prior context exists."))
}

// WriteReviewFeedbackInDir writes the supplied structured body verbatim into
// dir. Use this when a test needs full control over the file contents.
func WriteReviewFeedbackInDir(dir, body string) string {
	return writeReviewFeedbackInExactDir(dir, body)
}

// WriteFinalReviewMalformedVerdict returns a shell snippet that writes
// a review-feedback.md with an unrecognized verdict.
func WriteFinalReviewMalformedVerdict(iterDir string) string {
	body := "## Findings\n- malformed verdict fixture\n\n## Suggestions\n- (none)\n\n## Verdict\nLGTM\n"
	return fmt.Sprintf(`mkdir -p "%s"
cat > "%s/review-feedback.md" << 'REVIEWEOF'
%s
REVIEWEOF`, iterDir, iterDir, strings.TrimRight(body, "\n"))
}

// WriteFinalReviewMalformedVerdictLatest writes malformed review feedback for
// the latest Final Review iteration.
func WriteFinalReviewMalformedVerdictLatest(artifactDir string) string {
	body := "## Findings\n- malformed verdict fixture\n\n## Suggestions\n- (none)\n\n## Verdict\nLGTM\n"
	return WriteReviewFeedback(artifactDir, body)
}

func writeIterationReviewFeedbackInLatestIter(artifactDir, body string) string {
	return fmt.Sprintf(`_wrote_review_feedback=
for _prompt in $(find "%s" \( -path '*/iteration-*/*/review-prompt.md' -o -path '*/iteration-*/*/*/review-prompt.md' \) -type f 2>/dev/null | sort); do
  [ -f "$_prompt" ] || continue
  _d=$(dirname "$_prompt")
  _fb="$_d/review-feedback.md"
  _tmp="$_fb.tmp.$$"
  cat > "$_tmp" << 'REVIEWEOF'
%s
REVIEWEOF
  mv "$_tmp" "$_fb"
  _wrote_review_feedback=1
done
if [ -z "$_wrote_review_feedback" ]; then
  for _d in "%s"/iteration-*; do :; done
  cat > "$_d/review-feedback.md" << 'REVIEWEOF'
%s
REVIEWEOF
fi`, artifactDir, strings.TrimRight(body, "\n"), artifactDir, strings.TrimRight(body, "\n"))
}

func writeReviewFeedbackInExactDir(dir, body string) string {
	return fmt.Sprintf(`cat > "%s/review-feedback.md" << 'REVIEWEOF'
%s
REVIEWEOF`, dir, strings.TrimRight(body, "\n"))
}

// WriteMultiRepoReviewApproved returns a shell snippet that writes a minimal
// APPROVED review-feedback.md into the most-recent iteration-* directory under
// any `<artifactBase>/<repoName>/` that lacks one. Used by orchestrator tests
// where the same review script runs across multiple repos and must land its
// verdict in the correct per-repo iteration dir.
func WriteMultiRepoReviewApproved(artifactBase string) string {
	body := strings.TrimRight(StructuredReviewFeedback("", "", "APPROVED"), "\n")
	return fmt.Sprintf(`for _repo_dir in "%s"/*; do
  [ -d "$_repo_dir" ] || continue
  _wrote_review_feedback=
  for _prompt in $(find "$_repo_dir" -path '*/iteration-*/review*/review-prompt.md' -type f 2>/dev/null | sort); do
    _dir=$(dirname "$_prompt")
    _fb="$_dir/review-feedback.md"
    _tmp="$_fb.tmp.$$"
    cat > "$_tmp" << 'REVIEWEOF'
%s
REVIEWEOF
    mv "$_tmp" "$_fb"
    _wrote_review_feedback=1
  done
  if [ -n "$_wrote_review_feedback" ]; then continue; fi
  for _iter in "$_repo_dir"/iteration-*; do :; done
  [ -d "$_iter" ] || continue
  if [ -f "$_iter/review-feedback.md" ]; then continue; fi
  cat > "$_iter/review-feedback.md" << 'REVIEWEOF'
%s
REVIEWEOF
  break
done`, artifactBase, body, body)
}

// WriteValidatorApproved returns a shell snippet that writes a minimal APPROVED
// validation-<axis>-feedback.md to attemptDir. axis is the lowercase axis name
// the harness uses to compute the path (architecture, scope, structural,
// grounding, security, performance, testing).
func WriteValidatorApproved(attemptDir, axis string) string {
	return writeValidatorFeedback(attemptDir, axis, StructuredReviewFeedback("", "", "APPROVED"))
}

// WriteAnyValidatorApproved returns a shell snippet that auto-discovers which
// per-axis validator the harness is currently running by locating the
// `validation-*-prompt.md` file the harness wrote ahead of launching the
// helper, then writes the corresponding `validation-<axis>-feedback.md`
// APPROVED handoff alongside it. Use this when a single mock critic script
// has to satisfy multiple validators (different axes, different attemptDirs)
// without the test having to know each axis's path up front.
//
// rootHint is one or more absolute path prefixes the script will scan for
// validation prompt files (typically the test's t.TempDir() roots — "/tmp"
// on Linux, "/private/var/folders" on macOS). Pass at least one prefix; the
// script falls back to "/tmp /private/var/folders" if none provided.
func WriteAnyValidatorApproved(rootHints ...string) string {
	return writeAnyValidatorApproved(rootHints...)
}

func writeAnyValidatorApproved(rootHints ...string) string {
	body := strings.TrimRight(StructuredReviewFeedback("", "", "APPROVED"), "\n")
	if len(rootHints) == 0 {
		rootHints = []string{"/tmp", "/private/var/folders"}
	}
	// Atomic write: cat to a unique tempfile then mv. Multiple validator
	// goroutines can race on the same `_fb` path because each script's `find`
	// scans every axis's prompt file; without atomic rename, two `cat > "$_fb"`
	// calls interleave and produce a malformed feedback file (missing required
	// sections), which the parser rejects as CHANGES_REQUESTED.
	return fmt.Sprintf(`for _prompt in $(find %s -name 'validation-*-prompt.md' -type f 2>/dev/null); do
  _dir=$(dirname "$_prompt")
  _axis=$(basename "$_prompt" | sed -E 's/^validation-(.+)-prompt\.md$/\1/')
  _fb="$_dir/validation-$_axis-feedback.md"
  if [ -f "$_fb" ]; then
    continue
  fi
  _tmp="$_fb.tmp.$$"
  cat > "$_tmp" << 'REVIEWEOF'
%s
REVIEWEOF
  mv "$_tmp" "$_fb"
done`, strings.Join(rootHints, " "), body)
}

// WriteValidatorChangesRequested returns a shell snippet that writes a
// CHANGES_REQUESTED validation-<axis>-feedback.md (with the supplied findings
// prose) to attemptDir.
func WriteValidatorChangesRequested(attemptDir, axis, findings string) string {
	return writeValidatorFeedback(attemptDir, axis, StructuredReviewFeedback(findings, "", "CHANGES_REQUESTED"))
}

// WriteSpecificAxisApproved returns a shell snippet that auto-discovers the
// attemptDir for the supplied axis (by locating the matching
// `validation-<axis>-prompt.md` file under rootHint) and writes a structured
// APPROVED handoff with an optional `## Sticky Approval` block. Use this for
// tests that drive the plan loop with per-axis mock scripts and need each
// axis to emit different sticky-approval data.
func WriteSpecificAxisApproved(rootHint, axis string, frozenSections []string) string {
	body := strings.TrimRight(StructuredReviewFeedbackWithSticky("", "", "APPROVED", axis, frozenSections), "\n")
	return fmt.Sprintf(`for _prompt in $(find %s -name 'validation-%s-prompt.md' -type f 2>/dev/null); do
  _dir=$(dirname "$_prompt")
  _fb="$_dir/validation-%s-feedback.md"
  if [ -f "$_fb" ]; then continue; fi
  _tmp="$_fb.tmp.$$"
  cat > "$_tmp" << 'REVIEWEOF'
%s
REVIEWEOF
  mv "$_tmp" "$_fb"
done`, rootHint, axis, axis, body)
}

// WriteSpecificAxisChangesRequested is the CHANGES_REQUESTED counterpart of
// WriteSpecificAxisApproved.
func WriteSpecificAxisChangesRequested(rootHint, axis, findings string) string {
	body := strings.TrimRight(StructuredReviewFeedback(findings, "", "CHANGES_REQUESTED"), "\n")
	return fmt.Sprintf(`for _prompt in $(find %s -name 'validation-%s-prompt.md' -type f 2>/dev/null); do
  _dir=$(dirname "$_prompt")
  _fb="$_dir/validation-%s-feedback.md"
  if [ -f "$_fb" ]; then continue; fi
  _tmp="$_fb.tmp.$$"
  cat > "$_tmp" << 'REVIEWEOF'
%s
REVIEWEOF
  mv "$_tmp" "$_fb"
done`, rootHint, axis, axis, body)
}

// WriteAnyValidatorChangesRequested mirrors WriteAnyValidatorApproved but
// produces a CHANGES_REQUESTED handoff with the supplied findings prose.
func WriteAnyValidatorChangesRequested(rootHint, findings string, extraHints ...string) string {
	body := strings.TrimRight(StructuredReviewFeedback(findings, "", "CHANGES_REQUESTED"), "\n")
	hints := append([]string{rootHint}, extraHints...)
	return fmt.Sprintf(`for _prompt in $(find %s -name 'validation-*-prompt.md' -type f 2>/dev/null); do
  _dir=$(dirname "$_prompt")
  _axis=$(basename "$_prompt" | sed -E 's/^validation-(.+)-prompt\.md$/\1/')
  _fb="$_dir/validation-$_axis-feedback.md"
  if [ -f "$_fb" ]; then continue; fi
  _tmp="$_fb.tmp.$$"
  cat > "$_tmp" << 'REVIEWEOF'
%s
REVIEWEOF
  mv "$_tmp" "$_fb"
done`, strings.Join(hints, " "), body)
}

// WriteValidatorApprovedSticky returns a shell snippet that writes an APPROVED
// validation-<axis>-feedback.md with a `## Sticky Approval` block enumerating
// the supplied frozen sections — same shape revisers consume during sticky-
// approval propagation.
func WriteValidatorApprovedSticky(attemptDir, axis string, frozenSections []string) string {
	body := StructuredReviewFeedbackWithSticky("", "", "APPROVED", axis, frozenSections)
	return writeValidatorFeedback(attemptDir, axis, body)
}

// StructuredReviewFeedbackWithSticky is StructuredReviewFeedback plus an
// optional `## Sticky Approval` block (rendered between Suggestions and
// Verdict). axis is the lowercase axis name; frozenSections is enumerated
// verbatim under `frozen_sections:`. Pass an empty axis or nil sections to
// fall back to StructuredReviewFeedback (no sticky block).
func StructuredReviewFeedbackWithSticky(findings, suggestions, verdict, axis string, frozenSections []string) string {
	if verdict != "APPROVED" && verdict != "CHANGES_REQUESTED" {
		panic(fmt.Sprintf("StructuredReviewFeedbackWithSticky: verdict must be APPROVED or CHANGES_REQUESTED, got %q", verdict))
	}
	if axis == "" || len(frozenSections) == 0 {
		return StructuredReviewFeedback(findings, suggestions, verdict)
	}
	var b strings.Builder
	b.WriteString("## Findings\n")
	if strings.TrimSpace(findings) == "" {
		b.WriteString("- (none)\n\n")
	} else {
		fmt.Fprintf(&b, "%s\n\n", strings.TrimRight(findings, "\n"))
	}
	b.WriteString("## Suggestions\n")
	if strings.TrimSpace(suggestions) == "" {
		b.WriteString("- (none)\n\n")
	} else {
		fmt.Fprintf(&b, "%s\n\n", strings.TrimRight(suggestions, "\n"))
	}
	fmt.Fprintf(&b, "## Sticky Approval\n\naxis: %s\nfrozen_sections:\n", axis)
	for _, s := range frozenSections {
		fmt.Fprintf(&b, "- %s\n", s)
	}
	fmt.Fprintf(&b, "\n## Verdict\n%s\n", verdict)
	return b.String()
}

func writeValidatorFeedback(attemptDir, axis, body string) string {
	return fmt.Sprintf(`cat > "%s/validation-%s-feedback.md" << 'REVIEWEOF'
%s
REVIEWEOF`, attemptDir, axis, strings.TrimRight(body, "\n"))
}

// MockCommandBuilder returns a CommandBuilder func that dispatches:
// skipPerms=true → reviewScript, else → agentScript.
func MockCommandBuilder(agentScript, reviewScript string) func(model, prompt string, skipPerms bool) []string {
	return func(model, prompt string, skipPerms bool) []string {
		if skipPerms {
			return []string{"bash", reviewScript}
		}
		return []string{"bash", agentScript}
	}
}

// MockInteractiveCommandBuilder returns an InteractiveCommandBuilder func
// that always uses the given script.
func MockInteractiveCommandBuilder(script string) func(model, prompt, systemPrompt string, disallowedTools []string, dangerouslySkipPerms bool, additionalDirs ...string) []string {
	return func(model, prompt, systemPrompt string, disallowedTools []string, dangerouslySkipPerms bool, additionalDirs ...string) []string {
		return []string{"bash", script}
	}
}

// CapturingInteractiveCommandBuilder returns an InteractiveCommandBuilder that
// records the additionalDirs argument from each invocation. The captured dirs
// can be read from the returned slice pointer after the orchestrator completes.
func CapturingInteractiveCommandBuilder(script string) (func(model, prompt, systemPrompt string, disallowedTools []string, dangerouslySkipPerms bool, additionalDirs ...string) []string, *[][]string) {
	var mu sync.Mutex
	var captured [][]string
	builder := func(model, prompt, systemPrompt string, disallowedTools []string, dangerouslySkipPerms bool, additionalDirs ...string) []string {
		mu.Lock()
		dirs := make([]string, len(additionalDirs))
		copy(dirs, additionalDirs)
		captured = append(captured, dirs)
		mu.Unlock()
		return []string{"bash", script}
	}
	return builder, &captured
}

// MockResolveProviderName returns a simple provider name resolver for tests.
// Models in codexModels resolve to "codex"; all others resolve to "claude".
func MockResolveProviderName(codexModels ...string) func(string) (string, error) {
	set := make(map[string]bool, len(codexModels))
	for _, m := range codexModels {
		set[m] = true
	}
	return func(model string) (string, error) {
		if set[model] {
			return "codex", nil
		}
		return "claude", nil
	}
}
