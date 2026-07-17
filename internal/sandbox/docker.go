package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
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

func (d *DockerExecutor) Exec(ctx context.Context, command string, timeout time.Duration) (ExecResult, error) {
	if strings.TrimSpace(command) == "" {
		return ExecResult{}, fmt.Errorf("sandbox command cannot be empty")
	}
	if timeout <= 0 {
		return ExecResult{}, fmt.Errorf("sandbox command timeout must be greater than zero")
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	marker := "/tmp/luckyclaw-pgid-" + randomMarker()
	done := make(chan struct{})
	go func() {
		select {
		case <-execCtx.Done():
			d.killExecProcess(marker)
		case <-done:
		}
	}()
	output, truncated, err := runDocker(execCtx, nil, d.policy.MaxOutputBytes, buildDockerExecArgs(d.containerID, marker, command)...)
	close(done)
	result := ExecResult{Output: output, ExitCode: exitCode(err), Truncated: truncated}
	if execCtx.Err() != nil {
		d.killExecProcess(marker)
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, fmt.Errorf("sandbox command timed out after %s", timeout)
	}
	d.cleanupExecMarker(marker)
	if err != nil {
		return result, fmt.Errorf("sandbox command exited with code %d: %s", result.ExitCode, strings.TrimSpace(result.Output))
	}
	return result, nil
}

func buildDockerExecArgs(containerID, marker, command string) []string {
	const script = `echo $$ > "$1"; exec sh -lc "$2"`
	return []string{"exec", containerID, "setsid", "sh", "-c", script, "sh", marker, command}
}

func (d *DockerExecutor) killExecProcess(marker string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	const script = `pgid=$(cat "$1" 2>/dev/null); if [ -n "$pgid" ]; then kill -KILL -- "-$pgid" 2>/dev/null || true; fi; rm -f -- "$1"`
	_, _, _ = runDocker(ctx, nil, 64<<10, "exec", d.containerID, "sh", "-c", script, "sh", marker)
}

func (d *DockerExecutor) cleanupExecMarker(marker string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, _ = runDocker(ctx, nil, 64<<10, "exec", d.containerID, "rm", "-f", "--", marker)
}

func randomMarker() string {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(value[:])
}

func (d *DockerExecutor) ReadFile(ctx context.Context, rawPath string) ([]byte, error) {
	target, err := containerWorkspacePath(rawPath)
	if err != nil {
		return nil, err
	}
	const script = `target=$(realpath -e -- "$1") || { echo "path does not exist" >&2; exit 2; }; case "$target" in /workspace/*) ;; *) echo "path escapes workspace" >&2; exit 2;; esac; [ -f "$target" ] || { echo "path is not a regular file" >&2; exit 2; }; size=$(stat -c %s -- "$target") || exit 2; [ "$size" -le "$2" ] || { echo "file exceeds size limit" >&2; exit 2; }; cat -- "$target"`
	output, truncated, runErr := runDocker(ctx, nil, d.policy.MaxFileBytes, "exec", d.containerID, "sh", "-c", script, "sh", target, strconv.Itoa(d.policy.MaxFileBytes))
	if runErr != nil {
		return nil, fmt.Errorf("read sandbox file %q: %s", rawPath, strings.TrimSpace(output))
	}
	if truncated {
		return nil, fmt.Errorf("read sandbox file %q exceeded %d bytes", rawPath, d.policy.MaxFileBytes)
	}
	return []byte(output), nil
}

func (d *DockerExecutor) WriteFile(ctx context.Context, rawPath string, content []byte) error {
	if len(content) > d.policy.MaxFileBytes {
		return fmt.Errorf("sandbox file exceeds %d bytes", d.policy.MaxFileBytes)
	}
	target, err := containerWorkspacePath(rawPath)
	if err != nil {
		return err
	}
	if target == "/workspace" {
		return fmt.Errorf("cannot write the workspace directory as a file")
	}
	const script = `parent=$(dirname -- "$1"); ancestor="$parent"; while [ ! -e "$ancestor" ]; do next=$(dirname -- "$ancestor"); [ "$next" != "$ancestor" ] || exit 2; ancestor="$next"; done; resolved=$(realpath -e -- "$ancestor") || exit 2; case "$resolved" in /workspace|/workspace/*) ;; *) echo "path escapes workspace" >&2; exit 2;; esac; mkdir -p -- "$parent" || exit 2; resolved_parent=$(realpath -e -- "$parent") || exit 2; case "$resolved_parent" in /workspace|/workspace/*) ;; *) echo "path escapes workspace" >&2; exit 2;; esac; if [ -L "$1" ]; then resolved_target=$(realpath -e -- "$1") || exit 2; case "$resolved_target" in /workspace/*) ;; *) echo "symlink escapes workspace" >&2; exit 2;; esac; fi; tmp="$resolved_parent/.luckyclaw-write-$$"; umask 077; cat > "$tmp" || { rm -f -- "$tmp"; exit 2; }; size=$(stat -c %s -- "$tmp") || { rm -f -- "$tmp"; exit 2; }; [ "$size" -le "$2" ] || { rm -f -- "$tmp"; echo "file exceeds size limit" >&2; exit 2; }; mv -f -- "$tmp" "$1"`
	output, _, runErr := runDocker(ctx, bytes.NewReader(content), 64<<10, "exec", "--interactive", d.containerID, "sh", "-c", script, "sh", target, strconv.Itoa(d.policy.MaxFileBytes))
	if runErr != nil {
		return fmt.Errorf("write sandbox file %q: %s", rawPath, strings.TrimSpace(output))
	}
	return nil
}

func (d *DockerExecutor) ListDir(ctx context.Context, rawPath string) ([]FileEntry, error) {
	target, err := containerWorkspacePath(rawPath)
	if err != nil {
		return nil, err
	}
	const script = `target=$(realpath -e -- "$1") || { echo "path does not exist" >&2; exit 2; }; case "$target" in /workspace|/workspace/*) ;; *) echo "path escapes workspace" >&2; exit 2;; esac; [ -d "$target" ] || { echo "path is not a directory" >&2; exit 2; }; find "$target" -mindepth 1 -maxdepth 1 -printf '%f\0%y\0%s\0%m\0'`
	output, truncated, runErr := runDocker(ctx, nil, d.policy.MaxOutputBytes, "exec", d.containerID, "sh", "-c", script, "sh", target)
	if runErr != nil {
		return nil, fmt.Errorf("list sandbox directory %q: %s", rawPath, strings.TrimSpace(output))
	}
	if truncated {
		return nil, fmt.Errorf("sandbox directory listing exceeds %d bytes", d.policy.MaxOutputBytes)
	}
	return parseDirectoryEntries([]byte(output))
}

func containerWorkspacePath(rawPath string) (string, error) {
	cleaned, err := NormalizeWorkspacePath(rawPath)
	if err != nil {
		return "", err
	}
	if cleaned == "." {
		return "/workspace", nil
	}
	return path.Join("/workspace", cleaned), nil
}

func parseDirectoryEntries(data []byte) ([]FileEntry, error) {
	if len(data) == 0 {
		return []FileEntry{}, nil
	}
	fields := bytes.Split(data, []byte{0})
	if len(fields) > 0 && len(fields[len(fields)-1]) == 0 {
		fields = fields[:len(fields)-1]
	}
	if len(fields)%4 != 0 {
		return nil, fmt.Errorf("decode sandbox directory listing: malformed output")
	}
	entries := make([]FileEntry, 0, len(fields)/4)
	for index := 0; index < len(fields); index += 4 {
		size, err := strconv.ParseInt(string(fields[index+2]), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("decode sandbox directory entry size: %w", err)
		}
		entryType := "other"
		switch string(fields[index+1]) {
		case "f":
			entryType = "file"
		case "d":
			entryType = "directory"
		case "l":
			entryType = "symlink"
		}
		entries = append(entries, FileEntry{
			Name: string(fields[index]), Type: entryType, Size: size, Mode: string(fields[index+3]),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
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
