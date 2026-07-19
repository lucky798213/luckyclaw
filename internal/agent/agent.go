// Package agent 定义 LuckyClaw 智能体。
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lucky798213/luckyclaw/internal/bus"
	"github.com/lucky798213/luckyclaw/internal/provider"
	"github.com/lucky798213/luckyclaw/internal/session"
	"github.com/lucky798213/luckyclaw/internal/tools"
)

const (
	handleMessageErrorReply = "抱歉，消息处理失败，请稍后重试。"
	contextWindowErrorReply = "当前会话上下文过长，无法在模型窗口内安全处理。请缩短单条消息或稍后重试自动压缩。"
)

const (
	defaultMaxToolIterations         = 20
	defaultToolTimeout               = 30 * time.Second
	defaultContextWindowTokens       = 128000
	defaultCompactionThresholdTokens = 80000
	defaultCompactionRecentMessages  = 20
	repeatedToolCallLimit            = 3
	failedToolRoundLimit             = 3
)

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
	Tools        tools.Registry
	// SkillsSummary 是只包含当前 Agent Skill 名称和描述的常驻目录。
	SkillsSummary string
	// MaxToolIterations 限制一次用户请求可以经历的模型工具轮数。
	MaxToolIterations int
	// ToolTimeout 限制单次工具执行时间。
	ToolTimeout time.Duration
	// ContextWindowTokens 是模型上下文窗口的总 token 数。
	ContextWindowTokens int
	// CompactionThresholdTokens 是触发历史压缩的预算阈值。
	CompactionThresholdTokens int
	// CompactionRecentMessages 是压缩时优先保留的近期消息数量。
	CompactionRecentMessages int
}

// Agent 表示一个可以在模型白名单中选择模型的大模型智能体。
type Agent struct {
	id                string
	name              string
	defaultModel      string
	allowedModels     []string
	allowedModelSet   map[string]struct{}
	providers         *provider.Manager
	sessionsManager   *session.Manager
	soulPath          string
	soulMu            sync.RWMutex
	maxTokens         int
	temperature       float64
	tools             tools.Registry
	skillsSummary     string
	maxToolIterations int
	toolTimeout       time.Duration
	tokenBudget       tokenBudget
	recentMessages    int
}

type userVisibleError struct {
	message string
}

func (e *userVisibleError) Error() string { return e.message }

type contextWindowError struct {
	usage tokenUsage
	cause error
}

func (e *contextWindowError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("context window exceeded: %v", e.cause)
	}
	return fmt.Sprintf("context window exceeded: total=%d", e.usage.TotalTokens)
}

