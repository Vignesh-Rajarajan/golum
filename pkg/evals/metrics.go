package evals

import (
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
)

// Metrics is the per-run accounting an eval report compares across harnesses:
// not just whether a run passed, but what it cost to get there.
type Metrics struct {
	Model              string `json:"model,omitempty"`
	LatencyMs          int64  `json:"latency_ms"`
	TimeToFirstTokenMs int64  `json:"time_to_first_token_ms,omitempty"`

	ModelRequests int `json:"model_requests"`
	ToolCalls     int `json:"tool_calls"`
	ToolErrors    int `json:"tool_errors"`

	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
	// TokensReported distinguishes "the model used zero tokens" from "the
	// provider did not tell us", which some providers genuinely do not.
	TokensReported bool `json:"tokens_reported"`
	// EstimatedCostUSD is nil when no price is configured for the model.
	// Reporting nil rather than 0 keeps an unpriced run from looking free.
	EstimatedCostUSD *float64 `json:"estimated_cost_usd,omitempty"`

	StopReasons []string          `json:"stop_reasons,omitempty"`
	Violations  []PolicyViolation `json:"violations,omitempty"`

	// Outcome is the durable operation outcome: completed, failed, or aborted.
	Outcome string `json:"outcome,omitempty"`
	// GuardrailCode is the error code that ended the run when a guardrail
	// tripped: model_limit, tool_errors, or hook.
	GuardrailCode    string `json:"guardrail_code,omitempty"`
	GuardrailMessage string `json:"guardrail_message,omitempty"`
	// Compactions counts context compactions, including overflow recoveries.
	Compactions int `json:"compactions"`
	// NativeToolCount is the number of tool schemas sent on model requests.
	NativeToolCount int `json:"native_tool_count,omitempty"`
}

// ComputeMetrics derives run accounting from the durable records and the
// trajectory. Everything here is read back from what the harness already
// persisted rather than measured by a parallel instrumentation path, so eval
// numbers and production records cannot drift apart.
func ComputeMetrics(r *Result) Metrics {
	if r == nil {
		return Metrics{}
	}
	m := Metrics{
		Model:              r.Usage.Model,
		LatencyMs:          r.Elapsed.Milliseconds(),
		TimeToFirstTokenMs: r.TimeToFirstToken.Milliseconds(),
		InputTokens:        r.Usage.InputTokens,
		OutputTokens:       r.Usage.OutputTokens,
		TotalTokens:        r.Usage.TotalTokens,
		NativeToolCount:    r.NativeToolCount,
	}

	steps := r.Trajectory()
	for _, s := range steps {
		switch s.Kind {
		case StepToolCall:
			m.ToolCalls++
		case StepToolResult:
			if s.IsError {
				m.ToolErrors++
			}
		case StepAssistant:
			if s.StopReason != "" {
				m.StopReasons = append(m.StopReasons, s.StopReason)
			}
		}
	}
	if m.ToolCalls == 0 {
		// A trajectory can be empty when the run failed before any entry was
		// persisted; fall back to the streamed count.
		m.ToolCalls = r.Usage.ToolCalls
	}

	for _, rec := range r.Records {
		switch rec.Type {
		case session.RecordStepAttempt:
			switch rec.Step {
			case "assistant":
				m.ModelRequests++
			case "compaction":
				m.Compactions++
			}
		case session.RecordUsage:
			if rec.Usage != nil && rec.Usage.TotalTokens > 0 {
				m.TokensReported = true
			}
			if rec.Cause == "overflow" {
				m.Compactions++
			}
		case session.RecordOperationFinished:
			m.Outcome = rec.Outcome
			if rec.Error != nil {
				m.GuardrailCode = rec.Error.Code
				m.GuardrailMessage = rec.Error.Message
			}
		}
	}
	if m.TotalTokens > 0 {
		m.TokensReported = true
	}

	m.Violations = resolveViolationSteps(steps, r.Violations)
	if cost, ok := EstimateCost(m.Model, m.InputTokens, m.OutputTokens); ok {
		m.EstimatedCostUSD = &cost
	}
	return m
}

// resolveViolationSteps fills in the trajectory index of each violation by
// matching tool call ids, so a report can point at where the run went off the
// rails rather than just that it did.
func resolveViolationSteps(steps []TrajectoryStep, violations []PolicyViolation) []PolicyViolation {
	if len(violations) == 0 {
		return nil
	}
	index := make(map[string]TrajectoryStep, len(steps))
	for _, s := range steps {
		if s.ToolCallID == "" {
			continue
		}
		// Prefer the call site over the result: that is where the offending
		// arguments are.
		if existing, ok := index[s.ToolCallID]; ok && existing.Kind == StepToolCall {
			continue
		}
		index[s.ToolCallID] = s
	}
	out := make([]PolicyViolation, len(violations))
	copy(out, violations)
	for i := range out {
		s, ok := index[out[i].ToolCallID]
		if !ok {
			continue
		}
		out[i].StepIndex = s.Index
		if out[i].ToolName == "" {
			out[i].ToolName = s.ToolName
		}
		if out[i].Arguments == nil {
			out[i].Arguments = s.Arguments
		}
	}
	return out
}
