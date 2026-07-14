package channels

import (
	"context"
	"testing"
	"time"

	"lukcyclaw/internal/bus"
)

type fakeChannel struct {
	name      string
	accountID string
	sent      chan bus.OutboundMessage
}

func (f *fakeChannel) Name() string      { return f.name }
func (f *fakeChannel) AccountID() string { return f.accountID }
func (f *fakeChannel) Start(ctx context.Context) error {
	<-ctx.Done()
	return nil
}
func (f *fakeChannel) SendMessage(msg bus.OutboundMessage) error {
	f.sent <- msg
	return nil
}

func TestManagerRoutesOutboundToRegisteredChannel(t *testing.T) {
	messageBus := bus.New()
	manager, err := NewManager(messageBus)
	if err != nil {
		t.Fatal(err)
	}
	channel := &fakeChannel{name: "feishu", accountID: "cli_test", sent: make(chan bus.OutboundMessage, 1)}
	if err := manager.Register(channel); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)

	want := bus.OutboundMessage{
		Channel:   "feishu",
		AccountID: "cli_test",
		ChatID:    "chat-1",
		Text:      "hello",
	}
	messageBus.Outbound <- want

	select {
	case got := <-channel.sent:
		if got != want {
			t.Fatalf("sent message = %+v, want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("registered channel did not receive outbound message")
	}
}

func TestManagerSeparatesAccountsWithSameChatID(t *testing.T) {
	messageBus := bus.New()
	manager, err := NewManager(messageBus)
	if err != nil {
		t.Fatal(err)
	}
	first := &fakeChannel{name: "telegram", accountID: "bot-a", sent: make(chan bus.OutboundMessage, 1)}
	second := &fakeChannel{name: "telegram", accountID: "bot-b", sent: make(chan bus.OutboundMessage, 1)}
	if err := manager.Register(first); err != nil {
		t.Fatal(err)
	}
	if err := manager.Register(second); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)

	want := bus.OutboundMessage{
		Channel:   "telegram",
		AccountID: "bot-b",
		ChatID:    "shared-chat",
		ThreadID:  "topic-1",
		Text:      "hello",
	}
	messageBus.Outbound <- want

	select {
	case got := <-second.sent:
		if got != want {
			t.Fatalf("sent message = %+v, want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("目标账号没有收到出站消息")
	}
	select {
	case got := <-first.sent:
		t.Fatalf("错误账号收到了消息: %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
}
