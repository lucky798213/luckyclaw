package channels

import (
	"context"
	"testing"
	"time"

	"lukcyclaw/internal/bus"
)

type fakeChannel struct {
	sent chan bus.OutboundMessage
}

func (f *fakeChannel) Name() string      { return "feishu" }
func (f *fakeChannel) AccountID() string { return "cli_test" }
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
	channel := &fakeChannel{sent: make(chan bus.OutboundMessage, 1)}
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
