package agent

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lucky798213/luckyclaw/internal/provider"
	"github.com/lucky798213/luckyclaw/internal/session"
	toolruntime "github.com/lucky798213/luckyclaw/internal/tools"
)

func TestMemorySearchToolRetrievesEarlierSessionInToolLoop(t *testing.T) {
	ctx := context.Background()
	store, err := session.OpenSQLite(filepath.Join(t.TempDir(), "memory-tool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	address := inbound("chat-memory", "").Address()
	seedManager := session.NewManager("lucky", store)
	oldSession, err := seedManager.CurrentSession(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	if err := oldSession.Append(ctx, provider.Message{Role: "user", Content: "早期关键事实：项目代号是银鲤。"}); err != nil {
		t.Fatal(err)
	}
	if _, err := seedManager.NewSession(ctx, address); err != nil {
		t.Fatal(err)
	}
	memoryTool, err := toolruntime.NewMemorySearchTool(store, "lucky")
	if err != nil {
		t.Fatal(err)
	}
	registry := newScriptedRegistry(t, memoryTool)
	prov := &scriptedProvider{steps: []scriptedProviderStep{
		{message: provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{
			toolCall("call-memory", toolruntime.MemorySearchToolName, `{"query":"项目代号"}`),
		}}},
		{message: provider.Message{Role: "assistant", Content: "找到了，项目代号是银鲤。"}},
	}}
	a := newToolLoopAgent(t, prov, registry, store, 4, 0)
	if got := a.HandleMessage(ctx, inbound("chat-memory", "之前的项目代号是什么？")); got != "找到了，项目代号是银鲤。" {
		t.Fatalf("回答 = %q", got)
	}
	requests := prov.Requests()
	if len(requests) != 2 || !reflect.DeepEqual(definitionNames(requests[0].tools), []string{toolruntime.MemorySearchToolName}) {
		t.Fatalf("状态会话工具 = %+v", requests)
	}
	if got := requests[1].messages[len(requests[1].messages)-1]; got.Role != "tool" ||
		got.Name != toolruntime.MemorySearchToolName || !strings.Contains(got.Content, "银鲤") || !strings.Contains(got.Content, "不可信") {
		t.Fatalf("memory_search 结果 = %+v", got)
	}
}

func TestStatelessCompletionDoesNotExposeMemorySearch(t *testing.T) {
	store, err := session.OpenSQLite(filepath.Join(t.TempDir(), "stateless-memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	memoryTool, err := toolruntime.NewMemorySearchTool(store, "lucky")
	if err != nil {
		t.Fatal(err)
	}
	prov := &scriptedProvider{steps: []scriptedProviderStep{{
		message: provider.Message{Role: "assistant", Content: "无状态回答"},
	}}}
	a := newToolLoopAgent(t, prov, newScriptedRegistry(t, memoryTool), store, 4, 0)
	reply, err := a.Complete(context.Background(), []provider.Message{{Role: "user", Content: "你好"}}, CompletionOptions{ModelRef: "test/chat"})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Content != "无状态回答" {
		t.Fatalf("回答 = %+v", reply)
	}
	requests := prov.Requests()
	if len(requests) != 1 || len(requests[0].tools) != 0 {
		t.Fatalf("无状态请求暴露了长期记忆工具: %+v", requests)
	}
}
