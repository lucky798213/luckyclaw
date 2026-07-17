package agent

import (
	"context"

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
		reply := a.HandleMessage(ctx, msg)
		if ctx.Err() != nil {
			return
		}
		emitEvent(ctx, events, Event{Type: EventFinal, Data: EventData{Content: reply}})
	}()
	return events
}

func emitEvent(ctx context.Context, events chan<- Event, event Event) bool {
	select {
	case events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}
