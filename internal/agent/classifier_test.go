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
	"encoding/json"
	"math"
	"math/rand"
	"strconv"
	"sync"
	"testing"
)

func TestCNBClassifier_BasicAccuracy(t *testing.T) {
	c := NewCNBClassifier(1.0, 1.5)

	c.Fit("frontend", map[string]float64{
		"react": 3.0, "component": 2.5, "ui": 2.0, "css": 1.5, "button": 1.0,
	}, 1.0)
	c.Fit("backend", map[string]float64{
		"api": 3.0, "database": 2.5, "query": 2.0, "server": 1.5, "endpoint": 1.0,
	}, 1.0)
	c.Fit("infra", map[string]float64{
		"kubernetes": 3.0, "helm": 2.5, "deploy": 2.0, "cluster": 1.5, "container": 1.0,
	}, 1.0)
	c.Finalize()

	// Query with infra terms
	scores, err := c.Predict(map[string]float64{
		"deploy": 2.0, "kubernetes": 3.0, "cluster": 1.5,
	})
	if err != nil {
		t.Fatalf("Predict failed: %v", err)
	}
	if scores[0].Name != "infra" {
		t.Errorf("expected infra as top class, got %s (scores: %+v)", scores[0].Name, scores)
	}

	// Query with frontend terms
	scores, err = c.Predict(map[string]float64{
		"react": 3.0, "ui": 2.0, "component": 2.5,
	})
	if err != nil {
		t.Fatalf("Predict failed: %v", err)
	}
	if scores[0].Name != "frontend" {
		t.Errorf("expected frontend as top class, got %s (scores: %+v)", scores[0].Name, scores)
	}
}

func TestCNBClassifier_ComplementStatistics(t *testing.T) {
	c := NewCNBClassifier(1.0, 1.5)

	c.Fit("A", map[string]float64{"x": 2.0, "y": 3.0}, 1.0)
	c.Fit("B", map[string]float64{"x": 1.0, "z": 4.0}, 1.0)
	c.Finalize()

	// For class A, complement counts come from B: x=1.0, z=4.0
	// For class B, complement counts come from A: x=2.0, y=3.0
	// Vocab: {x, y, z} = 3 terms

	// Verify weights exist and are reasonable
	if len(c.WeightsMap()) != 2 {
		t.Fatalf("expected 2 class weights, got %d", len(c.WeightsMap()))
	}

	for class, weights := range c.WeightsMap() {
		for token, w := range weights {
			if math.IsNaN(w) || math.IsInf(w, 0) {
				t.Errorf("class %s, token %s: weight is %f (NaN/Inf)", class, token, w)
			}
		}
	}

	// Manual complement check for class A:
	// complement total from B = 1.0 + 4.0 = 5.0
	// denominator = 1.0*3 + 5.0 = 8.0
	// raw weight for x = log((1.0 + 1.0) / 8.0) = log(0.25)
	// raw weight for y = log((1.0 + 0.0) / 8.0) = log(0.125)
	// raw weight for z = log((1.0 + 4.0) / 8.0) = log(0.625)
	rawX := math.Log(2.0 / 8.0)
	rawY := math.Log(1.0 / 8.0)
	rawZ := math.Log(5.0 / 8.0)
	l1 := math.Abs(rawX) + math.Abs(rawY) + math.Abs(rawZ)

	expectedWeightX := rawX / l1
	expectedWeightY := rawY / l1
	expectedWeightZ := rawZ / l1

	tol := 1e-10
	if math.Abs(c.WeightsMap()["A"]["x"]-expectedWeightX) > tol {
		t.Errorf("Weights[A][x] = %f, want %f", c.WeightsMap()["A"]["x"], expectedWeightX)
	}
	if math.Abs(c.WeightsMap()["A"]["y"]-expectedWeightY) > tol {
		t.Errorf("Weights[A][y] = %f, want %f", c.WeightsMap()["A"]["y"], expectedWeightY)
	}
	if math.Abs(c.WeightsMap()["A"]["z"]-expectedWeightZ) > tol {
		t.Errorf("Weights[A][z] = %f, want %f", c.WeightsMap()["A"]["z"], expectedWeightZ)
	}
}

