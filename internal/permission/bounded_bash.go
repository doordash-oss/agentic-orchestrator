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
	"path/filepath"
	"strings"
)

// bashAllowed reports whether a bounded helper's bash command is permitted. The
// helper must be able to run its read-only analysis and write its own artifacts
// without ever being denied — glm-5p2 ends its turn on ANY denied tool call, so
// a false denial aborts the helper before it writes feedback + phase_complete.
// Permitted: the read-only artifact preflight; pipelines of read-only programs
// (quoted regex metacharacters and pipes are fine); and writes (redirects or
// touch) that target only the helper's declared artifact paths. Everything else
// — writes elsewhere, mutating programs, command substitution — is denied so the
// helper still cannot mutate the worktree.
func (h *BoundedHelperArtifactHandler) bashAllowed(input string) bool {
	if boundedHelperPreflightAllowed(input) {
		return true
	}
	command := strings.TrimSpace(extractBashCommand(input))
	if command == "" {
		return false
	}
	command = stripReadOnlyShellRedirections(command)
	segs, ok := parseBashSegments(command)
	if !ok || len(segs) == 0 {
		return false
	}
	for _, s := range segs {
		if !h.bashSegmentAllowed(s) {
			return false
		}
	}
	return true
}

// bashSegment is one pipeline/list stage: its words (program + args, with quotes
// resolved) and the targets of any stdout write redirection (> or >>).
type bashSegment struct {
	words        []string
	writeTargets []string
}

// bashSegmentAllowed approves a single segment: a write to a declared artifact
// (or /dev/null) by a content-emitting program, an artifact-creating touch, or a
// read-only program with no write redirection.
func (h *BoundedHelperArtifactHandler) bashSegmentAllowed(s bashSegment) bool {
	if len(s.words) == 0 {
		return false
	}
	prog := filepath.Base(s.words[0])

	if len(s.writeTargets) > 0 {
		for _, t := range s.writeTargets {
			if !h.writeTargetAllowed(t) {
				return false
			}
		}
		switch prog {
		case "cat", "echo", "printf", "true", ":":
			return true
		default:
			return false
		}
	}

	if prog == "touch" {
		paths := 0
		for _, w := range s.words[1:] {
			if strings.HasPrefix(w, "-") {
				continue
			}
			if !h.pathAllowed(w) {
				return false
			}
			paths++
		}
		return paths > 0
	}

	return readOnlyProgramAllowed(prog, s.words)
}

// writeTargetAllowed permits writing to a declared artifact path or discarding
// to /dev/null.
func (h *BoundedHelperArtifactHandler) writeTargetAllowed(target string) bool {
	if target == "/dev/null" {
		return true
	}
	return h.pathAllowed(target)
}

// readOnlyProgramAllowed reports whether a program (with its args) only reads.
func readOnlyProgramAllowed(prog string, words []string) bool {
	switch prog {
	case "cd", "pwd", "ls", "cat", "head", "tail", "wc", "grep", "egrep", "fgrep",
		"rg", "echo", "printf", "true", "false", "sort", "uniq", "cut", "tr", "comm",
		"diff", "nl", "tac", "rev", "fold", "column", "basename", "dirname", "realpath",
		"readlink", "stat", "file", "date", "seq", "jq", "yq", "awk", "gawk", "xxd", "od",
		"env", "test", "[":
		return true
	case "sed":
		return !hasSedInPlaceFlag(words[1:])
	case "git":
		return gitReadOnlySubcommandAllowed(words[1:])
	case "find":
		return !hasAnyShellToken(words[1:], "-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprintf", "-fls")
	default:
		return false
	}
}

// parseBashSegments splits a command into pipeline/list segments, quote-aware, so
// metacharacters inside quotes (a backtick in a regex, a pipe in '[a|b]') are
// literal rather than operators. It returns ok=false on command substitution
// ($(...) or backticks active outside single quotes), background (&), or
// unbalanced quotes — constructs a bounded helper has no need for and that could
// escape the read-only/artifact-write envelope. A heredoc (<<) ends parsing: its
// body is data, and any write redirect (cat > artifact) was already captured.
func parseBashSegments(cmd string) ([]bashSegment, bool) {
	var segs []bashSegment
	cur := bashSegment{}
	var word strings.Builder
	wordHasContent := false
	pendingWrite := false // next word is a > / >> target
	pendingDrop := false  // next word is a < input source (read; ignored)
	inSingle, inDouble := false, false

	flushWord := func() {
		if !wordHasContent {
			return
		}
		w := word.String()
		switch {
		case pendingWrite:
			cur.writeTargets = append(cur.writeTargets, w)
			pendingWrite = false
		case pendingDrop:
			pendingDrop = false
		default:
			cur.words = append(cur.words, w)
		}
		word.Reset()
		wordHasContent = false
	}
	flushSeg := func() {
		flushWord()
		segs = append(segs, cur)
		cur = bashSegment{}
	}

	rs := []rune(cmd)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			} else {
				word.WriteRune(c)
				wordHasContent = true
			}
			continue
		case inDouble:
			if c == '`' {
				return nil, false // command substitution
			}
			if c == '$' && i+1 < len(rs) && rs[i+1] == '(' {
				return nil, false
			}
			if c == '"' {
				inDouble = false
			} else {
				word.WriteRune(c)
				wordHasContent = true
			}
			continue
		}

		switch c {
		case '\'':
			inSingle = true
			wordHasContent = true
		case '"':
			inDouble = true
			wordHasContent = true
		case '`':
			return nil, false // command substitution
		case '$':
			if i+1 < len(rs) && rs[i+1] == '(' {
				return nil, false
			}
			word.WriteRune(c)
			wordHasContent = true
		case '\\':
			if i+1 < len(rs) {
				i++
				word.WriteRune(rs[i])
				wordHasContent = true
			}
		case ' ', '\t', '\n', '\r':
			flushWord()
		case '|':
			flushWord()
			if i+1 < len(rs) && rs[i+1] == '|' {
				i++
			}
			flushSeg()
		case '&':
			flushWord()
			if i+1 < len(rs) && rs[i+1] == '&' {
				i++
				flushSeg()
			} else {
				return nil, false // backgrounding
			}
		case ';':
			flushWord()
			flushSeg()
		case '>':
			flushWord()
			if i+1 < len(rs) && rs[i+1] == '>' {
				i++
			}
			pendingWrite = true
		case '<':
			flushWord()
			if i+1 < len(rs) && rs[i+1] == '<' {
				// heredoc: the remainder is body data, not commands.
				flushSeg()
				return segs, true
			}
			pendingDrop = true
		default:
			word.WriteRune(c)
			wordHasContent = true
		}
	}
	if inSingle || inDouble {
		return nil, false
	}
	flushSeg()
	return segs, true
}
