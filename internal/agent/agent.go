// Package agent 定义 LuckyClaw 智能体。
package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/lucky798213/luckyclaw/internal/bus"
	"github.com/lucky798213/luckyclaw/internal/provider"
	"github.com/lucky798213/luckyclaw/internal/session"
)

const handleMessageErrorReply = "抱歉，消息处理失败，请稍后重试。"

// Options 保存创建 Agent 所需的模型和身份配置。
type Options struct {
	ID           string
	Name         string
	DefaultModel string
	Models       []string
	SoulPath     string
	MaxTokens    int
	Temperature  float64
	SessionStore session.Store
}

// Agent 表示一个可以在模型白名单中选择模型的大模型智能体。
type Agent struct {
	id              string
	name            string
	defaultModel    string
	allowedModels   []string
	allowedModelSet map[string]struct{}
	providers       *provider.Manager
	sessionsManager *session.Manager
	soulPath        string
	maxTokens       int
	temperature     float64
}

type userVisibleError struct {
	message string
}

func (e *userVisibleError) Error() string { return e.message }

// New 创建一个支持多个模型的 Agent。
func New(options Options, providers *provider.Manager) (*Agent, error) {
	if strings.TrimSpace(options.ID) == "" {
		return nil, fmt.Errorf("agent id cannot be empty")
	}
	if strings.TrimSpace(options.Name) == "" {
		return nil, fmt.Errorf("agent name cannot be empty")
	}
	if providers == nil {
		return nil, fmt.Errorf("provider manager cannot be nil")
	}
	if strings.TrimSpace(options.SoulPath) == "" {
		return nil, fmt.Errorf("soul path cannot be empty")
	}
	if len(options.Models) == 0 {
		return nil, fmt.Errorf("agent models cannot be empty")
	}

	allowed := make([]string, 0, len(options.Models))
	allowedSet := make(map[string]struct{}, len(options.Models))
	for _, raw := range options.Models {
		ref, err := provider.ParseModelRef(raw)
		if err != nil {
			return nil, err
		}
		normalized := ref.String()
		if _, duplicate := allowedSet[normalized]; duplicate {
			return nil, fmt.Errorf("agent model %q is duplicated", normalized)
		}
		if _, err := providers.Resolve(normalized); err != nil {
			return nil, fmt.Errorf("agent model %q: %w", normalized, err)
		}
		allowedSet[normalized] = struct{}{}
		allowed = append(allowed, normalized)
	}

	defaultRef, err := provider.ParseModelRef(options.DefaultModel)
	if err != nil {
		return nil, fmt.Errorf("default model: %w", err)
	}
	defaultModel := defaultRef.String()
	if _, exists := allowedSet[defaultModel]; !exists {
		return nil, fmt.Errorf("default model %q is not allowed", defaultModel)
	}

	maxTokens := options.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	temperature := options.Temperature
	if temperature == 0 {
		temperature = 0.7
	}

	return &Agent{
		id:              options.ID,
		name:            options.Name,
		defaultModel:    defaultModel,
		allowedModels:   allowed,
		allowedModelSet: allowedSet,
		providers:       providers,
		sessionsManager: session.NewManager(options.ID, options.SessionStore),
		soulPath:        options.SoulPath,
		maxTokens:       maxTokens,
		temperature:     temperature,
	}, nil
}

// HandleMessage 处理统一入站消息，并返回可以直接发送给平台的文本。
func (a *Agent) HandleMessage(ctx context.Context, msg bus.InboundMessage) string {
	address := msg.Address()
	trimmed := strings.TrimSpace(msg.Text)
	if trimmed == "/new" {
		newSession, err := a.sessionsManager.NewSession(ctx, address)
		if err != nil {
			log.Printf("Agent %s 创建新会话失败: %v", a.id, err)
			return handleMessageErrorReply
		}
		return fmt.Sprintf("新会话已开始: %s", newSession.Key())
	}
	if fields := strings.Fields(trimmed); len(fields) > 0 && fields[0] == "/model" {
		return a.handleModelCommand(ctx, address, fields[1:])
	}

	reply, err := a.handleMessage(ctx, msg)
	if err != nil {
		var visible *userVisibleError
		if errors.As(err, &visible) {
			return visible.Error()
		}
		log.Printf("Agent %s 处理消息失败: %v", a.id, err)
		return handleMessageErrorReply
	}
	return reply.Content
}

