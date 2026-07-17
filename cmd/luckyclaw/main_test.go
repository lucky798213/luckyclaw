package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lucky798213/luckyclaw/internal/bus"
	"github.com/lucky798213/luckyclaw/internal/config"
	"github.com/lucky798213/luckyclaw/internal/mcp"
	"github.com/lucky798213/luckyclaw/internal/provider"
	sandboxruntime "github.com/lucky798213/luckyclaw/internal/sandbox"
	"github.com/lucky798213/luckyclaw/internal/session"
	"github.com/lucky798213/luckyclaw/internal/skills"
)

type capturingProvider struct {
	mu    sync.Mutex
	tools []provider.Tool
}

type capturingStream struct {
	message *provider.Message
	done    bool
}

func (s *capturingStream) Next() (provider.StreamChunk, error) {
	if s.done {
		return provider.StreamChunk{}, io.EOF
	}
	s.done = true
	return provider.StreamChunk{Done: true, Message: s.message}, nil
}

func (s *capturingStream) Close() error { return nil }

func (p *capturingProvider) Chat(_ context.Context, _ []provider.Message, tools []provider.Tool, _ string, _ int, _ float64) (*provider.Message, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tools = append([]provider.Tool(nil), tools...)
	return &provider.Message{Role: "assistant", Content: "reply"}, nil
}

func (p *capturingProvider) ChatStream(ctx context.Context, messages []provider.Message, tools []provider.Tool, model string, maxTokens int, temperature float64) (provider.Stream, error) {
	message, err := p.Chat(ctx, messages, tools, model, maxTokens, temperature)
	if err != nil {
		return nil, err
	}
	return &capturingStream{message: message}, nil
}

func TestWaitForShutdownCancelsAndWaitsAfterTerminalEOF(t *testing.T) {
	runtimeCtx, runtimeCancel := context.WithCancel(context.Background())
	terminalDone := make(chan struct{})
	gatewayDone := make(chan struct{})
	returned := make(chan struct{})
	go func() {
		waitForShutdown(context.Background(), terminalDone, gatewayDone, runtimeCancel)
		close(returned)
	}()

	close(terminalDone)
	waitForDone(t, runtimeCtx.Done(), "运行上下文没有被取消")
	assertNotDone(t, returned, "Gateway 结束前 waitForShutdown 已经返回")
	close(gatewayDone)
	waitForDone(t, returned, "Gateway 结束后 waitForShutdown 没有返回")
}

func TestWaitForShutdownCancelsAndWaitsAfterSignal(t *testing.T) {
	signalCtx, stopSignal := context.WithCancel(context.Background())
	runtimeCtx, runtimeCancel := context.WithCancel(context.Background())
	terminalDone := make(chan struct{})
	gatewayDone := make(chan struct{})
	returned := make(chan struct{})
	go func() {
		waitForShutdown(signalCtx, terminalDone, gatewayDone, runtimeCancel)
		close(returned)
	}()

	stopSignal()
	waitForDone(t, runtimeCtx.Done(), "运行上下文没有被取消")
	assertNotDone(t, returned, "Gateway 结束前 waitForShutdown 已经返回")
	close(gatewayDone)
	waitForDone(t, returned, "Gateway 结束后 waitForShutdown 没有返回")
}

func TestWaitForShutdownCancelsWhenGatewayStopsUnexpectedly(t *testing.T) {
	runtimeCtx, runtimeCancel := context.WithCancel(context.Background())
	terminalDone := make(chan struct{})
	gatewayDone := make(chan struct{})
	returned := make(chan struct{})
	go func() {
		waitForShutdown(context.Background(), terminalDone, gatewayDone, runtimeCancel)
		close(returned)
	}()

	close(gatewayDone)
	waitForDone(t, runtimeCtx.Done(), "Gateway 退出后运行上下文没有被取消")
	waitForDone(t, returned, "Gateway 退出后 waitForShutdown 没有返回")
}

