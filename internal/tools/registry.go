// Package tools 提供 LuckyClaw 可供模型调用的安全工具。
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"github.com/lucky798213/luckyclaw/internal/provider"
)

// Tool 描述一个可以暴露给模型并执行的工具。
type Tool interface {
	Definition() provider.Tool
	Execute(ctx context.Context, arguments json.RawMessage) (string, error)
}

// Executor 定义按名称执行工具的能力。
type Executor interface {
	Execute(ctx context.Context, name string, arguments json.RawMessage) (string, error)
}

// Registry 定义 Agent 使用工具注册表所需的最小能力。
type Registry interface {
	Executor
	Definitions() []provider.Tool
}

type registry struct {
	tools       map[string]Tool
	definitions []provider.Tool
}

// NewRegistry 创建一个工具注册表，并拒绝无效或重名工具。
func NewRegistry(input ...Tool) (Registry, error) {
	registered := make(map[string]Tool, len(input))
	definitions := make([]provider.Tool, 0, len(input))
	for index, tool := range input {
		if isNilTool(tool) {
			return nil, fmt.Errorf("tool %d cannot be nil", index)
		}
		definition := tool.Definition()
		name := definition.Function.Name
		if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) {
			return nil, fmt.Errorf("tool %d name cannot be empty or contain surrounding whitespace", index)
		}
		if definition.Type != "function" {
			return nil, fmt.Errorf("tool %q type must be function", name)
		}
		if definition.Function.Parameters == nil {
			return nil, fmt.Errorf("tool %q parameters cannot be nil", name)
		}
		if _, exists := registered[name]; exists {
			return nil, fmt.Errorf("tool %q is duplicated", name)
		}
		registered[name] = tool
		definitions = append(definitions, definition)
	}
	sort.Slice(definitions, func(i, j int) bool {
		return definitions[i].Function.Name < definitions[j].Function.Name
	})
	return &registry{tools: registered, definitions: definitions}, nil
}

// NewDefaultRegistry 创建包含所有内置安全工具的注册表。
func NewDefaultRegistry(additional ...Tool) (Registry, error) {
	builtins := []Tool{
		newCurrentTimeTool(nil),
		newCalculatorTool(),
		newHTTPFetchTool(nil),
	}
	builtins = append(builtins, additional...)
	return NewRegistry(builtins...)
}

// Definitions 返回按工具名稳定排序的工具定义副本。
func (r *registry) Definitions() []provider.Tool {
	return append([]provider.Tool(nil), r.definitions...)
}

// Execute 执行指定工具；未知工具不会触发任何外部操作。
func (r *registry) Execute(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	tool, exists := r.tools[name]
	if !exists {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	return tool.Execute(ctx, arguments)
}

func isNilTool(tool Tool) bool {
	if tool == nil {
		return true
	}
	value := reflect.ValueOf(tool)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func functionDefinition(name, description string, parameters map[string]any) provider.Tool {
	return provider.Tool{
		Type: "function",
		Function: provider.ToolFunction{
			Name:        name,
			Description: description,
			Parameters:  parameters,
		},
	}
}

func decodeArguments(arguments json.RawMessage, target any) error {
	if len(strings.TrimSpace(string(arguments))) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(strings.NewReader(string(arguments)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode arguments: %w", err)
	}
	if decoder.More() {
		return fmt.Errorf("decode arguments: multiple JSON values are not allowed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("decode arguments: trailing data is not allowed")
	}
	return nil
}
