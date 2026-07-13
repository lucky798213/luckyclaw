package session

import (
	"testing"

	"lukcyclaw/internal/provider"
)

func TestOpenCreatesIndependentSessions(t *testing.T) {
	manager := NewManager()
	first := manager.NewSession("terminal", "local", "default")
	first.Append(provider.Message{Role: "user", Content: "hello"})

	second := manager.NewSession("terminal", "local", "default")
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

func TestCurrentSessionIsolatesPlatformChats(t *testing.T) {
	manager := NewManager()
	first := manager.CurrentSession("feishu", "bot-a", "chat-1")
	first.Append(provider.Message{Role: "user", Content: "hello"})

	same := manager.CurrentSession("feishu", "bot-a", "chat-1")
	if same.Key() != first.Key() {
		t.Fatal("same platform chat did not reuse its active session")
	}

	other := manager.CurrentSession("feishu", "bot-a", "chat-2")
	if other.Key() == first.Key() {
		t.Fatal("different platform chats shared one session")
	}
	if got := len(other.Messages()); got != 0 {
		t.Fatalf("other chat has %d messages, want 0", got)
	}
}
