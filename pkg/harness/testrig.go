package harness

import (
	"context"
	"fmt"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/contextmgr"
	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/harnesstest"
	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
	"github.com/Vignesh-Rajarajan/golum/pkg/tool"
)

// TestRig assembles a workspace, session, registry, scripted model, and Driver
// so contract and crash tests can step one durable action at a time.
type TestRig struct {
	T         *testing.T
	Root      string
	Env       execenv.ExecutionEnv
	Memory    *session.InMemorySession
	Fault     *harnesstest.FaultSession
	Session   session.Session
	Registry  *tool.Registry
	Todos     *tool.TodoStore
	Deps      LoopDeps
	Cfg       LoopConfig
	Driver    *Driver
	Model     *harnesstest.ScriptedModel
	Events    []AgentEvent
	ownsModel bool
}

// RigOption customizes NewTestRig.
type RigOption func(*TestRig)

// WithScriptedModel uses an existing scripted server instead of starting a new one.
func WithScriptedModel(m *harnesstest.ScriptedModel) RigOption {
	return func(r *TestRig) { r.Model = m }
}

// WithLoopConfig overrides the default loop config.
func WithLoopConfig(cfg LoopConfig) RigOption {
	return func(r *TestRig) { r.Cfg = cfg }
}

// WithRigRegistry installs a pre-built tool registry.
func WithRigRegistry(reg *tool.Registry, todos *tool.TodoStore) RigOption {
	return func(r *TestRig) {
		r.Registry = reg
		r.Todos = todos
	}
}

// NewTestRig builds an isolated loop environment rooted at t.TempDir().
func NewTestRig(t *testing.T, opts ...RigOption) *TestRig {
	t.Helper()
	r := &TestRig{T: t, Cfg: DefaultLoopConfig()}
	for _, opt := range opts {
		opt(r)
	}
	r.Root = t.TempDir()
	env, err := execenv.NewOsExecutionEnv(r.Root)
	if err != nil {
		t.Fatal(err)
	}
	r.Env = env
	if r.Registry == nil {
		r.Registry, r.Todos = tool.DefaultRegistry(nil)
	}
	cfg := &config.Config{Model: "gpt-4o", ContextWindow: 128000}
	ctxMgr := contextmgr.NewContextManager(cfg, prompt.PromptConfig{CWD: r.Root}, nil, r.Registry.AsLLMTools())
	r.Memory = session.NewInMemorySession("rig", ctxMgr)
	r.Fault = harnesstest.WrapFault(r.Memory)
	r.Session = r.Fault
	if r.Model == nil {
		r.Model = harnesstest.NewScriptedModel()
		r.ownsModel = true
		t.Cleanup(r.Model.Close)
	}
	r.rebuildDriver()
	return r
}

func (r *TestRig) rebuildDriver() {
	approvals := r.Deps.Approvals
	if approvals == nil {
		approvals = AutoApprove{}
	}
	r.Deps = LoopDeps{
		Client:      r.Model.Client(),
		Session:     r.Session,
		Registry:    r.Registry,
		Env:         r.Env,
		Approvals:   approvals,
		Todos:       r.Todos,
		Compactor:   r.Deps.Compactor,
		Caps:        r.Deps.Caps,
		Directors:   r.Deps.Directors,
		ActiveTools: r.Deps.ActiveTools,
		Model:       "gpt-4o",
	}
	r.Driver = NewDriver(r.Deps, r.Cfg, func(ev AgentEvent) {
		r.Events = append(r.Events, ev)
	})
}

func (r *TestRig) ensureStarted() error {
	open, err := r.Session.FindOpenOperations("main", 2)
	if err != nil {
		return err
	}
	if len(open) > 1 {
		return &CorruptionError{Reason: CorruptionMultipleOpenOperations, Detail: "main"}
	}
	if len(open) > 0 {
		return nil
	}
	runID := session.NewRecordID()
	_, err = r.Session.AppendRecord(session.Record{
		ID: stableID("r_start_", runID), Lane: "main",
		Type: session.RecordOperationStarted, RunID: runID,
		SourceLeafID: r.Session.Leaf(),
		Intent: &session.OperationIntent{
			Kind: "run", ForceTool: r.Cfg.ForceTool, ForceToolAttempts: r.Cfg.ForceToolAttempts,
		},
	})
	return err
}

