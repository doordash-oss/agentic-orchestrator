package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/doordash-oss/agentic-orchestrator/internal/feature"
	"github.com/doordash-oss/agentic-orchestrator/internal/observe"
	"github.com/doordash-oss/agentic-orchestrator/internal/permission"
	"github.com/doordash-oss/agentic-orchestrator/internal/ports"
)

func main() {
	stateDir, _ := os.MkdirTemp("", "auto_review_behavioral")
	defer os.RemoveAll(stateDir)
	featureID := "behavioral-demo"
	os.MkdirAll(filepath.Join(stateDir, featureID), 0755)

	obs := observe.New(true, stateDir, false, "", false, "agentic")

	f := &feature.Feature{
		ID:            featureID,
		TraceID:       "trace-123456789012345678901234567890",
		Name:          "Behavioral Demo",
		FeatureSpanID: "span-abc123def4567890",
		ActiveRun:     1,
	}

	hook := func(observer *observe.Observer, feat *feature.Feature) permission.DecisionHook {
		if observer == nil || feat == nil {
			return nil
		}
		return func(toolName, toolInput string, allowed bool) {
			decision := "defer"
			if allowed {
				decision = "allow"
			}
			sc := observe.SpanContextForFeature(feat.ID, feat.TraceID, feat.Name, feat.FeatureSpanID).WithRun(feat.ActiveRun).Child()
			observer.AutoReviewed(sc, toolName, toolInput, decision)
		}
	}(obs, f)

	inner := &permission.AcceptEditsHandler{}
	classify := func(toolName, toolInput string) (bool, error) {
		if toolInput == "go test ./..." {
			return true, nil
		}
		if toolInput == "curl -s https://example.com" {
			return false, nil
		}
		return false, fmt.Errorf("classifier error")
	}

	ar := &permission.AutoReviewHandler{
		Inner:      inner,
		Cache:      permission.NewCache(nil),
		Classify:   classify,
		OnDecision: hook,
	}

	// 1. Allow
	ar.CanUseTool(ports.ToolPermissionRequest{ToolName: "Bash", Input: "go test ./..."})
	// 2. Defer
	ar.CanUseTool(ports.ToolPermissionRequest{ToolName: "Bash", Input: "curl -s https://example.com"})
	// 3. Error (classifier error)
	ar.CanUseTool(ports.ToolPermissionRequest{ToolName: "Bash", Input: "unknown command"})
	// 4. Static deny-list match (should NOT emit)
	ar.CanUseTool(ports.ToolPermissionRequest{ToolName: "Bash", Input: `{"command":"rm -rf /tmp"}`})
	// 5. Non-Bash defer (should NOT emit)
	ar.CanUseTool(ports.ToolPermissionRequest{ToolName: "Write", Input: `{"file_path":"/tmp/x"}`})

	// Read events
	eventsPath := filepath.Join(stateDir, featureID, "events.jsonl")
	file, err := os.Open(eventsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "opening events.jsonl: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()

	var events []map[string]any
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var evt map[string]any
		json.Unmarshal(scanner.Bytes(), &evt)
		events = append(events, evt)
	}

	// Filter to only permission.auto_reviewed events
	var autoReviewed []map[string]any
	for _, evt := range events {
		if evt["event_type"] == "permission.auto_reviewed" {
			autoReviewed = append(autoReviewed, evt)
		}
	}

	// Print the filtered events as JSONL
	for _, evt := range autoReviewed {
		b, _ := json.Marshal(evt)
		fmt.Println(string(b))
	}

	fmt.Printf("\nTotal permission.auto_reviewed events: %d\n", len(autoReviewed))
	fmt.Println("Expected: 3 (allow, defer, error)")
	fmt.Println("Not present: static-deny-list defer and non-Bash defer")
}
