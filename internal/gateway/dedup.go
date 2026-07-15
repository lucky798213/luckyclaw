package gateway

import (
	"sync"
	"time"

	"github.com/lucky798213/luckyclaw/internal/bus"
)

const (
	defaultDedupTTL      = 60 * time.Second
	dedupCleanupInterval = 30 * time.Second
)

// messageDedupKey 使用完整会话地址和平台消息 ID 唯一标识一条入站消息。
type messageDedupKey struct {
	Address   bus.ConversationAddress
	MessageID string
}

// messageDeduper 记录已接收消息的过期时间，并发访问时由互斥锁保护。
type messageDeduper struct {
	mu      sync.Mutex
	entries map[messageDedupKey]time.Time
	ttl     time.Duration
	now     func() time.Time
}

// newMessageDeduper 创建指定去重窗口的消息去重器。
func newMessageDeduper(ttl time.Duration) *messageDeduper {
	if ttl <= 0 {
		ttl = defaultDedupTTL
	}
	return &messageDeduper{
		entries: make(map[messageDedupKey]time.Time),
		ttl:     ttl,
		now:     time.Now,
	}
}

// isDuplicate 登记首次出现的消息，并判断当前消息是否仍在去重窗口内。
func (d *messageDeduper) isDuplicate(msg bus.InboundMessage) bool {
	if msg.MessageID == "" {
		return false
	}

	key := messageDedupKey{Address: msg.Address(), MessageID: msg.MessageID}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()

	if expiresAt, exists := d.entries[key]; exists && now.Before(expiresAt) {
		return true
	}
	d.entries[key] = now.Add(d.ttl)
	return false
}

// forget 撤销一条消息的去重登记，使未能入队的消息后续可以重试。
func (d *messageDeduper) forget(msg bus.InboundMessage) {
	if msg.MessageID == "" {
		return
	}

	key := messageDedupKey{Address: msg.Address(), MessageID: msg.MessageID}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.entries, key)
}

// cleanup 删除已经到期的消息记录，避免缓存持续增长。
func (d *messageDeduper) cleanup() {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()

	for key, expiresAt := range d.entries {
		if !now.Before(expiresAt) {
			delete(d.entries, key)
		}
	}
}
