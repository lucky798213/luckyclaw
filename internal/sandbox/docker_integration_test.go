package sandbox

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDockerExecutorIntegration(t *testing.T) {
	if os.Getenv("LUCKYCLAW_DOCKER_INTEGRATION") != "1" {
		t.Skip("设置 LUCKYCLAW_DOCKER_INTEGRATION=1 后运行真实 Docker 集成测试")
	}
	image := os.Getenv("LUCKYCLAW_SANDBOX_TEST_IMAGE")
	if image == "" {
		image = "luckyclaw-sandbox:test"
	}
	pool, err := NewDockerPool(DockerPolicy{
		Image: image, CPUs: 1, MemoryMB: 512, PIDsLimit: 128, TmpfsMB: 64,
		MaxOutputBytes: 64, MaxFileBytes: 1024,
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close(context.Background())

	executor, err := pool.Get(context.Background(), "agent-a", "session-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.WriteFile(context.Background(), "src/main.txt", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	content, err := executor.ReadFile(context.Background(), "src/main.txt")
	if err != nil || string(content) != "hello" {
		t.Fatalf("ReadFile() = %q, %v", content, err)
	}
	entries, err := executor.ListDir(context.Background(), "src")
	if err != nil || len(entries) != 1 || entries[0].Name != "main.txt" {
		t.Fatalf("ListDir() = %+v, %v", entries, err)
	}
	result, err := executor.Exec(context.Background(), `printf 'from-exec' > generated.txt`, 2*time.Second)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("Exec() = %+v, %v", result, err)
	}
	generated, err := executor.ReadFile(context.Background(), "generated.txt")
	if err != nil || string(generated) != "from-exec" {
		t.Fatalf("generated = %q, %v", generated, err)
	}

	other, err := pool.Get(context.Background(), "agent-a", "session-b", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.ReadFile(context.Background(), "generated.txt"); err == nil {
		t.Fatal("different session unexpectedly read the first session workspace")
	}
	if _, err := executor.ReadFile(context.Background(), "../secret"); err == nil {
		t.Fatal("path traversal error = nil")
	}
	if _, err := executor.Exec(context.Background(), `ln -s /etc/passwd escaped`, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.ReadFile(context.Background(), "escaped"); err == nil || !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("symlink escape error = %v", err)
	}
	if _, err := executor.Exec(context.Background(), "sleep 5", 100*time.Millisecond); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout error = %v", err)
	}
	alive, err := executor.Exec(context.Background(), "printf alive", time.Second)
	if err != nil || alive.Output != "alive" {
		t.Fatalf("container after timeout = %+v, %v", alive, err)
	}
	large, err := executor.Exec(context.Background(), "head -c 200 /dev/zero | tr '\\0' x", time.Second)
	if err != nil || !large.Truncated || len(large.Output) != 64 {
		t.Fatalf("truncated output = %+v, %v", large, err)
	}
	network, err := executor.Exec(context.Background(), "ls /sys/class/net", time.Second)
	if err != nil || strings.Contains(network.Output, "eth0") {
		t.Fatalf("network interfaces = %+v, %v", network, err)
	}
}
