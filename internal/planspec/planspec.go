// Package planspec loads and validates issue-less fanout plan specifications.
package planspec

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/butaosuinu/fanout/internal/naming"
)

const Version = 1

// Spec is the top-level JSON document consumed by fanout plan.
type Spec struct {
	Version int    `json:"version"`
	Plan    Plan   `json:"plan"`
	Tasks   []Task `json:"tasks"`
}

// Plan describes the parent plan that owns all tasks in the spec.
type Plan struct {
	Slug       string `json:"slug"`
	Title      string `json:"title"`
	Source     string `json:"source,omitempty"`
	BaseBranch string `json:"base_branch,omitempty"`
}

// Task describes one issue-less fanout task.
type Task struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Briefing    string   `json:"briefing"`
	Slug        string   `json:"slug,omitempty"`
	DisplayName string   `json:"display_name,omitempty"`
	Branch      string   `json:"branch,omitempty"`
	BlockedBy   []string `json:"blocked_by,omitempty"`
	Wave        string   `json:"wave,omitempty"`
}

// Load reads, parses, and validates a plan spec JSON file.
func Load(path string) (Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, fmt.Errorf("read plan spec %s: %w", path, err)
	}
	var spec Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return Spec{}, fmt.Errorf("parse plan spec %s: %w", path, err)
	}
	if err := Validate(spec); err != nil {
		return Spec{}, fmt.Errorf("validate plan spec %s: %w", path, err)
	}
	return spec, nil
}

// Validate verifies that a plan spec is internally consistent.
func Validate(spec Spec) error {
	return spec.Validate()
}

// Validate verifies that a plan spec is internally consistent.
func (s Spec) Validate() error {
	var errs []error
	if s.Version != Version {
		errs = append(errs, fmt.Errorf("version must be %d, got %d", Version, s.Version))
	}
	errs = append(errs, validateRequiredKebab("plan.slug", s.Plan.Slug)...)
	if strings.TrimSpace(s.Plan.Title) == "" {
		errs = append(errs, fmt.Errorf("plan.title is required"))
	}
	if len(s.Tasks) == 0 {
		errs = append(errs, fmt.Errorf("tasks must contain at least one task"))
	}

	seenIDs := map[string]int{}
	seenSlugs := map[string]int{}
	seenBranches := map[string]int{}
	taskIDs := map[string]bool{}
	for i, task := range s.Tasks {
		if task.ID != "" {
			taskIDs[task.ID] = true
		}
		errs = append(errs, validateTask(i, task)...)

		if task.ID != "" {
			if prev, ok := seenIDs[task.ID]; ok {
				errs = append(errs, fmt.Errorf("tasks[%d].id %q duplicates tasks[%d].id", i, task.ID, prev))
			} else {
				seenIDs[task.ID] = i
			}
		}

		if canResolveSlug(task) {
			slug := task.ResolvedSlug()
			if prev, ok := seenSlugs[slug]; ok {
				errs = append(errs, fmt.Errorf("tasks[%d].slug %q duplicates tasks[%d].slug", i, slug, prev))
			} else {
				seenSlugs[slug] = i
			}
			branch := task.ResolvedBranch()
			if prev, ok := seenBranches[branch]; ok {
				errs = append(errs, fmt.Errorf("tasks[%d].branch %q duplicates tasks[%d].branch", i, branch, prev))
			} else {
				seenBranches[branch] = i
			}
		}
	}

	for i, task := range s.Tasks {
		for j, dep := range task.BlockedBy {
			if !taskIDs[dep] {
				errs = append(errs, fmt.Errorf("tasks[%d].blocked_by[%d] references unknown task %q", i, j, dep))
			}
		}
	}
	if cycle := blockedByCycle(s.Tasks, taskIDs); len(cycle) > 0 {
		errs = append(errs, fmt.Errorf("blocked_by cycle detected: %s", strings.Join(cycle, " -> ")))
	}

	return errors.Join(errs...)
}

