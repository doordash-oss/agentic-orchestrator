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

package opencode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

var (
	openCodeSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	errSQLiteUnavailable     = errors.New("sqlite3 is not available")
)

type sqliteRunner interface {
	RunSQLite(ctx context.Context, dbPath, query string) ([]byte, error)
}

type sqliteCLIRunner struct{}

func (sqliteCLIRunner) RunSQLite(ctx context.Context, dbPath, query string) ([]byte, error) {
	sqlitePath, err := exec.LookPath("sqlite3")
	if err != nil {
		return nil, errSQLiteUnavailable
	}
	cmd := exec.CommandContext(ctx, sqlitePath, "-readonly", "-batch", "-noheader", "-separator", "\t", dbPath, query)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("sqlite3 query failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

type openCodeChildSessionCostLookup struct {
	dbPath string
	runner sqliteRunner
}

// AdditionalSessionCost returns costs for OpenCode-managed child sessions, such
// as Task subagents. OpenCode reports the parent ACP session cost separately
// from those rows, so Agentico has to add descendants explicitly.
func (p *Protocol) AdditionalSessionCost(ctx context.Context) (llm.SessionCostAdjustment, error) {
	sessionID := p.SessionID()
	return openCodeChildSessionCostLookup{}.Lookup(ctx, sessionID)
}

func (l openCodeChildSessionCostLookup) Lookup(ctx context.Context, parentSessionID string) (llm.SessionCostAdjustment, error) {
	parentSessionID = strings.TrimSpace(parentSessionID)
	if parentSessionID == "" || !openCodeSessionIDPattern.MatchString(parentSessionID) {
		return llm.SessionCostAdjustment{}, nil
	}
	if l.dbPath == "" {
		l.dbPath = defaultOpenCodeDBPath()
	}
	if l.dbPath == "" {
		return llm.SessionCostAdjustment{}, nil
	}
	if l.runner == nil {
		if _, err := os.Stat(l.dbPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return llm.SessionCostAdjustment{}, nil
			}
			return llm.SessionCostAdjustment{}, fmt.Errorf("stat OpenCode session database: %w", err)
		}
		l.runner = sqliteCLIRunner{}
	}

	out, err := l.runner.RunSQLite(ctx, l.dbPath, openCodeChildSessionCostQuery(parentSessionID))
	if err != nil {
		if errors.Is(err, errSQLiteUnavailable) {
			return llm.SessionCostAdjustment{}, nil
		}
		return llm.SessionCostAdjustment{}, err
	}
	return parseOpenCodeChildSessionCostOutput(out)
}

func defaultOpenCodeDBPath() string {
	if dataDir := strings.TrimSpace(os.Getenv("OPENCODE_DATA_DIR")); dataDir != "" {
		return filepath.Join(dataDir, "opencode.db")
	}
	if xdgData := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdgData != "" {
		return filepath.Join(xdgData, "opencode", "opencode.db")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "share", "opencode", "opencode.db")
}

func openCodeChildSessionCostQuery(parentSessionID string) string {
	parent := sqlStringLiteral(parentSessionID)
	return fmt.Sprintf(`WITH RECURSIVE descendants(id) AS (
  SELECT id FROM session WHERE parent_id = %s
  UNION ALL
  SELECT s.id FROM session s JOIN descendants d ON s.parent_id = d.id
)
SELECT
  COALESCE(SUM(s.cost), 0),
  COALESCE(SUM(s.tokens_input), 0),
  COALESCE(SUM(s.tokens_output), 0),
  COALESCE(SUM(s.tokens_cache_read), 0),
  COALESCE(SUM(s.tokens_cache_write), 0),
  COALESCE(group_concat(s.id, ','), '')
FROM session s
WHERE s.id IN (SELECT id FROM descendants);`, parent)
}

func sqlStringLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func parseOpenCodeChildSessionCostOutput(out []byte) (llm.SessionCostAdjustment, error) {
	line := strings.TrimSpace(string(out))
	if line == "" {
		return llm.SessionCostAdjustment{}, nil
	}
	fields := strings.Split(line, "\t")
	if len(fields) != 6 {
		return llm.SessionCostAdjustment{}, fmt.Errorf("unexpected OpenCode child cost output %q", line)
	}

	cost, err := strconv.ParseFloat(strings.TrimSpace(fields[0]), 64)
	if err != nil {
		return llm.SessionCostAdjustment{}, fmt.Errorf("parse child cost: %w", err)
	}
	input, err := parseSQLiteIntField(fields[1], "input tokens")
	if err != nil {
		return llm.SessionCostAdjustment{}, err
	}
	output, err := parseSQLiteIntField(fields[2], "output tokens")
	if err != nil {
		return llm.SessionCostAdjustment{}, err
	}
	cacheRead, err := parseSQLiteIntField(fields[3], "cache read tokens")
	if err != nil {
		return llm.SessionCostAdjustment{}, err
	}
	cacheWrite, err := parseSQLiteIntField(fields[4], "cache write tokens")
	if err != nil {
		return llm.SessionCostAdjustment{}, err
	}

	var sessionIDs []string
	for _, id := range strings.Split(strings.TrimSpace(fields[5]), ",") {
		id = strings.TrimSpace(id)
		if id != "" {
			sessionIDs = append(sessionIDs, id)
		}
	}

	return llm.SessionCostAdjustment{
		TotalCostUSD: cost,
		Usage: llm.Usage{
			InputTokens:              input,
			OutputTokens:             output,
			CacheReadInputTokens:     cacheRead,
			CacheCreationInputTokens: cacheWrite,
		},
		SessionIDs: sessionIDs,
	}, nil
}

func parseSQLiteIntField(value, label string) (int, error) {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 0)
	if err != nil {
		return 0, fmt.Errorf("parse child %s: %w", label, err)
	}
	return int(parsed), nil
}
