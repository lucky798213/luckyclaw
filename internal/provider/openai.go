package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// OpenAIProvider 使用 OpenAI 兼容的 Chat Completions API。
type OpenAIProvider struct {
	apiKey  string
	apiBase string
	client  *http.Client
}

// chatMessage 表示 OpenAI Chat Completions API 使用的消息格式。
type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// chatRequest 表示 OpenAI Chat Completions API 的请求体。
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Tools       []Tool        `json:"tools,omitempty"`
	MaxTokens   int           `json:"max_tokens"`
	Temperature float64       `json:"temperature"`
}

// toChatMessages 将 Provider 的统一消息转换为 OpenAI 消息。
func toChatMessages(messages []Message) []chatMessage {
	chatMessages := make([]chatMessage, 0, len(messages))
	for _, message := range messages {
		chatMessages = append(chatMessages, chatMessage{
			Role:       message.Role,
			Content:    message.Content,
			ToolCalls:  message.ToolCalls,
			ToolCallID: message.ToolCallID,
			Name:       message.Name,
		})
	}
	return chatMessages
}

// NewOpenAI 创建一个 OpenAI 兼容的 Provider。
func NewOpenAI(apiKey, apiBase string) (*OpenAIProvider, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("api key cannot be empty")
	}
	if strings.TrimSpace(apiBase) == "" {
		return nil, fmt.Errorf("api base cannot be empty")
	}

	return &OpenAIProvider{
		apiKey:  apiKey,
		apiBase: strings.TrimRight(apiBase, "/"),
		client:  http.DefaultClient,
	}, nil
}

// Chat 调用 OpenAI 兼容的 Chat Completions API。
func (p *OpenAIProvider) Chat(ctx context.Context, messages []Message, tools []Tool, model string, maxTokens int, temperature float64) (*Message, error) {
	httpReq, err := p.buildRequest(ctx, messages, tools, model, maxTokens, temperature)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("response contains no choices")
	}

	return &result.Choices[0].Message, nil
}

func (p *OpenAIProvider) buildRequest(ctx context.Context, messages []Message, tools []Tool, model string, maxTokens int, temperature float64) (*http.Request, error) {
	req := chatRequest{
		Model:       model,
		Messages:    toChatMessages(messages),
		Tools:       tools,
		MaxTokens:   maxTokens,
		Temperature: temperature,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiBase+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	return httpReq, nil
}
