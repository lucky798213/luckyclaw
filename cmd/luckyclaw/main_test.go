package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/lucky798213/luckyclaw/internal/bus"
	"github.com/lucky798213/luckyclaw/internal/config"
	"github.com/lucky798213/luckyclaw/internal/provider"
)

type capturingProvider struct {
	mu    sync.Mutex
	tools []provider.Tool
}

func (p *capturingProvider) Chat(_ context.Context, _ []provider.Message, tools []provider.Tool, _ string, _ int, _ float64) (*provider.Message, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tools = append([]provider.Tool(nil), tools...)
	return &provider.Message{Role: "assistant", Content: "reply"}, nil
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
	agents, err := buildAgents(map[string]config.AgentConfig{
		"lucky": {
			Name:               "LuckyClaw",
			SoulPath:           soulPath,
			DefaultModel:       "test/chat",
			Models:             []string{"test/chat"},
			MaxToolIterations:  4,
			ToolTimeoutSeconds: 2,
		},
	}, providers, nil)
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
	if !reflect.DeepEqual(names, []string{"calculator", "current_time", "http_fetch"}) {
		t.Fatalf("tools = %v", names)
	}
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
