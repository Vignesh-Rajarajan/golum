package harness

import (
	"fmt"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm/capability"
)

// Decision is what a director returns when PeekAction is about to finish a turn.
type Decision int

const (
	Pass Decision = iota
	Continue
	Fail
)

// InferenceHint is applied to the next model request only; it is not journaled.
type InferenceHint struct {
	System     string
	ToolChoice llm.ToolChoice
}

// DirectorContext is the journal-derived view directors inspect.
type DirectorContext struct {
	RunID    string
	Intent   session.OperationIntent
	Records  []session.Record
	Entries  []session.Entry
	Attempts int
	Config   LoopConfig
	Caps     capability.Caps
}

// Director intercepts inference and candidate completion without editing
// Driver.RunToCompletion. Keep this surface small: Pass/Continue/Fail is
// enough for ForceTool.
type Director interface {
	Name() string
	BeforeInference(dc DirectorContext) InferenceHint
	AfterAssistant(dc DirectorContext) (Decision, string)
}

// DefaultDirectors installs ForceTool when the loop requires one.
func DefaultDirectors(cfg LoopConfig) []Director {
	if cfg.ForceTool == "" {
		return nil
	}
	return []Director{ForceToolDirector{}}
}

func (d *Driver) directorsFor(intent session.OperationIntent) []Director {
	if d.deps.Directors != nil {
		return d.deps.Directors
	}
	cfg := d.cfg
	if cfg.ForceTool == "" {
		cfg.ForceTool = intent.ForceTool
	}
	return DefaultDirectors(cfg)
}

func (d *Driver) capRegistry() *capability.Registry {
	if d.deps.Caps != nil {
		return d.deps.Caps
	}
	return capability.Default()
}

func (d *Driver) resolveCaps() capability.Caps {
	model := d.deps.Model
	provider := "openai"
	if d.deps.Client != nil {
		provider = d.deps.Client.Provider()
		if model == "" {
			model = d.deps.Client.Model()
		}
	}
	return d.capRegistry().Resolve(capability.Selector{
		Provider: provider, Model: model, Transport: "chat_completions",
	})
}

func (d *Driver) directorContext(op *OperationState, records []session.Record) DirectorContext {
	intent := session.OperationIntent{}
	runID := ""
	if op != nil {
		intent = op.Intent
		runID = op.ID
	}
	entries := []session.Entry{}
	if d.deps.Session != nil {
		entries = d.deps.Session.Entries()
	}
	return DirectorContext{
		RunID:    runID,
		Intent:   intent,
		Records:  records,
		Entries:  entries,
		Attempts: assistantAttempts(records, runID),
		Config:   d.cfg,
		Caps:     d.resolveCaps(),
	}
}

func assistantAttempts(records []session.Record, runID string) int {
	n := 0
	for _, r := range records {
		if r.RunID == runID && r.Type == session.RecordStepAttempt && r.Step == "assistant" {
			n++
		}
	}
	return n
}

func successfulToolResult(entries []session.Entry, records []session.Record, runID, name string) bool {
	ids := map[string]bool{}
	for _, r := range records {
		if r.RunID != runID || r.Type != session.RecordToolStarted || r.ToolName != name {
			continue
		}
		ids[r.ResultEntryID] = true
	}
	byID := map[string]session.Entry{}
	for _, e := range entries {
		byID[e.ID] = e
	}
	for id := range ids {
		e, ok := byID[id]
		if !ok || e.Kind != session.EntryToolResult {
			continue
		}
		isErr, _ := e.Meta["is_error"].(bool)
		if !isErr {
			return true
		}
	}
	return false
}

func forceToolName(intent session.OperationIntent, cfg LoopConfig) string {
	if intent.ForceTool != "" {
		return intent.ForceTool
	}
	return cfg.ForceTool
}

func forceToolAttempts(intent session.OperationIntent, cfg LoopConfig) int {
	if intent.ForceToolAttempts > 0 {
		return intent.ForceToolAttempts
	}
	if cfg.ForceToolAttempts > 0 {
		return cfg.ForceToolAttempts
	}
	return 2
}

// ForceToolDirector keeps a required tool from being skipped. Soft instruction
// first; native tool_choice only when the capability registry says it is safe.
type ForceToolDirector struct{}

func (ForceToolDirector) Name() string { return "force_tool" }

func (ForceToolDirector) BeforeInference(dc DirectorContext) InferenceHint {
	name := forceToolName(dc.Intent, dc.Config)
	if name == "" {
		return InferenceHint{}
	}
	hint := InferenceHint{
		System: fmt.Sprintf("You must successfully call the %s tool before you finish. Do not end the turn without a matching successful invocation.", name),
	}
	if dc.Attempts > 1 && dc.Caps.ForcedToolChoice == capability.Supported {
		hint.ToolChoice = llm.ToolChoice{Name: name}
	}
	return hint
}

func (ForceToolDirector) AfterAssistant(dc DirectorContext) (Decision, string) {
	name := forceToolName(dc.Intent, dc.Config)
	if name == "" {
		return Pass, ""
	}
	if successfulToolResult(dc.Entries, dc.Records, dc.RunID, name) {
		return Pass, ""
	}
	if dc.Attempts < forceToolAttempts(dc.Intent, dc.Config) {
		return Continue, ""
	}
	return Fail, fmt.Sprintf("required tool %s was not invoked successfully", name)
}

func intentFromRecords(records []session.Record, runID string) session.OperationIntent {
	for _, r := range records {
		if r.RunID == runID && r.Type == session.RecordOperationStarted && r.Intent != nil {
			return *r.Intent
		}
	}
	return session.OperationIntent{}
}
