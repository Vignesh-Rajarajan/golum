package evals

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/sashabaranov/go-openai"
)

// Score is a [0,1] judgment with a short rationale.
type Score struct {
	Value     float64
	Rationale string
}

// Judge scores a harness Result against the original input.
type Judge func(ctx context.Context, result *Result, input string) (Score, error)

// Equals returns 1 when result.Output exactly equals expected, else 0.
func Equals(expected string) Judge {
	return func(_ context.Context, result *Result, _ string) (Score, error) {
		if result == nil {
			return Score{Value: 0, Rationale: "nil result"}, nil
		}
		if result.Output == expected {
			return Score{Value: 1, Rationale: "exact match"}, nil
		}
		return Score{Value: 0, Rationale: fmt.Sprintf("got %q want %q", result.Output, expected)}, nil
	}
}

// Contains returns 1 when result.Output contains substr.
func Contains(substr string) Judge {
	return func(_ context.Context, result *Result, _ string) (Score, error) {
		if result == nil {
			return Score{Value: 0, Rationale: "nil result"}, nil
		}
		if strings.Contains(result.Output, substr) {
			return Score{Value: 1, Rationale: "substring present"}, nil
		}
		return Score{Value: 0, Rationale: fmt.Sprintf("%q not found in output", substr)}, nil
	}
}

// ToolCalled returns 1 when a matching tool_call/tool_result pair appears in the transcript.
// If match is nil, any call to name counts as a pass (result content is ignored).
func ToolCalled(name string, match func(args map[string]any, result string) bool) Judge {
	return func(_ context.Context, result *Result, _ string) (Score, error) {
		if result == nil {
			return Score{Value: 0, Rationale: "nil result"}, nil
		}
		var pendingArgs map[string]any
		var pendingID string
		for _, ev := range result.Events {
			switch ev.Kind {
			case "tool_call":
				if ev.Name == name {
					pendingArgs = ev.Arguments
					pendingID = ev.ToolCallID
					if match == nil {
						return Score{Value: 1, Rationale: fmt.Sprintf("tool %q called", name)}, nil
					}
				}
			case "tool_result":
				if pendingID != "" && (ev.ToolCallID == pendingID || ev.Name == name) {
					if match == nil || match(pendingArgs, ev.Content) {
						return Score{Value: 1, Rationale: fmt.Sprintf("tool %q matched", name)}, nil
					}
					pendingID = ""
					pendingArgs = nil
				}
			}
		}
		// match==nil already returned on tool_call; if we get here with match set, no pair matched
		if match == nil {
			return Score{Value: 0, Rationale: fmt.Sprintf("tool %q not called", name)}, nil
		}
		return Score{Value: 0, Rationale: fmt.Sprintf("tool %q call/result did not match", name)}, nil
	}
}

// NewLLMJudge asks the model to score a run against a rubric, expecting JSON
// {"score": 0|1, "rationale": "..."}.
func NewLLMJudge(client *llm.Client, model, rubric string) Judge {
	return func(ctx context.Context, result *Result, input string) (Score, error) {
		if client == nil {
			return Score{}, fmt.Errorf("llm judge: nil client")
		}
		if result == nil {
			return Score{Value: 0, Rationale: "nil result"}, nil
		}
		summary := compactTranscript(result)
		system := `You are an eval judge. Score whether the agent run satisfies the rubric.
Reply with ONLY a JSON object: {"score": <number>, "rationale": "short reason"}.
score is a number from 0 to 1 reflecting how well the rubric was satisfied:
1 means fully satisfied, 0.5 means partially satisfied, 0 means not attempted
or clearly failed. Use 1 only when every requirement in the rubric was met.`
		user := fmt.Sprintf("Rubric:\n%s\n\nInput:\n%s\n\nTranscript summary:\n%s\n\nFinal output:\n%s\n",
			rubric, input, summary, result.Output)

		msgs := []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: system},
			{Role: openai.ChatMessageRoleUser, Content: user},
		}
		opts := llm.ChatCompletionOptions{Model: model, Stream: false, MaxRetries: 2}
		var content strings.Builder
		for ev := range client.ChatCompletion(ctx, msgs, opts) {
			switch ev.Type {
			case llm.EventTypeContentDelta:
				content.WriteString(ev.Content)
			case llm.EventTypeError:
				if ev.Error != nil {
					return Score{}, fmt.Errorf("llm judge: %w", ev.Error)
				}
			}
		}
		return parseJudgeJSON(content.String())
	}
}

func compactTranscript(result *Result) string {
	var b strings.Builder
	for _, ev := range result.Events {
		switch ev.Kind {
		case "tool_call":
			args, _ := json.Marshal(ev.Arguments)
			fmt.Fprintf(&b, "tool_call name=%s args=%s\n", ev.Name, string(args))
		case "tool_result":
			fmt.Fprintf(&b, "tool_result name=%s error=%v content=%s\n", ev.Name, ev.IsError, truncate(ev.Content, 500))
		case "message":
			if ev.Role == "assistant" && ev.Content != "" {
				fmt.Fprintf(&b, "assistant: %s\n", truncate(ev.Content, 500))
			}
		}
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func parseJudgeJSON(raw string) (Score, error) {
	obj, ok := extractJSONObject(raw)
	if !ok {
		return Score{}, fmt.Errorf("llm judge: no JSON object in response: %q", truncate(raw, 200))
	}
	var parsed struct {
		Score     float64 `json:"score"`
		Rationale string  `json:"rationale"`
	}
	if err := json.Unmarshal([]byte(obj), &parsed); err != nil {
		return Score{}, fmt.Errorf("llm judge: parse %q: %w", truncate(obj, 200), err)
	}
	// Clamp to the valid range only — a judge model may legitimately report
	// graded confidence (0.5, 0.75, ...), and forcing every value to a hard 0
	// or 1 here would make LiftReport's AvgScore mathematically identical to
	// its PassRate. PassRate still treats only Value >= 1 as a pass (matching
	// pi's "at least 1 counts as a pass" convention); AvgScore keeps the nuance.
	switch {
	case parsed.Score < 0:
		parsed.Score = 0
	case parsed.Score > 1:
		parsed.Score = 1
	}
	return Score{Value: parsed.Score, Rationale: parsed.Rationale}, nil
}

// extractJSONObject pulls the first balanced {...} from s, tolerating markdown fences.
func extractJSONObject(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		if j := strings.LastIndex(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
	}
	start := strings.Index(s, "{")
	if start < 0 {
		return "", false
	}
	depth := 0
	inString := false
	escape := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inString {
			if escape {
				escape = false
				continue
			}
			if c == '\\' {
				escape = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}
