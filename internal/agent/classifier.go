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
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"sync"
)

// ErrNotFinalized is returned when Predict is called before Finalize.
var ErrNotFinalized = errors.New("CNBClassifier.Predict called before Finalize()")

// ClassScore holds a class name and its probability score.
type ClassScore struct {
	Name        string  `json:"name"`
	Probability float64 `json:"probability"`
}

// CNBClassifier implements Complement Naive Bayes text classification
// with WCNB normalization (Rennie et al. 2003).
type CNBClassifier struct {
	ClassCounts map[string]map[string]float64 `json:"class_counts"`
	ClassTotals map[string]float64            `json:"class_totals"`
	ClassDocs   map[string]int                `json:"class_docs"`
	Vocabulary  map[string]bool               `json:"vocabulary"`
	Alpha       float64                       `json:"alpha"`
	Temperature float64                       `json:"temperature"`
	Weights     map[string]map[string]float64 `json:"-"` // lazily materialized via WeightsMap()

	// Internal slice-based representation for fast Finalize/Predict.
	vocabIndex   map[string]int // unexported: token -> contiguous index for slice ops
	classNames   []string       // ordered class names
	weightMatrix [][]float64    // [classIdx][vocabIdx] = normalized weight
	finalized    bool
	mu           sync.RWMutex // protects finalized and weightMatrix for concurrent Predict
}

// NewCNBClassifier creates a new Complement Naive Bayes classifier.
// alpha controls Laplace smoothing; temperature controls softmax sharpness.
func NewCNBClassifier(alpha, temperature float64) *CNBClassifier {
	return &CNBClassifier{
		ClassCounts: make(map[string]map[string]float64),
		ClassTotals: make(map[string]float64),
		ClassDocs:   make(map[string]int),
		Vocabulary:  make(map[string]bool),
		vocabIndex:  make(map[string]int),
		Alpha:       alpha,
		Temperature: temperature,
	}
}

// Fit adds training data for a class. tokens maps term -> TF-IDF weight.
// weight scales the contribution of this document.
func (c *CNBClassifier) Fit(class string, tokens map[string]float64, weight float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ClassCounts[class] == nil {
		c.ClassCounts[class] = make(map[string]float64)
	}
	for token, value := range tokens {
		scaled := value * weight
		c.ClassCounts[class][token] += scaled
		c.ClassTotals[class] += scaled
		if !c.Vocabulary[token] {
			c.vocabIndex[token] = len(c.vocabIndex)
			c.Vocabulary[token] = true
		}
	}
	c.ClassDocs[class]++
	c.finalized = false
	c.Weights = nil // invalidate cached map form
}

