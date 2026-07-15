package gateway

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lucky798213/luckyclaw/internal/agent"
	"github.com/lucky798213/luckyclaw/internal/bus"
	"github.com/lucky798213/luckyclaw/internal/config"
	"github.com/lucky798213/luckyclaw/internal/provider"
)

type fakeProvider struct {
	reply string
}

func (p *fakeProvider) Chat(_ context.Context, _ []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Message, error) {
	return &provider.Message{Role: "assistant", Content: p.reply}, nil
}

type controlledProvider struct {
	started     chan string
	releaseSlow chan struct{}
}

func (p *controlledProvider) Chat(ctx context.Context, messages []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Message, error) {
	text := messages[len(messages)-1].Content
	p.started <- text
	if text == "slow" {
		select {
		case <-p.releaseSlow:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &provider.Message{Role: "assistant", Content: text + " reply"}, nil
}

type timeoutProvider struct {
	started   chan struct{}
	cancelled chan struct{}
}

func (p *timeoutProvider) Chat(ctx context.Context, _ []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Message, error) {
	close(p.started)
	<-ctx.Done()
	close(p.cancelled)
	return nil, ctx.Err()
}

func newTestAgent(t *testing.T, id, reply string) *agent.Agent {
	return newTestAgentWithProvider(t, id, &fakeProvider{reply: reply})
}

func newTestAgentWithProvider(t *testing.T, id string, currentProvider provider.Provider) *agent.Agent {
	t.Helper()

	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	if err := os.WriteFile(soulPath, []byte("你是测试助手。"), 0o600); err != nil {
		t.Fatal(err)
	}
	providers := provider.NewManager()
	if err := providers.Register(id, currentProvider, []string{"chat"}); err != nil {
		t.Fatal(err)
	}
	current, err := agent.New(agent.Options{
		ID:           id,
		Name:         id,
		DefaultModel: id + "/chat",
		Models:       []string{id + "/chat"},
		SoulPath:     soulPath,
	}, providers)
	if err != nil {
		t.Fatal(err)
	}
	return current
}

func newControlledGateway(t *testing.T, currentProvider provider.Provider) (*Gateway, *bus.MessageBus) {
	return newControlledGatewayWithTaskQueueConfig(t, currentProvider, config.TaskQueueConfig{})
}

func newControlledGatewayWithTaskQueueConfig(
	t *testing.T,
	currentProvider provider.Provider,
	taskQueueConfig config.TaskQueueConfig,
) (*Gateway, *bus.MessageBus) {
	t.Helper()

	agents, err := agent.NewManager(map[string]*agent.Agent{
		"default": newTestAgentWithProvider(t, "default", currentProvider),
	}, "default")
	if err != nil {
		t.Fatal(err)
	}
	messageBus := bus.New()
	current, err := NewWithTaskQueueConfig(messageBus, agents, nil, taskQueueConfig)
	if err != nil {
		t.Fatal(err)
	}
	return current, messageBus
}

func newTestGateway(t *testing.T, bindings []config.BindingConfig) (*Gateway, *bus.MessageBus) {
	t.Helper()

	agents, err := agent.NewManager(map[string]*agent.Agent{
		"default": newTestAgent(t, "default", "default reply"),
		"account": newTestAgent(t, "account", "account reply"),
		"exact":   newTestAgent(t, "exact", "exact reply"),
		"thread":  newTestAgent(t, "thread", "thread reply"),
	}, "default")
	if err != nil {
		t.Fatal(err)
	}
	messageBus := bus.New()
	current, err := New(messageBus, agents, bindings)
	if err != nil {
		t.Fatal(err)
	}
	return current, messageBus
}

func inboundMessageWithText(address bus.ConversationAddress, messageID, text string) bus.InboundMessage {
	return bus.InboundMessage{
		Channel:   address.Channel,
		AccountID: address.AccountID,
		ChatID:    address.ChatID,
		ThreadID:  address.ThreadID,
		MessageID: messageID,
		Text:      text,
	}
}

func receiveOutbound(t *testing.T, messageBus *bus.MessageBus) bus.OutboundMessage {
	t.Helper()

	select {
	case out := <-messageBus.Outbound:
		return out
	case <-time.After(time.Second):
		t.Fatal("gateway did not publish outbound message")
		return bus.OutboundMessage{}
	}
}

func sendAndReceive(t *testing.T, messageBus *bus.MessageBus, in bus.InboundMessage) bus.OutboundMessage {
	t.Helper()

	messageBus.Inbound <- in
	return receiveOutbound(t, messageBus)
}

func assertNoOutbound(t *testing.T, messageBus *bus.MessageBus) {
	t.Helper()

	select {
	case out := <-messageBus.Outbound:
		t.Fatalf("收到非预期的出站消息: %+v", out)
	case <-time.After(100 * time.Millisecond):
	}
}

func receiveProviderStart(t *testing.T, started <-chan string) string {
	t.Helper()
	select {
	case text := <-started:
		return text
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
		return ""
	}
}

func startGateway(t *testing.T, current *Gateway) (context.CancelFunc, <-chan struct{}) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		current.Run(ctx)
		close(done)
	}()
	return cancel, done
}

