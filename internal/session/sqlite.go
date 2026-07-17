package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"

	"github.com/lucky798213/luckyclaw/internal/bus"
	"github.com/lucky798213/luckyclaw/internal/provider"
)

var errSessionNotFound = errors.New("session not found")

// SQLiteStore 使用一个 SQLite 文件保存所有 Agent 的会话。
type SQLiteStore struct {
	db *sql.DB
}

// OpenSQLite 打开会话数据库并执行幂等迁移。
func OpenSQLite(path string) (*SQLiteStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("sqlite path cannot be empty")
	}

	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, fmt.Errorf("create sqlite directory %q: %w", parent, err)
	}

	dsn := "file:" + filepath.Clean(path) +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite 同一时刻只允许一个写入者，单连接可以避免应用层堆积锁竞争。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	store := &SQLiteStore{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("protect sqlite file %q: %w", path, err)
	}
	return store, nil
}

// Close 关闭底层数据库连接。
func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteStore) migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS sessions (
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
		)`,
		`CREATE TABLE IF NOT EXISTS active_sessions (
			agent_id TEXT NOT NULL,
			channel TEXT NOT NULL,
			account_id TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			thread_id TEXT NOT NULL,
			session_key TEXT NOT NULL,
			PRIMARY KEY (agent_id, channel, account_id, chat_id, thread_id),
			FOREIGN KEY (agent_id, session_key)
				REFERENCES sessions(agent_id, session_key) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS memory_entries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			agent_id TEXT NOT NULL,
			session_key TEXT NOT NULL,
			seq INTEGER NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			UNIQUE (agent_id, session_key, seq),
			FOREIGN KEY (agent_id, session_key)
				REFERENCES sessions(agent_id, session_key) ON DELETE CASCADE
		)`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
			content,
			content='memory_entries',
			content_rowid='id',
			tokenize='trigram'
		)`,
		`CREATE TRIGGER IF NOT EXISTS memory_entries_ai AFTER INSERT ON memory_entries BEGIN
			INSERT INTO memory_fts(rowid, content) VALUES (new.id, new.content);
		END`,
		`CREATE TRIGGER IF NOT EXISTS memory_entries_ad AFTER DELETE ON memory_entries BEGIN
			INSERT INTO memory_fts(memory_fts, rowid, content)
			VALUES ('delete', old.id, old.content);
		END`,
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin session migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate sessions: %w", err)
		}
	}
	if err := ensureSessionColumn(ctx, tx, "summary", `ALTER TABLE sessions ADD COLUMN summary TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := ensureSessionColumn(ctx, tx, "compacted_until", `ALTER TABLE sessions ADD COLUMN compacted_until INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := backfillMemoryEntries(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session migration: %w", err)
	}
	return nil
}

