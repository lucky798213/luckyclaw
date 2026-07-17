// Package mcp 实现 Model Context Protocol 客户端。
package mcp

import (
	"context"
	"encoding/json"
	"time"
)

const latestProtocolVersion = "2025-11-25"

var supportedProtocolVersions = map[string]struct{}{
	"2025-11-25": {},
	"2025-06-18": {},
	"2025-03-26": {},
	"2024-11-05": {},
}

// Client 定义 MCP 传输需要提供的最小能力，后续 HTTP 可以实现同一接口。
type Client interface {
	Connect(ctx context.Context) error
	ListTools(ctx context.Context) ([]ToolDefinition, error)
	CallTool(ctx context.Context, name string, arguments json.RawMessage) (ToolResult, error)
	Close(ctx context.Context) error
}

// ToolDefinition 是 MCP 服务端返回的工具定义。
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ToolResult 保存 tools/call 的完整结果。
type ToolResult struct {
	Content           []json.RawMessage `json:"content"`
	StructuredContent json.RawMessage   `json:"structuredContent,omitempty"`
	IsError           bool              `json:"isError,omitempty"`
}

// StdioOptions 保存 stdio MCP 子进程参数。
type StdioOptions struct {
	Command        string
	Args           []string
	Env            map[string]string
	RequestTimeout time.Duration
	MaxResultBytes int
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type responseOrError struct {
	response rpcResponse
	err      error
}
