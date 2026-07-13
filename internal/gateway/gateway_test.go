package gateway

import (
	"context"
	"testing"
	"time"

	"lukcyclaw/internal/bus"
)

type fakeHandler struct {
	received chan bus.InboundMessage
}

func (h *fakeHandler) HandleMessage(_ context.Context, msg bus.InboundMessage) string {
	h.received <- msg
	return "agent reply"
}

func TestGatewayConvertsInboundToOutbound(t *testing.T) {
	messageBus := bus.New()
	handler := &fakeHandler{received: make(chan bus.InboundMessage, 1)}
	gateway, err := New(messageBus, handler)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go gateway.Run(ctx)

	in := bus.InboundMessage{
		Channel:   "feishu",
		AccountID: "cli_test",
		ChatID:    "chat-1",
		MessageID: "message-1",
		Text:      "hello",
	}
	messageBus.Inbound <- in

	select {
	case got := <-handler.received:
		if got != in {
			t.Fatalf("handler message = %+v, want %+v", got, in)
		}
	case <-time.After(time.Second):
		t.Fatal("handler did not receive inbound message")
	}

	select {
	case out := <-messageBus.Outbound:
		if out.Channel != in.Channel || out.AccountID != in.AccountID || out.ChatID != in.ChatID {
			t.Fatalf("outbound route = %+v", out)
		}
		if out.Text != "agent reply" || out.ReplyToMsgID != in.MessageID {
			t.Fatalf("outbound content = %+v", out)
		}
	case <-time.After(time.Second):
		t.Fatal("gateway did not publish outbound message")
	}
}
