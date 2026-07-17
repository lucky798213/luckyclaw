package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenAIProviderBuildRequestConvertsMessages(t *testing.T) {
	provider, err := NewOpenAI("test-key", "https://api.example.com/v1")
	if err != nil {
		t.Fatal(err)
	}

	request, err := provider.buildRequest(
		context.Background(),
		[]Message{
			{
				Role:    "assistant",
				Content: "calling tool",
				ToolCalls: []ToolCall{
					{
						ID:   "call-1",
						Type: "function",
						Function: FunctionCall{
							Name:      "weather",
							Arguments: `{"city":"Shanghai"}`,
						},
					},
				},
				RawAssistant: json.RawMessage(`{"internal":true}`),
			},
			{
				Role:       "tool",
				Content:    "sunny",
				ToolCallID: "call-1",
				Name:       "weather",
			},
		},
		[]Tool{
			{
				Type: "function",
				Function: ToolFunction{
					Name:       "weather",
					Parameters: map[string]any{"type": "object"},
				},
			},
		},
		"test-model",
		100,
		0.7,
	)
	if err != nil {
		t.Fatal(err)
	}

	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}

	var requestBody struct {
		Messages []map[string]any `json:"messages"`
		Tools    []Tool           `json:"tools"`
	}
	if err := json.Unmarshal(body, &requestBody); err != nil {
		t.Fatal(err)
	}
	if len(requestBody.Messages) != 2 {
		t.Fatalf("message count = %d, want 2", len(requestBody.Messages))
	}
	if requestBody.Messages[0]["role"] != "assistant" || requestBody.Messages[0]["content"] != "calling tool" {
		t.Fatalf("unexpected assistant message: %#v", requestBody.Messages[0])
	}
	if _, ok := requestBody.Messages[0]["tool_calls"]; !ok {
		t.Fatalf("assistant message does not contain tool_calls: %#v", requestBody.Messages[0])
	}
	if _, ok := requestBody.Messages[0]["_raw"]; ok {
		t.Fatalf("assistant message contains internal _raw field: %#v", requestBody.Messages[0])
	}
	if requestBody.Messages[1]["tool_call_id"] != "call-1" || requestBody.Messages[1]["name"] != "weather" {
		t.Fatalf("unexpected tool message: %#v", requestBody.Messages[1])
	}
	if len(requestBody.Tools) != 1 || requestBody.Tools[0].Function.Name != "weather" {
		t.Fatalf("unexpected tools: %#v", requestBody.Tools)
	}
}

func TestOpenAIProviderChat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{
				map[string]any{
					"message": map[string]any{
						"role":    "assistant",
						"content": "hello",
					},
				},
			},
		})
	}))
	defer server.Close()

	provider, err := NewOpenAI("test-key", server.URL)
	if err != nil {
		t.Fatal(err)
	}

	message, err := provider.Chat(
		context.Background(),
		[]Message{{Role: "user", Content: "hi"}},
		nil,
		"test-model",
		100,
		0.7,
	)
	if err != nil {
		t.Fatal(err)
	}
	if message.Role != "assistant" || message.Content != "hello" {
		t.Fatalf("unexpected message: %+v", message)
	}
}

func TestOpenAIProviderChatStreamAssemblesContentAndToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body["stream"] != true {
			t.Errorf("stream = %#v, want true", body["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"cal\",\"arguments\":\"{\\\"expression\\\":\\\"6*\"}}]}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"好\",\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"culator\",\"arguments\":\"8\\\"}\"}}]}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	current, err := NewOpenAI("test-key", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := current.ChatStream(context.Background(), []Message{{Role: "user", Content: "计算"}}, nil, "test-model", 100, 0.7)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	var deltas string
	var final *Message
	for {
		chunk, nextErr := stream.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		deltas += chunk.Delta
		if chunk.Done {
			final = chunk.Message
			break
		}
	}
	if deltas != "你好" {
		t.Fatalf("deltas = %q, want 你好", deltas)
	}
	if final == nil || final.Content != "你好" || len(final.ToolCalls) != 1 {
		t.Fatalf("final = %+v", final)
	}
	call := final.ToolCalls[0]
	if call.ID != "call-1" || call.Function.Name != "calculator" || call.Function.Arguments != `{"expression":"6*8"}` {
		t.Fatalf("tool call = %+v", call)
	}
	if len(final.RawAssistant) == 0 {
		t.Fatal("final RawAssistant is empty")
	}
}

func TestOpenAIProviderChatStreamRejectsNonOKResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	current, err := NewOpenAI("test-key", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = current.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, "test-model", 100, 0.7)
	if err == nil || !strings.Contains(err.Error(), "API error 503") {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenAIProviderChatStreamReportsMalformedChunkAndUnexpectedEOF(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		nextCalls int
		want      string
	}{
		{name: "畸形 JSON", body: "data: not-json\n\n", nextCalls: 1, want: "decode stream chunk"},
		{name: "缺少 DONE", body: "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n", nextCalls: 2, want: io.ErrUnexpectedEOF.Error()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			current, err := NewOpenAI("test-key", server.URL)
			if err != nil {
				t.Fatal(err)
			}
			stream, err := current.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, "test-model", 100, 0.7)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			for index := 0; index < test.nextCalls-1; index++ {
				if _, err := stream.Next(); err != nil {
					t.Fatalf("Next() %d error = %v", index+1, err)
				}
			}
			_, err = stream.Next()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestOpenAIProviderChatStreamCancellationReachesUpstream(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}))
	defer server.Close()
	current, err := NewOpenAI("test-key", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := current.ChatStream(ctx, []Message{{Role: "user", Content: "hi"}}, nil, "test-model", 100, 0.7)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	cancel()
	if _, err := stream.Next(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Next() error = %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("上游请求没有收到取消信号")
	}
}

type trackingBody struct {
	io.Reader
	closed bool
}

func (b *trackingBody) Close() error {
	b.closed = true
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestOpenAIProviderStreamCloseClosesResponseBodyOnce(t *testing.T) {
	body := &trackingBody{Reader: strings.NewReader("data: [DONE]\n\n")}
	current, err := NewOpenAI("test-key", "https://api.example.com/v1")
	if err != nil {
		t.Fatal(err)
	}
	current.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header)}, nil
	})}
	stream, err := current.ChatStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, "test-model", 100, 0.7)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if !body.closed {
		t.Fatal("关闭流时没有关闭响应体")
	}
}
