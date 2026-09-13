package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
)

type stdioBackend struct {
	name string
	cmd  *exec.Cmd
	w    io.WriteCloser
	mu   sync.Mutex
	id   atomic.Int64
	wait chan rpcResult
	err  error
}

type rpcResult struct {
	id     int64
	result json.RawMessage
	rpcErr string
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Message string `json:"message"`
}

// StartStdio launches command as an MCP stdio server and completes initialize.
func StartStdio(ctx context.Context, name string, spec ServerSpec) (Backend, error) {
	cmd := exec.Command(spec.Command, spec.Args...)
	cmd.Env = os.Environ()
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	b := &stdioBackend{name: name, cmd: cmd, w: stdin, wait: make(chan rpcResult, 8)}
	go b.readLoop(stdout)
	if err := b.initialize(ctx); err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	return b, nil
}

func (b *stdioBackend) Name() string { return b.name }

func (b *stdioBackend) List(ctx context.Context) ([]Operation, error) {
	raw, err := b.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	out := make([]Operation, 0, len(parsed.Tools))
	for _, t := range parsed.Tools {
		out = append(out, Operation{
			Name: t.Name, Description: t.Description, Version: "mcp", Schema: t.InputSchema,
		})
	}
	return out, nil
}

func (b *stdioBackend) Call(ctx context.Context, name string, args map[string]any) (string, error) {
	raw, err := b.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", err
	}
	var parsed struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return string(raw), nil
	}
	var bld string
	for _, c := range parsed.Content {
		if c.Type == "text" || c.Type == "" {
			bld += c.Text
		}
	}
	if parsed.IsError {
		return bld, fmt.Errorf("%s", bld)
	}
	return bld, nil
}

func (b *stdioBackend) initialize(ctx context.Context) error {
	_, err := b.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "golum", "version": "0.1.0"},
	})
	if err != nil {
		return err
	}
	return b.notify("notifications/initialized")
}

func (b *stdioBackend) notify(method string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	enc := json.NewEncoder(b.w)
	return enc.Encode(rpcRequest{JSONRPC: "2.0", Method: method})
}

func (b *stdioBackend) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := b.id.Add(1)
	b.mu.Lock()
	enc := json.NewEncoder(b.w)
	err := enc.Encode(rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params})
	b.mu.Unlock()
	if err != nil {
		return nil, err
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case res, ok := <-b.wait:
			if !ok {
				if b.err != nil {
					return nil, b.err
				}
				return nil, fmt.Errorf("mcp server closed")
			}
			if res.id != id {
				continue
			}
			if res.rpcErr != "" {
				return nil, fmt.Errorf("%s", res.rpcErr)
			}
			return res.result, nil
		}
	}
}

func (b *stdioBackend) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var resp rpcResponse
		if json.Unmarshal(line, &resp) != nil || resp.ID == 0 {
			continue
		}
		msg := rpcResult{id: resp.ID, result: resp.Result}
		if resp.Error != nil {
			msg.rpcErr = resp.Error.Message
		}
		select {
		case b.wait <- msg:
		default:
		}
	}
	b.err = sc.Err()
	close(b.wait)
}
