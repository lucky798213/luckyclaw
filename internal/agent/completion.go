package agent

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/lucky798213/luckyclaw/internal/provider"
)

// CompletionOptions 保存无状态完成请求覆盖的模型参数。
type CompletionOptions struct {
	ModelRef    string
	MaxTokens   int
	Temperature *float64
}

// Complete 使用完整消息数组执行一次无状态完成，不读写会话存储。
func (a *Agent) Complete(ctx context.Context, messages []provider.Message, options CompletionOptions) (*provider.Message, error) {
	resolved, prepared, maxTokens, temperature, err := a.prepareCompletion(messages, options)
	if err != nil {
		return nil, err
	}
	return a.runConversation(ctx, resolved, nil, prepared, nil, nil, maxTokens, temperature)
}

// CompleteStream 以统一事件执行一次无状态流式完成，不读写会话存储。
func (a *Agent) CompleteStream(ctx context.Context, messages []provider.Message, options CompletionOptions) <-chan Event {
	events := make(chan Event, 32)
	go func() {
		defer close(events)
		resolved, prepared, maxTokens, temperature, err := a.prepareCompletion(messages, options)
		if err == nil {
			var reply *provider.Message
			reply, err = a.runConversation(ctx, resolved, nil, prepared, nil, events, maxTokens, temperature)
			if err == nil {
				emitEvent(ctx, events, Event{Type: EventFinal, Data: EventData{Content: reply.Content}})
				return
			}
		}
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return
		}
		log.Printf("Agent %s 无状态流式完成失败: %v", a.id, err)
		emitEvent(ctx, events, Event{Type: EventError, Data: EventData{Message: handleMessageErrorReply}})
	}()
	return events
}

func (a *Agent) prepareCompletion(messages []provider.Message, options CompletionOptions) (
	provider.ResolvedModel,
	[]provider.Message,
	int,
	float64,
	error,
) {
	modelRef, err := a.validateAllowedModel(options.ModelRef)
	if err != nil {
		return provider.ResolvedModel{}, nil, 0, 0, err
	}
	resolved, err := a.providers.Resolve(modelRef)
	if err != nil {
		return provider.ResolvedModel{}, nil, 0, 0, fmt.Errorf("resolve model: %w", err)
	}
	soul, err := a.readSystemPrompt()
	if err != nil {
		return provider.ResolvedModel{}, nil, 0, 0, fmt.Errorf("read soul: %w", err)
	}
	prepared := make([]provider.Message, 0, len(messages)+1)
	prepared = append(prepared, provider.Message{Role: "system", Content: soul})
	for _, message := range messages {
		copyMessage := message
		if copyMessage.Role == "developer" {
			copyMessage.Role = "system"
		}
		prepared = append(prepared, copyMessage)
	}
	maxTokens := options.MaxTokens
	if maxTokens <= 0 {
		maxTokens = a.maxTokens
	}
	temperature := a.temperature
	if options.Temperature != nil {
		temperature = *options.Temperature
	}
	return resolved, prepared, maxTokens, temperature, nil
}
