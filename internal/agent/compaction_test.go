package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lucky798213/luckyclaw/internal/provider"
	"github.com/lucky798213/luckyclaw/internal/session"
	toolruntime "github.com/lucky798213/luckyclaw/internal/tools"
)

type compactionCall struct {
	messages    []provider.Message
	model       string
	maxTokens   int
	temperature float64
	summary     bool
}

type compactionProvider struct {
	mu           sync.Mutex
	calls        []compactionCall
	summary      string
	summaryError error
	reply        string
}

func (p *compactionProvider) Chat(
	_ context.Context,
	messages []provider.Message,
	_ []provider.Tool,
	model string,
	maxTokens int,
	temperature float64,
) (*provider.Message, error) {
	isSummary := len(messages) > 0 && messages[0].Role == "system" && messages[0].Content == summarySystemPrompt
	p.mu.Lock()
	p.calls = append(p.calls, compactionCall{
		messages:    append([]provider.Message(nil), messages...),
		model:       model,
		maxTokens:   maxTokens,
		temperature: temperature,
		summary:     isSummary,
	})
	p.mu.Unlock()
	if isSummary {
		if p.summaryError != nil {
			return nil, p.summaryError
		}
		summary := p.summary
		if summary == "" {
			summary = "旧对话摘要"
		}
		return &provider.Message{Role: "assistant", Content: summary}, nil
	}
	reply := p.reply
	if reply == "" {
		reply = "正常回答"
	}
	return &provider.Message{Role: "assistant", Content: reply}, nil
}

func (p *compactionProvider) ChatStream(
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

func (p *compactionProvider) snapshotCalls() []compactionCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]compactionCall(nil), p.calls...)
}

func newCompactionAgent(
	t *testing.T,
	prov provider.Provider,
	store session.Store,
	windowTokens int,
	thresholdTokens int,
	recentMessages int,
) *Agent {
	t.Helper()
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "保持简洁")
	registry, err := toolruntime.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	current, err := New(Options{
		ID:                        "lucky",
		Name:                      "LuckyClaw",
		DefaultModel:              "test/model",
		Models:                    []string{"test/model"},
		SoulPath:                  soulPath,
		MaxTokens:                 64,
		SessionStore:              store,
		Tools:                     registry,
		ContextWindowTokens:       windowTokens,
		CompactionThresholdTokens: thresholdTokens,
		CompactionRecentMessages:  recentMessages,
	}, newProviderManager(t, "test", []string{"model"}, prov))
	if err != nil {
		t.Fatal(err)
	}
	return current
}

func appendCompactionHistory(t *testing.T, current *session.Session, turns, contentSize int) {
	t.Helper()
	messages := make([]provider.Message, 0, turns*2)
	for turn := 0; turn < turns; turn++ {
		content := strings.Repeat(string(rune('a'+turn%20)), contentSize)
		if turn == 0 {
			content = "早期原文：项目代号是银鲤。" + content
		}
		messages = append(messages,
			provider.Message{Role: "user", Content: content},
			provider.Message{Role: "assistant", Content: "确认：" + content},
		)
	}
	if err := current.Append(context.Background(), messages...); err != nil {
		t.Fatal(err)
	}
}

func TestAutomaticCompactionPersistsSummaryAndKeepsFullHistory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sessions.db")
	store, err := session.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	prov := &compactionProvider{summary: "关键事实：项目代号是银鲤。", reply: "已继续"}
	a := newCompactionAgent(t, prov, store, 5000, 2500, 2)
	current, err := a.sessionsManager.CurrentSession(ctx, inbound("chat-1", "").Address())
	if err != nil {
		t.Fatal(err)
	}
	appendCompactionHistory(t, current, 5, 220)

	if got := a.HandleMessage(ctx, inbound("chat-1", "现在继续")); got != "已继续" {
		t.Fatalf("回答 = %q", got)
	}
	snapshot := current.ContextSnapshot()
	if snapshot.CompactedUntil != 8 || snapshot.Summary != "关键事实：项目代号是银鲤。" {
		t.Fatalf("压缩快照 = %+v", snapshot)
	}
	if len(snapshot.Messages) != 12 || !strings.Contains(snapshot.Messages[0].Content, "早期原文") {
		t.Fatalf("完整历史被删除: %+v", snapshot.Messages)
	}

	calls := prov.snapshotCalls()
	if len(calls) != 2 || !calls[0].summary || calls[1].summary {
		t.Fatalf("模型调用 = %+v", calls)
	}
	if calls[0].model != "model" || calls[0].temperature != summaryTemperature || calls[0].maxTokens != a.summaryTokenLimit() {
		t.Fatalf("摘要调用参数 = %+v", calls[0])
	}
	if len(calls[1].messages) != 5 || calls[1].messages[1].Role != "assistant" ||
		!strings.Contains(calls[1].messages[1].Content, "项目代号是银鲤") {
		t.Fatalf("压缩后的业务上下文 = %+v", calls[1].messages)
	}
	for _, message := range calls[1].messages[2:] {
		if strings.Contains(message.Content, "早期原文") {
			t.Fatalf("业务上下文仍包含早期原文: %+v", calls[1].messages)
		}
	}

	key := current.Key()
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
	if !exists || record.CompactedUntil != 8 || record.Summary != snapshot.Summary || len(record.Messages) != 12 {
		t.Fatalf("重启后的会话 = %+v, exists=%v", record, exists)
	}
}

