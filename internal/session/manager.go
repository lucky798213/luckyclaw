// Package session 管理对话会话。
package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"lukcyclaw/internal/bus"
	"lukcyclaw/internal/provider"
)

// Manager 管理 Agent 的所有会话。
type Manager struct {
	mu         sync.RWMutex
	sessions   map[string]*Session
	activeKeys map[bus.ConversationAddress]string
}

// Session 保存一段独立的会话历史。
// Message 中只存 user 和模型回答
type Session struct {
	mu       sync.RWMutex
	key      string
	address  bus.ConversationAddress
	modelRef string
	messages []provider.Message
}

// NewManager 创建一个内存会话管理器。
func NewManager() *Manager {
	return &Manager{
		sessions:   make(map[string]*Session),
		activeKeys: make(map[bus.ConversationAddress]string),
	}
}

// NewSession 为指定平台会话创建并切换到一段新的上下文。
func (m *Manager) NewSession(address bus.ConversationAddress) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	for {
		key := generateKey()
		if _, exists := m.sessions[key]; exists {
			continue
		}

		s := &Session{
			key:     key,
			address: address,
		}
		m.sessions[key] = s
		m.activeKeys[address] = key
		return s
	}
}

// CurrentSession 返回指定平台会话的当前上下文；不存在时自动创建。
func (m *Manager) CurrentSession(address bus.ConversationAddress) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	if activeKey, ok := m.activeKeys[address]; ok {
		if current, exists := m.sessions[activeKey]; exists {
			return current
		}
	}

	key := generateKey()
	s := &Session{
		key:     key,
		address: address,
	}
	m.sessions[key] = s
	m.activeKeys[address] = key
	return s
}

// Get 根据 Key 查找会话。
func (m *Manager) Get(key string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[key]
	return s, ok
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

// ModelRef 返回当前会话选择的模型；空字符串表示使用 Agent 默认模型。
func (s *Session) ModelRef() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.modelRef
}

// SetModelRef 设置当前会话后续消息使用的模型。
func (s *Session) SetModelRef(modelRef string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modelRef = modelRef
}

// ClearModelRef 清除会话模型覆盖，使后续消息恢复 Agent 默认模型。
func (s *Session) ClearModelRef() {
	s.SetModelRef("")
}

// Append 追加会话消息。
func (s *Session) Append(messages ...provider.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, messages...)
}

// generateKey 生成会话 Key，例如 s-1752393600000-a1b2c3d4。
func generateKey() string {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("s-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("s-%d-%s", time.Now().UnixMilli(), hex.EncodeToString(random))
}
