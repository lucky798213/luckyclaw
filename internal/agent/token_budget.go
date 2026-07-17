package agent

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/lucky798213/luckyclaw/internal/provider"
)

const tokenSafetyReserve = 1024

// tokenBudget 使用保守估算控制模型上下文，避免依赖特定模型的 tokenizer。
type tokenBudget struct {
	windowTokens    int
	thresholdTokens int
}

type tokenUsage struct {
	InputTokens  int
	OutputTokens int
	SafetyTokens int
	TotalTokens  int
}

func newTokenBudget(windowTokens, thresholdTokens int) (tokenBudget, error) {
	if windowTokens <= 0 {
		return tokenBudget{}, &budgetConfigError{message: "context window tokens must be greater than zero"}
	}
	if thresholdTokens <= 0 {
		return tokenBudget{}, &budgetConfigError{message: "compaction threshold tokens must be greater than zero"}
	}
	if thresholdTokens >= windowTokens {
		return tokenBudget{}, &budgetConfigError{message: "compaction threshold tokens must be less than context window tokens"}
	}
	return tokenBudget{windowTokens: windowTokens, thresholdTokens: thresholdTokens}, nil
}

type budgetConfigError struct {
	message string
}

func (e *budgetConfigError) Error() string { return e.message }

func (b tokenBudget) usage(messages []provider.Message, tools []provider.Tool, maxOutputTokens int) tokenUsage {
	input := estimateMessagesTokens(messages) + estimateToolsTokens(tools)
	usage := tokenUsage{
		InputTokens:  input,
		OutputTokens: maxOutputTokens,
		SafetyTokens: tokenSafetyReserve,
	}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens + usage.SafetyTokens
	return usage
}

func (b tokenBudget) shouldCompact(messages []provider.Message, tools []provider.Tool, maxOutputTokens int) bool {
	return b.usage(messages, tools, maxOutputTokens).TotalTokens >= b.thresholdTokens
}

func (b tokenBudget) fits(messages []provider.Message, tools []provider.Tool, maxOutputTokens int) bool {
	return b.usage(messages, tools, maxOutputTokens).TotalTokens <= b.windowTokens
}

func (b tokenBudget) hardInputLimit(maxOutputTokens int) int {
	return b.windowTokens - maxOutputTokens - tokenSafetyReserve
}

func estimateMessagesTokens(messages []provider.Message) int {
	total := 0
	for _, message := range messages {
		// 每条消息预留角色、字段名和 JSON 分隔符的结构开销。
		total += 8 + estimateTextTokens(message.Role) + estimateTextTokens(message.Content)
		total += estimateTextTokens(message.ToolCallID) + estimateTextTokens(message.Name)
		for _, call := range message.ToolCalls {
			total += 12
			total += estimateTextTokens(call.ID)
			total += estimateTextTokens(call.Type)
			total += estimateTextTokens(call.Function.Name)
			total += estimateTextTokens(call.Function.Arguments)
		}
	}
	return total
}

func estimateToolsTokens(tools []provider.Tool) int {
	total := 0
	for _, tool := range tools {
		payload, err := json.Marshal(tool)
		if err != nil {
			// 无法序列化时按固定高开销计入，宁可提前压缩。
			total += 256
			continue
		}
		total += 12 + estimateTextTokens(string(payload))
	}
	return total
}

func estimateTextTokens(text string) int {
	if text == "" {
		return 0
	}
	byBytes := (len(text) + 3) / 4
	byRunes := utf8.RuneCountInString(text)
	if byRunes > byBytes {
		return byRunes
	}
	return byBytes
}