// Finalize computes complement weights with WCNB normalization.
// Must be called after all Fit/AddTrainingData calls and before Predict.
func (c *CNBClassifier) Finalize() {
	c.mu.Lock()
	defer c.mu.Unlock()
	numClasses := len(c.ClassCounts)
	vocabSize := len(c.vocabIndex)
	fVocabSize := float64(vocabSize)
	alpha := c.Alpha

	// Build class list.
	c.classNames = make([]string, 0, numClasses)
	for class := range c.ClassCounts {
		c.classNames = append(c.classNames, class)
	}

	// Compute numerators[vi] = alpha + totalCounts[vi] from ClassCounts maps.
	numerators := make([]float64, vocabSize)
	for vi := range vocabSize {
		numerators[vi] = alpha
	}
	grandTotal := 0.0
	for class, counts := range c.ClassCounts {
		for token, count := range counts {
			numerators[c.vocabIndex[token]] += count
		}
		grandTotal += c.ClassTotals[class]
	}

	// Precompute base log values: log(alpha + totalCounts[vi]) for all vocab.
	baseLog := make([]float64, vocabSize)
	for vi := range vocabSize {
		baseLog[vi] = math.Log(numerators[vi])
	}

	// Precompute per-class log denominators.
	logDenoms := make([]float64, numClasses)
	for ci, class := range c.classNames {
		complementTotal := grandTotal - c.ClassTotals[class]
		logDenoms[ci] = math.Log(alpha*fVocabSize + complementTotal)
	}

	// Allocate weight matrix as one contiguous block.
	allWeights := make([]float64, numClasses*vocabSize)
	c.weightMatrix = make([][]float64, numClasses)
	for ci := range numClasses {
		c.weightMatrix[ci] = allWeights[ci*vocabSize : (ci+1)*vocabSize : (ci+1)*vocabSize]
	}

	// Compute WCNB weights per class in parallel.
	// Key optimization: reduce math.Log from 180K to ~24K calls by
	// precomputing baseLog once and only correcting per-class tokens.
	// Each class writes to an independent slice, reads shared data.
	var wg sync.WaitGroup
	wg.Add(numClasses)
	for ci, class := range c.classNames {
		go func(ci int, class string) {
			defer wg.Done()
			weights := c.weightMatrix[ci]
			logD := logDenoms[ci]
			counts := c.ClassCounts[class]
			vocab := c.vocabIndex

			// Fused pass: compute base weight and accumulate L1 norm.
			l1Norm := 0.0
			for vi := range vocabSize {
				w := baseLog[vi] - logD
				weights[vi] = w
				l1Norm += math.Abs(w)
			}

			// Correct tokens present in this class (~750 per class) and adjust L1.
			for token, count := range counts {
				vi := vocab[token]
				oldW := weights[vi]
				newW := math.Log(numerators[vi]-count) - logD
				weights[vi] = newW
				l1Norm += math.Abs(newW) - math.Abs(oldW)
			}

			// WCNB normalize.
			if l1Norm > 0 {
				invL1 := 1.0 / l1Norm
				for vi := range vocabSize {
					weights[vi] *= invL1
				}
			}
		}(ci, class)
	}
	wg.Wait()

	c.finalized = true
	c.Weights = nil // invalidate stale map form
}

// WeightsMap returns the weight matrix in map form for inspection/testing.
// This is lazily computed and cached.
func (c *CNBClassifier) WeightsMap() map[string]map[string]float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Weights != nil {
		return c.Weights
	}
	if !c.finalized {
		return nil
	}

	// Build inverse vocab index.
	vocabTerms := make([]string, len(c.vocabIndex))
	for term, idx := range c.vocabIndex {
		vocabTerms[idx] = term
	}

	c.Weights = make(map[string]map[string]float64, len(c.classNames))
	for ci, class := range c.classNames {
		weights := make(map[string]float64, len(vocabTerms))
		for vi, term := range vocabTerms {
			weights[term] = c.weightMatrix[ci][vi]
		}
		c.Weights[class] = weights
	}
	return c.Weights
}

// Predict returns class probabilities for the given token vector.
// tokens maps term -> TF-IDF weight. Returns ErrNotFinalized if Finalize
// has not been called.
func (c *CNBClassifier) Predict(tokens map[string]float64) ([]ClassScore, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.finalized {
		return nil, ErrNotFinalized
	}
	if len(c.classNames) == 0 {
		return nil, nil
	}

	numClasses := len(c.classNames)

	// Score each class using slice-based weights.
	rawScores := make([]float64, numClasses)
	maxScore := math.Inf(-1)
	for word, tfidf := range tokens {
		vi, ok := c.vocabIndex[word]
		if !ok {
			continue
		}
		for ci := range numClasses {
			rawScores[ci] -= tfidf * c.weightMatrix[ci][vi]
		}
	}
	for ci := range numClasses {
		if rawScores[ci] > maxScore {
			maxScore = rawScores[ci]
		}
	}

	// Softmax with temperature (subtract max for numerical stability).
	scores := make([]ClassScore, numClasses)
	sumExp := 0.0
	for ci := range numClasses {
		exp := math.Exp((rawScores[ci] - maxScore) / c.Temperature)
		rawScores[ci] = exp
		sumExp += exp
		scores[ci].Name = c.classNames[ci]
	}
	for ci := range numClasses {
		scores[ci].Probability = rawScores[ci] / sumExp
	}

	// Sort descending by probability, alphabetical tie-breaking.
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].Probability != scores[j].Probability {
			return scores[i].Probability > scores[j].Probability
		}
		return scores[i].Name < scores[j].Name
	})

	return scores, nil
}

