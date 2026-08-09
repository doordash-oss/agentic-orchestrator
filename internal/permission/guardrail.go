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

package permission

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// GuardrailMaxCommandLen is the maximum byte length of a command the guardrail
// will attempt to classify. Longer commands defer to the human prompt without
// attempting to classify a truncated prefix.
const GuardrailMaxCommandLen = 4096

const (
	automaticReviewStatusPrefix         = "Auto-approved Bash: "
	automaticReviewFastPathStatusPrefix = "Auto-approved Bash (fast path): "
	automaticReviewDeferStatusPrefix    = "Auto-review deferred Bash to you: "
	automaticReviewStatusLimit          = 200
)

// ReviewableBashCommand reports whether a command is well-formed enough to put
// in a reviewer prompt: valid UTF-8, non-blank, and within
// GuardrailMaxCommandLen. It makes no safety claim. The length limit is a
// practical prompt-cost ceiling; over-length commands defer to a human rather
// than being judged from a truncated prefix.
func ReviewableBashCommand(command string) bool {
	if !utf8.ValidString(command) {
		return false
	}
	if len(command) > GuardrailMaxCommandLen {
		return false
	}
	if strings.TrimSpace(command) == "" {
		return false
	}
	return true
}

// GuardrailFastPath determines whether a Bash command is safe to approve
// automatically without a model or human opinion. The command must be
// reviewable, structurally parseable by the supported shell subset, and every
// segment must match the curated development-command policy.
func GuardrailFastPath(command, workDir string, writableRoots []string) bool {
	if !ReviewableBashCommand(command) {
		return false
	}
	parsed, err := parseCommand(command)
	if err != nil {
		return false
	}
	if len(parsed.segments) == 0 {
		return false
	}
	if classifyProcessDiagnostic(parsed) {
		return true
	}
	return classifyParsedCommand(parsed, workDir, writableRoots)
}

// GuardrailBoundSummary returns a redacted, UTF-8-safe truncated summary of
// the command suitable for logging or audit. Uses the shared permission-audit
// sanitization and bounding primitives so the guardrail never introduces a
// second privacy vocabulary or truncation algorithm.
func GuardrailBoundSummary(command string) string {
	return sanitizeAuditInputSummary(command, maxAuditInputSummary)
}

// AutomaticReviewCommandSummary returns the provider-neutral command summary
// shared by automatic-review status and observation records.
func AutomaticReviewCommandSummary(command string) string {
	return automaticReviewCommandSummary(command, automaticReviewStatusPrefix)
}

func automaticReviewCommandSummary(command, prefix string) string {
	command = StripUnsafeControlContent(command)
	command = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		if !unicode.IsControl(r) {
			return r
		}
		if unicode.IsSpace(r) {
			return ' '
		}
		return -1
	}, command)
	summary := sanitizeAuditField(strings.Join(strings.Fields(command), " "))
	return boundAuditText(summary, automaticReviewStatusLimit-len(prefix))
}

// AutomaticReviewStatusLine returns the complete durable approval status,
// including its exact prefix and truncation marker, bounded to 200 bytes.
func AutomaticReviewStatusLine(command string) string {
	return automaticReviewStatusPrefix + AutomaticReviewCommandSummary(command)
}

// AutomaticReviewFastPathStatusLine returns the complete durable status for a
// deterministic approval that ran without a model opinion, bounded to 200
// bytes with a distinct prefix.
func AutomaticReviewFastPathStatusLine(command string) string {
	return automaticReviewFastPathStatusPrefix + automaticReviewCommandSummary(command, automaticReviewFastPathStatusPrefix)
}

// AutomaticReviewDeferStatusLine returns the durable status shown immediately
// before a model-reviewed Bash command is handed to the human permission UI.
func AutomaticReviewDeferStatusLine(command string) string {
	return automaticReviewDeferStatusPrefix + automaticReviewCommandSummary(command, automaticReviewDeferStatusPrefix)
}

// AutomaticReviewFailureStatusLine returns the durable status shown when an
// operational reviewer outcome falls back to the human permission UI.
func AutomaticReviewFailureStatusLine(command, outcome string) string {
	outcome = AutomaticReviewBoundReason(outcome)
	if outcome == "" {
		outcome = "unknown"
	}
	outcome = boundAuditText(outcome, 32)
	prefix := "Auto-review failed (" + outcome + "); asking you about Bash: "
	return prefix + automaticReviewCommandSummary(command, prefix)
}

