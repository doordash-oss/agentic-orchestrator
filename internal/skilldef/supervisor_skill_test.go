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

package skilldef

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	skillsFS "github.com/doordash-oss/agentic-orchestrator/skills"
)

// supervisorSkillDir is the embedded supervisor skill's top-level directory.
const supervisorSkillDir = "supervisor"

// skillAPIToken is one METHOD + /api/v1 path named in a skill document.
type skillAPIToken struct {
	Method string
	Path   string // query string stripped
	Source string // document the token came from
}

func (tok skillAPIToken) String() string {
	return fmt.Sprintf("%s %s (%s)", tok.Method, tok.Path, tok.Source)
}

// specOperation is one method on one OpenAPI path, with the enum values of
// any path parameter whose schema declares them, keyed by segment index.
type specOperation struct {
	Method   string
	Path     string
	Segments []string
	Enums    map[int][]string
}

var (
	// fencedBlockRE removes fenced code blocks before inline backtick spans
	// are paired, so a fence's backticks never mis-pair with inline ones.
	fencedBlockRE = regexp.MustCompile("(?s)```.*?```")
	// inlineSpanRE matches one inline backtick span.
	inlineSpanRE = regexp.MustCompile("`([^`\n]+)`")
	// spanTokenRE matches a span that names a METHOD and an /api/v1 path.
	spanTokenRE = regexp.MustCompile(`^(GET|POST|PUT|PATCH|DELETE)\s+(/api/v1/\S*)`)
	// helperCallRE matches a helper invocation anywhere (inline or fenced):
	// `api`, optional flags with values, METHOD, then the optionally quoted path.
	helperCallRE = regexp.MustCompile(`\bapi\s+(?:--[a-z-]+\s+[^\s-]\S*\s+)*(GET|POST|PUT|PATCH|DELETE)\s+'?(/api/v1/[^\s'` + "`" + `]*)`)
)

// extractSkillAPITokens returns every METHOD + /api/v1 path named in text:
// backticked `METHOD /api/v1/...` spans and helper invocations. Query strings
// are stripped. Duplicates are kept so callers can attribute each source.
func extractSkillAPITokens(source, text string) []skillAPIToken {
	var tokens []skillAPIToken
	add := func(method, rawPath string) {
		p, _, _ := strings.Cut(rawPath, "?")
		tokens = append(tokens, skillAPIToken{Method: method, Path: p, Source: source})
	}
	inline := fencedBlockRE.ReplaceAllString(text, "")
	for _, span := range inlineSpanRE.FindAllStringSubmatch(inline, -1) {
		if m := spanTokenRE.FindStringSubmatch(strings.TrimSpace(span[1])); m != nil {
			add(m[1], m[2])
		}
	}
	for _, m := range helperCallRE.FindAllStringSubmatch(text, -1) {
		add(m[1], m[2])
	}
	return tokens
}

// isTemplateSegment reports whether a path segment is a `{param}` placeholder.
func isTemplateSegment(seg string) bool {
	return len(seg) >= 2 && strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}")
}

// matches reports whether a skill path resolves to op the way the router
// would: segment counts agree, literal segments are equal, a `{anything}`
// skill segment sits on a spec `{param}` segment, and a literal skill segment
// on a spec parameter is one of that parameter's enum values when it has any.
func (op specOperation) matches(method, p string) bool {
	if method != op.Method {
		return false
	}
	segs := strings.Split(strings.Trim(p, "/"), "/")
	if len(segs) != len(op.Segments) {
		return false
	}
	for i, seg := range segs {
		specSeg := op.Segments[i]
		switch {
		case isTemplateSegment(seg):
			if !isTemplateSegment(specSeg) {
				return false
			}
		case isTemplateSegment(specSeg):
			if enum := op.Enums[i]; len(enum) > 0 && !slices.Contains(enum, seg) {
				return false
			}
		case seg != specSeg:
			return false
		}
	}
	return true
}

// unresolvedSkillAPITokens returns every token that matches no operation.
func unresolvedSkillAPITokens(tokens []skillAPIToken, ops []specOperation) []skillAPIToken {
	var bad []skillAPIToken
	for _, tok := range tokens {
		if !slices.ContainsFunc(ops, func(op specOperation) bool { return op.matches(tok.Method, tok.Path) }) {
			bad = append(bad, tok)
		}
	}
	return bad
}

type specParamDoc struct {
	Ref    string `yaml:"$ref"`
	Name   string `yaml:"name"`
	In     string `yaml:"in"`
	Schema struct {
		Ref  string   `yaml:"$ref"`
		Enum []string `yaml:"enum"`
	} `yaml:"schema"`
}

