package session

import (
	"fmt"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/contextmgr"
)

type RecordType string

const (
	RecordOperationStarted  RecordType = "operation_started"
	RecordAbortRequested    RecordType = "abort_requested"
	RecordOperationFinished RecordType = "operation_finished"
	RecordStepAttempt       RecordType = "step_attempt"
	RecordToolStarted       RecordType = "tool_started"
	RecordQueueEnqueued     RecordType = "queue_enqueued"
	RecordQueueCancelled    RecordType = "queue_cancelled"
	RecordWriteDeferred     RecordType = "write_deferred"
	RecordUsage             RecordType = "usage"
)

type ReplayPolicy string

const (
	ReplayNever ReplayPolicy = "never"
	ReplaySafe  ReplayPolicy = "safe"
)

type OpError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type OperationIntent struct {
	Kind                 string             `json:"kind"`
	OriginalPrompt       []ProvisionedEntry `json:"original_prompt,omitempty"`
	InitialMessages      []ProvisionedEntry `json:"initial_messages,omitempty"`
	SystemPromptOverride string             `json:"system_prompt_override,omitempty"`
	CustomInstructions   string             `json:"custom_instructions,omitempty"`
	ResultEntryID        string             `json:"result_entry_id,omitempty"`
	TargetID             string             `json:"target_id,omitempty"`
	Summarize            bool               `json:"summarize,omitempty"`
	Label                string             `json:"label,omitempty"`
	SummaryEntryID       string             `json:"summary_entry_id,omitempty"`
	ForceTool            string             `json:"force_tool,omitempty"`
	ForceToolAttempts    int                `json:"force_tool_attempts,omitempty"`
}

type Record struct {
	ID, Lane string
	Seq      int64
	Time     time.Time
	Type     RecordType
	RunID    string

	Intent       *OperationIntent
	SourceLeafID string
	Outcome      string
	Error        *OpError

	Step             string
	Attempt          int
	ResultEntryID    string
	CompactionReason string

	AssistantEntryID string
	ToolIndex        int
	ToolCallID       string
	ToolName         string
	EffectiveArgs    map[string]any
	Replay           ReplayPolicy
	Queue            string
	Target           *ProvisionedEntry
	EntryID          string
	Usage            *contextmgr.TokenUsage
	Cause            string
}

type RecordQuery struct {
	Lane  string
	RunID string
	Type  RecordType
	After int64
	Limit int
}

func NewRecordID() string { return newID("r_") }

func (r Record) Validate() error {
	if r.ID == "" {
		return fmt.Errorf("record id required")
	}
	if r.Lane == "" {
		r.Lane = "main"
	}
	switch r.Type {
	case RecordOperationStarted:
		if r.RunID == "" || r.Intent == nil || r.Intent.Kind == "" {
			return fmt.Errorf("operation_started requires run id and intent")
		}
	case RecordOperationFinished:
		if r.RunID == "" || (r.Outcome != "completed" && r.Outcome != "aborted" &&
			r.Outcome != "failed" && r.Outcome != "declined") {
			return fmt.Errorf("operation_finished has invalid outcome")
		}
	case RecordStepAttempt:
		if r.RunID == "" || r.Step == "" || r.Attempt <= 0 || r.ResultEntryID == "" {
			return fmt.Errorf("step_attempt missing required fields")
		}
	case RecordToolStarted:
		if r.RunID == "" || r.AssistantEntryID == "" || r.ToolCallID == "" ||
			r.ToolName == "" || r.ResultEntryID == "" {
			return fmt.Errorf("tool_started missing required fields")
		}
	case RecordQueueEnqueued:
		if r.Queue == "" || r.Target == nil {
			return fmt.Errorf("queue_enqueued requires queue and target")
		}
	case RecordQueueCancelled:
		if r.Queue == "" || r.EntryID == "" {
			return fmt.Errorf("queue_cancelled requires queue and entry id")
		}
	case RecordAbortRequested, RecordWriteDeferred, RecordUsage:
	default:
		return fmt.Errorf("unknown record type %q", r.Type)
	}
	return nil
}
