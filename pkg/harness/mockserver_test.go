package harness

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/Vignesh-Rajarajan/golum/pkg/config"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
)

// mockLLM is a minimal OpenAI-compatible server for driving RunAgentLoop
// end-to-end. pkg/llm has its own richer mock, but it lives in that package's
// test binary and cannot be imported here.
type mockLLM struct {
	server *httptest.Server

	mu       sync.Mutex
	turns    []mockTurn
	idx      int
	Requests []map[string]any
}

// mockTurn is one scripted model response.
type mockTurn struct {
	// Content is streamed as a single content delta.
	Content string
	// ToolCalls, when non-empty, are emitted as streamed tool-call fragments
	// and the turn finishes with finish_reason=tool_calls.
	ToolCalls []mockToolCall
	// Status, when non-zero, returns an HTTP error instead of a stream.
	Status int
	// ErrorBody is the body returned alongside Status.
	ErrorBody string
	// NonStream serves a non-streaming JSON completion (used by summarization).
	NonStream bool
}

type mockToolCall struct {
	ID   string
	Name string
	Args string
}

func newMockLLM() *mockLLM {
	m := &mockLLM{}
	m.server = httptest.NewServer(http.HandlerFunc(m.handle))
	return m
}

func (m *mockLLM) Close() { m.server.Close() }

// Script sets the sequence of turns the server will serve, in order.
func (m *mockLLM) Script(turns ...mockTurn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.turns = turns
	m.idx = 0
}

// Client returns an llm.Client pointed at this server.
func (m *mockLLM) Client() *llm.Client {
	return llm.NewClient(&config.Config{
		OpenAIAPIKey:  "test-key",
		BaseURL:       m.server.URL,
		Model:         "gpt-4o",
		ContextWindow: 1000,
	})
}

// RequestCount returns how many completion requests were served.
func (m *mockLLM) RequestCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Requests)
}

func (m *mockLLM) handle(w http.ResponseWriter, r *http.Request) {
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

	if turn.Status != 0 {
		body := turn.ErrorBody
		if body == "" {
			body = `{"error":{"message":"mock error"}}`
		}
		// Write the JSON envelope directly: http.Error would set text/plain and
		// the client could not parse the provider error message out of it.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(turn.Status)
		_, _ = w.Write([]byte(body))
		return
	}

	if turn.NonStream {
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{
			"id": "chatcmpl-mock", "object": "chat.completion",
			"created": time.Now().Unix(), "model": "gpt-4o",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": turn.Content},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
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

	send := func(delta map[string]any, finish string) {
		choice := map[string]any{"index": 0, "delta": delta}
		if finish != "" {
			choice["finish_reason"] = finish
		}
		chunk := map[string]any{
			"id": "chatcmpl-mock", "object": "chat.completion.chunk",
			"created": time.Now().Unix(), "model": "gpt-4o",
			"choices": []map[string]any{choice},
		}
		b, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	if turn.Content != "" {
		send(map[string]any{"content": turn.Content}, "")
	}
	finish := "stop"
	if len(turn.ToolCalls) > 0 {
		finish = "tool_calls"
		for i, tc := range turn.ToolCalls {
			send(map[string]any{"tool_calls": []map[string]any{{
				"index": i, "id": tc.ID, "type": "function",
				"function": map[string]any{"name": tc.Name, "arguments": tc.Args},
			}}}, "")
		}
	}
	send(map[string]any{}, finish)
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}
