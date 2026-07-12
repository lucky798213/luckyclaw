// Package provider 定义大模型服务提供方的统一接口。
package provider

import (
	"context"
	"encoding/json"
)

// Provider 定义大模型服务提供方需要实现的能力。
type Provider interface {
	Chat(ctx context.Context, messages []Message, tools []Tool, model string, maxTokens int, temperature float64) (*Message, error)
}

// 表示一条消息
type Message struct {
	// system、user、assistant、tool
	Role string `json:"role"`

	// 普通文本，或者工具执行结果
	Content string `json:"content,omitempty"`

	// assistant 消息：模型请求调用的工具
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`

	// tool 消息：对应哪一次工具调用
	ToolCallID string `json:"tool_call_id,omitempty"`

	// tool 消息：工具名称，方便日志和 UI 展示
	Name string `json:"name,omitempty"`

	// assistant 消息：模型返回的原始 JSON
	// FastClaw 用于提示词缓存和 Provider 兼容
	RawAssistant json.RawMessage `json:"_raw,omitempty"`
}

// ToolCall：模型填好的表格，说明它想调用哪个工具、具体参数是什么。
// ID：本次调用的唯一编号，返回工具结果时需要带回来。
// type：function，mcp。。。
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool：空白表格，说明工具叫什么、做什么、需要填写哪些参数。
// type：Function，mcp。。。
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters"`
}
