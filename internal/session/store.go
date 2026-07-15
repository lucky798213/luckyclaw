package session

import (
	"context"

	"github.com/lucky798213/luckyclaw/internal/bus"
	"github.com/lucky798213/luckyclaw/internal/provider"
)

// Record 是会话在持久化层中的完整快照。
type Record struct {
	Key      string
	Address  bus.ConversationAddress
	ModelRef string
	Messages []provider.Message
}

// Store 定义会话管理器需要的最小持久化能力。
type Store interface {
	CreateAndActivate(ctx context.Context, agentID string, record Record) error
	LoadActive(ctx context.Context, agentID string, address bus.ConversationAddress) (Record, bool, error)
	LoadByKey(ctx context.Context, agentID, key string) (Record, bool, error)
	UpdateMessages(ctx context.Context, agentID, key string, messages []provider.Message) error
	UpdateModelRef(ctx context.Context, agentID, key, modelRef string) error
	Close() error
}
