// Package agent 定义 LuckyClaw 智能体。
package agent

import (
	"fmt"

	"lukcyclaw/internal/provider"
)

// Agent 表示一个可以调用大模型的智能体。
type Agent struct {
	name        string
	model       string
	provider    provider.Provider
	maxTokens   int
	temperature float64
}

// New 创建一个 Agent。
func New(
	name string,
	model string,
	prov provider.Provider,
) (*Agent, error) {
	if name == "" {
		return nil, fmt.Errorf("agent name cannot be empty")
	}

	if model == "" {
		return nil, fmt.Errorf("model cannot be empty")
	}

	if prov == nil {
		return nil, fmt.Errorf("provider cannot be nil")
	}

	return &Agent{
		name:        name,
		model:       model,
		provider:    prov,
		maxTokens:   4096,
		temperature: 0.7,
	}, nil
}

// Name 返回 Agent 名称。
func (a *Agent) Name() string {
	return a.name
}

// Model 返回当前使用的模型。
func (a *Agent) Model() string {
	return a.model
}
