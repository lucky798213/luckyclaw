package provider

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ModelRef 表示一个带服务商前缀的完整模型引用。
type ModelRef struct {
	ProviderKey string
	ModelID     string
}

// ParseModelRef 将 provider/model 形式的引用拆分为服务商和模型标识。
// 只拆分第一个斜杠，因此模型标识本身仍然可以包含斜杠。
func ParseModelRef(raw string) (ModelRef, error) {
	raw = strings.TrimSpace(raw)
	index := strings.Index(raw, "/")
	if index <= 0 || index == len(raw)-1 {
		return ModelRef{}, fmt.Errorf("模型引用 %q 格式错误，应为 provider/model", raw)
	}
	ref := ModelRef{
		ProviderKey: strings.TrimSpace(raw[:index]),
		ModelID:     strings.TrimSpace(raw[index+1:]),
	}
	if ref.ProviderKey == "" || ref.ModelID == "" {
		return ModelRef{}, fmt.Errorf("模型引用 %q 格式错误，应为 provider/model", raw)
	}
	return ref, nil
}

// String 返回标准的 provider/model 模型引用。
func (r ModelRef) String() string {
	return r.ProviderKey + "/" + r.ModelID
}

// Definition 保存创建一个 Provider 所需的配置和模型目录。描述 Provider 运行时创建需要什么参数
type Definition struct {
	APIKey   string
	APIBase  string
	APIType  string
	AuthType string
	Models   []string
}

// ProviderInfo 是不包含密钥的 Provider 可见信息。
type ProviderInfo struct {
	Name   string
	Models []string
}

// ResolvedModel 是一次模型调用最终解析出的 Provider 和模型标识。
type ResolvedModel struct {
	Ref      ModelRef
	Provider Provider
	ModelID  string
}

type providerEntry struct {
	provider Provider
	models   []string
	modelSet map[string]struct{}
}

// Manager 线程安全地管理所有已注册的大模型服务商。
type Manager struct {
	mu      sync.RWMutex
	entries map[string]providerEntry
}

// NewManager 创建一个空的 ProviderManager。
func NewManager() *Manager {
	return &Manager{entries: make(map[string]providerEntry)}
}

// RegisterAll 根据完整配置批量创建 Provider，并在全部成功后原子替换注册表。
func (m *Manager) RegisterAll(definitions map[string]Definition) error {
	if m == nil {
		return fmt.Errorf("provider manager cannot be nil")
	}
	if len(definitions) == 0 {
		return fmt.Errorf("provider definitions cannot be empty")
	}

	names := make([]string, 0, len(definitions))
	for name := range definitions {
		names = append(names, name)
	}
	sort.Strings(names)

	next := make(map[string]providerEntry, len(definitions))
	for _, name := range names {
		definition := definitions[name]
		prov, err := NewFromDefinition(definition)
		if err != nil {
			return fmt.Errorf("创建 Provider %q: %w", name, err)
		}
		entry, err := newProviderEntry(name, prov, definition.Models)
		if err != nil {
			return err
		}
		next[name] = entry
	}

	m.mu.Lock()
	m.entries = next
	m.mu.Unlock()
	return nil
}

// Register 注册一个新的 Provider，已存在同名 Provider 时返回错误。
func (m *Manager) Register(name string, prov Provider, models []string) error {
	entry, err := newProviderEntry(name, prov, models)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.entries[name]; exists {
		return fmt.Errorf("Provider %q 已注册", name)
	}
	m.entries[name] = entry
	return nil
}

// Replace 替换一个已经存在的 Provider。
func (m *Manager) Replace(name string, prov Provider, models []string) error {
	entry, err := newProviderEntry(name, prov, models)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.entries[name]; !exists {
		return fmt.Errorf("Provider %q 未注册", name)
	}
	m.entries[name] = entry
	return nil
}

// Delete 删除一个已经注册的 Provider。
func (m *Manager) Delete(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.entries[name]; !exists {
		return fmt.Errorf("Provider %q 未注册", name)
	}
	delete(m.entries, name)
	return nil
}

// Get 根据名称返回 Provider 实例。
func (m *Manager) Get(name string) (Provider, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.entries[name]
	return entry.provider, ok
}

// List 返回不包含密钥的 Provider 和模型目录。
func (m *Manager) List() []ProviderInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.entries))
	for name := range m.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]ProviderInfo, 0, len(names))
	for _, name := range names {
		models := append([]string(nil), m.entries[name].models...)
		result = append(result, ProviderInfo{Name: name, Models: models})
	}
	return result
}

// Resolve 将完整模型引用解析为可直接调用的 Provider 和原始模型标识。
func (m *Manager) Resolve(raw string) (ResolvedModel, error) {
	ref, err := ParseModelRef(raw)
	if err != nil {
		return ResolvedModel{}, err
	}
	m.mu.RLock()
	entry, exists := m.entries[ref.ProviderKey]
	m.mu.RUnlock()
	if !exists {
		return ResolvedModel{}, fmt.Errorf("Provider %q 未注册", ref.ProviderKey)
	}
	if _, exists := entry.modelSet[ref.ModelID]; !exists {
		return ResolvedModel{}, fmt.Errorf("Provider %q 未配置模型 %q", ref.ProviderKey, ref.ModelID)
	}
	return ResolvedModel{Ref: ref, Provider: entry.provider, ModelID: ref.ModelID}, nil
}

func newProviderEntry(name string, prov Provider, models []string) (providerEntry, error) {
	if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) {
		return providerEntry{}, fmt.Errorf("Provider 名称不能为空或包含首尾空白")
	}
	if prov == nil {
		return providerEntry{}, fmt.Errorf("Provider %q 实例不能为空", name)
	}
	normalized, modelSet, err := normalizeModels(models)
	if err != nil {
		return providerEntry{}, fmt.Errorf("Provider %q: %w", name, err)
	}
	return providerEntry{provider: prov, models: normalized, modelSet: modelSet}, nil
}

func normalizeModels(models []string) ([]string, map[string]struct{}, error) {
	if len(models) == 0 {
		return nil, nil, fmt.Errorf("模型目录不能为空")
	}
	normalized := make([]string, 0, len(models))
	modelSet := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			return nil, nil, fmt.Errorf("模型标识不能为空")
		}
		if _, exists := modelSet[model]; exists {
			return nil, nil, fmt.Errorf("模型 %q 重复", model)
		}
		modelSet[model] = struct{}{}
		normalized = append(normalized, model)
	}
	return normalized, modelSet, nil
}
