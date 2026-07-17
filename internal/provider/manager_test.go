package provider

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

type managerFakeProvider struct {
	name string
}

func (p *managerFakeProvider) Chat(_ context.Context, _ []Message, _ []Tool, _ string, _ int, _ float64) (*Message, error) {
	return &Message{Role: "assistant", Content: p.name}, nil
}

func (p *managerFakeProvider) ChatStream(_ context.Context, _ []Message, _ []Tool, _ string, _ int, _ float64) (Stream, error) {
	return nil, fmt.Errorf("stream is not used in manager tests")
}

func TestParseModelRefKeepsNestedModelID(t *testing.T) {
	ref, err := ParseModelRef("openrouter/meta-llama/llama-3.3")
	if err != nil {
		t.Fatal(err)
	}
	if ref.ProviderKey != "openrouter" || ref.ModelID != "meta-llama/llama-3.3" {
		t.Fatalf("ref = %+v", ref)
	}
	if ref.String() != "openrouter/meta-llama/llama-3.3" {
		t.Fatalf("ref.String() = %q", ref.String())
	}
}

func TestManagerRegisterResolveListReplaceAndDelete(t *testing.T) {
	manager := NewManager()
	first := &managerFakeProvider{name: "first"}
	if err := manager.Register("zeta", first, []string{"model-b", "model-a"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Register("alpha", &managerFakeProvider{name: "alpha"}, []string{"vendor/model"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Register("zeta", first, []string{"model-c"}); err == nil {
		t.Fatal("duplicate Register() error = nil")
	}

	resolved, err := manager.Resolve("alpha/vendor/model")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ModelID != "vendor/model" || resolved.Provider == nil {
		t.Fatalf("resolved = %+v", resolved)
	}

	wantList := []ProviderInfo{
		{Name: "alpha", Models: []string{"vendor/model"}},
		{Name: "zeta", Models: []string{"model-b", "model-a"}},
	}
	if got := manager.List(); !reflect.DeepEqual(got, wantList) {
		t.Fatalf("List() = %#v, want %#v", got, wantList)
	}
	listed := manager.List()
	listed[0].Models[0] = "changed"
	if got := manager.List()[0].Models[0]; got != "vendor/model" {
		t.Fatalf("List() exposed internal models: %q", got)
	}

	replacement := &managerFakeProvider{name: "replacement"}
	if err := manager.Replace("zeta", replacement, []string{"new-model"}); err != nil {
		t.Fatal(err)
	}
	if got, ok := manager.Get("zeta"); !ok || got != replacement {
		t.Fatalf("Get() = %#v, %v", got, ok)
	}
	if _, err := manager.Resolve("zeta/model-a"); err == nil {
		t.Fatal("Resolve() accepted model removed by Replace")
	}
	if err := manager.Delete("zeta"); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.Get("zeta"); ok {
		t.Fatal("Delete() kept provider")
	}
	if err := manager.Delete("zeta"); err == nil {
		t.Fatal("second Delete() error = nil")
	}
}

func TestManagerResolveRejectsUnknownProviderAndModel(t *testing.T) {
	manager := NewManager()
	if err := manager.Register("known", &managerFakeProvider{}, []string{"chat"}); err != nil {
		t.Fatal(err)
	}
	for _, modelRef := range []string{"missing/chat", "known/missing", "invalid"} {
		if _, err := manager.Resolve(modelRef); err == nil {
			t.Fatalf("Resolve(%q) error = nil", modelRef)
		}
	}
}

func TestManagerRegisterAllIsAtomic(t *testing.T) {
	manager := NewManager()
	old := &managerFakeProvider{name: "old"}
	if err := manager.Register("old", old, []string{"chat"}); err != nil {
		t.Fatal(err)
	}

	err := manager.RegisterAll(map[string]Definition{
		"valid": {
			APIKey: "key", APIBase: "https://valid.example/v1", APIType: "openai-chat", AuthType: "bearer-token", Models: []string{"chat"},
		},
		"invalid": {
			APIKey: "key", APIBase: "https://invalid.example/v1", APIType: "anthropic", AuthType: "bearer-token", Models: []string{"chat"},
		},
	})
	if err == nil {
		t.Fatal("RegisterAll() error = nil")
	}
	if got, ok := manager.Get("old"); !ok || got != old {
		t.Fatal("failed RegisterAll() changed the existing registry")
	}
	if _, ok := manager.Get("valid"); ok {
		t.Fatal("failed RegisterAll() left a partially registered provider")
	}

	err = manager.RegisterAll(map[string]Definition{
		"deepseek": {
			APIKey: "key-1", APIBase: "https://deepseek.example/v1", APIType: "openai-chat", AuthType: "bearer-token", Models: []string{"deepseek-chat"},
		},
		"openrouter": {
			APIKey: "key-2", APIBase: "https://openrouter.example/v1", APIType: "openai", AuthType: "bearer", Models: []string{"vendor/model"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.Get("old"); ok {
		t.Fatal("successful RegisterAll() did not replace the old registry")
	}
	if _, err := manager.Resolve("openrouter/vendor/model"); err != nil {
		t.Fatal(err)
	}
}

func TestValidateDefinitionRejectsUnsupportedTypes(t *testing.T) {
	base := Definition{
		APIKey: "key", APIBase: "https://example.com/v1", APIType: "openai-chat", AuthType: "bearer-token", Models: []string{"chat"},
	}
	if err := ValidateDefinition(base); err != nil {
		t.Fatal(err)
	}

	invalidAPI := base
	invalidAPI.APIType = "anthropic"
	if err := ValidateDefinition(invalidAPI); err == nil {
		t.Fatal("unsupported api type error = nil")
	}
	invalidAuth := base
	invalidAuth.AuthType = "api-key"
	if err := ValidateDefinition(invalidAuth); err == nil {
		t.Fatal("unsupported auth type error = nil")
	}
}
