// Package skills 负责发现并校验 Agent Skills 目录。
package skills

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const manifestName = "SKILL.md"

var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Skill 保存一个已经通过规范校验的 Skill 元数据。
type Skill struct {
	Name        string
	Description string
	Root        string
	Manifest    string
}

// Catalog 保存按名称索引的 Skill 集合。
type Catalog struct {
	byName map[string]Skill
}

type frontmatter struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license,omitempty"`
	Compatibility string            `yaml:"compatibility,omitempty"`
	Metadata      map[string]string `yaml:"metadata,omitempty"`
	AllowedTools  string            `yaml:"allowed-tools,omitempty"`
}

// Discover 按配置顺序扫描目录；重名或不符合规范的 Skill 会使启动失败。
func Discover(directories []string) (*Catalog, error) {
	catalog := &Catalog{byName: make(map[string]Skill)}
	for _, rawDirectory := range directories {
		directory := strings.TrimSpace(rawDirectory)
		if directory == "" {
			return nil, fmt.Errorf("skill directory cannot be empty")
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			return nil, fmt.Errorf("read skill directory %q: %w", directory, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			manifest := filepath.Join(directory, entry.Name(), manifestName)
			if _, err := os.Stat(manifest); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, fmt.Errorf("stat skill manifest %q: %w", manifest, err)
			}
			skill, err := parseSkill(entry.Name(), manifest)
			if err != nil {
				return nil, err
			}
			if existing, duplicate := catalog.byName[skill.Name]; duplicate {
				return nil, fmt.Errorf("skill %q is duplicated in %q and %q", skill.Name, existing.Root, skill.Root)
			}
			catalog.byName[skill.Name] = skill
		}
	}
	return catalog, nil
}

// Select 返回按名称排序的白名单 Skill；未知名称会直接报错。
func (c *Catalog) Select(names []string) ([]Skill, error) {
	if c == nil {
		if len(names) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("skill catalog is not configured")
	}
	selected := make([]Skill, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, rawName := range names {
		name := strings.TrimSpace(rawName)
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("skill %q is duplicated in agent allowlist", name)
		}
		skill, exists := c.byName[name]
		if !exists {
			return nil, fmt.Errorf("unknown skill %q", name)
		}
		seen[name] = struct{}{}
		selected = append(selected, skill)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Name < selected[j].Name })
	return selected, nil
}

// All 返回按名称排序的 Skill 元数据副本。
func (c *Catalog) All() []Skill {
	if c == nil {
		return nil
	}
	result := make([]Skill, 0, len(c.byName))
	for _, skill := range c.byName {
		result = append(result, skill)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func parseSkill(directoryName, manifest string) (Skill, error) {
	data, err := os.ReadFile(manifest)
	if err != nil {
		return Skill{}, fmt.Errorf("read skill manifest %q: %w", manifest, err)
	}
	header, err := splitFrontmatter(data)
	if err != nil {
		return Skill{}, fmt.Errorf("parse skill manifest %q: %w", manifest, err)
	}
	var metadata frontmatter
	decoder := yaml.NewDecoder(bytes.NewReader(header))
	decoder.KnownFields(true)
	if err := decoder.Decode(&metadata); err != nil {
		return Skill{}, fmt.Errorf("decode skill manifest %q: %w", manifest, err)
	}
	if err := validateFrontmatter(directoryName, metadata); err != nil {
		return Skill{}, fmt.Errorf("validate skill manifest %q: %w", manifest, err)
	}
	root, err := filepath.EvalSymlinks(filepath.Dir(manifest))
	if err != nil {
		return Skill{}, fmt.Errorf("resolve skill root %q: %w", filepath.Dir(manifest), err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return Skill{}, fmt.Errorf("resolve absolute skill root %q: %w", root, err)
	}
	return Skill{
		Name:        metadata.Name,
		Description: strings.TrimSpace(metadata.Description),
		Root:        root,
		Manifest:    filepath.Join(root, manifestName),
	}, nil
}

func splitFrontmatter(data []byte) ([]byte, error) {
	content := strings.ReplaceAll(strings.TrimPrefix(string(data), "\ufeff"), "\r\n", "\n")
	lines := strings.Split(content, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return nil, fmt.Errorf("YAML frontmatter is required")
	}
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == "---" {
			return []byte(strings.Join(lines[1:index], "\n")), nil
		}
	}
	return nil, fmt.Errorf("YAML frontmatter is not closed")
}

func validateFrontmatter(directoryName string, metadata frontmatter) error {
	if !skillNamePattern.MatchString(metadata.Name) || len(metadata.Name) > 64 {
		return fmt.Errorf("name must use 1-64 lowercase letters, numbers, or single hyphens")
	}
	if metadata.Name != directoryName {
		return fmt.Errorf("name %q must match parent directory %q", metadata.Name, directoryName)
	}
	description := strings.TrimSpace(metadata.Description)
	if description == "" || utf8.RuneCountInString(description) > 1024 {
		return fmt.Errorf("description must contain 1-1024 characters")
	}
	compatibility := strings.TrimSpace(metadata.Compatibility)
	if compatibility != "" && utf8.RuneCountInString(compatibility) > 500 {
		return fmt.Errorf("compatibility cannot exceed 500 characters")
	}
	return nil
}