// AutomaticReviewBoundReason sanitizes a best-effort failure reason with the
// same secret/control vocabulary and 200-byte UTF-8-safe bound as the status.
func AutomaticReviewBoundReason(reason string) string {
	reason = StripUnsafeControlContent(reason)
	reason = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		if unicode.IsControl(r) {
			if unicode.IsSpace(r) {
				return ' '
			}
			return -1
		}
		return r
	}, reason)
	return boundAuditText(sanitizeAuditField(strings.Join(strings.Fields(reason), " ")), automaticReviewStatusLimit)
}

// StripUnsafeControlContent removes complete terminal control strings rather
// than leaving their payload visible after deleting only the introducer and
// terminator. It recognizes both seven-bit ESC forms and their C1 equivalents.
func StripUnsafeControlContent(text string) string {
	const (
		controlNormal = iota
		controlEscape
		controlCSI
		controlOSC
		controlString
		controlOSCEscape
		controlStringEscape
	)

	state := controlNormal
	var safe strings.Builder
	for _, r := range text {
		switch state {
		case controlNormal:
			switch r {
			case '\x1b':
				state = controlEscape
			case '\u009b':
				state = controlCSI
			case '\u009d':
				state = controlOSC
			case '\u0090', '\u009e', '\u009f':
				state = controlString
			default:
				safe.WriteRune(r)
			}
		case controlEscape:
			switch r {
			case '[':
				state = controlCSI
			case ']':
				state = controlOSC
			case 'P', '^', '_':
				state = controlString
			default:
				state = controlNormal
			}
		case controlCSI:
			if r >= 0x40 && r <= 0x7e {
				state = controlNormal
			}
		case controlOSC:
			switch r {
			case '\a', '\u009c':
				state = controlNormal
			case '\x1b':
				state = controlOSCEscape
			}
		case controlString:
			switch r {
			case '\u009c':
				state = controlNormal
			case '\x1b':
				state = controlStringEscape
			}
		case controlOSCEscape:
			if r == '\\' {
				state = controlNormal
			} else {
				state = controlOSC
			}
		case controlStringEscape:
			if r == '\\' {
				state = controlNormal
			} else {
				state = controlString
			}
		}
	}
	return safe.String()
}

// classifySegment checks one parsed segment for structural and policy
// eligibility. Every check must pass; any failure means the segment (and
// therefore the whole compound command) defers.
func classifySegment(seg *parsedSegment, workDir string, writableRoots []string) bool {
	if !validateAssignments(seg.assignments) {
		return false
	}
	if !validateRedirects(seg.redirects) {
		return false
	}
	return classifyCommand(seg, workDir, writableRoots)
}

func classifyParsedCommand(parsed *parsedCommand, workDir string, writableRoots []string) bool {
	effectiveWorkDir := workDir
	for i := range parsed.segments {
		seg := &parsed.segments[i]
		if !classifySegment(seg, effectiveWorkDir, writableRoots) {
			return false
		}
		if !isCDSegment(seg) || i == len(parsed.segments)-1 || segmentInPipeline(parsed, i) {
			continue
		}
		next := parsed.connectors[i]
		if previousConnectorIs(parsed, i, tokOr) || next == tokOr || next == tokSemi || next == tokNewline {
			return false
		}
		if next != tokAnd || hasLaterFailedBranchConnector(parsed, i) {
			return false
		}
		nextWorkDir, ok := cdTargetWorkDir(seg, effectiveWorkDir)
		if !ok {
			return false
		}
		effectiveWorkDir = nextWorkDir
	}
	return true
}

func isCDSegment(seg *parsedSegment) bool {
	return !seg.nameQuoted && seg.name == "cd"
}

func previousConnectorIs(parsed *parsedCommand, segmentIndex int, kind tokenKind) bool {
	return segmentIndex > 0 && parsed.connectors[segmentIndex-1] == kind
}

func segmentInPipeline(parsed *parsedCommand, segmentIndex int) bool {
	if segmentIndex > 0 && parsed.connectors[segmentIndex-1] == tokPipe {
		return true
	}
	return segmentIndex < len(parsed.connectors) && parsed.connectors[segmentIndex] == tokPipe
}

