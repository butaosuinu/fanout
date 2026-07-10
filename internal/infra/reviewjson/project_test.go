package reviewjson

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectWritesCompatibleCache(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "review.json")
	cache := filepath.Join(dir, "cache")
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	data := `{
  "backend": "bounded-isolated-reviewer",
  "review_type": "broad",
  "reviewer_agent": "\ud83d\ude80",
  "same_agent_review": false,
  "finding_count": 1,
  "truncated": false,
  "new_regressions": null,
  "findings": [{
    "severity": " major ",
    "file": "a.go",
    "line": 7,
    "title": " bad\t title ",
    "description": "desc\nline",
    "recommendation": " fix "
  }]
}`
	if err := os.WriteFile(input, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Project(input, cache); err != nil {
		t.Fatalf("Project() error = %v", err)
	}

	want := map[string]string{
		"backend":                         "bounded-isolated-reviewer",
		"review_type":                     "broad",
		"reviewer_agent":                  "🚀",
		"same_agent_review":               "false",
		"finding_count":                   "1",
		"truncated":                       "false",
		"new_regressions":                 "null",
		"findings_count":                  "1",
		"findings_missing_required_count": "0",
		"findings.tsv":                    "1\tmajor\ta.go\t7\tbad title\ta21125a4556bf5109b4601123a4af49fae067786\tdesc line\tfix\n",
		"valid":                           "",
	}
	for name, expected := range want {
		got, err := os.ReadFile(filepath.Join(cache, name))
		if err != nil {
			t.Errorf("ReadFile(%s) error = %v", name, err)
			continue
		}
		if string(got) != expected {
			t.Errorf("cache %s = %q, want %q", name, got, expected)
		}
	}
	if _, err := os.Stat(filepath.Join(cache, "reviewer_provenance")); !os.IsNotExist(err) {
		t.Errorf("missing scalar cache file exists or Stat failed: %v", err)
	}
}

func TestProjectRejectsInvalidInputWithoutValidMarker(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{name: "invalid UTF-8", data: append([]byte(`{"backend":"`), 0xff, '"', '}'), want: "not valid UTF-8"},
		{name: "lone high surrogate", data: []byte(`{"description":"\ud800"}`), want: "unpaired UTF-16 surrogate"},
		{name: "lone low surrogate", data: []byte(`{"description":"\udc00"}`), want: "unpaired UTF-16 surrogate"},
		{name: "trailing JSON", data: []byte(`{} {}`), want: "parse reviewer JSON"},
		{name: "array", data: []byte(`[]`), want: "parse reviewer JSON"},
		{name: "null", data: []byte(`null`), want: "must be an object"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			input := filepath.Join(dir, "review.json")
			cache := filepath.Join(dir, "cache")
			if err := os.Mkdir(cache, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cache, "valid"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(input, tc.data, 0o600); err != nil {
				t.Fatal(err)
			}

			err := Project(input, cache)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Project() error = %v, want containing %q", err, tc.want)
			}
			if _, statErr := os.Stat(filepath.Join(cache, "valid")); !os.IsNotExist(statErr) {
				t.Errorf("valid marker survived rejected input: %v", statErr)
			}
		})
	}
}

func TestProjectDoesNotMarkPartialOutputValid(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "review.json")
	cache := filepath.Join(dir, "cache")
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "valid"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(cache, "backend"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, []byte(`{"backend":"bounded-isolated-reviewer"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	err := Project(input, cache)
	if err == nil || !strings.Contains(err.Error(), "write cache file backend") {
		t.Fatalf("Project() error = %v, want backend write failure", err)
	}
	if _, statErr := os.Stat(filepath.Join(cache, "valid")); !os.IsNotExist(statErr) {
		t.Errorf("valid marker survived partial projection: %v", statErr)
	}
}
