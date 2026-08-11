package harness

import (
	"context"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/hooks"
)

func TestAbortQuiescesWriterBeforeFinishingOperation(t *testing.T) {
	sess := driverSession()
	const runID = "run"
	if _, err := sess.AppendRecord(session.Record{
		Type: session.RecordOperationStarted, RunID: runID,
		Intent: &session.OperationIntent{Kind: "run"},
	}); err != nil {
		t.Fatal(err)
	}
	turnCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	h := &AgentHarness{
		session: sess, phase: PhaseStreaming,
		streamCancel: cancel, streamDone: done,
	}
	writeErr := make(chan error, 1)
	go func() {
		<-turnCtx.Done()
		_, err := sess.AppendRecord(session.Record{
			Type: session.RecordUsage, RunID: runID, Cause: "quiescing writer",
		})
		writeErr <- err
		h.finishStream(done)
	}()

	if _, err := h.AbortContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-writeErr; err != nil {
		t.Fatal(err)
	}
	records, err := sess.FindRecords(session.RecordQuery{Lane: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRecordLog(RecordLogSlice{Entries: sess.Entries(), Records: records}); err != nil {
		t.Fatalf("abort corrupted record log: %v", err)
	}
	var usageSeq, abortSeq, finishSeq int64
	for _, r := range records {
		switch r.Type {
		case session.RecordUsage:
			usageSeq = r.Seq
		case session.RecordAbortRequested:
			abortSeq = r.Seq
		case session.RecordOperationFinished:
			finishSeq = r.Seq
		}
	}
	if !(usageSeq < abortSeq && abortSeq < finishSeq) {
		t.Fatalf("abort ordering: usage=%d abort=%d finish=%d", usageSeq, abortSeq, finishSeq)
	}
}

func TestPeekActionFinishesPersistedAbort(t *testing.T) {
	sess := driverSession()
	_, _ = sess.AppendRecord(session.Record{
		Type: session.RecordOperationStarted, RunID: "run",
		Intent: &session.OperationIntent{Kind: "run"},
	})
	_, _ = sess.AppendRecord(session.Record{Type: session.RecordAbortRequested, RunID: "run"})
	action, err := NewDriver(LoopDeps{Session: sess}, DefaultLoopConfig(), nil).
		PeekAction(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if action == nil || action.Kind != ActionFinishOperation ||
		action.Record.Outcome != "aborted" {
		t.Fatalf("abort action=%#v", action)
	}
}

func TestAbortQuiescesResumedOperationBeforeFinishing(t *testing.T) {
	sess := driverSession()
	const runID = "resumed_run"
	_, _ = sess.AppendRecord(session.Record{
		Type: session.RecordOperationStarted, RunID: runID,
		Intent: &session.OperationIntent{Kind: "run"},
	})
	entered := make(chan struct{})
	hm := hooks.New()
	hm.OnBeforeResume(func(ctx context.Context, _ *hooks.RunEvent) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	})
	h := &AgentHarness{
		session: sess, hooks: hm, phase: PhaseIdle,
		events: NewHarnessEventBus(),
	}
	resumeErr := make(chan error, 1)
	go func() {
		_, err := h.Resume(context.Background())
		resumeErr <- err
	}()
	<-entered

	if _, err := h.AbortContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-resumeErr; err != context.Canceled {
		t.Fatalf("resume error=%v, want context.Canceled", err)
	}
	records, err := sess.FindRecords(session.RecordQuery{Lane: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateRecordLog(RecordLogSlice{Entries: sess.Entries(), Records: records}); err != nil {
		t.Fatalf("resumed abort corrupted record log: %v", err)
	}
	if records[len(records)-1].Type != session.RecordOperationFinished ||
		records[len(records)-1].Outcome != "aborted" {
		t.Fatalf("last record=%#v", records[len(records)-1])
	}
}