// CreateAndActivate 原子创建新会话并切换该地址的活动会话。
func (s *SQLiteStore) CreateAndActivate(ctx context.Context, agentID string, record Record) error {
	messages, err := marshalMessages(record.Messages)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create session: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO sessions (
			agent_id, session_key, channel, account_id, chat_id, thread_id,
			model_ref, messages_json, summary, compacted_until, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		agentID,
		record.Key,
		record.Address.Channel,
		record.Address.AccountID,
		record.Address.ChatID,
		record.Address.ThreadID,
		record.ModelRef,
		messages,
		record.Summary,
		record.CompactedUntil,
		now,
		now,
	); err != nil {
		return fmt.Errorf("insert session %q: %w", record.Key, err)
	}
	if err := insertMemoryEntries(ctx, tx, agentID, record.Key, 0, record.Messages, now, false); err != nil {
		return fmt.Errorf("index session %q: %w", record.Key, err)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO active_sessions (
			agent_id, channel, account_id, chat_id, thread_id, session_key
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (agent_id, channel, account_id, chat_id, thread_id)
		DO UPDATE SET session_key = excluded.session_key`,
		agentID,
		record.Address.Channel,
		record.Address.AccountID,
		record.Address.ChatID,
		record.Address.ThreadID,
		record.Key,
	); err != nil {
		return fmt.Errorf("activate session %q: %w", record.Key, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session %q: %w", record.Key, err)
	}
	return nil
}

// LoadActive 读取指定会话地址当前激活的会话。
func (s *SQLiteStore) LoadActive(ctx context.Context, agentID string, address bus.ConversationAddress) (Record, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT s.session_key, s.channel, s.account_id, s.chat_id, s.thread_id,
			s.model_ref, s.messages_json, s.summary, s.compacted_until
		FROM active_sessions AS a
		JOIN sessions AS s
			ON s.agent_id = a.agent_id AND s.session_key = a.session_key
		WHERE a.agent_id = ? AND a.channel = ? AND a.account_id = ?
			AND a.chat_id = ? AND a.thread_id = ?`,
		agentID,
		address.Channel,
		address.AccountID,
		address.ChatID,
		address.ThreadID,
	)
	return scanRecord(row)
}

// LoadByKey 根据 Agent 和会话 Key 读取历史会话。
func (s *SQLiteStore) LoadByKey(ctx context.Context, agentID, key string) (Record, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT session_key, channel, account_id, chat_id, thread_id,
			model_ref, messages_json, summary, compacted_until
		FROM sessions
		WHERE agent_id = ? AND session_key = ?`, agentID, key)
	return scanRecord(row)
}

// ListByAgent 按最近更新时间倒序列出一个 Agent 的会话。
// channel 为空时返回全部渠道，否则只返回指定渠道。
func (s *SQLiteStore) ListByAgent(ctx context.Context, agentID, channel string) ([]Summary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT session_key, channel, account_id, chat_id, thread_id,
			model_ref, messages_json, created_at, updated_at
		FROM sessions
		WHERE agent_id = ? AND (? = '' OR channel = ?)
		ORDER BY updated_at DESC, created_at DESC`, agentID, channel, channel)
	if err != nil {
		return nil, fmt.Errorf("list agent sessions: %w", err)
	}
	defer rows.Close()

	summaries := make([]Summary, 0)
	for rows.Next() {
		var summary Summary
		var messagesJSON string
		var createdAt int64
		var updatedAt int64
		if err := rows.Scan(
			&summary.Key,
			&summary.Address.Channel,
			&summary.Address.AccountID,
			&summary.Address.ChatID,
			&summary.Address.ThreadID,
			&summary.ModelRef,
			&messagesJSON,
			&createdAt,
			&updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan agent session summary: %w", err)
		}
		if err := json.Unmarshal([]byte(messagesJSON), &summary.Messages); err != nil {
			return nil, fmt.Errorf("decode session %q messages: %w", summary.Key, err)
		}
		if summary.Messages == nil {
			summary.Messages = []provider.Message{}
		}
		summary.CreatedAt = time.UnixMilli(createdAt)
		summary.UpdatedAt = time.UnixMilli(updatedAt)
		summaries = append(summaries, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agent sessions: %w", err)
	}
	return summaries, nil
}

// UpdateMessages 仅允许追加完整历史，并在同一事务中写入长期记忆索引。
func (s *SQLiteStore) UpdateMessages(ctx context.Context, agentID, key string, messages []provider.Message) error {
	payload, err := marshalMessages(messages)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin update session messages %q: %w", key, err)
	}
	defer func() { _ = tx.Rollback() }()
	var existingJSON string
	if err := tx.QueryRowContext(ctx,
		`SELECT messages_json FROM sessions WHERE agent_id = ? AND session_key = ?`, agentID, key).Scan(&existingJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", errSessionNotFound, key)
		}
		return fmt.Errorf("load session messages %q: %w", key, err)
	}
	var existing []provider.Message
	if err := json.Unmarshal([]byte(existingJSON), &existing); err != nil {
		return fmt.Errorf("decode existing session messages %q: %w", key, err)
	}
	if len(messages) < len(existing) || !reflect.DeepEqual(existing, messages[:len(existing)]) {
		return fmt.Errorf("update session messages %q must append to the complete history", key)
	}
	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `
		UPDATE sessions
		SET messages_json = ?, updated_at = ?
		WHERE agent_id = ? AND session_key = ?`,
		payload, now, agentID, key)
	if err != nil {
		return fmt.Errorf("update session messages %q: %w", key, err)
	}
	if err := requireUpdated(result, key); err != nil {
		return err
	}
	if err := insertMemoryEntries(ctx, tx, agentID, key, len(existing), messages[len(existing):], now, false); err != nil {
		return fmt.Errorf("index session messages %q: %w", key, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session messages %q: %w", key, err)
	}
	return nil
}

