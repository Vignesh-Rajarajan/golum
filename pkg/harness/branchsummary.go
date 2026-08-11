package harness

import (
	"context"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/hooks"
)

func (h *AgentHarness) NavigateTree(ctx context.Context, targetID string, opts NavigationOptions) (NavigationOutcome, error) {
	if h.hooks != nil {
		event := &hooks.NavigationEvent{TargetID: targetID}
		if err := h.hooks.Emit(ctx, hooks.BeforeNavigation, event); err != nil {
			return NavigationOutcome{}, err
		}
		targetID = event.TargetID
	}
	if _, ok := h.session.GetEntry(targetID); !ok {
		return NavigationOutcome{}, &UnknownTargetError{TargetID: targetID}
	}
	open, err := h.session.FindOpenOperations("main", 2)
	if err != nil {
		return NavigationOutcome{}, err
	}
	if len(open) > 0 {
		return NavigationOutcome{}, &BusyError{
			Lane: "main", OperationID: open[0].RunID, OperationKind: open[0].Intent.Kind,
		}
	}
	summaryID := ""
	if opts.Summarize {
		summaryID = session.NewEntryID()
	}
	runID := session.NewRecordID()
	sourceLeaf := h.session.Leaf()
	if _, err := h.session.AppendRecord(session.Record{
		Lane: "main", Type: session.RecordOperationStarted, RunID: runID,
		SourceLeafID: sourceLeaf,
		Intent: &session.OperationIntent{
			Kind: "navigation", TargetID: targetID, Summarize: opts.Summarize,
			Label: opts.Label, SummaryEntryID: summaryID,
		},
	}); err != nil {
		return NavigationOutcome{}, err
	}
	finish := func(outcome string, opErr error) {
		r := session.Record{Lane: "main", Type: session.RecordOperationFinished, RunID: runID, Outcome: outcome}
		if opErr != nil {
			r.Error = &session.OpError{Code: "navigation", Message: opErr.Error()}
		}
		_, _ = h.session.AppendRecord(r)
	}

	var summary string
	if opts.Summarize {
		abandoned, collectErr := collectAbandonedEntries(h.session, targetID)
		if collectErr != nil {
			finish("failed", collectErr)
			return NavigationOutcome{}, collectErr
		}
		summary, err = h.compactor.summarize(ctx, abandoned, "")
		if err != nil {
			finish("failed", err)
			return NavigationOutcome{}, err
		}
	}
	if err := h.session.MoveTo(targetID); err != nil {
		finish("failed", err)
		return NavigationOutcome{}, err
	}
	if opts.Summarize {
		_, err = h.session.AppendProvisioned(session.ProvisionedEntry{
			ID: summaryID, Kind: session.EntryBranchSummary, Content: summary,
			Meta: map[string]any{"from_id": sourceLeaf, "summary": summary},
		})
		if err != nil {
			finish("failed", err)
			return NavigationOutcome{}, err
		}
	}
	if opts.Label != "" {
		if err := h.session.SetLabel(opts.Label); err != nil {
			finish("failed", err)
			return NavigationOutcome{}, err
		}
	}
	finish("completed", nil)
	return NavigationOutcome{LeafID: h.session.Leaf(), SummaryEntryID: summaryID}, nil
}

func collectAbandonedEntries(sess session.Session, targetID string) ([]session.Entry, error) {
	current, err := sess.GetPathToRoot(sess.Leaf())
	if err != nil {
		return nil, err
	}
	target, err := sess.GetPathToRoot(targetID)
	if err != nil {
		return nil, err
	}
	common := 0
	for common < len(current) && common < len(target) && current[common].ID == target[common].ID {
		common++
	}
	return append([]session.Entry(nil), current[common:]...), nil
}
