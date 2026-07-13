// Package session 管理对话会话。
package session

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"lukcyclaw/internal/provider"
)

// Manager 管理 Agent 的所有会话。
type Manager struct {
	mu        sync.RWMutex
	sessions  map[string]*Session
	activeKey string
}

// Session 保存一段独立的会话历史。
// Message 中只存 user 和模型回答
type Session struct {
	mu       sync.RWMutex
	key      string
	channel  string
	messages []provider.Message
}

// NewManager 创建一个内存会话管理器。
func NewManager() *Manager {
	return &Manager{sessions: make(map[string]*Session)}
}

// NewSession 创建并切换到新会话。
func (m *Manager) NewSession(channel string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	for {
		key := generateKey()
		if _, exists := m.sessions[key]; exists {
			continue
		}

		s := &Session{key: key, channel: channel}
		m.sessions[key] = s
		m.activeKey = key
		return s
	}
}

// CurrentSession 返回当前会话；不存在时自动创建。
func (m *Manager) CurrentSession(channel string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	//如果当前的 activeKey 存在就返回对应的会话
	if current, ok := m.sessions[m.activeKey]; ok {
		return current
	}

	//如果当前还没规划，就创建一个新的session，并返回
	key := generateKey()
	s := &Session{key: key, channel: channel}
	m.sessions[key] = s
	m.activeKey = key
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

// Channel 返回会话来源平台。
func (s *Session) Channel() string {
	return s.channel
}

// Messages 返回会话消息副本。
func (s *Session) Messages() []provider.Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]provider.Message(nil), s.messages...)
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
