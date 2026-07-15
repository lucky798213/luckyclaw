package gateway

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lukcyclaw/internal/agent"
	"lukcyclaw/internal/bus"
	"lukcyclaw/internal/config"
	"lukcyclaw/internal/provider"
)

type fakeProvider struct {
	reply string
}

func (p *fakeProvider) Chat(_ context.Context, _ []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Message, error) {
	return &provider.Message{Role: "assistant", Content: p.reply}, nil
}

func newTestAgent(t *testing.T, id, reply string) *agent.Agent {
	t.Helper()

	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	if err := os.WriteFile(soulPath, []byte("你是测试助手。"), 0o600); err != nil {
		t.Fatal(err)
	}
	providers := provider.NewManager()
	if err := providers.Register(id, &fakeProvider{reply: reply}, []string{"chat"}); err != nil {
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

func sendAndReceive(t *testing.T, messageBus *bus.MessageBus, in bus.InboundMessage) bus.OutboundMessage {
	t.Helper()

	messageBus.Inbound <- in
	select {
	case out := <-messageBus.Outbound:
		return out
	case <-time.After(time.Second):
		t.Fatal("gateway did not publish outbound message")
		return bus.OutboundMessage{}
	}
}

func assertNoOutbound(t *testing.T, messageBus *bus.MessageBus) {
	t.Helper()

	select {
	case out := <-messageBus.Outbound:
		t.Fatalf("收到非预期的出站消息: %+v", out)
	case <-time.After(100 * time.Millisecond):
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
