package agent

import (
	"strings"
	"testing"

	"github.com/lucky798213/luckyclaw/internal/provider"
)

func TestEstimateTextTokensUsesConservativeLanguageEstimate(t *testing.T) {
	if got := estimateTextTokens("abcdefgh"); got != 8 {
		t.Fatalf("英文估算 = %d, want 8", got)
	}
	if got := estimateTextTokens("长期记忆"); got != 4 {
		t.Fatalf("中文估算 = %d, want 4", got)
	}
}

func TestTokenBudgetCountsMessagesToolsAndOutputReserve(t *testing.T) {
	budget, err := newTokenBudget(5000, 3000)
	if err != nil {
		t.Fatal(err)
	}
	messages := []provider.Message{{
		Role:    "assistant",
		Content: strings.Repeat("a", 400),
		ToolCalls: []provider.ToolCall{{
			ID:   "call-1",
			Type: "function",
			Function: provider.FunctionCall{
				Name:      "lookup",
				Arguments: `{"query":"早期事实"}`,
			},
		}},
	}}
	tools := []provider.Tool{{
		Type: "function",
		Function: provider.ToolFunction{
			Name:        "lookup",
			Description: strings.Repeat("工具说明", 20),
			Parameters:  map[string]any{"type": "object"},
		},
	}}
	usage := budget.usage(messages, tools, 512)
	if usage.InputTokens <= 100 || usage.OutputTokens != 512 || usage.SafetyTokens != tokenSafetyReserve {
		t.Fatalf("预算明细 = %+v", usage)
	}
	if usage.TotalTokens != usage.InputTokens+512+tokenSafetyReserve {
		t.Fatalf("总预算 = %+v", usage)
	}
}

func TestTokenBudgetThresholdAndHardWindow(t *testing.T) {
	budget, err := newTokenBudget(2200, 1500)
	if err != nil {
		t.Fatal(err)
	}
	short := []provider.Message{{Role: "user", Content: "hello"}}
	if budget.shouldCompact(short, nil, 128) || !budget.fits(short, nil, 128) {
		t.Fatal("短消息错误触发预算限制")
	}
	long := []provider.Message{{Role: "user", Content: strings.Repeat("x", 1800)}}
	if !budget.shouldCompact(long, nil, 128) {
		t.Fatal("长消息没有触发压缩")
	}
	if budget.fits(long, nil, 128) {
		t.Fatal("长消息错误通过硬窗口检查")
	}
	if got := budget.hardInputLimit(128); got != 1048 {
		t.Fatalf("硬输入上限 = %d, want 1048", got)
	}
}

func TestNewTokenBudgetRejectsInvalidConfiguration(t *testing.T) {
	for _, test := range []struct {
		window    int
		threshold int
	}{
		{0, 1},
		{10, 0},
		{10, 10},
		{10, 11},
	} {
		if _, err := newTokenBudget(test.window, test.threshold); err == nil {
			t.Fatalf("newTokenBudget(%d, %d) error = nil", test.window, test.threshold)
		}
	}
}
