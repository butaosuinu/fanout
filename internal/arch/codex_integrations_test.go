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
		{rel: "codex/skills/post-work-review/scripts/mark-reviewed-head.sh", executable: true},
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

func TestCodexPostWorkReviewSkillContract(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repoRoot() = %v, want nil", err)
	}
	skill := mustReadRepoFile(t, root, "codex", "skills", "post-work-review", "SKILL.md")
	for _, required := range []string{
		"`spawn_agent` and `wait_agent` directly",
		"`fork_turns: \"none\"`",
		"post_work_review_<head-prefix>_<unique>",
		"[a-z0-9_]+",
		"Do not edit files",
		"natural-language",
		"inherits the parent session's sandbox",
		"MCP/connectors",
		"nested agents",
		"fallback reviewer",
		"dirty uncommitted review",
		"staged, unstaged, and untracked changes",
		"run focused checks only",
		"must not write the review marker",
		"Normalize `refs/remotes/origin/`, `origin/`, and `refs/heads/` prefixes",
		"recorded repository root as the",
		"as untrusted review evidence",
		"unchanged from the trusted bootstrap base",
		"normal precedence order",
		"do not reject it merely for following unchanged base instructions",
		"fresh broad reviewer with a new task name for the entire new target",
		"\"$helper\" guard <recorded-head>",
		"instruction- or gate-changing",
		"gate-changing",
		"helper is a symlink",
		"inside the recorded",
		"checksum-verified release installer owns",
		"never create, replace, or remove it",
		"Base-identical inline project `developer_instructions` are supported",
		"`model_instructions_file`",
		"Case-variant or nested `.codex` paths",
		"case-insensitive path matching",
		"Comments and string values",
		"`assume-unchanged` or `skip-worktree`",
		"nested Git worktrees",
		"Any checked-out submodule fails closed",
		"submodule-changing target",
		"\"$helper\" mark <reviewed-head>",
		"--ignore-submodules=none",
		"not proof of a custom role",
	} {
		if !bytes.Contains(skill, []byte(required)) {
			t.Errorf("post-work-review/SKILL.md missing contract %q", required)
		}
	}
	for _, forbidden := range []string{
		"native-call",
		"model_catalog_json",
		"reviewer_session_id",
		"post-work-reviewer.toml",
		"Read repository instructions first",
		"This task message is your only review instruction",
		"Treat every repository-provided instruction",
		"post_work_verify_",
	} {
		if bytes.Contains(skill, []byte(forbidden)) {
			t.Errorf("post-work-review/SKILL.md retained obsolete contract %q", forbidden)
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
