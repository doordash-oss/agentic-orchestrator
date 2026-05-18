package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/doordash-oss/agentic-orchestrator/internal/config"
	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
)

// TestDetailDesignSnapshot renders the detail view for a feature currently in
// the Design phase and writes the rendered text to the iteration evidence dir
// when AGENTIC_SNAPSHOT_PATH is set. Without that env var, it still asserts
// that the rendered view shows the canonical "Design" label between Research
// and Plan, so the behavior is pinned for future runs.
func TestDetailDesignSnapshot(t *testing.T) {
	t.Parallel()
	f := &feature.Feature{
		ID:           "feat-design-snap",
		Name:         "Design Snapshot",
		Slug:         "design-snapshot",
		Status:       feature.StatusDesigning,
		CurrentPhase: feature.PhaseDesign,
		Repos:        []feature.FeatureRepo{{Name: "agentic-orchestrator", Path: "/tmp/agentic-orchestrator"}},
		Models: config.ModelConfig{
			Research:       "opus",
			Planning:       "opus",
			Implementation: "opus",
			Review:         "opus",
		},
	}
	m := NewDetailModel(f, "")
	view := m.View()

	for _, want := range []string{"Research", "Design", "Planning"} {
		if !strings.Contains(view, want) {
			t.Errorf("expected detail view to contain %q; got:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Brainstorm") {
		t.Errorf("did not expect legacy label \"Brainstorm\" in detail view; got:\n%s", view)
	}

	if out := os.Getenv("AGENTIC_SNAPSHOT_PATH"); out != "" {
		if err := os.WriteFile(out, []byte(view), 0o644); err != nil {
			t.Fatalf("write snapshot: %v", err)
		}
	}
}
