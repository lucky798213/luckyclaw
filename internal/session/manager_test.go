package session

import (
	"context"
	"testing"

	"github.com/lucky798213/luckyclaw/internal/bus"
	"github.com/lucky798213/luckyclaw/internal/provider"
)

func testAddress(channel, accountID, chatID string) bus.ConversationAddress {
	return bus.ConversationAddress{Channel: channel, AccountID: accountID, ChatID: chatID}
}

func newMemoryManager() *Manager {
	return NewManager("test-agent", nil)
}

func requireCurrentSession(t *testing.T, manager *Manager, address bus.ConversationAddress) *Session {
	t.Helper()
	current, err := manager.CurrentSession(context.Background(), address)
	if err != nil {
		t.Fatal(err)
	}
	return current
}

func requireNewSession(t *testing.T, manager *Manager, address bus.ConversationAddress) *Session {
	t.Helper()
	current, err := manager.NewSession(context.Background(), address)
	if err != nil {
		t.Fatal(err)
	}
	return current
}

func TestOpenCreatesIndependentSessions(t *testing.T) {
	manager := newMemoryManager()
	address := testAddress("terminal", "local", "default")
	first := requireNewSession(t, manager, address)
	if err := first.Append(context.Background(), provider.Message{Role: "user", Content: "hello"}); err != nil {
		t.Fatal(err)
	}

	second := requireNewSession(t, manager, address)
	if first.Key() == second.Key() {
		t.Fatalf("new session reused key %q", first.Key())
	}
	if got := len(second.Messages()); got != 0 {
		t.Fatalf("new session has %d messages, want 0", got)
	}
	saved, ok, err := manager.Get(context.Background(), first.Key())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || len(saved.Messages()) != 1 {
		t.Fatal("old session was not kept in the sessions map")
	}
}

func TestCurrentSessionIsolatesPlatformChats(t *testing.T) {
	manager := newMemoryManager()
	first := requireCurrentSession(t, manager, testAddress("feishu", "bot-a", "chat-1"))
	if err := first.Append(context.Background(), provider.Message{Role: "user", Content: "hello"}); err != nil {
		t.Fatal(err)
	}

	same := requireCurrentSession(t, manager, testAddress("feishu", "bot-a", "chat-1"))
	if same.Key() != first.Key() {
		t.Fatal("same platform chat did not reuse its active session")
	}

	other := requireCurrentSession(t, manager, testAddress("feishu", "bot-a", "chat-2"))
	if other.Key() == first.Key() {
		t.Fatal("different platform chats shared one session")
	}
	if got := len(other.Messages()); got != 0 {
		t.Fatalf("other chat has %d messages, want 0", got)
	}
}

func TestSessionModelSelectionIsThreadSafeAndIsolated(t *testing.T) {
	manager := newMemoryManager()
	first := requireCurrentSession(t, manager, testAddress("feishu", "bot-a", "chat-1"))
	second := requireCurrentSession(t, manager, testAddress("feishu", "bot-a", "chat-2"))

	if err := first.SetModelRef(context.Background(), "deepseek/deepseek-reasoner"); err != nil {
		t.Fatal(err)
	}
	if got := first.ModelRef(); got != "deepseek/deepseek-reasoner" {
		t.Fatalf("first model = %q", got)
	}
	if got := second.ModelRef(); got != "" {
		t.Fatalf("second model = %q, want empty", got)
	}

	if err := first.ClearModelRef(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := first.ModelRef(); got != "" {
		t.Fatalf("cleared model = %q", got)
	}
}

func TestNewSessionDoesNotInheritModelSelection(t *testing.T) {
	manager := newMemoryManager()
	address := testAddress("terminal", "local", "default")
	current := requireCurrentSession(t, manager, address)
	if err := current.SetModelRef(context.Background(), "openrouter/vendor/model"); err != nil {
		t.Fatal(err)
	}

	next := requireNewSession(t, manager, address)
	if got := next.ModelRef(); got != "" {
		t.Fatalf("new session model = %q, want empty", got)
	}
	if got := current.ModelRef(); got != "openrouter/vendor/model" {
		t.Fatalf("old session model = %q", got)
	}
}

func TestCurrentSessionIsolatesThreads(t *testing.T) {
	manager := newMemoryManager()
	base := testAddress("telegram", "bot-a", "chat-1")
	firstAddress := base
	firstAddress.ThreadID = "topic-1"
	secondAddress := base
	secondAddress.ThreadID = "topic-2"

	first := requireCurrentSession(t, manager, firstAddress)
	same := requireCurrentSession(t, manager, firstAddress)
	second := requireCurrentSession(t, manager, secondAddress)
	baseSession := requireCurrentSession(t, manager, base)

	if same.Key() != first.Key() {
		t.Fatal("同一线程没有复用活跃会话")
	}
	if second.Key() == first.Key() || baseSession.Key() == first.Key() || baseSession.Key() == second.Key() {
		t.Fatal("基础聊天和不同线程共享了会话")
	}
	if first.Address() != firstAddress {
		t.Fatalf("Address() = %+v, want %+v", first.Address(), firstAddress)
	}
	if first.Channel() != "telegram" || first.AccountID() != "bot-a" || first.ChatID() != "chat-1" || first.ThreadID() != "topic-1" {
		t.Fatalf("会话地址便捷方法返回错误: %+v", first.Address())
	}
}