func validateTask(i int, task Task) []error {
	var errs []error
	errs = append(errs, validateRequiredKebab(fmt.Sprintf("tasks[%d].id", i), task.ID)...)
	if strings.TrimSpace(task.Title) == "" {
		errs = append(errs, fmt.Errorf("tasks[%d].title is required", i))
	}
	if strings.TrimSpace(task.Briefing) == "" {
		errs = append(errs, fmt.Errorf("tasks[%d].briefing is required", i))
	}
	if canResolveSlug(task) {
		errs = append(errs, validateRequiredKebab(fmt.Sprintf("tasks[%d].slug", i), task.ResolvedSlug())...)
	}
	if task.Branch != "" && strings.ContainsAny(task.Branch, " \t\r\n") {
		errs = append(errs, fmt.Errorf("tasks[%d].branch must not contain whitespace, got %q", i, task.Branch))
	}
	return errs
}

func validateRequiredKebab(field, value string) []error {
	if value == "" {
		return []error{fmt.Errorf("%s is required", field)}
	}
	var errs []error
	if len(value) > naming.MaxSlugLength {
		errs = append(errs, fmt.Errorf("%s length must be <= %d, got %d", field, naming.MaxSlugLength, len(value)))
	}
	if !isKebab(value) {
		errs = append(errs, fmt.Errorf("%s must be lowercase kebab-case (alnum+hyphens, starting with alnum), got %q", field, value))
	}
	return errs
}

func isKebab(value string) bool {
	for i, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0:
		default:
			return false
		}
	}
	return value != ""
}

func canResolveSlug(task Task) bool {
	return task.Slug != "" || (task.ID != "" && strings.TrimSpace(task.Title) != "")
}

// ResolvedSlug returns the explicit task slug or the deterministic default.
func (t Task) ResolvedSlug() string {
	return ResolveSlug(t)
}

// ResolveSlug returns the explicit task slug or the deterministic default.
func ResolveSlug(task Task) string {
	if task.Slug != "" {
		return task.Slug
	}
	base := naming.Slugify(task.Title)
	if base == "" {
		base = "task"
	}
	suffix := "-" + task.ID
	maxBase := naming.MaxSlugLength - len(suffix)
	if maxBase < 1 {
		return task.ID
	}
	if len(base) > maxBase {
		base = strings.Trim(base[:maxBase], "-")
		if base == "" {
			base = "task"
		}
	}
	return base + suffix
}

// ResolvedBranch returns the explicit task branch or the deterministic default.
func (t Task) ResolvedBranch() string {
	return ResolveBranch(t)
}

// ResolveBranch returns the explicit task branch or the deterministic default.
func ResolveBranch(task Task) string {
	return naming.BranchName(task.Branch, naming.DefaultBranchPrefix, ResolveSlug(task))
}

func blockedByCycle(tasks []Task, taskIDs map[string]bool) []string {
	graph := make(map[string][]string, len(tasks))
	for _, task := range tasks {
		if task.ID == "" {
			continue
		}
		for _, dep := range task.BlockedBy {
			if taskIDs[dep] {
				graph[task.ID] = append(graph[task.ID], dep)
			}
		}
	}

	state := map[string]int{}
	stackIndex := map[string]int{}
	var stack []string
	var visit func(string) []string
	visit = func(id string) []string {
		switch state[id] {
		case 1:
			cycle := append([]string{}, stack[stackIndex[id]:]...)
			return append(cycle, id)
		case 2:
			return nil
		}
		state[id] = 1
		stackIndex[id] = len(stack)
		stack = append(stack, id)
		for _, dep := range graph[id] {
			if cycle := visit(dep); len(cycle) > 0 {
				return cycle
			}
		}
		stack = stack[:len(stack)-1]
		delete(stackIndex, id)
		state[id] = 2
		return nil
	}

	for _, task := range tasks {
		if task.ID == "" {
			continue
		}
		if cycle := visit(task.ID); len(cycle) > 0 {
			return cycle
		}
	}
	return nil
}
