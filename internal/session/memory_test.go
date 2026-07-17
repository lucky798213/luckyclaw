package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lucky798213/luckyclaw/internal/bus"
	"github.com/lucky798213/luckyclaw/internal/provider"
)

func TestSQLiteMemorySearchSupportsChineseEnglishAndConversationScope(t *testing.T) {
	ctx := context.Background()
	store := openTestSQLite(t, filepath.Join(t.TempDir(), "memory.db"))
	defer store.Close()
	address := bus.ConversationAddress{
		Channel:   "terminal",
		AccountID: "local",
		ChatID:    "chat-a",
		ThreadID:  "topic-a",
	}
	manager := NewManager("agent-a", store)
	first, err := manager.CurrentSession(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Append(ctx,
		provider.Message{Role: "user", Content: "早期事实：项目代号是银鲤，发布使用金丝雀策略。"},
		provider.Message{Role: "assistant", Content: "The deployment uses a canary rollout."},
		provider.Message{Role: "tool", Name: "http_fetch", Content: "普通工具结果：构建编号 build-7788。"},
		provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{
			ID:   "call-memory",
			Type: "function",
			Function: provider.FunctionCall{
				Name:      "memory_search",
				Arguments: `{"query":"递归调用参数标记"}`,
			},
		}}},
		provider.Message{Role: "tool", Name: "memory_search", Content: "递归污染标记 recursive-memory-result"},
	); err != nil {
		t.Fatal(err)
	}
	second, err := manager.NewSession(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Append(ctx, provider.Message{Role: "user", Content: "跨新会话线索：负责人是小林。共同关键词"}); err != nil {
		t.Fatal(err)
	}
	if err := second.Append(ctx, provider.Message{Role: "assistant", Content: "共同关键词再次出现"}); err != nil {
		t.Fatal(err)
	}

	otherAddress := address
	otherAddress.ChatID = "chat-b"
	other, err := manager.CurrentSession(ctx, otherAddress)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Append(ctx, provider.Message{Role: "user", Content: "隔离秘密：不要返回给 chat-a"}); err != nil {
		t.Fatal(err)
	}
	otherAgent := NewManager("agent-b", store)
	otherAgentSession, err := otherAgent.CurrentSession(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	if err := otherAgentSession.Append(ctx, provider.Message{Role: "user", Content: "另一个 Agent 的隔离秘密"}); err != nil {
		t.Fatal(err)
	}

	assertMemorySearchContains(t, store, "agent-a", address, "项目代号", "银鲤")
	assertMemorySearchContains(t, store, "agent-a", address, "deploy", "canary")
	assertMemorySearchContains(t, store, "agent-a", address, "银", "银鲤")
	assertMemorySearchContains(t, store, "agent-a", address, "build-7788", "普通工具结果")
	assertMemorySearchContains(t, store, "agent-a", address, "跨新会话", "小林")

	results, err := store.SearchMemory(ctx, "agent-a", address, "隔离秘密", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("检索越过会话或 Agent 边界: %+v", results)
	}
	results, err = store.SearchMemory(ctx, "agent-a", address, "递归污染标记", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("memory_search 结果被再次索引: %+v", results)
	}
	results, err = store.SearchMemory(ctx, "agent-a", address, "递归调用参数", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Fatalf("memory_search 调用参数被再次索引: %+v", results)
	}
	results, err = store.SearchMemory(ctx, "agent-a", address, "共同关键词", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("limit 未生效: %+v", results)
	}
	if _, err := store.SearchMemory(ctx, "agent-a", address, `项目" OR *`, 5); err != nil {
		t.Fatalf("特殊字符导致 FTS 语法错误: %v", err)
	}
	results, err = store.SearchMemory(ctx, "agent-a", address, "%", 5)
	if err != nil {
		t.Fatalf("LIKE 特殊字符查询失败: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("LIKE 通配符未转义: %+v", results)
	}
}

func TestSQLiteMemoryMigrationBackfillsExistingMessagesIdempotently(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-memory.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE sessions (
		agent_id TEXT NOT NULL,
		session_key TEXT NOT NULL,
		channel TEXT NOT NULL,
		account_id TEXT NOT NULL,
		chat_id TEXT NOT NULL,
		thread_id TEXT NOT NULL,
		model_ref TEXT NOT NULL DEFAULT '',
		messages_json TEXT NOT NULL DEFAULT '[]',
		summary TEXT NOT NULL DEFAULT '',
		compacted_until INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (agent_id, session_key)
	)`); err != nil {
		t.Fatal(err)
	}
	messages, err := json.Marshal([]provider.Message{
		{Role: "user", Content: "旧库回填事实：迁移暗号是白鹭。"},
		{Role: "assistant", Content: "已经记录迁移暗号。"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`INSERT INTO sessions (
		agent_id, session_key, channel, account_id, chat_id, thread_id,
		model_ref, messages_json, summary, compacted_until, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, '', ?, '', 0, 1000, 1000)`,
		"agent-a", "legacy-session", "terminal", "local", "default", "", string(messages)); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	address := bus.ConversationAddress{Channel: "terminal", AccountID: "local", ChatID: "default"}

	for attempt := 0; attempt < 2; attempt++ {
		store := openTestSQLite(t, path)
		results, err := store.SearchMemory(ctx, "agent-a", address, "迁移暗号", 5)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, result := range results {
			if strings.Contains(result.Content, "白鹭") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("旧消息没有回填: %+v", results)
		}
		var count int
		if err := store.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM memory_entries WHERE agent_id = ? AND session_key = ?`,
			"agent-a", "legacy-session").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 2 {
			t.Fatalf("第 %d 次迁移后的索引数量 = %d, want 2", attempt+1, count)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSQLiteUpdateMessagesRejectsHistoryReplacement(t *testing.T) {
	ctx := context.Background()
	store := openTestSQLite(t, filepath.Join(t.TempDir(), "append-only.db"))
	defer store.Close()
	manager := NewManager("agent-a", store)
	current, err := manager.CurrentSession(ctx, testAddress("terminal", "local", "default"))
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Append(ctx, provider.Message{Role: "user", Content: "原始事实"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateMessages(ctx, "agent-a", current.Key(), []provider.Message{{Role: "user", Content: "替换事实"}}); err == nil {
		t.Fatal("UpdateMessages() accepted history replacement")
	}
	record, exists, err := store.LoadByKey(ctx, "agent-a", current.Key())
	if err != nil {
		t.Fatal(err)
	}
	if !exists || len(record.Messages) != 1 || record.Messages[0].Content != "原始事实" {
		t.Fatalf("替换失败后历史发生变化: %+v", record)
	}
}

func assertMemorySearchContains(
	t *testing.T,
	store *SQLiteStore,
	agentID string,
	address bus.ConversationAddress,
	query string,
	want string,
) {
	t.Helper()
	results, err := store.SearchMemory(context.Background(), agentID, address, query, 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if strings.Contains(result.Content, want) {
			return
		}
	}
	t.Fatalf("SearchMemory(%q) = %+v, missing %q", query, results, want)
}
