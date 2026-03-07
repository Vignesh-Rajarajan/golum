# Integration Test Plan

## Overview

This document outlines a comprehensive integration test strategy for the LLM client that tests end-to-end connectivity with real or recorded API interactions.

## Test Categories

### 1. Real API Integration Tests

**Purpose**: Test actual API connectivity and behavior

**Prerequisites**:
- Valid API keys (OpenAI, OpenRouter)
- Network connectivity
- Test account with sufficient quota

**Test Cases**:

#### 1.1 Basic Connectivity
```go
func TestIntegration_OpenAI_Connectivity(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping integration test in short mode")
    }
    
    cfg := loadTestConfig(t)
    client := llm.NewClient(cfg)
    
    messages := []openai.ChatCompletionMessage{
        {Role: openai.ChatMessageRoleUser, Content: "Say 'pong'"},
    }
    
    opts := llm.ChatCompletionOptions{
        Stream:     false,
        MaxRetries: 1,
        Timeout:    10 * time.Second,
    }
    
    ctx := context.Background()
    events := client.ChatCompletion(ctx, messages, opts)
    
    var content string
    for event := range events {
        if event.Type == llm.EventTypeContentDelta {
            content += event.Content
        }
        if event.Type == llam.EventTypeError {
            t.Fatalf("API error: %v", event.Error)
        }
    }
    
    if !strings.Contains(strings.ToLower(content), "pong") {
        t.Errorf("expected 'pong' in response, got: %s", content)
    }
}
```

#### 1.2 Streaming Response
```go
func TestIntegration_OpenAI_Streaming(t *testing.T) {
    cfg := loadTestConfig(t)
    client := llm.NewClient(cfg)
    
    messages := []openai.ChatCompletionMessage{
        {Role: openai.ChatMessageRoleUser, Content: "Count from 1 to 5"},
    }
    
    opts := llm.ChatCompletionOptions{
        Stream:     true,
        MaxRetries: 1,
        Timeout:    15 * time.Second,
    }
    
    events := client.ChatCompletion(context.Background(), messages, opts)
    
    chunks := 0
    for event := range events {
        if event.Type == llm.EventTypeContentDelta {
            chunks++
        }
    }
    
    if chunks < 5 {
        t.Errorf("expected at least 5 chunks, got %d", chunks)
    }
}
```

#### 1.3 Tool Calling
```go
func TestIntegration_OpenAI_ToolCalling(t *testing.T) {
    cfg := loadTestConfig(t)
    client := llm.NewClient(cfg)
    
    tools := []llm.Tool{
        {
            Type: "function",
            Function: llm.ToolFunction{
                Name:        "get_weather",
                Description: "Get weather for a location",
                Parameters: map[string]interface{}{
                    "type": "object",
                    "properties": map[string]interface{}{
                        "location": map[string]interface{}{
                            "type":        "string",
                            "description": "City name",
                        },
                    },
                },
            },
        },
    }
    
    messages := []openai.ChatCompletionMessage{
        {Role: openai.ChatMessageRoleUser, Content: "What's the weather in Tokyo?"},
    }
    
    opts := llam.ChatCompletionOptions{
        Stream:     false,
        Tools:      tools,
        MaxRetries: 1,
    }
    
    events := client.ChatCompletion(context.Background(), messages, opts)
    
    var toolCall *llm.ToolCall
    for event := range events {
        if event.Type == llam.EventTypeToolCall {
            toolCall = event.Tool
        }
    }
    
    if toolCall == nil {
        t.Fatal("expected tool call")
    }
    
    if toolCall.Name != "get_weather" {
        t.Errorf("expected tool 'get_weather', got '%s'", toolCall.Name)
    }
}
```

