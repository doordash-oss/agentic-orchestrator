// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package errcat

import (
	"fmt"
	"io"
	"strings"
)

// classPrefix maps a severity class to the terminal prefix of its heading
// line. An unknown class renders as a blocking error.
func classPrefix(class Class) string {
	switch class {
	case ClassWarning:
		return "warning"
	case ClassNeedsAction:
		return "needs-action"
	default:
		return "error"
	}
}

// Fprint renders e to w in the class-keyed terminal shape:
//
//	error[<code>]: <title>
//	  <summary>
//	  hint: <remediation hint>
//	  <context key: value lines>
//	  detail: <diagnostics>
//	    <continued diagnostics lines>
//
// The first line is `warning[...]` for the warning class and
// `needs-action[...]` for the needs_action class. The hint line appears only
// when the entry carries a remediation hint, context blocks render as
// indented key-value lines, and `detail:` appears only when diagnostics are
// nonempty, with every additional diagnostics line indented beneath it. No
// line ever carries trailing whitespace, and a zero-value error renders as
// the fallback internal error rather than an empty block.
func Fprint(w io.Writer, e Error) error {
	e = completeForRender(e)
	if err := FprintHeading(w, e); err != nil {
		return err
	}
	if err := FprintSummary(w, e); err != nil {
		return err
	}
	if err := FprintHint(w, e); err != nil {
		return err
	}
	if err := writeContext(w, e); err != nil {
		return err
	}
	return writeDetail(w, e)
}

// FprintHeading writes only the class-keyed heading line. Callers that list
// additional lines between the summary and the hint (the CLI's protocol
// violation listing) compose the block from these part writers so the
// terminal shape stays owned by this package.
func FprintHeading(w io.Writer, e Error) error {
	_, err := fmt.Fprintf(w, "%s[%s]: %s\n", classPrefix(e.Class), e.Code, strings.TrimSpace(e.Title))
	return err
}

// FprintSummary writes only the indented summary line.
func FprintSummary(w io.Writer, e Error) error {
	_, err := fmt.Fprintln(w, "  "+strings.TrimSpace(e.Summary))
	return err
}

// FprintHint writes only the indented hint line, and nothing when e carries
// no remediation hint.
func FprintHint(w io.Writer, e Error) error {
	if e.Remediation == nil {
		return nil
	}
	hint := strings.TrimSpace(e.Remediation.Hint)
	if hint == "" {
		return nil
	}
	_, err := fmt.Fprintln(w, "  hint: "+hint)
	return err
}

// completeForRender replaces a degenerate error (zero value, empty title or
// summary, unknown class) with the fallback internal error so the renderer
// never emits an empty or malformed block.
func completeForRender(e Error) Error {
	if e.Code == "" || !e.Class.Valid() ||
		strings.TrimSpace(e.Title) == "" || strings.TrimSpace(e.Summary) == "" {
		return Error{
			Code:    InternalError,
			Class:   ClassBlocking,
			Title:   fallbackTitle,
			Summary: fallbackSummary,
		}
	}
	return e
}

// writeContext renders the context blocks as indented key-value lines.
func writeContext(w io.Writer, e Error) error {
	if e.Context == nil {
		return nil
	}
	for _, repo := range e.Context.Repositories {
		line := "  repository: " + strings.TrimSpace(repo.Name)
		if repo.Branch != "" {
			line += ", branch " + repo.Branch
		}
		if err := writeLine(w, line); err != nil {
			return err
		}
		if err := writeRepositoryFields(w, repo); err != nil {
			return err
		}
	}
	if e.Context.SetupTask != nil {
		line := "  setup_task: " + strings.TrimSpace(e.Context.SetupTask.Label)
		if kind := strings.TrimSpace(e.Context.SetupTask.Kind); kind != "" {
			line += ", kind " + kind
		}
		if err := writeLine(w, line); err != nil {
			return err
		}
	}
	if e.Context.Phase != nil {
		line := "  phase: " + strings.TrimSpace(e.Context.Phase.Name)
		if e.Context.Phase.Iteration != 0 {
			line += fmt.Sprintf(" (iteration %d)", e.Context.Phase.Iteration)
		}
		if err := writeLine(w, line); err != nil {
			return err
		}
	}
	if e.Context.Command != nil {
		if e.Context.Command.ExitCode != 0 {
			if err := writeLine(w, fmt.Sprintf("  exit_code: %d", e.Context.Command.ExitCode)); err != nil {
				return err
			}
		}
		if len(e.Context.Command.LogPaths) > 0 {
			if err := writeLine(w, "  log_paths: "+strings.Join(e.Context.Command.LogPaths, ", ")); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeRepositoryFields renders one repository's file lists and SHA fields as
// lines indented under its `repository:` line.
func writeRepositoryFields(w io.Writer, repo CodeRepository) error {
	lists := []struct {
		key    string
		values []string
	}{
		{"conflict_files", repo.ConflictFiles},
		{"dirty_files", repo.DirtyFiles},
	}
	for _, list := range lists {
		if len(list.values) == 0 {
			continue
		}
		if err := writeLine(w, "    "+list.key+": "+strings.Join(list.values, ", ")); err != nil {
			return err
		}
	}
	scalars := []struct {
		key   string
		value string
	}{
		{"rebase_target", repo.RebaseTarget},
		{"parent_anchor_sha", repo.ParentAnchorSHA},
		{"expected_ref_sha", repo.ExpectedRefSHA},
		{"child_head_sha", repo.ChildHeadSHA},
		{"candidate_sha", repo.CandidateSHA},
		{"merge_head", repo.MergeHEAD},
		{"observed_sha", repo.ObservedSHA},
	}
	for _, scalar := range scalars {
		if scalar.value == "" {
			continue
		}
		if err := writeLine(w, "    "+scalar.key+": "+scalar.value); err != nil {
			return err
		}
	}
	if repo.RemoteOnlyCommits != 0 {
		if err := writeLine(w, fmt.Sprintf("    remote_only_commits: %d", repo.RemoteOnlyCommits)); err != nil {
			return err
		}
	}
	return nil
}

// writeDetail renders the raw diagnostics: the first line on the `detail:`
// line and every further line indented beneath it. Diagnostics are printed
// verbatim (they are the deepest disclosure), only trimmed of trailing
// whitespace per line.
func writeDetail(w io.Writer, e Error) error {
	detail := strings.TrimSpace(e.Diagnostics)
	if detail == "" {
		return nil
	}
	lines := strings.Split(detail, "\n")
	if err := writeLine(w, "  detail: "+strings.TrimSpace(lines[0])); err != nil {
		return err
	}
	for _, line := range lines[1:] {
		if err := writeLine(w, "    "+strings.TrimSpace(line)); err != nil {
			return err
		}
	}
	return nil
}

// writeLine writes one already-indented content line, right-trimmed so no
// line ever ends with whitespace.
func writeLine(w io.Writer, line string) error {
	_, err := fmt.Fprintln(w, strings.TrimRight(line, " \t"))
	return err
}
