// Package gateway 负责把统一入站消息交给正确的 Agent，并生成统一出站消息。
package gateway

import (
	"context"
	"fmt"
	"time"

	"lukcyclaw/internal/agent"
	"lukcyclaw/internal/bus"
	"lukcyclaw/internal/config"
)

const defaultMessageTimeout = 30 * time.Second

// Gateway 是平台渠道和多个 Agent 之间的统一消息入口。
type Gateway struct {
	bus             *bus.MessageBus
	agents          *agent.Manager
	exactBindings   map[string]string
	accountBindings map[string]string
	messageTimeout  time.Duration
}

// New 创建一个支持显式 Agent、聊天绑定、账号绑定和默认 Agent 的网关。
func New(messageBus *bus.MessageBus, agents *agent.Manager, bindings []config.BindingConfig) (*Gateway, error) {
	if messageBus == nil {
		return nil, fmt.Errorf("message bus cannot be nil")
	}
	if agents == nil {
		return nil, fmt.Errorf("agent manager cannot be nil")
	}
	exactBindings := make(map[string]string)
	accountBindings := make(map[string]string)
	for index, binding := range bindings {
		//找 agent_id
		if agents.AgentByID(binding.AgentID) == nil {
			return nil, fmt.Errorf("binding %d references unknown agent %q", index, binding.AgentID)
		}

		//确保这个 agent具体的用户绑定了
		if binding.Channel == "" || binding.AccountID == "" {
			return nil, fmt.Errorf("binding %d requires channel and account_id", index)
		}

		if binding.ChatID == "" {
			key := accountBindingKey(binding.Channel, binding.AccountID)
			if _, duplicate := accountBindings[key]; duplicate {
				return nil, fmt.Errorf("binding %d duplicates channel/account", index)
			}
			accountBindings[key] = binding.AgentID
			continue
		}
		key := exactBindingKey(binding.Channel, binding.AccountID, binding.ChatID)
		if _, duplicate := exactBindings[key]; duplicate {
			return nil, fmt.Errorf("binding %d duplicates channel/account/chat", index)
		}
		exactBindings[key] = binding.AgentID
	}
	return &Gateway{
		bus:             messageBus,
		agents:          agents,
		exactBindings:   exactBindings,
		accountBindings: accountBindings,
		messageTimeout:  defaultMessageTimeout,
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
	//匹配 agent
	target, err := g.matchAgent(msg)
	var reply string
	if err != nil {
		reply = err.Error()
	} else {
		turnCtx, cancel := context.WithTimeout(ctx, g.messageTimeout)
		reply = target.HandleMessage(turnCtx, msg)
		cancel()
	}
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

func (g *Gateway) matchAgent(msg bus.InboundMessage) (*agent.Agent, error) {
	if msg.AgentID != "" {
		if target := g.agents.AgentByID(msg.AgentID); target != nil {
			return target, nil
		}
		return nil, fmt.Errorf("没有找到指定的 Agent: %s", msg.AgentID)
	}
	if agentID, exists := g.exactBindings[exactBindingKey(msg.Channel, msg.AccountID, msg.ChatID)]; exists {
		return g.agents.AgentByID(agentID), nil
	}
	if agentID, exists := g.accountBindings[accountBindingKey(msg.Channel, msg.AccountID)]; exists {
		return g.agents.AgentByID(agentID), nil
	}
	if target := g.agents.DefaultAgent(); target != nil {
		return target, nil
	}
	return nil, fmt.Errorf("没有可用的默认 Agent")
}

func exactBindingKey(channel, accountID, chatID string) string {
	return channel + "\x00" + accountID + "\x00" + chatID
}

func accountBindingKey(channel, accountID string) string {
	return channel + "\x00" + accountID
}