func TestBuildAgentsWiresDefaultTools(t *testing.T) {
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	if err := os.WriteFile(soulPath, []byte("测试助手"), 0o600); err != nil {
		t.Fatal(err)
	}
	captured := &capturingProvider{}
	providers := provider.NewManager()
	if err := providers.Register("test", captured, []string{"chat"}); err != nil {
		t.Fatal(err)
	}
	store, err := session.OpenSQLite(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	agents, err := buildAgents(map[string]config.AgentConfig{
		"lucky": {
			Name:               "LuckyClaw",
			SoulPath:           soulPath,
			DefaultModel:       "test/chat",
			Models:             []string{"test/chat"},
			MaxToolIterations:  4,
			ToolTimeoutSeconds: 2,
		},
	}, providers, store, mustEmptySkillCatalog(t), mustEmptyMCPManager(t), nil, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	reply := agents["lucky"].HandleMessage(context.Background(), bus.InboundMessage{
		Channel:   "terminal",
		AccountID: "local",
		ChatID:    "default",
		Text:      "hello",
	})
	if reply != "reply" {
		t.Fatalf("reply = %q", reply)
	}
	captured.mu.Lock()
	defer captured.mu.Unlock()
	var names []string
	for _, definition := range captured.tools {
		names = append(names, definition.Function.Name)
	}
	if !reflect.DeepEqual(names, []string{"calculator", "current_time", "http_fetch", "memory_search"}) {
		t.Fatalf("tools = %v", names)
	}
}

func mustEmptyMCPManager(t *testing.T) *mcp.Manager {
	t.Helper()
	manager, err := mcp.NewManager(context.Background(), config.MCPConfig{
		RequestTimeoutSeconds: 30,
		MaxResultBytes:        1 << 20,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func mustEmptySkillCatalog(t *testing.T) *skills.Catalog {
	t.Helper()
	catalog, err := skills.Discover(nil)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func waitForDone(t *testing.T, done <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal(failure)
	}
}

func assertNotDone(t *testing.T, done <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-done:
		t.Fatal(failure)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestRuntimeLoadsSkillCallsMCPAndExecutesInDocker(t *testing.T) {
	if os.Getenv("LUCKYCLAW_DOCKER_INTEGRATION") != "1" {
		t.Skip("设置 LUCKYCLAW_DOCKER_INTEGRATION=1 后运行真实运行时验收")
	}
	image := os.Getenv("LUCKYCLAW_SANDBOX_TEST_IMAGE")
	if image == "" {
		image = "luckyclaw-sandbox:test"
	}
	skillRoot := t.TempDir()
	skillDir := filepath.Join(skillRoot, "code-runner")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := "---\nname: code-runner\ndescription: run code safely\n---\n\nUse exec and print skill-loaded."
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := skills.Discover([]string{skillRoot})
	if err != nil {
		t.Fatal(err)
	}
	mcpManager, err := mcp.NewManager(context.Background(), config.MCPConfig{
		RequestTimeoutSeconds: 2,
		MaxResultBytes:        1 << 20,
		Servers: map[string]config.MCPServerConfig{
			"echo": {
				Transport: "stdio",
				Command:   os.Args[0],
				Args:      []string{"-test.run=TestRuntimeMCPHelperProcess"},
				Env:       map[string]string{"GO_WANT_RUNTIME_MCP_HELPER": "1"},
			},
		},
	}, []string{"echo"})
	if err != nil {
		t.Fatal(err)
	}
	defer mcpManager.Close(context.Background())
	pool, err := sandboxruntime.NewDockerPool(sandboxruntime.DockerPolicy{
		Image: image, CPUs: 1, MemoryMB: 512, PIDsLimit: 128, TmpfsMB: 64,
		MaxOutputBytes: 1 << 20, MaxFileBytes: 1 << 20,
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close(context.Background())
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	if err := os.WriteFile(soulPath, []byte("test agent"), 0o600); err != nil {
		t.Fatal(err)
	}
	prov := &runtimeAcceptanceProvider{}
	providers := provider.NewManager()
	if err := providers.Register("test", prov, []string{"chat"}); err != nil {
		t.Fatal(err)
	}
	store, err := session.OpenSQLite(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	agents, err := buildAgents(map[string]config.AgentConfig{
		"lucky": {
			Name: "LuckyClaw", SoulPath: soulPath, DefaultModel: "test/chat", Models: []string{"test/chat"},
			Skills: []string{"code-runner"}, MCPServers: []string{"echo"}, SandboxEnabled: true,
			MaxToolIterations: 8, ToolTimeoutSeconds: 5,
		},
	}, providers, store, catalog, mcpManager, pool, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	reply := agents["lucky"].HandleMessage(context.Background(), bus.InboundMessage{
		Channel: "terminal", AccountID: "local", ChatID: "acceptance", Text: "run acceptance",
	})
	if reply != "runtime-ok" {
		t.Fatalf("reply = %q", reply)
	}
	if strings.Join(prov.toolResults, "\n") == "" ||
		!strings.Contains(prov.toolResults[0], "skill-loaded") ||
		!strings.Contains(prov.toolResults[1], "mcp-ok") ||
		!strings.Contains(prov.toolResults[2], "sandbox-ok") {
		t.Fatalf("tool results = %#v", prov.toolResults)
	}
}

type runtimeAcceptanceProvider struct {
	mu          sync.Mutex
	step        int
	toolResults []string
}

func (p *runtimeAcceptanceProvider) Chat(_ context.Context, messages []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Message, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(messages) > 0 && messages[len(messages)-1].Role == "tool" {
		p.toolResults = append(p.toolResults, messages[len(messages)-1].Content)
	}
	p.step++
	switch p.step {
	case 1:
		return runtimeToolCall("load", "load_skill", `{"name":"code-runner"}`), nil
	case 2:
		return runtimeToolCall("mcp", "mcp_echo_echo", `{"value":"mcp-ok"}`), nil
	case 3:
		return runtimeToolCall("docker", "exec", `{"command":"printf sandbox-ok"}`), nil
	default:
		return &provider.Message{Role: "assistant", Content: "runtime-ok"}, nil
	}
}

func (p *runtimeAcceptanceProvider) ChatStream(ctx context.Context, messages []provider.Message, tools []provider.Tool, model string, maxTokens int, temperature float64) (provider.Stream, error) {
	message, err := p.Chat(ctx, messages, tools, model, maxTokens, temperature)
	if err != nil {
		return nil, err
	}
	return &capturingStream{message: message}, nil
}

func runtimeToolCall(id, name, arguments string) *provider.Message {
	return &provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{
		ID: id, Type: "function", Function: provider.FunctionCall{Name: name, Arguments: arguments},
	}}}
}

func TestRuntimeMCPHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_RUNTIME_MCP_HELPER") != "1" {
		return
	}
	initialized := false
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		switch request.Method {
		case "initialize":
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{
				"protocolVersion": "2025-11-25", "capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]string{"name": "runtime-test", "version": "1"},
			}})
		case "notifications/initialized":
			initialized = true
		case "tools/list":
			if !initialized {
				os.Exit(3)
			}
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"tools": []any{map[string]any{
				"name": "echo", "description": "echo", "inputSchema": map[string]any{"type": "object"},
			}}}})
		case "tools/call":
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": fmt.Sprintf("mcp-ok:%s", request.Params)}},
			}})
		}
	}
	os.Exit(0)
}