#### 1.4 Rate Limiting
```go
func TestIntegration_OpenAI_RateLimit(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping rate limit test in short mode")
    }
    
    cfg := loadTestConfig(t)
    client := llm.NewClient(cfg)
    
    // Send multiple requests rapidly to trigger rate limit
    var wg sync.WaitGroup
    errors := make([]error, 10)
    
    for i := 0; i < 10; i++ {
        wg.Add(1)
        go func(idx int) {
            defer wg.Done()
            
            messages := []openai.ChatCompletionMessage{
                {Role: openai.ChatMessageRoleUser, Content: "Test"},
            }
            
            opts := llm.ChatCompletionOptions{
                Stream:     false,
                MaxRetries: 3,
                Timeout:    30 * time.Second,
            }
            
            events := client.ChatCompletion(context.Background(), messages, opts)
            
            for event := range events {
                if event.Type == llm.EventTypeError {
                    errors[idx] = event.Error
                }
            }
        }(i)
    }
    
    wg.Wait()
    
    // At least some requests should succeed after retries
    successCount := 0
    for _, err := range errors {
        if err == nil {
            successCount++
        }
    }
    
    if successCount == 0 {
        t.Error("all requests failed")
    }
}
```

#### 1.5 Context Cancellation
```go
func TestIntegration_OpenAI_ContextCancellation(t *testing.T) {
    cfg := loadTestConfig(t)
    client := llm.NewClient(cfg)
    
    ctx, cancel := context.WithCancel(context.Background())
    
    messages := []openai.ChatCompletionMessage{
        {Role: openai.ChatMessageRoleUser, Content: "Tell me a very long story"},
    }
    
    opts := llm.ChatCompletionOptions{
        Stream:     true,
        MaxRetries: 0,
    }
    
    events := client.ChatCompletion(ctx, messages, opts)
    
    // Cancel after receiving a few chunks
    chunks := 0
    for event := range events {
        if event.Type == llm.EventTypeContentDelta {
            chunks++
            if chunks == 5 {
                cancel()
            }
        }
    }
    
    if chunks < 5 {
        t.Errorf("expected at least 5 chunks before cancellation, got %d", chunks)
    }
}
```

#### 1.6 OpenRouter Integration
```go
func TestIntegration_OpenRouter_Connectivity(t *testing.T) {
    apiKey := os.Getenv("OPENROUTER_API_KEY")
    if apiKey == "" {
        t.Skip("OPENROUTER_API_KEY not set")
    }
    
    cfg := &config.Config{
        OpenRouterAPIKey: apiKey,
        Model:            "openai/gpt-4o",
    }
    
    client := llm.NewClient(cfg)
    
    messages := []openai.ChatCompletionMessage{
        {Role: openai.ChatMessageRoleUser, Content: "Hello"},
    }
    
    opts := llm.ChatCompletionOptions{
        Stream:     true,
        MaxRetries: 1,
    }
    
    events := client.ChatCompletion(context.Background(), messages, opts)
    
    var hasContent bool
    for event := range events {
        if event.Type == llm.EventTypeContentDelta && event.Content != "" {
            hasContent = true
        }
    }
    
    if !hasContent {
        t.Error("no content received from OpenRouter")
    }
}
```

### 2. Recorded/Replay Tests (VCR-style)

**Purpose**: Record real API interactions and replay them for deterministic testing

**Implementation**:

```go
// recorder.go
package testutil

import (
    "encoding/json"
    "os"
    "path/filepath"
    "sync"
)

type RecordedInteraction struct {
    Request  RecordedRequest  `json:"request"`
    Response RecordedResponse `json:"response"`
}

type RecordedRequest struct {
    Method string                 `json:"method"`
    Path   string                 `json:"path"`
    Body   map[string]interface{} `json:"body"`
}

type RecordedResponse struct {
    StatusCode int               `json:"status_code"`
    Headers    map[string]string `json:"headers"`
    Body       string            `json:"body"`
}

type Recorder struct {
    mode       string // "record", "replay", "off"
    cassette   string
    recordings []RecordedInteraction
    mu         sync.Mutex
}

func NewRecorder(mode, cassette string) *Recorder {
    return &Recorder{
        mode:       mode,
        cassette:   cassette,
        recordings: make([]RecordedInteraction, 0),
    }
}

func (r *Recorder) Save() error {
    if r.mode != "record" {
        return nil
    }
    
    data, err := json.MarshalIndent(r.recordings, "", "  ")
    if err != nil {
        return err
    }
    
    path := filepath.Join("testdata", "cassettes", r.cassette+".json")
    return os.WriteFile(path, data, 0644)
}

func (r *Recorder) Load() error {
    if r.mode != "replay" {
        return nil
    }
    
    path := filepath.Join("testdata", "cassettes", r.cassette+".json")
    data, err := os.ReadFile(path)
    if err != nil {
        return err
    }
    
    return json.Unmarshal(data, &r.recordings)
}
```

