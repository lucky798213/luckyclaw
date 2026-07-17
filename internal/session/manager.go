// Package session 管理对话会话。
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lucky798213/luckyclaw/internal/bus"
	"github.com/lucky798213/luckyclaw/internal/provider"
)

// Manager 管理一个 Agent 的所有会话。
type Manager struct {
	mu         sync.RWMutex
	agentID    string
	store      Store
	sessions   map[string]*Session
	activeKeys map[bus.ConversationAddress]string
}

// Session 保存一段独立的会话历史。
// Message 中只存 user 和模型回答。
type Session struct {
	mu             sync.RWMutex
	agentID        string
	store          Store
	key            string
	address        bus.ConversationAddress
	modelRef       string
	messages       []provider.Message
	summary        string
	compactedUntil int
}

// ContextSnapshot 是构造模型上下文所需的完整会话快照。
type ContextSnapshot struct {
	Messages       []provider.Message
	Summary        string
	CompactedUntil int
}

// NewManager 创建一个会话管理器；store 为空时仅在内存中保存，供单元测试使用。
func NewManager(agentID string, store Store) *Manager {
	return &Manager{
		agentID:    agentID,
		store:      store,
		sessions:   make(map[string]*Session),
		activeKeys: make(map[bus.ConversationAddress]string),
	}
}

// NewSession 为指定平台会话创建并切换到一段新的上下文。
func (m *Manager) NewSession(ctx context.Context, address bus.ConversationAddress) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.createSessionLocked(ctx, address)
}

// CurrentSession 返回指定平台会话的当前上下文；不存在时自动创建。
func (m *Manager) CurrentSession(ctx context.Context, address bus.ConversationAddress) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if activeKey, ok := m.activeKeys[address]; ok {
		if current, exists := m.sessions[activeKey]; exists {
			return current, nil
		}
	}

	if m.store != nil {
		record, exists, err := m.store.LoadActive(ctx, m.agentID, address)
		if err != nil {
			return nil, fmt.Errorf("load active session: %w", err)
		}
		if exists {
			current := m.sessionFromRecord(record)
			m.sessions[current.key] = current
			m.activeKeys[address] = current.key
			return current, nil
		}
	}

	return m.createSessionLocked(ctx, address)
}

// Get 根据 Key 查找会话，不存在于内存时会尝试从持久化层加载。
func (m *Manager) Get(ctx context.Context, key string) (*Session, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, ok := m.sessions[key]; ok {
		return current, true, nil
	}
	if m.store == nil {
		return nil, false, nil
	}

	record, exists, err := m.store.LoadByKey(ctx, m.agentID, key)
	if err != nil {
		return nil, false, fmt.Errorf("load session %q: %w", key, err)
	}
	if !exists {
		return nil, false, nil
	}
	current := m.sessionFromRecord(record)
	m.sessions[current.key] = current
	return current, true, nil
}

func (m *Manager) createSessionLocked(ctx context.Context, address bus.ConversationAddress) (*Session, error) {
	for {
		key := generateKey()
		if _, exists := m.sessions[key]; exists {
			continue
		}

		record := Record{Key: key, Address: address, Messages: []provider.Message{}}
		if m.store != nil {
			if err := m.store.CreateAndActivate(ctx, m.agentID, record); err != nil {
				return nil, fmt.Errorf("create session: %w", err)
			}
		}
		current := m.sessionFromRecord(record)
		m.sessions[key] = current
		m.activeKeys[address] = key
		return current, nil
	}
}

func (m *Manager) sessionFromRecord(record Record) *Session {
	return &Session{
		agentID:        m.agentID,
		store:          m.store,
		key:            record.Key,
		address:        record.Address,
		modelRef:       record.ModelRef,
		messages:       append([]provider.Message(nil), record.Messages...),
		summary:        record.Summary,
		compactedUntil: record.CompactedUntil,
	}
}

// Key 返回会话 Key。
func (s *Session) Key() string {
	return s.key
}

// Address 返回当前会话对应的平台会话地址。
func (s *Session) Address() bus.ConversationAddress {
	return s.address
}

// Channel 返回会话来源平台。
func (s *Session) Channel() string {
	return s.address.Channel
}

// AccountID 返回接收消息的机器人账号标识。
func (s *Session) AccountID() string {
	return s.address.AccountID
}

// ChatID 返回平台侧的聊天标识。
func (s *Session) ChatID() string {
	return s.address.ChatID
}

// ThreadID 返回平台侧的线程或话题标识。
func (s *Session) ThreadID() string {
	return s.address.ThreadID
}

// Messages 返回会话消息副本。
func (s *Session) Messages() []provider.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]provider.Message(nil), s.messages...)
}

// ContextSnapshot 返回消息、旧消息摘要和压缩位置的一致副本。
func (s *Session) ContextSnapshot() ContextSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return ContextSnapshot{
		Messages:       append([]provider.Message(nil), s.messages...),
		Summary:        s.summary,
		CompactedUntil: s.compactedUntil,
	}
}

// ApplyCompaction 原子保存新摘要和压缩位置；持久化失败时不修改内存状态。
func (s *Session) ApplyCompaction(ctx context.Context, expectedUntil int, summary string, compactedUntil int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if expectedUntil != s.compactedUntil {
		return ErrCompactionConflict
	}
	if compactedUntil < expectedUntil || compactedUntil > len(s.messages) {
		return fmt.Errorf("invalid compacted position %d for %d messages", compactedUntil, len(s.messages))
	}
	if compactedUntil > 0 && strings.TrimSpace(summary) == "" {
		return fmt.Errorf("compaction summary cannot be empty")
	}
	if s.store != nil {
		if err := s.store.UpdateCompaction(ctx, s.agentID, s.key, expectedUntil, summary, compactedUntil); err != nil {
			return err
		}
	}
	s.summary = summary
	s.compactedUntil = compactedUntil
	return nil
}

// ModelRef 返回当前会话选择的模型；空字符串表示使用 Agent 默认模型。
func (s *Session) ModelRef() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.modelRef
}

// SetModelRef 设置当前会话后续消息使用的模型。
func (s *Session) SetModelRef(ctx context.Context, modelRef string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.modelRef == modelRef {
		return nil
	}
	if s.store != nil {
		if err := s.store.UpdateModelRef(ctx, s.agentID, s.key, modelRef); err != nil {
			return err
		}
	}
	s.modelRef = modelRef
	return nil
}

// ClearModelRef 清除会话模型覆盖，使后续消息恢复 Agent 默认模型。
func (s *Session) ClearModelRef(ctx context.Context) error {
	return s.SetModelRef(ctx, "")
}

// Append 追加会话消息；持久化失败时不修改内存状态。
func (s *Session) Append(ctx context.Context, messages ...provider.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := make([]provider.Message, 0, len(s.messages)+len(messages))
	next = append(next, s.messages...)
	next = append(next, messages...)
	if s.store != nil {
		if err := s.store.UpdateMessages(ctx, s.agentID, s.key, next); err != nil {
			return err
		}
	}
	s.messages = next
	return nil
}

// generateKey 生成会话 Key，例如 s-1752393600000-a1b2c3d4。
func generateKey() string {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("s-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("s-%d-%s", time.Now().UnixMilli(), hex.EncodeToString(random))
}
