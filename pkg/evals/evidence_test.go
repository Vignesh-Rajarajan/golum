package evals

import (
	"context"
	"testing"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
)

func TestMetricsComeFromDurableRecords(t *testing.T) {
	r := &Result{
		Elapsed: 2 * time.Second,
		Usage:   Usage{Model: "gpt-4o", InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		Records: []session.Record{
			{Type: session.RecordStepAttempt, Step: "assistant"},
			{Type: session.RecordStepAttempt, Step: "compaction"},
			{Type: session.RecordOperationFinished, Outcome: "completed"},
		},
	}
	m := ComputeMetrics(r)
	if m.ModelRequests != 1 {
		t.Fatalf("model requests=%d", m.ModelRequests)
	}
	if m.Compactions != 1 {
		t.Fatalf("compactions=%d", m.Compactions)
	}
	if m.Outcome != "completed" {
		t.Fatalf("outcome=%s", m.Outcome)
	}
}

func TestMissingUsageAndPricingAreUnavailable(t *testing.T) {
	m := ComputeMetrics(&Result{Usage: Usage{Model: "mystery-model"}})
	if m.TokensReported {
		t.Fatal("missing usage must not look like zero tokens reported")
	}
	if m.EstimatedCostUSD != nil {
		t.Fatal("missing pricing must not look free")
	}
}

func TestFailedRunKeepsPartialEvidence(t *testing.T) {
	run := Evaluate(context.Background(), Task{
		ID: "t", Objective: "go",
		Acceptance: AcceptanceCriteria{Outcome: []OutcomeVerifier{FinalAnswerEquals("x")}},
	}, &Result{Output: "partial", Entries: []session.Entry{{ID: "e", Kind: session.EntryUserMessage}}}, nil)
	if run.Result == nil || len(run.Result.Entries) == 0 {
		t.Fatal("failed run dropped evidence")
	}
}

func TestArtifactPathsStayInEvalDir(t *testing.T) {
	check := ArtifactReferenceValid().Verify(context.Background(), &Result{Entries: []session.Entry{
		{Kind: session.EntryToolResult, Meta: map[string]any{"artifact_path": "../etc/passwd"}},
	}})
	if check.Passed {
		t.Fatal("escaping artifact path must fail")
	}
}