func (a *Agent) handleModelCommand(ctx context.Context, address bus.ConversationAddress, args []string) string {
	currentSession, err := a.sessionsManager.CurrentSession(ctx, address)
	if err != nil {
		log.Printf("Agent %s 加载会话失败: %v", a.id, err)
		return handleMessageErrorReply
	}
	if len(args) == 0 || (len(args) == 1 && args[0] == "list") {
		current := currentSession.ModelRef()
		if current == "" {
			current = a.defaultModel
		}
		models := append([]string(nil), a.allowedModels...)
		sort.Strings(models)
		return fmt.Sprintf("当前模型: %s\n默认模型: %s\n可用模型:\n- %s", current, a.defaultModel, strings.Join(models, "\n- "))
	}
	if len(args) != 1 {
		return "用法: /model、/model list、/model <provider/model> 或 /model default"
	}
	if args[0] == "default" {
		if err := currentSession.ClearModelRef(ctx); err != nil {
			log.Printf("Agent %s 清除会话模型失败: %v", a.id, err)
			return handleMessageErrorReply
		}
		return fmt.Sprintf("已恢复默认模型: %s", a.defaultModel)
	}
	modelRef, err := a.validateAllowedModel(args[0])
	if err != nil {
		return err.Error()
	}
	if err := currentSession.SetModelRef(ctx, modelRef); err != nil {
		log.Printf("Agent %s 保存会话模型失败: %v", a.id, err)
		return handleMessageErrorReply
	}
	return fmt.Sprintf("当前会话模型已切换为: %s", modelRef)
}

// handleMessage 执行一次模型调用，并只在成功后保存本轮上下文。
func (a *Agent) handleMessage(ctx context.Context, msg bus.InboundMessage) (*provider.Message, error) {
	soul, err := os.ReadFile(a.soulPath)
	if err != nil {
		return nil, fmt.Errorf("read soul: %w", err)
	}

	currentSession, err := a.sessionsManager.CurrentSession(ctx, msg.Address())
	if err != nil {
		return nil, fmt.Errorf("load session: %w", err)
	}
	selectedModel := msg.ModelRef
	if selectedModel == "" {
		selectedModel = currentSession.ModelRef()
	}
	if selectedModel == "" {
		selectedModel = a.defaultModel
	}
	selectedModel, err = a.validateAllowedModel(selectedModel)
	if err != nil {
		return nil, &userVisibleError{message: err.Error()}
	}
	resolved, err := a.providers.Resolve(selectedModel)
	if err != nil {
		return nil, &userVisibleError{message: fmt.Sprintf("模型 %s 当前不可用: %v", selectedModel, err)}
	}

	userMessage := provider.Message{Role: "user", Content: msg.Text}
	history := currentSession.Messages()
	messages := make([]provider.Message, 0, len(history)+2)
	messages = append(messages, provider.Message{Role: "system", Content: string(soul)})
	messages = append(messages, history...)
	messages = append(messages, userMessage)

	assistantMessage, err := resolved.Provider.Chat(
		ctx,
		messages,
		nil,
		resolved.ModelID,
		a.maxTokens,
		a.temperature,
	)
	if err != nil {
		return nil, err
	}

	if err := currentSession.Append(ctx, userMessage, *assistantMessage); err != nil {
		return nil, fmt.Errorf("save session messages: %w", err)
	}
	return assistantMessage, nil
}

func (a *Agent) validateAllowedModel(raw string) (string, error) {
	ref, err := provider.ParseModelRef(raw)
	if err != nil {
		return "", fmt.Errorf("模型选择无效: %v", err)
	}
	modelRef := ref.String()
	if _, exists := a.allowedModelSet[modelRef]; !exists {
		return "", fmt.Errorf("Agent %s 不允许使用模型 %s", a.name, modelRef)
	}
	if _, err := a.providers.Resolve(modelRef); err != nil {
		return "", fmt.Errorf("模型 %s 当前不可用: %v", modelRef, err)
	}
	return modelRef, nil
}

// ID 返回 Agent 的稳定标识。
func (a *Agent) ID() string { return a.id }

// Name 返回 Agent 的显示名称。
func (a *Agent) Name() string { return a.name }

// Model 返回 Agent 的默认模型。
func (a *Agent) Model() string { return a.defaultModel }

// Models 返回 Agent 模型白名单的副本。
func (a *Agent) Models() []string {
	return append([]string(nil), a.allowedModels...)
}
