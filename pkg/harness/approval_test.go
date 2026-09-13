package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/harnesstest"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
)

type scriptedBroker struct {
	approve bool
	err     error
}

func (s scriptedBroker) Request(ctx context.Context, _ llm.ToolCall) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return s.approve, s.err
}

func TestApprovalAcceptedAndRejected(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		r := NewTestRig(t)
		r.Deps.Approvals = scriptedBroker{approve: true}
		r.rebuildDriver()
		r.Model.Script(
			harnesstest.Turn{ToolCalls: []harnesstest.ToolCall{{
				ID: "c1", Name: "write_file", Args: `{"path":"ok.txt","content":"hi"}`,
			}}},
			harnesstest.Turn{Content: "done"},
		)
		r.Prompt("write")
		if err := r.RunToCompletion(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(r.Root, "ok.txt")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("rejected", func(t *testing.T) {
		r := NewTestRig(t)
		r.Deps.Approvals = scriptedBroker{approve: false}
		r.rebuildDriver()
		r.Model.Script(
			harnesstest.Turn{ToolCalls: []harnesstest.ToolCall{{
				ID: "c1", Name: "write_file", Args: `{"path":"no.txt","content":"hi"}`,
			}}},
			harnesstest.Turn{Content: "ok"},
		)
		r.Prompt("write")
		if err := r.RunToCompletion(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(r.Root, "no.txt")); !os.IsNotExist(err) {
			t.Fatal("rejected write landed")
		}
	})
	t.Run("broker_error", func(t *testing.T) {
		r := NewTestRig(t)
		r.Deps.Approvals = scriptedBroker{err: errors.New("broker down")}
		r.rebuildDriver()
		r.Model.Script(
			harnesstest.Turn{ToolCalls: []harnesstest.ToolCall{{
				ID: "c1", Name: "write_file", Args: `{"path":"x.txt","content":"hi"}`,
			}}},
			harnesstest.Turn{Content: "ok"},
		)
		r.Prompt("write")
		if err := r.RunToCompletion(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("context_cancelled", func(t *testing.T) {
		r := NewTestRig(t)
		r.Deps.Approvals = scriptedBroker{approve: true}
		r.rebuildDriver()
		r.Model.Script(harnesstest.Turn{ToolCalls: []harnesstest.ToolCall{{
			ID: "c1", Name: "write_file", Args: `{"path":"x.txt","content":"hi"}`,
		}}})
		r.Prompt("write")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = r.RunToCompletion(ctx)
	})
}

func TestAlwaysAllowPersistsInSessionOnly(t *testing.T) {
	r := NewTestRig(t)
	if _, err := r.Session.AppendApprovalAlways("write_file"); err != nil {
		t.Fatal(err)
	}
	if !r.Session.AlwaysAllowed("write_file") {
		t.Fatal("expected always-allow")
	}
	other := NewTestRig(t)
	if other.Session.AlwaysAllowed("write_file") {
		t.Fatal("always-allow crossed sessions")
	}
}
