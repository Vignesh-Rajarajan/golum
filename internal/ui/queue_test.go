package ui

import (
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
)

func TestQueueDisplayRemovesOnlyConsumedItem(t *testing.T) {
	m := Model{queued: []session.ProvisionedEntry{
		{ID: "first", Content: "first steer"},
		{ID: "second", Content: "second steer"},
	}}
	m.handleAgentEvent(harness.AgentEvent{
		Type: harness.EventQueueConsumed,
		Meta: map[string]string{"entry_id": "first"},
	})
	if len(m.queued) != 1 || m.queued[0].ID != "second" {
		t.Fatalf("queued after consume=%v", m.queued)
	}
	m.handleAgentEvent(harness.AgentEvent{
		Type: harness.EventContentDelta, Content: "reply",
	})
	if len(m.queued) != 1 || m.queued[0].ID != "second" {
		t.Fatalf("content delta cleared pending queue: %v", m.queued)
	}
}
