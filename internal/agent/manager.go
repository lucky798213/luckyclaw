package agent

import (
	"fmt"
	"sort"
	"sync"
)

// Manager 线程安全地管理所有 Agent 和默认 Agent。
type Manager struct {
	mu             sync.RWMutex
	agents         map[string]*Agent
	defaultAgentID string
}

// NewManager 创建 AgentManager，并校验默认 Agent 是否存在。
func NewManager(agents map[string]*Agent, defaultAgentID string) (*Manager, error) {
	if len(agents) == 0 {
		return nil, fmt.Errorf("agents cannot be empty")
	}
	copyAgents := make(map[string]*Agent, len(agents))
	for id, current := range agents {
		if current == nil {
			return nil, fmt.Errorf("agent %q cannot be nil", id)
		}
		if id != current.ID() {
			return nil, fmt.Errorf("agent map key %q does not match id %q", id, current.ID())
		}
		copyAgents[id] = current
	}
	if _, exists := copyAgents[defaultAgentID]; !exists {
		return nil, fmt.Errorf("default agent %q does not exist", defaultAgentID)
	}
	return &Manager{agents: copyAgents, defaultAgentID: defaultAgentID}, nil
}

// AgentByID 根据稳定标识查找 Agent。
func (m *Manager) AgentByID(id string) *Agent {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.agents[id]
}

// DefaultAgent 返回默认 Agent。
func (m *Manager) DefaultAgent() *Agent {
	return m.AgentByID(m.defaultAgentID)
}

// All 返回按 Agent ID 排序的实例列表。
func (m *Manager) All() []*Agent {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.agents))
	for id := range m.agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]*Agent, 0, len(ids))
	for _, id := range ids {
		result = append(result, m.agents[id])
	}
	return result
}
