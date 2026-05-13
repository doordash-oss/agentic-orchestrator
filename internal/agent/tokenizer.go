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
	"strings"
)

// StopWords is the set of common English words excluded from text classification.
// Exported so downstream code can inspect or extend it.
var StopWords = map[string]bool{
	"a": true, "an": true, "the": true, "is": true, "are": true,
	"was": true, "were": true, "be": true, "been": true, "being": true,
	"have": true, "has": true, "had": true, "do": true, "does": true,
	"did": true, "will": true, "would": true, "could": true, "should": true,
	"may": true, "might": true, "shall": true, "can": true,
	"for": true, "and": true, "nor": true, "but": true, "or": true,
	"yet": true, "so": true, "at": true, "by": true, "from": true,
	"in": true, "into": true, "of": true, "on": true, "to": true,
	"with": true, "as": true, "if": true, "that": true, "which": true,
	"this": true, "it": true, "its": true, "not": true, "no": true,
	"i": true, "we": true, "you": true, "they": true, "he": true, "she": true,
	"my": true, "our": true, "your": true, "their": true,
	"what": true, "when": true, "where": true, "how": true, "who": true,
	"all": true, "each": true, "every": true, "both": true, "few": true,
	"more": true, "most": true, "other": true, "some": true, "such": true,
	"than": true, "too": true, "very": true, "just": true, "about": true,
}

// Tokenize splits text into tokens on non-alphanumeric boundaries,
// preserving hyphens and underscores within tokens.
func Tokenize(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return !('a' <= r && r <= 'z') &&
			!('A' <= r && r <= 'Z') &&
			!('0' <= r && r <= '9') &&
			r != '-' && r != '_'
	})
}

// ExtractBigrams generates adjacent token pairs joined with "_".
// For example, ["rate", "limiting"] produces ["rate_limiting"].
func ExtractBigrams(tokens []string) []string {
	if len(tokens) < 2 {
		return nil
	}
	bigrams := make([]string, 0, len(tokens)-1)
	for i := 0; i < len(tokens)-1; i++ {
		bigrams = append(bigrams, tokens[i]+"_"+tokens[i+1])
	}
	return bigrams
}

// TokenizeAndProcess is a convenience pipeline: Tokenize -> lowercase ->
// FilterStopWords -> filter len<2 -> add bigrams -> return combined unigrams+bigrams.
func TokenizeAndProcess(text string) []string {
	raw := Tokenize(strings.ToLower(text))

	// Filter stop words and short tokens
	filtered := make([]string, 0, len(raw))
	for _, tok := range raw {
		if len(tok) >= 2 && !StopWords[tok] {
			filtered = append(filtered, tok)
		}
	}

	bigrams := ExtractBigrams(filtered)
	result := make([]string, 0, len(filtered)+len(bigrams))
	result = append(result, filtered...)
	result = append(result, bigrams...)
	return result
}

// BuildIDF computes Inverse Document Frequency across a corpus of
// pre-tokenized documents. IDF(term) = log(N / df(term)).
func BuildIDF(documents map[string][]string) map[string]float64 {
	if len(documents) == 0 {
		return make(map[string]float64)
	}
	n := float64(len(documents))

	df := make(map[string]int)
	for _, tokens := range documents {
		seen := make(map[string]bool)
		for _, tok := range tokens {
			if !seen[tok] {
				df[tok]++
				seen[tok] = true
			}
		}
	}

	idf := make(map[string]float64, len(df))
	for term, count := range df {
		idf[term] = math.Log(n / float64(count))
	}
	return idf
}

// ComputeTFIDF computes a TF-IDF vector for a single document using
// sublinear TF: TF(word) = 1 + log(count(word)) if count > 0.
// Tokens not present in the IDF map are skipped.
func ComputeTFIDF(tokens []string, idf map[string]float64) map[string]float64 {
	if len(tokens) == 0 {
		return make(map[string]float64)
	}

	// Count term frequencies
	tf := make(map[string]int)
	for _, tok := range tokens {
		tf[tok]++
	}

	// Compute TF-IDF with sublinear TF
	vec := make(map[string]float64)
	for term, count := range tf {
		idfVal, ok := idf[term]
		if !ok {
			continue
		}
		vec[term] = (1.0 + math.Log(float64(count))) * idfVal
	}
	return vec
}
