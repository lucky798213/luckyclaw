// Package gateway 负责把统一入站消息交给正确的 Agent，并生成统一出站消息。
package gateway

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/lucky798213/luckyclaw/internal/agent"
	"github.com/lucky798213/luckyclaw/internal/bus"
	"github.com/lucky798213/luckyclaw/internal/config"
	"github.com/lucky798213/luckyclaw/internal/taskqueue"
)

// Gateway 是平台渠道和多个 Agent 之间的统一消息入口。
type Gateway struct {
	bus             *bus.MessageBus
	agents          *agent.Manager
	threadBindings  map[bus.ConversationAddress]string
	chatBindings    map[bus.ConversationAddress]string
	accountBindings map[bus.ChannelAccount]string
	deduper         *messageDeduper
	taskQueue       *taskqueue.Queue
}

// New 使用默认任务队列参数创建支持多级 Agent 绑定的网关。
func New(messageBus *bus.MessageBus, agents *agent.Manager, bindings []config.BindingConfig) (*Gateway, error) {
	return NewWithTaskQueueConfig(messageBus, agents, bindings, config.TaskQueueConfig{})
}

// NewWithTaskQueueConfig 使用指定任务队列参数创建支持多级 Agent 绑定的网关。
func NewWithTaskQueueConfig(
	messageBus *bus.MessageBus,
	agents *agent.Manager,
	bindings []config.BindingConfig,
	taskQueueConfig config.TaskQueueConfig,
) (*Gateway, error) {
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
	gateway := &Gateway{
		bus:             messageBus,
		agents:          agents,
		threadBindings:  threadBindings,
		chatBindings:    chatBindings,
		accountBindings: accountBindings,
		deduper:         newMessageDeduper(defaultDedupTTL),
	}
	effectiveTaskQueueConfig := taskQueueConfig.WithDefaults()
	queue, err := taskqueue.NewQueue(
		effectiveTaskQueueConfig.MaxConcurrent,
		time.Duration(effectiveTaskQueueConfig.TaskTimeoutSeconds)*time.Second,
		effectiveTaskQueueConfig.MaxPendingPerConversation,
		gateway.processInbound,
	)
	if err != nil {
		return nil, fmt.Errorf("create task queue: %w", err)
	}
	gateway.taskQueue = queue
	return gateway, nil
}

// Run 快速消费入站消息，并把 Agent 处理交给按会话分组的任务队列。
func (g *Gateway) Run(ctx context.Context) {
	// 创建定时触发器。
	cleanupTicker := time.NewTicker(dedupCleanupInterval)
	defer cleanupTicker.Stop()
	defer g.taskQueue.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-cleanupTicker.C:
			g.deduper.cleanup()
		case msg := <-g.bus.Inbound:
			g.handleInbound(msg)
		}
	}
}

// handleInbound 在 Gateway 前端完成去重并快速提交任务。
func (g *Gateway) handleInbound(msg bus.InboundMessage) {
	// 阶段一：在进入任务队列前去重，避免平台重试造成重复模型调用和重复写会话。
	if g.deduper.isDuplicate(msg) {
		log.Printf(
			"忽略重复入站消息: channel=%s account_id=%s chat_id=%s thread_id=%s message_id=%s",
			msg.Channel,
			msg.AccountID,
			msg.ChatID,
			msg.ThreadID,
			msg.MessageID,
		)
		return
	}

	// 阶段二：快速入队，不在 Gateway 消费循环里等待模型响应。
	if err := g.taskQueue.Submit(msg); err != nil {
		if errors.Is(err, taskqueue.ErrConversationQueueFull) {
			// 背压拒绝不算真正消费成功，撤销去重记录后平台仍可稍后重试。
			g.deduper.forget(msg)
			log.Printf(
				"会话任务队列已满，拒绝入站消息: channel=%s account_id=%s chat_id=%s thread_id=%s message_id=%s",
				msg.Channel,
				msg.AccountID,
				msg.ChatID,
				msg.ThreadID,
				msg.MessageID,
			)
			return
		}
		log.Printf(
			"提交入站消息失败: channel=%s account_id=%s chat_id=%s thread_id=%s message_id=%s error=%v",
			msg.Channel,
			msg.AccountID,
			msg.ChatID,
			msg.ThreadID,
			msg.MessageID,
			err,
		)
	}
}

// processInbound 在会话队列中匹配 Agent、执行任务并生成出站消息。
func (g *Gateway) processInbound(ctx context.Context, msg bus.InboundMessage) {
	// 阶段一：根据显式选择和多级绑定解析本条消息的目标 Agent。
	target, err := g.matchAgent(msg)
	var reply string
	if err != nil {
		reply = err.Error()
	} else {
		reply = target.HandleMessage(ctx, msg)
	}
	if reply == "" {
		return
	}

	// 阶段二：重新封装为统一出站消息，渠道层不需要理解 Agent 内部协议。
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
	// 优先级一：调用方为本条消息显式指定 Agent。
	if msg.AgentID != "" {
		if target := g.agents.AgentByID(msg.AgentID); target != nil {
			return target, nil
		}
		return nil, fmt.Errorf("没有找到指定的 Agent: %s", msg.AgentID)
	}
	address := msg.Address()
	// 优先级二：线程绑定最精确，可以覆盖同一聊天的默认绑定。
	if msg.ThreadID != "" {
		if agentID, exists := g.threadBindings[address]; exists {
			return g.agents.AgentByID(agentID), nil
		}
	}
	// 优先级三和四：依次回退到聊天绑定、渠道账号绑定。
	address.ThreadID = ""
	if agentID, exists := g.chatBindings[address]; exists {
		return g.agents.AgentByID(agentID), nil
	}
	if agentID, exists := g.accountBindings[address.Account()]; exists {
		return g.agents.AgentByID(agentID), nil
	}
	// 优先级五：没有任何显式规则时使用全局默认 Agent。
	if target := g.agents.DefaultAgent(); target != nil {
		return target, nil
	}
	return nil, fmt.Errorf("没有可用的默认 Agent")
}
