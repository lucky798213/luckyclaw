// Package agent 定义 LuckyClaw 智能体。
package agent

import (
	"context"
	"fmt"
	"os"

	"lukcyclaw/internal/provider"
	"lukcyclaw/internal/session"
)

const terminalChannel = "terminal"

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

// Chat 发送消息，并将成功的对话保存到当前会话。
func (a *Agent) Chat(ctx context.Context, content string) (*provider.Message, error) {
	//读取人设
	soul, err := os.ReadFile(a.soulPath)
	if err != nil {
		return nil, fmt.Errorf("read soul: %w", err)
	}

	//因为要添加Message，真正要修改 session，所以返回一个指针，让下一层同步改变
	currentSession := a.sessionsManager.CurrentSession(terminalChannel)

	//把用户输入变成 Message
	userMessage := provider.Message{Role: "user", Content: content}

	//获取会话历史副本
	history := currentSession.Messages()

	//调换上下文顺序
	messages := make([]provider.Message, 0, len(history)+2)
	messages = append(messages, provider.Message{Role: "system", Content: string(soul)})
	messages = append(messages, history...)
	messages = append(messages, userMessage)

	//通过 provider 向模型发送请求
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

	//只把 userMessage 和 assistantMessage添加到上下文中
	currentSession.Append(userMessage, *assistantMessage)
	return assistantMessage, nil
}

// Reset 创建一个新的终端会话，旧会话仍保留在 Manager 中。
func (a *Agent) Reset() {
	a.sessionsManager.NewSession(terminalChannel)
}

// SessionKey 返回当前终端会话的内部 Key。
func (a *Agent) SessionKey() string {
	return a.sessionsManager.CurrentSession(terminalChannel).Key()
}

// Name 返回 Agent 名称。
func (a *Agent) Name() string {
	return a.name
}

// Model 返回当前使用的模型。
func (a *Agent) Model() string {
	return a.model
}
