package atomicfs

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type payload struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

// TestWriteJSON pins the exact byte layout (two-space MarshalIndent plus a
// trailing newline), the final file mode, and directory auto-creation.
func TestWriteJSON(t *testing.T) {
	tests := []struct {
		name     string
		relPath  string
		v        any
		perm     os.FileMode
		want     string
		wantPerm os.FileMode
		durable  bool
	}{
		{
			name:     "struct is indented with trailing newline",
			relPath:  "out.json",
			v:        payload{Name: "a", N: 7},
			perm:     0o644,
			want:     "{\n  \"name\": \"a\",\n  \"n\": 7\n}\n",
			wantPerm: 0o644,
		},
		{
			name:     "0600 perm is applied exactly",
			relPath:  "secret.json",
			v:        map[string]string{"token": "t"},
			perm:     0o600,
			want:     "{\n  \"token\": \"t\"\n}\n",
			wantPerm: 0o600,
		},
		{
			name:     "missing parent directories are created",
			relPath:  filepath.Join("a", "b", "out.json"),
			v:        payload{},
			perm:     0o644,
			want:     "{\n  \"name\": \"\",\n  \"n\": 0\n}\n",
			wantPerm: 0o644,
		},
		{
			name:     "durable replacement keeps exact bytes and mode",
			relPath:  "durable.json",
			v:        payload{Name: "durable", N: 1},
			perm:     0o600,
			want:     "{\n  \"name\": \"durable\",\n  \"n\": 1\n}\n",
			wantPerm: 0o600,
			durable:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tt.relPath)
			write := WriteJSON
			if tt.durable {
				write = WriteJSONDurable
			}
			if err := write(path, tt.v, tt.perm); err != nil {
				t.Fatalf("WriteJSON(%q) = %v, want nil", path, err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile(%q) = %v, want nil", path, err)
			}
			if got := string(data); got != tt.want {
				t.Errorf("WriteJSON wrote %q, want %q", got, tt.want)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("Stat(%q) = %v, want nil", path, err)
			}
			if got := info.Mode().Perm(); got != tt.wantPerm {
				t.Errorf("WriteJSON perm = %v, want %v", got, tt.wantPerm)
			}
		})
	}
}

// TestWriteJSONUnmarshalableValue guarantees the marshal error surfaces and no
// file is left behind.
func TestWriteJSONUnmarshalableValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.json")
	if err := WriteJSON(path, func() {}, 0o644); err == nil {
		t.Fatal("WriteJSON(func) = nil, want marshal error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Stat(%q) after failed WriteJSON = %v, want IsNotExist", path, err)
	}
}

func TestWriteFileExclusivePublishesOnceWithoutReplacing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := WriteFileExclusive(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileExclusive(path, []byte("second"), 0o644); !os.IsExist(err) {
		t.Fatalf("second WriteFileExclusive() error = %v, want IsExist", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first" {
		t.Fatalf("exclusive file bytes = %q, want first", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("exclusive file mode = %o, want 600", info.Mode().Perm())
	}
}

func TestCompareAndSwapFilePublishesOnlyExpectedPreimage(t *testing.T) {
	t.Run("match", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "plan.json")
		if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := CompareAndSwapFile(path, []byte("old"), []byte("new"), 0o600); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "new" {
			t.Fatalf("CAS bytes = %q, want new", data)
		}
	})

	t.Run("mismatch rolls back", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "plan.json")
		if err := os.WriteFile(path, []byte("concurrent"), 0o600); err != nil {
			t.Fatal(err)
		}
		before, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		err = CompareAndSwapFile(path, []byte("old"), []byte("new"), 0o600)
		if err == nil {
			t.Fatal("CompareAndSwapFile() accepted a mismatched preimage")
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(data) != "concurrent" {
			t.Fatalf("rolled-back bytes = %q, want concurrent", data)
		}
		after, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if !os.SameFile(before, after) {
			t.Fatal("mismatched preimage was transiently replaced")
		}
		matches, globErr := filepath.Glob(filepath.Join(dir, ".fanout-cas-*.tmp"))
		if globErr != nil {
			t.Fatal(globErr)
		}
		if len(matches) != 0 {
			t.Fatalf("successful rollback left recovery files: %v", matches)
		}
	})
}

func TestCompareAndSwapFileSerializesDestinationWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var writers sync.WaitGroup
	for _, replacement := range [][]byte{[]byte("first"), []byte("second")} {
		writers.Add(1)
		go func(data []byte) {
			defer writers.Done()
			<-start
			results <- CompareAndSwapFile(path, []byte("old"), data, 0o600)
		}(replacement)
	}
	close(start)
	writers.Wait()
	close(results)

	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent writers = %d, want 1", successes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first" && string(data) != "second" {
		t.Fatalf("serialized destination bytes = %q", data)
	}
}

