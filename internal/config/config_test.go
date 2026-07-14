package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validConfigYAML = `
providers:
  deepseek:
    api_key: deepseek-key
    api_base: https://deepseek.example/v1
    api_type: openai-chat
    auth_type: bearer-token
    models:
      - deepseek-chat
      - deepseek-reasoner
  openrouter:
    api_key: openrouter-key
    api_base: https://openrouter.example/v1
    api_type: openai
    auth_type: bearer
    models:
      - meta-llama/llama-3.3
agents:
  lucky:
    name: LuckyClaw
    soul_path: SOUL.md
    default_model: deepseek/deepseek-chat
    models:
      - deepseek/deepseek-chat
      - openrouter/meta-llama/llama-3.3
  coder:
    name: Coder
    soul_path: CODER.md
    default_model: deepseek/deepseek-reasoner
    models:
      - deepseek/deepseek-reasoner
default_agent: lucky
bindings:
  - channel: terminal
    account_id: local
    agent_id: lucky
  - channel: feishu
    account_id: bot
    chat_id: coder-chat
    agent_id: coder
`

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadFileLoadsMultipleProvidersAgentsAndBindings(t *testing.T) {
	cfg, err := LoadFile(writeConfig(t, validConfigYAML))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers) != 2 || len(cfg.Agents) != 2 {
		t.Fatalf("providers = %d, agents = %d", len(cfg.Providers), len(cfg.Agents))
	}
	if cfg.DefaultAgent != "lucky" {
		t.Fatalf("default agent = %q", cfg.DefaultAgent)
	}
	if got := cfg.Agents["lucky"].Models[1]; got != "openrouter/meta-llama/llama-3.3" {
		t.Fatalf("nested model ref = %q", got)
	}
	if len(cfg.Bindings) != 2 || cfg.Bindings[1].ChatID != "coder-chat" {
		t.Fatalf("bindings = %+v", cfg.Bindings)
	}
}

func TestLoadFileStrictValidation(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "缺少默认 Agent",
			content: strings.Replace(validConfigYAML, "default_agent: lucky", "default_agent: missing", 1),
			want:    "does not exist",
		},
		{
			name:    "Agent 引用未知 Provider",
			content: strings.Replace(validConfigYAML, "openrouter/meta-llama/llama-3.3", "missing/meta-llama/llama-3.3", 1),
			want:    "unknown provider",
		},
		{
			name:    "Agent 引用未知模型",
			content: strings.Replace(validConfigYAML, "      - deepseek/deepseek-chat\n      - openrouter", "      - deepseek/not-configured\n      - openrouter", 1),
			want:    "unknown model",
		},
		{
			name:    "默认模型不在白名单",
			content: strings.Replace(validConfigYAML, "default_model: deepseek/deepseek-chat", "default_model: deepseek/deepseek-reasoner", 1),
			want:    "is not in its models list",
		},
		{
			name:    "绑定引用未知 Agent",
			content: strings.Replace(validConfigYAML, "    agent_id: lucky", "    agent_id: missing", 1),
			want:    "references unknown agent",
		},
		{
			name: "重复账号绑定",
			content: validConfigYAML + `
  - channel: terminal
    account_id: local
    agent_id: coder
`,
			want: "duplicates channel/account/chat",
		},
		{
			name: "重复聊天绑定",
			content: validConfigYAML + `
  - channel: feishu
    account_id: bot
    chat_id: coder-chat
    agent_id: lucky
`,
			want: "duplicates channel/account/chat",
		},
		{
			name:    "Provider 模型目录为空",
			content: strings.Replace(validConfigYAML, "    models:\n      - meta-llama/llama-3.3", "    models: []", 1),
			want:    "模型目录不能为空",
		},
		{
			name:    "Agent 模型列表为空",
			content: strings.Replace(validConfigYAML, "    models:\n      - deepseek/deepseek-reasoner\ndefault_agent", "    models: []\ndefault_agent", 1),
			want:    "models cannot be empty",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := LoadFile(writeConfig(t, test.content))
			if err == nil {
				t.Fatal("LoadFile() error = nil")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %q, want substring %q", err, test.want)
			}
		})
	}
}

func TestLoadFileRejectsLegacyProviderOnlyConfig(t *testing.T) {
	legacy := `
providers:
  deepseek:
    api_key: key
    api_base: https://deepseek.example/v1
    api_type: openai-chat
    auth_type: bearer-token
`
	_, err := LoadFile(writeConfig(t, legacy))
	if err == nil {
		t.Fatal("LoadFile() accepted legacy provider-only config")
	}
}

func TestLoadFileRejectsUnknownFields(t *testing.T) {
	content := strings.Replace(validConfigYAML, "default_agent: lucky", "unknown_field: value\ndefault_agent: lucky", 1)
	_, err := LoadFile(writeConfig(t, content))
	if err == nil || !strings.Contains(err.Error(), "field unknown_field not found") {
		t.Fatalf("error = %v", err)
	}
}
