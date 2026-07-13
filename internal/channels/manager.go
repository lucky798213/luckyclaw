package channels

import (
	"context"
	"fmt"
	"log"
	"sync"

	"lukcyclaw/internal/bus"
)

// Manager 管理所有平台实例，并按照平台和账号分发出站消息。
type Manager struct {
	mu       sync.RWMutex
	channels map[string]Channel
	bus      *bus.MessageBus
}

// NewManager 创建渠道管理器。
func NewManager(messageBus *bus.MessageBus) (*Manager, error) {
	if messageBus == nil {
		return nil, fmt.Errorf("message bus cannot be nil")
	}
	return &Manager{
		channels: make(map[string]Channel),
		bus:      messageBus,
	}, nil
}

// Register 注册一个平台账号。同一个平台账号只保留最后注册的实例。
func (m *Manager) Register(channel Channel) error {
	if channel == nil {
		return fmt.Errorf("channel cannot be nil")
	}
	if channel.Name() == "" {
		return fmt.Errorf("channel name cannot be empty")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.channels[channelKey(channel.Name(), channel.AccountID())] = channel
	return nil
}

// Start 启动所有平台监听和统一出站分发。该方法不会阻塞调用方。
func (m *Manager) Start(ctx context.Context) {
	go m.routeOutbound(ctx)

	m.mu.RLock()
	registered := make([]Channel, 0, len(m.channels))
	for _, channel := range m.channels {
		registered = append(registered, channel)
	}
	m.mu.RUnlock()

	for _, channel := range registered {
		go func(ch Channel) {
			if err := ch.Start(ctx); err != nil && ctx.Err() == nil {
				log.Printf("渠道 %s:%s 已停止: %v", ch.Name(), ch.AccountID(), err)
			}
		}(channel)
	}
}

// routeOutbound 持续读取统一出站消息，并交给对应的平台实例。
func (m *Manager) routeOutbound(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-m.bus.Outbound:
			m.mu.RLock()
			channel, ok := m.channels[channelKey(msg.Channel, msg.AccountID)]
			m.mu.RUnlock()
			if !ok {
				log.Printf("没有找到出站渠道: %s:%s", msg.Channel, msg.AccountID)
				continue
			}
			if err := channel.SendMessage(msg); err != nil {
				log.Printf("渠道 %s:%s 发送失败: %v", msg.Channel, msg.AccountID, err)
			}
		}
	}
}

func channelKey(channel, accountID string) string {
	return channel + "\x00" + accountID
}
