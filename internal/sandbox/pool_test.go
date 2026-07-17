package sandbox

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestDockerPoolReusesOnlySameAgentAndSession(t *testing.T) {
	pool := newFakeDockerPool(t)
	first, err := pool.Get(context.Background(), "agent-a", "session-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	again, err := pool.Get(context.Background(), "agent-a", "session-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	otherSession, err := pool.Get(context.Background(), "agent-a", "session-b", nil)
	if err != nil {
		t.Fatal(err)
	}
	otherAgent, err := pool.Get(context.Background(), "agent-b", "session-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	if first != again || first == otherSession || first == otherAgent || otherSession == otherAgent {
		t.Fatal("pool did not isolate agent/session executors")
	}
	if err := pool.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDockerPoolMergesConcurrentCreation(t *testing.T) {
	pool := newFakeDockerPool(t)
	var wait sync.WaitGroup
	results := make([]SandboxExecutor, 8)
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index], _ = pool.Get(context.Background(), "agent", "session", nil)
		}(index)
	}
	wait.Wait()
	for _, result := range results[1:] {
		if result != results[0] {
			t.Fatal("concurrent Get created more than one executor")
		}
	}
}

func TestDockerPoolRejectsMountChanges(t *testing.T) {
	pool := newFakeDockerPool(t)
	mounts := []SkillMount{{Name: "one", HostPath: t.TempDir()}}
	if _, err := pool.Get(context.Background(), "agent", "session", mounts); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Get(context.Background(), "agent", "session", nil); err == nil {
		t.Fatal("mount change error = nil")
	}
}

func newFakeDockerPool(t *testing.T) *DockerPool {
	t.Helper()
	pool, err := NewDockerPool(DockerPolicy{
		Image: "test", CPUs: 1, MemoryMB: 64, PIDsLimit: 8, TmpfsMB: 8,
		MaxOutputBytes: 1024, MaxFileBytes: 1024,
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pool.factory = func(_ context.Context, _ DockerPolicy, workspace string, _ []SkillMount) (SandboxExecutor, error) {
		return &fakeSandboxExecutor{workspace: workspace}, nil
	}
	return pool
}

type fakeSandboxExecutor struct {
	workspace string
	closed    bool
}

func (e *fakeSandboxExecutor) Exec(context.Context, string, time.Duration) (ExecResult, error) {
	return ExecResult{}, nil
}

func (e *fakeSandboxExecutor) ReadFile(context.Context, string) ([]byte, error) { return nil, nil }
func (e *fakeSandboxExecutor) WriteFile(context.Context, string, []byte) error  { return nil }
func (e *fakeSandboxExecutor) ListDir(context.Context, string) ([]FileEntry, error) {
	return nil, nil
}
func (e *fakeSandboxExecutor) Close(context.Context) error { e.closed = true; return nil }
