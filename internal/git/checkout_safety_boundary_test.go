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

package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// staticZRunner returns a fixed stdout for every command, standing in for
// what the limited drain writer captured from a real git process.
func staticZRunner(stdout string) BranchProbeRunnerFunc {
	return func(ctx context.Context, repoPath string, args []string, diagnosticLimit int) BranchProbeCommandResult {
		return BranchProbeCommandResult{Stdout: stdout, ExitCode: 0}
	}
}

// boundaryListing builds an NUL-record path listing whose first 4,096
// records are 255-byte names (256 bytes each with the NUL), filling exactly
// checkoutSafetyOutputBound bytes. Extra records follow after that boundary,
// exactly where the bounded drain writer truncates the captured prefix.
func boundaryListing(extraRecords ...string) string {
	var b strings.Builder
	for i := 0; i < 4096; i++ {
		b.WriteString(fmt.Sprintf("%04d", i))
		b.WriteString(strings.Repeat("a", 251))
		b.WriteByte(0)
	}
	for _, record := range extraRecords {
		b.WriteString(record)
		b.WriteByte(0)
	}
	return b.String()
}

func TestBoundedZOutputAcceptsCompleteListingAtExactBound(t *testing.T) {
	// A genuinely complete listing of exactly bound bytes must stay
	// accepted: the extra-byte capture must not reject valid repositories.
	listing := boundaryListing()
	if len(listing) != checkoutSafetyOutputBound {
		t.Fatalf("fixture listing = %d bytes; want exactly %d", len(listing), checkoutSafetyOutputBound)
	}
	output, err := boundedZOutput(context.Background(), t.TempDir(), []string{"ls-files", "--others", "--ignored", "--exclude-standard", "-z"}, OriginCheckOptions{
		Runner: staticZRunner(listing),
	})
	if err != nil {
		t.Fatalf("boundedZOutput: %v", err)
	}
	if output != listing {
		t.Fatal("boundedZOutput altered a complete listing")
	}
}

func TestBoundedZOutputRejectsOverflowAtRecordBoundary(t *testing.T) {
	// Regression: the captured prefix ends exactly on a NUL record boundary
	// at the bound, with the colliding path in the discarded remainder. The
	// fake returns the first bound+1 bytes of the full stream, which is what
	// the limited drain writer now captures; the previous logic accepted the
	// bound-byte prefix because it ended in NUL.
	full := boundaryListing("added.txt")
	if len(full) != checkoutSafetyOutputBound+10 {
		t.Fatalf("full listing = %d bytes; want %d", len(full), checkoutSafetyOutputBound+10)
	}
	captured := full[:checkoutSafetyOutputBound+1]
	if strings.HasSuffix(captured, "\x00") {
		t.Fatal("test setup: captured prefix must end mid-record")
	}
	_, err := boundedZOutput(context.Background(), t.TempDir(), []string{"ls-files", "--others", "--ignored", "--exclude-standard", "-z"}, OriginCheckOptions{
		Runner: staticZRunner(captured),
	})
	if err == nil {
		t.Fatal("boundedZOutput accepted a boundary-truncated capture; want a fail-closed error")
	}
	if !strings.Contains(err.Error(), "exceeded the bounded output size") {
		t.Fatalf("err = %v; want the bounded-output-size error", err)
	}
}

func TestBoundedZOutputRejectsMidRecordTruncation(t *testing.T) {
	// Truncation mid-record (no trailing NUL) fails closed for both the
	// ignored-path and the incoming-diff inspection.
	for _, args := range [][]string{
		{"ls-files", "--others", "--ignored", "--exclude-standard", "-z"},
		{"diff", "--name-only", "--no-renames", "--diff-filter=AMCT", "-z", "old", "new"},
	} {
		_, err := boundedZOutput(context.Background(), t.TempDir(), args, OriginCheckOptions{
			Runner: staticZRunner("path/one\x00path/two-no-terminator"),
		})
		if err == nil {
			t.Fatalf("%s: boundedZOutput accepted output without a trailing NUL", args[0])
		}
		if !strings.Contains(err.Error(), "was truncated") {
			t.Fatalf("%s: err = %v; want the truncation error", args[0], err)
		}
	}
}

func TestUpdateSourceFromOriginalCheckoutIgnoredListingBoundaryOverflowRefuses(t *testing.T) {
	fx := newOriginalCheckoutFixture(t)
	expected := fx.originalExpectation(t, LocalSourceModeDefault)

	// The remote already adds tracked added.txt (fixture). Ignore all
	// untracked content in the original checkout, then fill the ignored
	// listing to exactly the capture bound with 4,096 ignored files of
	// 255-byte names (256 bytes per NUL-terminated record), and place the
	// colliding added.txt after that record boundary. Under the previous
	// capture behavior the collision was truncated away and the update
	// overwrote the ignored file; it must now fail closed.
	if err := os.WriteFile(filepath.Join(fx.repo, ".git", "info", "exclude"), []byte("*\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4096; i++ {
		name := fmt.Sprintf("%04d", i) + strings.Repeat("a", 251)
		if err := os.WriteFile(filepath.Join(fx.repo, name), []byte("collateral"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	precious := filepath.Join(fx.repo, "added.txt")
	if err := os.WriteFile(precious, []byte("precious local data\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	listing := runGitUpdateTest(t, fx.repo, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
	if len(listing) <= checkoutSafetyOutputBound {
		t.Fatalf("ignored listing = %d bytes; want a listing exceeding the capture bound", len(listing))
	}
	if !strings.HasPrefix(listing[checkoutSafetyOutputBound:], "added.txt") {
		t.Fatalf("collision target must sit after the %d-byte record boundary", checkoutSafetyOutputBound)
	}

	outcome, err := UpdateSourceFromOrigin(context.Background(), fx.repo, expected, SourceUpdateOptions{})
	if err == nil {
		t.Fatalf("update outcome = %+v; want an error when ignored-path inspection overflows", outcome)
	}
	if !errors.Is(err, ErrSourceUpdateUnavailable) {
		t.Fatalf("err = %v; want ErrSourceUpdateUnavailable", err)
	}
	if outcome.Result == SourceUpdateUpdated {
		t.Fatal("update must not proceed when inspection is truncated")
	}
	if got := gitUpdateSHA(t, fx.repo, "refs/heads/main"); got != expected.ExpectedLocalSHA {
		t.Fatalf("refs/heads/main = %s; want the displayed tip preserved", got)
	}
	got, readErr := os.ReadFile(precious)
	if readErr != nil || string(got) != "precious local data\n" {
		t.Fatalf("added.txt = %q err=%v; want ignored local bytes preserved", got, readErr)
	}
	for _, probe := range []int{0, 2047, 4095} {
		name := fmt.Sprintf("%04d", probe) + strings.Repeat("a", 251)
		content, readErr := os.ReadFile(filepath.Join(fx.repo, name))
		if readErr != nil || string(content) != "collateral" {
			t.Fatalf("filler %s = %q err=%v; want ignored local bytes preserved", name, content, readErr)
		}
	}
	if got := runGitUpdateTest(t, fx.repo, "status", "--porcelain"); got != "" {
		t.Fatalf("status --porcelain = %q; want the checkout to remain clean", got)
	}
}
