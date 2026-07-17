package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const dockerCloseTimeout = 2 * time.Second

// DockerPolicy 保存 Docker 容器的安全与资源边界。
type DockerPolicy struct {
	Image          string
	NetworkEnabled bool
	CPUs           float64
	MemoryMB       int
	PIDsLimit      int
	TmpfsMB        int
	MaxOutputBytes int
	MaxFileBytes   int
}

// DockerExecutor 在一个长生命周期容器内实现 SandboxExecutor。
type DockerExecutor struct {
	containerID string
	policy      DockerPolicy
	keeperInput io.WriteCloser
	keeperDone  chan error
	closeOnce   sync.Once
	closeErr    error
}

// NewDockerExecutor 创建并启动会话专属容器。
func NewDockerExecutor(ctx context.Context, policy DockerPolicy, workspace string, skills []SkillMount) (*DockerExecutor, error) {
	if err := validateDockerPolicy(policy); err != nil {
		return nil, err
	}
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve Docker workspace: %w", err)
	}
	if strings.Contains(workspace, ",") {
		return nil, fmt.Errorf("Docker workspace path cannot contain a comma")
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return nil, fmt.Errorf("create Docker workspace %q: %w", workspace, err)
	}
	for _, mount := range skills {
		if err := validateSkillMount(mount); err != nil {
			return nil, err
		}
	}
	arguments := buildContainerCreateArgs(policy, workspace, skills, os.Getuid(), os.Getgid())
	output, _, err := runDocker(ctx, nil, 64<<10, arguments...)
	if err != nil {
		return nil, fmt.Errorf("create Docker sandbox: %w: %s", err, strings.TrimSpace(output))
	}
	containerID := strings.TrimSpace(output)
	if containerID == "" || strings.ContainsAny(containerID, "\r\n\t ") {
		return nil, fmt.Errorf("Docker create returned invalid container id %q", containerID)
	}

	keeper := exec.Command("docker", "start", "--attach", "--interactive", containerID)
	keeperInput, err := keeper.StdinPipe()
	if err != nil {
		_, _, _ = runDocker(context.Background(), nil, 64<<10, "rm", "--force", containerID)
		return nil, fmt.Errorf("create Docker keeper stdin: %w", err)
	}
	keeper.Stdout = io.Discard
	keeper.Stderr = io.Discard
	if err := keeper.Start(); err != nil {
		_ = keeperInput.Close()
		_, _, _ = runDocker(context.Background(), nil, 64<<10, "rm", "--force", containerID)
		return nil, fmt.Errorf("start Docker sandbox: %w", err)
	}
	executor := &DockerExecutor{
		containerID: containerID,
		policy:      policy,
		keeperInput: keeperInput,
		keeperDone:  make(chan error, 1),
	}
	go func() { executor.keeperDone <- keeper.Wait() }()
	if err := executor.waitUntilRunning(ctx); err != nil {
		_ = executor.Close(context.Background())
		return nil, err
	}
	return executor, nil
}

func validateDockerPolicy(policy DockerPolicy) error {
	if strings.TrimSpace(policy.Image) == "" {
		return fmt.Errorf("Docker sandbox image cannot be empty")
	}
	if policy.CPUs <= 0 {
		return fmt.Errorf("Docker sandbox CPUs must be greater than zero")
	}
	if policy.MemoryMB < 6 {
		return fmt.Errorf("Docker sandbox memory must be at least 6 MB")
	}
	if policy.PIDsLimit < 1 || policy.TmpfsMB < 1 {
		return fmt.Errorf("Docker sandbox PID and tmpfs limits must be greater than zero")
	}
	if policy.MaxOutputBytes < 1 || policy.MaxFileBytes < 1 {
		return fmt.Errorf("Docker sandbox output and file limits must be greater than zero")
	}
	return nil
}

