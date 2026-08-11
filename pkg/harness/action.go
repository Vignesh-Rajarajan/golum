package harness

import (
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/Vignesh-Rajarajan/golum/pkg/tool"
)

type ActionKind string

const (
	ActionAppendEntry       ActionKind = "append_entry"
	ActionAppendRecord      ActionKind = "append_record"
	ActionMoveLeaf          ActionKind = "move_leaf"
	ActionSetFact           ActionKind = "set_fact"
	ActionTryFinishRun      ActionKind = "try_finish_run"
	ActionFinishOperation   ActionKind = "finish_operation"
	ActionCommitFollowUp    ActionKind = "commit_follow_up"
	ActionConsumeQueueItem  ActionKind = "consume_queue_item"
	ActionApplyPendingWrite ActionKind = "apply_pending_write"
	ActionStreamAssistant   ActionKind = "stream_assistant"
	ActionCompact           ActionKind = "compact"
	ActionExecuteTool       ActionKind = "execute_tool"
	ActionHook              ActionKind = "hook"
	ActionSleep             ActionKind = "sleep"
)

type Action struct {
	Kind            ActionKind
	Record          *session.Record
	Entry           *session.ProvisionedEntry
	ToolCall        *llm.ToolCall
	ToolStarted     *session.Record
	SyntheticResult *tool.Result
}
