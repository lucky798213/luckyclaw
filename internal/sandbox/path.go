package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

const maxWorkspacePathBytes = 4096

// NormalizeWorkspacePath 校验模型提供的路径并返回 Linux 容器相对路径。
func NormalizeWorkspacePath(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("workspace path cannot be empty")
	}
	if len(raw) > maxWorkspacePathBytes {
		return "", fmt.Errorf("workspace path exceeds %d bytes", maxWorkspacePathBytes)
	}
	if strings.ContainsRune(raw, '\x00') {
		return "", fmt.Errorf("workspace path cannot contain NUL")
	}
	if strings.Contains(raw, "\\") {
		return "", fmt.Errorf("workspace path must use forward slashes")
	}
	if path.IsAbs(raw) {
		return "", fmt.Errorf("workspace path must be relative")
	}
	for _, component := range strings.Split(raw, "/") {
		if component == ".." {
			return "", fmt.Errorf("workspace path cannot contain parent traversal")
		}
	}
	cleaned := path.Clean(raw)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("workspace path escapes the workspace")
	}
	return cleaned, nil
}

// WorkspaceDirectory 使用不可逆目录段隔离不受信任的 Agent/session 标识。
func WorkspaceDirectory(root, agentID, sessionKey string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("workspace root cannot be empty")
	}
	if strings.TrimSpace(agentID) == "" {
		return "", fmt.Errorf("workspace agent id cannot be empty")
	}
	if strings.TrimSpace(sessionKey) == "" {
		return "", fmt.Errorf("workspace session key cannot be empty")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	return filepath.Join(absoluteRoot, hashSegment(agentID), hashSegment(sessionKey)), nil
}

func hashSegment(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