func hasLaterFailedBranchConnector(parsed *parsedCommand, segmentIndex int) bool {
	for _, connector := range parsed.connectors[segmentIndex+1:] {
		switch connector {
		case tokOr, tokSemi, tokNewline:
			return true
		}
	}
	return false
}

// validateAssignments checks that every prefix assignment has an allowed name
// and a literal, non-secret value. Unknown names, secret-matching names, and
// values that match the shared permission-audit secret vocabulary defer.
func validateAssignments(assignments []assignment) bool {
	for _, a := range assignments {
		if !allowedAssignmentName(a.key) {
			return false
		}
		if containsSecretKeyword(a.key) {
			return false
		}
		if valueMatchesSecretPattern(a.value) {
			return false
		}
	}
	return true
}

// allowedAssignmentName reports whether name is in the cross-ecosystem
// allowlist of harmless build/test control variables.
func allowedAssignmentName(name string) bool {
	return guardrailAssignmentAllowlist[name]
}

// guardrailAssignmentAllowlist is the explicit allowlist of harmless
// build/test control variables the guardrail may pass to the reviewer.
// Free-form flag variables that can carry execution-affecting options
// (GOFLAGS, RUSTFLAGS, CFLAGS, CXXFLAGS, LDFLAGS, CPPFLAGS) are excluded
// because they can invoke helper executables (e.g., GOFLAGS=-toolexec=./x)
// or load plugins (e.g., CFLAGS=-fplugin=./evil.so) after model approval.
var guardrailAssignmentAllowlist = map[string]bool{
	// Go — runtime/toolchain toggles only, not flag passthrough
	"GOTRACEBACK": true, "CGO_ENABLED": true,
	"GOOS": true, "GOARCH": true, "GODEBUG": true,
	"GOMAXPROCS": true, "GOGC": true, "GOMEMLIMIT": true,
	"GOEXPERIMENT": true,
	// Rust
	"RUST_BACKTRACE":    true,
	"CARGO_INCREMENTAL": true, "CARGO_TERM_COLOR": true,
	// JS/TS
	"NODE_ENV": true, "CI": true, "FORCE_COLOR": true, "NO_COLOR": true,
	// Python
	"PYTHONDONTWRITEBYTECODE": true, "PYTHONUNBUFFERED": true, "PYTHONHASHSEED": true,
	// General
	"TERM": true, "COLOR": true, "CLICOLOR": true, "CLICOLOR_FORCE": true,
}

// valueMatchesSecretPattern reports whether the assignment value matches any
// of the shared permission-audit redaction patterns.
func valueMatchesSecretPattern(value string) bool {
	for _, pattern := range auditRedactionPatterns {
		if pattern.re.MatchString(value) {
			return true
		}
	}
	return false
}

