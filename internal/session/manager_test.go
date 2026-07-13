package session

import (
	"testing"

	"lukcyclaw/internal/provider"
)

func TestOpenCreatesIndependentSessions(t *testing.T) {
	manager := NewManager()
	first := manager.NewSession("terminal")
	first.Append(provider.Message{Role: "user", Content: "hello"})

	second := manager.NewSession("terminal")
	if first.Key() == second.Key() {
		t.Fatalf("new session reused key %q", first.Key())
	}
	if got := len(second.Messages()); got != 0 {
		t.Fatalf("new session has %d messages, want 0", got)
	}
	if saved, ok := manager.Get(first.Key()); !ok || len(saved.Messages()) != 1 {
		t.Fatal("old session was not kept in the sessions map")
	}
}