func TestCNBClassifier_WCNBNormalization(t *testing.T) {
	c := NewCNBClassifier(1.0, 1.5)

	c.Fit("A", map[string]float64{"x": 2.0, "y": 3.0, "z": 1.0}, 1.0)
	c.Fit("B", map[string]float64{"x": 1.0, "z": 4.0, "w": 2.0}, 1.0)
	c.Finalize()

	tol := 1e-10
	for class, weights := range c.WeightsMap() {
		l1 := 0.0
		for _, w := range weights {
			l1 += math.Abs(w)
		}
		if math.Abs(l1-1.0) > tol {
			t.Errorf("class %s: L1 norm of weights = %f, want 1.0", class, l1)
		}
	}
}

func TestCNBClassifier_SoftmaxProbabilities(t *testing.T) {
	c := NewCNBClassifier(1.0, 1.5)

	c.Fit("frontend", map[string]float64{"react": 5.0, "css": 3.0}, 1.0)
	c.Fit("backend", map[string]float64{"api": 5.0, "database": 3.0}, 1.0)
	c.Fit("infra", map[string]float64{"kubernetes": 5.0, "deploy": 3.0}, 1.0)
	c.Finalize()

	scores, err := c.Predict(map[string]float64{"react": 3.0, "css": 2.0})
	if err != nil {
		t.Fatalf("Predict failed: %v", err)
	}

	// Sum of probabilities should be 1.0
	sum := 0.0
	for _, s := range scores {
		sum += s.Probability
	}
	if math.Abs(sum-1.0) > 1e-10 {
		t.Errorf("probability sum = %f, want 1.0", sum)
	}

	// Top class should have higher probability than second
	if scores[0].Probability <= scores[1].Probability {
		t.Errorf("top probability %f should be > second %f",
			scores[0].Probability, scores[1].Probability)
	}
}

func TestCNBClassifier_TemperatureEffect(t *testing.T) {
	buildModel := func(temp float64) *CNBClassifier {
		c := NewCNBClassifier(1.0, temp)
		c.Fit("frontend", map[string]float64{"react": 5.0, "component": 3.0}, 1.0)
		c.Fit("backend", map[string]float64{"api": 5.0, "database": 3.0}, 1.0)
		c.Fit("infra", map[string]float64{"kubernetes": 5.0, "deploy": 3.0}, 1.0)
		c.Finalize()
		return c
	}

	query := map[string]float64{"react": 3.0, "component": 2.0}

	sharp := buildModel(0.5)
	flat := buildModel(5.0)

	sharpScores, err := sharp.Predict(query)
	if err != nil {
		t.Fatalf("Predict failed: %v", err)
	}
	flatScores, err := flat.Predict(query)
	if err != nil {
		t.Fatalf("Predict failed: %v", err)
	}

	// Sharp (T=0.5) should have higher top probability
	if sharpScores[0].Probability <= flatScores[0].Probability {
		t.Errorf("T=0.5 top probability %f should be > T=5.0 top probability %f",
			sharpScores[0].Probability, flatScores[0].Probability)
	}

	// Same top class in both cases
	if sharpScores[0].Name != flatScores[0].Name {
		t.Errorf("top class should be same: T=0.5 got %s, T=5.0 got %s",
			sharpScores[0].Name, flatScores[0].Name)
	}
}

func TestCNBClassifier_LaplaceSmoothingEffect(t *testing.T) {
	buildModel := func(alpha float64) *CNBClassifier {
		c := NewCNBClassifier(alpha, 1.5)
		c.Fit("frontend", map[string]float64{"react": 5.0, "component": 3.0}, 1.0)
		c.Fit("backend", map[string]float64{"api": 5.0, "database": 3.0}, 1.0)
		c.Finalize()
		return c
	}

	query := map[string]float64{"react": 3.0, "component": 2.0}

	confident := buildModel(0.1)
	uniform := buildModel(10.0)

	confScores, err := confident.Predict(query)
	if err != nil {
		t.Fatalf("Predict failed: %v", err)
	}
	uniScores, err := uniform.Predict(query)
	if err != nil {
		t.Fatalf("Predict failed: %v", err)
	}

	// Higher alpha -> more uniform predictions (lower top probability)
	if confScores[0].Probability <= uniScores[0].Probability {
		t.Errorf("alpha=0.1 top prob %f should be > alpha=10.0 top prob %f",
			confScores[0].Probability, uniScores[0].Probability)
	}
}