func TestRollbackCompareAndSwapPreservesConcurrentDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plan.json")
	tmpPath := filepath.Join(dir, "displaced.tmp")
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	replacementInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(tmpPath, []byte("displaced"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	displacedInfo, err := os.Stat(tmpPath)
	if err != nil {
		t.Fatal(err)
	}
	concurrentPath := filepath.Join(dir, "concurrent.tmp")
	if writeErr := os.WriteFile(concurrentPath, []byte("newest"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if renameErr := os.Rename(concurrentPath, path); renameErr != nil {
		t.Fatal(renameErr)
	}

	cleanup, rollbackErr := rollbackCompareAndSwap(
		tmpPath,
		path,
		replacementInfo,
		displacedInfo,
		[]byte("replacement"),
		0o600,
	)
	if rollbackErr == nil || cleanup {
		t.Fatalf("rollback result = cleanup %t, error %v; want retained recovery file", cleanup, rollbackErr)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != "newest" {
		t.Fatalf("concurrent destination bytes = %q, want newest", current)
	}
	displaced, err := os.ReadFile(tmpPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(displaced) != "displaced" {
		t.Fatalf("displaced recovery bytes = %q, want displaced", displaced)
	}
}

func TestWriteJSONDurableRequiresExistingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "out.json")
	if err := WriteJSONDurable(path, payload{}, 0o600); err == nil {
		t.Fatal("WriteJSONDurable() created an unverified destination directory")
	}
	if _, err := os.Lstat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("durable write created destination directory: %v", err)
	}
}

// TestReadJSON pins the (found, err) contract: missing file (false, nil),
// read failure (false, err), decode failure (true, err), success (true, nil).
func TestReadJSON(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T, dir string) string // returns path
		wantFound bool
		wantErr   bool
		want      payload
	}{
		{
			name: "missing file is found=false with nil error",
			setup: func(t *testing.T, dir string) string {
				t.Helper()
				return filepath.Join(dir, "absent.json")
			},
			wantFound: false,
			wantErr:   false,
		},
		{
			name: "valid file decodes into v",
			setup: func(t *testing.T, dir string) string {
				t.Helper()
				path := filepath.Join(dir, "ok.json")
				if err := os.WriteFile(path, []byte(`{"name":"a","n":7}`), 0o644); err != nil {
					t.Fatal(err)
				}
				return path
			},
			wantFound: true,
			wantErr:   false,
			want:      payload{Name: "a", N: 7},
		},
		{
			name: "malformed JSON is found=true with error",
			setup: func(t *testing.T, dir string) string {
				t.Helper()
				path := filepath.Join(dir, "bad.json")
				if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
					t.Fatal(err)
				}
				return path
			},
			wantFound: true,
			wantErr:   true,
		},
		{
			// A directory exists but cannot be read as a file: the read stage
			// fails, so found must stay false (distinguishes wrap messages).
			name: "unreadable path is found=false with error",
			setup: func(t *testing.T, dir string) string {
				t.Helper()
				path := filepath.Join(dir, "isadir")
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
				return path
			},
			wantFound: false,
			wantErr:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.setup(t, t.TempDir())
			var got payload
			found, err := ReadJSON(path, &got)
			if found != tt.wantFound {
				t.Errorf("ReadJSON(%q) found = %t, want %t", path, found, tt.wantFound)
			}
			if (err != nil) != tt.wantErr {
				t.Errorf("ReadJSON(%q) err = %v, wantErr %t", path, err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("ReadJSON(%q) decoded %+v, want %+v", path, got, tt.want)
			}
		})
	}
}

func TestReadJSONStrictRejectsDuplicateFieldsAtEveryObjectLevel(t *testing.T) {
	type nestedPayload struct {
		Value int `json:"value"`
	}
	type strictPayload struct {
		Name   string          `json:"name"`
		Nested nestedPayload   `json:"nested"`
		Items  []nestedPayload `json:"items"`
	}

	tests := []struct {
		name string
		body string
	}{
		{
			name: "top level",
			body: `{"name":"first","name":"second","nested":{"value":1},"items":[]}`,
		},
		{
			name: "nested object",
			body: `{"name":"ok","nested":{"value":1,"value":2},"items":[]}`,
		},
		{
			name: "object inside array",
			body: `{"name":"ok","nested":{"value":1},"items":[{"value":1,"\u0076alue":2}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "duplicate.json")
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			var got strictPayload
			found, err := ReadJSONStrict(path, &got)
			if !found || err == nil {
				t.Fatalf("ReadJSONStrict() = found:%t err:%v, want duplicate-field error", found, err)
			}
		})
	}
}

func TestReadJSONStrictAcceptsUniqueNestedFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unique.json")
	if err := os.WriteFile(
		path,
		[]byte(`{"name":"ok","nested":{"value":1},"items":[{"value":2}]}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Name   string `json:"name"`
		Nested struct {
			Value int `json:"value"`
		} `json:"nested"`
		Items []struct {
			Value int `json:"value"`
		} `json:"items"`
	}
	found, err := ReadJSONStrict(path, &got)
	if !found || err != nil {
		t.Fatalf("ReadJSONStrict() = found:%t err:%v", found, err)
	}
	if got.Name != "ok" || got.Nested.Value != 1 || len(got.Items) != 1 || got.Items[0].Value != 2 {
		t.Fatalf("ReadJSONStrict() decoded %+v", got)
	}
}
