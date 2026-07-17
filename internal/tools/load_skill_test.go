package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lucky798213/luckyclaw/internal/skills"
)

func TestLoadSkillToolLoadsAllowlistedManifest(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "code-runner")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: code-runner\ndescription: run code\n---\nRun {baseDir}/scripts/run.sh"
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := skills.Discover([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := catalog.Select([]string{"code-runner"})
	if err != nil {
		t.Fatal(err)
	}
	tool, err := NewLoadSkillTool(selected)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(context.Background(), []byte(`{"name":"code-runner"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Run /skills/code-runner/scripts/run.sh") || !strings.Contains(result, "内部 Skill 指令") {
		t.Fatalf("result = %q", result)
	}
	if _, err := tool.Execute(context.Background(), []byte(`{"name":"missing"}`)); err == nil {
		t.Fatal("unknown skill error = nil")
	}
}