**Test Example**:

```go
func TestIntegration_Recorded_Streaming(t *testing.T) {
    recorder := testutil.NewRecorder("replay", "streaming_response")
    if err := recorder.Load(); err != nil {
        t.Skip("cassette not found, run with -record flag to create")
    }
    
    // Use mock server that replays recorded interactions
    server := testutil.NewReplayServer(recorder)
    defer server.Close()
    
    cfg := &config.Config{
        OpenAIAPIKey: "test",
        BaseURL:      server.URL(),
        Model:        "gpt-4o",
    }
    
    client := llm.NewClient(cfg)
    
    messages := []openai.ChatCompletionMessage{
        {Role: openai.ChatMessageRoleUser, Content: "Hello"},
    }
    
    opts := llm.ChatCompletionOptions{
        Stream:     true,
        MaxRetries: 0,
    }
    
    events := client.ChatCompletion(context.Background(), messages, opts)
    
    // Verify deterministic response
    var content string
    for event := range events {
        if event.Type == llm.EventTypeContentDelta {
            content += event.Content
        }
    }
    
    expected := "Hello! How can I help you today?"
    if content != expected {
        t.Errorf("expected %q, got %q", expected, content)
    }
}
```

### 3. Chaos Engineering Tests

**Purpose**: Test resilience under adverse conditions

#### 3.1 Network Latency
```go
func TestChaos_HighLatency(t *testing.T) {
    // Use a proxy that adds latency
    proxy := testutil.NewLatencyProxy(500 * time.Millisecond)
    defer proxy.Close()
    
    cfg := &config.Config{
        OpenAIAPIKey: os.Getenv("OPENAI_API_KEY"),
        BaseURL:      proxy.URL(),
        Model:        "gpt-4o",
    }
    
    client := llm.NewClient(cfg)
    
    messages := []openai.ChatCompletionMessage{
        {Role: openai.ChatMessageRoleUser, Content: "Test"},
    }
    
    opts := llm.ChatCompletionOptions{
        Stream:     true,
        MaxRetries: 1,
        Timeout:    5 * time.Second,
    }
    
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()
    
    events := client.ChatCompletion(ctx, messages, opts)
    
    var success bool
    for event := range events {
        if event.Type == llm.EventTypeContentDone {
            success = true
        }
    }
    
    if !success {
        t.Error("failed under high latency")
    }
}
```

#### 3.2 Intermittent Failures
```go
func TestChaos_IntermittentFailures(t *testing.T) {
    // Use a proxy that randomly fails 50% of requests
    proxy := testutil.NewChaosProxy(0.5)
    defer proxy.Close()
    
    cfg := &config.Config{
        OpenAIAPIKey: os.Getenv("OPENAI_API_KEY"),
        BaseURL:      proxy.URL(),
        Model:        "gpt-4o",
    }
    
    client := llm.NewClient(cfg)
    
    messages := []openai.ChatCompletionMessage{
        {Role: openai.ChatMessageRoleUser, Content: "Test"},
    }
    
    opts := llm.ChatCompletionOptions{
        Stream:     false,
        MaxRetries: 5, // High retry count to handle failures
    }
    
    events := client.ChatCompletion(context.Background(), messages, opts)
    
    var success bool
    for event := range events {
        if event.Type == llm.EventTypeContentDone {
            success = true
        }
    }
    
    if !success {
        t.Error("failed to recover from intermittent failures")
    }
}
```

