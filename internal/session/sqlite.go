package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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

// UpdateMessages 覆盖会话的工作消息集。
func (s *SQLiteStore) UpdateMessages(ctx context.Context, agentID, key string, messages []provider.Message) error {
	payload, err := marshalMessages(messages)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE sessions
		SET messages_json = ?, updated_at = ?
		WHERE agent_id = ? AND session_key = ?`,
		payload, time.Now().UnixMilli(), agentID, key)
	if err != nil {
		return fmt.Errorf("update session messages %q: %w", key, err)
	}
	return requireUpdated(result, key)
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
