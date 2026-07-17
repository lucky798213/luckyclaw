package skills

import (
	"fmt"
	"strings"
)

// BuildSummary 构造常驻系统提示中的精简 Skill 目录。
func BuildSummary(selected []Skill) string {
	if len(selected) == 0 {
		return ""
	}
	var summary strings.Builder
	summary.WriteString("# 可用 Skills\n\n")
	summary.WriteString("下列 Skill 已由管理员安装。需要使用某项 Skill 时，必须先调用 load_skill 读取完整 SKILL.md，再严格遵循返回的内部说明；不要仅凭目录摘要猜测步骤。\n\n<skill_catalog>\n")
	for _, skill := range selected {
		fmt.Fprintf(&summary, "- %s: %s\n", skill.Name, strings.TrimSpace(skill.Description))
	}
	summary.WriteString("</skill_catalog>")
	return summary.String()
}