### 4. Performance Tests

#### 4.1 Throughput
```go
func TestPerformance_Throughput(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping performance test")
    }
    
    cfg := loadTestConfig(t)
    client := llm.NewClient(cfg)
    
    numRequests := 20
    duration := 30 * time.Second
    
    ctx, cancel := context.WithTimeout(context.Background(), duration)
    defer cancel()
    
    var wg sync.WaitGroup
    completed := make(chan int, numRequests)
    
    start := time.Now()
    
    for i := 0; i < numRequests; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            
            messages := []openai.ChatCompletionMessage{
                {Role: openai.ChatMessageRoleUser, Content: "Hello"},
            }
            
            opts := llm.ChatCompletionOptions{
                Stream:     false,
                MaxRetries: 2,
            }
            
            events := client.ChatCompletion(ctx, messages, opts)
            
            for event := range events {
                if event.Type == llm.EventTypeContentDone {
                    completed <- 1
                }
            }
        }()
    }
    
    wg.Wait()
    close(completed)
    
    elapsed := time.Since(start)
    
    var count int
    for range completed {
        count++
    }
    
    throughput := float64(count) / elapsed.Seconds()
    t.Logf("Throughput: %.2f requests/second", throughput)
    t.Logf("Completed: %d/%d requests", count, numRequests)
    
    if count < numRequests/2 {
        t.Errorf("too few requests completed: %d/%d", count, numRequests)
    }
}
```

#### 4.2 Memory Usage
```go
func TestPerformance_MemoryUsage(t *testing.T) {
    cfg := loadTestConfig(t)
    client := llm.NewClient(cfg)
    
    var m1, m2 runtime.MemStats
    runtime.GC()
    runtime.ReadMemStats(&m1)
    
    // Make 10 requests
    for i := 0; i < 10; i++ {
        messages := []openai.ChatCompletionMessage{
            {Role: openai.ChatMessageRoleUser, Content: "Write a 100 word story"},
        }
        
        opts := llm.ChatCompletionOptions{
            Stream:     true,
            MaxRetries: 0,
        }
        
        events := client.ChatCompletion(context.Background(), messages, opts)
        
        // Drain events
        for range events {
        }
    }
    
    runtime.GC()
    runtime.ReadMemStats(&m2)
    
    memIncrease := m2.Alloc - m1.Alloc
    t.Logf("Memory increase: %d bytes (%.2f MB)", memIncrease, float64(memIncrease)/1024/1024)
    
    // Check for excessive memory usage
    maxAllowed := 50 * 1024 * 1024 // 50 MB
    if memIncrease > maxAllowed {
        t.Errorf("memory leak detected: %d bytes", memIncrease)
    }
}
```

## Test Infrastructure

### Directory Structure
```
pkg/llm/
├── integration_test.go      # Integration tests
├── chaos_test.go            # Chaos engineering tests
├── performance_test.go      # Performance tests
├── testutil/
│   ├── recorder.go          # VCR-style recorder
│   ├── proxy.go             # Test proxies
│   └── fixtures.go          # Test fixtures
└── testdata/
    └── cassettes/           # Recorded API interactions
        ├── streaming_response.json
        ├── tool_calling.json
        └── rate_limit.json
```

### Running Tests

```bash
# Run unit tests only
go test ./pkg/llm -v

# Run integration tests (requires API keys)
go test ./pkg/llm -v -tags=integration

# Run performance tests
go test ./pkg/llm -v -tags=performance

# Run all tests
go test ./pkg/llm -v -tags=all

# Record new cassettes
go test ./pkg/llm -v -tags=integration -record

# Run with specific cassette
go test ./pkg/llm -v -cassette=streaming_response
```

