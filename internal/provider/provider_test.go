package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
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
