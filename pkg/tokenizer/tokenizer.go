package tokenizer

import (
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/pkoukk/tiktoken-go"
)

const (
	// DefaultModel is used when CountTokens is called with an empty model name.
	DefaultModel = "gpt-4"

	// DefaultTruncateSuffix matches the Python default.
	DefaultTruncateSuffix = "\n... [truncated]"
)

var (
	tokenizerMu      sync.RWMutex
	tokenizerByModel = map[string]*tiktoken.Tiktoken{}
)

// GetTokenizer returns a tiktoken encoder for the given model, falling back to
// cl100k_base when the model is unknown (same behavior as Python).
func GetTokenizer(model string) (*tiktoken.Tiktoken, error) {
	tokenizerMu.RLock()
	if t, ok := tokenizerByModel[model]; ok {
		tokenizerMu.RUnlock()
		return t, nil
	}
	tokenizerMu.RUnlock()

	tokenizerMu.Lock()
	defer tokenizerMu.Unlock()
	if t, ok := tokenizerByModel[model]; ok {
		return t, nil
	}

	t, err := tiktoken.EncodingForModel(model)
	if err != nil {
		t, err = tiktoken.GetEncoding(tiktoken.MODEL_CL100K_BASE)
		if err != nil {
			return nil, err
		}
	}
	tokenizerByModel[model] = t
	return t, nil
}

// Encode returns token IDs for text using the tokenizer for model (with cl100k_base fallback).
func Encode(text, model string) ([]int, error) {
	tok, err := GetTokenizer(model)
	if err != nil {
		return nil, err
	}
	return tok.Encode(text, nil, nil), nil
}

// CountTokens returns the token count for text. On tokenizer failure it falls
// back to EstimateTokens (Python: if tokenizer then len else estimate).
func CountTokens(text, model string) int {
	if model == "" {
		model = DefaultModel
	}
	tok, err := GetTokenizer(model)
	if err != nil || tok == nil {
		return EstimateTokens(text)
	}
	return len(tok.Encode(text, nil, nil))
}

// EstimateTokens approximates tokens as max(1, runeCount/4), matching Python len(text)//4 on str.
func EstimateTokens(text string) int {
	n := utf8.RuneCountInString(text)
	est := n / 4
	if est < 1 {
		return 1
	}
	return est
}

// TruncateText shortens text to at most maxTokens (including suffix cost).
// suffix defaults to DefaultTruncateSuffix when empty. preserveLines mirrors Python.
func TruncateText(text, model string, maxTokens int, suffix string, preserveLines bool) string {
	if suffix == "" {
		suffix = DefaultTruncateSuffix
	}
	if model == "" {
		model = DefaultModel
	}

	current := CountTokens(text, model)
	if current <= maxTokens {
		return text
	}

	suffixTokens := CountTokens(suffix, model)
	target := maxTokens - suffixTokens
	if target <= 0 {
		return strings.TrimSpace(suffix)
	}

	if preserveLines {
		return truncateByLines(text, target, suffix, model)
	}
	return truncateByRunes(text, target, suffix, model)
}

func truncateByLines(text string, targetTokens int, suffix, model string) string {
	lines := strings.Split(text, "\n")
	var out []string
	current := 0
	for _, line := range lines {
		lineTokens := CountTokens(line+"\n", model)
		if current+lineTokens > targetTokens {
			break
		}
		out = append(out, line)
		current += lineTokens
	}
	if len(out) == 0 {
		return truncateByRunes(text, targetTokens, suffix, model)
	}
	return strings.Join(out, "\n") + suffix
}

func truncateByRunes(text string, targetTokens int, suffix, model string) string {
	rs := []rune(text)
	low, high := 0, len(rs)
	for low < high {
		mid := (low + high + 1) / 2
		chunk := string(rs[:mid])
		if CountTokens(chunk, model) <= targetTokens {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return string(rs[:low]) + suffix
}
