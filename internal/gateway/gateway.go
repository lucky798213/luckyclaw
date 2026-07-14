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
	threadBindings  map[bus.ConversationAddress]string
	chatBindings    map[bus.ConversationAddress]string
	accountBindings map[bus.ChannelAccount]string
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
	threadBindings := make(map[bus.ConversationAddress]string)
	chatBindings := make(map[bus.ConversationAddress]string)
	accountBindings := make(map[bus.ChannelAccount]string)
	for index, binding := range bindings {
		// 确保绑定引用的 Agent 已经加载。
		if agents.AgentByID(binding.AgentID) == nil {
			return nil, fmt.Errorf("binding %d references unknown agent %q", index, binding.AgentID)
		}

		// 平台和机器人账号共同决定消息属于哪个渠道实例。
		if binding.Channel == "" || binding.AccountID == "" {
			return nil, fmt.Errorf("binding %d requires channel and account_id", index)
		}
		if binding.ThreadID != "" && binding.ChatID == "" {
			return nil, fmt.Errorf("binding %d thread_id requires chat_id", index)
		}

		if binding.ChatID == "" {
			key := bus.ChannelAccount{Channel: binding.Channel, AccountID: binding.AccountID}
			if _, duplicate := accountBindings[key]; duplicate {
				return nil, fmt.Errorf("binding %d duplicates channel/account", index)
			}
			accountBindings[key] = binding.AgentID
			continue
		}

		key := bus.ConversationAddress{
			Channel:   binding.Channel,
			AccountID: binding.AccountID,
			ChatID:    binding.ChatID,
			ThreadID:  binding.ThreadID,
		}
		if binding.ThreadID != "" {
			if _, duplicate := threadBindings[key]; duplicate {
				return nil, fmt.Errorf("binding %d duplicates channel/account/chat/thread", index)
			}
			threadBindings[key] = binding.AgentID
			continue
		}
		if _, duplicate := chatBindings[key]; duplicate {
			return nil, fmt.Errorf("binding %d duplicates channel/account/chat", index)
		}
		chatBindings[key] = binding.AgentID
	}
	return &Gateway{
		bus:             messageBus,
		agents:          agents,
		threadBindings:  threadBindings,
		chatBindings:    chatBindings,
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
		ThreadID:     msg.ThreadID,
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
	address := msg.Address()
	if msg.ThreadID != "" {
		if agentID, exists := g.threadBindings[address]; exists {
			return g.agents.AgentByID(agentID), nil
		}
	}
	address.ThreadID = ""
	if agentID, exists := g.chatBindings[address]; exists {
		return g.agents.AgentByID(agentID), nil
	}
	if agentID, exists := g.accountBindings[address.Account()]; exists {
		return g.agents.AgentByID(agentID), nil
	}
	if target := g.agents.DefaultAgent(); target != nil {
		return target, nil
	}
	return nil, fmt.Errorf("没有可用的默认 Agent")
}
