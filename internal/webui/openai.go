package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/lucky798213/luckyclaw/internal/agent"
	"github.com/lucky798213/luckyclaw/internal/provider"
)

const maxChatCompletionsBody = 1 << 20

type chatCompletionsRequest struct {
	Model       string                   `json:"model"`
	Messages    []chatCompletionsMessage `json:"messages"`
	Stream      bool                     `json:"stream,omitempty"`
	Temperature *float64                 `json:"temperature,omitempty"`
	MaxTokens   *int                     `json:"max_tokens,omitempty"`
	Tools       json.RawMessage          `json:"tools,omitempty"`
	ToolChoice  json.RawMessage          `json:"tool_choice,omitempty"`
}

type chatCompletionsMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIErrorBody struct {
	Error openAIError `json:"error"`
}

type openAIError struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    *string `json:"code"`
}

func (s *Server) chatCompletions(w http.ResponseWriter, r *http.Request) {
	current := s.agents.DefaultAgent()
	if requestedAgent := strings.TrimSpace(r.Header.Get("X-LuckyClaw-Agent-ID")); requestedAgent != "" {
		current = s.agents.AgentByID(requestedAgent)
		if current == nil {
			writeOpenAIError(w, http.StatusNotFound, "找不到指定的 Agent", "invalid_request_error", "X-LuckyClaw-Agent-ID")
			return
		}
	}
	request, err := decodeChatCompletionsRequest(w, r)
	if err != nil {
		return
	}
	request.Model = strings.TrimSpace(request.Model)
	if validation := validateChatCompletionsRequest(current, request); validation != nil {
		writeOpenAIError(w, http.StatusBadRequest, validation.message, "invalid_request_error", validation.param)
		return
	}
	messages := make([]provider.Message, 0, len(request.Messages))
	for _, message := range request.Messages {
		messages = append(messages, provider.Message{Role: message.Role, Content: message.Content})
	}
	options := agent.CompletionOptions{ModelRef: request.Model, Temperature: request.Temperature}
	if request.MaxTokens != nil {
		options.MaxTokens = *request.MaxTokens
	}
	if request.Stream {
		s.streamChatCompletion(w, r, current, request.Model, messages, options)
		return
	}
	reply, err := current.Complete(r.Context(), messages, options)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "模型请求失败", "api_error", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      "chatcmpl-" + randomID(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   request.Model,
		"choices": []any{map[string]any{
			"index": 0,
			"message": map[string]string{
				"role":    "assistant",
				"content": reply.Content,
			},
			"finish_reason": "stop",
		}},
		"usage": zeroTokenUsage(),
	})
}

func (s *Server) streamChatCompletion(
	w http.ResponseWriter,
	r *http.Request,
	current *agent.Agent,
	model string,
	messages []provider.Message,
	options agent.CompletionOptions,
) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, http.StatusInternalServerError, "当前服务器不支持流式响应", "api_error", "")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	events := current.CompleteStream(ctx, messages, options)
	defer func() {
		cancel()
		for range events {
		}
	}()
	id := "chatcmpl-" + randomID()
	created := time.Now().Unix()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err := writeOpenAIStreamData(w, flusher, completionChunk(id, model, created, map[string]string{"role": "assistant"}, nil)); err != nil {
		return
	}
	for event := range events {
		switch event.Type {
		case agent.EventTokenDelta:
			if err := writeOpenAIStreamData(w, flusher, completionChunk(id, model, created, map[string]string{"content": event.Data.Delta}, nil)); err != nil {
				cancel()
				return
			}
		case agent.EventFinal:
			finishReason := "stop"
			if err := writeOpenAIStreamData(w, flusher, completionChunk(id, model, created, map[string]string{}, &finishReason)); err != nil {
				return
			}
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		case agent.EventError:
			errorPayload := openAIErrorBody{Error: openAIError{Message: event.Data.Message, Type: "api_error"}}
			_ = writeOpenAIStreamData(w, flusher, errorPayload)
			return
		case agent.EventToolStart, agent.EventToolResult:
			// 内部工具状态只服务 LuckyClaw 页面，不暴露到 OpenAI 兼容流。
		}
	}
}

func completionChunk(id, model string, created int64, delta map[string]string, finishReason *string) map[string]any {
	return map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         delta,
			"finish_reason": finishReason,
		}},
	}
}

func writeOpenAIStreamData(w http.ResponseWriter, flusher http.Flusher, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func decodeChatCompletionsRequest(w http.ResponseWriter, r *http.Request) (chatCompletionsRequest, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxChatCompletionsBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request chatCompletionsRequest
	if err := decoder.Decode(&request); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "请求内容格式不正确", "invalid_request_error", "")
		return chatCompletionsRequest{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeOpenAIError(w, http.StatusBadRequest, "请求体只能包含一个 JSON 对象", "invalid_request_error", "")
		return chatCompletionsRequest{}, fmt.Errorf("unexpected trailing JSON content")
	}
	return request, nil
}

type requestValidationError struct {
	message string
	param   string
}

func validateChatCompletionsRequest(current *agent.Agent, request chatCompletionsRequest) *requestValidationError {
	if request.Model == "" {
		return &requestValidationError{message: "model 为必填字段", param: "model"}
	}
	if _, err := provider.ParseModelRef(request.Model); err != nil {
		return &requestValidationError{message: "model 必须使用 provider/model 格式", param: "model"}
	}
	if !contains(current.Models(), request.Model) {
		return &requestValidationError{message: "所选 Agent 不允许使用这个模型", param: "model"}
	}
	if len(request.Messages) == 0 {
		return &requestValidationError{message: "messages 不能为空", param: "messages"}
	}
	for index, message := range request.Messages {
		switch message.Role {
		case "system", "developer", "user", "assistant":
		default:
			return &requestValidationError{message: "仅支持 system、developer、user、assistant 文本消息", param: fmt.Sprintf("messages.%d.role", index)}
		}
	}
	if request.Temperature != nil && (*request.Temperature < 0 || *request.Temperature > 2) {
		return &requestValidationError{message: "temperature 必须在 0 到 2 之间", param: "temperature"}
	}
	if request.MaxTokens != nil && *request.MaxTokens <= 0 {
		return &requestValidationError{message: "max_tokens 必须大于 0", param: "max_tokens"}
	}
	if request.Tools != nil {
		return &requestValidationError{message: "当前版本不支持客户端 tools", param: "tools"}
	}
	if request.ToolChoice != nil {
		return &requestValidationError{message: "当前版本不支持客户端 tool_choice", param: "tool_choice"}
	}
	return nil
}

func zeroTokenUsage() map[string]int {
	return map[string]int{
		"prompt_tokens":     0,
		"completion_tokens": 0,
		"total_tokens":      0,
	}
}

func writeOpenAIError(w http.ResponseWriter, status int, message, errorType, param string) {
	var paramPointer *string
	if param != "" {
		paramPointer = &param
	}
	writeJSON(w, status, openAIErrorBody{Error: openAIError{
		Message: message,
		Type:    errorType,
		Param:   paramPointer,
	}})
}