// RemoveClass removes a class and rebuilds the vocabulary.
func (c *CNBClassifier) RemoveClass(class string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.ClassCounts, class)
	delete(c.ClassTotals, class)
	delete(c.ClassDocs, class)

	// Rebuild vocabulary from remaining classes with new contiguous indices.
	c.Vocabulary = make(map[string]bool)
	c.vocabIndex = make(map[string]int)
	for _, counts := range c.ClassCounts {
		for token := range counts {
			if !c.Vocabulary[token] {
				c.vocabIndex[token] = len(c.vocabIndex)
				c.Vocabulary[token] = true
			}
		}
	}
	c.finalized = false
	c.Weights = nil
}

// AddTrainingData accumulates additional training data for a class.
// Same logic as Fit — accumulate counts and mark as unfinalized.
func (c *CNBClassifier) AddTrainingData(class string, tokens map[string]float64, weight float64) {
	c.Fit(class, tokens, weight)
}

// jsonEscapeString writes a JSON-escaped string (with quotes) to buf.
func jsonEscapeString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if c < 0x20 {
				buf.WriteString(`\u00`)
				buf.WriteByte("0123456789abcdef"[c>>4])
				buf.WriteByte("0123456789abcdef"[c&0xf])
			} else {
				buf.WriteByte(c)
			}
		}
	}
	buf.WriteByte('"')
}

// MarshalJSON serializes the classifier state (excluding computed weights).
// Uses a custom encoder for performance. The wire format matches the
// plan contract: vocabulary as map[string]bool, class_counts as
// map[string]map[string]float64.
func (c *CNBClassifier) MarshalJSON() ([]byte, error) {
	estimatedEntries := 0
	for _, counts := range c.ClassCounts {
		estimatedEntries += len(counts)
	}
	buf := bytes.NewBuffer(make([]byte, 0, estimatedEntries*24+len(c.Vocabulary)*16+256))

	floatBuf := make([]byte, 0, 32)

	// ClassCounts as map[string]map[string]float64.
	buf.WriteString(`{"class_counts":{`)
	firstClass := true
	for class, counts := range c.ClassCounts {
		if !firstClass {
			buf.WriteByte(',')
		}
		firstClass = false
		jsonEscapeString(buf, class)
		buf.WriteString(`:{`)
		firstToken := true
		for token, count := range counts {
			if !firstToken {
				buf.WriteByte(',')
			}
			firstToken = false
			jsonEscapeString(buf, token)
			buf.WriteByte(':')
			floatBuf = strconv.AppendFloat(floatBuf[:0], count, 'g', -1, 64)
			buf.Write(floatBuf)
		}
		buf.WriteByte('}')
	}

	// ClassTotals as map[string]float64.
	buf.WriteString(`},"class_totals":{`)
	firstClass = true
	for class, total := range c.ClassTotals {
		if !firstClass {
			buf.WriteByte(',')
		}
		firstClass = false
		jsonEscapeString(buf, class)
		buf.WriteByte(':')
		floatBuf = strconv.AppendFloat(floatBuf[:0], total, 'g', -1, 64)
		buf.Write(floatBuf)
	}

	// ClassDocs as map[string]int.
	buf.WriteString(`},"class_docs":{`)
	firstClass = true
	for class, docs := range c.ClassDocs {
		if !firstClass {
			buf.WriteByte(',')
		}
		firstClass = false
		jsonEscapeString(buf, class)
		buf.WriteByte(':')
		floatBuf = strconv.AppendInt(floatBuf[:0], int64(docs), 10)
		buf.Write(floatBuf)
	}

	// Vocabulary as map[string]bool.
	buf.WriteString(`},"vocabulary":{`)
	first := true
	for term := range c.Vocabulary {
		if !first {
			buf.WriteByte(',')
		}
		first = false
		jsonEscapeString(buf, term)
		buf.WriteString(`:true`)
	}

	buf.WriteString(`},"alpha":`)
	floatBuf = strconv.AppendFloat(floatBuf[:0], c.Alpha, 'g', -1, 64)
	buf.Write(floatBuf)
	buf.WriteString(`,"temperature":`)
	floatBuf = strconv.AppendFloat(floatBuf[:0], c.Temperature, 'g', -1, 64)
	buf.Write(floatBuf)
	buf.WriteByte('}')

	return buf.Bytes(), nil
}

