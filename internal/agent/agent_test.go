package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

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

func TestChatIncludesPreviousMessages(t *testing.T) {
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "friendly")
	prov := &fakeProvider{}
	a := newTestAgent(t, prov, soulPath)

	if _, err := a.Chat(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Chat(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}

	want := []provider.Message{
		{Role: "system", Content: "friendly"},
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "reply"},
		{Role: "user", Content: "second"},
	}
	assertMessages(t, prov.requests[1], want)
}

func TestChatReloadsSoul(t *testing.T) {
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "friendly")
	prov := &fakeProvider{}
	a := newTestAgent(t, prov, soulPath)

	if _, err := a.Chat(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	writeSoul(t, soulPath, "serious")
	if _, err := a.Chat(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}

	if got := prov.requests[1][0]; got.Role != "system" || got.Content != "serious" {
		t.Fatalf("system message = %+v, want updated soul", got)
	}
}

func TestResetClearsConversation(t *testing.T) {
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "friendly")
	prov := &fakeProvider{}
	a := newTestAgent(t, prov, soulPath)

	if _, err := a.Chat(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	a.Reset()
	if _, err := a.Chat(context.Background(), "after reset"); err != nil {
		t.Fatal(err)
	}

	want := []provider.Message{
		{Role: "system", Content: "friendly"},
		{Role: "user", Content: "after reset"},
	}
	assertMessages(t, prov.requests[1], want)
}

func TestChatFailureDoesNotSaveMessages(t *testing.T) {
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	writeSoul(t, soulPath, "friendly")
	prov := &fakeProvider{err: errors.New("request failed")}
	a := newTestAgent(t, prov, soulPath)

	if _, err := a.Chat(context.Background(), "failed"); err == nil {
		t.Fatal("Chat() error = nil")
	}
	prov.err = nil
	if _, err := a.Chat(context.Background(), "retry"); err != nil {
		t.Fatal(err)
	}

	want := []provider.Message{
		{Role: "system", Content: "friendly"},
		{Role: "user", Content: "retry"},
	}
	assertMessages(t, prov.requests[1], want)
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
