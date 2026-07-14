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
	mu         sync.RWMutex
	sessions   map[string]*Session
	activeKeys map[string]string
}

// Session 保存一段独立的会话历史。
// Message 中只存 user 和模型回答
type Session struct {
	mu        sync.RWMutex
	key       string
	channel   string
	accountID string
	chatID    string
	modelRef  string
	messages  []provider.Message
}

// NewManager 创建一个内存会话管理器。
func NewManager() *Manager {
	return &Manager{
		sessions:   make(map[string]*Session),
		activeKeys: make(map[string]string),
	}
}

// NewSession 为指定平台会话创建并切换到一段新的上下文。
func (m *Manager) NewSession(channel, accountID, chatID string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	conversation := conversationKey(channel, accountID, chatID)
	for {
		key := generateKey()
		if _, exists := m.sessions[key]; exists {
			continue
		}

		s := &Session{
			key:       key,
			channel:   channel,
			accountID: accountID,
			chatID:    chatID,
		}
		m.sessions[key] = s
		m.activeKeys[conversation] = key
		return s
	}
}

// CurrentSession 返回指定平台会话的当前上下文；不存在时自动创建。
func (m *Manager) CurrentSession(channel, accountID, chatID string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	conversation := conversationKey(channel, accountID, chatID)
	if activeKey, ok := m.activeKeys[conversation]; ok {
		if current, exists := m.sessions[activeKey]; exists {
			return current
		}
	}

	key := generateKey()
	s := &Session{
		key:       key,
		channel:   channel,
		accountID: accountID,
		chatID:    chatID,
	}
	m.sessions[key] = s
	m.activeKeys[conversation] = key
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

// AccountID 返回接收消息的机器人账号标识。
func (s *Session) AccountID() string {
	return s.accountID
}

// ChatID 返回平台侧的聊天标识。
func (s *Session) ChatID() string {
	return s.chatID
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

func conversationKey(channel, accountID, chatID string) string {
	return channel + "\x00" + accountID + "\x00" + chatID
}

// generateKey 生成会话 Key，例如 s-1752393600000-a1b2c3d4。
func generateKey() string {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("s-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("s-%d-%s", time.Now().UnixMilli(), hex.EncodeToString(random))
}