// UnmarshalJSON deserializes the classifier state. Caller must call Finalize()
// after unmarshaling before using Predict. The wire format uses vocabulary as
// map[string]bool and class_counts as map[string]map[string]float64.
//
// Uses a single-pass byte scanner to split top-level fields and targeted
// parsers for each field, avoiding reflection overhead from json.Unmarshal.
func (c *CNBClassifier) UnmarshalJSON(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Weights = nil
	c.finalized = false

	fields, err := splitJSONObject(data)
	if err != nil {
		return fmt.Errorf("unmarshal CNBClassifier: %w", err)
	}

	// Validate required top-level fields.
	required := []string{"alpha", "temperature", "vocabulary", "class_totals", "class_docs", "class_counts"}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return fmt.Errorf("unmarshal CNBClassifier: missing required field %q", key)
		}
	}

	var parseErr error

	if raw, ok := fields["alpha"]; ok {
		c.Alpha, parseErr = strconv.ParseFloat(string(raw), 64)
		if parseErr != nil {
			return fmt.Errorf("unmarshal CNBClassifier: invalid alpha value: %w", parseErr)
		}
	}
	if raw, ok := fields["temperature"]; ok {
		c.Temperature, parseErr = strconv.ParseFloat(string(raw), 64)
		if parseErr != nil {
			return fmt.Errorf("unmarshal CNBClassifier: invalid temperature value: %w", parseErr)
		}
	}

	if raw, ok := fields["vocabulary"]; ok {
		c.Vocabulary, err = parseVocabJSON(raw)
		if err != nil {
			return fmt.Errorf("unmarshal CNBClassifier: vocabulary: %w", err)
		}
	}

	// Build vocabIndex from parsed vocabulary with contiguous indices.
	c.vocabIndex = make(map[string]int, len(c.Vocabulary))
	idx := 0
	for term := range c.Vocabulary {
		c.vocabIndex[term] = idx
		idx++
	}

	if raw, ok := fields["class_totals"]; ok {
		c.ClassTotals, err = parseFloat64MapJSON(raw)
		if err != nil {
			return fmt.Errorf("unmarshal CNBClassifier: class_totals: %w", err)
		}
	}
	if raw, ok := fields["class_docs"]; ok {
		c.ClassDocs, err = parseIntMapJSON(raw)
		if err != nil {
			return fmt.Errorf("unmarshal CNBClassifier: class_docs: %w", err)
		}
	}
	if raw, ok := fields["class_counts"]; ok {
		c.ClassCounts, err = parseNestedMapJSON(raw)
		if err != nil {
			return fmt.Errorf("unmarshal CNBClassifier: class_counts: %w", err)
		}
	}

	// Validate all class_counts tokens exist in vocabulary.
	for class, counts := range c.ClassCounts {
		for token := range counts {
			if !c.Vocabulary[token] {
				return fmt.Errorf("unmarshal CNBClassifier: class_counts[%q] contains token %q not present in vocabulary", class, token)
			}
		}
	}

	return nil
}

