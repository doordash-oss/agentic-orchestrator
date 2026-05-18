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

package feature

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// discoverCarriedPhasePlanDirs scans `sealedRunDir` for `phase-*`
// subdirectories and returns the relative paths of each `phase-NN/plan/`
// subdirectory that exists on disk. Used ONLY for rewind-to-PhaseImplement.
// Returns entries in lexicographic order for deterministic test assertions.
//
// Returns (nil, nil) if the sealed run directory does not exist (treated as
// "nothing to carry", not an error — matches the copy helper's missing-
// source behavior).
func discoverCarriedPhasePlanDirs(sealedRunDir string) ([]string, error) {
	entries, err := os.ReadDir(sealedRunDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning sealed run dir: %w", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "phase-") {
			continue
		}
		planSub := filepath.Join(e.Name(), "plan")
		if info, err := os.Stat(filepath.Join(sealedRunDir, planSub)); err == nil && info.IsDir() {
			out = append(out, planSub)
		}
	}
	sort.Strings(out)
	return out, nil
}

// discoverCarriedPhaseFiles scans immediate subdirectories of `sealedRunDir`
// and returns any root-level contract/history files that should survive a
// rewind to implementation. The scan is file-driven so new phase or cycle
// directory names do not require code changes here.
func discoverCarriedPhaseFiles(sealedRunDir string) ([]string, error) {
	entries, err := os.ReadDir(sealedRunDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning sealed run dir for phase files: %w", err)
	}

	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		for _, fileName := range []string{"testing-contract.yaml"} {
			rel := filepath.Join(e.Name(), fileName)
			if info, err := os.Stat(filepath.Join(sealedRunDir, rel)); err == nil && !info.IsDir() {
				out = append(out, rel)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func discoverPartialImplementCarryForward(sealedRunDir string, targetRoadmapPhase int) ([]string, error) {
	entries, err := os.ReadDir(sealedRunDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning sealed run dir for partial rewind: %w", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		phase, ok := roadmapPhaseDirNumber(e.Name())
		if !ok {
			continue
		}
		if phase <= targetRoadmapPhase {
			planSub := filepath.Join(e.Name(), "plan")
			if info, err := os.Stat(filepath.Join(sealedRunDir, planSub)); err == nil && info.IsDir() {
				out = append(out, planSub)
			}
		}
		if phase < targetRoadmapPhase {
			implSub := filepath.Join(e.Name(), "implement")
			if info, err := os.Stat(filepath.Join(sealedRunDir, implSub)); err == nil && info.IsDir() {
				out = append(out, implSub)
			}
			contract := filepath.Join(e.Name(), "testing-contract.yaml")
			if info, err := os.Stat(filepath.Join(sealedRunDir, contract)); err == nil && !info.IsDir() {
				out = append(out, contract)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func roadmapPhaseDirNumber(name string) (int, bool) {
	raw, ok := strings.CutPrefix(name, "phase-")
	if !ok || raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// copyRunArtifactsForward deep-copies each relative file or directory in
// `dirs` from `sealedRunDir` to `newRunDir`. Missing source paths are
// silently skipped (not an error — matches the "pipelines with different
// phase sets" reality described in the roadmap).
//
// File mode is preserved. Symlinks are copied as regular files (via
// os.ReadFile / os.WriteFile); Agentic does not use symlinks in run
// directories, so the simpler behavior is acceptable.
func copyRunArtifactsForward(sealedRunDir, newRunDir string, dirs []string) error {
	for _, rel := range dirs {
		src := filepath.Join(sealedRunDir, rel)
		dst := filepath.Join(newRunDir, rel)
		info, err := os.Stat(src)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("stat carry-forward source %s: %w", rel, err)
		}
		if !info.IsDir() {
			if err := copyRegularFile(src, dst); err != nil {
				return fmt.Errorf("copying %s: %w", rel, err)
			}
			continue
		}
		if err := copyTree(src, dst); err != nil {
			return fmt.Errorf("copying %s: %w", rel, err)
		}
	}
	return nil
}

// copyTree is a minimal recursive directory copy using filepath.WalkDir. It
// preserves file modes and creates intermediate directories as needed.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			return os.MkdirAll(target, info.Mode().Perm())
		}
		return copyRegularFile(path, target)
	})
}

// copyRegularFile streams src to dst, preserving mode.
func copyRegularFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir dest parent: %w", err)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("open dest: %w", err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy bytes: %w", err)
	}
	return nil
}

