package provider

import (
	"fmt"
	"strings"
)

// ValidateDefinition 校验 Provider 工厂可以识别的协议和鉴权配置。
func ValidateDefinition(definition Definition) error {
	if strings.TrimSpace(definition.APIKey) == "" {
		return fmt.Errorf("api key cannot be empty")
	}
	if strings.TrimSpace(definition.APIBase) == "" {
		return fmt.Errorf("api base cannot be empty")
	}
	apiType := strings.ToLower(strings.TrimSpace(definition.APIType))
	if apiType != "openai-chat" && apiType != "openai" {
		return fmt.Errorf("unsupported api type %q", definition.APIType)
	}
	authType := strings.ToLower(strings.TrimSpace(definition.AuthType))
	if authType != "bearer-token" && authType != "bearer" {
		return fmt.Errorf("unsupported auth type %q", definition.AuthType)
	}
	if _, _, err := normalizeModels(definition.Models); err != nil {
		return err
	}
	return nil
}

// NewFromDefinition 根据配置创建一个 OpenAI Chat Completions 兼容 Provider。
func NewFromDefinition(definition Definition) (Provider, error) {
	if err := ValidateDefinition(definition); err != nil {
		return nil, err
	}
	return NewOpenAI(definition.APIKey, definition.APIBase)
}
