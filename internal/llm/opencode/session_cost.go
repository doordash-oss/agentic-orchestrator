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
	"log"
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
	adjustment, err := p.costLookup.Lookup(ctx, p.SessionID())
	if err != nil {
		return adjustment, err
	}
	p.mu.Lock()
	baseline := p.costBaseline
	p.mu.Unlock()
	return baseline.subtractChildren(adjustment), nil
}

// ReconciledUsage reports the parent session's totals from OpenCode's database
// when they exceed what the stream delivered. OpenCode streams cost and tokens
// only at turn end, and the prompt result carries just the final step's tokens,
// so an interrupted turn and multi-step turns would otherwise under-report.
func (p *Protocol) ReconciledUsage(ctx context.Context) *llm.Usage {
	p.mu.Lock()
	usage := p.usageLocked()
	streamPriced := p.costUSD > 0
	baseline := p.costBaseline
	price := p.price
	resumed := p.resumeSessionID != ""
	p.mu.Unlock()
	if resumed && !baseline.fromDB {
		return nil
	}
	totals, ok, err := p.costLookup.sessionTotals(ctx, p.SessionID())
	if err != nil {
		log.Printf("opencode: session cost reconciliation unavailable for %s: %v", p.SessionID(), err)
	}
	if err != nil || !ok {
		return nil
	}
	dbCost := max(totals.TotalCostUSD-baseline.parentCostUSD, 0)
	dbUsage := subtractUsage(totals.Usage, baseline.parentUsage)
	var streamCost float64
	if usage.CostSource == "" {
		streamCost = usage.CostUSD
	}
	moreTokens := usageTokenTotal(dbUsage) > usageTokenTotal(usage)
	if dbCost <= streamCost && !moreTokens {
		return nil
	}
	if moreTokens {
		usage.InputTokens = dbUsage.InputTokens
		usage.OutputTokens = dbUsage.OutputTokens
		usage.CacheReadInputTokens = dbUsage.CacheReadInputTokens
		usage.CacheCreationInputTokens = dbUsage.CacheCreationInputTokens
	}
	usage.CostUSD, usage.CostSource = openCodeCost(streamPriced || totals.TotalCostUSD > 0, max(dbCost, streamCost), price, usage)
	return &usage
}

func usageTokenTotal(u llm.Usage) int {
	return u.InputTokens + u.OutputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
}

func subtractUsage(a, b llm.Usage) llm.Usage {
	return llm.Usage{
		InputTokens:              max(a.InputTokens-b.InputTokens, 0),
		OutputTokens:             max(a.OutputTokens-b.OutputTokens, 0),
		CacheReadInputTokens:     max(a.CacheReadInputTokens-b.CacheReadInputTokens, 0),
		CacheCreationInputTokens: max(a.CacheCreationInputTokens-b.CacheCreationInputTokens, 0),
	}
}

// openCodeCostBaseline is the spend a resumed native session already carried
// when this process loaded it.
type openCodeCostBaseline struct {
	parentCostUSD float64
	parentUsage   llm.Usage
	children      llm.SessionCostAdjustment
	fromDB        bool
}

func (b openCodeCostBaseline) subtractChildren(a llm.SessionCostAdjustment) llm.SessionCostAdjustment {
	if len(b.children.SessionIDs) == 0 && b.children.TotalCostUSD == 0 {
		return a
	}
	prior := make(map[string]bool, len(b.children.SessionIDs))
	for _, id := range b.children.SessionIDs {
		prior[id] = true
	}
	var ids []string
	for _, id := range a.SessionIDs {
		if !prior[id] {
			ids = append(ids, id)
		}
	}
	return llm.SessionCostAdjustment{
		TotalCostUSD: max(a.TotalCostUSD-b.children.TotalCostUSD, 0),
		Usage: llm.Usage{
			InputTokens:              max(a.Usage.InputTokens-b.children.Usage.InputTokens, 0),
			OutputTokens:             max(a.Usage.OutputTokens-b.children.Usage.OutputTokens, 0),
			CacheReadInputTokens:     max(a.Usage.CacheReadInputTokens-b.children.Usage.CacheReadInputTokens, 0),
			CacheCreationInputTokens: max(a.Usage.CacheCreationInputTokens-b.children.Usage.CacheCreationInputTokens, 0),
		},
		SessionIDs: ids,
	}
}

