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
	"database/sql"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/llm"
)

// openCodeTestDB creates a WAL-mode database in OpenCode's session table
// shape and returns a writer that stays open, so uncheckpointed WAL frames
// remain for readers.
func openCodeTestDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	w, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=wal_autocheckpoint(0)")
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	w.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = w.Close() })
	mustExec(t, w, `CREATE TABLE session (
  id text PRIMARY KEY,
  parent_id text,
  cost real DEFAULT 0 NOT NULL,
  tokens_input integer DEFAULT 0 NOT NULL,
  tokens_output integer DEFAULT 0 NOT NULL,
  tokens_reasoning integer DEFAULT 0 NOT NULL,
  tokens_cache_read integer DEFAULT 0 NOT NULL,
  tokens_cache_write integer DEFAULT 0 NOT NULL
)`)
	mustExec(t, w, `INSERT INTO session VALUES
  ('ses_p', NULL, 0.14521732, 53917, 452, 0, 188092, 0),
  ('ses_c', 'ses_p', 0.25, 100, 20, 0, 300, 4),
  ('ses_g', 'ses_c', 0.5, 10, 2, 0, 30, 0)`)
	return path, w
}

func mustExec(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.Exec(query); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func TestSQLiteDriverReadsOpenCodeDatabase(t *testing.T) {
	tests := []struct {
		name      string
		session   string
		write     func(t *testing.T, w *sql.DB)
		wantOK    bool
		wantCost  float64
		wantUsage llm.Usage
	}{
		{
			name: "committed row", session: "ses_p", wantOK: true, wantCost: 0.14521732,
			wantUsage: llm.Usage{InputTokens: 53917, OutputTokens: 452, CacheReadInputTokens: 188092},
		},
		{
			name: "uncheckpointed WAL commit is visible", session: "ses_p", wantOK: true, wantCost: 0.3,
			write: func(t *testing.T, w *sql.DB) {
				mustExec(t, w, "UPDATE session SET cost = 0.3, tokens_input = 60000 WHERE id = 'ses_p'")
			},
			wantUsage: llm.Usage{InputTokens: 60000, OutputTokens: 452, CacheReadInputTokens: 188092},
		},
		{
			name: "open write transaction does not block", session: "ses_p", wantOK: true, wantCost: 0.14521732,
			write: func(t *testing.T, w *sql.DB) {
				tx, err := w.Begin()
				if err != nil {
					t.Fatalf("begin: %v", err)
				}
				t.Cleanup(func() { _ = tx.Rollback() })
				if _, err := tx.Exec("UPDATE session SET cost = 9 WHERE id = 'ses_p'"); err != nil {
					t.Fatalf("update: %v", err)
				}
			},
			wantUsage: llm.Usage{InputTokens: 53917, OutputTokens: 452, CacheReadInputTokens: 188092},
		},
		{
			name:    "missing row",
			session: "ses_missing",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, w := openCodeTestDB(t)
			if tt.write != nil {
				tt.write(t, w)
			}
			if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
				t.Fatalf("expected uncheckpointed WAL content, stat: %v", err)
			}
			lookup := openCodeChildSessionCostLookup{dbPath: path}
			got, ok, err := lookup.sessionTotals(context.Background(), tt.session)
			if err != nil {
				t.Fatalf("sessionTotals: %v", err)
			}
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if math.Abs(got.TotalCostUSD-tt.wantCost) > 1e-9 || got.Usage != tt.wantUsage {
				t.Errorf("totals = %+v, want cost %v usage %+v", got, tt.wantCost, tt.wantUsage)
			}
		})
	}
}

func TestSQLiteDriverSumsChildSessions(t *testing.T) {
	path, _ := openCodeTestDB(t)
	got, err := openCodeChildSessionCostLookup{dbPath: path}.Lookup(context.Background(), "ses_p")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	want := llm.Usage{InputTokens: 110, OutputTokens: 22, CacheReadInputTokens: 330, CacheCreationInputTokens: 4}
	if math.Abs(got.TotalCostUSD-0.75) > 1e-9 || got.Usage != want || strings.Join(got.SessionIDs, ",") != "ses_c,ses_g" {
		t.Errorf("children = %+v, want cost 0.75, usage %+v, ids ses_c,ses_g", got, want)
	}
}

func TestSQLiteDriverIsReadOnly(t *testing.T) {
	path, _ := openCodeTestDB(t)
	if _, err := (sqliteDriverRunner{}).RunSQLite(context.Background(), path, "DELETE FROM session"); err == nil {
		t.Fatal("write through the reader succeeded, want read-only error")
	}
}

func TestSQLiteDriverMissingDatabaseFailsSoft(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.db")
	_, ok, err := openCodeChildSessionCostLookup{dbPath: path}.sessionTotals(context.Background(), "ses_p")
	if err != nil || ok {
		t.Fatalf("sessionTotals = ok %v, err %v; want soft miss", ok, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reader created the database: %v", err)
	}
}
