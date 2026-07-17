// Package config 负责加载应用配置。
package config

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lucky798213/luckyclaw/internal/bus"
	"github.com/lucky798213/luckyclaw/internal/provider"
)

const defaultConfigPath = "config.yaml"

const (
	defaultTaskQueueMaxConcurrent             = 10
	defaultTaskQueueTimeoutSeconds            = 30
	defaultTaskQueueMaxPendingPerConversation = 100
	defaultStoragePath                        = "data/luckyclaw.db"
	defaultWebListen                          = "127.0.0.1:8080"
	defaultMaxToolIterations                  = 20
	defaultToolTimeoutSeconds                 = 30
)

// Config 保存 LuckyClaw 的运行配置。
type Config struct {
	Providers    map[string]ProviderConfig `yaml:"providers"`
	Agents       map[string]AgentConfig    `yaml:"agents"`
	DefaultAgent string                    `yaml:"default_agent"`
	Bindings     []BindingConfig           `yaml:"bindings"`
	TaskQueue    TaskQueueConfig           `yaml:"task_queue,omitempty"`
	Storage      StorageConfig             `yaml:"storage,omitempty"`
	Web          WebConfig                 `yaml:"web,omitempty"`
}

// WebConfig 保存网页工作台的监听配置。
type WebConfig struct {
	Listen string `yaml:"listen,omitempty"`
}

// WithDefaults 返回补齐默认监听地址后的网页配置。
func (c WebConfig) WithDefaults() WebConfig {
	if strings.TrimSpace(c.Listen) == "" {
		c.Listen = defaultWebListen
	}
	return c
}

// StorageConfig 保存本地持久化配置。
type StorageConfig struct {
	Path string `yaml:"path,omitempty"`
}

// WithDefaults 返回补齐默认数据库路径后的存储配置。
func (c StorageConfig) WithDefaults() StorageConfig {
	if c.Path == "" {
		c.Path = defaultStoragePath
	}
	return c
}

// TaskQueueConfig 保存 Gateway 任务队列的并发、超时和积压限制。
type TaskQueueConfig struct {
	MaxConcurrent             int `yaml:"max_concurrent,omitempty"`
	TaskTimeoutSeconds        int `yaml:"task_timeout_seconds,omitempty"`
	MaxPendingPerConversation int `yaml:"max_pending_per_conversation,omitempty"`
}

// WithDefaults 返回补齐默认值后的任务队列配置，零值表示使用默认值。
func (c TaskQueueConfig) WithDefaults() TaskQueueConfig {
	if c.MaxConcurrent == 0 {
		c.MaxConcurrent = defaultTaskQueueMaxConcurrent
	}
	if c.TaskTimeoutSeconds == 0 {
		c.TaskTimeoutSeconds = defaultTaskQueueTimeoutSeconds
	}
	if c.MaxPendingPerConversation == 0 {
		c.MaxPendingPerConversation = defaultTaskQueueMaxPendingPerConversation
	}
	return c
}

// ProviderConfig 保存一个大模型服务商的连接配置。
type ProviderConfig struct {
	// APIKeyEnv 是保存服务商密钥的环境变量名。
	APIKeyEnv string `yaml:"api_key_env"`
	// APIKey 是从环境变量解析出的运行时密钥，不从 YAML 读取。
	APIKey string `yaml:"-"`
	// APIBase 是服务商 API 地址。
	APIBase string `yaml:"api_base"`
	// APIType 是接口协议类型，决定创建哪种 Provider 实现。
	APIType string `yaml:"api_type,omitempty"`
	// AuthType 是认证方式，例如 Bearer Token 或 API Key Header。
	AuthType string `yaml:"auth_type,omitempty"`
	// Models 是该服务商允许调用的模型目录。
	Models []string `yaml:"models"`
}

// AgentConfig 保存一个 Agent 的模型白名单和运行参数。
type AgentConfig struct {
	Name               string   `yaml:"name"`
	SoulPath           string   `yaml:"soul_path"`
	DefaultModel       string   `yaml:"default_model"`
	Models             []string `yaml:"models"`
	MaxToolIterations  int      `yaml:"max_tool_iterations,omitempty"`
	ToolTimeoutSeconds int      `yaml:"tool_timeout_seconds,omitempty"`
}

