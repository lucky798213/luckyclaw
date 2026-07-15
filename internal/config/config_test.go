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
    api_key_env: DEEPSEEK_API_KEY
    api_base: https://deepseek.example/v1
    api_type: openai-chat
    auth_type: bearer-token
    models:
      - deepseek-chat
      - deepseek-reasoner
  openrouter:
    api_key_env: OPENROUTER_API_KEY
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
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-key")
	t.Setenv("OPENROUTER_API_KEY", "openrouter-key")
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
	if got := cfg.Providers["deepseek"]; got.APIKeyEnv != "DEEPSEEK_API_KEY" || got.APIKey != "deepseek-key" {
		t.Fatalf("deepseek provider = %+v", got)
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
	wantTaskQueue := TaskQueueConfig{
		MaxConcurrent:             10,
		TaskTimeoutSeconds:        30,
		MaxPendingPerConversation: 100,
	}
	if cfg.TaskQueue != wantTaskQueue {
		t.Fatalf("task queue = %+v, want %+v", cfg.TaskQueue, wantTaskQueue)
	}
	if cfg.Storage.Path != "data/luckyclaw.db" {
		t.Fatalf("storage path = %q", cfg.Storage.Path)
	}
}

func TestLoadFileRequiresProviderAPIKeyEnv(t *testing.T) {
	content := strings.Replace(validConfigYAML, "    api_key_env: DEEPSEEK_API_KEY\n", "", 1)
	_, err := LoadFile(writeConfig(t, content))
	if err == nil || !strings.Contains(err.Error(), `provider "deepseek": api_key_env is required`) {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadFileRejectsMissingProviderAPIKeyEnvironmentVariable(t *testing.T) {
	const envName = "LUCKYCLAW_TEST_MISSING_API_KEY"
	unsetEnv(t, envName)
	content := strings.Replace(validConfigYAML, "DEEPSEEK_API_KEY", envName, 1)
	_, err := LoadFile(writeConfig(t, content))
	if err == nil || !strings.Contains(err.Error(), `environment variable "`+envName+`" is not set or empty`) {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadFileRejectsEmptyProviderAPIKeyEnvironmentVariable(t *testing.T) {
	const envName = "LUCKYCLAW_TEST_EMPTY_API_KEY"
	t.Setenv(envName, "   ")
	content := strings.Replace(validConfigYAML, "DEEPSEEK_API_KEY", envName, 1)
	_, err := LoadFile(writeConfig(t, content))
	if err == nil || !strings.Contains(err.Error(), `environment variable "`+envName+`" is not set or empty`) {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadFileRejectsInlineProviderAPIKey(t *testing.T) {
	content := strings.Replace(validConfigYAML, "api_key_env: DEEPSEEK_API_KEY", "api_key: secret", 1)
	_, err := LoadFile(writeConfig(t, content))
	if err == nil || !strings.Contains(err.Error(), "field api_key not found") {
		t.Fatalf("error = %v", err)
	}
}

func unsetEnv(t *testing.T, name string) {
	t.Helper()
	value, exists := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if exists {
			_ = os.Setenv(name, value)
			return
		}
		_ = os.Unsetenv(name)
	})
}

func TestLoadFileLoadsStoragePath(t *testing.T) {
	content := strings.Replace(validConfigYAML, "default_agent: lucky", `default_agent: lucky
storage:
  path: var/sessions.db`, 1)
	cfg, err := LoadFile(writeConfig(t, content))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.Path != "var/sessions.db" {
		t.Fatalf("storage path = %q", cfg.Storage.Path)
	}
}

func TestLoadFileLoadsTaskQueueConfig(t *testing.T) {
	content := strings.Replace(validConfigYAML, "default_agent: lucky", `default_agent: lucky
task_queue:
  max_concurrent: 3
  task_timeout_seconds: 45
  max_pending_per_conversation: 20`, 1)
	cfg, err := LoadFile(writeConfig(t, content))
	if err != nil {
		t.Fatal(err)
	}
	want := TaskQueueConfig{
		MaxConcurrent:             3,
		TaskTimeoutSeconds:        45,
		MaxPendingPerConversation: 20,
	}
	if cfg.TaskQueue != want {
		t.Fatalf("task queue = %+v, want %+v", cfg.TaskQueue, want)
	}
}

func TestLoadFileLoadsThreadBinding(t *testing.T) {
	content := validConfigYAML + `
  - channel: feishu
    account_id: bot
    chat_id: coder-chat
    thread_id: topic-1
    agent_id: lucky
`
	cfg, err := LoadFile(writeConfig(t, content))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Bindings) != 3 || cfg.Bindings[2].ThreadID != "topic-1" {
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
			want: "duplicates channel/account",
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
			name: "重复线程绑定",
			content: validConfigYAML + `
  - channel: feishu
    account_id: bot
    chat_id: coder-chat
    thread_id: topic-1
    agent_id: lucky
  - channel: feishu
    account_id: bot
    chat_id: coder-chat
    thread_id: topic-1
    agent_id: coder
`,
			want: "duplicates channel/account/chat/thread",
		},
		{
			name: "线程绑定缺少聊天",
			content: validConfigYAML + `
  - channel: feishu
    account_id: bot
    thread_id: topic-1
    agent_id: lucky
`,
			want: "thread_id requires chat_id",
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
		{
			name: "任务队列并发数为负数",
			content: strings.Replace(validConfigYAML, "default_agent: lucky", `default_agent: lucky
task_queue:
  max_concurrent: -1`, 1),
			want: "task_queue.max_concurrent",
		},
		{
			name: "任务队列超时为负数",
			content: strings.Replace(validConfigYAML, "default_agent: lucky", `default_agent: lucky
task_queue:
  task_timeout_seconds: -1`, 1),
			want: "task_queue.task_timeout_seconds",
		},
		{
			name: "任务队列积压上限为负数",
			content: strings.Replace(validConfigYAML, "default_agent: lucky", `default_agent: lucky
task_queue:
  max_pending_per_conversation: -1`, 1),
			want: "task_queue.max_pending_per_conversation",
		},
		{
			name: "数据库路径只有空白",
			content: strings.Replace(validConfigYAML, "default_agent: lucky", `default_agent: lucky
storage:
  path: "   "`, 1),
			want: "storage.path",
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
