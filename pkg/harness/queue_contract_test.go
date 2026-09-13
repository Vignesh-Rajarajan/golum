package harness

import (
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
)

func TestQueueCancelIsAuditableAndNotExecuted(t *testing.T) {
	sess := driverSession()
	start := started(1, "run")
	if _, err := sess.AppendRecord(start); err != nil {
		t.Fatal(err)
	}
	target := session.ProvisionedEntry{ID: "q1", Kind: session.EntryUserMessage, Role: "user", Content: "steer"}
	if _, err := sess.AppendRecord(session.Record{
		Type: session.RecordQueueEnqueued, RunID: "run", Queue: "steer", Target: &target,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendRecord(session.Record{
		Type: session.RecordQueueCancelled, RunID: "run", Queue: "steer", EntryID: "q1",
	}); err != nil {
		t.Fatal(err)
	}
	recs, _ := sess.FindRecords(session.RecordQuery{})
	if err := ValidateRecordLog(RecordLogSlice{Records: recs, Entries: sess.Entries()}); err != nil {
		t.Fatal(err)
	}
	reduced, err := ReduceLaneState(ReductionInput{Lane: "main", Records: recs, Entries: sess.Entries()})
	if err != nil {
		t.Fatal(err)
	}
	if reduced.State.Operation != nil && len(reduced.State.Operation.PendingSteer) != 0 {
		t.Fatal("cancelled steer must not remain pending")
	}
}

func TestQueueItemCannotExecuteTwice(t *testing.T) {
	target := session.ProvisionedEntry{ID: "q1", Kind: session.EntryUserMessage, Role: "user", Content: "once"}
	enq := rec(2, session.RecordQueueEnqueued)
	enq.Queue, enq.Target, enq.RunID = "followUp", &target, "run"
	records := []session.Record{started(1, "run"), enq}
	reduced, err := ReduceLaneState(ReductionInput{
		Lane: "main", Records: records,
		Entries: []session.Entry{{ID: "q1", Kind: session.EntryUserMessage, Role: "user", Content: "once"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reduced.State.Operation != nil && len(reduced.State.Operation.PendingFollowUp) != 0 {
		t.Fatal("materialized queue item must not stay pending")
	}
}