func TestCNBClassifier_WeightedFit(t *testing.T) {
	c := NewCNBClassifier(1.0, 1.5)

	tokens := map[string]float64{"x": 2.0, "y": 3.0}
	c.Fit("A", tokens, 1.0)
	c.Fit("B", tokens, 10.0)

	// B's counts should be 10x A's counts
	tol := 1e-10
	for token := range tokens {
		if math.Abs(c.ClassCounts["B"][token]-c.ClassCounts["A"][token]*10) > tol {
			t.Errorf("ClassCounts[B][%s] = %f, want %f",
				token, c.ClassCounts["B"][token], c.ClassCounts["A"][token]*10)
		}
	}
}

func TestCNBClassifier_RemoveClass(t *testing.T) {
	c := NewCNBClassifier(1.0, 1.5)

	c.Fit("frontend", map[string]float64{"react": 5.0, "frontend_only": 2.0}, 1.0)
	c.Fit("backend", map[string]float64{"api": 5.0, "shared": 2.0}, 1.0)
	c.Fit("infra", map[string]float64{"kubernetes": 5.0, "shared": 2.0}, 1.0)

	c.RemoveClass("frontend")
	c.Finalize()

	scores, err := c.Predict(map[string]float64{"api": 3.0})
	if err != nil {
		t.Fatalf("Predict failed: %v", err)
	}

	// Frontend should not be in results
	for _, s := range scores {
		if s.Name == "frontend" {
			t.Error("removed class 'frontend' should not appear in predictions")
		}
	}

	// Vocabulary should not contain frontend_only
	if _, exists := c.Vocabulary["frontend_only"]; exists {
		t.Error("'frontend_only' should be removed from vocabulary after removing frontend class")
	}

	// Should still have 2 classes
	if len(scores) != 2 {
		t.Errorf("expected 2 classes after removal, got %d", len(scores))
	}
}

func TestCNBClassifier_IncrementalEqualsFullRetrain(t *testing.T) {
	// Model A: train all 3 classes at once
	a := NewCNBClassifier(1.0, 1.5)
	a.Fit("X", map[string]float64{"a": 2.0, "b": 3.0}, 1.0)
	a.Fit("Y", map[string]float64{"c": 2.0, "d": 3.0}, 1.0)
	a.Fit("Z", map[string]float64{"e": 2.0, "f": 3.0}, 1.0)
	a.Finalize()

	// Model B: train 2 classes, finalize, then add 3rd incrementally
	b := NewCNBClassifier(1.0, 1.5)
	b.Fit("X", map[string]float64{"a": 2.0, "b": 3.0}, 1.0)
	b.Fit("Y", map[string]float64{"c": 2.0, "d": 3.0}, 1.0)
	b.Finalize()
	b.AddTrainingData("Z", map[string]float64{"e": 2.0, "f": 3.0}, 1.0)
	b.Finalize()

	query := map[string]float64{"a": 1.0, "c": 1.0, "e": 1.0}
	scoresA, err := a.Predict(query)
	if err != nil {
		t.Fatalf("Predict failed: %v", err)
	}
	scoresB, err := b.Predict(query)
	if err != nil {
		t.Fatalf("Predict failed: %v", err)
	}

	if len(scoresA) != len(scoresB) {
		t.Fatalf("different number of scores: A=%d, B=%d", len(scoresA), len(scoresB))
	}

	tol := 1e-10
	for i := range scoresA {
		if scoresA[i].Name != scoresB[i].Name {
			t.Errorf("score[%d] name: A=%s, B=%s", i, scoresA[i].Name, scoresB[i].Name)
		}
		if math.Abs(scoresA[i].Probability-scoresB[i].Probability) > tol {
			t.Errorf("score[%d] prob: A=%f, B=%f", i, scoresA[i].Probability, scoresB[i].Probability)
		}
	}
}

func TestCNBClassifier_EmptyTokens(t *testing.T) {
	c := NewCNBClassifier(1.0, 1.5)

	c.Fit("A", map[string]float64{"x": 1.0}, 1.0)
	c.Fit("B", map[string]float64{"y": 1.0}, 1.0)
	c.Fit("C", map[string]float64{"z": 1.0}, 1.0)
	c.Finalize()

	scores, err := c.Predict(map[string]float64{})
	if err != nil {
		t.Fatalf("Predict failed: %v", err)
	}
	numClasses := float64(len(scores))
	expected := 1.0 / numClasses
	tol := 1e-10

	for _, s := range scores {
		if math.Abs(s.Probability-expected) > tol {
			t.Errorf("class %s: probability %f, want %f (uniform)", s.Name, s.Probability, expected)
		}
	}
}

