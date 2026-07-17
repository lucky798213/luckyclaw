package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lucky798213/luckyclaw/internal/provider"
	"github.com/lucky798213/luckyclaw/internal/session"
	toolruntime "github.com/lucky798213/luckyclaw/internal/tools"
)

type scriptedProviderStep struct {
	message provider.Message
	err     error
}

type scriptedProviderRequest struct {
	messages []provider.Message
	tools    []provider.Tool
}

type scriptedProvider struct {
	mu       sync.Mutex
	steps    []scriptedProviderStep
	requests []scriptedProviderRequest
}

func (p *scriptedProvider) Chat(_ context.Context, messages []provider.Message, tools []provider.Tool, _ string, _ int, _ float64) (*provider.Message, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, scriptedProviderRequest{
		messages: append([]provider.Message(nil), messages...),
		tools:    append([]provider.Tool(nil), tools...),
	})
	index := len(p.requests) - 1
	if index >= len(p.steps) {
		return nil, fmt.Errorf("unexpected provider call %d", index+1)
	}
	step := p.steps[index]
	if step.err != nil {
		return nil, step.err
	}
	message := step.message
	return &message, nil
}

func (p *scriptedProvider) ChatStream(ctx context.Context, messages []provider.Message, tools []provider.Tool, model string, maxTokens int, temperature float64) (provider.Stream, error) {
	message, err := p.Chat(ctx, messages, tools, model, maxTokens, temperature)
	if err != nil {
		return nil, err
	}
	return &singleMessageStream{message: message}, nil
}

func (p *scriptedProvider) Requests() []scriptedProviderRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]scriptedProviderRequest(nil), p.requests...)
}

type scriptedTool struct {
	name    string
	execute func(context.Context, json.RawMessage) (string, error)
}

func (t *scriptedTool) Definition() provider.Tool {
	return provider.Tool{
		Type: "function",
		Function: provider.ToolFunction{
			Name:       t.name,
			Parameters: map[string]any{"type": "object"},
		},
	}
}

func (t *scriptedTool) Execute(ctx context.Context, arguments json.RawMessage) (string, error) {
	return t.execute(ctx, arguments)
}

func newToolLoopAgent(
	t *testing.T,
	currentProvider provider.Provider,
	registry toolruntime.Registry,
	store session.Store,
	maxIterations int,
	toolTimeout time.Duration,
) *Agent {
	t.Helper()
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	if err := os.WriteFile(soulPath, []byte("你是测试助手。"), 0o600); err != nil {
		t.Fatal(err)
	}
	providers := newProviderManager(t, "test", []string{"chat"}, currentProvider)
	current, err := New(Options{
		ID:                "lucky",
		Name:              "LuckyClaw",
		DefaultModel:      "test/chat",
		Models:            []string{"test/chat"},
		SoulPath:          soulPath,
		SessionStore:      store,
		Tools:             registry,
		MaxToolIterations: maxIterations,
		ToolTimeout:       toolTimeout,
	}, providers)
	if err != nil {
		t.Fatal(err)
	}
	return current
}

