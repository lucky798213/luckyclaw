package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
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
	Stream      bool          `json:"stream,omitempty"`
}

type chatCompletionChunk struct {
	Choices []struct {
		Delta struct {
			Content   string              `json:"content"`
			ToolCalls []chatToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
}

type chatToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIStream struct {
	ctx       context.Context
	body      io.ReadCloser
	scanner   *bufio.Scanner
	content   strings.Builder
	toolCalls map[int]*ToolCall
	done      bool
	closed    bool
	closeErr  error
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

// ChatStream 调用 OpenAI 兼容的流式 Chat Completions API。
func (p *OpenAIProvider) ChatStream(ctx context.Context, messages []Message, tools []Tool, model string, maxTokens int, temperature float64) (Stream, error) {
	httpReq, err := p.buildStreamRequest(ctx, messages, tools, model, maxTokens, temperature)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return &openAIStream{
		ctx:       ctx,
		body:      resp.Body,
		scanner:   scanner,
		toolCalls: make(map[int]*ToolCall),
	}, nil
}

func (p *OpenAIProvider) buildRequest(ctx context.Context, messages []Message, tools []Tool, model string, maxTokens int, temperature float64) (*http.Request, error) {
	return p.buildRequestWithStream(ctx, messages, tools, model, maxTokens, temperature, false)
}

func (p *OpenAIProvider) buildStreamRequest(ctx context.Context, messages []Message, tools []Tool, model string, maxTokens int, temperature float64) (*http.Request, error) {
	return p.buildRequestWithStream(ctx, messages, tools, model, maxTokens, temperature, true)
}

func (p *OpenAIProvider) buildRequestWithStream(ctx context.Context, messages []Message, tools []Tool, model string, maxTokens int, temperature float64, stream bool) (*http.Request, error) {
	req := chatRequest{
		Model:       model,
		Messages:    toChatMessages(messages),
		Tools:       tools,
		MaxTokens:   maxTokens,
		Temperature: temperature,
		Stream:      stream,
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

// Next 返回下一个文本增量或最终完整消息。
func (s *openAIStream) Next() (StreamChunk, error) {
	if s.done {
		return StreamChunk{}, io.EOF
	}
	if err := s.ctx.Err(); err != nil {
		return StreamChunk{}, err
	}

	for s.scanner.Scan() {
		line := s.scanner.Text()
		if line == "" || strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			s.done = true
			message := s.finalMessage()
			return StreamChunk{Message: &message, Done: true}, nil
		}

		var chunk chatCompletionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return StreamChunk{}, fmt.Errorf("decode stream chunk: %w", err)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta
		s.appendToolCallDeltas(delta.ToolCalls)
		if delta.Content != "" {
			s.content.WriteString(delta.Content)
			return StreamChunk{Delta: delta.Content}, nil
		}
	}

	if err := s.ctx.Err(); err != nil {
		return StreamChunk{}, err
	}
	if err := s.scanner.Err(); err != nil {
		return StreamChunk{}, fmt.Errorf("read stream: %w", err)
	}
	return StreamChunk{}, io.ErrUnexpectedEOF
}

func (s *openAIStream) appendToolCallDeltas(deltas []chatToolCallDelta) {
	for _, delta := range deltas {
		call, exists := s.toolCalls[delta.Index]
		if !exists {
			call = &ToolCall{}
			s.toolCalls[delta.Index] = call
		}
		if delta.ID != "" {
			call.ID = delta.ID
		}
		if delta.Type != "" {
			call.Type = delta.Type
		}
		call.Function.Name += delta.Function.Name
		call.Function.Arguments += delta.Function.Arguments
	}
}

func (s *openAIStream) finalMessage() Message {
	indexes := make([]int, 0, len(s.toolCalls))
	for index := range s.toolCalls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	toolCalls := make([]ToolCall, 0, len(indexes))
	for _, index := range indexes {
		call := *s.toolCalls[index]
		if call.Type == "" {
			call.Type = "function"
		}
		toolCalls = append(toolCalls, call)
	}
	message := Message{
		Role:      "assistant",
		Content:   s.content.String(),
		ToolCalls: toolCalls,
	}
	raw, err := json.Marshal(chatMessage{
		Role:      message.Role,
		Content:   message.Content,
		ToolCalls: message.ToolCalls,
	})
	if err == nil {
		message.RawAssistant = raw
	}
	return message
}

// Close 关闭上游模型响应体，可重复调用。
func (s *openAIStream) Close() error {
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	s.closeErr = s.body.Close()
	return s.closeErr
}
