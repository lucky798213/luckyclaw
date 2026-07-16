package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/lucky798213/luckyclaw/internal/provider"
)

type stubTool struct {
	definition provider.Tool
	result     string
	err        error
}

func (t *stubTool) Definition() provider.Tool { return t.definition }

func (t *stubTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return t.result, t.err
}

func stubDefinition(name string) provider.Tool {
	return functionDefinition(name, name+" description", map[string]any{"type": "object"})
}

func TestNewRegistrySortsDefinitionsAndExecutesTools(t *testing.T) {
	registry, err := NewRegistry(
		&stubTool{definition: stubDefinition("zeta"), result: "z"},
		&stubTool{definition: stubDefinition("alpha"), result: "a"},
	)
	if err != nil {
		t.Fatal(err)
	}
	definitions := registry.Definitions()
	gotNames := []string{definitions[0].Function.Name, definitions[1].Function.Name}
	if !reflect.DeepEqual(gotNames, []string{"alpha", "zeta"}) {
		t.Fatalf("definition names = %v", gotNames)
	}
	result, err := registry.Execute(context.Background(), "alpha", json.RawMessage(`{}`))
	if err != nil || result != "a" {
		t.Fatalf("Execute() = %q, %v", result, err)
	}
	definitions[0].Function.Name = "changed"
	if registry.Definitions()[0].Function.Name != "alpha" {
		t.Fatal("Definitions() returned mutable registry storage")
	}
}

func TestNewRegistryRejectsInvalidTools(t *testing.T) {
	tests := []struct {
		name  string
		tools []Tool
		want  string
	}{
		{name: "空工具", tools: []Tool{nil}, want: "cannot be nil"},
		{name: "空名称", tools: []Tool{&stubTool{definition: stubDefinition("")}}, want: "name cannot be empty"},
		{name: "错误类型", tools: []Tool{&stubTool{definition: provider.Tool{Type: "mcp", Function: provider.ToolFunction{Name: "bad", Parameters: map[string]any{}}}}}, want: "type must be function"},
		{name: "空参数", tools: []Tool{&stubTool{definition: provider.Tool{Type: "function", Function: provider.ToolFunction{Name: "bad"}}}}, want: "parameters cannot be nil"},
		{name: "重名", tools: []Tool{&stubTool{definition: stubDefinition("same")}, &stubTool{definition: stubDefinition("same")}}, want: "duplicated"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewRegistry(test.tools...)
			if err == nil || !contains(err.Error(), test.want) {
				t.Fatalf("NewRegistry() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRegistryReturnsToolAndUnknownToolErrors(t *testing.T) {
	wantErr := errors.New("failed")
	registry, err := NewRegistry(&stubTool{definition: stubDefinition("broken"), result: "partial", err: wantErr})
	if err != nil {
		t.Fatal(err)
	}
	result, err := registry.Execute(context.Background(), "broken", json.RawMessage(`{}`))
	if result != "partial" || !errors.Is(err, wantErr) {
		t.Fatalf("broken Execute() = %q, %v", result, err)
	}
	if _, err := registry.Execute(context.Background(), "missing", json.RawMessage(`{}`)); err == nil || !contains(err.Error(), "unknown tool") {
		t.Fatalf("unknown Execute() error = %v", err)
	}
}

func TestDefaultRegistryContainsSafeTools(t *testing.T) {
	registry, err := NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	definitions := registry.Definitions()
	got := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		got = append(got, definition.Function.Name)
	}
	want := []string{"calculator", "current_time", "http_fetch"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("default tools = %v, want %v", got, want)
	}
}

func contains(value, substring string) bool {
	return len(substring) == 0 || len(value) >= len(substring) && stringContains(value, substring)
}

func stringContains(value, substring string) bool {
	for index := 0; index+len(substring) <= len(value); index++ {
		if value[index:index+len(substring)] == substring {
			return true
		}
	}
	return false
}
