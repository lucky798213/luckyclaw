package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lucky798213/luckyclaw/internal/provider"
	"github.com/lucky798213/luckyclaw/internal/session"
	toolruntime "github.com/lucky798213/luckyclaw/internal/tools"
)

const (
	longConversationFact      = "项目代号是银鲤"
	longConversationRawMarker = "第1轮关键事实"
	longConversationQuestion  = "100轮后，请检索并告诉我之前的项目代号是什么？"
)

type longConversationCall struct {
	messages  []provider.Message
	tools     []provider.Tool
	maxTokens int
	summary   bool
}

type longConversationProvider struct {
	mu                    sync.Mutex
	calls                 []longConversationCall
	businessCalls         int
	retrievedOriginalFact bool
}

func (p *longConversationProvider) Chat(
	_ context.Context,
	messages []provider.Message,
	tools []provider.Tool,
	_ string,
	maxTokens int,
	_ float64,
) (*provider.Message, error) {
	isSummary := len(messages) > 0 && messages[0].Role == "system" && messages[0].Content == summarySystemPrompt
	p.mu.Lock()
	p.calls = append(p.calls, longConversationCall{
		messages:  append([]provider.Message(nil), messages...),
		tools:     append([]provider.Tool(nil), tools...),
		maxTokens: maxTokens,
		summary:   isSummary,
	})
	if !isSummary {
		p.businessCalls++
	}
	p.mu.Unlock()
	if isSummary {
		joined := joinMessageContents(messages)
		summary := "已压缩此前普通对话。"
		if strings.Contains(joined, "银鲤") {
			summary = "关键事实：项目代号是银鲤。其余旧轮次已压缩。"
		}
		return &provider.Message{Role: "assistant", Content: summary}, nil
	}
	last := messages[len(messages)-1]
	if last.Role == "tool" && last.Name == toolruntime.MemorySearchToolName {
		found := strings.Contains(last.Content, "银鲤")
		p.mu.Lock()
		p.retrievedOriginalFact = p.retrievedOriginalFact || found
		p.mu.Unlock()
		if found {
			return &provider.Message{Role: "assistant", Content: "检索结果确认：项目代号是银鲤。"}, nil
		}
		return &provider.Message{Role: "assistant", Content: "没有检索到项目代号。"}, nil
	}
	if last.Role == "user" && strings.Contains(last.Content, "项目代号是什么") {
		return &provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{
			toolCall("call-long-memory", toolruntime.MemorySearchToolName, `{"query":"项目代号"}`),
		}}, nil
	}
	return &provider.Message{Role: "assistant", Content: "已记录本轮。"}, nil
}

func (p *longConversationProvider) ChatStream(
	ctx context.Context,
	messages []provider.Message,
	tools []provider.Tool,
	model string,
	maxTokens int,
	temperature float64,
) (provider.Stream, error) {
	message, err := p.Chat(ctx, messages, tools, model, maxTokens, temperature)
	if err != nil {
		return nil, err
	}
	return &singleMessageStream{message: message}, nil
}

func (p *longConversationProvider) snapshot() ([]longConversationCall, int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]longConversationCall(nil), p.calls...), p.businessCalls, p.retrievedOriginalFact
}

func newLongConversationAgent(
	t *testing.T,
	store *session.SQLiteStore,
	prov *longConversationProvider,
) *Agent {
	t.Helper()
	memoryTool, err := toolruntime.NewMemorySearchTool(store, "lucky")
	if err != nil {
		t.Fatal(err)
	}
	registry, err := toolruntime.NewRegistry(memoryTool)
	if err != nil {
		t.Fatal(err)
	}
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "你是长对话评测助手。")
	current, err := New(Options{
		ID:                        "lucky",
		Name:                      "LuckyClaw",
		DefaultModel:              "test/model",
		Models:                    []string{"test/model"},
		SoulPath:                  soulPath,
		MaxTokens:                 64,
		SessionStore:              store,
		Tools:                     registry,
		MaxToolIterations:         4,
		ContextWindowTokens:       5000,
		CompactionThresholdTokens: 2600,
		CompactionRecentMessages:  8,
	}, newProviderManager(t, "test", []string{"model"}, prov))
	if err != nil {
		t.Fatal(err)
	}
	return current
}