// loadSpecOperations parses api/openapi.yaml into its operations, resolving
// path-parameter enums through component parameter and schema references.
func loadSpecOperations(t *testing.T) []specOperation {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read api/openapi.yaml: %v", err)
	}
	var spec struct {
		Paths      map[string]map[string]yaml.Node `yaml:"paths"`
		Components struct {
			Parameters map[string]specParamDoc `yaml:"parameters"`
			Schemas    map[string]struct {
				Enum []string `yaml:"enum"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(data, &spec); err != nil {
		t.Fatalf("parse api/openapi.yaml: %v", err)
	}
	resolve := func(p specParamDoc) specParamDoc {
		if ref, ok := strings.CutPrefix(p.Ref, "#/components/parameters/"); ok {
			p = spec.Components.Parameters[ref]
		}
		if ref, ok := strings.CutPrefix(p.Schema.Ref, "#/components/schemas/"); ok && len(p.Schema.Enum) == 0 {
			p.Schema.Enum = spec.Components.Schemas[ref].Enum
		}
		return p
	}
	decodeParams := func(node yaml.Node) []specParamDoc {
		var params []specParamDoc
		if err := node.Decode(&params); err != nil {
			t.Fatalf("decode parameters: %v", err)
		}
		return params
	}

	var ops []specOperation
	for specPath, item := range spec.Paths {
		var shared []specParamDoc
		if node, ok := item["parameters"]; ok {
			shared = decodeParams(node)
		}
		segments := strings.Split(strings.Trim(specPath, "/"), "/")
		for key, node := range item {
			method := strings.ToUpper(key)
			switch method {
			case "GET", "POST", "PUT", "PATCH", "DELETE":
			default:
				continue
			}
			var opDoc struct {
				Parameters yaml.Node `yaml:"parameters"`
			}
			if err := node.Decode(&opDoc); err != nil {
				t.Fatalf("decode %s %s: %v", method, specPath, err)
			}
			params := slices.Clone(shared)
			if opDoc.Parameters.Kind != 0 {
				params = append(params, decodeParams(opDoc.Parameters)...)
			}
			enums := map[int][]string{}
			for _, raw := range params {
				p := resolve(raw)
				if p.In != "path" || len(p.Schema.Enum) == 0 {
					continue
				}
				if i := slices.Index(segments, "{"+p.Name+"}"); i >= 0 {
					enums[i] = p.Schema.Enum
				}
			}
			ops = append(ops, specOperation{Method: method, Path: specPath, Segments: segments, Enums: enums})
		}
	}
	if len(ops) == 0 {
		t.Fatal("api/openapi.yaml has no operations")
	}
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].Path != ops[j].Path {
			return ops[i].Path < ops[j].Path
		}
		return ops[i].Method < ops[j].Method
	})
	return ops
}

// supervisorSkillTokens extracts the API tokens from every markdown file in
// the embedded supervisor skill tree.
func supervisorSkillTokens(t *testing.T) []skillAPIToken {
	t.Helper()
	var tokens []skillAPIToken
	err := fs.WalkDir(skillsFS.FS, supervisorSkillDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || path.Ext(p) != ".md" {
			return nil
		}
		data, err := fs.ReadFile(skillsFS.FS, p)
		if err != nil {
			return err
		}
		tokens = append(tokens, extractSkillAPITokens(p, string(data))...)
		return nil
	})
	if err != nil {
		t.Fatalf("walking embedded %s skill: %v", supervisorSkillDir, err)
	}
	return tokens
}

// excludedFromSupervisorSkill reports routes the skill must never name: the
// supervisor's own namespace and the octet-stream uploads route.
func excludedFromSupervisorSkill(p string) bool {
	for _, prefix := range []string{"/api/v1/supervisor", "/api/v1/uploads"} {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

// normalizedTemplatePath replaces every `{param}` segment with `{}`.
func normalizedTemplatePath(p string) string {
	segs := strings.Split(p, "/")
	for i, seg := range segs {
		if isTemplateSegment(seg) {
			segs[i] = "{}"
		}
	}
	return strings.Join(segs, "/")
}

// TestSupervisorSkillAPIPathsExistInSpec pins every REST path the supervisor
// skill names to an operation in api/openapi.yaml, so the skill cannot drift
// from the API it teaches.
func TestSupervisorSkillAPIPathsExistInSpec(t *testing.T) {
	ops := loadSpecOperations(t)
	tokens := supervisorSkillTokens(t)
	if len(tokens) == 0 {
		t.Fatal("no METHOD /api/v1/... tokens extracted from the supervisor skill; the extractor is broken")
	}

	for _, tok := range unresolvedSkillAPITokens(tokens, ops) {
		t.Errorf("supervisor skill names an operation missing from api/openapi.yaml: %s", tok)
	}

	normalized := map[string]bool{}
	for _, tok := range tokens {
		normalized[tok.Method+" "+normalizedTemplatePath(tok.Path)] = true
		if excludedFromSupervisorSkill(tok.Path) {
			t.Errorf("supervisor skill names an excluded route: %s", tok)
		}
	}
	t.Logf("extracted %d API tokens (%d distinct operations) from the supervisor skill", len(tokens), len(normalized))
	for _, want := range []string{
		"POST /api/v1/features",
		"POST /api/v1/features/{}/actions/start",
	} {
		if !normalized[want] {
			t.Errorf("supervisor skill tokens missing %q; extracted set may be vacuous", want)
		}
	}

	t.Run("bogus fixture is reported", func(t *testing.T) {
		fixture := strings.Join([]string{
			"- `GET /api/v1/features` exists.",
			"- `POST /api/v1/features/{feature_id}/actions/frobnicate` is not a spec action.",
			"- `GET /api/v1/bogus/path` is not a spec path.",
			"- `DELETE /api/v1/features` uses the wrong method.",
			"```bash",
			`"$AGENTICO_BIN" api --timeout 5s GET '/api/v1/nowhere?x=1'`,
			"```",
		}, "\n")
		got := unresolvedSkillAPITokens(extractSkillAPITokens("fixture.md", fixture), ops)
		var paths []string
		for _, tok := range got {
			paths = append(paths, tok.Method+" "+tok.Path)
		}
		want := []string{
			"POST /api/v1/features/{feature_id}/actions/frobnicate",
			"GET /api/v1/bogus/path",
			"DELETE /api/v1/features",
			"GET /api/v1/nowhere",
		}
		if !slices.Equal(paths, want) {
			t.Fatalf("unresolved fixture tokens = %q, want %q", paths, want)
		}
	})
}