// splitJSONObject splits a top-level JSON object into field name → raw value bytes.
func splitJSONObject(data []byte) (map[string][]byte, error) {
	result := make(map[string][]byte, 8)
	i, n := 0, len(data)

	// Skip to opening {.
	for i < n && data[i] != '{' {
		if data[i] > ' ' {
			return nil, fmt.Errorf("expected '{', got %q at position %d", data[i], i)
		}
		i++
	}
	if i >= n {
		return nil, fmt.Errorf("unexpected end of input: expected '{'")
	}
	i++

	for i < n {
		// Skip whitespace and commas.
		for i < n && (data[i] <= ' ' || data[i] == ',') {
			i++
		}
		if i >= n {
			return nil, fmt.Errorf("unexpected end of input: expected closing '}'")
		}
		if data[i] == '}' {
			i++ // consume closing }
			// Reject trailing non-whitespace.
			for i < n {
				if data[i] > ' ' {
					return nil, fmt.Errorf("unexpected trailing byte %q at position %d after root object", data[i], i)
				}
				i++
			}
			return result, nil
		}

		// Parse key string.
		if data[i] != '"' {
			return nil, fmt.Errorf("expected '\"' for key, got %q at position %d", data[i], i)
		}
		i++
		keyStart := i
		for i < n && data[i] != '"' {
			if data[i] == '\\' {
				i++
			}
			i++
		}
		if i >= n {
			return nil, fmt.Errorf("unterminated string key starting at position %d", keyStart-1)
		}
		key := string(data[keyStart:i])
		i++ // closing "

		// Skip : and whitespace.
		for i < n && (data[i] == ':' || data[i] <= ' ') {
			i++
		}

		// Find value extent.
		valueStart := i
		i = skipJSONValue(data, i)
		result[key] = data[valueStart:i]
	}

	return nil, fmt.Errorf("unexpected end of input: expected closing '}'")

}

// skipJSONValue advances past a single JSON value starting at position i.
func skipJSONValue(data []byte, i int) int {
	n := len(data)
	if i >= n {
		return i
	}
	switch data[i] {
	case '{', '[':
		depth := 1
		i++
		inStr := false
		for i < n && depth > 0 {
			if inStr {
				if data[i] == '\\' {
					i += 2
					continue
				}
				if data[i] == '"' {
					inStr = false
				}
			} else {
				switch data[i] {
				case '"':
					inStr = true
				case '{', '[':
					depth++
				case '}', ']':
					depth--
				}
			}
			i++
		}
	case '"':
		i++
		for i < n {
			if data[i] == '\\' {
				i += 2
				continue
			}
			if data[i] == '"' {
				i++
				return i
			}
			i++
		}
	default:
		// number, bool, null
		for i < n && data[i] != ',' && data[i] != '}' && data[i] != ']' && data[i] > ' ' {
			i++
		}
	}
	return i
}

// parseIntMapJSON parses {"key":123,...} into map[string]int.
func parseIntMapJSON(data []byte) (map[string]int, error) {
	m := make(map[string]int)
	i, n := 0, len(data)

	// Skip whitespace to find opening {.
	for i < n && data[i] <= ' ' {
		i++
	}
	if i >= n || data[i] != '{' {
		return nil, fmt.Errorf("expected '{', got %q", string(data))
	}
	i++

	for i < n {
		for i < n && data[i] != '"' {
			if data[i] == '}' {
				return m, nil
			}
			i++
		}
		if i >= n {
			break
		}
		i++
		start := i
		for i < n && data[i] != '"' {
			if data[i] == '\\' {
				i++
			}
			i++
		}
		if i >= n {
			return nil, fmt.Errorf("unterminated string in int map")
		}
		key := string(data[start:i])
		i++
		for i < n && (data[i] == ':' || data[i] <= ' ') {
			i++
		}
		start = i
		for i < n && data[i] != ',' && data[i] != '}' {
			i++
		}
		val, err := strconv.Atoi(string(bytes.TrimSpace(data[start:i])))
		if err != nil {
			return nil, fmt.Errorf("invalid int value for key %q: %w", key, err)
		}
		m[key] = val
		if i < n && data[i] == ',' {
			i++
		}
	}
	return m, nil
}