func stopGateway(t *testing.T, cancel context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("gateway did not stop")
	}
}

func TestGatewayConvertsInboundToOutbound(t *testing.T) {
	gateway, messageBus := newTestGateway(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go gateway.Run(ctx)

	in := bus.InboundMessage{
		Channel:   "feishu",
		AccountID: "cli_test",
		ChatID:    "chat-1",
		ThreadID:  "thread-1",
		MessageID: "message-1",
		Text:      "hello",
	}
	out := sendAndReceive(t, messageBus, in)
	if out.Address() != in.Address() {
		t.Fatalf("outbound route = %+v", out)
	}
	if out.Text != "default reply" || out.ReplyToMsgID != in.MessageID {
		t.Fatalf("outbound content = %+v", out)
	}
}

func TestGatewayRoutingPriority(t *testing.T) {
	bindings := []config.BindingConfig{
		{Channel: "feishu", AccountID: "bot", AgentID: "account"},
		{Channel: "feishu", AccountID: "bot", ChatID: "vip", AgentID: "exact"},
		{Channel: "feishu", AccountID: "bot", ChatID: "vip", ThreadID: "topic-1", AgentID: "thread"},
	}
	gateway, messageBus := newTestGateway(t, bindings)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go gateway.Run(ctx)

	tests := []struct {
		name string
		msg  bus.InboundMessage
		want string
	}{
		{
			name: "线程绑定优先于聊天和账号绑定",
			msg:  bus.InboundMessage{Channel: "feishu", AccountID: "bot", ChatID: "vip", ThreadID: "topic-1", Text: "hello"},
			want: "thread reply",
		},
		{
			name: "没有线程绑定时回退到聊天绑定",
			msg:  bus.InboundMessage{Channel: "feishu", AccountID: "bot", ChatID: "vip", ThreadID: "topic-2", Text: "hello"},
			want: "exact reply",
		},
		{
			name: "聊天绑定优先于账号绑定",
			msg:  bus.InboundMessage{Channel: "feishu", AccountID: "bot", ChatID: "vip", Text: "hello"},
			want: "exact reply",
		},
		{
			name: "账号绑定处理其他聊天",
			msg:  bus.InboundMessage{Channel: "feishu", AccountID: "bot", ChatID: "normal", Text: "hello"},
			want: "account reply",
		},
		{
			name: "没有绑定时使用默认 Agent",
			msg:  bus.InboundMessage{Channel: "terminal", AccountID: "local", ChatID: "default", Text: "hello"},
			want: "default reply",
		},
		{
			name: "显式 Agent 覆盖所有绑定",
			msg:  bus.InboundMessage{Channel: "feishu", AccountID: "bot", ChatID: "vip", ThreadID: "topic-1", AgentID: "account", Text: "hello"},
			want: "account reply",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if out := sendAndReceive(t, messageBus, test.msg); out.Text != test.want {
				t.Fatalf("reply = %q, want %q", out.Text, test.want)
			}
		})
	}
}

