package harness

import "github.com/Vignesh-Rajarajan/golum/pkg/harness/session"

type RunOutcome struct {
	Kind                 string
	LeafID, FinalEntryID string
	FinalMessage         string
	Err                  *session.OpError
}

type ResumeOutcome struct{ RunOutcome }

type AbortResult struct {
	Steer, FollowUp []session.ProvisionedEntry
}

type CompactOptions struct{ Instructions string }
type NavigationOptions struct {
	Summarize bool
	Label     string
}
type CompactionOutcome struct {
	EntryID string
	Summary string
}
type NavigationOutcome struct {
	LeafID, SummaryEntryID string
}
