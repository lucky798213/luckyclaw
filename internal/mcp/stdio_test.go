package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestStdioClientHandshakeListsAndCallsTools(t *testing.T) {
	client := newHelperClient(t, "normal", time.Second)
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	definitions, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 2 || definitions[0].Name != "echo" || definitions[1].Name != "second" {
		t.Fatalf("tools = %+v", definitions)
	}
	result, err := client.CallTool(context.Background(), "echo", json.RawMessage(`{"value":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) != 1 || !strings.Contains(string(result.Content[0]), "hello") {
		t.Fatalf("result = %+v", result)
	}
}

func TestStdioClientAcceptsOlderStableVersion(t *testing.T) {
	client := newHelperClient(t, "old", time.Second)
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStdioClientTimesOutToolCall(t *testing.T) {
	client := newHelperClient(t, "hang", 50*time.Millisecond)
	if err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer client.Close(context.Background())
	_, err := client.CallTool(context.Background(), "echo", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("timeout error = %v", err)
	}
}

func newHelperClient(t *testing.T, mode string, timeout time.Duration) *StdioClient {
	t.Helper()
	client, err := NewStdioClient(StdioOptions{
		Command:        os.Args[0],
		Args:           []string{"-test.run=TestMCPHelperProcess"},
		Env:            map[string]string{"GO_WANT_MCP_HELPER": "1", "MCP_HELPER_MODE": mode},
		RequestTimeout: timeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_HELPER") != "1" {
		return
	}
	mode := os.Getenv("MCP_HELPER_MODE")
	scanner := bufio.NewScanner(os.Stdin)
	initialized := false
	for scanner.Scan() {
		var message struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			os.Exit(2)
		}
		switch message.Method {
		case "initialize":
			version := latestProtocolVersion
			if mode == "old" {
				version = "2024-11-05"
			}
			writeHelperResponse(message.ID, map[string]any{
				"protocolVersion": version,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]string{"name": "test", "version": "1"},
			})
		case "notifications/initialized":
			initialized = true
		case "tools/list":
			if !initialized {
				writeHelperError(message.ID, -32000, "not initialized")
				continue
			}
			var params struct {
				Cursor string `json:"cursor"`
			}
			_ = json.Unmarshal(message.Params, &params)
			if params.Cursor == "next" {
				writeHelperResponse(message.ID, map[string]any{"tools": []any{helperTool("second")}})
			} else {
				writeHelperResponse(message.ID, map[string]any{"tools": []any{helperTool("echo")}, "nextCursor": "next"})
			}
		case "tools/call":
			if mode == "hang" {
				continue
			}
			writeHelperResponse(message.ID, map[string]any{
				"content": []any{map[string]any{"type": "text", "text": string(message.Params)}},
			})
		case "notifications/cancelled":
		default:
			writeHelperError(message.ID, -32601, "unknown method")
		}
	}
	os.Exit(0)
}

func helperTool(name string) map[string]any {
	return map[string]any{
		"name":        name,
		"description": name + " tool",
		"inputSchema": map[string]any{"type": "object"},
	}
}

func writeHelperResponse(id int64, result any) {
	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	fmt.Println(string(payload))
}

func writeHelperError(id int64, code int, message string) {
	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}})
	fmt.Println(string(payload))
}
