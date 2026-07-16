package tools

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestCurrentTimeUsesDefaultAndRequestedTimezone(t *testing.T) {
	fixed := time.Date(2026, time.July, 16, 3, 4, 5, 0, time.UTC)
	tool := newCurrentTimeTool(func() time.Time { return fixed })

	defaultResult, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	assertTimeResult(t, defaultResult, fixed.In(time.Local).Format(time.RFC3339), time.Local.String())

	shanghaiResult, err := tool.Execute(context.Background(), json.RawMessage(`{"timezone":"Asia/Shanghai"}`))
	if err != nil {
		t.Fatal(err)
	}
	assertTimeResult(t, shanghaiResult, "2026-07-16T11:04:05+08:00", "Asia/Shanghai")
}

func TestCurrentTimeRejectsInvalidArguments(t *testing.T) {
	tool := newCurrentTimeTool(func() time.Time { return time.Time{} })
	for _, raw := range []string{
		`{"timezone":"Mars/Olympus"}`,
		`{"unknown":true}`,
		`not-json`,
	} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(raw)); err == nil {
			t.Fatalf("Execute(%s) error = nil", raw)
		}
	}
}

func assertTimeResult(t *testing.T, raw, wantTime, wantTimezone string) {
	t.Helper()
	var result map[string]string
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if result["time"] != wantTime || result["timezone"] != wantTimezone {
		t.Fatalf("time result = %#v", result)
	}
}