// parseNestedMapJSON parses {"class":{"key":1.23,...},...} into nested maps.
func parseNestedMapJSON(data []byte) (map[string]map[string]float64, error) {
	result := make(map[string]map[string]float64)
	i, n := 0, len(data)

	// Skip whitespace to find opening {.
	for i < n && data[i] <= ' ' {
		i++
	}
	if i >= n || data[i] != '{' {
		return nil, fmt.Errorf("expected '{', got %q", string(data))
	}
	i++

	for i < n {
		// Skip whitespace and commas.
		for i < n && (data[i] <= ' ' || data[i] == ',') {
			i++
		}
		if i >= n || data[i] == '}' {
			break
		}
		// Parse class name.
		if data[i] != '"' {
			return nil, fmt.Errorf("expected '\"' for class key, got %q", data[i])
		}
		i++
		start := i
		for i < n && data[i] != '"' {
			if data[i] == '\\' {
				i++
			}
			i++
		}
		if i >= n {
			return nil, fmt.Errorf("unterminated class key string")
		}
		className := string(data[start:i])
		i++ // closing "

		// Skip : and whitespace.
		for i < n && (data[i] == ':' || data[i] <= ' ') {
			i++
		}

		// Find inner object extent and parse it.
		innerStart := i
		i = skipJSONValue(data, i)
		inner, err := parseFloat64MapJSON(data[innerStart:i])
		if err != nil {
			return nil, fmt.Errorf("class %q: %w", className, err)
		}
		result[className] = inner
	}

	// Consume closing } and reject trailing non-whitespace.
	if i < n && data[i] == '}' {
		i++
	}
	for i < n {
		if data[i] > ' ' {
			return nil, fmt.Errorf("unexpected trailing bytes in class_counts at position %d", i)
		}
		i++
	}
	return result, nil
}

// parseVocabJSON parses {"term":true,"term2":true,...} into map[string]bool.
// Rejects duplicate keys.
func parseVocabJSON(data []byte) (map[string]bool, error) {
	m := make(map[string]bool, 8192)
	i := 0
	n := len(data)

	// Skip whitespace to find opening {.
	for i < n && data[i] <= ' ' {
		i++
	}
	if i >= n || data[i] != '{' {
		return nil, fmt.Errorf("expected '{', got %q", string(data))
	}
	i++

	for i < n {
		// Skip whitespace and commas.
		for i < n && (data[i] <= ' ' || data[i] == ',') {
			i++
		}
		if i >= n || data[i] == '}' {
			break
		}

		// Find opening quote of key.
		if data[i] != '"' {
			return nil, fmt.Errorf("expected '\"' for vocab key, got %q at position %d", data[i], i)
		}
		i++ // skip opening quote

		// Scan key until closing quote (handle escapes).
		start := i
		for i < n && data[i] != '"' {
			if data[i] == '\\' {
				i++
			}
			i++
		}
		if i >= n {
			return nil, fmt.Errorf("unterminated vocab key string")
		}
		key := string(data[start:i])
		i++ // skip closing quote

		// Reject duplicate vocabulary keys.
		if m[key] {
			return nil, fmt.Errorf("duplicate vocabulary key %q", key)
		}

		// Skip :true (expect colon + "true")
		for i < n && (data[i] == ':' || data[i] <= ' ') {
			i++
		}
		// Validate value is "true"
		if i+4 <= n && string(data[i:i+4]) == "true" {
			i += 4
		} else {
			// Extract what the value actually is for the error message.
			valStart := i
			for i < n && data[i] != ',' && data[i] != '}' {
				i++
			}
			return nil, fmt.Errorf("vocabulary value for key %q must be true, got %q", key, string(data[valStart:i]))
		}

		m[key] = true

		// Skip comma/whitespace.
		for i < n && (data[i] <= ' ' || data[i] == ',') {
			i++
		}
	}
	// Consume closing } and reject trailing non-whitespace.
	if i < n && data[i] == '}' {
		i++
	}
	for i < n {
		if data[i] > ' ' {
			return nil, fmt.Errorf("unexpected trailing bytes in vocabulary at position %d", i)
		}
		i++
	}
	return m, nil
}