// TestSupervisorAPIReferenceCoversOperatorSurface asserts the api-reference
// names every spec operation outside the supervisor namespace and uploads.
func TestSupervisorAPIReferenceCoversOperatorSurface(t *testing.T) {
	ops := loadSpecOperations(t)
	refPath := path.Join(supervisorSkillDir, "api-reference.md")
	data, err := fs.ReadFile(skillsFS.FS, refPath)
	if err != nil {
		t.Fatalf("reading embedded %s: %v", refPath, err)
	}
	tokens := extractSkillAPITokens(refPath, string(data))
	for _, op := range ops {
		if excludedFromSupervisorSkill(op.Path) {
			continue
		}
		covered := slices.ContainsFunc(tokens, func(tok skillAPIToken) bool { return op.matches(tok.Method, tok.Path) })
		if !covered {
			t.Errorf("%s does not document %s %s", refPath, op.Method, op.Path)
		}
	}
}

// TestEmbeddedSupervisorSkillPresent asserts the supervisor skill's core,
// references and user guide ship in the embedded FS and the retired chat
// skill does not.
func TestEmbeddedSupervisorSkillPresent(t *testing.T) {
	for _, rel := range []string{
		"SKILL.md",
		"api-reference.md",
		"recipes.md",
		"environment.md",
		"user-guide/index.md",
	} {
		p := path.Join(supervisorSkillDir, rel)
		data, err := fs.ReadFile(skillsFS.FS, p)
		if err != nil {
			t.Errorf("embedded %s missing: %v", p, err)
			continue
		}
		if len(data) == 0 {
			t.Errorf("embedded %s is empty", p)
		}
	}
	if _, err := fs.Stat(skillsFS.FS, "chat"); err == nil {
		t.Error("embedded FS still ships the retired chat skill")
	}

	core, err := fs.ReadFile(skillsFS.FS, path.Join(supervisorSkillDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("reading supervisor SKILL.md: %v", err)
	}
	for _, link := range []string{"](api-reference.md)", "](recipes.md)", "](environment.md)", "](user-guide/index.md)"} {
		if !strings.Contains(string(core), link) {
			t.Errorf("supervisor SKILL.md does not link %q", link)
		}
	}

	retiredChat := regexp.MustCompile(`Ask Me Anything|\bAMA\b`)
	err = fs.WalkDir(skillsFS.FS, path.Join(supervisorSkillDir, "user-guide"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(skillsFS.FS, p)
		if err != nil {
			return err
		}
		if loc := retiredChat.FindIndex(data); loc != nil {
			t.Errorf("%s still names the retired chat: %q", p, data[loc[0]:loc[1]])
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking supervisor user guide: %v", err)
	}
}
