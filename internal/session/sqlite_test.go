package session

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lucky798213/luckyclaw/internal/bus"
	"github.com/lucky798213/luckyclaw/internal/provider"
)

func openTestSQLite(t *testing.T, path string) *SQLiteStore {
	t.Helper()
	store, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestSQLiteStoreRestoresCompleteSessionAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sessions.db")
	address := bus.ConversationAddress{
		Channel:   "telegram",
		AccountID: "bot-a",
		ChatID:    "chat-1",
		ThreadID:  "topic-1",
	}
	store := openTestSQLite(t, path)
	manager := NewManager("agent-a", store)
	current, err := manager.CurrentSession(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.SetModelRef(ctx, "openai/gpt-test"); err != nil {
		t.Fatal(err)
	}
	wantMessages := []provider.Message{
		{Role: "user", Content: "hello"},
		{
			Role: "assistant",
			ToolCalls: []provider.ToolCall{{
				ID:   "call-1",
				Type: "function",
				Function: provider.FunctionCall{
					Name:      "weather",
					Arguments: `{"city":"上海"}`,
				},
			}},
			RawAssistant: json.RawMessage(`{"role":"assistant","cached":true}`),
		},
		{Role: "tool", Content: "sunny", ToolCallID: "call-1", Name: "weather"},
	}
	if err := current.Append(ctx, wantMessages...); err != nil {
		t.Fatal(err)
	}
	wantKey := current.Key()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openTestSQLite(t, path)
	defer reopened.Close()
	reloadedManager := NewManager("agent-a", reopened)
	reloaded, err := reloadedManager.CurrentSession(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Key() != wantKey {
		t.Fatalf("session key = %q, want %q", reloaded.Key(), wantKey)
	}
	if reloaded.ModelRef() != "openai/gpt-test" {
		t.Fatalf("model ref = %q", reloaded.ModelRef())
	}
	if got := reloaded.Messages(); !reflect.DeepEqual(got, wantMessages) {
		t.Fatalf("messages = %#v, want %#v", got, wantMessages)
	}
}

func TestSQLiteStorePersistsNewActiveSessionAndKeepsOldSession(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sessions.db")
	address := bus.ConversationAddress{Channel: "terminal", AccountID: "local", ChatID: "default"}
	store := openTestSQLite(t, path)
	manager := NewManager("agent-a", store)
	oldSession, err := manager.CurrentSession(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	if err := oldSession.Append(ctx, provider.Message{Role: "user", Content: "old"}); err != nil {
		t.Fatal(err)
	}
	newSession, err := manager.NewSession(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openTestSQLite(t, path)
	defer reopened.Close()
	reloadedManager := NewManager("agent-a", reopened)
	active, err := reloadedManager.CurrentSession(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	if active.Key() != newSession.Key() {
		t.Fatalf("active key = %q, want %q", active.Key(), newSession.Key())
	}
	if len(active.Messages()) != 0 || active.ModelRef() != "" {
		t.Fatalf("new active session inherited state: %+v", active.Messages())
	}
	reloadedOld, ok, err := reloadedManager.Get(ctx, oldSession.Key())
	if err != nil {
		t.Fatal(err)
	}
	if !ok || len(reloadedOld.Messages()) != 1 || reloadedOld.Messages()[0].Content != "old" {
		t.Fatalf("old session was not restored: ok=%v messages=%+v", ok, reloadedOld.Messages())
	}
}

func TestSQLiteStoreIsolatesAgentsAndThreads(t *testing.T) {
	ctx := context.Background()
	store := openTestSQLite(t, filepath.Join(t.TempDir(), "sessions.db"))
	defer store.Close()
	base := bus.ConversationAddress{Channel: "telegram", AccountID: "bot", ChatID: "chat"}
	topic := base
	topic.ThreadID = "topic"

	agentA := NewManager("agent-a", store)
	agentB := NewManager("agent-b", store)
	aBase, err := agentA.CurrentSession(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	aTopic, err := agentA.CurrentSession(ctx, topic)
	if err != nil {
		t.Fatal(err)
	}
	bBase, err := agentB.CurrentSession(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if aBase.Key() == aTopic.Key() || aBase.Key() == bBase.Key() || aTopic.Key() == bBase.Key() {
		t.Fatal("不同 Agent 或线程共享了会话")
	}
}

func TestSessionDoesNotMutateMemoryWhenSQLiteWriteFails(t *testing.T) {
	ctx := context.Background()
	store := openTestSQLite(t, filepath.Join(t.TempDir(), "sessions.db"))
	manager := NewManager("agent-a", store)
	current, err := manager.CurrentSession(ctx, testAddress("terminal", "local", "default"))
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Append(ctx, provider.Message{Role: "user", Content: "saved"}); err != nil {
		t.Fatal(err)
	}
	if err := current.SetModelRef(ctx, "openai/saved"); err != nil {
		t.Fatal(err)
	}
	wantMessages := current.Messages()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	if err := current.Append(ctx, provider.Message{Role: "user", Content: "lost"}); err == nil {
		t.Fatal("Append() error = nil after closing database")
	}
	if got := current.Messages(); !reflect.DeepEqual(got, wantMessages) {
		t.Fatalf("messages changed after failed write: %+v", got)
	}
	if err := current.SetModelRef(ctx, "openai/lost"); err == nil {
		t.Fatal("SetModelRef() error = nil after closing database")
	}
	if got := current.ModelRef(); got != "openai/saved" {
		t.Fatalf("model changed after failed write: %q", got)
	}
}

func TestSQLiteStoreRejectsCorruptMessageJSON(t *testing.T) {
	ctx := context.Background()
	store := openTestSQLite(t, filepath.Join(t.TempDir(), "sessions.db"))
	defer store.Close()
	manager := NewManager("agent-a", store)
	current, err := manager.CurrentSession(ctx, testAddress("terminal", "local", "default"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx,
		`UPDATE sessions SET messages_json = ? WHERE agent_id = ? AND session_key = ?`,
		`{"broken"`, "agent-a", current.Key()); err != nil {
		t.Fatal(err)
	}

	reloaded := NewManager("agent-a", store)
	if _, err := reloaded.CurrentSession(ctx, current.Address()); err == nil {
		t.Fatal("CurrentSession() accepted corrupt message JSON")
	}
}