// containsSecretKeyword reports whether s contains a known secret-vocabulary
// keyword (case-insensitive). This supplements the regex-based
// auditRedactionPatterns for path components and assignment names where
// key=value syntax may not apply.
func containsSecretKeyword(s string) bool {
	lower := strings.ToLower(s)
	for _, kw := range secretKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// secretKeywords are the case-insensitive keywords from the shared
// permission-audit secret vocabulary.
var secretKeywords = []string{
	"api_key", "apikey", "access_token", "refresh_token",
	"auth_token", "id_token", "secret", "token",
	"password", "passwd", "pwd", "credential",
}

// validateRedirects checks that every redirection is an accepted form:
// descriptor routing (e.g., 2>&1) or redirection to /dev/null only.
func validateRedirects(redirects []redirectSpec) bool {
	for _, r := range redirects {
		if !isAcceptedRedirect(r) {
			return false
		}
	}
	return true
}

// isAcceptedRedirect reports whether a redirect specification is one of the
// accepted forms: stdout/stderr to /dev/null (>, >>, 2>) or descriptor
// routing (2>&1, 1>&2). Every other redirect — input from file, output to a
// non-/dev/null path, heredoc, or malformed descriptor — defers.
func isAcceptedRedirect(r redirectSpec) bool {
	if r.isFdRedir {
		switch r.op {
		case "2>&1", "1>&2":
			return true
		default:
			return false
		}
	}
	if !r.isDevNull {
		return false
	}
	switch r.op {
	case ">", ">>", "2>":
		return true
	default:
		return false
	}
}

// classifyCommand dispatches to the command policy. This is the integration
// point between the structural parser and the curated direct-command and
// project-target policies.
func classifyCommand(seg *parsedSegment, workDir string, writableRoots []string) bool {
	name := filepath.Base(seg.name)
	if seg.nameQuoted {
		return false
	}

	// cd is a structural command: validate its target path.
	if seg.name == "cd" {
		return classifyCD(seg, workDir, writableRoots)
	}

	// Recognized wrapper scripts at the project root (./gradlew, ./mvnw) are
	// task runners, not arbitrary direct repository executables. Only the
	// root-bounded wrapper form is accepted; any other path to a wrapper
	// script defers as a direct script.
	if seg.name == "./gradlew" || seg.name == "./mvnw" {
		return classifyProjectTarget(name, seg, workDir, writableRoots)
	}

	// Direct scripts and interpreters always defer.
	if isDirectScript(seg.name) {
		return false
	}

	// Try the curated direct-command policy.
	eligible, found := classifyByPolicy(name, seg, workDir, writableRoots)
	if found {
		return eligible
	}

	// Try the project-target / generator / Git policy.
	if classifyProjectTarget(name, seg, workDir, writableRoots) {
		return true
	}

	return false
}

// classifyCD validates a cd command: the target must be a literal path within
// the working directory or declared roots, with no parent escape, home
// shorthand, variables, external paths, or sensitive components.
func classifyCD(seg *parsedSegment, workDir string, writableRoots []string) bool {
	if len(seg.args) != 1 {
		return false
	}
	if strings.HasPrefix(seg.args[0], "-") || seg.args[0] == "/dev/null" {
		return false
	}
	if !validatePathOperand(seg.args[0], workDir, writableRoots) {
		return false
	}
	_, ok := cdTargetWorkDir(seg, workDir)
	return ok
}

func cdTargetWorkDir(seg *parsedSegment, workDir string) (string, bool) {
	if len(seg.args) != 1 || strings.HasPrefix(seg.args[0], "-") || seg.args[0] == "/dev/null" {
		return "", false
	}
	abs, ok := guardrailOperandAbs(seg.args[0], workDir)
	if !ok {
		return "", false
	}
	resolved, ok := resolveGuardrailPath(abs)
	if !ok {
		return "", false
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return resolved, true
}

// isDirectScript reports whether the command name is a direct script
// invocation (starts with ./ or / or contains a path separator).
func isDirectScript(name string) bool {
	return strings.HasPrefix(name, "./") ||
		strings.HasPrefix(name, "../") ||
		strings.HasPrefix(name, "~/") ||
		strings.HasPrefix(name, "/") ||
		strings.Contains(name, "/")
}

// validatePathOperand validates a literal path argument: it must resolve within
// the working directory or declared roots, with /dev/null as the only accepted
// external path. Existing paths and symlinked ancestors are resolved before the
// root containment check, so a repository-local symlink cannot stand in for an
// external directory. Parent escape, home shorthand, variables, external paths,
// and sensitive components defer.
func validatePathOperand(arg, workDir string, writableRoots []string) bool {
	if arg == "/dev/null" {
		return true
	}
	if arg == "" {
		return false
	}
	if strings.HasPrefix(arg, "~") {
		return false
	}
	parts := strings.Split(arg, "/")
	for i, part := range parts {
		if part == ".." {
			return false
		}
		if isSensitivePathComponent(part) && !isExemptWorktreesComponent(parts, i) {
			return false
		}
	}
	abs, ok := guardrailOperandAbs(arg, workDir)
	if !ok {
		return false
	}
	resolved, ok := resolveGuardrailPath(abs)
	if !ok {
		return false
	}
	return pathWithinGuardrailRoots(resolved, workDir, writableRoots)
}

func guardrailOperandAbs(arg, workDir string) (string, bool) {
	path := filepath.Clean(arg)
	if !filepath.IsAbs(path) {
		base := workDir
		if strings.TrimSpace(base) == "" {
			base = "."
		}
		path = filepath.Join(base, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	return filepath.Clean(abs), true
}

func resolveGuardrailPath(abs string) (string, bool) {
	clean := filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return filepath.Clean(resolved), true
	}

	current := clean
	var suffix []string
	for {
		info, err := os.Lstat(current)
		if err == nil {
			if len(suffix) > 0 && !info.IsDir() {
				return "", false
			}
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", false
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), true
		}
		if !os.IsNotExist(err) {
			return "", false
		}
		parent := filepath.Dir(current)
		if parent == current {
			return clean, true
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func pathWithinGuardrailRoots(path, workDir string, writableRoots []string) bool {
	roots := make([]string, 0, len(writableRoots)+2)
	roots = append(roots, writableRoots...)
	if strings.TrimSpace(workDir) == "" {
		roots = append(roots, ".")
	} else {
		roots = append(roots, workDir)
	}
	for _, root := range roots {
		for _, candidate := range guardrailRootCandidates(root) {
			if path == candidate || strings.HasPrefix(path, candidate+string(filepath.Separator)) {
				return true
			}
		}
	}
	return false
}

func guardrailRootCandidates(root string) []string {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	abs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil
	}
	candidates := []string{filepath.Clean(abs)}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		resolved = filepath.Clean(resolved)
		if resolved != candidates[0] {
			candidates = append(candidates, resolved)
		}
	}
	return candidates
}

// validateOperand validates a non-flag argument as a literal filesystem operand:
// it must be non-sensitive and resolve within the working directory or declared
// roots. Bare basenames receive the same symlink-aware containment check as
// slash-bearing paths because formatters and generators commonly treat them as
// input or output paths.
func validateOperand(arg, workDir string, writableRoots []string) bool {
	if strings.HasPrefix(arg, "@") {
		return false
	}
	return validatePathOperand(arg, workDir, writableRoots)
}

// validateGitPathspec validates a Git pathspec operand, which may appear in
// REV:path form (e.g., HEAD:.env). The path portion is extracted and checked
// for sensitive components and root bounds.
func validateGitPathspec(arg, workDir string, writableRoots []string) bool {
	path := arg
	if idx := strings.Index(arg, ":"); idx >= 0 && idx < len(arg)-1 {
		path = arg[idx+1:]
	}
	if path == "" {
		return true
	}
	return validateOperand(path, workDir, writableRoots)
}

// isSensitivePathComponent reports whether a path component (basename or
// directory name) is a known sensitive file, directory, or matches the
// shared secret vocabulary.
func isSensitivePathComponent(name string) bool {
	if name == "" || name == "." {
		return false
	}
	lower := strings.ToLower(name)
	for _, s := range sensitivePathComponents {
		if lower == s || strings.HasPrefix(lower, s+".") || strings.HasPrefix(lower, s+"-") {
			return true
		}
	}
	return containsSecretKeyword(name)
}

// sensitivePathComponents are known sensitive file and directory names that
// must never appear in an eligible command's path operands.
var sensitivePathComponents = []string{
	".env", ".ssh", ".aws", ".azure", ".boto", ".gnupg", ".gpg", ".kube", ".netrc",
	".npmrc", ".pypirc", ".docker", ".oci", ".password-store",
	".git", ".hg", ".svn", ".jj", ".git-credentials", ".gitconfig",
	".agentic-workflow", ".agentic-orchestrator", ".claude", ".claude.json",
	".codex", ".gemini", ".opencode", ".config",
	"credentials", "credential", "secret", "secrets",
	"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519",
	"password", "passwd",
}

// StateParentComponents are the orchestrator state-parent directory names.
// Feature repo checkouts live inside them under a worktrees/ subtree, so a
// state-parent path component immediately followed by "worktrees" is exempt
// from sensitive-path protection; every other child (features/, config.yaml,
// tokens, provider state) stays protected. internal/agent's sandbox grant
// policy shares this definition.
var StateParentComponents = []string{".agentic-workflow", ".agentic-orchestrator"}

// isExemptWorktreesComponent reports whether the sensitive component parts[i]
// is a state parent immediately followed by a "worktrees" component.
func isExemptWorktreesComponent(parts []string, i int) bool {
	if i+1 >= len(parts) || !strings.EqualFold(parts[i+1], "worktrees") {
		return false
	}
	lower := strings.ToLower(parts[i])
	for _, s := range StateParentComponents {
		if lower == s {
			return true
		}
	}
	return false
}
