// Package agent 定义 LuckyClaw 智能体。
package agent

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"lukcyclaw/internal/bus"
	"lukcyclaw/internal/provider"
	"lukcyclaw/internal/session"
)

const handleMessageErrorReply = "抱歉，消息处理失败，请稍后重试。"

// Agent 表示一个可以调用大模型的智能体。
type Agent struct {
	name            string
	model           string
	provider        provider.Provider
	sessionsManager *session.Manager
	soulPath        string
	maxTokens       int
	temperature     float64
}

// New 创建一个 Agent。
func New(name string, model string, prov provider.Provider, soulPath string) (*Agent, error) {
	if name == "" {
		return nil, fmt.Errorf("agent name cannot be empty")
	}

	if model == "" {
		return nil, fmt.Errorf("model cannot be empty")
	}

	if prov == nil {
		return nil, fmt.Errorf("provider cannot be nil")
	}

	if soulPath == "" {
		return nil, fmt.Errorf("soul path cannot be empty")
	}

	sessionsManager := session.NewManager()

	return &Agent{
		name:            name,
		model:           model,
		provider:        prov,
		sessionsManager: sessionsManager,
		soulPath:        soulPath,
		maxTokens:       4096,
		temperature:     0.7,
	}, nil
}

// HandleMessage 处理统一入站消息，并返回可以直接发送给平台的文本。
func (a *Agent) HandleMessage(ctx context.Context, msg bus.InboundMessage) string {
	channel, accountID, chatID := msg.SessionTriple()
	if strings.TrimSpace(msg.Text) == "/new" {
		newSession := a.sessionsManager.NewSession(channel, accountID, chatID)
		return fmt.Sprintf("新会话已开始: %s", newSession.Key())
	}

	reply, err := a.handleMessage(ctx, msg)
	if err != nil {
		log.Printf("Agent %s 处理消息失败: %v", a.name, err)
		return handleMessageErrorReply
	}
	return reply.Content
}

// handleMessage 执行一次模型调用，并只在成功后保存本轮上下文。
func (a *Agent) handleMessage(ctx context.Context, msg bus.InboundMessage) (*provider.Message, error) {
	soul, err := os.ReadFile(a.soulPath)
	if err != nil {
		return nil, fmt.Errorf("read soul: %w", err)
	}

	channel, accountID, chatID := msg.SessionTriple()
	currentSession := a.sessionsManager.CurrentSession(channel, accountID, chatID)

	userMessage := provider.Message{Role: "user", Content: msg.Text}

	history := currentSession.Messages()

	messages := make([]provider.Message, 0, len(history)+2)
	messages = append(messages, provider.Message{Role: "system", Content: string(soul)})
	messages = append(messages, history...)
	messages = append(messages, userMessage)

	assistantMessage, err := a.provider.Chat(
		ctx,
		messages,
		nil,
		a.model,
		a.maxTokens,
		a.temperature,
	)
	if err != nil {
		return nil, err
	}

	currentSession.Append(userMessage, *assistantMessage)
	return assistantMessage, nil
}

// Name 返回 Agent 名称。
func (a *Agent) Name() string {
	return a.name
}

// Model 返回当前使用的模型。
func (a *Agent) Model() string {
	return a.model
}
