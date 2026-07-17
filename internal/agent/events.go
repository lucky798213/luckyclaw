package agent

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/lucky798213/luckyclaw/internal/bus"
)

// EventType 表示 Agent 对外发送的统一流式事件类型。
type EventType string

const (
	EventTokenDelta EventType = "token_delta"
	EventToolStart  EventType = "tool_start"
	EventToolResult EventType = "tool_result"
	EventFinal      EventType = "final"
	EventError      EventType = "error"
)

// Event 表示一次 Agent 流式执行产生的事件。
type Event struct {
	Type EventType `json:"type"`
	Data EventData `json:"data"`
}

// EventData 保存不同事件使用的数据字段。
type EventData struct {
	Delta      string `json:"delta,omitempty"`
	Content    string `json:"content,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	Arguments  string `json:"arguments,omitempty"`
	Result     string `json:"result,omitempty"`
	Success    *bool  `json:"success,omitempty"`
	Message    string `json:"message,omitempty"`
}

// HandleMessageStream 以统一事件流处理一条入站消息。
func (a *Agent) HandleMessageStream(ctx context.Context, msg bus.InboundMessage) <-chan Event {
	events := make(chan Event, 32)
	go func() {
		defer close(events)
		trimmed := strings.TrimSpace(msg.Text)
		if trimmed == "/new" || isModelCommand(trimmed) {
			reply := a.HandleMessage(ctx, msg)
			if ctx.Err() == nil {
				emitEvent(ctx, events, Event{Type: EventFinal, Data: EventData{Content: reply}})
			}
			return
		}

		reply, err := a.handleMessageWithEvents(ctx, msg, events)
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return
		}
		if err != nil {
			message := handleMessageErrorReply
			var visible *userVisibleError
			if errors.As(err, &visible) {
				message = visible.Error()
			} else {
				log.Printf("Agent %s 流式处理消息失败: %v", a.id, err)
			}
			emitEvent(ctx, events, Event{Type: EventError, Data: EventData{Message: message}})
			return
		}
		emitEvent(ctx, events, Event{Type: EventFinal, Data: EventData{Content: reply.Content}})
	}()
	return events
}

func isModelCommand(trimmed string) bool {
	fields := strings.Fields(trimmed)
	return len(fields) > 0 && fields[0] == "/model"
}

func emitOptionalEvent(ctx context.Context, events chan<- Event, event Event) bool {
	if events == nil {
		return true
	}
	return emitEvent(ctx, events, event)
}

func boolPointer(value bool) *bool {
	return &value
}

func emitEvent(ctx context.Context, events chan<- Event, event Event) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}
