package harness

import (
	"context"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
)

type QueueMode string

const (
	QueueOneAtATime QueueMode = "one-at-a-time"
	QueueAll        QueueMode = "all"
)

func (h *AgentHarness) Steer(ctx context.Context, text string) (session.ProvisionedEntry, error) {
	return h.enqueue(ctx, "steer", text, true)
}

func (h *AgentHarness) FollowUp(ctx context.Context, text string) (session.ProvisionedEntry, error) {
	return h.enqueue(ctx, "followUp", text, true)
}

func (h *AgentHarness) NextRun(ctx context.Context, text string) (session.ProvisionedEntry, error) {
	return h.enqueue(ctx, "nextRun", text, false)
}

func (h *AgentHarness) enqueue(ctx context.Context, queue, text string, requireRun bool) (session.ProvisionedEntry, error) {
	if err := ctx.Err(); err != nil {
		return session.ProvisionedEntry{}, err
	}
	if text == "" {
		return session.ProvisionedEntry{}, &InvalidMessageError{Lane: "main", Reason: "empty queued message"}
	}
	open, err := h.session.FindOpenOperations("main", 2)
	if err != nil {
		return session.ProvisionedEntry{}, err
	}
	if requireRun && len(open) == 0 {
		return session.ProvisionedEntry{}, &NoActiveRunError{Lane: "main"}
	}
	runID := ""
	if len(open) > 0 {
		runID = open[0].RunID
	}
	p := session.ProvisionedEntry{
		ID: session.NewEntryID(), Kind: session.EntryUserMessage, Role: "user", Content: text,
	}
	_, err = h.session.AppendRecord(session.Record{
		Lane: "main", Type: session.RecordQueueEnqueued, RunID: runID,
		Queue: queue, Target: &p,
	})
	return p, err
}

func (h *AgentHarness) CancelQueued(ctx context.Context, entryID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	records, err := h.session.FindRecords(session.RecordQuery{Lane: "main"})
	if err != nil {
		return err
	}
	queue, runID := "", ""
	for _, r := range records {
		if r.Type == session.RecordQueueEnqueued && r.Target != nil && r.Target.ID == entryID {
			queue, runID = r.Queue, r.RunID
		}
	}
	if queue == "" {
		return &UnknownQueueItemError{Lane: "main", EntryID: entryID}
	}
	_, err = h.session.AppendRecord(session.Record{
		Lane: "main", Type: session.RecordQueueCancelled, RunID: runID,
		Queue: queue, EntryID: entryID,
	})
	return err
}