// Step peeks, optionally injects a "during" crash, then executes one action.
func (r *TestRig) Step(ctx context.Context) (*Action, error) {
	if err := r.ensureStarted(); err != nil {
		return nil, err
	}
	action, err := r.Driver.PeekAction(ctx)
	if err != nil || action == nil {
		return action, err
	}
	if err := r.injectDuring(action); err != nil {
		return action, err
	}
	return r.Driver.ExecuteAction(ctx)
}

func (r *TestRig) injectDuring(action *Action) error {
	var b harnesstest.Boundary
	switch action.Kind {
	case ActionExecuteTool:
		b = harnesstest.BoundaryDuringToolExecution
	case ActionConsumeQueueItem:
		b = harnesstest.BoundaryDuringQueueConsumption
	case ActionCompact:
		b = harnesstest.BoundaryDuringCompaction
	default:
		return nil
	}
	return r.Fault.Hit(b, false)
}

// Restart rebuilds a fresh Driver and session from the durable journal.
func (r *TestRig) Restart() {
	entries := r.Memory.Entries()
	records, _ := r.Memory.FindRecords(session.RecordQuery{})
	cfg := &config.Config{Model: "gpt-4o", ContextWindow: 128000}
	ctxMgr := contextmgr.NewContextManager(cfg, prompt.PromptConfig{CWD: r.Root}, nil, r.Registry.AsLLMTools())
	next := session.NewInMemorySession(r.Memory.ID(), ctxMgr)
	for _, e := range entries {
		next.LoadEntry(e)
	}
	for _, rec := range records {
		next.LoadRecord(rec)
	}
	if err := next.RebuildContext(); err != nil {
		r.T.Fatalf("restart rebuild: %v", err)
	}
	r.Memory = next
	r.Fault = harnesstest.WrapFault(next)
	r.Session = r.Fault
	r.Events = nil
	r.rebuildDriver()
}

// RunToCompletion drives the loop until idle or error.
func (r *TestRig) RunToCompletion(ctx context.Context) error {
	if err := r.ensureStarted(); err != nil {
		return err
	}
	return r.Driver.RunToCompletion(ctx)
}

// Records returns the journal in append order.
func (r *TestRig) Records() []session.Record {
	recs, err := r.Session.FindRecords(session.RecordQuery{})
	if err != nil {
		r.T.Fatal(err)
	}
	return recs
}

// AssertInvariants checks the journal and context-balance contracts.
func (r *TestRig) AssertInvariants() {
	r.T.Helper()
	recs := r.Records()
	if err := ValidateRecordLog(RecordLogSlice{
		Entries: r.Session.Entries(), Records: recs,
	}); err != nil {
		r.T.Fatalf("journal invariants: %v", err)
	}
	harnesstest.AssertBalanced(r.T, r.Session)
}

// Prompt seeds a user message so PeekAction has work to do.
func (r *TestRig) Prompt(content string) {
	r.T.Helper()
	if _, err := r.Session.AppendUserMessage(content); err != nil {
		r.T.Fatal(err)
	}
}

// Outcome is the last operation_finished outcome, or empty if none.
func (r *TestRig) Outcome() (string, *session.OpError) {
	var last session.Record
	for _, rec := range r.Records() {
		if rec.Type == session.RecordOperationFinished {
			last = rec
		}
	}
	return last.Outcome, last.Error
}

// FormatRecords is a compact journal dump for failure messages.
func FormatRecords(recs []session.Record) string {
	var b []byte
	for _, rec := range recs {
		b = append(b, []byte(fmt.Sprintf("%d %s %s %s\n", rec.Seq, rec.Type, rec.RunID, rec.Outcome))...)
	}
	return string(b)
}