func TestGatewayRejectsUnknownExplicitAgent(t *testing.T) {
	gateway, messageBus := newTestGateway(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go gateway.Run(ctx)

	out := sendAndReceive(t, messageBus, bus.InboundMessage{
		Channel: "terminal", AccountID: "local", ChatID: "default", AgentID: "missing", Text: "hello",
	})
	if !strings.Contains(out.Text, "没有找到指定的 Agent") {
		t.Fatalf("reply = %q", out.Text)
	}
}

func TestGatewayDeduplicatesInboundMessages(t *testing.T) {
	gateway, messageBus := newTestGateway(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go gateway.Run(ctx)

	msg := bus.InboundMessage{
		Channel:   "feishu",
		AccountID: "bot",
		ChatID:    "chat-1",
		ThreadID:  "topic-1",
		MessageID: "message-1",
		Text:      "hello",
	}
	if out := sendAndReceive(t, messageBus, msg); out.Text != "default reply" {
		t.Fatalf("首次消息回复 = %q", out.Text)
	}
	messageBus.Inbound <- msg
	assertNoOutbound(t, messageBus)
}

func TestGatewayDedupKeepsConversationsIsolated(t *testing.T) {
	gateway, messageBus := newTestGateway(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go gateway.Run(ctx)

	messages := []bus.InboundMessage{
		{Channel: "feishu", AccountID: "bot", ChatID: "chat-1", ThreadID: "topic-1", MessageID: "shared-message", Text: "hello"},
		{Channel: "feishu", AccountID: "bot", ChatID: "chat-1", ThreadID: "topic-2", MessageID: "shared-message", Text: "hello"},
		{Channel: "feishu", AccountID: "bot", ChatID: "chat-2", ThreadID: "topic-1", MessageID: "shared-message", Text: "hello"},
	}
	for _, msg := range messages {
		if out := sendAndReceive(t, messageBus, msg); out.Text != "default reply" {
			t.Fatalf("地址 %+v 的回复 = %q", msg.Address(), out.Text)
		}
	}
}

func TestGatewayDeduplicatesBeforeAgentMatching(t *testing.T) {
	gateway, messageBus := newTestGateway(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go gateway.Run(ctx)

	msg := bus.InboundMessage{
		Channel:   "terminal",
		AccountID: "local",
		ChatID:    "default",
		MessageID: "message-1",
		AgentID:   "missing",
		Text:      "hello",
	}
	if out := sendAndReceive(t, messageBus, msg); !strings.Contains(out.Text, "没有找到指定的 Agent") {
		t.Fatalf("首次消息回复 = %q", out.Text)
	}
	messageBus.Inbound <- msg
	assertNoOutbound(t, messageBus)
}

func TestGatewaySerializesMessagesFromSameConversation(t *testing.T) {
	currentProvider := &controlledProvider{started: make(chan string, 2), releaseSlow: make(chan struct{})}
	gateway, messageBus := newControlledGateway(t, currentProvider)
	cancel, done := startGateway(t, gateway)
	defer stopGateway(t, cancel, done)

	address := bus.ConversationAddress{Channel: "feishu", AccountID: "bot", ChatID: "chat", ThreadID: "topic"}
	messageBus.Inbound <- inboundMessageWithText(address, "message-1", "slow")
	if got := receiveProviderStart(t, currentProvider.started); got != "slow" {
		t.Fatalf("首个 Provider 请求 = %q", got)
	}
	messageBus.Inbound <- inboundMessageWithText(address, "message-2", "fast")
	select {
	case text := <-currentProvider.started:
		t.Fatalf("首条消息完成前启动了第二条消息: %q", text)
	case <-time.After(100 * time.Millisecond):
	}

	close(currentProvider.releaseSlow)
	if out := receiveOutbound(t, messageBus); out.ReplyToMsgID != "message-1" {
		t.Fatalf("第一条出站消息 = %+v", out)
	}
	if got := receiveProviderStart(t, currentProvider.started); got != "fast" {
		t.Fatalf("第二个 Provider 请求 = %q", got)
	}
	if out := receiveOutbound(t, messageBus); out.ReplyToMsgID != "message-2" {
		t.Fatalf("第二条出站消息 = %+v", out)
	}
}

func TestGatewayRunsDifferentThreadsConcurrently(t *testing.T) {
	currentProvider := &controlledProvider{started: make(chan string, 2), releaseSlow: make(chan struct{})}
	gateway, messageBus := newControlledGateway(t, currentProvider)
	cancel, done := startGateway(t, gateway)
	defer stopGateway(t, cancel, done)

	first := bus.ConversationAddress{Channel: "feishu", AccountID: "bot", ChatID: "chat", ThreadID: "topic-1"}
	second := first
	second.ThreadID = "topic-2"
	messageBus.Inbound <- inboundMessageWithText(first, "message-1", "slow")
	if got := receiveProviderStart(t, currentProvider.started); got != "slow" {
		t.Fatalf("首个 Provider 请求 = %q", got)
	}
	messageBus.Inbound <- inboundMessageWithText(second, "message-2", "fast")
	if got := receiveProviderStart(t, currentProvider.started); got != "fast" {
		t.Fatalf("并发 Provider 请求 = %q", got)
	}
	if out := receiveOutbound(t, messageBus); out.ReplyToMsgID != "message-2" {
		t.Fatalf("先完成的出站消息 = %+v", out)
	}

	close(currentProvider.releaseSlow)
	if out := receiveOutbound(t, messageBus); out.ReplyToMsgID != "message-1" {
		t.Fatalf("慢任务出站消息 = %+v", out)
	}
}

func TestGatewayUsesConfiguredGlobalConcurrency(t *testing.T) {
	currentProvider := &controlledProvider{started: make(chan string, 2), releaseSlow: make(chan struct{})}
	gateway, messageBus := newControlledGatewayWithTaskQueueConfig(t, currentProvider, config.TaskQueueConfig{
		MaxConcurrent:             1,
		TaskTimeoutSeconds:        2,
		MaxPendingPerConversation: 10,
	})
	cancel, done := startGateway(t, gateway)
	defer stopGateway(t, cancel, done)

	first := bus.ConversationAddress{Channel: "feishu", AccountID: "bot", ChatID: "chat", ThreadID: "topic-1"}
	second := first
	second.ThreadID = "topic-2"
	messageBus.Inbound <- inboundMessageWithText(first, "message-1", "slow")
	if got := receiveProviderStart(t, currentProvider.started); got != "slow" {
		t.Fatalf("首个 Provider 请求 = %q", got)
	}
	messageBus.Inbound <- inboundMessageWithText(second, "message-2", "fast")
	select {
	case text := <-currentProvider.started:
		t.Fatalf("全局并发槽释放前启动了第二个任务: %q", text)
	case <-time.After(100 * time.Millisecond):
	}

	close(currentProvider.releaseSlow)
	if out := receiveOutbound(t, messageBus); out.ReplyToMsgID != "message-1" {
		t.Fatalf("首条出站消息 = %+v", out)
	}
	if got := receiveProviderStart(t, currentProvider.started); got != "fast" {
		t.Fatalf("第二个 Provider 请求 = %q", got)
	}
	if out := receiveOutbound(t, messageBus); out.ReplyToMsgID != "message-2" {
		t.Fatalf("第二条出站消息 = %+v", out)
	}
}

func TestGatewayUsesConfiguredTaskTimeout(t *testing.T) {
	currentProvider := &timeoutProvider{started: make(chan struct{}), cancelled: make(chan struct{})}
	gateway, messageBus := newControlledGatewayWithTaskQueueConfig(t, currentProvider, config.TaskQueueConfig{
		MaxConcurrent:             1,
		TaskTimeoutSeconds:        1,
		MaxPendingPerConversation: 10,
	})
	cancel, done := startGateway(t, gateway)
	defer stopGateway(t, cancel, done)

	messageBus.Inbound <- inboundMessageWithText(
		bus.ConversationAddress{Channel: "feishu", AccountID: "bot", ChatID: "chat"},
		"message-1",
		"wait for timeout",
	)
	select {
	case <-currentProvider.started:
	case <-time.After(time.Second):
		t.Fatal("Provider 没有开始执行")
	}
	select {
	case <-currentProvider.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("配置的任务超时没有取消 Provider")
	}
}

func TestGatewayForgetsDedupEntryWhenConversationQueueIsFull(t *testing.T) {
	currentProvider := &controlledProvider{started: make(chan string, 3), releaseSlow: make(chan struct{})}
	gateway, messageBus := newControlledGatewayWithTaskQueueConfig(t, currentProvider, config.TaskQueueConfig{
		MaxConcurrent:             1,
		TaskTimeoutSeconds:        1,
		MaxPendingPerConversation: 1,
	})
	defer gateway.taskQueue.Stop()

	address := bus.ConversationAddress{Channel: "feishu", AccountID: "bot", ChatID: "chat"}
	first := inboundMessageWithText(address, "message-1", "slow")
	second := inboundMessageWithText(address, "message-2", "second")
	retry := inboundMessageWithText(address, "message-3", "retry")
	gateway.handleInbound(first)
	if got := receiveProviderStart(t, currentProvider.started); got != "slow" {
		t.Fatalf("首个 Provider 请求 = %q", got)
	}
	gateway.handleInbound(second)
	gateway.handleInbound(retry)

	close(currentProvider.releaseSlow)
	receiveOutbound(t, messageBus)
	if got := receiveProviderStart(t, currentProvider.started); got != "second" {
		t.Fatalf("第二个 Provider 请求 = %q", got)
	}
	receiveOutbound(t, messageBus)

	gateway.handleInbound(retry)
	if got := receiveProviderStart(t, currentProvider.started); got != "retry" {
		t.Fatalf("重试 Provider 请求 = %q", got)
	}
	if out := receiveOutbound(t, messageBus); out.ReplyToMsgID != retry.MessageID {
		t.Fatalf("重试出站消息 = %+v", out)
	}
}

func TestGatewayValidatesBindings(t *testing.T) {
	agents, err := agent.NewManager(map[string]*agent.Agent{
		"default": newTestAgent(t, "default", "reply"),
	}, "default")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		bindings []config.BindingConfig
	}{
		{
			name:     "未知 Agent",
			bindings: []config.BindingConfig{{Channel: "terminal", AccountID: "local", AgentID: "missing"}},
		},
		{
			name: "重复账号绑定",
			bindings: []config.BindingConfig{
				{Channel: "terminal", AccountID: "local", AgentID: "default"},
				{Channel: "terminal", AccountID: "local", AgentID: "default"},
			},
		},
		{
			name: "重复聊天绑定",
			bindings: []config.BindingConfig{
				{Channel: "feishu", AccountID: "bot", ChatID: "chat", AgentID: "default"},
				{Channel: "feishu", AccountID: "bot", ChatID: "chat", AgentID: "default"},
			},
		},
		{
			name: "重复线程绑定",
			bindings: []config.BindingConfig{
				{Channel: "feishu", AccountID: "bot", ChatID: "chat", ThreadID: "topic", AgentID: "default"},
				{Channel: "feishu", AccountID: "bot", ChatID: "chat", ThreadID: "topic", AgentID: "default"},
			},
		},
		{
			name:     "线程绑定缺少聊天",
			bindings: []config.BindingConfig{{Channel: "feishu", AccountID: "bot", ThreadID: "topic", AgentID: "default"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(bus.New(), agents, test.bindings); err == nil {
				t.Fatal("New() error = nil")
			}
		})
	}
}
