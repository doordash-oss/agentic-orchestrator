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
	"math"
	"strconv"
	"strings"
	"testing"
)

func TestTokenize_Exported(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "basic splitting with hyphens preserved",
			input:    "fix the graph-runner TUI bug in agentic",
			expected: []string{"fix", "the", "graph-runner", "TUI", "bug", "in", "agentic"},
		},
		{
			name:     "underscores preserved",
			input:    "my_variable some_func",
			expected: []string{"my_variable", "some_func"},
		},
		{
			name:     "punctuation splits",
			input:    "hello.world,test;case",
			expected: []string{"hello", "world", "test", "case"},
		},
		{
			name:     "empty string",
			input:    "",
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Tokenize(tt.input)
			if len(got) != len(tt.expected) {
				t.Fatalf("Tokenize(%q) = %v (len %d), want %v (len %d)",
					tt.input, got, len(got), tt.expected, len(tt.expected))
			}
			for i := range tt.expected {
				if got[i] != tt.expected[i] {
					t.Errorf("token[%d] = %q, want %q", i, got[i], tt.expected[i])
				}
			}
		})
	}
}

func TestExtractBigrams(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "normal bigrams",
			input:    []string{"rate", "limiting", "api"},
			expected: []string{"rate_limiting", "limiting_api"},
		},
		{
			name:     "single token",
			input:    []string{"hello"},
			expected: nil,
		},
		{
			name:     "empty input",
			input:    nil,
			expected: nil,
		},
		{
			name:     "two tokens",
			input:    []string{"hello", "world"},
			expected: []string{"hello_world"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractBigrams(tt.input)
			if len(got) != len(tt.expected) {
				t.Fatalf("ExtractBigrams(%v) = %v, want %v", tt.input, got, tt.expected)
			}
			for i := range tt.expected {
				if got[i] != tt.expected[i] {
					t.Errorf("bigram[%d] = %q, want %q", i, got[i], tt.expected[i])
				}
			}
		})
	}
}

func TestTokenizeAndProcess(t *testing.T) {
	result := TokenizeAndProcess("Fix the Bug in rate limiting API")

	// Should lowercase, remove stop words ("the", "in"), filter short tokens
	// Unigrams: "fix", "bug", "rate", "limiting", "api"
	// Bigrams: "fix_bug", "bug_rate", "rate_limiting", "limiting_api"
	resultSet := make(map[string]bool)
	for _, tok := range result {
		resultSet[tok] = true
	}

	// Check unigrams present
	for _, expected := range []string{"fix", "bug", "rate", "limiting", "api"} {
		if !resultSet[expected] {
			t.Errorf("expected unigram %q in result, got %v", expected, result)
		}
	}

	// Check bigrams present
	for _, expected := range []string{"fix_bug", "bug_rate", "rate_limiting", "limiting_api"} {
		if !resultSet[expected] {
			t.Errorf("expected bigram %q in result, got %v", expected, result)
		}
	}

	// Verify stop words removed
	for _, stopWord := range []string{"the", "in"} {
		if resultSet[stopWord] {
			t.Errorf("stop word %q should not be in result", stopWord)
		}
	}
}

func TestBuildIDF(t *testing.T) {
	documents := map[string][]string{
		"doc1": {"go", "service", "kubernetes"},
		"doc2": {"go", "service", "database"},
		"doc3": {"go", "service", "authentication"},
	}

	idf := BuildIDF(documents)

	// "go" appears in all 3 docs -> IDF = log(3/3) = 0
	if math.Abs(idf["go"]) > 1e-10 {
		t.Errorf("IDF(go) = %f, want ~0 (appears in all docs)", idf["go"])
	}

	// "kubernetes" appears in 1 doc -> IDF = log(3/1) = log(3) ≈ 1.099
	expectedIDF := math.Log(3.0)
	if math.Abs(idf["kubernetes"]-expectedIDF) > 1e-10 {
		t.Errorf("IDF(kubernetes) = %f, want %f", idf["kubernetes"], expectedIDF)
	}

	// Rare terms should have higher IDF than common terms
	if idf["go"] >= idf["kubernetes"] {
		t.Errorf("IDF(go)=%f should be < IDF(kubernetes)=%f", idf["go"], idf["kubernetes"])
	}

	// Empty documents
	emptyIDF := BuildIDF(nil)
	if len(emptyIDF) != 0 {
		t.Errorf("empty documents should produce empty IDF, got %v", emptyIDF)
	}
}

func TestComputeTFIDF_SublinearTF(t *testing.T) {
	idf := map[string]float64{
		"go":         0.5,
		"kubernetes": 1.0,
	}

	// "go" appears 3 times, "kubernetes" appears 1 time
	tokens := []string{"go", "go", "go", "kubernetes"}
	vec := ComputeTFIDF(tokens, idf)

	// TF(go) = 1 + log(3) ≈ 2.099, TF-IDF(go) = 2.099 * 0.5 ≈ 1.049
	expectedGoTFIDF := (1.0 + math.Log(3.0)) * 0.5
	if math.Abs(vec["go"]-expectedGoTFIDF) > 1e-10 {
		t.Errorf("TF-IDF(go) = %f, want %f", vec["go"], expectedGoTFIDF)
	}

	// TF(kubernetes) = 1 + log(1) = 1.0, TF-IDF(kubernetes) = 1.0 * 1.0 = 1.0
	expectedK8sTFIDF := 1.0
	if math.Abs(vec["kubernetes"]-expectedK8sTFIDF) > 1e-10 {
		t.Errorf("TF-IDF(kubernetes) = %f, want %f", vec["kubernetes"], expectedK8sTFIDF)
	}
}

func TestComputeTFIDF_UnknownTermsIgnored(t *testing.T) {
	idf := map[string]float64{
		"known": 1.0,
	}
	tokens := []string{"known", "unknown", "also_unknown"}
	vec := ComputeTFIDF(tokens, idf)

	if _, ok := vec["unknown"]; ok {
		t.Error("unknown term should not appear in TF-IDF vector")
	}
	if _, ok := vec["also_unknown"]; ok {
		t.Error("also_unknown term should not appear in TF-IDF vector")
	}
	if _, ok := vec["known"]; !ok {
		t.Error("known term should appear in TF-IDF vector")
	}

	// Empty tokens
	emptyVec := ComputeTFIDF(nil, idf)
	if len(emptyVec) != 0 {
		t.Errorf("empty tokens should produce empty vector, got %v", emptyVec)
	}
}

// --- Benchmarks ---

func BenchmarkTokenizeAndProcess(b *testing.B) {
	b.ReportAllocs()
	// ~200 word text simulating a feature description
	text := strings.Repeat("implement rate limiting middleware for the API gateway service with redis backend and exponential backoff retry logic including circuit breaker pattern for downstream service calls ", 5)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		TokenizeAndProcess(text)
	}
}

func BenchmarkComputeTFIDF(b *testing.B) {
	b.ReportAllocs()

	// Build a 5000-term IDF map using unique keys via index suffix.
	idf := make(map[string]float64, 5000)
	for i := 0; i < 5000; i++ {
		term := "term_" + strconv.Itoa(i)
		idf[term] = 0.5 + float64(i%100)/100.0
	}

	// 150-token document using terms from the IDF map
	tokens := make([]string, 150)
	i := 0
	for term := range idf {
		if i >= 150 {
			break
		}
		tokens[i] = term
		i++
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ComputeTFIDF(tokens, idf)
	}
}