func TestCNBClassifier_SingleClass(t *testing.T) {
	c := NewCNBClassifier(1.0, 1.5)
	c.Fit("only", map[string]float64{"x": 1.0, "y": 2.0}, 1.0)
	c.Finalize()

	scores, err := c.Predict(map[string]float64{"x": 1.0})
	if err != nil {
		t.Fatalf("Predict failed: %v", err)
	}
	if len(scores) != 1 {
		t.Fatalf("expected 1 score, got %d", len(scores))
	}
	if scores[0].Name != "only" {
		t.Errorf("expected class 'only', got %s", scores[0].Name)
	}
	if math.Abs(scores[0].Probability-1.0) > 1e-10 {
		t.Errorf("expected probability 1.0, got %f", scores[0].Probability)
	}
}

func TestCNBClassifier_MarshalRoundTrip(t *testing.T) {
	c := NewCNBClassifier(1.0, 1.5)
	c.Fit("frontend", map[string]float64{"react": 5.0, "component": 3.0}, 1.0)
	c.Fit("backend", map[string]float64{"api": 5.0, "database": 3.0}, 1.0)
	c.Fit("infra", map[string]float64{"kubernetes": 5.0, "deploy": 3.0}, 1.0)
	c.Finalize()

	query := map[string]float64{"react": 3.0, "kubernetes": 1.0}
	originalScores, err := c.Predict(query)
	if err != nil {
		t.Fatalf("Predict failed: %v", err)
	}

	// Marshal
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("MarshalJSON failed: %v", err)
	}

	// Unmarshal into new classifier
	var restored CNBClassifier
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("UnmarshalJSON failed: %v", err)
	}
	restored.Finalize()

	// Predict with restored model
	restoredScores, err := restored.Predict(query)
	if err != nil {
		t.Fatalf("Predict failed: %v", err)
	}

	if len(originalScores) != len(restoredScores) {
		t.Fatalf("different number of scores: original=%d, restored=%d",
			len(originalScores), len(restoredScores))
	}

	tol := 1e-10
	for i := range originalScores {
		if originalScores[i].Name != restoredScores[i].Name {
			t.Errorf("score[%d] name: original=%s, restored=%s",
				i, originalScores[i].Name, restoredScores[i].Name)
		}
		if math.Abs(originalScores[i].Probability-restoredScores[i].Probability) > tol {
			t.Errorf("score[%d] prob: original=%f, restored=%f",
				i, originalScores[i].Probability, restoredScores[i].Probability)
		}
	}
}

func TestCNBClassifier_JSONSchemaContract(t *testing.T) {
	c := NewCNBClassifier(1.0, 1.5)
	c.Fit("frontend", map[string]float64{"react": 5.0, "component": 3.0}, 1.0)
	c.Fit("backend", map[string]float64{"api": 5.0, "database": 3.0}, 1.0)
	c.Finalize()

	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("MarshalJSON failed: %v", err)
	}

	// Parse into generic map to verify wire format structure.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	// Verify all expected top-level keys exist.
	for _, key := range []string{"class_counts", "class_totals", "class_docs", "vocabulary", "alpha", "temperature"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing expected key %q in JSON output", key)
		}
	}

	// Verify vocabulary is a map[string]bool (not an array).
	var vocab map[string]bool
	if err := json.Unmarshal(raw["vocabulary"], &vocab); err != nil {
		t.Fatalf("vocabulary should be map[string]bool: %v", err)
	}
	if !vocab["react"] || !vocab["component"] || !vocab["api"] || !vocab["database"] {
		t.Errorf("vocabulary missing expected terms: %v", vocab)
	}

	// Verify class_counts is map[string]map[string]float64 (not indexed arrays).
	var counts map[string]map[string]float64
	if err := json.Unmarshal(raw["class_counts"], &counts); err != nil {
		t.Fatalf("class_counts should be map[string]map[string]float64: %v", err)
	}
	if counts["frontend"]["react"] != 5.0 {
		t.Errorf("class_counts[frontend][react] = %f, want 5.0", counts["frontend"]["react"])
	}
	if counts["backend"]["api"] != 5.0 {
		t.Errorf("class_counts[backend][api] = %f, want 5.0", counts["backend"]["api"])
	}
}

