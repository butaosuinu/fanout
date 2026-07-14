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
	"bundle_sha256": "e0b082ea1630370a8a6ba7e08afdbdbada22a1831a34f6fa7d531cda988f25c9",
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
		"bundle_sha256":                   "e0b082ea1630370a8a6ba7e08afdbdbada22a1831a34f6fa7d531cda988f25c9",
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

func TestBundleSHA256(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bundle := filepath.Join(dir, "review-bundle.md")
	if err := os.WriteFile(bundle, []byte("review bundle\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := BundleSHA256(bundle)
	if err != nil {
		t.Fatalf("BundleSHA256() error = %v", err)
	}
	const want = "e0b082ea1630370a8a6ba7e08afdbdbada22a1831a34f6fa7d531cda988f25c9"
	if got != want {
		t.Fatalf("BundleSHA256() = %q, want %q", got, want)
	}
}

func TestBundleSHA256RejectsInvalidFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	regular := filepath.Join(dir, "regular.md")
	if err := os.WriteFile(regular, []byte("bundle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty.md")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "bundle-link.md")
	if err := os.Symlink(regular, symlink); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		filepath.Join(dir, "missing.md"),
		dir,
		empty,
		symlink,
	} {
		if _, err := BundleSHA256(path); err == nil {
			t.Errorf("BundleSHA256(%q) error = nil, want rejection", path)
		}
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
