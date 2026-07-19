package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode"

	"github.com/lucky798213/luckyclaw/internal/config"
	"github.com/lucky798213/luckyclaw/internal/provider"
	"github.com/lucky798213/luckyclaw/internal/tools"
)

const maxProviderToolNameBytes = 64

// Manager 管理本次运行所需的 MCP 服务及工具路由。
type Manager struct {
	servers map[string]*managedServer
}

type managedServer struct {
	client Client
	tools  []ToolDefinition
}

type clientFactory func(config.MCPServerConfig, StdioOptions) (Client, error)

// NewManager 只启动 Agent 白名单实际引用的 MCP 服务。
func NewManager(ctx context.Context, cfg config.MCPConfig, referenced []string) (*Manager, error) {
	return newManager(ctx, cfg, referenced, func(_ config.MCPServerConfig, options StdioOptions) (Client, error) {
		return NewStdioClient(options)
	})
}

func newManager(ctx context.Context, cfg config.MCPConfig, referenced []string, factory clientFactory) (*Manager, error) {
	manager := &Manager{servers: make(map[string]*managedServer)}
	// 阶段一：仅处理 Agent 白名单实际引用的服务，并固定启动顺序方便定位错误。
	names := append([]string(nil), referenced...)
	sort.Strings(names)
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		serverCfg, exists := cfg.Servers[name]
		if !exists {
			manager.Close(context.Background())
			return nil, fmt.Errorf("unknown MCP server %q", name)
		}
		// 阶段二：在宿主侧解析环境变量引用，缺失密钥时快速失败且不经过 Shell。
		environment, err := expandEnvironment(serverCfg.Env)
		if err != nil {
			manager.Close(context.Background())
			return nil, fmt.Errorf("MCP server %q: %w", name, err)
		}
		client, err := factory(serverCfg, StdioOptions{
			Command:        serverCfg.Command,
			Args:           serverCfg.Args,
			Env:            environment,
			RequestTimeout: cfg.RequestTimeout(),
			MaxResultBytes: cfg.MaxResultBytes,
		})
		if err != nil {
			manager.Close(context.Background())
			return nil, fmt.Errorf("create MCP server %q: %w", name, err)
		}
		// 阶段三：完成 MCP 初始化握手后拉取工具目录，任何失败都会关闭此前已启动的进程。
		if err := client.Connect(ctx); err != nil {
			_ = client.Close(context.Background())
			manager.Close(context.Background())
			return nil, fmt.Errorf("connect MCP server %q: %w", name, err)
		}
		definitions, err := client.ListTools(ctx)
		if err != nil {
			_ = client.Close(context.Background())
			manager.Close(context.Background())
			return nil, fmt.Errorf("list MCP server %q tools: %w", name, err)
		}
		// 阶段四：校验 schema 和重名后才发布服务，避免把不完整工具暴露给模型。
		if err := validateToolDefinitions(name, definitions); err != nil {
			_ = client.Close(context.Background())
			manager.Close(context.Background())
			return nil, err
		}
		manager.servers[name] = &managedServer{client: client, tools: definitions}
	}
	return manager, nil
}

// ToolsFor 把指定服务的 MCP 工具转换成统一 Tool Registry 工具。
func (m *Manager) ToolsFor(serverNames []string) ([]tools.Tool, error) {
	if len(serverNames) == 0 {
		return nil, nil
	}
	result := make([]tools.Tool, 0)
	exposedNames := make(map[string]string)
	for _, serverName := range serverNames {
		server := m.servers[serverName]
		if server == nil {
			return nil, fmt.Errorf("MCP server %q is not connected", serverName)
		}
		for _, definition := range server.tools {
			exposed := exposedToolName(serverName, definition.Name)
			origin := serverName + "/" + definition.Name
			if previous, duplicate := exposedNames[exposed]; duplicate {
				return nil, fmt.Errorf("MCP tools %q and %q map to duplicate name %q", previous, origin, exposed)
			}
			exposedNames[exposed] = origin
			result = append(result, &managerTool{
				client:       server.client,
				originalName: definition.Name,
				definition: provider.Tool{Type: "function", Function: provider.ToolFunction{
					Name:        exposed,
					Description: definition.Description,
					Parameters:  definition.InputSchema,
				}},
			})
		}
	}
	return result, nil
}

