package sandbox

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeWorkspacePath(t *testing.T) {
	valid := map[string]string{
		".":              ".",
		"src/main.go":    "src/main.go",
		"src//nested.go": "src/nested.go",
	}
	for input, want := range valid {
		got, err := NormalizeWorkspacePath(input)
		if err != nil || got != want {
			t.Fatalf("NormalizeWorkspacePath(%q) = %q, %v", input, got, err)
		}
	}
	for _, input := range []string{"", "/etc/passwd", "../secret", "a/../secret", "a\\..\\secret", "bad\x00name"} {
		if _, err := NormalizeWorkspacePath(input); err == nil {
			t.Fatalf("NormalizeWorkspacePath(%q) error = nil", input)
		}
	}
}

func TestWorkspaceDirectoryHashesUntrustedIdentifiers(t *testing.T) {
	root := t.TempDir()
	first, err := WorkspaceDirectory(root, "../agent", "../../session")
	if err != nil {
		t.Fatal(err)
	}
	second, err := WorkspaceDirectory(root, "agent", "session")
	if err != nil {
		t.Fatal(err)
	}
	absoluteRoot, _ := filepath.Abs(root)
	if !strings.HasPrefix(first, absoluteRoot+string(filepath.Separator)) || first == second {
		t.Fatalf("workspace paths = %q, %q", first, second)
	}
	relative, err := filepath.Rel(absoluteRoot, first)
	if err != nil || strings.Contains(relative, "..") {
		t.Fatalf("relative workspace = %q, %v", relative, err)
	}
}
