package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lucky798213/luckyclaw/internal/bus"
	"github.com/lucky798213/luckyclaw/internal/sandbox"
)

func TestSandboxToolsRouteThroughTrustedSessionScope(t *testing.T) {
	executor := &fakeToolSandboxExecutor{}
	pool := &fakeToolSandboxPool{executor: executor}
	registered, err := NewSandboxTools(pool, nil, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]Tool)
	for _, tool := range registered {
		byName[tool.Definition().Function.Name] = tool
	}
	ctx := WithSessionScope(context.Background(), "agent-a", bus.ConversationAddress{}, "session-a")
	if result, err := byName[WriteFileToolName].Execute(ctx, json.RawMessage(`{"path":"main.go","content":"package main"}`)); err != nil || !strings.Contains(result, `"bytes":12`) {
		t.Fatalf("write_file = %q, %v", result, err)
	}
	executor.content = []byte("package main")
	if result, err := byName[ReadFileToolName].Execute(ctx, json.RawMessage(`{"path":"main.go"}`)); err != nil || !strings.Contains(result, "package main") {
		t.Fatalf("read_file = %q, %v", result, err)
	}
	if result, err := byName[ExecToolName].Execute(ctx, json.RawMessage(`{"command":"go test ./...","timeout_seconds":2}`)); err != nil || !strings.Contains(result, `"exit_code":0`) {
		t.Fatalf("exec = %q, %v", result, err)
	}
	if result, err := byName[ListDirToolName].Execute(ctx, json.RawMessage(`{"path":"."}`)); err != nil || !strings.Contains(result, "main.go") {
		t.Fatalf("list_dir = %q, %v", result, err)
	}
	if pool.agentID != "agent-a" || pool.sessionKey != "session-a" {
		t.Fatalf("pool scope = %q/%q", pool.agentID, pool.sessionKey)
	}
	if _, err := byName[ExecToolName].Execute(context.Background(), json.RawMessage(`{"command":"true"}`)); err == nil {
		t.Fatal("missing session scope error = nil")
	}
}

func TestIsStatefulToolName(t *testing.T) {
	for _, name := range []string{MemorySearchToolName, ExecToolName, ReadFileToolName, WriteFileToolName, ListDirToolName} {
		if !IsStatefulToolName(name) {
			t.Fatalf("%s should be stateful", name)
		}
	}
	if IsStatefulToolName(LoadSkillToolName) {
		t.Fatal("load_skill should remain available to stateless completions")
	}
}

type fakeToolSandboxPool struct {
	executor   sandbox.SandboxExecutor
	agentID    string
	sessionKey string
}

func (p *fakeToolSandboxPool) Get(_ context.Context, agentID, sessionKey string, _ []sandbox.SkillMount) (sandbox.SandboxExecutor, error) {
	p.agentID, p.sessionKey = agentID, sessionKey
	return p.executor, nil
}
func (p *fakeToolSandboxPool) Close(context.Context) error { return nil }

type fakeToolSandboxExecutor struct{ content []byte }

func (e *fakeToolSandboxExecutor) Exec(context.Context, string, time.Duration) (sandbox.ExecResult, error) {
	return sandbox.ExecResult{Output: "ok", ExitCode: 0}, nil
}
func (e *fakeToolSandboxExecutor) ReadFile(context.Context, string) ([]byte, error) {
	return e.content, nil
}
func (e *fakeToolSandboxExecutor) WriteFile(_ context.Context, _ string, content []byte) error {
	e.content = append([]byte(nil), content...)
	return nil
}
func (e *fakeToolSandboxExecutor) ListDir(context.Context, string) ([]sandbox.FileEntry, error) {
	return []sandbox.FileEntry{{Name: "main.go", Type: "file", Size: int64(len(e.content))}}, nil
}
func (e *fakeToolSandboxExecutor) Close(context.Context) error { return nil }
