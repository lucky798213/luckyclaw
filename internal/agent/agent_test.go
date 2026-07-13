package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"lukcyclaw/internal/bus"
	"lukcyclaw/internal/provider"
)

type fakeProvider struct {
	requests [][]provider.Message
	err      error
}

func (p *fakeProvider) Chat(_ context.Context, messages []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Message, error) {
	request := append([]provider.Message(nil), messages...)
	p.requests = append(p.requests, request)
	if p.err != nil {
		return nil, p.err
	}
	return &provider.Message{Role: "assistant", Content: "reply"}, nil
}

func writeSoul(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newTestAgent(t *testing.T, prov provider.Provider, soulPath string) *Agent {
	t.Helper()
	a, err := New("LuckyClaw", "test-model", prov, soulPath)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func inbound(chatID, text string) bus.InboundMessage {
	return bus.InboundMessage{
		Channel:   "terminal",
		AccountID: "local",
		ChatID:    chatID,
		Text:      text,
	}
}

func TestHandleMessageIncludesPreviousMessages(t *testing.T) {
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "friendly")
	prov := &fakeProvider{}
	a := newTestAgent(t, prov, soulPath)

	if got := a.HandleMessage(context.Background(), inbound("chat-1", "first")); got != "reply" {
		t.Fatalf("first reply = %q", got)
	}
	if got := a.HandleMessage(context.Background(), inbound("chat-1", "second")); got != "reply" {
		t.Fatalf("second reply = %q", got)
	}

	want := []provider.Message{
		{Role: "system", Content: "friendly"},
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "reply"},
		{Role: "user", Content: "second"},
	}
	assertMessages(t, prov.requests[1], want)
}

func TestHandleMessageReloadsSoul(t *testing.T) {
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "friendly")
	prov := &fakeProvider{}
	a := newTestAgent(t, prov, soulPath)

	a.HandleMessage(context.Background(), inbound("chat-1", "first"))
	writeSoul(t, soulPath, "serious")
	a.HandleMessage(context.Background(), inbound("chat-1", "second"))

	if got := prov.requests[1][0]; got.Role != "system" || got.Content != "serious" {
		t.Fatalf("system message = %+v, want updated soul", got)
	}
}

func TestResetClearsConversation(t *testing.T) {
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "friendly")
	prov := &fakeProvider{}
	a := newTestAgent(t, prov, soulPath)

	a.HandleMessage(context.Background(), inbound("chat-1", "first"))
	if got := a.HandleMessage(context.Background(), inbound("chat-1", "/new")); got == "" {
		t.Fatal("reset reply is empty")
	}
	a.HandleMessage(context.Background(), inbound("chat-1", "after reset"))

	want := []provider.Message{
		{Role: "system", Content: "friendly"},
		{Role: "user", Content: "after reset"},
	}
	assertMessages(t, prov.requests[1], want)
}

func TestHandleMessageFailureDoesNotSaveMessages(t *testing.T) {
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "friendly")
	prov := &fakeProvider{err: errors.New("request failed")}
	a := newTestAgent(t, prov, soulPath)

	if got := a.HandleMessage(context.Background(), inbound("chat-1", "failed")); got != handleMessageErrorReply {
		t.Fatalf("failure reply = %q", got)
	}
	prov.err = nil
	a.HandleMessage(context.Background(), inbound("chat-1", "retry"))

	want := []provider.Message{
		{Role: "system", Content: "friendly"},
		{Role: "user", Content: "retry"},
	}
	assertMessages(t, prov.requests[1], want)
}

func TestHandleMessageIsolatesChats(t *testing.T) {
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "friendly")
	prov := &fakeProvider{}
	a := newTestAgent(t, prov, soulPath)

	a.HandleMessage(context.Background(), inbound("chat-a", "from a"))
	a.HandleMessage(context.Background(), inbound("chat-b", "from b"))
	a.HandleMessage(context.Background(), inbound("chat-a", "a again"))

	wantChatB := []provider.Message{
		{Role: "system", Content: "friendly"},
		{Role: "user", Content: "from b"},
	}
	assertMessages(t, prov.requests[1], wantChatB)

	wantChatA := []provider.Message{
		{Role: "system", Content: "friendly"},
		{Role: "user", Content: "from a"},
		{Role: "assistant", Content: "reply"},
		{Role: "user", Content: "a again"},
	}
	assertMessages(t, prov.requests[2], wantChatA)
}

func assertMessages(t *testing.T, got, want []provider.Message) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("message count = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Content != want[i].Content {
			t.Fatalf("message[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
