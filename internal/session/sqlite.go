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

	"lukcyclaw/internal/bus"
	"lukcyclaw/internal/provider"
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
			model_ref, messages_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		agentID,
		record.Key,
		record.Address.Channel,
		record.Address.AccountID,
		record.Address.ChatID,
		record.Address.ThreadID,
		record.ModelRef,
		messages,
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
			s.model_ref, s.messages_json
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
			model_ref, messages_json
		FROM sessions
		WHERE agent_id = ? AND session_key = ?`, agentID, key)
	return scanRecord(row)
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
	return record, true, nil
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