func TestCNBClassifier_UnmarshalJSON_MalformedInput(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{
			name: "completely invalid JSON",
			json: `not json at all`,
		},
		{
			name: "non-numeric alpha",
			json: `{"alpha":"foo","temperature":1.5,"vocabulary":{},"class_totals":{},"class_docs":{},"class_counts":{}}`,
		},
		{
			name: "invalid class_docs value",
			json: `{"alpha":1.0,"temperature":1.5,"vocabulary":{},"class_totals":{},"class_docs":{"a":"not_int"},"class_counts":{}}`,
		},
		{
			name: "invalid nested class_counts value",
			json: `{"alpha":1.0,"temperature":1.5,"vocabulary":{},"class_totals":{},"class_docs":{},"class_counts":{"cls":{"term":"bad"}}}`,
		},
		{
			name: "missing alpha field",
			json: `{"temperature":1.5,"vocabulary":{},"class_totals":{},"class_docs":{},"class_counts":{}}`,
		},
		{
			name: "missing vocabulary field",
			json: `{"alpha":1.0,"temperature":1.5,"class_totals":{},"class_docs":{},"class_counts":{}}`,
		},
		{
			name: "non-boolean vocabulary value",
			json: `{"alpha":1.0,"temperature":1.5,"vocabulary":{"term":123},"class_totals":{},"class_docs":{},"class_counts":{}}`,
		},
		{
			name: "class_docs is null",
			json: `{"alpha":1.0,"temperature":1.5,"vocabulary":{},"class_totals":{},"class_docs":null,"class_counts":{}}`,
		},
		{
			name: "class_totals is scalar",
			json: `{"alpha":1.0,"temperature":1.5,"vocabulary":{},"class_totals":42,"class_docs":{},"class_counts":{}}`,
		},
		{
			name: "vocabulary is scalar",
			json: `{"alpha":1.0,"temperature":1.5,"vocabulary":42,"class_totals":{},"class_docs":{},"class_counts":{}}`,
		},
		{
			name: "class_counts is null",
			json: `{"alpha":1.0,"temperature":1.5,"vocabulary":{},"class_totals":{},"class_docs":{},"class_counts":null}`,
		},
		{
			name: "class_counts token absent from vocabulary",
			json: `{"alpha":1.0,"temperature":1.5,"vocabulary":{"x":true},"class_totals":{"A":5},"class_docs":{"A":1},"class_counts":{"A":{"x":2,"missing":3}}}`,
		},
		{
			name: "missing top-level closing brace",
			json: `{"alpha":1.0,"temperature":1.5,"vocabulary":{},"class_totals":{},"class_docs":{},"class_counts":{}`,
		},
		{
			name: "trailing non-whitespace after root object",
			json: `{"alpha":1.0,"temperature":1.5,"vocabulary":{},"class_totals":{},"class_docs":{},"class_counts":{}}junk`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c CNBClassifier
			err := json.Unmarshal([]byte(tt.json), &c)
			if err == nil {
				t.Errorf("expected error for input %q, got nil", tt.json)
			}
		})
	}
}

func TestCNBClassifier_ErrorIfNotFinalized(t *testing.T) {
	c := NewCNBClassifier(1.0, 1.5)
	c.Fit("A", map[string]float64{"x": 1.0}, 1.0)

	scores, err := c.Predict(map[string]float64{"x": 1.0})
	if err != ErrNotFinalized {
		t.Errorf("expected ErrNotFinalized, got %v", err)
	}
	if scores != nil {
		t.Errorf("expected nil scores, got %v", scores)
	}
}

func TestCNBClassifier_ConcurrentPredict(t *testing.T) {
	c := NewCNBClassifier(1.0, 1.5)
	c.Fit("frontend", map[string]float64{"react": 5.0, "component": 3.0}, 1.0)
	c.Fit("backend", map[string]float64{"api": 5.0, "database": 3.0}, 1.0)
	c.Finalize()

	// Run concurrent Predict calls to verify thread-safety under race detector.
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				scores, err := c.Predict(map[string]float64{"react": 3.0, "api": 1.0})
				if err != nil {
					t.Errorf("concurrent Predict failed: %v", err)
					return
				}
				if len(scores) != 2 {
					t.Errorf("expected 2 scores, got %d", len(scores))
					return
				}
			}
		}()
	}
	wg.Wait()
}