### CI/CD Integration

```yaml
# .github/workflows/test.yml
name: Tests

on: [push, pull_request]

jobs:
  unit-tests:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-go@v4
        with:
          go-version: '1.24'
      - run: go test ./pkg/llm -v -coverprofile=coverage.out
      - uses: codecov/codecov-action@v3
        with:
          file: ./coverage.out

  integration-tests:
    runs-on: ubuntu-latest
    if: github.event_name == 'push' && github.ref == 'refs/heads/main'
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-go@v4
        with:
          go-version: '1.24'
      - env:
          OPENAI_API_KEY: ${{ secrets.OPENAI_API_KEY }}
        run: go test ./pkg/llm -v -tags=integration -timeout=30m

  performance-tests:
    runs-on: ubuntu-latest
    if: github.event_name == 'schedule'
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-go@v4
        with:
          go-version: '1.24'
      - env:
          OPENAI_API_KEY: ${{ secrets.OPENAI_API_KEY }}
        run: go test ./pkg/llm -v -tags=performance -timeout=1h
```

## Test Data Management

### Fixtures
```go
// testutil/fixtures.go
package testutil

var TestMessages = struct {
    Simple     []openai.ChatCompletionMessage
    Complex    []openai.ChatCompletionMessage
    WithSystem []openai.ChatCompletionMessage
}{
    Simple: []openai.ChatCompletionMessage{
        {Role: openai.ChatMessageRoleUser, Content: "Hello"},
    },
    Complex: []openai.ChatCompletionMessage{
        {Role: openai.ChatMessageRoleSystem, Content: "You are a helpful assistant"},
        {Role: openai.ChatMessageRoleUser, Content: "What is AI?"},
        {Role: openai.ChatMessageRoleAssistant, Content: "AI is..."},
        {Role: openai.ChatMessageRoleUser, Content: "Tell me more"},
    },
    WithSystem: []openai.ChatCompletionMessage{
        {Role: openai.ChatMessageRoleSystem, Content: "Be concise"},
        {Role: openai.ChatMessageRoleUser, Content: "Explain quantum computing"},
    },
}
```

## Monitoring & Observability

### Test Metrics
```go
// testutil/metrics.go
package testutil

type TestMetrics struct {
    RequestCount    int
    SuccessCount    int
    ErrorCount      int
    TotalDuration   time.Duration
    AvgDuration     time.Duration
    RateLimitHits   int
    RetryCount      int
}

func (m *TestMetrics) Record(duration time.Duration, success bool, retries int) {
    m.RequestCount++
    m.TotalDuration += duration
    
    if success {
        m.SuccessCount++
    } else {
        m.ErrorCount++
    }
    
    m.RetryCount += retries
}

func (m *TestMetrics) Report() string {
    return fmt.Sprintf(
        "Requests: %d, Success: %d, Errors: %d, Avg Duration: %v, Retries: %d",
        m.RequestCount,
        m.SuccessCount,
        m.ErrorCount,
        m.AvgDuration,
        m.RetryCount,
    )
}
```

## Implementation Checklist

- [ ] Create test utilities (recorder, proxy, fixtures)
- [ ] Implement basic connectivity tests
- [ ] Implement streaming tests
- [ ] Implement tool calling tests
- [ ] Implement rate limiting tests
- [ ] Implement context cancellation tests
- [ ] Implement OpenRouter tests
- [ ] Create recorded cassettes for deterministic testing
- [ ] Implement chaos engineering tests
- [ ] Implement performance tests
- [ ] Set up CI/CD pipeline
- [ ] Add test metrics collection
- [ ] Document test procedures

## Success Criteria

1. **Coverage**: >90% code coverage on unit tests
2. **Reliability**: All integration tests pass 99% of the time
3. **Performance**: No memory leaks, goroutine leaks
4. **Resilience**: Recovery from rate limits, network errors
5. **Documentation**: All test cases documented with expected behavior