// WithDefaults 返回补齐工具循环默认值后的 Agent 配置。
func (c AgentConfig) WithDefaults() AgentConfig {
	if c.MaxToolIterations == 0 {
		c.MaxToolIterations = defaultMaxToolIterations
	}
	if c.ToolTimeoutSeconds == 0 {
		c.ToolTimeoutSeconds = defaultToolTimeoutSeconds
	}
	return c
}

// BindingConfig 描述一条渠道消息到 Agent 的绑定规则。
type BindingConfig struct {
	Channel   string `yaml:"channel"`
	AccountID string `yaml:"account_id"`
	ChatID    string `yaml:"chat_id,omitempty"`
	ThreadID  string `yaml:"thread_id,omitempty"`
	AgentID   string `yaml:"agent_id"`
}

// Load 从默认的 config.yaml 文件加载配置。
func Load() (*Config, error) {
	return LoadFile(defaultConfigPath)
}

// LoadFile 从指定的 YAML 文件加载配置。
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file %q: %w", path, err)
	}

	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode config file %q: %w", path, err)
	}
	if err := resolveProviderAPIKeys(&cfg); err != nil {
		return nil, fmt.Errorf("resolve config file %q: %w", path, err)
	}
	cfg.TaskQueue = cfg.TaskQueue.WithDefaults()
	cfg.Storage = cfg.Storage.WithDefaults()
	cfg.Web = cfg.Web.WithDefaults()
	for agentID, agentCfg := range cfg.Agents {
		cfg.Agents[agentID] = agentCfg.WithDefaults()
	}

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("validate config file %q: %w", path, err)
	}

	return &cfg, nil
}

// resolveProviderAPIKeys 按 Provider 配置的环境变量名解析运行时密钥。
func resolveProviderAPIKeys(cfg *Config) error {
	for _, name := range sortedKeys(cfg.Providers) {
		providerCfg := cfg.Providers[name]
		envName := strings.TrimSpace(providerCfg.APIKeyEnv)
		if envName == "" {
			return fmt.Errorf("provider %q: api_key_env is required", name)
		}
		apiKey, exists := os.LookupEnv(envName)
		if !exists || strings.TrimSpace(apiKey) == "" {
			return fmt.Errorf("provider %q: environment variable %q is not set or empty", name, envName)
		}
		providerCfg.APIKeyEnv = envName
		providerCfg.APIKey = apiKey
		cfg.Providers[name] = providerCfg
	}
	return nil
}

