package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lucky798213/luckyclaw/internal/provider"
	"github.com/lucky798213/luckyclaw/internal/sandbox"
	"github.com/lucky798213/luckyclaw/internal/skills"
)

const (
	ExecToolName      = "exec"
	ReadFileToolName  = "read_file"
	WriteFileToolName = "write_file"
	ListDirToolName   = "list_dir"
)

var sandboxToolNames = map[string]struct{}{
	ExecToolName: {}, ReadFileToolName: {}, WriteFileToolName: {}, ListDirToolName: {},
}

type sandboxToolRuntime struct {
	pool       sandbox.ExecutorPool
	mounts     []sandbox.SkillMount
	maxTimeout time.Duration
}

type execTool struct{ runtime *sandboxToolRuntime }
type readFileTool struct{ runtime *sandboxToolRuntime }
type writeFileTool struct{ runtime *sandboxToolRuntime }
type listDirTool struct{ runtime *sandboxToolRuntime }

type execArguments struct {
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type filePathArguments struct {
	Path string `json:"path"`
}

type writeFileArguments struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// NewSandboxTools 创建共享同一个会话执行器的四个 Docker 工具。
func NewSandboxTools(pool sandbox.ExecutorPool, selectedSkills []skills.Skill, maxTimeout time.Duration) ([]Tool, error) {
	if pool == nil {
		return nil, fmt.Errorf("sandbox executor pool cannot be nil")
	}
	if maxTimeout <= 0 {
		return nil, fmt.Errorf("sandbox exec timeout must be greater than zero")
	}
	mounts := make([]sandbox.SkillMount, 0, len(selectedSkills))
	for _, skill := range selectedSkills {
		mounts = append(mounts, sandbox.SkillMount{Name: skill.Name, HostPath: skill.Root})
	}
	runtime := &sandboxToolRuntime{pool: pool, mounts: mounts, maxTimeout: maxTimeout}
	return []Tool{
		&execTool{runtime: runtime},
		&readFileTool{runtime: runtime},
		&writeFileTool{runtime: runtime},
		&listDirTool{runtime: runtime},
	}, nil
}

// IsStatefulToolName 判断工具是否依赖持久会话作用域。
func IsStatefulToolName(name string) bool {
	if name == MemorySearchToolName {
		return true
	}
	_, exists := sandboxToolNames[name]
	return exists
}

func (t *execTool) Definition() provider.Tool {
	return functionDefinition(ExecToolName, "在当前会话隔离的 Docker Workspace 中执行 Shell 命令。", map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"command":         map[string]any{"type": "string", "description": "要在 /workspace 中执行的命令"},
			"timeout_seconds": map[string]any{"type": "integer", "description": "可选超时，不能超过系统上限"},
		},
		"required": []string{"command"},
	})
}

func (t *execTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var arguments execArguments
	if err := decodeArguments(raw, &arguments); err != nil {
		return "", err
	}
	if strings.TrimSpace(arguments.Command) == "" {
		return "", fmt.Errorf("command is required")
	}
	timeout := t.runtime.maxTimeout
	if arguments.TimeoutSeconds < 0 {
		return "", fmt.Errorf("timeout_seconds cannot be negative")
	}
	if arguments.TimeoutSeconds > 0 {
		timeout = time.Duration(arguments.TimeoutSeconds) * time.Second
		if timeout > t.runtime.maxTimeout {
			return "", fmt.Errorf("timeout_seconds cannot exceed %d", int(t.runtime.maxTimeout/time.Second))
		}
	}
	executor, err := t.runtime.executor(ctx)
	if err != nil {
		return "", err
	}
	result, runErr := executor.Exec(ctx, arguments.Command, timeout)
	payload, encodeErr := json.Marshal(result)
	if encodeErr != nil {
		return "", fmt.Errorf("encode exec result: %w", encodeErr)
	}
	if runErr != nil {
		return "", fmt.Errorf("%v; result=%s", runErr, payload)
	}
	return string(payload), nil
}

func (t *readFileTool) Definition() provider.Tool {
	return fileToolDefinition(ReadFileToolName, "读取当前会话 Docker Workspace 中的 UTF-8 文本文件。")
}

func (t *readFileTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	arguments, err := decodeFilePathArguments(raw)
	if err != nil {
		return "", err
	}
	executor, err := t.runtime.executor(ctx)
	if err != nil {
		return "", err
	}
	content, err := executor.ReadFile(ctx, arguments.Path)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(content) {
		return "", fmt.Errorf("read_file only supports UTF-8 text files")
	}
	payload, err := json.Marshal(map[string]any{"path": arguments.Path, "content": string(content)})
	if err != nil {
		return "", fmt.Errorf("encode read_file result: %w", err)
	}
	return string(payload), nil
}

func (t *writeFileTool) Definition() provider.Tool {
	return functionDefinition(WriteFileToolName, "在当前会话 Docker Workspace 中原子写入 UTF-8 文本文件。", map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"path":    map[string]any{"type": "string", "description": "相对于 /workspace 的文件路径"},
			"content": map[string]any{"type": "string", "description": "UTF-8 文本内容"},
		},
		"required": []string{"path", "content"},
	})
}

func (t *writeFileTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var arguments writeFileArguments
	if err := decodeArguments(raw, &arguments); err != nil {
		return "", err
	}
	if strings.TrimSpace(arguments.Path) == "" {
		return "", fmt.Errorf("path is required")
	}
	executor, err := t.runtime.executor(ctx)
	if err != nil {
		return "", err
	}
	if err := executor.WriteFile(ctx, arguments.Path, []byte(arguments.Content)); err != nil {
		return "", err
	}
	payload, err := json.Marshal(map[string]any{"path": arguments.Path, "bytes": len([]byte(arguments.Content))})
	if err != nil {
		return "", fmt.Errorf("encode write_file result: %w", err)
	}
	return string(payload), nil
}

func (t *listDirTool) Definition() provider.Tool {
	return fileToolDefinition(ListDirToolName, "列出当前会话 Docker Workspace 中目录的直接子项。")
}

func (t *listDirTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	arguments, err := decodeFilePathArguments(raw)
	if err != nil {
		return "", err
	}
	executor, err := t.runtime.executor(ctx)
	if err != nil {
		return "", err
	}
	entries, err := executor.ListDir(ctx, arguments.Path)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(map[string]any{"path": arguments.Path, "entries": entries})
	if err != nil {
		return "", fmt.Errorf("encode list_dir result: %w", err)
	}
	return string(payload), nil
}

func (r *sandboxToolRuntime) executor(ctx context.Context) (sandbox.SandboxExecutor, error) {
	scope, exists := ctx.Value(sessionScopeKey{}).(sessionScope)
	if !exists || scope.agentID == "" || scope.sessionKey == "" {
		return nil, fmt.Errorf("sandbox tools are only available in a stateful session")
	}
	return r.pool.Get(ctx, scope.agentID, scope.sessionKey, r.mounts)
}

func decodeFilePathArguments(raw json.RawMessage) (filePathArguments, error) {
	var arguments filePathArguments
	if err := decodeArguments(raw, &arguments); err != nil {
		return arguments, err
	}
	if strings.TrimSpace(arguments.Path) == "" {
		return arguments, fmt.Errorf("path is required")
	}
	return arguments, nil
}

func fileToolDefinition(name, description string) provider.Tool {
	return functionDefinition(name, description, map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "相对于 /workspace 的路径；列根目录时使用 ."},
		},
		"required": []string{"path"},
	})
}
