package session

import (
	"encoding/json"
	"reflect"

	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
)

// ProvisionedEntry is an Entry whose identity is allocated before storage.
// Parent, sequence, and timestamp remain storage-assigned.
type ProvisionedEntry struct {
	ID       string         `json:"id"`
	Kind     EntryKind      `json:"kind"`
	Role     string         `json:"role,omitempty"`
	Content  string         `json:"content,omitempty"`
	ToolCall *llm.ToolCall  `json:"tool_call,omitempty"`
	Meta     map[string]any `json:"meta,omitempty"`
}

// Matches reports whether e carries exactly the provisioned payload.
func (p ProvisionedEntry) Matches(e Entry) bool {
	if p.ID != e.ID {
		return false
	}
	left, leftErr := normalizeProvisionedPayload(struct {
		Kind     EntryKind      `json:"kind"`
		Role     string         `json:"role,omitempty"`
		Content  string         `json:"content,omitempty"`
		ToolCall *llm.ToolCall  `json:"tool_call,omitempty"`
		Meta     map[string]any `json:"meta,omitempty"`
	}{p.Kind, p.Role, p.Content, p.ToolCall, p.Meta})
	right, rightErr := normalizeProvisionedPayload(struct {
		Kind     EntryKind      `json:"kind"`
		Role     string         `json:"role,omitempty"`
		Content  string         `json:"content,omitempty"`
		ToolCall *llm.ToolCall  `json:"tool_call,omitempty"`
		Meta     map[string]any `json:"meta,omitempty"`
	}{e.Kind, e.Role, e.Content, e.ToolCall, e.Meta})
	return leftErr == nil && rightErr == nil && reflect.DeepEqual(left, right)
}

func normalizeProvisionedPayload(payload any) (any, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var normalized any
	if err := json.Unmarshal(data, &normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

func provision(kind EntryKind, role, content string, toolCall *llm.ToolCall, meta map[string]any) ProvisionedEntry {
	return ProvisionedEntry{
		ID: NewEntryID(), Kind: kind, Role: role, Content: content,
		ToolCall: toolCall, Meta: meta,
	}
}
