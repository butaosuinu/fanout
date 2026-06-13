package planspec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/naming"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Spec)
		wantErr string
	}{
		{
			name: "valid defaulted spec",
		},
		{
			name: "valid explicit slug and branch",
			mutate: func(spec *Spec) {
				spec.Tasks[0].Slug = "base-types"
				spec.Tasks[0].Branch = "feat/base-types"
			},
		},
		{
			name: "valid defaulted non ASCII task title",
			mutate: func(spec *Spec) {
				spec.Tasks[0].Title = "認証"
			},
		},
		{
			name: "version mismatch",
			mutate: func(spec *Spec) {
				spec.Version = 2
			},
			wantErr: "version must be 1, got 2",
		},
		{
			name: "missing plan slug",
			mutate: func(spec *Spec) {
				spec.Plan.Slug = ""
			},
			wantErr: "plan.slug is required",
		},
		{
			name: "invalid plan slug",
			mutate: func(spec *Spec) {
				spec.Plan.Slug = "Auth Refactor"
			},
			wantErr: "plan.slug must be lowercase kebab-case",
		},
		{
			name: "plan slug too long",
			mutate: func(spec *Spec) {
				spec.Plan.Slug = strings.Repeat("a", naming.MaxSlugLength+1)
			},
			wantErr: "plan.slug length must be <= 80, got 81",
		},
		{
			name: "missing plan title",
			mutate: func(spec *Spec) {
				spec.Plan.Title = " "
			},
			wantErr: "plan.title is required",
		},
		{
			name: "empty task list",
			mutate: func(spec *Spec) {
				spec.Tasks = nil
			},
			wantErr: "tasks must contain at least one task",
		},
		{
			name: "missing task id",
			mutate: func(spec *Spec) {
				spec.Tasks[0].ID = ""
				spec.Tasks[1].BlockedBy = nil
			},
			wantErr: "tasks[0].id is required",
		},
		{
			name: "invalid task id",
			mutate: func(spec *Spec) {
				spec.Tasks[0].ID = "Base Types"
				spec.Tasks[1].BlockedBy = nil
			},
			wantErr: "tasks[0].id must be lowercase kebab-case",
		},
		{
			name: "task id too long",
			mutate: func(spec *Spec) {
				spec.Tasks[0].ID = strings.Repeat("a", naming.MaxSlugLength+1)
				spec.Tasks[1].BlockedBy = nil
			},
			wantErr: "tasks[0].id length must be <= 80, got 81",
		},
		{
			name: "missing task title",
			mutate: func(spec *Spec) {
				spec.Tasks[0].Title = "\t"
			},
			wantErr: "tasks[0].title is required",
		},
		{
			name: "missing task briefing",
			mutate: func(spec *Spec) {
				spec.Tasks[0].Briefing = "\n"
			},
			wantErr: "tasks[0].briefing is required",
		},
		{
			name: "invalid resolved task slug",
			mutate: func(spec *Spec) {
				spec.Tasks[0].Slug = "Base Types"
			},
			wantErr: "tasks[0].slug must be lowercase kebab-case",
		},
		{
			name: "resolved task slug too long",
			mutate: func(spec *Spec) {
				spec.Tasks[0].Slug = strings.Repeat("a", naming.MaxSlugLength+1)
			},
			wantErr: "tasks[0].slug length must be <= 80, got 81",
		},
		{
			name: "duplicate task id",
			mutate: func(spec *Spec) {
				spec.Tasks[1].ID = "base-types"
				spec.Tasks[1].BlockedBy = nil
			},
			wantErr: `tasks[1].id "base-types" duplicates tasks[0].id`,
		},
		{
			name: "duplicate resolved task slug",
			mutate: func(spec *Spec) {
				spec.Tasks[0].Slug = "shared-task"
				spec.Tasks[1].Slug = "shared-task"
			},
			wantErr: `tasks[1].slug "shared-task" duplicates tasks[0].slug`,
		},
		{
			name: "duplicate resolved task branch",
			mutate: func(spec *Spec) {
				spec.Tasks[0].Branch = "feat/shared"
				spec.Tasks[1].Branch = "feat/shared"
			},
			wantErr: `tasks[1].branch "feat/shared" duplicates tasks[0].branch`,
		},
		{
			name: "branch contains whitespace",
			mutate: func(spec *Spec) {
				spec.Tasks[0].Branch = "feat/base types"
			},
			wantErr: `tasks[0].branch must not contain whitespace`,
		},
		{
			name: "blocked_by references unknown task",
			mutate: func(spec *Spec) {
				spec.Tasks[1].BlockedBy = []string{"missing-task"}
			},
			wantErr: `tasks[1].blocked_by[0] references unknown task "missing-task"`,
		},
		{
			name: "blocked_by cycle",
			mutate: func(spec *Spec) {
				spec.Tasks[0].BlockedBy = []string{"api-client"}
			},
			wantErr: "blocked_by cycle detected: base-types -> api-client -> base-types",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := validSpec()
			if tc.mutate != nil {
				tc.mutate(&spec)
			}

			err := Validate(spec)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() error = nil, want %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestTaskResolvedDefaults(t *testing.T) {
	tests := []struct {
		name       string
		task       Task
		wantSlug   string
		wantBranch string
	}{
		{
			name:       "derives slug and branch",
			task:       Task{ID: "api-client", Title: "Extract auth API client"},
			wantSlug:   "extract-auth-api-client-api-client",
			wantBranch: "fanout/extract-auth-api-client-api-client",
		},
		{
			name:       "falls back when title slug is empty",
			task:       Task{ID: "api-client", Title: "認証 API"},
			wantSlug:   "api-api-client",
			wantBranch: "fanout/api-api-client",
		},
		{
			name:       "falls back when title has no slug characters",
			task:       Task{ID: "api-client", Title: "認証"},
			wantSlug:   "task-api-client",
			wantBranch: "fanout/task-api-client",
		},
		{
			name:       "clamps derived slug length",
			task:       Task{ID: "api-client", Title: strings.Repeat("a", 200)},
			wantSlug:   strings.Repeat("a", naming.MaxSlugLength-len("-api-client")) + "-api-client",
			wantBranch: "fanout/" + strings.Repeat("a", naming.MaxSlugLength-len("-api-client")) + "-api-client",
		},
		{
			name:       "honors explicit slug and branch",
			task:       Task{ID: "api-client", Title: "Extract auth API client", Slug: "extract-auth-api", Branch: "feat/auth-api-client"},
			wantSlug:   "extract-auth-api",
			wantBranch: "feat/auth-api-client",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.task.ResolvedSlug(); got != tc.wantSlug {
				t.Fatalf("ResolvedSlug() = %q, want %q", got, tc.wantSlug)
			}
			if got := tc.task.ResolvedBranch(); got != tc.wantBranch {
				t.Fatalf("ResolvedBranch() = %q, want %q", got, tc.wantBranch)
			}
			if got := ResolveSlug(tc.task); got != tc.wantSlug {
				t.Fatalf("ResolveSlug() = %q, want %q", got, tc.wantSlug)
			}
			if got := ResolveBranch(tc.task); got != tc.wantBranch {
				t.Fatalf("ResolveBranch() = %q, want %q", got, tc.wantBranch)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name: "valid spec",
			body: `{
				"version": 1,
				"plan": {"slug": "auth-refactor", "title": "Auth refactor"},
				"tasks": [{"id": "api-client", "title": "Extract API client", "briefing": "## Goal\nExtract it"}]
			}`,
		},
		{
			name:    "invalid json",
			body:    `{"version":`,
			wantErr: "parse plan spec",
		},
		{
			name: "invalid spec",
			body: `{
				"version": 2,
				"plan": {"slug": "auth-refactor", "title": "Auth refactor"},
				"tasks": [{"id": "api-client", "title": "Extract API client", "briefing": "## Goal\nExtract it"}]
			}`,
			wantErr: "validate plan spec",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "spec.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatalf("write spec: %v", err)
			}
			spec, err := Load(path)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Load() error = %v, want nil", err)
				}
				if spec.Version != Version {
					t.Fatalf("Load() version = %d, want %d", spec.Version, Version)
				}
				return
			}
			if err == nil {
				t.Fatalf("Load() error = nil, want %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Load() error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func validSpec() Spec {
	return Spec{
		Version: Version,
		Plan: Plan{
			Slug:  "auth-refactor",
			Title: "Auth refactor",
		},
		Tasks: []Task{
			{
				ID:       "base-types",
				Title:    "Define base types",
				Briefing: "## Goal\nDefine the shared types",
			},
			{
				ID:        "api-client",
				Title:     "Extract auth API client",
				Briefing:  "## Goal\nExtract the API client",
				BlockedBy: []string{"base-types"},
				Wave:      "2",
			},
		},
	}
}