func validate(cfg *Config) error {
	if strings.TrimSpace(cfg.Storage.Path) == "" {
		return fmt.Errorf("storage.path cannot be empty")
	}
	if _, _, err := net.SplitHostPort(cfg.Web.Listen); err != nil {
		return fmt.Errorf("web.listen must be a valid host:port address: %w", err)
	}
	if cfg.TaskQueue.MaxConcurrent <= 0 {
		return fmt.Errorf("task_queue.max_concurrent must be greater than zero")
	}
	if cfg.TaskQueue.TaskTimeoutSeconds <= 0 {
		return fmt.Errorf("task_queue.task_timeout_seconds must be greater than zero")
	}
	if cfg.TaskQueue.MaxPendingPerConversation <= 0 {
		return fmt.Errorf("task_queue.max_pending_per_conversation must be greater than zero")
	}

	if len(cfg.Providers) == 0 {
		return fmt.Errorf("config must contain at least one provider")
	}
	providerModels := make(map[string]map[string]struct{}, len(cfg.Providers))
	providerNames := sortedKeys(cfg.Providers)
	for _, name := range providerNames {
		providerCfg := cfg.Providers[name]
		if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) {
			return fmt.Errorf("provider name cannot be empty or contain surrounding whitespace")
		}
		definition := provider.Definition{
			APIKey:   providerCfg.APIKey,
			APIBase:  providerCfg.APIBase,
			APIType:  providerCfg.APIType,
			AuthType: providerCfg.AuthType,
			Models:   providerCfg.Models,
		}
		if err := provider.ValidateDefinition(definition); err != nil {
			return fmt.Errorf("provider %q: %w", name, err)
		}
		models := make(map[string]struct{}, len(providerCfg.Models))
		for _, model := range providerCfg.Models {
			models[strings.TrimSpace(model)] = struct{}{}
		}
		providerModels[name] = models
	}

	if len(cfg.Agents) == 0 {
		return fmt.Errorf("config must contain at least one agent")
	}
	if strings.TrimSpace(cfg.DefaultAgent) == "" {
		return fmt.Errorf("default_agent is required")
	}
	if _, exists := cfg.Agents[cfg.DefaultAgent]; !exists {
		return fmt.Errorf("default_agent %q does not exist", cfg.DefaultAgent)
	}

	agentNames := sortedKeys(cfg.Agents)
	for _, agentID := range agentNames {
		agentCfg := cfg.Agents[agentID]
		if strings.TrimSpace(agentID) == "" || agentID != strings.TrimSpace(agentID) {
			return fmt.Errorf("agent id cannot be empty or contain surrounding whitespace")
		}
		if strings.TrimSpace(agentCfg.Name) == "" {
			return fmt.Errorf("agent %q name is required", agentID)
		}
		if strings.TrimSpace(agentCfg.SoulPath) == "" {
			return fmt.Errorf("agent %q soul_path is required", agentID)
		}
		if len(agentCfg.Models) == 0 {
			return fmt.Errorf("agent %q models cannot be empty", agentID)
		}
		if agentCfg.MaxToolIterations <= 0 {
			return fmt.Errorf("agent %q max_tool_iterations must be greater than zero", agentID)
		}
		if agentCfg.ToolTimeoutSeconds <= 0 {
			return fmt.Errorf("agent %q tool_timeout_seconds must be greater than zero", agentID)
		}
		allowed := make(map[string]struct{}, len(agentCfg.Models))
		for _, raw := range agentCfg.Models {
			ref, err := provider.ParseModelRef(raw)
			if err != nil {
				return fmt.Errorf("agent %q: %w", agentID, err)
			}
			if _, duplicate := allowed[ref.String()]; duplicate {
				return fmt.Errorf("agent %q model %q is duplicated", agentID, ref.String())
			}
			models, exists := providerModels[ref.ProviderKey]
			if !exists {
				return fmt.Errorf("agent %q references unknown provider %q", agentID, ref.ProviderKey)
			}
			if _, exists := models[ref.ModelID]; !exists {
				return fmt.Errorf("agent %q references unknown model %q on provider %q", agentID, ref.ModelID, ref.ProviderKey)
			}
			allowed[ref.String()] = struct{}{}
		}
		defaultRef, err := provider.ParseModelRef(agentCfg.DefaultModel)
		if err != nil {
			return fmt.Errorf("agent %q default_model: %w", agentID, err)
		}
		if _, exists := allowed[defaultRef.String()]; !exists {
			return fmt.Errorf("agent %q default_model %q is not in its models list", agentID, defaultRef.String())
		}
	}

	accountBindings := make(map[bus.ChannelAccount]struct{}, len(cfg.Bindings))
	chatBindings := make(map[bus.ConversationAddress]struct{}, len(cfg.Bindings))
	threadBindings := make(map[bus.ConversationAddress]struct{}, len(cfg.Bindings))
	for index, binding := range cfg.Bindings {
		if strings.TrimSpace(binding.Channel) == "" {
			return fmt.Errorf("binding %d channel is required", index)
		}
		if strings.TrimSpace(binding.AccountID) == "" {
			return fmt.Errorf("binding %d account_id is required", index)
		}
		if _, exists := cfg.Agents[binding.AgentID]; !exists {
			return fmt.Errorf("binding %d references unknown agent %q", index, binding.AgentID)
		}
		if binding.ThreadID != "" && binding.ChatID == "" {
			return fmt.Errorf("binding %d thread_id requires chat_id", index)
		}

		if binding.ChatID == "" {
			key := bus.ChannelAccount{Channel: binding.Channel, AccountID: binding.AccountID}
			if _, duplicate := accountBindings[key]; duplicate {
				return fmt.Errorf("binding %d duplicates channel/account", index)
			}
			accountBindings[key] = struct{}{}
			continue
		}

		key := bus.ConversationAddress{
			Channel:   binding.Channel,
			AccountID: binding.AccountID,
			ChatID:    binding.ChatID,
			ThreadID:  binding.ThreadID,
		}
		if binding.ThreadID != "" {
			if _, duplicate := threadBindings[key]; duplicate {
				return fmt.Errorf("binding %d duplicates channel/account/chat/thread", index)
			}
			threadBindings[key] = struct{}{}
			continue
		}
		if _, duplicate := chatBindings[key]; duplicate {
			return fmt.Errorf("binding %d duplicates channel/account/chat", index)
		}
		chatBindings[key] = struct{}{}
	}
	return nil
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
