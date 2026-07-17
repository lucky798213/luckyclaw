package sandbox

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

type executorFactory func(context.Context, DockerPolicy, string, []SkillMount) (SandboxExecutor, error)

type poolEntry struct {
	ready       chan struct{}
	executor    SandboxExecutor
	err         error
	mountDigest string
}

// DockerPool 为每个 Agent/session 惰性创建一个独立 DockerExecutor。
type DockerPool struct {
	mu            sync.Mutex
	policy        DockerPolicy
	workspaceRoot string
	factory       executorFactory
	entries       map[string]*poolEntry
	closed        bool
}

// NewDockerPool 创建 Docker 执行器池。
func NewDockerPool(policy DockerPolicy, workspaceRoot string) (*DockerPool, error) {
	if err := validateDockerPolicy(policy); err != nil {
		return nil, err
	}
	if strings.TrimSpace(workspaceRoot) == "" {
		return nil, fmt.Errorf("Docker workspace root cannot be empty")
	}
	return &DockerPool{
		policy:        policy,
		workspaceRoot: workspaceRoot,
		factory: func(ctx context.Context, policy DockerPolicy, workspace string, mounts []SkillMount) (SandboxExecutor, error) {
			return NewDockerExecutor(ctx, policy, workspace, mounts)
		},
		entries: make(map[string]*poolEntry),
	}, nil
}

// Get 返回当前 Agent/session 独享的执行器，并并发合并首次创建请求。
func (p *DockerPool) Get(ctx context.Context, agentID, sessionKey string, mounts []SkillMount) (SandboxExecutor, error) {
	workspace, err := WorkspaceDirectory(p.workspaceRoot, agentID, sessionKey)
	if err != nil {
		return nil, err
	}
	key := hashSegment(agentID) + "\x00" + hashSegment(sessionKey)
	digest := mountDigest(mounts)
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, fmt.Errorf("Docker executor pool is closed")
	}
	if existing := p.entries[key]; existing != nil {
		if existing.mountDigest != digest {
			p.mu.Unlock()
			return nil, fmt.Errorf("Docker sandbox skill mounts changed for active session")
		}
		ready := existing.ready
		p.mu.Unlock()
		select {
		case <-ready:
			return existing.executor, existing.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	entry := &poolEntry{ready: make(chan struct{}), mountDigest: digest}
	p.entries[key] = entry
	p.mu.Unlock()

	executor, createErr := p.factory(ctx, p.policy, workspace, append([]SkillMount(nil), mounts...))
	p.mu.Lock()
	entry.executor = executor
	entry.err = createErr
	if createErr != nil {
		delete(p.entries, key)
	}
	close(entry.ready)
	p.mu.Unlock()
	return executor, createErr
}

// Close 停止所有已创建或正在创建的会话容器。
func (p *DockerPool) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	entries := make([]*poolEntry, 0, len(p.entries))
	for _, entry := range p.entries {
		entries = append(entries, entry)
	}
	p.mu.Unlock()

	var closeErrors []error
	for _, entry := range entries {
		select {
		case <-entry.ready:
			if entry.executor != nil {
				if err := entry.executor.Close(ctx); err != nil {
					closeErrors = append(closeErrors, err)
				}
			}
		case <-ctx.Done():
			closeErrors = append(closeErrors, ctx.Err())
			return errors.Join(closeErrors...)
		}
	}
	return errors.Join(closeErrors...)
}

func mountDigest(mounts []SkillMount) string {
	values := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		values = append(values, mount.Name+"\x00"+mount.HostPath)
	}
	sort.Strings(values)
	return strings.Join(values, "\x01")
}

var _ ExecutorPool = (*DockerPool)(nil)