func TestCompactionCutoffStartsAtCompleteUserTurn(t *testing.T) {
	messages := []provider.Message{
		{Role: "user", Content: "第一轮"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "call-1"}}},
		{Role: "tool", ToolCallID: "call-1", Content: "结果"},
		{Role: "assistant", Content: "第一轮完成"},
		{Role: "user", Content: "第二轮"},
		{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "call-2"}}},
		{Role: "tool", ToolCallID: "call-2", Content: "结果"},
		{Role: "assistant", Content: "第二轮完成"},
	}
	if got := preferredCompactionCutoff(messages, 0, 3); got != 4 {
		t.Fatalf("压缩边界 = %d, want 4", got)
	}
}

func TestCompactionFailureFallsBackOnlyWhileOriginalContextFits(t *testing.T) {
	ctx := context.Background()
	prov := &compactionProvider{summaryError: errors.New("摘要服务不可用"), reply: "降级回答"}
	a := newCompactionAgent(t, prov, nil, 5000, 2500, 2)
	current, err := a.sessionsManager.CurrentSession(ctx, inbound("chat-1", "").Address())
	if err != nil {
		t.Fatal(err)
	}
	appendCompactionHistory(t, current, 5, 220)
	if got := a.HandleMessage(ctx, inbound("chat-1", "继续")); got != "降级回答" {
		t.Fatalf("降级回答 = %q", got)
	}
	if snapshot := current.ContextSnapshot(); snapshot.CompactedUntil != 0 || snapshot.Summary != "" {
		t.Fatalf("失败摘要污染会话: %+v", snapshot)
	}

	hardProv := &compactionProvider{summaryError: errors.New("摘要服务不可用")}
	hardAgent := newCompactionAgent(t, hardProv, nil, 3000, 1500, 2)
	hardSession, err := hardAgent.sessionsManager.CurrentSession(ctx, inbound("chat-hard", "").Address())
	if err != nil {
		t.Fatal(err)
	}
	appendCompactionHistory(t, hardSession, 2, 600)
	if got := hardAgent.HandleMessage(ctx, inbound("chat-hard", "继续")); !strings.Contains(got, "上下文过长") {
		t.Fatalf("硬窗口错误 = %q", got)
	}
	if calls := hardProv.snapshotCalls(); len(calls) != 1 || !calls[0].summary {
		t.Fatalf("硬窗口后仍调用业务模型: %+v", calls)
	}
}

func TestOversizedCurrentTurnNeverCallsProvider(t *testing.T) {
	prov := &compactionProvider{}
	a := newCompactionAgent(t, prov, nil, 3000, 1500, 2)
	got := a.HandleMessage(context.Background(), inbound("chat-1", strings.Repeat("x", 2200)))
	if !strings.Contains(got, "上下文过长") {
		t.Fatalf("超大消息错误 = %q", got)
	}
	if calls := prov.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("超大消息仍调用 Provider: %+v", calls)
	}
}

func TestStatelessCompletionRejectsOversizedContext(t *testing.T) {
	prov := &compactionProvider{}
	a := newCompactionAgent(t, prov, nil, 3000, 1500, 2)
	_, err := a.Complete(context.Background(), []provider.Message{{
		Role:    "user",
		Content: strings.Repeat("x", 2200),
	}}, CompletionOptions{ModelRef: "test/model", MaxTokens: 64})
	var windowErr *contextWindowError
	if !errors.As(err, &windowErr) {
		t.Fatalf("无状态超窗错误 = %v", err)
	}
	if calls := prov.snapshotCalls(); len(calls) != 0 {
		t.Fatalf("无状态超窗仍调用 Provider: %+v", calls)
	}
}

func TestCompactionUsesCurrentMessageModelOverride(t *testing.T) {
	ctx := context.Background()
	defaultProvider := &compactionProvider{}
	overrideProvider := &compactionProvider{summary: "覆盖模型摘要", reply: "覆盖模型回答"}
	providers := newProviderManager(t, "default", []string{"model"}, defaultProvider)
	if err := providers.Register("override", overrideProvider, []string{"model"}); err != nil {
		t.Fatal(err)
	}
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "保持简洁")
	registry, err := toolruntime.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Options{
		ID:                        "lucky",
		Name:                      "LuckyClaw",
		DefaultModel:              "default/model",
		Models:                    []string{"default/model", "override/model"},
		SoulPath:                  soulPath,
		MaxTokens:                 64,
		Tools:                     registry,
		ContextWindowTokens:       5000,
		CompactionThresholdTokens: 2500,
		CompactionRecentMessages:  2,
	}, providers)
	if err != nil {
		t.Fatal(err)
	}
	current, err := a.sessionsManager.CurrentSession(ctx, inbound("chat-1", "").Address())
	if err != nil {
		t.Fatal(err)
	}
	appendCompactionHistory(t, current, 5, 220)
	message := inbound("chat-1", "继续")
	message.ModelRef = "override/model"
	if got := a.HandleMessage(ctx, message); got != "覆盖模型回答" {
		t.Fatalf("覆盖模型回答 = %q", got)
	}
	if len(defaultProvider.snapshotCalls()) != 0 {
		t.Fatal("压缩错误使用默认模型")
	}
	if calls := overrideProvider.snapshotCalls(); len(calls) != 2 || !calls[0].summary || calls[1].summary {
		t.Fatalf("覆盖模型调用 = %+v", calls)
	}
}
