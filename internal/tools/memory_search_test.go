package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lucky798213/luckyclaw/internal/bus"
	"github.com/lucky798213/luckyclaw/internal/session"
)

type memorySearcherSpy struct {
	agentID string
	address bus.ConversationAddress
	query   string
	limit   int
	results []session.MemorySearchResult
	err     error
}

func (s *memorySearcherSpy) SearchMemory(
	_ context.Context,
	agentID string,
	address bus.ConversationAddress,
	query string,
	limit int,
) ([]session.MemorySearchResult, error) {
	s.agentID = agentID
	s.address = address
	s.query = query
	s.limit = limit
	return s.results, s.err
}

func TestMemorySearchToolUsesTrustedSessionScopeAndBoundsOutput(t *testing.T) {
	address := bus.ConversationAddress{Channel: "terminal", AccountID: "local", ChatID: "default", ThreadID: "topic"}
	searcher := &memorySearcherSpy{results: []session.MemorySearchResult{{
		SessionKey: "session-old",
		Sequence:   7,
		Role:       "user",
		Content:    strings.Repeat("记", 600),
		CreatedAt:  time.Date(2026, 7, 17, 8, 30, 0, 0, time.FixedZone("CST", 8*60*60)),
	}}}
	tool, err := NewMemorySearchTool(searcher, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithSessionScope(context.Background(), "agent-a", address)
	result, err := tool.Execute(ctx, json.RawMessage(`{"query":"  项目代号  "}`))
	if err != nil {
		t.Fatal(err)
	}
	if searcher.agentID != "agent-a" || searcher.address != address || searcher.query != "项目代号" || searcher.limit != 5 {
		t.Fatalf("检索参数 = %+v", searcher)
	}
	var output memorySearchOutput
	if err := json.Unmarshal([]byte(result), &output); err != nil {
		t.Fatal(err)
	}
	if output.Query != "项目代号" || len(output.Results) != 1 || output.Results[0].SessionKey != "session-old" {
		t.Fatalf("工具输出 = %+v", output)
	}
	if !strings.Contains(output.Warning, "不可信") || output.Results[0].Timestamp != "2026-07-17T00:30:00Z" {
		t.Fatalf("工具输出元数据 = %+v", output)
	}
	if got := []rune(output.Results[0].Content); len(got) != 501 || got[500] != '…' {
		t.Fatalf("结果未限制为 500 字符: 长度=%d", len(got))
	}
}

func TestMemorySearchToolValidatesArgumentsScopeAndSearcherErrors(t *testing.T) {
	searcher := &memorySearcherSpy{}
	tool, err := NewMemorySearchTool(searcher, "agent-a")
	if err != nil {
		t.Fatal(err)
	}
	validScope := WithSessionScope(context.Background(), "agent-a", bus.ConversationAddress{ChatID: "chat"})
	tests := []struct {
		name string
		ctx  context.Context
		raw  string
		want string
	}{
		{name: "缺少查询", ctx: validScope, raw: `{}`, want: "query is required"},
		{name: "查询过长", ctx: validScope, raw: `{"query":"` + strings.Repeat("查", 201) + `"}`, want: "200"},
		{name: "limit 过大", ctx: validScope, raw: `{"query":"事实","limit":21}`, want: "limit"},
		{name: "未知参数", ctx: validScope, raw: `{"query":"事实","scope":"all"}`, want: "unknown field"},
		{name: "无状态调用", ctx: context.Background(), raw: `{"query":"事实"}`, want: "stateful session"},
		{name: "Agent 不匹配", ctx: WithSessionScope(context.Background(), "agent-b", bus.ConversationAddress{}), raw: `{"query":"事实"}`, want: "does not match"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := tool.Execute(test.ctx, json.RawMessage(test.raw)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Execute() error = %v, want %q", err, test.want)
			}
		})
	}
	searcher.err = errors.New("database unavailable")
	if _, err := tool.Execute(validScope, json.RawMessage(`{"query":"事实"}`)); err == nil || !strings.Contains(err.Error(), "database unavailable") {
		t.Fatalf("检索错误 = %v", err)
	}
}

func TestNewMemorySearchToolValidatesDependencies(t *testing.T) {
	if _, err := NewMemorySearchTool(nil, "agent-a"); err == nil {
		t.Fatal("nil searcher accepted")
	}
	if _, err := NewMemorySearchTool(&memorySearcherSpy{}, " "); err == nil {
		t.Fatal("empty agent id accepted")
	}
}