// UpdateModelRef 更新会话级模型选择。
func (s *SQLiteStore) UpdateModelRef(ctx context.Context, agentID, key, modelRef string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE sessions
		SET model_ref = ?, updated_at = ?
		WHERE agent_id = ? AND session_key = ?`,
		modelRef, time.Now().UnixMilli(), agentID, key)
	if err != nil {
		return fmt.Errorf("update session model %q: %w", key, err)
	}
	return requireUpdated(result, key)
}

// UpdateCompaction 使用旧位置作乐观锁，原子推进摘要和压缩游标。
func (s *SQLiteStore) UpdateCompaction(
	ctx context.Context,
	agentID string,
	key string,
	expectedUntil int,
	summary string,
	compactedUntil int,
) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE sessions
		SET summary = ?, compacted_until = ?, updated_at = ?
		WHERE agent_id = ? AND session_key = ? AND compacted_until = ?`,
		summary, compactedUntil, time.Now().UnixMilli(), agentID, key, expectedUntil)
	if err != nil {
		return fmt.Errorf("update session compaction %q: %w", key, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read compaction update result for session %q: %w", key, err)
	}
	if count > 0 {
		return nil
	}
	var exists int
	if err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM sessions WHERE agent_id = ? AND session_key = ?`, agentID, key).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", errSessionNotFound, key)
		}
		return fmt.Errorf("check session compaction conflict %q: %w", key, err)
	}
	return ErrCompactionConflict
}

// SearchMemory 在同一 Agent 和会话地址下跨 session 检索原始历史。
func (s *SQLiteStore) SearchMemory(
	ctx context.Context,
	agentID string,
	address bus.ConversationAddress,
	query string,
	limit int,
) ([]MemorySearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("memory search query cannot be empty")
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}
	terms := strings.Fields(query)
	if len(terms) == 0 {
		return nil, fmt.Errorf("memory search query cannot be empty")
	}
	for _, term := range terms {
		if utf8.RuneCountInString(term) < 3 {
			return s.searchMemoryLike(ctx, agentID, address, terms, limit)
		}
	}

	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		quoted = append(quoted, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.session_key, e.seq, e.role, e.content,
			snippet(memory_fts, 0, '[', ']', '…', 32), e.created_at
		FROM memory_fts
		JOIN memory_entries AS e ON e.id = memory_fts.rowid
		JOIN sessions AS ss
			ON ss.agent_id = e.agent_id AND ss.session_key = e.session_key
		WHERE memory_fts MATCH ?
			AND e.agent_id = ?
			AND ss.channel = ? AND ss.account_id = ?
			AND ss.chat_id = ? AND ss.thread_id = ?
		ORDER BY bm25(memory_fts), e.created_at DESC
		LIMIT ?`,
		strings.Join(quoted, " AND "),
		agentID,
		address.Channel,
		address.AccountID,
		address.ChatID,
		address.ThreadID,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("search memory fts: %w", err)
	}
	defer rows.Close()
	return scanMemoryResults(rows)
}

func (s *SQLiteStore) searchMemoryLike(
	ctx context.Context,
	agentID string,
	address bus.ConversationAddress,
	terms []string,
	limit int,
) ([]MemorySearchResult, error) {
	var statement strings.Builder
	statement.WriteString(`
		SELECT e.session_key, e.seq, e.role, e.content, e.content, e.created_at
		FROM memory_entries AS e
		JOIN sessions AS ss
			ON ss.agent_id = e.agent_id AND ss.session_key = e.session_key
		WHERE e.agent_id = ?
			AND ss.channel = ? AND ss.account_id = ?
			AND ss.chat_id = ? AND ss.thread_id = ?`)
	arguments := []any{
		agentID,
		address.Channel,
		address.AccountID,
		address.ChatID,
		address.ThreadID,
	}
	for _, term := range terms {
		statement.WriteString(` AND e.content LIKE ? ESCAPE '\'`)
		arguments = append(arguments, "%"+escapeLikeTerm(term)+"%")
	}
	statement.WriteString(` ORDER BY e.created_at DESC LIMIT ?`)
	arguments = append(arguments, limit)
	rows, err := s.db.QueryContext(ctx, statement.String(), arguments...)
	if err != nil {
		return nil, fmt.Errorf("search memory fallback: %w", err)
	}
	defer rows.Close()
	return scanMemoryResults(rows)
}

func scanMemoryResults(rows *sql.Rows) ([]MemorySearchResult, error) {
	results := make([]MemorySearchResult, 0)
	for rows.Next() {
		var result MemorySearchResult
		var createdAt int64
		if err := rows.Scan(
			&result.SessionKey,
			&result.Sequence,
			&result.Role,
			&result.Content,
			&result.Snippet,
			&createdAt,
		); err != nil {
			return nil, fmt.Errorf("scan memory result: %w", err)
		}
		result.CreatedAt = time.UnixMilli(createdAt)
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memory results: %w", err)
	}
	return results, nil
}