func validateSkillMount(mount SkillMount) error {
	if mount.Name == "" || strings.ContainsAny(mount.Name, "/\\,") || mount.Name == "." || mount.Name == ".." {
		return fmt.Errorf("invalid Docker skill mount name %q", mount.Name)
	}
	absolute, err := filepath.Abs(mount.HostPath)
	if err != nil {
		return fmt.Errorf("resolve skill %q mount: %w", mount.Name, err)
	}
	if strings.Contains(absolute, ",") {
		return fmt.Errorf("Docker skill mount path cannot contain a comma")
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return fmt.Errorf("stat skill %q mount: %w", mount.Name, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("skill %q mount must be a directory", mount.Name)
	}
	return nil
}

func buildContainerCreateArgs(policy DockerPolicy, workspace string, skills []SkillMount, uid, gid int) []string {
	network := "none"
	if policy.NetworkEnabled {
		network = "bridge"
	}
	memory := strconv.Itoa(policy.MemoryMB) + "m"
	arguments := []string{
		"create", "--interactive", "--rm", "--init",
		"--label", "luckyclaw.sandbox=true",
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--network", network,
		"--cpus", strconv.FormatFloat(policy.CPUs, 'f', -1, 64),
		"--memory", memory,
		"--memory-swap", memory,
		"--pids-limit", strconv.Itoa(policy.PIDsLimit),
		"--ulimit", "nofile=1024:1024",
		"--tmpfs", fmt.Sprintf("/tmp:rw,nosuid,nodev,size=%dm", policy.TmpfsMB),
		"--user", fmt.Sprintf("%d:%d", uid, gid),
		"--workdir", "/workspace",
		"--env", "HOME=/workspace",
		"--env", "GOCACHE=/workspace/.cache/go-build",
		"--mount", "type=bind,source=" + workspace + ",target=/workspace",
	}
	for _, mount := range skills {
		absolute, _ := filepath.Abs(mount.HostPath)
		arguments = append(arguments, "--mount", "type=bind,source="+absolute+",target=/skills/"+mount.Name+",readonly")
	}
	arguments = append(arguments, policy.Image, "sh", "-c", "cat >/dev/null")
	return arguments
}

func (d *DockerExecutor) waitUntilRunning(ctx context.Context) error {
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		output, _, err := runDocker(ctx, nil, 64<<10, "inspect", "--format", "{{.State.Running}}", d.containerID)
		if err == nil && strings.TrimSpace(output) == "true" {
			return nil
		}
		select {
		case keeperErr := <-d.keeperDone:
			return fmt.Errorf("Docker sandbox stopped during startup: %v", keeperErr)
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("Docker sandbox did not become ready")
		case <-ticker.C:
		}
	}
}

func (d *DockerExecutor) Exec(context.Context, string, time.Duration) (ExecResult, error) {
	return ExecResult{}, fmt.Errorf("Docker exec is not implemented")
}

func (d *DockerExecutor) ReadFile(context.Context, string) ([]byte, error) {
	return nil, fmt.Errorf("Docker read_file is not implemented")
}

func (d *DockerExecutor) WriteFile(context.Context, string, []byte) error {
	return fmt.Errorf("Docker write_file is not implemented")
}

func (d *DockerExecutor) ListDir(context.Context, string) ([]FileEntry, error) {
	return nil, fmt.Errorf("Docker list_dir is not implemented")
}

// Close 先关闭 keeper stdin，再定向强制删除未退出的容器。
func (d *DockerExecutor) Close(ctx context.Context) error {
	if d == nil {
		return nil
	}
	d.closeOnce.Do(func() {
		if d.keeperInput != nil {
			_ = d.keeperInput.Close()
		}
		timer := time.NewTimer(dockerCloseTimeout)
		defer timer.Stop()
		select {
		case <-d.keeperDone:
			return
		case <-ctx.Done():
			d.closeErr = ctx.Err()
		case <-timer.C:
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), dockerCloseTimeout)
		defer cancel()
		output, _, err := runDocker(cleanupCtx, nil, 64<<10, "rm", "--force", d.containerID)
		if err != nil && !strings.Contains(output, "No such container") {
			d.closeErr = fmt.Errorf("remove Docker sandbox: %w: %s", err, strings.TrimSpace(output))
		}
	})
	return d.closeErr
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(input []byte) (int, error) {
	original := len(input)
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		if len(input) > remaining {
			input = input[:remaining]
			b.truncated = true
		}
		_, _ = b.buffer.Write(input)
	} else if len(input) > 0 {
		b.truncated = true
	}
	return original, nil
}

func runDocker(ctx context.Context, stdin io.Reader, limit int, arguments ...string) (string, bool, error) {
	command := exec.CommandContext(ctx, "docker", arguments...)
	command.Stdin = stdin
	output := &limitedBuffer{limit: limit}
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	return output.buffer.String(), output.truncated, err
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	return -1
}

var _ SandboxExecutor = (*DockerExecutor)(nil)