func newScriptedRegistry(t *testing.T, tools ...toolruntime.Tool) toolruntime.Registry {
	t.Helper()
	registry, err := toolruntime.NewRegistry(tools...)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func toolCall(id, name, arguments string) provider.ToolCall {
	return provider.ToolCall{
		ID:   id,
		Type: "function",
		Function: provider.FunctionCall{
			Name:      name,
			Arguments: arguments,
		},
	}
}

func TestToolCallingLoopPersistsAndRestoresCompleteChain(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "sessions.db")
	store, err := session.OpenSQLite(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	firstProvider := &scriptedProvider{steps: []scriptedProviderStep{
		{message: provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{toolCall("call-calc", "calculator", `{"expression":"6*(7+1)"}`)}}},
		{message: provider.Message{Role: "assistant", Content: "结果是 48。"}},
	}}
	firstAgent := newToolLoopAgent(t, firstProvider, newDefaultToolRegistry(t), store, 0, 0)
	if got := firstAgent.HandleMessage(ctx, inbound("chat-tools", "帮我计算")); got != "结果是 48。" {
		t.Fatalf("reply = %q", got)
	}

	requests := firstProvider.Requests()
	if len(requests) != 2 {
		t.Fatalf("provider calls = %d", len(requests))
	}
	for index, request := range requests {
		if got := definitionNames(request.tools); !reflect.DeepEqual(got, []string{"calculator", "current_time", "http_fetch"}) {
			t.Fatalf("request %d tools = %v", index, got)
		}
	}
	secondMessages := requests[1].messages
	if len(secondMessages) != 4 {
		t.Fatalf("second request messages = %#v", secondMessages)
	}
	if secondMessages[1].Role != "user" || secondMessages[2].Role != "assistant" || len(secondMessages[2].ToolCalls) != 1 {
		t.Fatalf("second request tool chain = %#v", secondMessages)
	}
	if got := secondMessages[3]; got.Role != "tool" || got.ToolCallID != "call-calc" || got.Name != "calculator" || got.Content != "48" {
		t.Fatalf("tool result = %#v", got)
	}
	current, err := firstAgent.sessionsManager.CurrentSession(ctx, inbound("chat-tools", "").Address())
	if err != nil {
		t.Fatal(err)
	}
	if got := current.Messages(); len(got) != 4 || got[0].Role != "user" || got[1].Role != "assistant" || got[2].Role != "tool" || got[3].Content != "结果是 48。" {
		t.Fatalf("saved messages = %#v", got)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := session.OpenSQLite(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	secondProvider := &scriptedProvider{steps: []scriptedProviderStep{{message: provider.Message{Role: "assistant", Content: "历史已恢复。"}}}}
	secondAgent := newToolLoopAgent(t, secondProvider, newDefaultToolRegistry(t), reopened, 0, 0)
	if got := secondAgent.HandleMessage(ctx, inbound("chat-tools", "刚才算了什么？")); got != "历史已恢复。" {
		t.Fatalf("restart reply = %q", got)
	}
	restoredRequest := secondProvider.Requests()[0].messages
	if len(restoredRequest) != 6 || restoredRequest[2].Role != "assistant" || restoredRequest[3].Role != "tool" || restoredRequest[3].Content != "48" {
		t.Fatalf("restored request = %#v", restoredRequest)
	}
}

func TestToolCallingLoopExecutesMultipleCallsInOrder(t *testing.T) {
	var mu sync.Mutex
	var order []string
	makeTool := func(name string, fail bool) toolruntime.Tool {
		return &scriptedTool{name: name, execute: func(_ context.Context, raw json.RawMessage) (string, error) {
			mu.Lock()
			order = append(order, name+":"+string(raw))
			mu.Unlock()
			if fail {
				return "", errors.New("scripted failure")
			}
			return name + " result", nil
		}}
	}
	strictTool := &scriptedTool{name: "strict", execute: func(_ context.Context, raw json.RawMessage) (string, error) {
		mu.Lock()
		order = append(order, "strict:"+string(raw))
		mu.Unlock()
		var arguments map[string]any
		if err := json.Unmarshal(raw, &arguments); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
		return "strict result", nil
	}}
	registry := newScriptedRegistry(t, makeTool("alpha", false), makeTool("broken", true), strictTool)
	currentProvider := &scriptedProvider{steps: []scriptedProviderStep{
		{message: provider.Message{ToolCalls: []provider.ToolCall{
			toolCall("a", "alpha", `{"step":1}`),
			toolCall("b", "missing", `{}`),
			toolCall("c", "broken", `{"step":3}`),
			toolCall("d", "strict", `{"broken"`),
		}}},
		{message: provider.Message{Content: "已处理。"}},
	}}
	agent := newToolLoopAgent(t, currentProvider, registry, nil, 0, 0)
	if got := agent.HandleMessage(context.Background(), inbound("ordered", "执行")); got != "已处理。" {
		t.Fatalf("reply = %q", got)
	}
	mu.Lock()
	gotOrder := append([]string(nil), order...)
	mu.Unlock()
	if !reflect.DeepEqual(gotOrder, []string{`alpha:{"step":1}`, `broken:{"step":3}`, `strict:{"broken"`}) {
		t.Fatalf("execution order = %v", gotOrder)
	}
	secondRequest := currentProvider.Requests()[1].messages
	results := secondRequest[len(secondRequest)-4:]
	if results[0].Name != "alpha" || results[0].Content != "alpha result" ||
		results[1].Name != "missing" || !strings.Contains(results[1].Content, "unknown tool") ||
		results[2].Name != "broken" || !strings.Contains(results[2].Content, "scripted failure") ||
		results[3].Name != "strict" || !strings.Contains(results[3].Content, "invalid arguments") {
		t.Fatalf("ordered results = %#v", results)
	}
}

func TestHandleMessageStreamEmitsToolLifecycleInOrder(t *testing.T) {
	registry := newScriptedRegistry(t, &scriptedTool{name: "lookup", execute: func(_ context.Context, _ json.RawMessage) (string, error) {
		return "找到结果", nil
	}})
	currentProvider := &scriptedProvider{steps: []scriptedProviderStep{
		{message: provider.Message{ToolCalls: []provider.ToolCall{toolCall("call-lookup", "lookup", `{"query":"LuckyClaw"}`)}}},
		{message: provider.Message{Content: "这是最终回答。"}},
	}}
	currentAgent := newToolLoopAgent(t, currentProvider, registry, nil, 3, time.Second)

	events := collectEvents(currentAgent.HandleMessageStream(context.Background(), inbound("stream-tools", "查询")))
	if len(events) != 4 {
		t.Fatalf("events = %#v", events)
	}
	if events[0].Type != EventToolStart || events[0].Data.ToolCallID != "call-lookup" || events[0].Data.ToolName != "lookup" {
		t.Fatalf("tool_start = %#v", events[0])
	}
	if events[1].Type != EventToolResult || events[1].Data.Result != "找到结果" || events[1].Data.Success == nil || !*events[1].Data.Success {
		t.Fatalf("tool_result = %#v", events[1])
	}
	if events[2].Type != EventTokenDelta || events[2].Data.Delta != "这是最终回答。" {
		t.Fatalf("token_delta = %#v", events[2])
	}
	if events[3].Type != EventFinal || events[3].Data.Content != "这是最终回答。" {
		t.Fatalf("final = %#v", events[3])
	}
}

func TestHandleMessageStreamCancellationDiscardsWholeTurn(t *testing.T) {
	store, err := session.OpenSQLite(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	toolStopped := make(chan struct{})
	registry := newScriptedRegistry(t, &scriptedTool{name: "wait", execute: func(ctx context.Context, _ json.RawMessage) (string, error) {
		<-ctx.Done()
		close(toolStopped)
		return "", ctx.Err()
	}})
	currentProvider := &scriptedProvider{steps: []scriptedProviderStep{
		{message: provider.Message{ToolCalls: []provider.ToolCall{toolCall("call-wait", "wait", `{}`)}}},
	}}
	currentAgent := newToolLoopAgent(t, currentProvider, registry, store, 3, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	events := currentAgent.HandleMessageStream(ctx, inbound("cancel-tools", "等待"))

	first, ok := <-events
	if !ok || first.Type != EventToolStart {
		t.Fatalf("first event = %#v, open = %v", first, ok)
	}
	cancel()
	select {
	case <-toolStopped:
	case <-time.After(time.Second):
		t.Fatal("工具没有收到取消信号")
	}
	for event := range events {
		if event.Type == EventFinal || event.Type == EventError {
			t.Fatalf("取消后不应发送事件: %#v", event)
		}
	}
	current, err := currentAgent.sessionsManager.CurrentSession(context.Background(), inbound("cancel-tools", "").Address())
	if err != nil {
		t.Fatal(err)
	}
	if messages := current.Messages(); len(messages) != 0 {
		t.Fatalf("取消后保存了消息: %#v", messages)
	}
}

func collectEvents(events <-chan Event) []Event {
	var collected []Event
	for event := range events {
		collected = append(collected, event)
	}
	return collected
}

func TestToolCallingLoopConvertsTimeoutToToolResult(t *testing.T) {
	release := make(chan struct{})
	registry := newScriptedRegistry(t, &scriptedTool{name: "wait", execute: func(_ context.Context, _ json.RawMessage) (string, error) {
		<-release
		return "late", nil
	}})
	currentProvider := &scriptedProvider{steps: []scriptedProviderStep{
		{message: provider.Message{ToolCalls: []provider.ToolCall{toolCall("wait-1", "wait", `{}`)}}},
		{message: provider.Message{Content: "超时后仍然完成。"}},
	}}
	agent := newToolLoopAgent(t, currentProvider, registry, nil, 3, 10*time.Millisecond)
	started := time.Now()
	if got := agent.HandleMessage(context.Background(), inbound("timeout", "等待")); got != "超时后仍然完成。" {
		t.Fatalf("reply = %q", got)
	}
	close(release)
	if elapsed := time.Since(started); elapsed >= 100*time.Millisecond {
		t.Fatalf("hard timeout returned after %s", elapsed)
	}
	toolResult := currentProvider.Requests()[1].messages[3]
	if toolResult.Role != "tool" || !strings.Contains(toolResult.Content, "timed out") {
		t.Fatalf("timeout result = %#v", toolResult)
	}
}

func TestToolCallingLoopSynthesizesAfterEmptyModelResponse(t *testing.T) {
	registry := newScriptedRegistry(t)
	currentProvider := &scriptedProvider{steps: []scriptedProviderStep{
		{message: provider.Message{}},
		{message: provider.Message{Content: "空响应已归纳。"}},
	}}
	agent := newToolLoopAgent(t, currentProvider, registry, nil, 3, time.Second)
	if got := agent.HandleMessage(context.Background(), inbound("empty", "回答")); got != "空响应已归纳。" {
		t.Fatalf("reply = %q", got)
	}
	requests := currentProvider.Requests()
	if len(requests) != 2 || requests[1].tools != nil {
		t.Fatalf("requests = %#v", requests)
	}
	current, err := agent.sessionsManager.CurrentSession(context.Background(), inbound("empty", "").Address())
	if err != nil {
		t.Fatal(err)
	}
	messages := current.Messages()
	if len(messages) != 2 || messages[0].Role != "user" || messages[1].Content != "空响应已归纳。" {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestNormalizeToolCallsProducesUniqueNonEmptyIDs(t *testing.T) {
	calls := normalizeToolCalls([]provider.ToolCall{
		{ID: "call-1-2"},
		{ID: "call-1-2"},
		{ID: ""},
		{ID: "   "},
	}, 0)
	seen := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		if strings.TrimSpace(call.ID) == "" {
			t.Fatal("normalized ID is empty")
		}
		if _, duplicate := seen[call.ID]; duplicate {
			t.Fatalf("duplicate normalized ID %q", call.ID)
		}
		seen[call.ID] = struct{}{}
		if call.Type != "function" {
			t.Fatalf("normalized type = %q", call.Type)
		}
	}
}

func TestToolCallingLoopBlocksThirdEquivalentCall(t *testing.T) {
	var mu sync.Mutex
	executions := 0
	registry := newScriptedRegistry(t, &scriptedTool{name: "repeat", execute: func(_ context.Context, _ json.RawMessage) (string, error) {
		mu.Lock()
		executions++
		mu.Unlock()
		return "ok", nil
	}})
	currentProvider := &scriptedProvider{steps: []scriptedProviderStep{
		{message: provider.Message{ToolCalls: []provider.ToolCall{toolCall("r1", "repeat", `{"a":1,"b":2}`)}}},
		{message: provider.Message{ToolCalls: []provider.ToolCall{toolCall("r2", "repeat", `{"b":2,"a":1}`)}}},
		{message: provider.Message{ToolCalls: []provider.ToolCall{toolCall("r3", "repeat", ` { "a" : 1, "b" : 2 } `)}}},
		{message: provider.Message{Content: "已停止重复调用。"}},
	}}
	agent := newToolLoopAgent(t, currentProvider, registry, nil, 5, time.Second)
	if got := agent.HandleMessage(context.Background(), inbound("repeat", "重复")); got != "已停止重复调用。" {
		t.Fatalf("reply = %q", got)
	}
	mu.Lock()
	gotExecutions := executions
	mu.Unlock()
	if gotExecutions != 2 {
		t.Fatalf("tool executions = %d, want 2", gotExecutions)
	}
	requests := currentProvider.Requests()
	if len(requests) != 4 || requests[3].tools != nil {
		t.Fatalf("provider requests = %#v", requests)
	}
	lastToolResult := requests[3].messages[len(requests[3].messages)-2]
	if lastToolResult.ToolCallID != "r3" || !strings.Contains(lastToolResult.Content, "repeated tool call blocked") {
		t.Fatalf("repeat result = %#v", lastToolResult)
	}
}

func TestToolCallingLoopDisablesToolsAfterConsecutiveFailedRounds(t *testing.T) {
	registry := newScriptedRegistry(t, &scriptedTool{name: "fail", execute: func(_ context.Context, _ json.RawMessage) (string, error) {
		return "", errors.New("unavailable")
	}})
	currentProvider := &scriptedProvider{steps: []scriptedProviderStep{
		{message: provider.Message{ToolCalls: []provider.ToolCall{toolCall("f1", "fail", `{"try":1}`)}}},
		{message: provider.Message{ToolCalls: []provider.ToolCall{toolCall("f2", "fail", `{"try":2}`)}}},
		{message: provider.Message{ToolCalls: []provider.ToolCall{toolCall("f3", "fail", `{"try":3}`)}}},
		{message: provider.Message{Content: "来源不可用，以下内容未验证。"}},
	}}
	agent := newToolLoopAgent(t, currentProvider, registry, nil, 10, time.Second)
	if got := agent.HandleMessage(context.Background(), inbound("fail", "查询")); got != "来源不可用，以下内容未验证。" {
		t.Fatalf("reply = %q", got)
	}
	requests := currentProvider.Requests()
	if len(requests) != 4 || requests[3].tools != nil {
		t.Fatalf("provider calls = %#v", requests)
	}
	lastMessage := requests[3].messages[len(requests[3].messages)-1]
	if lastMessage.Role != "system" || !strings.Contains(lastMessage.Content, "连续三轮") {
		t.Fatalf("degrade nudge = %#v", lastMessage)
	}
}

func TestToolCallingLoopSuccessfulRoundResetsFailureCount(t *testing.T) {
	registry := newScriptedRegistry(t, &scriptedTool{name: "sometimes", execute: func(_ context.Context, raw json.RawMessage) (string, error) {
		if strings.Contains(string(raw), `"success":true`) {
			return "ok", nil
		}
		return "", errors.New("failed")
	}})
	steps := []scriptedProviderStep{
		{message: provider.Message{ToolCalls: []provider.ToolCall{toolCall("s1", "sometimes", `{"round":1}`)}}},
		{message: provider.Message{ToolCalls: []provider.ToolCall{toolCall("s2", "sometimes", `{"success":true}`)}}},
		{message: provider.Message{ToolCalls: []provider.ToolCall{toolCall("s3", "sometimes", `{"round":3}`)}}},
		{message: provider.Message{ToolCalls: []provider.ToolCall{toolCall("s4", "sometimes", `{"round":4}`)}}},
		{message: provider.Message{ToolCalls: []provider.ToolCall{toolCall("s5", "sometimes", `{"round":5}`)}}},
		{message: provider.Message{Content: "重置后又连续失败三轮。"}},
	}
	currentProvider := &scriptedProvider{steps: steps}
	agent := newToolLoopAgent(t, currentProvider, registry, nil, 10, time.Second)
	if got := agent.HandleMessage(context.Background(), inbound("reset-failure", "执行")); got != "重置后又连续失败三轮。" {
		t.Fatalf("reply = %q", got)
	}
	requests := currentProvider.Requests()
	if len(requests) != 6 || requests[4].tools == nil || requests[5].tools != nil {
		t.Fatalf("tool disable timing is wrong: %#v", requests)
	}
}

func TestToolCallingLoopForcesFinalDeliveryAtIterationLimit(t *testing.T) {
	registry := newScriptedRegistry(t, &scriptedTool{name: "work", execute: func(_ context.Context, _ json.RawMessage) (string, error) {
		return "partial", nil
	}})
	currentProvider := &scriptedProvider{steps: []scriptedProviderStep{
		{message: provider.Message{ToolCalls: []provider.ToolCall{toolCall("m1", "work", `{"step":1}`)}}},
		{message: provider.Message{ToolCalls: []provider.ToolCall{toolCall("m2", "work", `{"step":2}`)}}},
		{message: provider.Message{Content: "根据已有结果完成。"}},
	}}
	agent := newToolLoopAgent(t, currentProvider, registry, nil, 2, time.Second)
	if got := agent.HandleMessage(context.Background(), inbound("limit", "完成")); got != "根据已有结果完成。" {
		t.Fatalf("reply = %q", got)
	}
	requests := currentProvider.Requests()
	if len(requests) != 3 || requests[2].tools != nil {
		t.Fatalf("provider calls = %#v", requests)
	}
	if last := requests[2].messages[len(requests[2].messages)-1]; !strings.Contains(last.Content, "2 次工具迭代上限") {
		t.Fatalf("limit nudge = %#v", last)
	}
}

func TestToolCallingLoopPersistsFallbackForInvalidFinalSynthesis(t *testing.T) {
	tests := []struct {
		name string
		step scriptedProviderStep
	}{
		{name: "Provider 错误", step: scriptedProviderStep{err: errors.New("model unavailable")}},
		{name: "空文本", step: scriptedProviderStep{message: provider.Message{}}},
		{name: "仍然调用工具", step: scriptedProviderStep{message: provider.Message{ToolCalls: []provider.ToolCall{toolCall("ignored", "work", `{}`)}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := newScriptedRegistry(t, &scriptedTool{name: "work", execute: func(_ context.Context, _ json.RawMessage) (string, error) {
				return "partial", nil
			}})
			currentProvider := &scriptedProvider{steps: []scriptedProviderStep{
				{message: provider.Message{ToolCalls: []provider.ToolCall{toolCall("m1", "work", `{}`)}}},
				test.step,
			}}
			agent := newToolLoopAgent(t, currentProvider, registry, nil, 1, time.Second)
			chatID := "fallback-" + test.name
			reply := agent.HandleMessage(context.Background(), inbound(chatID, "完成"))
			if !strings.Contains(reply, "工具调用未能完成") || !strings.Contains(reply, "1 次工具迭代上限") {
				t.Fatalf("fallback reply = %q", reply)
			}
			current, err := agent.sessionsManager.CurrentSession(context.Background(), inbound(chatID, "").Address())
			if err != nil {
				t.Fatal(err)
			}
			messages := current.Messages()
			if len(messages) != 4 || messages[3].Role != "assistant" || messages[3].Content != reply || len(messages[3].ToolCalls) != 0 {
				t.Fatalf("fallback messages = %#v", messages)
			}
		})
	}
}

func definitionNames(definitions []provider.Tool) []string {
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		names = append(names, definition.Function.Name)
	}
	return names
}
