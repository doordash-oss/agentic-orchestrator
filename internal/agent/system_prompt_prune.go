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
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

// DefaultSystemPromptPruneGrace protects prompt directories created by a
// session that is still launching.
const DefaultSystemPromptPruneGrace = 5 * time.Minute

// ErrProcessListUnavailable reports that live process command lines could
// not be read, so no prompt directory was pruned.
var ErrProcessListUnavailable = errors.New("process list unavailable")

// SystemPromptPruneOpts tunes PruneOrphanedSystemPrompts. Zero values use the
// default grace period, the real process list, and the wall clock.
type SystemPromptPruneOpts struct {
	Grace       time.Duration
	ProcessArgs func() ([]string, error)
	Now         func() time.Time
}

// PruneOrphanedSystemPrompts removes system prompt directories under the
// provider state of featureStateDir that no live process references on its
// command line and that are older than the grace period. It returns the
// number removed. When the process list cannot be read nothing is removed.
func PruneOrphanedSystemPrompts(featureStateDir string, opts SystemPromptPruneOpts) (int, error) {
	stateDir := providerStateDir(featureStateDir)
	if stateDir == "" {
		return 0, nil
	}
	base := filepath.Join(stateDir, systemPromptsDirName)
	entries, err := os.ReadDir(base)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading system prompt directory: %w", err)
	}
	if opts.Grace <= 0 {
		opts.Grace = DefaultSystemPromptPruneGrace
	}
	if opts.ProcessArgs == nil {
		opts.ProcessArgs = liveProcessArgs
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	var candidates []string
	cutoff := opts.Now().Add(-opts.Grace)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if ok, _ := filepath.Match(systemPromptDirPattern, e.Name()); !ok {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		candidates = append(candidates, filepath.Join(base, e.Name()))
	}
	if len(candidates) == 0 {
		return 0, nil
	}
	args, err := opts.ProcessArgs()
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrProcessListUnavailable, err)
	}
	removed := 0
	for _, dir := range candidates {
		if referencedByAny(args, dir) {
			continue
		}
		if err := os.RemoveAll(dir); err == nil {
			removed++
		}
	}
	return removed, nil
}

func referencedByAny(args []string, dir string) bool {
	for _, a := range args {
		if strings.Contains(a, dir) {
			return true
		}
	}
	return false
}

// liveProcessArgs returns one space-joined command line per visible process.
func liveProcessArgs() ([]string, error) {
	if runtime.GOOS == "linux" {
		return procCmdlines()
	}
	out, err := NewExecCommandRunner().Run(context.Background(), "ps", []string{"-axww", "-o", "args="}, ports.CommandOpts{})
	if err != nil {
		return nil, fmt.Errorf("listing processes: %w", err)
	}
	return strings.Split(string(out), "\n"), nil
}

func procCmdlines() ([]string, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("reading /proc: %w", err)
	}
	var args []string
	for _, e := range entries {
		name := e.Name()
		if name == "" || name[0] < '0' || name[0] > '9' {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", name, "cmdline"))
		if err != nil || len(data) == 0 {
			continue
		}
		args = append(args, string(bytes.ReplaceAll(data, []byte{0}, []byte{' '})))
	}
	if len(args) == 0 {
		return nil, errors.New("no readable process command lines")
	}
	return args, nil
}
