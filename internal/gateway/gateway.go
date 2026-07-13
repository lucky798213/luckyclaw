// Package gateway 负责把统一入站消息交给 Agent，并生成统一出站消息。
package gateway

import (
	"context"
	"fmt"
	"time"

	"lukcyclaw/internal/bus"
)

const defaultMessageTimeout = 30 * time.Second

// MessageHandler 是 Gateway 对 Agent 的最小依赖。
type MessageHandler interface {
	HandleMessage(ctx context.Context, msg bus.InboundMessage) string
}

// Gateway 是平台渠道和 Agent 之间的统一消息入口。
type Gateway struct {
	bus            *bus.MessageBus
	handler        MessageHandler
	messageTimeout time.Duration
}

// New 创建一个只路由到单个 Agent 的最小网关。
func New(messageBus *bus.MessageBus, handler MessageHandler) (*Gateway, error) {
	if messageBus == nil {
		return nil, fmt.Errorf("message bus cannot be nil")
	}
	if handler == nil {
		return nil, fmt.Errorf("message handler cannot be nil")
	}
	return &Gateway{
		bus:            messageBus,
		handler:        handler,
		messageTimeout: defaultMessageTimeout,
	}, nil
}

// Run 串行消费入站消息。后续需要并发时，可以在这里增加按会话分组的任务队列。
func (g *Gateway) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-g.bus.Inbound:
			g.handleInbound(ctx, msg)
		}
	}
}

func (g *Gateway) handleInbound(ctx context.Context, msg bus.InboundMessage) {
	turnCtx, cancel := context.WithTimeout(ctx, g.messageTimeout)
	reply := g.handler.HandleMessage(turnCtx, msg)
	cancel()
	if reply == "" {
		return
	}

	out := bus.OutboundMessage{
		Channel:      msg.Channel,
		AccountID:    msg.AccountID,
		ChatID:       msg.ChatID,
		Text:         reply,
		ReplyToMsgID: msg.MessageID,
	}
	select {
	case g.bus.Outbound <- out:
	case <-ctx.Done():
	}
}