// --- SelectByThreshold tests (ported from tfidf_test.go) ---

func TestSelectByThreshold(t *testing.T) {
	tests := []struct {
		name          string
		scores        []ClassScore
		liftThreshold float64
		maxSelections int
		want          []string
	}{
		{
			name: "single score clears lift threshold",
			scores: []ClassScore{
				{Name: "high", Probability: 0.5},
				{Name: "medium", Probability: 0.3},
				{Name: "low", Probability: 0.2},
			},
			liftThreshold: 1.1,
			maxSelections: 3,
			want:          []string{"high"},
		},
		{
			name: "two scores clear lift threshold",
			scores: []ClassScore{
				{Name: "first", Probability: 0.50},
				{Name: "second", Probability: 0.40},
				{Name: "third", Probability: 0.10},
			},
			liftThreshold: 1.1,
			maxSelections: 3,
			want:          []string{"first", "second"},
		},
		{
			name: "top score selected even below lift threshold",
			scores: []ClassScore{
				{Name: "a", Probability: 0.52},
				{Name: "b", Probability: 0.48},
			},
			liftThreshold: 1.1,
			maxSelections: 3,
			want:          []string{"a"},
		},
		{
			name:          "empty scores",
			liftThreshold: 1.1,
			maxSelections: 3,
			want:          nil,
		},
		{
			name: "deterministic tie breaker respects max selections",
			scores: []ClassScore{
				{Name: "delta", Probability: 0.5},
				{Name: "alpha", Probability: 0.5},
				{Name: "bravo", Probability: 0.5},
			},
			liftThreshold: 1.1,
			maxSelections: 1,
			want:          []string{"alpha"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SelectByThreshold(tt.scores, tt.liftThreshold, tt.maxSelections)
			if len(got) != len(tt.want) {
				t.Fatalf("SelectByThreshold(%v, %v, %d) = %v, want %v", tt.scores, tt.liftThreshold, tt.maxSelections, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("SelectByThreshold(%v, %v, %d)[%d] = %q, want %q", tt.scores, tt.liftThreshold, tt.maxSelections, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// --- Benchmarks ---

func generateVocab(size int) []string {
	prefixes := []string{
		"api", "service", "config", "deploy", "build", "test", "auth",
		"user", "data", "log", "cache", "queue", "worker", "handler",
		"client", "server", "proxy", "gateway", "monitor", "alert",
	}
	suffixes := []string{
		"manager", "controller", "provider", "factory", "builder",
		"validator", "parser", "formatter", "converter", "transformer",
		"listener", "dispatcher", "scheduler", "executor", "resolver",
	}

	vocab := make([]string, 0, size)
	for i := 0; i < size; i++ {
		prefix := prefixes[i%len(prefixes)]
		suffix := suffixes[i%len(suffixes)]
		vocab = append(vocab, prefix+"_"+suffix+"_"+strconv.Itoa(i))
	}
	return vocab
}

func buildRealisticClassifier(b *testing.B) *CNBClassifier {
	b.Helper()
	c := NewCNBClassifier(1.0, 1.5)
	rng := rand.New(rand.NewSource(42))

	classes := []string{
		"agentic", "taulu", "graph-runner", "pedregal", "humanlayer",
		"dev-console", "cluster-config", "dbaccess", "dd-util-go",
		"identity-service", "runtime", "cassandra", "aurora-extract",
		"secret-platform", "service-configs", "odxtools", "dbmesh",
		"dbmesh-ra", "buildkite-agent", "services-protobuf",
	}

	vocab := generateVocab(8000)

	for _, class := range classes {
		tokens := make(map[string]float64)
		numTokens := 500 + rng.Intn(1000)
		for i := 0; i < numTokens; i++ {
			term := vocab[rng.Intn(len(vocab))]
			tokens[term] = 1.0 + rng.Float64()*3.0
		}
		for j := 0; j < 50; j++ {
			tokens[class+"_specific_"+strconv.Itoa(j)] = 2.0 + rng.Float64()*5.0
		}
		c.Fit(class, tokens, 1.0)
	}
	c.Finalize()
	return c
}

func buildRealisticQuery(rng *rand.Rand, vocab []string) map[string]float64 {
	tokens := make(map[string]float64)
	for i := 0; i < 150; i++ {
		term := vocab[rng.Intn(len(vocab))]
		tokens[term] = 1.0 + rng.Float64()*3.0
	}
	return tokens
}

func BenchmarkCNBClassifier_Predict_20Classes(b *testing.B) {
	b.ReportAllocs()
	c := buildRealisticClassifier(b)
	rng := rand.New(rand.NewSource(99))
	vocab := generateVocab(8000)
	query := buildRealisticQuery(rng, vocab)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = c.Predict(query)
	}
}

func BenchmarkCNBClassifier_Train_20Classes(b *testing.B) {
	b.ReportAllocs()
	rng := rand.New(rand.NewSource(42))
	vocab := generateVocab(8000)

	classes := []string{
		"agentic", "taulu", "graph-runner", "pedregal", "humanlayer",
		"dev-console", "cluster-config", "dbaccess", "dd-util-go",
		"identity-service", "runtime", "cassandra", "aurora-extract",
		"secret-platform", "service-configs", "odxtools", "dbmesh",
		"dbmesh-ra", "buildkite-agent", "services-protobuf",
	}

	// Pre-generate training data
	type trainData struct {
		class  string
		tokens map[string]float64
	}
	data := make([]trainData, 0, len(classes))
	for _, class := range classes {
		tokens := make(map[string]float64)
		numTokens := 500 + rng.Intn(1000)
		for i := 0; i < numTokens; i++ {
			term := vocab[rng.Intn(len(vocab))]
			tokens[term] = 1.0 + rng.Float64()*3.0
		}
		for j := 0; j < 50; j++ {
			tokens[class+"_specific_"+strconv.Itoa(j)] = 2.0 + rng.Float64()*5.0
		}
		data = append(data, trainData{class: class, tokens: tokens})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c := NewCNBClassifier(1.0, 1.5)
		for _, d := range data {
			c.Fit(d.class, d.tokens, 1.0)
		}
		c.Finalize()
	}
}

func BenchmarkCNBClassifier_Finalize_20Classes(b *testing.B) {
	b.ReportAllocs()
	// Build classifier without finalizing
	rng := rand.New(rand.NewSource(42))
	vocab := generateVocab(8000)

	classes := []string{
		"agentic", "taulu", "graph-runner", "pedregal", "humanlayer",
		"dev-console", "cluster-config", "dbaccess", "dd-util-go",
		"identity-service", "runtime", "cassandra", "aurora-extract",
		"secret-platform", "service-configs", "odxtools", "dbmesh",
		"dbmesh-ra", "buildkite-agent", "services-protobuf",
	}

	template := NewCNBClassifier(1.0, 1.5)
	for _, class := range classes {
		tokens := make(map[string]float64)
		numTokens := 500 + rng.Intn(1000)
		for i := 0; i < numTokens; i++ {
			term := vocab[rng.Intn(len(vocab))]
			tokens[term] = 1.0 + rng.Float64()*3.0
		}
		for j := 0; j < 50; j++ {
			tokens[class+"_specific_"+strconv.Itoa(j)] = 2.0 + rng.Float64()*5.0
		}
		template.Fit(class, tokens, 1.0)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Copy the classifier state and re-finalize
		c := &CNBClassifier{
			ClassCounts: template.ClassCounts,
			ClassTotals: template.ClassTotals,
			ClassDocs:   template.ClassDocs,
			Vocabulary:  template.Vocabulary,
			vocabIndex:  template.vocabIndex,
			Alpha:       template.Alpha,
			Temperature: template.Temperature,
		}
		c.Finalize()
	}
}

func BenchmarkCNBClassifier_MarshalJSON(b *testing.B) {
	b.ReportAllocs()
	c := buildRealisticClassifier(b)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := json.Marshal(c)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCNBClassifier_UnmarshalJSON(b *testing.B) {
	b.ReportAllocs()
	c := buildRealisticClassifier(b)
	data, err := json.Marshal(c)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var restored CNBClassifier
		if err := json.Unmarshal(data, &restored); err != nil {
			b.Fatal(err)
		}
	}
}
