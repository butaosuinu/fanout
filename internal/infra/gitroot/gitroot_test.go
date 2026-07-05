package gitroot

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return dir
}

// mustEvalSymlinks canonicalizes a path (macOS t.TempDir lives under the
// /tmp -> /private/tmp symlink; git prints the resolved path).
func mustEvalSymlinks(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// TestToplevel pins the two shared user-visible error strings and the
// dir/cwd resolution behavior.
func TestToplevel(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)
	subDir := filepath.Join(repo, "sub")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	nonRepo := t.TempDir()

	tests := []struct {
		name    string
		dir     string
		chdir   string // non-empty: t.Chdir here first
		want    string // non-empty: expected root
		wantErr string // non-empty: expected exact error string
	}{
		{
			name: "resolves the toplevel of dir",
			dir:  repo,
			want: repo,
		},
		{
			name: "resolves the toplevel from a subdirectory",
			dir:  subDir,
			want: repo,
		},
		{
			name:  "empty dir resolves from the current working directory",
			dir:   "",
			chdir: repo,
			want:  repo,
		},
		{
			name:  "whitespace-only dir also resolves from cwd",
			dir:   "   ",
			chdir: repo,
			want:  repo,
		},
		{
			name:    "outside a work tree reports the fixed error",
			dir:     nonRepo,
			wantErr: "current directory is not inside a git work tree",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.chdir != "" {
				t.Chdir(tt.chdir)
			}
			got, err := Toplevel(tt.dir)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("Toplevel(%q) error = %v, want %q", tt.dir, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Toplevel(%q) error = %v", tt.dir, err)
			}
			if mustEvalSymlinks(t, got) != mustEvalSymlinks(t, tt.want) {
				t.Fatalf("Toplevel(%q) = %q, want %q", tt.dir, got, tt.want)
			}
		})
	}
}

func TestIsWorkTree(t *testing.T) {
	requireGit(t)
	repo := initRepo(t)

	tests := []struct {
		name string
		dir  string
		want bool
	}{
		{name: "inside a work tree", dir: repo, want: true},
		{name: "outside any work tree", dir: t.TempDir(), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsWorkTree(tt.dir); got != tt.want {
				t.Fatalf("IsWorkTree(%q) = %v, want %v", tt.dir, got, tt.want)
			}
		})
	}
}