// carryForwardArtifactKey reports whether an Artifacts map key survives
// carry-forward for the given rewind target. Keys correspond to the static
// phase-dir names plus `phase-N-plan` patterns produced by the per-phase
// planning loop.
func carryForwardArtifactKey(key string, target Phase) bool {
	switch target {
	case PhaseResearch:
		return key == "inquire"
	case PhaseDesign:
		return key == "inquire" || key == "research"
	case PhasePlan:
		return key == "inquire" || key == "research" || key == "design"
	case PhaseImplement:
		switch key {
		case "inquire", "research", "design", "roadmap", "plan":
			return true
		}
		return strings.HasPrefix(key, "phase-") && strings.HasSuffix(key, "-plan")
	}
	return false
}

func carryForwardArtifactKeyForPartialImplement(key string, targetRoadmapPhase int) bool {
	switch key {
	case "inquire", "research", "design", "roadmap":
		return true
	}
	if phase, kind, ok := roadmapArtifactKey(key); ok {
		switch kind {
		case "plan":
			return phase <= targetRoadmapPhase
		case "implement", "impl":
			return phase < targetRoadmapPhase
		}
	}
	return false
}

func roadmapArtifactKey(key string) (phase int, kind string, ok bool) {
	if !strings.HasPrefix(key, "phase-") {
		return 0, "", false
	}
	rest := strings.TrimPrefix(key, "phase-")
	nRaw, suffix, found := strings.Cut(rest, "-")
	if !found {
		return 0, "", false
	}
	n, err := strconv.Atoi(nRaw)
	if err != nil || n <= 0 || suffix == "" {
		return 0, "", false
	}
	return n, suffix, true
}

// normalizeToRunRelative converts an absolute path that points under
// `sealedRunDir` to its run-relative suffix (e.g.
// `/state-dir/<id>/runs/run-001/inquire/out.md` + `sealedRunDir =
// /state-dir/<id>/runs/run-001` → `inquire/out.md`). Values that are already
// run-relative, or that point outside `sealedRunDir`, pass through
// unchanged. This is the normalization step that gives the new run's
// Artifacts map run-relative values per the roadmap's State Model.
func normalizeToRunRelative(abs, sealedRunDir string) string {
	if sealedRunDir == "" {
		return abs
	}
	prefix := sealedRunDir + string(filepath.Separator)
	if rel, ok := strings.CutPrefix(abs, prefix); ok {
		return rel
	}
	return abs
}

// carryForwardArtifactsMap returns a fresh map containing only the entries
// from `old` whose keys satisfy carryForwardArtifactKey for `target`, with
// each surviving value normalized to run-relative form via
// normalizeToRunRelative(v, sealedRunDir). Returns nil (not empty map)
// when no entries survive, so the YAML `omitempty` tag on Run.Artifacts
// keeps serialization clean for targets that carry nothing (PhaseInquire).
// The `sealedRunDir` argument is the absolute path of the run being
// sealed (as returned by Store.RunDir); callers pass the pre-seal active
// run's directory.
func carryForwardArtifactsMap(old map[string]string, target Phase, sealedRunDir string) map[string]string {
	return carryForwardArtifactsMapForRequest(old, target, sealedRunDir, 0)
}

func carryForwardArtifactsMapForRequest(old map[string]string, target Phase, sealedRunDir string, targetRoadmapPhase int) map[string]string {
	if len(old) == 0 {
		return nil
	}
	out := make(map[string]string, len(old))
	for k, v := range old {
		keep := carryForwardArtifactKey(k, target)
		if target == PhaseImplement && targetRoadmapPhase > 0 {
			keep = carryForwardArtifactKeyForPartialImplement(k, targetRoadmapPhase)
		}
		if keep {
			out[k] = normalizeToRunRelative(v, sealedRunDir)
		}
	}
	if target == PhaseImplement && targetRoadmapPhase > 0 {
		if _, hadPlanAlias := old["plan"]; hadPlanAlias {
			targetPlanKey := fmt.Sprintf("phase-%d-plan", targetRoadmapPhase)
			if targetPlan, ok := out[targetPlanKey]; ok {
				out["plan"] = targetPlan
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func carryForwardRoadmapPhaseCommitAnchors(old map[int]map[string]string, targetRoadmapPhase int) map[int]map[string]string {
	if len(old) == 0 || targetRoadmapPhase <= 1 {
		return nil
	}
	out := make(map[int]map[string]string)
	for phase, anchors := range old {
		if phase >= targetRoadmapPhase || len(anchors) == 0 {
			continue
		}
		copied := make(map[string]string, len(anchors))
		for repo, sha := range anchors {
			copied[repo] = sha
		}
		out[phase] = copied
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
