package session

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/contextmgr"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
	"github.com/Vignesh-Rajarajan/golum/pkg/prompt"
	"github.com/sashabaranov/go-openai"
)

// SessionMeta is lightweight metadata for listing sessions.
type SessionMeta struct {
	ID         string    `json:"id"`
	Label      string    `json:"label,omitempty"`
	CWD        string    `json:"cwd,omitempty"`
	EntryCount int       `json:"entry_count,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// SessionRepo creates/opens/lists durable sessions under ~/.golum/sessions.
// It does NOT use the workspace ExecutionEnv — session storage is outside the workspace.
type SessionRepo interface {
	Create(ctx context.Context) (Session, error)
	Open(ctx context.Context, id string) (Session, error)
	List(ctx context.Context) ([]SessionMeta, error)
	Delete(ctx context.Context, id string) error
	Fork(ctx context.Context, id, atEntryID string) (Session, error)
}

// JsonlSession wraps InMemorySession and appends entries as JSON lines.
type JsonlSession struct {
	*InMemorySession
	path string
}

func (s *JsonlSession) AppendProvisioned(p ProvisionedEntry) (Entry, error) {
	if existing, ok := s.GetEntry(p.ID); ok {
		if !p.Matches(existing) {
			return Entry{}, &ProvisionedEntryMismatchError{ID: p.ID}
		}
		return existing, nil
	}
	e, err := s.InMemorySession.AppendProvisioned(p)
	if err != nil {
		return e, err
	}
	return e, s.persist(e)
}

// Append helpers override to also persist.
func (s *JsonlSession) AppendUserMessage(content string) (Entry, error) {
	e, err := s.InMemorySession.AppendUserMessage(content)
	if err != nil {
		return e, err
	}
	return e, s.persist(e)
}

func (s *JsonlSession) AppendAssistantMessage(content string, toolCalls []openai.ToolCall) (Entry, error) {
	e, err := s.InMemorySession.AppendAssistantMessage(content, toolCalls)
	if err != nil {
		return e, err
	}
	return e, s.persist(e)
}

func (s *JsonlSession) AppendToolResult(toolCallID, content string) (Entry, error) {
	e, err := s.InMemorySession.AppendToolResult(toolCallID, content)
	if err != nil {
		return e, err
	}
	return e, s.persist(e)
}

func (s *JsonlSession) AppendSystemNotice(content string) (Entry, error) {
	e, err := s.InMemorySession.AppendSystemNotice(content)
	if err != nil {
		return e, err
	}
	return e, s.persist(e)
}

func (s *JsonlSession) AppendCompactionAt(summary, cutEntryID string) (Entry, error) {
	e, err := s.InMemorySession.AppendCompactionAt(summary, cutEntryID)
	if err != nil {
		return e, err
	}
	return e, s.persist(e)
}

func (s *JsonlSession) AppendCompaction(summary string) error {
	if err := s.InMemorySession.AppendCompaction(summary); err != nil {
		return err
	}
	entries := s.Entries()
	if len(entries) == 0 {
		return nil
	}
	return s.persist(entries[len(entries)-1])
}

func (s *JsonlSession) AppendTodos(items any) (Entry, error) {
	e, err := s.InMemorySession.AppendTodos(items)
	if err != nil {
		return e, err
	}
	return e, s.persist(e)
}

func (s *JsonlSession) AppendApprovalAlways(toolName string) (Entry, error) {
	e, err := s.InMemorySession.AppendApprovalAlways(toolName)
	if err != nil {
		return e, err
	}
	return e, s.persist(e)
}

func (s *JsonlSession) AppendModelChange(model string) (Entry, error) {
	return s.AppendProvisioned(provision(EntryModelChange, "", model, nil, map[string]any{"model": model}))
}

func (s *JsonlSession) AppendThinkingLevelChange(level string) (Entry, error) {
	return s.AppendProvisioned(provision(EntryThinkingLevelChange, "", level, nil, map[string]any{"level": level}))
}

func (s *JsonlSession) AppendActiveToolsChange(names []string) (Entry, error) {
	return s.AppendProvisioned(provision(EntryActiveToolsChange, "", "", nil, map[string]any{"tools": names}))
}

func (s *JsonlSession) persist(e Entry) error {
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	return enc.Encode(e)
}

// FileSessionRepo implements SessionRepo with JSONL files under dir.
type FileSessionRepo struct {
	dir       string
	cfg       *config.Config
	promptCfg prompt.PromptConfig
	tools     []llm.Tool
}

// NewFileSessionRepo creates a repo under baseDir (typically ~/.golum/sessions).
func NewFileSessionRepo(baseDir string, cfg *config.Config, promptCfg prompt.PromptConfig, tools []llm.Tool) (*FileSessionRepo, error) {
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		return nil, err
	}
	return &FileSessionRepo{dir: baseDir, cfg: cfg, promptCfg: promptCfg, tools: tools}, nil
}

// DefaultSessionsDir returns ~/.golum/sessions.
func DefaultSessionsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".golum", "sessions"), nil
}

func (r *FileSessionRepo) pathFor(id string) string {
	return filepath.Join(r.dir, id+".jsonl")
}

func (r *FileSessionRepo) Create(ctx context.Context) (Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := NewSessionID()
	ctxMgr := contextmgr.NewContextManager(r.cfg, r.promptCfg, nil, r.tools)
	inner := NewInMemorySession(id, ctxMgr)
	js := &JsonlSession{InMemorySession: inner, path: r.pathFor(id)}
	// touch file
	f, err := os.OpenFile(js.path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	_ = f.Close()
	return js, nil
}

func (r *FileSessionRepo) Open(ctx context.Context, id string) (Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := r.pathFor(id)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	ctxMgr := contextmgr.NewContextManager(r.cfg, r.promptCfg, nil, r.tools)
	inner := NewInMemorySession(id, ctxMgr)
	js := &JsonlSession{InMemorySession: inner, path: path}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		// Rehydrate tool_calls from meta JSON
		if e.Kind == EntryAssistantMessage && e.Meta != nil {
			if raw, ok := e.Meta["tool_calls"]; ok {
				b, _ := json.Marshal(raw)
				var tcs []openai.ToolCall
				if json.Unmarshal(b, &tcs) == nil {
					e.Meta["tool_calls"] = tcs
				}
			}
		}
		js.InMemorySession.LoadEntry(e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	// Derive rather than replay linearly: see InMemorySession.LoadEntry.
	if err := js.InMemorySession.RebuildContext(); err != nil {
		return nil, err
	}
	return js, nil
}

func (r *FileSessionRepo) List(ctx context.Context) ([]SessionMeta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil, err
	}
	var out []SessionMeta
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(ent.Name(), ".jsonl")
		info, err := ent.Info()
		if err != nil {
			continue
		}
		meta := SessionMeta{
			ID:        id,
			CreatedAt: info.ModTime(),
			UpdatedAt: info.ModTime(),
		}
		// Best-effort: read label from last label entry
		if sess, err := r.Open(ctx, id); err == nil {
			meta.Label = sess.Label()
			if js, ok := sess.(*JsonlSession); ok && len(js.entries) > 0 {
				meta.CreatedAt = js.entries[0].Time
				meta.UpdatedAt = js.entries[len(js.entries)-1].Time
			}
		}
		out = append(out, meta)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

func (r *FileSessionRepo) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Remove(r.pathFor(id))
}

func (r *FileSessionRepo) Fork(ctx context.Context, id, atEntryID string) (Session, error) {
	src, err := r.Open(ctx, id)
	if err != nil {
		return nil, err
	}
	dst, err := r.Create(ctx)
	if err != nil {
		return nil, err
	}
	jsDst, ok := dst.(*JsonlSession)
	if !ok {
		return nil, fmt.Errorf("unexpected session type")
	}
	for _, e := range src.Entries() {
		_ = jsDst.InMemorySession.ReplayEntry(e)
		_ = jsDst.persist(e)
		if e.ID == atEntryID {
			break
		}
	}
	return dst, nil
}
