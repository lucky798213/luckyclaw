package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lucky798213/luckyclaw/internal/provider"
)

type managerTestProvider struct{}

func (managerTestProvider) Chat(_ context.Context, _ []provider.Message, _ []provider.Tool, _ string, _ int, _ float64) (*provider.Message, error) {
	return &provider.Message{Role: "assistant", Content: "reply"}, nil
}

func managerTestAgent(t *testing.T, id string) *Agent {
	t.Helper()
	soulPath := filepath.Join(t.TempDir(), "SOUL.md")
	if err := os.WriteFile(soulPath, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	providers := provider.NewManager()
	if err := providers.Register("test", managerTestProvider{}, []string{id}); err != nil {
		t.Fatal(err)
	}
	current, err := New(Options{
		ID:           id,
		Name:         id,
		DefaultModel: "test/" + id,
		Models:       []string{"test/" + id},
		SoulPath:     soulPath,
		Tools:        newDefaultToolRegistry(t),
	}, providers)
	if err != nil {
		t.Fatal(err)
	}
	return current
}

func TestAgentManagerLookupDefaultAndSortedList(t *testing.T) {
	alpha := managerTestAgent(t, "alpha")
	zeta := managerTestAgent(t, "zeta")
	manager, err := NewManager(map[string]*Agent{"zeta": zeta, "alpha": alpha}, "zeta")
	if err != nil {
		t.Fatal(err)
	}
	if manager.AgentByID("alpha") != alpha || manager.AgentByID("missing") != nil {
		t.Fatal("AgentByID() returned an unexpected result")
	}
	if manager.DefaultAgent() != zeta {
		t.Fatal("DefaultAgent() returned an unexpected Agent")
	}
	all := manager.All()
	if len(all) != 2 || all[0] != alpha || all[1] != zeta {
		t.Fatalf("All() = %#v", all)
	}
}

func TestAgentManagerValidatesInput(t *testing.T) {
	alpha := managerTestAgent(t, "alpha")
	tests := []struct {
		name         string
		agents       map[string]*Agent
		defaultAgent string
	}{
		{name: "空 Agent 列表", agents: nil, defaultAgent: "alpha"},
		{name: "空 Agent 实例", agents: map[string]*Agent{"alpha": nil}, defaultAgent: "alpha"},
		{name: "映射键与 ID 不符", agents: map[string]*Agent{"other": alpha}, defaultAgent: "other"},
		{name: "默认 Agent 不存在", agents: map[string]*Agent{"alpha": alpha}, defaultAgent: "missing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewManager(test.agents, test.defaultAgent); err == nil {
				t.Fatal("NewManager() error = nil")
			}
		})
	}
}