func (e *contextWindowError) Unwrap() error { return e.cause }

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
	if options.Tools == nil {
		return nil, fmt.Errorf("tool registry cannot be nil")
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
	maxToolIterations := options.MaxToolIterations
	if maxToolIterations == 0 {
		maxToolIterations = defaultMaxToolIterations
	}
	if maxToolIterations < 0 {
		return nil, fmt.Errorf("max tool iterations cannot be negative")
	}
	toolTimeout := options.ToolTimeout
	if toolTimeout == 0 {
		toolTimeout = defaultToolTimeout
	}
	if toolTimeout < 0 {
		return nil, fmt.Errorf("tool timeout cannot be negative")
	}
	contextWindowTokens := options.ContextWindowTokens
	if contextWindowTokens == 0 {
		contextWindowTokens = defaultContextWindowTokens
	}
	compactionThresholdTokens := options.CompactionThresholdTokens
	if compactionThresholdTokens == 0 {
		compactionThresholdTokens = defaultCompactionThresholdTokens
	}
	recentMessages := options.CompactionRecentMessages
	if recentMessages == 0 {
		recentMessages = defaultCompactionRecentMessages
	}
	budget, err := newTokenBudget(contextWindowTokens, compactionThresholdTokens)
	if err != nil {
		return nil, err
	}
	if recentMessages < 0 {
		return nil, fmt.Errorf("compaction recent messages cannot be negative")
	}
	if budget.hardInputLimit(maxTokens) <= 0 {
		return nil, fmt.Errorf("context window tokens must exceed max output tokens and safety reserve")
	}

	return &Agent{
		id:                options.ID,
		name:              options.Name,
		defaultModel:      defaultModel,
		allowedModels:     allowed,
		allowedModelSet:   allowedSet,
		providers:         providers,
		sessionsManager:   session.NewManager(options.ID, options.SessionStore),
		soulPath:          options.SoulPath,
		maxTokens:         maxTokens,
		temperature:       temperature,
		tools:             options.Tools,
		skillsSummary:     strings.TrimSpace(options.SkillsSummary),
		maxToolIterations: maxToolIterations,
		toolTimeout:       toolTimeout,
		tokenBudget:       budget,
		recentMessages:    recentMessages,
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

	//如果第一条指令是/model，就把后面所有参数传给模型处理函数并返回。
	//strings.Fields()：把字符串按空白（空格 / 制表符）切分成字符串切片，自动忽略多余空格
	if fields := strings.Fields(trimmed); len(fields) > 0 && fields[0] == "/model" {
		return a.handleModelCommand(ctx, address, fields[1:])
	}

	reply, err := a.handleMessage(ctx, msg)
	if err != nil {
		var visible *userVisibleError
		if errors.As(err, &visible) {
			return visible.Error()
		}
		var windowErr *contextWindowError
		if errors.As(err, &windowErr) {
			return contextWindowErrorReply
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

// handleMessage 循环执行模型和工具，并在整轮成功后保存上下文。
func (a *Agent) handleMessage(ctx context.Context, msg bus.InboundMessage) (*provider.Message, error) {
	return a.handleMessageWithEvents(ctx, msg, nil)
}

func (a *Agent) handleMessageWithEvents(ctx context.Context, msg bus.InboundMessage, events chan<- Event) (*provider.Message, error) {
	// 1. 每次处理消息时重新读取 Soul，使运行期间修改的角色设定可以立即生效。
	soul, err := a.readSystemPrompt()
	if err != nil {
		return nil, fmt.Errorf("read soul: %w", err)
	}

	// 2. 找到当前平台会话；首次对话时 SessionManager 会自动创建一个新会话。
	currentSession, err := a.sessionsManager.CurrentSession(ctx, msg.Address())
	if err != nil {
		return nil, fmt.Errorf("load session: %w", err)
	}

	// 3. 按“本条消息指定 > 会话已选择 > Agent 默认”的优先级确定本轮模型。
	selectedModel := msg.ModelRef
	if selectedModel == "" {
		selectedModel = currentSession.ModelRef()
	}
	if selectedModel == "" {
		selectedModel = a.defaultModel
	}
	// 模型既要在 Agent 白名单内，也必须能在 ProviderManager 中解析成功。
	selectedModel, err = a.validateAllowedModel(selectedModel)
	if err != nil {
		return nil, &userVisibleError{message: err.Error()}
	}
	resolved, err := a.providers.Resolve(selectedModel)
	if err != nil {
		return nil, &userVisibleError{message: fmt.Sprintf("模型 %s 当前不可用: %v", selectedModel, err)}
	}

	// 4. 组装发给模型的预算内上下文；首次模型调用失败时不会污染会话历史。
	userMessage := provider.Message{Role: "user", Content: msg.Text}
	messages, err := a.prepareStatefulContext(ctx, resolved, currentSession, soul, userMessage)
	if err != nil {
		return nil, err
	}
	return a.runConversation(ctx, resolved, currentSession, messages, []provider.Message{userMessage}, events, a.maxTokens, a.temperature)
}

func (a *Agent) runConversation(
	ctx context.Context,
	resolved provider.ResolvedModel,
	currentSession *session.Session,
	messages []provider.Message,
	pendingMessages []provider.Message,
	events chan<- Event,
	maxTokens int,
	temperature float64,
) (*provider.Message, error) {
	// 阶段一：初始化本轮工具循环状态。
	// toolCallCounts 用于识别同名同参数的重复调用；failedRounds 用于连续失败降级。
	toolDefinitions := a.tools.Definitions()

	//正常网页平台聊天，这里都不会为空。
	if currentSession == nil {
		filtered := make([]provider.Tool, 0, len(toolDefinitions))
		for _, definition := range toolDefinitions {
			if !tools.IsStatefulToolName(definition.Function.Name) {
				filtered = append(filtered, definition)
			}
		}
		toolDefinitions = filtered
	}
	toolCallCounts := make(map[toolCallSignature]int)
	failedRounds := 0

	// 阶段二：进入 ReAct 工具循环，模型决定是否调用工具，结果再反馈给模型继续推理。
	for iteration := 0; iteration < a.maxToolIterations; iteration++ {
		// 正常模型调用始终携带完整工具定义，让模型可以选择直接回答或发起 ToolCalls。
		assistantMessage, err := a.callModel(
			ctx,
			resolved,
			messages,
			toolDefinitions,
			maxTokens,
			temperature,
			events,
		)
		if err != nil {
			return nil, err
		}
		if assistantMessage == nil {
			return nil, fmt.Errorf("provider returned a nil message")
		}

		// Provider 返回的消息统一按 assistant 角色写入，避免兼容接口漏填 Role。
		assistant := *assistantMessage
		assistant.Role = "assistant"

		// 没有 ToolCalls 说明模型准备结束本轮；非空文本就是最终答复。
		if len(assistant.ToolCalls) == 0 {
			// 空文本不能作为有效终点，因此禁用工具再请求模型做一次最终归纳。
			if strings.TrimSpace(assistant.Content) == "" {
				return a.synthesizeFinal(ctx, resolved, currentSession, messages, pendingMessages, "模型返回了空响应", events, maxTokens, temperature)
			}
			if err := appendFinalMessage(ctx, currentSession, pendingMessages, assistant); err != nil {
				return nil, err
			}
			return &assistant, nil
		}

		// 阶段三：模型请求调用工具时，先补齐类型和 ID，确保每个结果都能准确配对。
		assistant.ToolCalls = normalizeToolCalls(assistant.ToolCalls, iteration)
		toolMessages := make([]provider.Message, 0, len(assistant.ToolCalls))
		// 默认认为本轮全部失败，只要有一个工具成功就会改为 false，并重置连续失败计数。
		roundAllFailed := true
		repeatedCallDetected := false
		for _, call := range assistant.ToolCalls {
			if !emitOptionalEvent(ctx, events, Event{
				Type: EventToolStart,
				Data: EventData{
					ToolCallID: call.ID,
					ToolName:   call.Function.Name,
					Arguments:  call.Function.Arguments,
				},
			}) {
				return nil, ctx.Err()
			}
			// 对工具名和规范化后的 JSON 参数生成签名，参数字段顺序不同也会视为同一次调用。
			signature := makeToolCallSignature(call)
			toolCallCounts[signature]++
			var result string
			var executeErr error
			// 相同调用第三次出现时直接拦截，避免模型在无效路径上无限消耗迭代次数。
			if toolCallCounts[signature] >= repeatedToolCallLimit {
				repeatedCallDetected = true
				executeErr = fmt.Errorf("repeated tool call blocked after %d attempts", repeatedToolCallLimit)
			} else {
				// 每个工具都由 executeToolCall 添加独立超时，单个慢工具不会拖死整个循环。
				result, executeErr = a.executeToolCall(ctx, currentSession, call)
			}
			if ctx.Err() != nil || errors.Is(executeErr, context.Canceled) {
				return nil, ctx.Err()
			}
			// 工具错误不会直接终止 Agent，而是转换为模型可读的 tool result，让模型调整策略。
			success := executeErr == nil
			if executeErr != nil {
				result = formatToolError(executeErr)
			} else {
				roundAllFailed = false
			}
			// tool 消息必须携带原 ToolCallID，Provider 才能把结果和 assistant 调用对应起来。
			toolMessages = append(toolMessages, provider.Message{
				Role:       "tool",
				Content:    result,
				ToolCallID: call.ID,
				Name:       call.Function.Name,
			})
			if !emitOptionalEvent(ctx, events, Event{
				Type: EventToolResult,
				Data: EventData{
					ToolCallID: call.ID,
					ToolName:   call.Function.Name,
					Result:     result,
					Success:    boolPointer(success),
				},
			}) {
				return nil, ctx.Err()
			}
		}

		// 阶段四：将完整工具轮加入本轮暂存和模型上下文，不提前写入持久化会话。
		pendingMessages = append(pendingMessages, assistant)
		pendingMessages = append(pendingMessages, toolMessages...)
		messages = append(messages, assistant)
		messages = append(messages, toolMessages...)

		// 重复调用达到阈值后立即禁用工具，要求模型基于已有结果生成最终答复。
		if repeatedCallDetected {
			return a.synthesizeFinal(ctx, resolved, currentSession, messages, pendingMessages, "检测到重复工具调用", events, maxTokens, temperature)
		}

		// 只有整轮工具全部失败才累计失败轮数；任一成功结果都会将计数清零。
		if roundAllFailed {
			failedRounds++
		} else {
			failedRounds = 0
		}
		// 连续三轮全部失败时停止继续试工具，避免在不可用服务或错误参数上反复消耗。
		if failedRounds >= failedToolRoundLimit {
			return a.synthesizeFinal(ctx, resolved, currentSession, messages, pendingMessages, "连续三轮工具调用全部失败", events, maxTokens, temperature)
		}
	}

	// 阶段五：用完最大迭代次数仍没有最终文本时，进行一次不携带工具的强制归纳。
	return a.synthesizeFinal(ctx, resolved, currentSession, messages, pendingMessages, fmt.Sprintf("已达到 %d 次工具迭代上限", a.maxToolIterations), events, maxTokens, temperature)
}

func (a *Agent) callModel(
	ctx context.Context,
	resolved provider.ResolvedModel,
	messages []provider.Message,
	toolDefinitions []provider.Tool,
	maxTokens int,
	temperature float64,
	events chan<- Event,
) (*provider.Message, error) {
	if !a.tokenBudget.fits(messages, toolDefinitions, maxTokens) {
		return nil, &contextWindowError{usage: a.tokenBudget.usage(messages, toolDefinitions, maxTokens)}
	}
	if events == nil {
		return resolved.Provider.Chat(ctx, messages, toolDefinitions, resolved.ModelID, maxTokens, temperature)
	}
	stream, err := resolved.Provider.ChatStream(ctx, messages, toolDefinitions, resolved.ModelID, maxTokens, temperature)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	for {
		chunk, nextErr := stream.Next()
		if nextErr != nil {
			if errors.Is(nextErr, io.EOF) {
				return nil, fmt.Errorf("provider stream ended before final message")
			}
			return nil, nextErr
		}
		if chunk.Delta != "" && !emitEvent(ctx, events, Event{Type: EventTokenDelta, Data: EventData{Delta: chunk.Delta}}) {
			return nil, ctx.Err()
		}
		if chunk.Done {
			if chunk.Message == nil {
				return nil, fmt.Errorf("provider stream returned a nil final message")
			}
			return chunk.Message, nil
		}
	}
}

type toolCallSignature struct {
	name string
	hash [32]byte
}

func normalizeToolCalls(calls []provider.ToolCall, iteration int) []provider.ToolCall {
	normalized := append([]provider.ToolCall(nil), calls...)
	usedIDs := make(map[string]struct{}, len(normalized))
	for index := range normalized {
		call := &normalized[index]
		if call.Type == "" {
			call.Type = "function"
		}
		_, duplicate := usedIDs[call.ID]
		if strings.TrimSpace(call.ID) == "" || duplicate {
			base := fmt.Sprintf("call-%d-%d", iteration+1, index+1)
			call.ID = base
			for suffix := 2; ; suffix++ {
				if _, exists := usedIDs[call.ID]; !exists {
					break
				}
				call.ID = fmt.Sprintf("%s-%d", base, suffix)
			}
		}
		usedIDs[call.ID] = struct{}{}
	}
	return normalized
}

func makeToolCallSignature(call provider.ToolCall) toolCallSignature {
	arguments := []byte(strings.TrimSpace(call.Function.Arguments))
	var decoded any
	if json.Unmarshal(arguments, &decoded) == nil {
		if canonical, err := json.Marshal(decoded); err == nil {
			arguments = canonical
		}
	}
	return toolCallSignature{
		name: call.Function.Name,
		hash: sha256.Sum256(arguments),
	}
}

func (a *Agent) executeToolCall(ctx context.Context, currentSession *session.Session, call provider.ToolCall) (string, error) {
	if call.Type != "function" {
		return "", fmt.Errorf("unsupported tool call type %q", call.Type)
	}
	// 阶段一：会话作用域由运行时注入，模型参数不能伪造 Agent、聊天或 session 身份。
	if currentSession != nil {
		ctx = tools.WithSessionScope(ctx, a.id, currentSession.Address(), currentSession.Key())
	}
	// 阶段二：为单次工具执行建立比整轮请求更细的超时边界。
	toolCtx, cancel := context.WithTimeout(ctx, a.toolTimeout)
	defer cancel()
	type executionResult struct {
		content string
		err     error
	}
	// 阶段三：工具放到独立 goroutine 执行；缓冲通道保证调用方超时返回后不会阻塞发送结果。
	resultChannel := make(chan executionResult, 1)
	go func() {
		result, err := a.tools.Execute(toolCtx, call.Function.Name, json.RawMessage(call.Function.Arguments))
		resultChannel <- executionResult{content: result, err: err}
	}()
	select {
	case <-toolCtx.Done():
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("tool %q timed out after %s", call.Function.Name, a.toolTimeout)
	case result := <-resultChannel:
		if errors.Is(toolCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return "", fmt.Errorf("tool %q timed out after %s", call.Function.Name, a.toolTimeout)
		}
		return result.content, result.err
	}
}

func formatToolError(err error) string {
	return fmt.Sprintf("tool execution failed: %v", err)
}

func appendFinalMessage(
	ctx context.Context,
	currentSession *session.Session,
	pendingMessages []provider.Message,
	assistant provider.Message,
) error {
	if currentSession == nil {
		return nil
	}
	batch := make([]provider.Message, 0, len(pendingMessages)+1)
	batch = append(batch, pendingMessages...)
	batch = append(batch, assistant)
	if err := currentSession.Append(ctx, batch...); err != nil {
		return fmt.Errorf("save session messages: %w", err)
	}
	return nil
}

func (a *Agent) synthesizeFinal(
	ctx context.Context,
	resolved provider.ResolvedModel,
	currentSession *session.Session,
	messages []provider.Message,
	pendingMessages []provider.Message,
	reason string,
	events chan<- Event,
	maxTokens int,
	temperature float64,
) (*provider.Message, error) {
	finalMessages := append([]provider.Message(nil), messages...)
	finalMessages = append(finalMessages, provider.Message{
		Role: "system",
		Content: fmt.Sprintf(
			"%s。工具现已禁用。请根据已有消息和工具结果直接给出完整最终答复；无法确认的信息要明确标记为未验证，不要再调用工具。",
			reason,
		),
	})
	response, err := a.callModel(
		ctx,
		resolved,
		finalMessages,
		nil,
		maxTokens,
		temperature,
		events,
	)
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return nil, ctx.Err()
	}
	var windowErr *contextWindowError
	if errors.As(err, &windowErr) {
		return nil, windowErr
	}
	assistant := provider.Message{Role: "assistant"}
	if err == nil && response != nil && len(response.ToolCalls) == 0 && strings.TrimSpace(response.Content) != "" {
		assistant = *response
		assistant.Role = "assistant"
	} else {
		if err != nil {
			log.Printf("Agent %s 最终归纳调用失败: %v", a.id, err)
		}
		assistant.Content = fmt.Sprintf("工具调用未能完成（%s），暂时无法生成可靠结果，请稍后重试。", reason)
	}
	if err := appendFinalMessage(ctx, currentSession, pendingMessages, assistant); err != nil {
		return nil, err
	}
	return &assistant, nil
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

// ReadSoul 读取 Agent 当前使用的角色设定。
func (a *Agent) ReadSoul() (string, error) {
	a.soulMu.RLock()
	defer a.soulMu.RUnlock()
	content, err := os.ReadFile(a.soulPath)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func (a *Agent) readSystemPrompt() (string, error) {
	soul, err := a.ReadSoul()
	if err != nil {
		return "", err
	}
	if a.skillsSummary == "" {
		return soul, nil
	}
	return strings.TrimSpace(soul) + "\n\n" + a.skillsSummary, nil
}

// UpdateSoul 保存 Agent 的角色设定，下一条消息会立即使用新内容。
func (a *Agent) UpdateSoul(content string) error {
	a.soulMu.Lock()
	defer a.soulMu.Unlock()
	return os.WriteFile(a.soulPath, []byte(content), 0o600)
}

// NewSession 为网页等控制面创建一段新的会话。
func (a *Agent) NewSession(ctx context.Context, address bus.ConversationAddress) (*session.Session, error) {
	return a.sessionsManager.NewSession(ctx, address)
}