func escapeLikeTerm(term string) string {
	term = strings.ReplaceAll(term, `\`, `\\`)
	term = strings.ReplaceAll(term, `%`, `\%`)
	return strings.ReplaceAll(term, `_`, `\_`)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRecord(row rowScanner) (Record, bool, error) {
	var record Record
	var messagesJSON string
	if err := row.Scan(
		&record.Key,
		&record.Address.Channel,
		&record.Address.AccountID,
		&record.Address.ChatID,
		&record.Address.ThreadID,
		&record.ModelRef,
		&messagesJSON,
		&record.Summary,
		&record.CompactedUntil,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Record{}, false, nil
		}
		return Record{}, false, fmt.Errorf("scan session: %w", err)
	}
	if err := json.Unmarshal([]byte(messagesJSON), &record.Messages); err != nil {
		return Record{}, false, fmt.Errorf("decode session %q messages: %w", record.Key, err)
	}
	if record.Messages == nil {
		record.Messages = []provider.Message{}
	}
	if record.CompactedUntil < 0 || record.CompactedUntil > len(record.Messages) {
		return Record{}, false, fmt.Errorf("decode session %q: compacted position %d exceeds %d messages", record.Key, record.CompactedUntil, len(record.Messages))
	}
	if record.CompactedUntil > 0 && strings.TrimSpace(record.Summary) == "" {
		return Record{}, false, fmt.Errorf("decode session %q: compacted summary is empty", record.Key)
	}
	return record, true, nil
}

func ensureSessionColumn(ctx context.Context, tx *sql.Tx, column, alterStatement string) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(sessions)`)
	if err != nil {
		return fmt.Errorf("inspect sessions columns: %w", err)
	}
	found := false
	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue sql.NullString
		var primaryKey int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan sessions columns: %w", err)
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close sessions columns: %w", err)
	}
	if found {
		return nil
	}
	if _, err := tx.ExecContext(ctx, alterStatement); err != nil {
		return fmt.Errorf("add sessions column %q: %w", column, err)
	}
	return nil
}

type memoryBackfillSession struct {
	agentID   string
	key       string
	messages  []provider.Message
	createdAt int64
}

func backfillMemoryEntries(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT agent_id, session_key, messages_json, created_at FROM sessions`)
	if err != nil {
		return fmt.Errorf("load sessions for memory backfill: %w", err)
	}
	var sessions []memoryBackfillSession
	for rows.Next() {
		var item memoryBackfillSession
		var messagesJSON string
		if err := rows.Scan(&item.agentID, &item.key, &messagesJSON, &item.createdAt); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan session memory backfill: %w", err)
		}
		if err := json.Unmarshal([]byte(messagesJSON), &item.messages); err != nil {
			_ = rows.Close()
			return fmt.Errorf("decode session %q for memory backfill: %w", item.key, err)
		}
		sessions = append(sessions, item)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close session memory backfill: %w", err)
	}
	for _, item := range sessions {
		if err := insertMemoryEntries(ctx, tx, item.agentID, item.key, 0, item.messages, item.createdAt, true); err != nil {
			return fmt.Errorf("backfill session %q memory: %w", item.key, err)
		}
	}
	return nil
}

func insertMemoryEntries(
	ctx context.Context,
	tx *sql.Tx,
	agentID string,
	key string,
	startSequence int,
	messages []provider.Message,
	createdAt int64,
	ignoreExisting bool,
) error {
	insert := `INSERT INTO memory_entries (
		agent_id, session_key, seq, role, content, created_at
	) VALUES (?, ?, ?, ?, ?, ?)`
	if ignoreExisting {
		insert = `INSERT OR IGNORE INTO memory_entries (
			agent_id, session_key, seq, role, content, created_at
		) VALUES (?, ?, ?, ?, ?, ?)`
	}
	for offset, message := range messages {
		content, searchable := searchableMemoryContent(message)
		if !searchable {
			continue
		}
		sequence := startSequence + offset
		if _, err := tx.ExecContext(ctx, insert,
			agentID, key, sequence, message.Role, content, createdAt+int64(offset)); err != nil {
			return err
		}
	}
	return nil
}

func searchableMemoryContent(message provider.Message) (string, bool) {
	if message.Role != "user" && message.Role != "assistant" && message.Role != "tool" {
		return "", false
	}
	if message.Role == "tool" && message.Name == "memory_search" {
		return "", false
	}
	var content strings.Builder
	content.WriteString(strings.TrimSpace(message.Content))
	for _, call := range message.ToolCalls {
		if call.Function.Name == "memory_search" {
			continue
		}
		if content.Len() > 0 {
			content.WriteString("\n")
		}
		content.WriteString(call.Function.Name)
		content.WriteString(" ")
		content.WriteString(call.Function.Arguments)
	}
	text := strings.TrimSpace(content.String())
	return text, text != ""
}

func marshalMessages(messages []provider.Message) (string, error) {
	if messages == nil {
		messages = []provider.Message{}
	}
	payload, err := json.Marshal(messages)
	if err != nil {
		return "", fmt.Errorf("encode session messages: %w", err)
	}
	return string(payload), nil
}

func requireUpdated(result sql.Result, key string) error {
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read update result for session %q: %w", key, err)
	}
	if count == 0 {
		return fmt.Errorf("%w: %s", errSessionNotFound, key)
	}
	return nil
}
