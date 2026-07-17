package skills

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDiscoverAndSelectSkills(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "zeta", "Zeta skill")
	writeSkill(t, root, "alpha", "Alpha skill")
	catalog, err := Discover([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	all := catalog.All()
	if got := []string{all[0].Name, all[1].Name}; !reflect.DeepEqual(got, []string{"alpha", "zeta"}) {
		t.Fatalf("skill names = %v", got)
	}
	selected, err := catalog.Select([]string{"zeta"})
	if err != nil || len(selected) != 1 || selected[0].Description != "Zeta skill" {
		t.Fatalf("Select() = %+v, %v", selected, err)
	}
}

func TestDiscoverRejectsInvalidAndDuplicateSkills(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "缺少 frontmatter", body: "# test", want: "frontmatter is required"},
		{name: "目录名不匹配", body: "---\nname: other\ndescription: test\n---\n", want: "must match parent directory"},
		{name: "非法名称", body: "---\nname: Bad_Name\ndescription: test\n---\n", want: "name must use"},
		{name: "缺少描述", body: "---\nname: test-skill\ndescription: ''\n---\n", want: "description must contain"},
		{name: "未知字段", body: "---\nname: test-skill\ndescription: test\nunknown: value\n---\n", want: "field unknown not found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			directory := filepath.Join(root, "test-skill")
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, manifestName), []byte(test.body), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Discover([]string{root})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}

	first := t.TempDir()
	second := t.TempDir()
	writeSkill(t, first, "same", "first")
	writeSkill(t, second, "same", "second")
	if _, err := Discover([]string{first, second}); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestSelectRejectsUnknownAndDuplicateNames(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "known", "known")
	catalog, err := Discover([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Select([]string{"missing"}); err == nil || !strings.Contains(err.Error(), "unknown skill") {
		t.Fatalf("unknown error = %v", err)
	}
	if _, err := catalog.Select([]string{"known", "known"}); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate error = %v", err)
	}
}

func writeSkill(t *testing.T, root, name, description string) {
	t.Helper()
	directory := filepath.Join(root, name)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n# Instructions\n"
	if err := os.WriteFile(filepath.Join(directory, manifestName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