// Close 关闭全部 MCP 子进程并聚合错误。
func (m *Manager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	names := make([]string, 0, len(m.servers))
	for name := range m.servers {
		names = append(names, name)
	}
	sort.Strings(names)
	var closeErrors []error
	for _, name := range names {
		if err := m.servers[name].client.Close(ctx); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close MCP server %q: %w", name, err))
		}
	}
	return errors.Join(closeErrors...)
}

type managerTool struct {
	client       Client
	originalName string
	definition   provider.Tool
}

func (t *managerTool) Definition() provider.Tool { return t.definition }

func (t *managerTool) Execute(ctx context.Context, arguments json.RawMessage) (string, error) {
	result, err := t.client.CallTool(ctx, t.originalName, arguments)
	if err != nil {
		return "", err
	}
	text := formatToolResult(result)
	if result.IsError {
		return "", fmt.Errorf("MCP tool reported an error: %s", text)
	}
	return text, nil
}

func formatToolResult(result ToolResult) string {
	texts := make([]string, 0, len(result.Content))
	allText := len(result.StructuredContent) == 0
	for _, raw := range result.Content {
		var content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &content) != nil || content.Type != "text" {
			allText = false
			break
		}
		texts = append(texts, content.Text)
	}
	if allText {
		return strings.Join(texts, "\n")
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return "MCP tool returned an unreadable result"
	}
	return string(payload)
}

func validateToolDefinitions(serverName string, definitions []ToolDefinition) error {
	seen := make(map[string]struct{}, len(definitions))
	for index, definition := range definitions {
		if strings.TrimSpace(definition.Name) == "" {
			return fmt.Errorf("MCP server %q tool %d has an empty name", serverName, index)
		}
		if definition.InputSchema == nil {
			return fmt.Errorf("MCP server %q tool %q has no inputSchema", serverName, definition.Name)
		}
		if _, duplicate := seen[definition.Name]; duplicate {
			return fmt.Errorf("MCP server %q tool %q is duplicated", serverName, definition.Name)
		}
		seen[definition.Name] = struct{}{}
	}
	return nil
}

func exposedToolName(serverName, toolName string) string {
	raw := "mcp_" + sanitizeToolName(serverName) + "_" + sanitizeToolName(toolName)
	if len(raw) <= maxProviderToolNameBytes {
		return raw
	}
	digest := sha256.Sum256([]byte(raw))
	suffix := hex.EncodeToString(digest[:8])
	return raw[:maxProviderToolNameBytes-len(suffix)-1] + "_" + suffix
}

func sanitizeToolName(value string) string {
	var result strings.Builder
	for _, current := range value {
		if current <= unicode.MaxASCII && (unicode.IsLetter(current) || unicode.IsDigit(current) || current == '_' || current == '-') {
			result.WriteRune(current)
		} else {
			result.WriteByte('_')
		}
	}
	return result.String()
}

func expandEnvironment(input map[string]string) (map[string]string, error) {
	result := make(map[string]string, len(input))
	for key, value := range input {
		if strings.HasPrefix(value, "$") {
			name := strings.TrimPrefix(value, "$")
			if name == "" || strings.Contains(name, "$") {
				return nil, fmt.Errorf("environment %q has an invalid reference", key)
			}
			resolved, exists := os.LookupEnv(name)
			if !exists || resolved == "" {
				return nil, fmt.Errorf("environment variable %q for %q is not set or empty", name, key)
			}
			result[key] = resolved
			continue
		}
		result[key] = value
	}
	return result, nil
}

var _ tools.Tool = (*managerTool)(nil)