// parseFloat64MapJSON parses {"key":1.23,"key2":4.56,...} into map[string]float64.
func parseFloat64MapJSON(data []byte) (map[string]float64, error) {
	// Pre-size: typical entry is ~25 bytes ("key":value,).
	est := len(data) / 25
	if est < 8 {
		est = 8
	}
	m := make(map[string]float64, est)
	i := 0
	n := len(data)

	// Skip whitespace to find opening {.
	for i < n && data[i] <= ' ' {
		i++
	}
	if i >= n || data[i] != '{' {
		return nil, fmt.Errorf("expected '{', got %q", string(data))
	}
	i++

	for i < n {
		// Find opening quote of key.
		for i < n && data[i] != '"' {
			if data[i] == '}' {
				return m, nil
			}
			i++
		}
		if i >= n {
			break
		}
		i++ // skip opening quote

		// Scan key until closing quote.
		start := i
		for i < n && data[i] != '"' {
			if data[i] == '\\' {
				i++
			}
			i++
		}
		if i >= n {
			return nil, fmt.Errorf("unterminated string in float64 map")
		}
		key := string(data[start:i])
		i++ // skip closing quote

		// Skip colon and whitespace.
		for i < n && (data[i] == ':' || data[i] == ' ') {
			i++
		}

		// Scan number value.
		start = i
		for i < n && data[i] != ',' && data[i] != '}' {
			i++
		}
		val, err := strconv.ParseFloat(string(bytes.TrimSpace(data[start:i])), 64)
		if err != nil {
			return nil, fmt.Errorf("invalid float64 value for key %q: %w", key, err)
		}
		m[key] = val

		if i < n && data[i] == ',' {
			i++
		}
	}
	return m, nil
}

// SelectByThreshold selects classes using a two-tier strategy:
//  1. The top-ranked class is always included (the caller ensures vocabulary
//     overlap via ComputeTFIDF, so the top class has the best available signal).
//  2. Additional classes (2nd, 3rd) are included only if their lift
//     (probability / uniform) meets liftThreshold.
//
// This is necessary because WCNB L1 normalization dilutes per-token weight
// in large vocabularies (~8000 terms), making softmax probabilities nearly
// uniform even when the classifier correctly ranks the top class. A
// lift-only approach produces zero selections for queries with few
// vocabulary-matching tokens.
//
// Scores are sorted descending by probability with alphabetical tie-breaking.
func SelectByThreshold(scores []ClassScore, liftThreshold float64, maxSelections int) []string {
	if len(scores) == 0 {
		return nil
	}

	// Enforce deterministic ordering: descending probability, alphabetical ties.
	sorted := make([]ClassScore, len(scores))
	copy(sorted, scores)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Probability != sorted[j].Probability {
			return sorted[i].Probability > sorted[j].Probability
		}
		return sorted[i].Name < sorted[j].Name
	})

	// Always include the top class.
	selected := []string{sorted[0].Name}

	// Additional classes must exceed lift threshold.
	uniform := 1.0 / float64(len(scores))
	for i := 1; i < len(sorted) && len(selected) < maxSelections; i++ {
		lift := sorted[i].Probability / uniform
		if lift >= liftThreshold {
			selected = append(selected, sorted[i].Name)
		}
	}
	return selected
}