// sessionTotals reads a session's own cumulative cost and tokens. ok is false
// when the database or the session row is unavailable.
func (l openCodeChildSessionCostLookup) sessionTotals(ctx context.Context, sessionID string) (llm.SessionCostAdjustment, bool, error) {
	out, ok, err := l.query(ctx, sessionID, openCodeSessionCostQuery)
	if err != nil || !ok {
		return llm.SessionCostAdjustment{}, false, err
	}
	totals, err := parseOpenCodeSessionCostOutput(out)
	if err != nil {
		return llm.SessionCostAdjustment{}, false, err
	}
	return totals, len(totals.SessionIDs) > 0, nil
}

func (l openCodeChildSessionCostLookup) Lookup(ctx context.Context, parentSessionID string) (llm.SessionCostAdjustment, error) {
	out, ok, err := l.query(ctx, parentSessionID, openCodeChildSessionCostQuery)
	if err != nil || !ok {
		return llm.SessionCostAdjustment{}, err
	}
	return parseOpenCodeSessionCostOutput(out)
}

// resumeBaseline reads the parent and child spend already recorded for a
// session. ok is false when the session database cannot be read.
func (l openCodeChildSessionCostLookup) resumeBaseline(ctx context.Context, sessionID string) (openCodeCostBaseline, bool, error) {
	out, ok, err := l.query(ctx, sessionID, openCodeSessionCostQuery)
	if err != nil || !ok {
		return openCodeCostBaseline{}, false, err
	}
	parent, err := parseOpenCodeSessionCostOutput(out)
	if err != nil {
		return openCodeCostBaseline{}, false, err
	}
	children, err := l.Lookup(ctx, sessionID)
	if err != nil {
		return openCodeCostBaseline{}, false, err
	}
	return openCodeCostBaseline{
		parentCostUSD: parent.TotalCostUSD,
		parentUsage:   parent.Usage,
		children:      children,
		fromDB:        true,
	}, true, nil
}

func (l openCodeChildSessionCostLookup) query(ctx context.Context, sessionID string, build func(string) string) ([]byte, bool, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || !openCodeSessionIDPattern.MatchString(sessionID) {
		return nil, false, nil
	}
	if l.dbPath == "" {
		l.dbPath = defaultOpenCodeDBPath()
	}
	if l.dbPath == "" {
		return nil, false, nil
	}
	if l.runner == nil {
		if _, err := os.Stat(l.dbPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, false, nil
			}
			return nil, false, fmt.Errorf("stat OpenCode session database: %w", err)
		}
		l.runner = sqliteCLIRunner{}
	}
	out, err := l.runner.RunSQLite(ctx, l.dbPath, build(sessionID))
	if err != nil {
		if errors.Is(err, errSQLiteUnavailable) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return out, true, nil
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

func openCodeSessionCostQuery(sessionID string) string {
	return fmt.Sprintf(`SELECT
  COALESCE(SUM(cost), 0),
  COALESCE(SUM(tokens_input), 0),
  COALESCE(SUM(tokens_output), 0),
  COALESCE(SUM(tokens_cache_read), 0),
  COALESCE(SUM(tokens_cache_write), 0),
  COALESCE(group_concat(id, ','), '')
FROM session WHERE id = %s;`, sqlStringLiteral(sessionID))
}

func sqlStringLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func parseOpenCodeSessionCostOutput(out []byte) (llm.SessionCostAdjustment, error) {
	line := strings.TrimSpace(string(out))
	if line == "" {
		return llm.SessionCostAdjustment{}, nil
	}
	fields := strings.Split(line, "\t")
	if len(fields) != 6 {
		return llm.SessionCostAdjustment{}, fmt.Errorf("unexpected OpenCode session cost output %q", line)
	}

	cost, err := strconv.ParseFloat(strings.TrimSpace(fields[0]), 64)
	if err != nil {
		return llm.SessionCostAdjustment{}, fmt.Errorf("parse session cost: %w", err)
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
		return 0, fmt.Errorf("parse session %s: %w", label, err)
	}
	return int(parsed), nil
}
