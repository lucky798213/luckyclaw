package channels

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/lucky798213/luckyclaw/internal/bus"
)

func TestTerminalConvertsInputToInboundMessage(t *testing.T) {
	messageBus := bus.New()
	var output bytes.Buffer
	terminal, err := NewTerminal(strings.NewReader("hello\n"), &output, messageBus)
	if err != nil {
		t.Fatal(err)
	}

	if err := terminal.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	msg := <-messageBus.Inbound
	if msg.Channel != "terminal" || msg.AccountID != "local" || msg.ChatID != "default" {
		t.Fatalf("inbound route = %+v", msg)
	}
	if msg.Text != "hello" {
		t.Fatalf("inbound text = %q", msg.Text)
	}
}
