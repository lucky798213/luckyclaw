// Package config 负责加载应用配置。
package config

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

const defaultConfigPath = "config.yaml"

// Config 保存 LuckyClaw 的运行配置。
type Config struct {
	Providers map[string]ProviderConfig `yaml:"providers"`
}

// ProviderConfig 保存一个大模型服务商的连接配置。
type ProviderConfig struct {
	// APIKey 是调用服务商接口使用的密钥。
	APIKey string `yaml:"api_key"`
	// APIBase 是服务商 API 地址。
	APIBase string `yaml:"api_base"`
	// APIType 是接口协议类型，决定创建哪种 Provider 实现。
	APIType string `yaml:"api_type,omitempty"`
	// AuthType 是认证方式，例如 Bearer Token 或 API Key Header。
	AuthType string `yaml:"auth_type,omitempty"`
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

	if len(cfg.Providers) == 0 {
		return nil, fmt.Errorf("config must contain at least one provider")
	}

	for name, providerCfg := range cfg.Providers {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("provider name cannot be empty")
		}

		if strings.TrimSpace(providerCfg.APIKey) == "" {
			return nil, fmt.Errorf("provider %q api_key is required", name)
		}

		if strings.TrimSpace(providerCfg.APIBase) == "" {
			return nil, fmt.Errorf("provider %q api_base is required", name)
		}
	}

	return &cfg, nil
}
