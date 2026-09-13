// Package harnesstest is the importable (non-_test.go) testkit for golum's
// agent loop. pkg/evals, pkg/tool, and pkg/harness all need the same scripted
// model and fault injector; those cannot live in a single package's test binary.
package harnesstest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
)

// ScriptedModel is an OpenAI-compatible HTTP server that serves a scripted
// sequence of completions. The existing llm.Client is the seam; this is not a
// second provider interface.
type ScriptedModel struct {
	server *httptest.Server

	mu       sync.Mutex
	turns    []Turn
	idx      int
	Requests []map[string]any
}

// Turn is one scripted model response.
type Turn struct {
	Content   string
	ToolCalls []ToolCall
	// Status, when non-zero, returns an HTTP error instead of a stream.
	Status    int
	ErrorBody string
	// NonStream serves a non-streaming JSON completion (used by summarization).
	NonStream bool
	// DropFinal omits the finish chunk and the [DONE] trailer.
	DropFinal bool
	// MidStreamError, when set, is written as a raw SSE error after any content.
	MidStreamError string
	// Usage overrides the usage object on the final chunk. A nil map uses a
	// default; an empty non-nil map omits usage entirely.
	Usage map[string]any
	// MalformedUsage writes a non-object usage field.
	MalformedUsage bool
	// Delay sleeps before serving the turn.
	Delay time.Duration
	// FinishReason overrides the streamed finish_reason (default stop/tool_calls).
	FinishReason string
	// CloseAfterContent closes the connection after the first content delta.
	CloseAfterContent bool
}

// ToolCall is one streamed function call.
type ToolCall struct {
	ID   string
	Name string
	Args string
}

// NewScriptedModel starts a local OpenAI-compatible server.
func NewScriptedModel() *ScriptedModel {
	m := &ScriptedModel{}
	m.server = httptest.NewServer(http.HandlerFunc(m.handle))
	return m
}

// Close shuts the server down.
func (m *ScriptedModel) Close() {
	if m != nil && m.server != nil {
		m.server.Close()
	}
}

// Script sets the sequence of turns the server will serve, in order.
func (m *ScriptedModel) Script(turns ...Turn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.turns = turns
	m.idx = 0
	m.Requests = nil
}

// BaseURL is the server root the llm.Client should be pointed at.
func (m *ScriptedModel) BaseURL() string { return m.server.URL }

// Client returns an llm.Client pointed at this server.
func (m *ScriptedModel) Client() *llm.Client {
	return llm.NewClient(&config.Config{
		OpenAIAPIKey:  "test-key",
		BaseURL:       m.server.URL,
		Model:         "gpt-4o",
		ContextWindow: 1000,
	})
}

// RequestCount returns how many completion requests were served.
func (m *ScriptedModel) RequestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Requests)
}

func (m *ScriptedModel) handle(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	m.Requests = append(m.Requests, body)

	if m.idx >= len(m.turns) {
		m.mu.Unlock()
		http.Error(w, `{"error":{"message":"no more scripted turns"}}`, http.StatusInternalServerError)
		return
	}
	turn := m.turns[m.idx]
	m.idx++
	m.mu.Unlock()

	if turn.Delay > 0 {
		time.Sleep(turn.Delay)
	}

	if turn.Status != 0 {
		body := turn.ErrorBody
		if body == "" {
			body = `{"error":{"message":"mock error"}}`
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(turn.Status)
		_, _ = w.Write([]byte(body))
		return
	}

	if turn.NonStream {
		w.Header().Set("Content-Type", "application/json")
		usage := turnUsage(turn)
		resp := map[string]any{
			"id": "chatcmpl-mock", "object": "chat.completion",
			"created": time.Now().Unix(), "model": "gpt-4o",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": turn.Content},
				"finish_reason": finishReason(turn),
			}},
		}
		if usage != nil {
			resp["usage"] = usage
		}
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no flusher", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")

	send := func(delta map[string]any, finish string, extra map[string]any) {
		choice := map[string]any{"index": 0, "delta": delta}
		if finish != "" {
			choice["finish_reason"] = finish
		}
		chunk := map[string]any{
			"id": "chatcmpl-mock", "object": "chat.completion.chunk",
			"created": time.Now().Unix(), "model": "gpt-4o",
			"choices": []map[string]any{choice},
		}
		for k, v := range extra {
			chunk[k] = v
		}
		b, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	if turn.Content != "" {
		send(map[string]any{"content": turn.Content}, "", nil)
		if turn.CloseAfterContent {
			if hijacker, ok := w.(http.Hijacker); ok {
				conn, _, err := hijacker.Hijack()
				if err == nil {
					_ = conn.Close()
				}
			}
			return
		}
	}
	if turn.MidStreamError != "" {
		fmt.Fprintf(w, "data: {\"error\":{\"message\":%q}}\n\n", turn.MidStreamError)
		flusher.Flush()
		return
	}
	finish := finishReason(turn)
	if len(turn.ToolCalls) > 0 {
		if finish == "stop" {
			finish = "tool_calls"
		}
		for i, tc := range turn.ToolCalls {
			send(map[string]any{"tool_calls": []map[string]any{{
				"index": i, "id": tc.ID, "type": "function",
				"function": map[string]any{"name": tc.Name, "arguments": tc.Args},
			}}}, "", nil)
		}
	}
	if turn.DropFinal {
		return
	}
	extra := map[string]any{}
	if turn.MalformedUsage {
		extra["usage"] = "not-an-object"
	} else if usage := turnUsage(turn); usage != nil {
		extra["usage"] = usage
	}
	send(map[string]any{}, finish, extra)
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func finishReason(turn Turn) string {
	if turn.FinishReason != "" {
		return turn.FinishReason
	}
	return "stop"
}

func turnUsage(turn Turn) any {
	if turn.MalformedUsage {
		return nil
	}
	if turn.Usage != nil {
		if len(turn.Usage) == 0 {
			return nil
		}
		return turn.Usage
	}
	return map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
}

// RequestHasNamedToolChoice reports whether a captured request forced a named function.
func RequestHasNamedToolChoice(req map[string]any, name string) bool {
	raw, ok := req["tool_choice"]
	if !ok {
		return false
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	fn, _ := obj["function"].(map[string]any)
	return obj["type"] == "function" && fn["name"] == name
}

// MessagesOf extracts the chat messages from a captured request body.
func MessagesOf(req map[string]any) []map[string]any {
	list, _ := req["messages"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, raw := range list {
		if m, ok := raw.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// ToolRoleContent returns the first tool-role message content in a request.
func ToolRoleContent(req map[string]any) string {
	for _, raw := range MessagesOf(req) {
		if raw["role"] == "tool" {
			content, _ := raw["content"].(string)
			return content
		}
	}
	return ""
}

// RequestHasAgentStatus reports whether a request carried ephemeral agent_status.
func RequestHasAgentStatus(req map[string]any) bool {
	for _, raw := range MessagesOf(req) {
		content, _ := raw["content"].(string)
		if containsAgentStatus(content) {
			return true
		}
	}
	return false
}

func containsAgentStatus(s string) bool {
	return strings.Contains(s, "<agent_status>")
}
