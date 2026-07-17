// Package sandbox 提供按 Agent 和会话隔离的代码执行抽象。
package sandbox

import (
	"context"
	"time"
)

// ExecResult 保存一次沙箱命令的可观察结果。
type ExecResult struct {
	Output    string `json:"output"`
	ExitCode  int    `json:"exit_code"`
	Truncated bool   `json:"truncated"`
}

// FileEntry 保存目录中的一个直接子项。
type FileEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Size int64  `json:"size,omitempty"`
	Mode string `json:"mode,omitempty"`
}

// SkillMount 描述允许当前 Agent 只读访问的 Skill 目录。
type SkillMount struct {
	Name     string
	HostPath string
}

// SandboxExecutor 抽象一个会话专属的安全执行环境。
type SandboxExecutor interface {
	Exec(ctx context.Context, command string, timeout time.Duration) (ExecResult, error)
	ReadFile(ctx context.Context, path string) ([]byte, error)
	WriteFile(ctx context.Context, path string, content []byte) error
	ListDir(ctx context.Context, path string) ([]FileEntry, error)
	Close(ctx context.Context) error
}

// ExecutorPool 惰性创建并复用每个 Agent/session 的执行器。
type ExecutorPool interface {
	Get(ctx context.Context, agentID, sessionKey string, skills []SkillMount) (SandboxExecutor, error)
	Close(ctx context.Context) error
}
