package skills

import (
	"strings"
	"testing"
)

func TestBuildSummaryContainsOnlyCatalogMetadata(t *testing.T) {
	summary := BuildSummary([]Skill{{Name: "code-runner", Description: "安全执行代码"}})
	if !strings.Contains(summary, "code-runner: 安全执行代码") || !strings.Contains(summary, "load_skill") {
		t.Fatalf("summary = %q", summary)
	}
	if strings.Contains(summary, "SKILL BODY") {
		t.Fatal("summary unexpectedly contains full skill body")
	}
}