func TestLongConversationRecallsKeyFactAfter100Turns(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "long-conversation.db")
	store, err := session.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	prov := &longConversationProvider{}
	a := newLongConversationAgent(t, store, prov)
	first := longConversationRawMarker + "：项目代号是银鲤，请长期记住。"
	if got := a.HandleMessage(ctx, inbound("chat-100", first)); got != "已记录本轮。" {
		t.Fatalf("第 1 轮回答 = %q", got)
	}
	for turn := 2; turn <= 100; turn++ {
		message := fmt.Sprintf("第%03d轮填充消息：%s", turn, strings.Repeat(string(rune('a'+turn%20)), 40))
		if got := a.HandleMessage(ctx, inbound("chat-100", message)); got != "已记录本轮。" {
			t.Fatalf("第 %d 轮回答 = %q", turn, got)
		}
	}

	current, err := a.sessionsManager.CurrentSession(ctx, inbound("chat-100", "").Address())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := current.ContextSnapshot()
	if len(snapshot.Messages) != 200 {
		t.Fatalf("100 轮完整历史消息数 = %d, want 200", len(snapshot.Messages))
	}
	if snapshot.CompactedUntil <= 0 || !strings.Contains(snapshot.Summary, longConversationFact) {
		t.Fatalf("100 轮后的压缩状态 = %+v", snapshot)
	}
	calls, businessCalls, _ := prov.snapshot()
	if businessCalls != 100 {
		t.Fatalf("100 轮业务模型调用数 = %d", businessCalls)
	}
	assertEveryLongConversationCallFits(t, a, calls)
	lastBusiness := lastNonSummaryCall(t, calls)
	if strings.Contains(joinMessageContents(lastBusiness.messages), longConversationRawMarker) {
		t.Fatalf("早期原文仍在第 100 轮直接上下文: %+v", lastBusiness.messages)
	}
	if !strings.Contains(joinMessageContents(lastBusiness.messages), longConversationFact) {
		t.Fatalf("第 100 轮上下文没有持久化摘要: %+v", lastBusiness.messages)
	}

	if got := a.HandleMessage(ctx, inbound("chat-100", longConversationQuestion)); got != "检索结果确认：项目代号是银鲤。" {
		t.Fatalf("第 101 次回忆回答 = %q", got)
	}
	calls, _, retrieved := prov.snapshot()
	if !retrieved {
		t.Fatal("第 101 次回答没有从 memory_search 结果读取早期事实")
	}
	assertEveryLongConversationCallFits(t, a, calls)
	snapshot = current.ContextSnapshot()
	if len(snapshot.Messages) != 204 || !strings.Contains(snapshot.Messages[0].Content, longConversationRawMarker) {
		t.Fatalf("回忆后的完整历史 = %+v", snapshot.Messages)
	}
	key := current.Key()
	compactedUntil := snapshot.CompactedUntil
	summary := snapshot.Summary
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := session.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	record, exists, err := reopened.LoadByKey(ctx, "lucky", key)
	if err != nil {
		t.Fatal(err)
	}
	if !exists || record.CompactedUntil != compactedUntil || record.Summary != summary || len(record.Messages) != 204 {
		t.Fatalf("重启后的会话状态 = %+v, exists=%v", record, exists)
	}
	results, err := reopened.SearchMemory(ctx, "lucky", record.Address, "项目代号", 20)
	if err != nil {
		t.Fatal(err)
	}
	foundOriginal := false
	for _, result := range results {
		if result.SessionKey == key && result.Sequence == 0 && strings.Contains(result.Content, longConversationFact) {
			foundOriginal = true
			break
		}
	}
	if !foundOriginal {
		t.Fatalf("重启后无法检索第 1 条原始事实: %+v", results)
	}

	restartedProvider := &longConversationProvider{}
	restartedAgent := newLongConversationAgent(t, reopened, restartedProvider)
	if got := restartedAgent.HandleMessage(ctx, inbound("chat-100", "重启后，之前的项目代号是什么？")); got != "检索结果确认：项目代号是银鲤。" {
		t.Fatalf("重启后的回忆回答 = %q", got)
	}
	restartedCalls, _, restartedRetrieved := restartedProvider.snapshot()
	if !restartedRetrieved {
		t.Fatal("重启后没有通过 memory_search 找回事实")
	}
	assertEveryLongConversationCallFits(t, restartedAgent, restartedCalls)
}

func assertEveryLongConversationCallFits(t *testing.T, a *Agent, calls []longConversationCall) {
	t.Helper()
	for index, call := range calls {
		if !a.tokenBudget.fits(call.messages, call.tools, call.maxTokens) {
			t.Fatalf("模型调用 %d 超过上下文窗口: %+v", index+1, a.tokenBudget.usage(call.messages, call.tools, call.maxTokens))
		}
	}
}

func lastNonSummaryCall(t *testing.T, calls []longConversationCall) longConversationCall {
	t.Helper()
	for index := len(calls) - 1; index >= 0; index-- {
		if !calls[index].summary {
			return calls[index]
		}
	}
	t.Fatal("没有业务模型调用")
	return longConversationCall{}
}

func joinMessageContents(messages []provider.Message) string {
	var content strings.Builder
	for _, message := range messages {
		content.WriteString(message.Content)
		content.WriteString("\n")
	}
	return content.String()
}
