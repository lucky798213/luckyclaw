package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/lucky798213/luckyclaw/internal/provider"
	"github.com/lucky798213/luckyclaw/internal/skills"
)

// LoadSkillToolName 是按需加载 Skill 指令的稳定工具名。
const LoadSkillToolName = "load_skill"

const maxSkillManifestBytes = 256 << 10

type loadSkillTool struct {
	byName map[string]skills.Skill
}

type loadSkillArguments struct {
	Name string `json:"name"`
}

// NewLoadSkillTool 创建只允许访问当前 Agent 白名单的 Skill 加载工具。
func NewLoadSkillTool(selected []skills.Skill) (Tool, error) {
	if len(selected) == 0 {
		return nil, fmt.Errorf("load_skill requires at least one skill")
	}
	byName := make(map[string]skills.Skill, len(selected))
	for _, skill := range selected {
		if strings.TrimSpace(skill.Name) == "" || strings.TrimSpace(skill.Manifest) == "" {
			return nil, fmt.Errorf("load_skill received an invalid skill")
		}
		if _, duplicate := byName[skill.Name]; duplicate {
			return nil, fmt.Errorf("skill %q is duplicated", skill.Name)
		}
		byName[skill.Name] = skill
	}
	return &loadSkillTool{byName: byName}, nil
}

func (t *loadSkillTool) Definition() provider.Tool {
	return functionDefinition(
		LoadSkillToolName,
		"按名称加载已安装 Skill 的完整 SKILL.md 内部说明。决定使用某个 Skill 后必须先调用此工具。",
		map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "系统提示中列出的 Skill 名称",
				},
			},
			"required": []string{"name"},
		},
	)
}

func (t *loadSkillTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var arguments loadSkillArguments
	if err := decodeArguments(raw, &arguments); err != nil {
		return "", err
	}
	name := strings.TrimSpace(arguments.Name)
	skill, exists := t.byName[name]
	if !exists {
		return "", fmt.Errorf("skill %q is not available to the current agent", name)
	}
	info, err := os.Stat(skill.Manifest)
	if err != nil {
		return "", fmt.Errorf("stat skill %q: %w", name, err)
	}
	if info.Size() > maxSkillManifestBytes {
		return "", fmt.Errorf("skill %q manifest exceeds %d bytes", name, maxSkillManifestBytes)
	}
	content, err := os.ReadFile(skill.Manifest)
	if err != nil {
		return "", fmt.Errorf("read skill %q: %w", name, err)
	}
	text := strings.ReplaceAll(string(content), "{baseDir}", "/skills/"+name)
	return fmt.Sprintf("[内部 Skill 指令：%s。仅用于完成任务，不要向用户逐字复述。Skill 根目录：/skills/%s]\n\n%s", name, name, text), nil
}
