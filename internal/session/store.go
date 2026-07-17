package session

import (
	"context"
	"errors"
	"time"

	"github.com/lucky798213/luckyclaw/internal/bus"
	"github.com/lucky798213/luckyclaw/internal/provider"
)

// ErrCompactionConflict 表示压缩期间会话摘要已被其他请求推进。
var ErrCompactionConflict = errors.New("session compaction position changed")

// Record 是会话在持久化层中的完整快照。
type Record struct {
	Key            string
	Address        bus.ConversationAddress
	ModelRef       string
	Messages       []provider.Message
	Summary        string
	CompactedUntil int
}

// Summary 是侧边栏展示会话时使用的轻量快照。
type Summary struct {
	Key       string
	Address   bus.ConversationAddress
	ModelRef  string
	Messages  []provider.Message
	CreatedAt time.Time
	UpdatedAt time.Time
}

// MemorySearchResult 是一条限定会话地址的长期记忆检索结果。
type MemorySearchResult struct {
	SessionKey string
	Sequence   int
	Role       string
	Content    string
	Snippet    string
	CreatedAt  time.Time
}

// Store 定义会话管理器需要的最小持久化能力。
type Store interface {
	CreateAndActivate(ctx context.Context, agentID string, record Record) error
	LoadActive(ctx context.Context, agentID string, address bus.ConversationAddress) (Record, bool, error)
	LoadByKey(ctx context.Context, agentID, key string) (Record, bool, error)
	UpdateMessages(ctx context.Context, agentID, key string, messages []provider.Message) error
	UpdateModelRef(ctx context.Context, agentID, key, modelRef string) error
	UpdateCompaction(ctx context.Context, agentID, key string, expectedUntil int, summary string, compactedUntil int) error
	SearchMemory(ctx context.Context, agentID string, address bus.ConversationAddress, query string, limit int) ([]MemorySearchResult, error)
	Close() error
}
