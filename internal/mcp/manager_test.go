package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lucky798213/luckyclaw/internal/config"
)

func TestManagerRegistersPrefixedTools(t *testing.T) {
	cfg := config.MCPConfig{
		RequestTimeoutSeconds: 1,
		MaxResultBytes:        1024,
		Servers: map[string]config.MCPServerConfig{
			"demo": {Transport: "stdio", Command: "ignored"},
		},
	}
	client := &fakeManagerClient{definitions: []ToolDefinition{{
		Name: "echo", Description: "echo tool", InputSchema: map[string]any{"type": "object"},
	}}}
	manager, err := newManager(context.Background(), cfg, []string{"demo"}, func(config.MCPServerConfig, StdioOptions) (Client, error) {
		return client, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	registered, err := manager.ToolsFor([]string{"demo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(registered) != 1 || registered[0].Definition().Function.Name != "mcp_demo_echo" {
		t.Fatalf("registered = %+v", registered)
	}
	result, err := registered[0].Execute(context.Background(), json.RawMessage(`{"value":"hello"}`))
	if err != nil || result != "hello" {
		t.Fatalf("Execute() = %q, %v", result, err)
	}
}

func TestExposedToolNameIsStableAndBounded(t *testing.T) {
	first := exposedToolName(strings.Repeat("server", 20), strings.Repeat("tool", 30))
	second := exposedToolName(strings.Repeat("server", 20), strings.Repeat("tool", 30))
	if first != second || len(first) > maxProviderToolNameBytes {
		t.Fatalf("tool name = %q", first)
	}
}

func TestExpandEnvironmentRequiresReferencedValue(t *testing.T) {
	t.Setenv("LUCKYCLAW_MCP_TOKEN", "secret")
	resolved, err := expandEnvironment(map[string]string{"TOKEN": "$LUCKYCLAW_MCP_TOKEN", "MODE": "test"})
	if err != nil || resolved["TOKEN"] != "secret" || resolved["MODE"] != "test" {
		t.Fatalf("expandEnvironment() = %v, %v", resolved, err)
	}
	if _, err := expandEnvironment(map[string]string{"TOKEN": "$LUCKYCLAW_MISSING_TOKEN"}); err == nil {
		t.Fatal("missing reference error = nil")
	}
}

type fakeManagerClient struct {
	definitions []ToolDefinition
	closed      bool
}

func (c *fakeManagerClient) Connect(context.Context) error { return nil }

func (c *fakeManagerClient) ListTools(context.Context) ([]ToolDefinition, error) {
	return c.definitions, nil
}

func (c *fakeManagerClient) CallTool(context.Context, string, json.RawMessage) (ToolResult, error) {
	return ToolResult{Content: []json.RawMessage{json.RawMessage(`{"type":"text","text":"hello"}`)}}, nil
}

func (c *fakeManagerClient) Close(context.Context) error {
	c.closed = true
	return nil
}
