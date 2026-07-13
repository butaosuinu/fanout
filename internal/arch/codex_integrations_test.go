package arch

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestCodexSkillMetadata(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repoRoot() = %v, want nil", err)
	}
	skillsRoot := filepath.Join(root, "codex", "skills")
	entries, err := os.ReadDir(skillsRoot)
	if err != nil {
		t.Fatalf("ReadDir(codex/skills) = %v, want nil", err)
	}

	wantSkills := []string{"fanout", "fanout-issues", "fanout-plan", "post-work-review", "pr-watch"}
	var gotSkills []string
	for _, entry := range entries {
		if entry.IsDir() {
			gotSkills = append(gotSkills, entry.Name())
		}
	}
	slices.Sort(gotSkills)
	slices.Sort(wantSkills)
	if !slices.Equal(gotSkills, wantSkills) {
		t.Fatalf("codex/skills directories = %v, want %v", gotSkills, wantSkills)
	}

	for _, skill := range wantSkills {
		t.Run(skill, func(t *testing.T) {
			skillDir := filepath.Join(skillsRoot, skill)
			frontmatter := parseSkillFrontmatter(t, mustReadRepoFile(t, skillDir, "SKILL.md"))
			var gotKeys []string
			for key := range frontmatter {
				gotKeys = append(gotKeys, key)
			}
			slices.Sort(gotKeys)
			wantKeys := []string{"description", "name"}
			if !slices.Equal(gotKeys, wantKeys) {
				t.Errorf("SKILL.md frontmatter keys = %v, want %v", gotKeys, wantKeys)
			}
			if got := frontmatter["name"]; got != skill {
				t.Errorf("SKILL.md name = %q, want directory name %q", got, skill)
			}
			if strings.TrimSpace(frontmatter["description"]) == "" {
				t.Error("SKILL.md description is empty")
			}

			metadata := mustReadRepoFile(t, skillDir, "agents", "openai.yaml")
			if token := "$" + skill; !bytes.Contains(metadata, []byte(token)) {
				t.Errorf("agents/openai.yaml does not contain %q", token)
			}
		})
	}
}

func TestCodexSkillResources(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repoRoot() = %v, want nil", err)
	}
	resources := []struct {
		rel        string
		executable bool
	}{
		{rel: "codex/skills/fanout/references/batch-workflow.md"},
		{rel: "codex/skills/fanout/references/cli-modes.md"},
		{rel: "codex/skills/pr-watch/references/repair-playbook.md"},
		{rel: "codex/skills/pr-watch/scripts/watch-pr.sh", executable: true},
	}
	for _, resource := range resources {
		t.Run(filepath.ToSlash(resource.rel), func(t *testing.T) {
			info, err := os.Stat(filepath.Join(root, filepath.FromSlash(resource.rel)))
			if err != nil {
				t.Fatalf("Stat(%s) = %v, want nil", resource.rel, err)
			}
			if !info.Mode().IsRegular() {
				t.Errorf("%s mode = %v, want regular file", resource.rel, info.Mode())
			}
			if resource.executable && info.Mode().Perm()&0o111 == 0 {
				t.Errorf("%s mode = %v, want at least one executable bit", resource.rel, info.Mode())
			}
		})
	}
}

func TestCodexReviewAgentConfigs(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repoRoot() = %v, want nil", err)
	}
	wants := []struct {
		name   string
		model  string
		effort string
	}{
		{name: "post-work-reviewer", model: "gpt-5.6-sol", effort: "xhigh"},
		{name: "post-work-verifier", model: "gpt-5.6-terra", effort: "high"},
	}
	for _, want := range wants {
		t.Run(want.name, func(t *testing.T) {
			agentsDir := filepath.Join(root, "codex", "agents")
			tomlData := mustReadRepoFile(t, agentsDir, want.name+".toml")
			for key, expected := range map[string]string{
				"name":                   want.name,
				"model":                  want.model,
				"model_reasoning_effort": want.effort,
				"sandbox_mode":           "read-only",
			} {
				if got := tomlStringValue(t, tomlData, key); got != expected {
					t.Errorf("%s = %q, want %q", key, got, expected)
				}
			}

			instructions := tomlDeveloperInstructions(t, tomlData)
			mirror := mustReadRepoFile(t, agentsDir, want.name+".md")
			if !bytes.Equal(instructions, mirror) {
				t.Errorf("developer_instructions does not byte-match codex/agents/%s.md", want.name)
			}
			for _, required := range []string{
				"`reviewer_session_id`",
				"`CODEX_THREAD_ID`",
				"Do not substitute a task name",
				"Return JSON only",
			} {
				if !bytes.Contains(instructions, []byte(required)) {
					t.Errorf("developer_instructions missing session contract %q", required)
				}
			}
		})
	}

	skill := mustReadRepoFile(t, root, "codex", "skills", "post-work-review", "SKILL.md")
	for _, required := range []string{
		"If either is unavailable",
		"never substitute another role or model",
		"custom_agent_selection_unavailable",
		"custom_role_selector=false",
		"agent_type: \"post-work-reviewer\"",
		"agent_type: \"post-work-verifier\"",
		"fork_turns: \"none\"",
		"task_name",
		"every stored result has passed the driver's",
	} {
		if !bytes.Contains(skill, []byte(required)) {
			t.Errorf("post-work-review/SKILL.md missing fail-closed contract %q", required)
		}
	}
}

func mustReadRepoFile(t *testing.T, base string, elem ...string) []byte {
	t.Helper()
	file := filepath.Join(append([]string{base}, elem...)...)
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("ReadFile(%s) = %v, want nil", file, err)
	}
	return data
}

func parseSkillFrontmatter(t *testing.T, data []byte) map[string]string {
	t.Helper()
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		t.Fatal("SKILL.md does not start with YAML frontmatter")
	}
	frontmatter := make(map[string]string)
	closed := false
	for _, line := range lines[1:] {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "---" {
			closed = true
			break
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			t.Fatalf("invalid frontmatter line %q", line)
		}
		key = strings.TrimSpace(key)
		if _, exists := frontmatter[key]; exists {
			t.Fatalf("duplicate frontmatter key %q", key)
		}
		frontmatter[key] = unquoteStaticString(t, strings.TrimSpace(value))
	}
	if !closed {
		t.Fatal("SKILL.md frontmatter is not closed")
	}
	return frontmatter
}

func tomlStringValue(t *testing.T, data []byte, key string) string {
	t.Helper()
	for line := range strings.Lines(string(data)) {
		name, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.TrimSpace(name) != key {
			continue
		}
		return unquoteStaticString(t, strings.TrimSpace(value))
	}
	t.Fatalf("TOML key %q not found", key)
	return ""
}

func tomlDeveloperInstructions(t *testing.T, data []byte) []byte {
	t.Helper()
	const opening = "developer_instructions = \"\"\"\n"
	_, rest, ok := bytes.Cut(data, []byte(opening))
	if !ok {
		t.Fatal("developer_instructions triple-quoted string not found")
	}
	end := bytes.LastIndex(rest, []byte("\n\"\"\""))
	if end < 0 {
		t.Fatal("developer_instructions triple-quoted string is not closed")
	}
	return rest[:end+1]
}

func unquoteStaticString(t *testing.T, value string) string {
	t.Helper()
	if value == "" || (value[0] != '"' && value[0] != '\'') {
		return value
	}
	unquoted, err := strconv.Unquote(value)
	if err != nil {
		t.Fatalf("unquote %q = %v", value, err)
	}
	return unquoted
}
