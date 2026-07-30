package tool

import (
	"context"
	"fmt"
	"strings"

	"github.com/Vignesh-Rajarajan/golum/pkg/execenv"
	"github.com/Vignesh-Rajarajan/golum/pkg/memory"
)

// memoryTool exposes the memory store to the model. Like todos it is stateful
// and takes no ExecutionEnv — it reads and writes the session database, not the
// workspace.
type memoryTool struct {
	store     *memory.Store
	sessionID func() string
}

// NewMemoryTool creates the memory tool bound to a store. sessionID may be nil.
func NewMemoryTool(store *memory.Store, sessionID func() string) AgentTool {
	return &memoryTool{store: store, sessionID: sessionID}
}

// indexProject rebuilds semantic memory from the workspace on disk.
func (t *memoryTool) indexProject(ctx context.Context, env execenv.ExecutionEnv) (Result, error) {
	if env == nil {
		return errResult("indexing requires a workspace"), nil
	}
	n, err := memory.NewGoRepoIndex(t.store).Reindex(ctx, env.CWD())
	if err != nil {
		return errResult(err.Error()), nil
	}
	return Result{
		Content: fmt.Sprintf("Indexed %d entries of project structure.", n),
		Display: fmt.Sprintf("indexed %d entries", n),
	}, nil
}

func (t *memoryTool) Name() string { return "memory" }

func (t *memoryTool) Description() string {
	return "Remember or recall durable facts across sessions. " +
		"actions: remember (store a fact), search (find facts), list, forget, " +
		"index (rebuild the map of this project's packages, types and functions). " +
		"Tiers: procedural (how to work here / user preferences), " +
		"episodic (what happened in past sessions), semantic (how the codebase is structured). " +
		"Prefer search here over re-reading files when you need to recall where something lives."
}

func (t *memoryTool) Parameters() map[string]any {
	return objectSchema(map[string]any{
		"action": map[string]any{
			"type":        "string",
			"enum":        []any{"remember", "search", "list", "forget", "index"},
			"description": "What to do",
		},
		"content": map[string]any{"type": "string", "description": "Fact to store (action=remember)"},
		"key": map[string]any{"type": "string",
			"description": "Stable identifier; re-remembering the same key updates it instead of duplicating"},
		"query": map[string]any{"type": "string", "description": "Search text (action=search)"},
		"tier": map[string]any{"type": "string",
			"enum":        []any{"procedural", "episodic", "semantic"},
			"description": "Defaults to procedural for remember, all tiers for search"},
		"scope": map[string]any{"type": "string",
			"enum":        []any{"project", "user"},
			"description": "project = about this codebase, user = about the user. Defaults to user for remember."},
		"id": map[string]any{"type": "string", "description": "Memory id or key to remove (action=forget)"},
	}, []string{"action"})
}

func (t *memoryTool) Execute(ctx context.Context, args map[string]any, env execenv.ExecutionEnv) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if t.store == nil {
		return errResult("memory is unavailable (no session database)"), nil
	}
	action, _ := stringArg(args, "action")
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "remember":
		return t.remember(ctx, args)
	case "search":
		return t.search(ctx, args)
	case "list":
		return t.list(ctx, args)
	case "forget":
		return t.forget(ctx, args)
	case "index":
		return t.indexProject(ctx, env)
	default:
		return errResult(fmt.Sprintf(
			"unknown action %q (want remember, search, list, forget, or index)", action)), nil
	}
}

func (t *memoryTool) remember(ctx context.Context, args map[string]any) (Result, error) {
	content, _ := stringArg(args, "content")
	if strings.TrimSpace(content) == "" {
		return errResult("missing required argument: content"), nil
	}
	rec := memory.Record{
		Content: content,
		Key:     firstNonEmpty(args, "key"),
		// Default to a user-scoped preference, matching the prompt's guidance
		// that memory is for facts that should personalize future sessions.
		Tier:  memory.TierProcedural,
		Scope: memory.ScopeUser,
	}
	if tier, ok := stringArg(args, "tier"); ok && tier != "" {
		if !memory.ValidTier(tier) {
			return errResult(fmt.Sprintf("invalid tier %q", tier)), nil
		}
		rec.Tier = memory.Tier(tier)
	}
	if scope, ok := stringArg(args, "scope"); ok && scope != "" {
		if !memory.ValidScope(scope) {
			return errResult(fmt.Sprintf("invalid scope %q", scope)), nil
		}
		rec.Scope = memory.Scope(scope)
	}
	if t.sessionID != nil {
		rec.SessionID = t.sessionID()
	}

	saved, err := t.store.Put(ctx, rec)
	if err != nil {
		return errResult(err.Error()), nil
	}
	label := saved.Key
	if label == "" {
		label = truncateRunes(saved.Content, 40)
	}
	return Result{
		Content: fmt.Sprintf("Remembered (%s/%s): %s", saved.Tier, saved.Scope, label),
		Display: fmt.Sprintf("remembered %s", label),
	}, nil
}

func (t *memoryTool) search(ctx context.Context, args map[string]any) (Result, error) {
	query, _ := stringArg(args, "query")
	var tier memory.Tier
	if s, ok := stringArg(args, "tier"); ok && s != "" {
		if !memory.ValidTier(s) {
			return errResult(fmt.Sprintf("invalid tier %q", s)), nil
		}
		tier = memory.Tier(s)
	}
	records, err := t.store.Search(ctx, tier, query, 10)
	if err != nil {
		return errResult(err.Error()), nil
	}
	if len(records) == 0 {
		return Result{Content: "No matching memories.", Display: "memory: no matches"}, nil
	}
	return Result{
		Content: formatRecords(records),
		Display: fmt.Sprintf("memory: %d match(es)", len(records)),
	}, nil
}

func (t *memoryTool) list(ctx context.Context, args map[string]any) (Result, error) {
	var tier memory.Tier
	if s, ok := stringArg(args, "tier"); ok && s != "" {
		if !memory.ValidTier(s) {
			return errResult(fmt.Sprintf("invalid tier %q", s)), nil
		}
		tier = memory.Tier(s)
	}
	records, err := t.store.List(ctx, tier, 20)
	if err != nil {
		return errResult(err.Error()), nil
	}
	if len(records) == 0 {
		return Result{Content: "No memories stored.", Display: "memory: empty"}, nil
	}
	return Result{
		Content: formatRecords(records),
		Display: fmt.Sprintf("memory: %d record(s)", len(records)),
	}, nil
}

func (t *memoryTool) forget(ctx context.Context, args map[string]any) (Result, error) {
	id := firstNonEmpty(args, "id", "key")
	if id == "" {
		return errResult("missing required argument: id"), nil
	}
	n, err := t.store.Forget(ctx, id)
	if err != nil {
		return errResult(err.Error()), nil
	}
	if n == 0 {
		return errResult(fmt.Sprintf("no memory matched %q", id)), nil
	}
	return Result{
		Content: fmt.Sprintf("Forgot %d memory record(s).", n),
		Display: fmt.Sprintf("forgot %s", id),
	}, nil
}

func formatRecords(records []memory.Record) string {
	var b strings.Builder
	for _, r := range records {
		fmt.Fprintf(&b, "[%s/%s] ", r.Tier, r.Scope)
		if r.Key != "" {
			fmt.Fprintf(&b, "%s: ", r.Key)
		}
		b.WriteString(r.Content)
		if r.FilePath != "" {
			fmt.Fprintf(&b, " (%s)", r.FilePath)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func firstNonEmpty(args map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := stringArg(args, k); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
